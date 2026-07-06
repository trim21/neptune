# Neptune 对等体重连逻辑分析

本文档对比 Neptune 和 libtorrent 的 peer 重连算法，分析当前问题根因。

---

## 1. 用户观察到的现象

> "慢速下载一段时间后，peer 断开了之后没有能重连，然后过段时间（可能是 announce）了，又开始重新满速下载"

**症状**：peer 断开 → 长时间无新连接 → tracker announce → 恢复满速。
**间隔**：通常 30 分钟（tracker 标准 announce interval）。

---

## 2. 当前 Neptune 重连架构

### 2.1 数据流

```
Peer 来源:
  Tracker announce ──→ OnPeers callback ──→ pendingPeers.Push()
  PEX 消息         ──→ pexAdd channel    ──→ pendingPeers.Push()
  Disconnect       ──→ peer.close()      ──→ pendingPeers.Push()

        │
        ▼
  pendingPeers (优先级堆，按 BEP40 排序)
        │
        ▼
  connectToPeers()   ← 10s ticker + pendingPeersSignal 触发
        │
        ├─ connHistory LRU 检查 backoff
        ├─ peers.Load 检查是否已连接
        ├─ sem.TryAcquire 检查全局连接数
        └─ global.Dial() → 成功 → NewOutgoingPeer
                          → 失败 → 记录 reason + backoff
```

### 2.2 Backoff 策略

| 断开原因 | Backoff | 触发条件 |
|---|---|---|
| `connReasonNone` | 0 | 首次尝试 |
| `connReasonTimeout` | 30s | dial timeout / read deadline |
| `connReasonRefused` | 10min | ECONNREFUSED |
| `connReasonEOF` | 30s | 对端主动关闭 |
| `connReasonError` | 1min | 其他网络错误 |
| `hadTrans == true` | 0（立即重试）| peer 曾传输过数据 |

### 2.3 重连触发时机

1. **定时器**：每 10s `reconnectTicker` 触发 → `connectToPeers()`
2. **信号**：`pendingPeersSignal`（新 peer 到达 / peer 断开）
3. **Tracker announce**：由 tracker 控制，通常 30min

---

## 3. Libtorrent 重连架构（对比基准）

### 3.1 架构差异

```
                    libtorrent                          Neptune
              ┌──────────────────┐              ┌──────────────────┐
Peer 存储      │  peer_list       │              │  pendingPeers    │
              │  持久化，全量保留  │              │  (堆，瞬时快照)   │
              │  + candidate_cache │              │  + connHistory   │
              │  (预计算候选)      │              │  (LRU 256, TTL)  │
              └──────────────────┘              └──────────────────┘
Peer 生命周期  │  永久保留          │              │  堆中 pop 后丢失  │
              │  断连不删除         │              │  重连失败后堆中   │
              │                    │              │  保留但可能 LRU   │
              │                    │              │  驱逐             │
              └──────────────────┘              └──────────────────┘
重连触发      │  on_tick (1s)      │              │  ticker (10s)    │
              │  + bandwidth       │              │  + signal        │
              │  + 每 torrent      │              │                  │
              │    逐个尝试连接     │              │                  │
              └──────────────────┘              └──────────────────┘
Peer 发现     │  Tracker + DHT     │              │  Tracker + PEX   │
              │  + LSD + PEX       │              │  (DHT 是 stub)   │
              └──────────────────┘              └──────────────────┘
Peer 轮换     │  optimistic        │              │  无               │
              │  disconnect        │              │                  │
              │  (peer_turnover)   │              │                  │
              └──────────────────┘              └──────────────────┘
```

### 3.2 Libtorrent 重连候选判定

```cpp
bool is_connect_candidate(torrent_peer const& p) const {
    if (p.connection          // 已有连接 → no
        || p.banned           // 被 ban → no
        || p.web_seed         // web seed → no
        || !p.connectable     // 无 listen port → no
        || (p.seed && m_finished)  // 已完成 + 对方是 seed → no
        || p.failcount >= m_max_failcount)  // 失败太多次 → no
        return false;
    return true;
}
```

