# Neptune 下载调度算法

本文档描述 Neptune 的下载调度算法，基于 [libtorrent](https://github.com/arvidn/libtorrent) 的 `piece_picker` + `request_a_block` 架构实现。

---

## 目录

1. [架构概览](#1-架构概览)
2. [Piece Picker — 全局 Block 管理器](#2-piece-picker--全局-block-管理器)
3. [Block 状态机](#3-block-状态机)
4. [Piece 优先级系统](#4-piece-优先级系统)
5. [Pick Pieces — Block 选择算法](#5-pick-pieces--block-选择算法)
6. [requestABlock — Per-Peer Block 分配](#6-requestablock--per-peer-block-分配)
7. [Pipeline 动态调整](#7-pipeline-动态调整)
8. [Endgame 模式](#8-endgame-模式)
9. [Block 发送与排队](#9-block-发送与排队)
10. [Peer 生命周期集成](#10-peer-生命周期集成)
11. [数据接收与写入](#11-数据接收与写入)
12. [完整数据流](#12-完整数据流)

---

## 1. 架构概览

下载调度由三层协作完成：

```
┌─────────────────────────────────────────────────────┐
│  backgroundReqScheduler (定时/信号触发)               │
│    遍历所有 peer，对每个 peer 调用 requestABlock()     │
└──────────────────────┬──────────────────────────────┘
                       │
         ┌─────────────▼─────────────┐
         │    requestABlock(peer)    │  ← 核心调度函数
         │  · 计算 pipeline 大小      │
         │  · 调用 picker.pickPieces │
         │  · 过滤 + 验证 block       │
         │  · 推入 peer.blockRequests │
         └─────────────┬─────────────┘
                       │
   ┌───────────────────┼───────────────────┐
   │                   │                   │
   ▼                   ▼                   ▼
┌──────────┐   ┌──────────────┐   ┌──────────────┐
│ Peer #1  │   │   Peer #2    │   │   Peer #N    │
│  排队 →  │   │   排队 →     │   │   排队 →     │
│  发送    │   │   发送       │   │   发送       │
└──────────┘   └──────────────┘   └──────────────┘
        │               │                  │
        └───────────────┴──────────────────┘
                        │
              ┌─────────▼─────────┐
              │  piecePicker      │  ← 全局 Block 状态跟踪
              │  · availability[] │
              │  · blockInfos[]   │
              │  · priorities[]   │
              └───────────────────┘
```

核心设计原则：
- **全局 Block 级跟踪**：每个 block 的状态、哪个 peer 在请求、多少个 peer 在排队，全部集中在一个 `piecePicker` 中
- **Rarest-First 优先**：越稀有的 piece 越优先下载，确保在 swarm 中保持稀有块
- **Partial Piece 优先**：已经开始下载的 piece 优先完成，减少内存碎片
- **动态 Pipeline**：根据 peer 的下载速率实时调整未完成请求数，而非固定值

---

## 2. Piece Picker — 全局 Block 管理器

`piecePicker`（位于 `internal/download/piece_picker.go`）是下载调度的核心数据结构。它集中管理所有 block 的状态。

### 2.1 数据结构

```go
type piecePicker struct {
    mu sync.Mutex

    numPieces      uint32        // 总 piece 数
    blocksPerPiece uint32        // 每个 piece 的 block 数（通常 = pieceLength / 16KB）
    blockSize      int64         // block 大小（固定 16KB）

    // ── 可用性 ──
    availability []uint16        // availability[i] = 拥有 piece i 的 peer 数

    // ── 优先级排序 ──
    pieces          []uint32     // 按优先级排序的 piece 索引（仅含未开始下载的 piece）
    piecePriorities []uint32     // 每个 piece 的优先级分数
    dirty           bool         // 优先级数组是否需要重建

    // ── 正在下载的 piece ──
    downloadingPieces []downloadingPiece  // 按 index 排序，支持 O(log n) 查找

    // ── Block 状态 ──
    blockInfos []blockInfo       // 扁平数组，以 piece 为单位分组
                                 // blockInfos[pieceInfoIdx + i] 对应 piece 的第 i 个 block

    // ── 统计 ──
    downloadQueueSize int        // 所有 peer 的总未完成请求数
    numWantLeft       int        // 未分配但想要的 piece 数
}
```

### 2.2 可用性（Availability）

每个 piece 维护一个引用计数，表示当前连接的 peer 中有多少个拥有该 piece：

```
availability[pieceIndex] = count(peers that have this piece in their bitfield)
```

**何时增加**（`incRefcount`）：
- Peer 发送 `Bitfield` 消息 → 对该 peer 拥有的所有 piece 各 +1
- Peer 发送 `Have` 消息 → 对该 piece +1
- Peer 发送 `HaveAll` 消息 → 对所有 piece +1

**何时减少**（`decRefcount`）：
- Peer 发送 `DontHave` 扩展消息 → 对该 piece -1
- Peer 发送 `HaveAll`（需要先减少旧 bitfield 的计数，再增加新计数）
- Peer 断开连接 → 对该 peer 拥有的所有 piece 各 -1

---

## 3. Block 状态机

每个 block 处于以下四种状态之一：

```
                    markAsRequesting()
  stateNone ────────────────────────────► stateRequested
     ▲                                        │
     │   abortDownload()                      │  markAsWriting()
     │   resetPiece()                         │  (数据到达)
     │   (hash 校验失败)                        │
     │                                        ▼
     │                                  stateWriting
     │                                        │
     │                                        │  markAsFinished()
     │                                        │  (写入磁盘完成)
     │                                        ▼
     └──────────────────────────────── stateFinished
             weHave()                   
             (hash 校验通过，整个 piece 标记完成)
```

每个 block 额外跟踪：
- `peer *Peer`：当前请求该 block 的 peer（`stateRequested` 时有效）
- `numPeers uint16`：有多少个 peer 将该 block 加入了自己的队列（用于 endgame 去重）

---

## 4. Piece 优先级系统

### 4.1 优先级公式

```
priority = (availability + 1) × priorityFactor
         = (availability + 1) × 3
```

| availability | priority | 含义 |
|---|---|---|
| 0 | 3 | 最稀有 —— 只有一个 peer 有 |
| 1 | 6 | 稀有 |
| 2 | 9 | 一般 |
| 10 | 33 | 常见 |
| N | (N+1)×3 | 越常见优先级越低 |

**优先级越高 → 越优先被选中下载**。

稀有度优先（rarest-first）确保 swarm 中的稀有 piece 被优先传播，防止唯一的拥有者离开后 piece 不可获取。

### 4.2 优先级重建

当 `dirty == true` 时，`rebuildPriorities()` 被调用：

1. **过滤**：排除已经在下载中的 piece（至少一个 block 处于 `stateRequested` 或 `stateWriting`）
2. **排序**：剩余 piece 按 `piecePriorities` 降序排列，同优先级按 index 升序
3. **更新**：`numWantLeft = len(available)` — 记录还有多少 piece 可分配

标记 `dirty = true` 的时机：
- `incRefcount()` / `decRefcount()` 使 availability 跨越 0↔1 边界
- `addDownloadingPiece()` 或 `removeDownloadingPiece()`
- `weHave()` — piece 已完成下载
- `resetPiece()` — hash 校验失败，重置

---

## 5. Pick Pieces — Block 选择算法

`pickPieces()` 是 Block 选择的入口。它接收一个 peer 的信息，返回两组 block：

- **freeBlocks**：未被任何 peer 请求的 block（`stateNone`）
- **busyBlocks**：已被其他 peer 请求的 block（`stateRequested`），仅在 endgame 使用

### 5.1 输入参数

| 参数 | 类型 | 说明 |
|---|---|---|
| `bitfield` | `*bm.Bitmap` | Peer 拥有的 piece 集合 |
| `choked` | `bool` | Peer 是否 choked 了我们 |
| `allowedFast` | `*bm.Bitmap` | Allowed-Fast piece 集合（仅 choked 时有效） |
| `numBlocks` | `int` | 期望挑选的 block 数量 |
| `preferContiguous` | `int` | >0 时优先整 piece 下载（当前未启用） |
| `suggestedPieces` | `[]uint32` | Peer 建议的 piece 列表 |
| `info` | `meta.Info` | Torrent 元数据 |

### 5.2 三阶段选择

```
pickPieces(bitfield, choked, allowFast, numBlocks, ...)

Phase 1: Partial Pieces (正在下载中的 piece)
  │
  ├─ 遍历 downloadingPieces[]
  ├─ 过滤：peer 有这些 piece、未被 choked（或 allowed fast）
  ├─ 对每个 piece 统计 stateNone 的 block 数
  ├─ 按 piece 优先级（= availability 稀有度）排序
  ├─ 提取 stateNone 的 block → freeBlocks
  └─ 收集 stateRequested 的 block → busyBlocks

Phase 2: Suggested Pieces (peer 建议的 piece)
  │
  ├─ 遍历 suggestedPieces[]
  ├─ 对每个 piece 调用 pickBlocksFromPiece()
  └─ 提取所有 stateNone block → freeBlocks

Phase 3: Open Pieces (尚未开始下载的 piece)
  │
  ├─ 遍历 openPieces（按 priority 降序）
  ├─ 跳过已在 freeBlocks 中的 piece
  ├─ 对每个 piece 调用 pickBlocksFromPiece()
  └─ 提取所有 stateNone 的 block → freeBlocks
```

### 5.3 pickBlocksFromPiece 细节

```
pickBlocksFromPiece(pieceIndex, info, &numBlocks, result):
  for i in 0 .. blocksInPiece:
    if blockInfos[idx+i].state == stateNone:
      result.freeBlocks.append(pieceBlock{pieceIndex, i})
      numBlocks--
      if numBlocks <= 0: return
    else if blockInfos[idx+i].state == stateRequested:
      result.busyBlocks.append(pieceBlock{pieceIndex, i})
```

---

## 6. requestABlock — Per-Peer Block 分配

`requestABlock()`（位于 `internal/download/download.go`）是每个 peer 的调度入口。它决定"为这个 peer 分配哪些 block"。

### 6.1 算法流程

```
requestABlock(peer):

  1. 前置检查
     ├─ 我们已经是 seed → return
     ├─ peer 已关闭或正在断开 → return
     ├─ 不在 Downloading 状态 → return
     └─ peer.noDownload() → return

  2. 计算 Pipeline 大小
     desiredQueueSize = peer.updateDesiredQueueSize()
     capacity = desiredQueueSize - peer.myRequests.Size() - len(peer.requestQueue)
     if capacity <= 0: return

  3. Endgame 判断
     remaining = SelectedSize - completed
     if remaining <= 100 MiB: endGameMode = true

  4. 调用 Piece Picker
     result = picker.pickPieces(peer.Bitmap, choked, allowFast, capacity, ...)

  5. 处理 freeBlocks（逐个）
     for each fb in result.freeBlocks:
       if piece 已完成 or block 已写入磁盘 → skip
       if block 已在 peer 队列中 → skip
       picker.markAsRequesting(fb, peer)
       picker.addDownloadingPiece(fb.pieceIndex)
       peer.blockRequests <- fb

  6. 判断是否进入 Endgame
     if capacity 已用完:
       peer.setEndgame(false)  ← 该 peer 不在 endgame
       return
     if 未 choked:
       peer.setEndgame(true)   ← 该 peer 进入 endgame
       endGameMode = true

  7. 处理 busyBlocks（仅 endgame + 无 outstanding request）
     if !endGameMode or peer.myRequests.Size() + len(peer.requestQueue) > 0:
       return  ← 避免所有 peer 抢同一 block
     for each bb in result.busyBlocks:
       ...同样的过滤...
       picker.markAsRequesting(bb, peer)
       peer.blockRequests <- bb
```

### 6.2 关键设计决策

**为什么每个 peer 有独立的 `requestQueue`？**

在 libtorrent 中，block 被 picker 选中后先进入 `m_request_queue`，然后在 `send_block_requests_impl()` 中批量发送。这样做的好处是：
- 调度（picker）和发送（I/O）解耦
- 多个 block 可以合并为更大的连续请求（`prefer_contiguous_blocks`）
- 当 peer 被 choke 时可以回退

**为什么 freeBlock 用尽后才进入 endgame？**

Endgame 模式允许重复请求已被其他 peer 请求的 block（busy block）。如果在 free block 充足时就进入 endgame，会导致大量重复请求，浪费带宽。只有当 free block 用尽时，说明该 peer 能贡献的 block 已经全部被分配，此时允许请求 busy block 来提高最后阶段的完成速度。

---

## 7. Pipeline 动态调整

`updateDesiredQueueSize()` 位于 `p.go`，每个 peer 独立计算。

### 7.1 公式

```
desiredQueueSize = queueTime × downloadRate / blockSize

其中:
  queueTime  = 3 秒         (想要维护的 pipeline 时间)
  blockSize  = 16 KB        (每个 block 的大小)
  downloadRate = 该 peer 当前的下载速率 (bytes/sec)
```

### 7.2 Clamping

```
desiredQueueSize = clamp(
    rate_based_value,
    min = minRequestQueue = 2,      // 至少保持 2 个未完成请求
    max = min(
        maxRequestQueue = 250,      // 全局上限
        peer.QueueLimit              // peer 在扩展握手中通告的队列限制
    )
)
```

### 7.3 特殊情况

| 情况 | desiredQueueSize | 说明 |
|---|---|---|
| `snubbed == true` | 1 | 被 snub 的 peer 降到最小 pipeline |
| `endgame == true` | 1 | Endgame 中每个 peer 只保持 1 个请求 |
| `downloadRate ≈ 0` | 2 | 新连接或无流量，使用最小值 |
| `downloadRate = 1 MB/s` | ≈ 192 | `3 × 1,048,576 / 16,384` |

---

## 8. Endgame 模式

### 8.1 触发条件

有两种条件触发 endgame：

1. **全局触发**：剩余待下载字节 ≤ 100 MiB
   ```go
   if d.SelectedSize() - d.completed.Load() <= 100 MiB {
       d.endGameMode.Store(true)
   }
   ```

2. **Per-peer 触发**：当 `requestABlock()` 无法为该 peer 找到足够的 free block 时
   ```go
   if numRequests > 0 && !p.peerChoking.Load() {
       p.setEndgame(true)
       d.endGameMode.Store(true)
   }
   ```

### 8.2 Endgame 中的行为

- **Busy block**：允许请求已被其他 peer 请求的 block（`stateRequested`）
- **单请求限制**：每个 peer 只保持 1 个 outstanding request（`desiredQueueSize = 1`）
- **去重**：通过 picker 的 `numPeers` 计数字段跟踪每个 block 被多少个 peer 请求

### 8.3 Strict Endgame

当前采用简化版 strict endgame：只有在以下条件**同时满足**时才允许 busy block：
1. 全局 `endGameMode == true`
2. 该 peer 当前没有 outstanding request（`myRequests.Size() + len(requestQueue) == 0`）

这防止了以下场景：
- 5 个 peer 同时下载最后 1 个 piece 的 1 个 block → 4 个重复请求被浪费

### 8.4 退出 Endgame

当 peer 找到足够的 free block 时，退出 endgame：
```go
if numRequests <= 0 {
    p.setEndgame(false)  // 该 peer 不再在 endgame 中
}
```

全局 endgame 模式不自动退出 —— 一旦剩余 ≤ 100 MiB，始终保持在 endgame。

---

## 9. Block 发送与排队

### 9.1 数据流

```
requestABlock() 
    │
    ├─ picker.markAsRequesting(block, peer)
    └─ peer.blockRequests ← pieceBlock          ← 容量 50 的 channel
        │
        ▼
    ourRequestHandle()
        │
        ├─ block 追加到 peer.requestQueue[]      ← 无界 slice
        └─ sendBlockRequests()
            │
            ├─ 检查 peer 的 QueueLimit / 2      ← 避免淹没慢 peer
            ├─ 检查 peer 是否 choked             ← choked 时只发 allowed-fast
            └─ p.Request(chunk)                 ← 发送 BT wire request
                │
                └─ myRequests.Store(chunk, time.Now())
```

### 9.2 sendBlockRequests 逻辑

```go
func (p *Peer) sendBlockRequests() {
    desiredSize := p.updateDesiredQueueSize()

    for {
        if p.myRequests.Size() >= desiredSize {
            return  // pipeline 已满
        }
        if len(p.requestQueue) == 0 {
            return  // 没有待发送的 block
        }

        block := p.requestQueue[0]
        p.requestQueue = p.requestQueue[1:]

        chunk := pieceChunk(p.d.info, block.pieceIndex, block.blockIndex)

        // Choke 检查
        if p.peerChoking.Load() && !p.allowFast.Contains(block.pieceIndex) {
            p.d.picker.abortDownload(block.pieceIndex, block.blockIndex)
            continue
        }

        // Queue limit 检查（使用 peer 通告限制的一半）
        if p.myRequests.Size() >= int(p.QueueLimit.Load()) / 2 {
            // 放回队列头部，等待下次尝试
            p.requestQueue = append([]pieceBlock{block}, p.requestQueue...)
            return
        }

        p.Request(chunk)
    }
}
```

### 9.3 流量控制

| 限流点 | 说明 |
|---|---|
| `desiredQueueSize` | 基于下载速率的动态 pipeline |
| `QueueLimit / 2` | 不超过 peer 通告最大队列的一半 |
| `peerChoking` | Choked 时只发 allowed-fast piece 的 block |
| `blockRequests` channel 容量 50 | 防止调度器超前产生过多 block（回退时 `abortDownload`） |

---

## 10. Peer 生命周期集成

### 10.1 连接建立

```
Peer 连接建立
  │
  ├─ Bitfield/Hav​​e 到达 → picker.incRefcount(pieceIndex)
  │   ├─ availability[pieceIndex]++
  │   └─ 如果从 0 变为 1 → dirty = true (触发优先级重建)
  │
  └─ Unchoke 到达
      ├─ scheduleRequestSignal → backgroundReqScheduler
      └─ requestABlock(peer) → 分配 block
```

### 10.2 Block 请求超时（Snub）

```
checkRequestTimeouts() 每 5 秒运行
  │
  ├─ 检查 myRequests 中最老的请求时间
  ├─ 如果超过 30 秒 → timedOut
  │
  └─ snub 处理:
      ├─ snubbed = true
      ├─ 遍历 myRequests:
      │   └─ picker.abortDownload(pieceIndex, blockIndex)
      ├─ 清空 requestQueue
      └─ scheduleRequestSignal (触发其他 peer 重新抓取)
```

Snubbed peer 的 `desiredQueueSize` 变为 1。当再次收到该 peer 的数据时，snub 自动清除。

### 10.3 Peer 断开连接

```
peer.close()
  │
  ├─ disconnecting = true
  │
  ├─ 遍历 Bitmap:
  │   └─ picker.decRefcount(pieceIndex) — 降低可用性
  │
  ├─ 遍历 myRequests:
  │   └─ picker.abortDownload(pieceIndex, blockIndex) — 释放 block
  │
  └─ 如果 peer 是 outgoing 且有 hadTransfer:
      └─ 立即重新加入 pendingPeers (无 backoff)
```

---

## 11. 数据接收与写入

### 11.1 数据接收

```
proto.Piece 消息到达
  │
  ├─ resIsValid:
  │   └─ myRequests.LoadAndDelete(chunk) → 记录 RTT
  │
  ├─ 如果是 snubbed peer → 解除 snub
  │
  └─ d.ResChan ← response
      │
      ▼
  handleRes(res):
      │
      ├─ picker.markAsWriting(pieceIndex, blockIndex)
      │   └─ stateNone/stateRequested → stateWriting
      │
      ├─ 加入 chunkHeap
      │   ├─ heap size < 1000 → 累积，检查 piece 是否完整
      │   └─ heap size ≥ 1000 → 弹出连续 chunk 序列（≤ 10 个），
      │       写入磁盘，markAsFinished()
      │
      └─ checkPieceBitmapDone(pieceIndex):
          └─ 所有 chunk done → checkPiece()
```

### 11.2 Piece 校验

```
checkPiece(pieceIndex):
  │
  ├─ 流式 SHA-1 hash 校验
  │
  ├─ 失败 → picker.resetPiece(pieceIndex)
  │   └─ 所有 block 回到 stateNone → 可被其他 peer 重新请求
  │   └─ corrupted[pieceIndex]++
  │
  └─ 通过 → picker.weHave(pieceIndex)
      └─ 所有 block → stateFinished
      └─ dirty = true (触发优先级重建)
      └─ have(pieceIndex) → 通知所有 peer
```

### 11.3 Endgame 写入（handleResEndgame）

在 endgame 模式中，chunk 不合并写入，而是**每个 chunk 立即写入**：
- 不等待完整的 piece 数据累积
- 每收到一个 chunk 就写盘 + markAsFinished
- 当 piece 的 chunk bitmap 完整时立即 hash 校验

---

## 12. 完整数据流

```
   Tracker /
   DHT / PEX
      │
      ├─ 新 peer 加入
      │   └─ connectToPeers() + pendingPeersSignal
      │
      ▼
┌──────────────────────────────────────────────────────────┐
│                    Peer 连接生命周期                       │
│                                                          │
│  Connect → Handshake → Bitfield → picker.incRefcount()  │
│     │                                                    │
│     ├─ Unchoke → scheduleRequestSignal                  │
│     ├─ Have    → picker.incRefcount()                    │
│     │          → scheduleRequestSignal                   │
│     └─ Piece   → responseCond.Signal()                   │
│                → ResChan → handleRes()                   │
│                                                          │
│  Close → picker.decRefcount() + abortDownload()         │
└──────────────────────────────────────────────────────────┘

                    scheduleRequestSignal
                           │
                    ┌──────▼──────┐
                    │ Scheduler   │  ← backgroundReqScheduler
                    │ 遍历 peers  │     定时器 5s + 信号触发
                    └──────┬──────┘
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
        requestABlock  requestABlock  requestABlock
        (peer A)       (peer B)       (peer C)
              │            │            │
              └────────────┼────────────┘
                           │
                    ┌──────▼──────┐
                    │ Pick Pieces │  ← picker.pickPieces()
                    │             │
                    │ Phase 1:    │
                    │  Partial    │
                    │ Phase 2:    │
                    │  Suggested  │
                    │ Phase 3:    │
                    │  Rarest-    │
                    │  First      │
                    └──────┬──────┘
                           │
                freeBlocks │ busyBlocks
                           │ (endgame)
                           ▼
                  ┌────────────────┐
                  │ markAsRequesting│ ← picker 更新 block 状态
                  │ blockRequests  │ ← peer channel
                  └───────┬────────┘
                          │
                   ┌──────▼──────┐
                   │ sendBlock   │ ← peer.sendBlockRequests()
                   │ Requests    │    requestQueue → wire
                   └──────┬──────┘
                          │
                   ┌──────▼──────┐
                   │ Wire Request│ ← proto.Request 发送
                   │ myRequests  │    记录时间戳
                   └──────┬──────┘
                          │
          ┌───────────────┼───────────────┐
          ▼               ▼               ▼
     ┌─────────┐   ┌──────────┐   ┌──────────┐
     │ Timeout │   │ Reject   │   │  Piece   │
     │ → snub  │   │ → 重分配  │   │  Response│
     └─────────┘   └──────────┘   └────┬─────┘
                                       │
                                ┌──────▼──────┐
                                │ markAsWriting│ ← picker
                                │ 写入磁盘      │
                                │ markAsFinished│
                                └──────┬──────┘
                                       │
                                ┌──────▼──────┐
                                │ SHA-1 Hash  │
                                │ 校验         │
                                └──┬──────┬───┘
                                   │      │
                              通过  │      │  失败
                                   ▼      ▼
                           ┌──────┐  ┌──────────┐
                           │weHave│  │resetPiece│
                           │      │  │corrupted │
                           └──┬───┘  └──────────┘
                              │
                      ┌───────▼───────┐
                      │ checkDone()   │
                      │ → Seeding     │
                      └───────────────┘
```

---

## 参考

- [libtorrent piece_picker.hpp](https://github.com/arvidn/libtorrent/blob/master/include/libtorrent/piece_picker.hpp)
- [libtorrent request_blocks.cpp](https://github.com/arvidn/libtorrent/blob/master/src/request_blocks.cpp)
- [libtorrent peer_connection.cpp — send_block_requests](https://github.com/arvidn/libtorrent/blob/master/src/peer_connection.cpp)
- [Arvid's Blog: Block request time-outs](http://blog.libtorrent.org/2011/11/block-request-time-outs/)