关键：**failcount 默认可达 3 次**（`m_max_failcount = 3`）。即使失败 3 次，peer 仍保留在 `peer_list` 中，当 tracker 再次返回时会 `update_peer()` 重置 `failcount`。

### 3.3 Libtorrent 重连间隔

```cpp
// find_connect_candidates 中的间隔检查：
if (pe.last_connected
    && session_time - pe.last_connected <
    (pe.failcount + 1) * state->min_reconnect_time)
    continue;

// min_reconnect_time 默认 60s
// failcount=0 → 60s
// failcount=1 → 120s
// failcount=2 → 180s
```

同时在 `connection_closed` 中：
```cpp
if (c.failed()) {
    if (p->failcount < 31) ++p->failcount;  // failcount 增加
}

if (is_connect_candidate(*p))
    update_connect_candidates(1);  // 立刻重新加入候选集

if (!c.fast_reconnect())
    p->last_connected = session_time;  // 更新时间戳
else
    // fast_reconnect: 不更新时间戳 → 可立即重连
```

### 3.4 Libtorrent 的 Peer 轮换

```cpp
// session_impl::on_tick 中的轮换逻辑：
int peers_to_disconnect = min(max(
    t->num_peers() * peer_turnover / 100, 1),
    t->num_connect_candidates());
t->disconnect_peers(peers_to_disconnect, errors::optimistic_disconnect);
```

默认 `peer_turnover = 1/50`（2%），即定期断开 2% 的连接来轮换 peer，发现更快的连接。

---

## 4. 问题根因分析

### 4.1 问题 #1：Peer 池枯竭（核心问题）

**Neptune 的 `pendingPeers` 是一个瞬时堆**，不是持久化存储。

```
时间线:
  T=0     Tracker 返回 50 个 peer → pendingPeers 有 50 个
  T=10s   connectToPeers() → 连接了 10 个
  T=30s   慢速 peer 超时断开 → 第 1 个断开的放回 pendingPeers
  T=35s   另一个断开 → 放回 pendingPeers
  ...
  T=5min  10 个 peer 都断开 → pendingPeers 还是这些 peer(可能就10个)
          所有 peer 在经历 backoff（30s/1min/10min）
          connectToPeers() 每次 pop 出来 → canRetry=true (跳过) → push 回去
          没有新 peer 来源 → pendingPeers 里只有这些正在 backoff 的 peer
  T=30min Tracker announce → 50 个新 peer → pendingPeers 重新填充 → 满速恢复！
```

**Libtorrent 如何避免**：
- `peer_list` 永久保留所有已知 peer，不会因为"试完了"就丢失
- DHT announce 每 20 分钟独立提供新 peer
- LSD 每 5 分钟在局域网提供新 peer
- PEX 持续交换 peer 信息
- `optimistic_disconnect` 定期轮换连接，持续引入新 peer

### 4.2 问题 #2：`hadTrans` 被覆盖（Bug）

在 `connectToPeers()` 中：

```go
// d_conn.go:86 (修复前)
tasks.Submit(func() {
    ch := connHistory{lastTry: time.Now()}  // hadTrans 默认为 false！
    d.connectionHistory.Add(pp.addrPort, ch) // 覆盖了之前的 hadTrans=true
    ...
})
```

**问题流程**：
1. Peer 传输了数据 → `close()` 调用 `recordDisconnect()` → `hadTrans = true`
2. Peer 被重新放入 `pendingPeers`
3. `connectToPeers()` 尝试重连 → 创建新的 `connHistory{hadTrans: false}` → **覆盖**了 `hadTrans`
4. 即使重连失败（例如 TCP timeout），`canRetry()` 检查到 `hadTrans == false` → 应用 30s backoff
5. **应该的行为**：`hadTrans == true` → 立即重试，0 秒 backoff

**已修复**：现在 `connectToPeers()` 会从旧的 `connHistory` 中保留 `hadTrans` 标志。

### 4.3 问题 #3：无 DHT 导致 Peer 来源单一

| Peer 来源 | libtorrent | Neptune | 间隔 |
|---|---|---|---|
| HTTP Tracker | ✅ | ✅ | 30min（tracker 控制） |
| UDP Tracker | ✅ | ❌ | - |
| DHT | ✅ | ❌ (stub) | 20min |
| LSD | ✅ | ❌ | 5min |
| PEX | ✅ | ✅ | 实时 |

Neptune 只有 2 个 peer 来源：Tracker（30min）和 PEX（实时）。PEX 依赖于**已连接的 peer 相互交换**。但当所有 peer 都断开后，PEX 也无法获取新 peer。

这就导致了"断开后只能等 tracker announce"的现象。

### 4.4 问题 #4：Sem 满时的行为

```go
if !d.c.sem.TryAcquire(1) {
    retryLater = append(retryLater, pp)
    break  // ← 直接跳出循环，剩余 peers 留在堆里
}
```

当连接数达到上限时：
- 剩余 peer 被 push 回堆
- 但之前已修复：现在会发送 `pendingPeersSignal` 触发更快重试
- 但如果连接数持续满载，这些 peer 永远无法被尝试

**Libtorrent 的做法**：有 `peer_turnover` 机制，定期断开旧连接释放 slot。

---

## 5. 修复建议

### 5.1 短期（低风险）

1. ~~**修复 `hadTrans` 覆盖 bug**~~ ✅ 已完成
2. ~~**Sem 满时立即发 signal**~~ ✅ 已完成
3. **降低 `connBackoffRefused`**：10min → 2min。Refused 可能只是 peer 临时重启，10min 太长
4. **降低 `connBackoffTimeout`**：30s → 10s。超时可能只是临时网络波动
5. **增加 backoff 衰减**：连续失败时递增 backoff，而非固定值

### 5.2 中期（结构改进）

6. **实现持久化 Peer 列表**：

```go
type persistentPeer struct {
    addrPort      netip.AddrPort
    source        peerSourceFlags  // tracker, pex, dht, incoming
    failcount     uint8
    lastConnected int64
    connectable   bool
    seed          bool
}
```

- 类似 libtorrent 的 `peer_list` + `torrent_peer`
- Peer 断连后不删除，保留在列表中
- 用 `candidateCache` 预计算可连接的 peer
- tracker announce 返回已有 peer 时更新（而非重复添加）

7. **实现 DHT**：DHT stub → 完整实现，提供独立的 peer 来源

8. **Peer 轮换（Turnover）**：

```go
// 每 5 分钟随机断开 1 个慢速 peer，腾出 slot 连接新 peer
func (d *Download) peerTurnover() {
    // 找到最慢的 peer 断开
    // 触发 connectToPeers 连接候选
}
```

### 5.3 长期

9. **实现 LSD（Local Service Discovery）**：局域网 peer 发现
10. **实现 UDP Tracker**：更高效的 tracker 协议

---

## 6. 当前 vs libtorrent 关键指标对比

| 指标 | Neptune | libtorrent |
|---|---|---|
| Peer 持久化 | ❌ 瞬时堆 + LRU 256 | ✅ peer_list（永久） |
| Peer 候选缓存 | ❌ | ✅ candidate_cache |
| 重连间隔 | 固定 10s ticker | 1s tick + bandwidth 驱动 |
| Backoff 策略 | 固定时间（30s/1min/10min） | failcount × min_reconnect_time（60s/120s/180s） |
| hadTrans 保护 | 已修复 ✅ | fast_reconnect 标志 |
| Peer 轮换 | ❌ | ✅ optimistic_disconnect (2%/round) |
| 多源 peer 发现 | Tracker + PEX | Tracker + DHT + LSD + PEX |
| 连接失败上限 | 无限重试 | max_failcount = 3（后排除） |
| Peer 恢复 | 仅 Tracker 重新返回时 | 永久保留，announce 时 update |
