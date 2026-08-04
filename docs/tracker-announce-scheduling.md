# Tracker Announce Scheduling

本文档定义 Neptune tracker announce 的行为、API 边界和并发不变量。修改
`internal/client/tracker` 或 Download 与 tracker 的生命周期接线时，应以本文档作为
设计与 review 的依据。

本文档是规范而不是实现注释。如果确实需要改变这些行为，代码、本文档和对应回归测试
必须在同一个变更中更新；不得让实现静默偏离本文档。

## 目标

- 普通添加的 torrent 在初始化完成后立即首次 announce。
- 从 active resume 恢复的 torrent 错峰首次 announce，避免进程启动负载尖峰。
- tracker response 驱动后续周期 announce，Download 不管理周期定时器。
- Download 状态变化可以提交即时 tracker 事件。
- 请求执行期间出现的新事件不会被旧 response 覆盖，也不会永久滞留。
- 一次 announce 按 tier 顺序尝试（BEP 12）：主 tracker 可用时 backup tracker 不被打扰；
  主 tier 整体不可达时才启用 backup。

## 所有权边界

调度分为两个来源：

1. **外部事件**：Download 的状态变化统一由 `syncTrackerState`（在
   `commitStateTransition` 末尾调用）翻译成 `Start(0)` 或 `Announce(EventStopped)`。
   手动 reannounce 是独立的外部命令，直接调用 `Reannounce()`，不走状态转移。
2. **内部续约**：一次 announce 完成后，每个被尝试的 tracker 根据其 response interval
   续期自己的 `NextAnnounce`，timer loop 按最早的 `NextAnnounce` 唤醒下一轮普通 announce。

Download 只表达生命周期意图，不计算周期 deadline，也不感知 tracker 列表结构。
Tracker 不读取或镜像 Download 状态。

各状态转移路径（Start/Stop、recheck、文件优先级变化、错误、Move 恢复）不得再各自
手动调用 tracker API——统一由 `syncTrackerState` 根据新状态和链当前 active 状态
决定，避免遗漏。仅有两个显式例外：`completed`（见 `finalizeDownloadCompletion`）
和 resume 错峰 `Start(trackerStagger)`（构造时调用，不走转移路径）。

## Tier 语义（BEP 12）

announce 按 tier 分组（`announce-list`，tier 内顺序在加载时洗牌）。一次 announce 是
一个**整体轮**，按 tier 顺序尝试：

- 轮从 tier 0 开始；tier 内所有可尝试的 tracker **并行**发请求。
- tier 内至少一个成功 → 轮结束，backup tier 不被尝试。
- tier 内所有 tracker 都被尝试且全部失败 → 推进到下一 tier（backup 首次启用）。
- tier 内存在处于节流期（`NextAnnounce`/`EarliestAnnounce` 未到）的 tracker → 整轮
  停止，不推进：该 tier 仍然活着，只是安静，backup 不应被激活。
- 成功的 tracker 被移到其 tier 的前端（BEP 12：成功连接移到 tier 前端）。

由此，backup tier 只在主 tier 整体不可达时被启用；主 tier 恢复后（其 tracker 在
重试期后成功）自然重新接管。

## 公共 API

### `Start(maxDelay time.Duration)`

激活 announce 链，并安排**一轮** `event=started`（组级，从 tier 0 开始尝试）。

- `maxDelay == 0`：立即 announce，用于普通添加和用户显式启动。
- `maxDelay > 0`：先将窗口限制在 5 秒至 60 分钟，再为整组选择 `[0, normalizedMaxDelay)`
  内的一个随机延迟，用于 active resume 恢复。错峰对象是 torrent（整轮），不是单个
  tracker。
- `Start` 把组级期望事件设为 `started`；尚未发出的 `started/stopped` 可以被后续状态
  覆盖，但不能取消已经 in-flight 的轮。
- 重复调用必须来自真实的 Download 状态变化；无状态变化的重复 Start 不应发送新的
  `started`（由 `syncTrackerState` 保证）。

### `Announce(event AnnounceEvent)`

把外部生命周期事件提交给组级状态机；事件可立即执行或在当前轮完成后执行。

- `EventCompleted`：设置一次性 latch，不改变链的 active 状态；发送后链继续周期 announce。
- `EventStopped`：将链设为 inactive，并广播 `stopped` 给所有**曾经发出过请求**的
  tracker；从未被尝试的 backup 不打扰。stopped 轮完成后不得续约。
- 已经 in-flight 的轮不会被取消。`completed` 会被 latch，`started/stopped` 只保留最新
  的期望状态。

### `Reannounce() bool`

安排一次无 event 的普通轮。

- 仅 active announce 链允许执行。
- 轮内只尝试已经达到 `EarliestAnnounce` 的 tracker。
- 没有任何 tracker 符合条件时返回 `false`。
- 手动 reannounce 不得伪造 `started` 或 `completed`。
- 生命周期事件待处理或 in-flight 时，普通 announce 不会抢占它，函数返回 `false`。
- 返回 `true` 表示已安排一轮普通 announce。

### `Run()` 和 `Shutdown()`

- `Run` 持有唯一的 timer loop，按组级调度状态唤醒：待处理事件立即执行，否则按最早的
  `NextAnnounce`（或错峰 `pendingAt`）唤醒。
- `Shutdown` 停止后续调度，并直接对每个曾经发出过请求的 tracker 同步发送 `stopped`——
  包括请求 in-flight 的 tracker；传输错误记录到 tracker，in-flight 请求并发完成，因链
  已设为 inactive 且 pending 已清空，不会续约。
- 不得依赖固定周期 ticker 兜底；除已经终止 `Run` 的 Shutdown 外，所有会让轮从
  in-flight 重新变为可调度的路径都必须唤醒 loop。

## 单一调度状态

调度状态是**组级**的：一次 announce 是一个轮，轮内 tier 顺序尝试。每个 tracker 只
保存自己的节流和结果数据，不保存调度意图。

```go
type Trackers struct {
    active          bool
    inFlight        bool
    inFlightEvent   AnnounceEvent
    pendingEvent    AnnounceEvent   // 最新期望生命周期事件（started/stopped）
    pendingAt       time.Time       // pendingEvent 最早可派发时间（错峰）
    pendingCompleted bool           // completed latch
    reannounce      bool            // 已安排一轮手动普通 announce
}

type Tracker struct {
    LastAnnounceTime time.Time
    NextAnnounce     time.Time
    EarliestAnnounce time.Time
    Err              error
    URL              string
    FailureMessage   string
    Interval         time.Duration
    PeerCount        int
    everAttempted    atomic.Bool    // 是否发出过请求（stopped 广播范围）
}
```

`NextAnnounce` 是普通轮可再次尝试该 tracker 的最早时间（按 response interval 或失败
重试间隔续期），仅用于 timer 选择和轮内节流。`EarliestAnnounce` 是手动 reannounce 的
节流（min interval）。从未被尝试的 tracker 的 `NextAnnounce` 为零值（表示"推进到该
tier 时可立即尝试"），但不驱动调度：它只经由事件轮或 tier 推进被尝试。

不得增加 tracker 外部的 pending map，或在 Download 中镜像 tracker 调度状态。

状态含义：

- `inFlight=false` 且无 pending：当前没有工作，loop 睡到最早的 `NextAnnounce`。
- 有 pending 事件且 `now >= pendingAt`：立即派发事件轮。
- `inFlight=true`：轮执行中；`pendingEvent`/`pendingCompleted` 记录轮完成后必须处理的
  生命周期意图。

## 轮执行规则

一次轮从调度到续约的正常流程：

```text
Start / Announce / NextAnnounce 到期
                |
                v
         派发轮（inFlight=true）
                |
                v
   tier 0 并行尝试 -> 成功：轮结束，成功 tracker 前移
        | 全失败
        v
   tier 1 并行尝试 -> 成功：轮结束
        | 全失败
        v
      ... 直到最后一个 tier；全部失败则按失败重试间隔续期各 tracker
```

规则：

1. 事件轮（`started`/`completed`）不节流：尝试当前 tier 的所有 tracker。普通轮
   （周期/reannounce）只尝试达到节流时间的 tracker；未到期的 tracker 使整轮停止。
2. response 更新 `LastAnnounceTime`、`EarliestAnnounce`、`NextAnnounce`、`Interval`、
   错误和 swarm 统计；成功时该 tracker 移到所在 tier 前端。
3. 如果没有 pending 生命周期事件且链 active，各 tracker 按自己的 `NextAnnounce`
   安排下一次普通轮（由 timer loop 按最早者唤醒）。
4. 如果存在 pending lifecycle event，轮完成后立即安排下一轮，不得把它推迟到下一个
   interval。
5. `EventStopped` 轮完成后不得安排周期 announce。
6. 释放 `inFlight` 后必须唤醒 timer loop；已经终止 `Run` 的 Shutdown 除外。
7. 轮内对 tier 列表做深拷贝，Remove/Replace 与 HTTP 请求并发安全。

## Event 语义

### `started`

- 普通添加初始化完成后发送。
- active resume 在错峰窗口内发送。
- stopped torrent 被用户显式启动后发送。
- recheck 后进入 `Downloading`/`Seeding` 且链处于 inactive 时，由 `syncTrackerState`
  发送；链已 active 的 recheck 不发送。
- 主 tier 整体失败、backup tier 首次启用时，backup 的 tracker 收到 `started`。
- 不得用于普通手动 reannounce。

### `completed`

- 只在本次下载真正完成全部 torrent pieces 后发送一次。
- 启用 completion recheck 时，应在复检通过后发送。
- 从 resume 恢复时发现数据已经完整，不代表本次运行完成了下载，不得发送
  `completed`。
- 对已有 seed 做手动 recheck，不得发送 `completed`。
- partial download 进入 Seeding 也不得发送 `completed`。
- `completed` 是组级 latch，优先于 `started/stopped`；随轮发送，轮全部失败时不重发。
- 错峰 `started` 尚未发出时收到 `completed`，`started` 的派发时间提前到当前时间，
  保持 `completed` 先于 `started` 的 wire 顺序。

### `stopped`

- 用户显式 Stop 或进程 Shutdown 时发送。
- 只发送给曾经发出过请求的 tracker；从未被尝试的 backup 不发送。
- stopped response 不得启动新的周期 announce。
- 快速 Stop/Start 时，已经 in-flight 的 stopped 可以完成；随后安排的 started 必须继续
  执行。

## Download 场景映射

| 场景 | 行为 |
|---|---|
| 普通添加完成初始化 | `syncTrackerState` 在 checkNew 转移后调用 `Start(0)` |
| active resume 恢复 | 构造时显式 `Start(trackerStagger)` |
| stopped resume 恢复 | 不启动 announce 链 |
| 用户显式 Start | 状态确实变化后由 `syncTrackerState` 调用 `Start(0)` |
| 完整下载完成 | `Announce(EventCompleted)` |
| 用户显式 Stop | 状态确实变化后由 `syncTrackerState` 调用 `Announce(EventStopped)` |
| 手动 reannounce | `Reannounce()` |
| 手动 recheck（链已 active）数据完整 | 不发送任何 tracker 事件，链按原周期继续 |
| 手动 recheck（链 inactive）进入 Seeding/Downloading | `syncTrackerState` 自动 `Start(0)` 激活链 |

Checking 和 Moving 是本地瞬时状态，不拥有 tracker 周期调度。进入这些状态本身不应
创建第二套 tracker 状态或定时器。

## 统一状态处理

所有状态转移在 `commitStateTransition` 末尾统一调用 `syncTrackerState(to)`，它是
Download 侧唯一把状态翻译成 tracker 事件的路径：

- `to` 为 `Stopped` 或 `Error`：若链 active，`Announce(EventStopped)`。
- `to` 为 `Downloading` 或 `Seeding`：若链 inactive，`Start(0)`。
- 其他（`PendingDownloading`、`Checking`、`Moving`）：不触碰链。

`completed` 不在此推导：只有真正完成下载时才由 `finalizeDownloadCompletion` 显式
发送。resume 的错峰 `Start(trackerStagger)` 在构造时显式调用，不走转移路径。

## 首次错峰规则

resume 加载前，Client 统计 resume 文件总数：

```text
trackerStagger = totalResumeDownloads / 2 seconds
```

这对应约每秒两个 torrent 开始首次 announce。

- 未完成 torrent 的窗口上限为 5 分钟，使下载能及时恢复。
- seed 的窗口由总 resume 数决定。
- `Start` 将正数窗口限制在 5 秒至 60 分钟。
- 延迟窗口从真正调用 `Start` 时开始，而不是从构造 Download 时开始。
- 普通添加不得复用 resume stagger。

## 并发与最终一致性

- 已经 in-flight 的 HTTP 请求不要求取消。
- `completed` 必须保留到轮发送；`started/stopped` 允许在未发送时收敛为最新状态。
- 同时只有一轮在飞；轮执行期间到达的事件更新 pending 状态，轮完成后立即执行。
- Remove 可以让 in-flight 请求自然完成，但被移除的 tracker 不得再次续约，其 late
  response 的所有副作用（swarm 统计、错误、peer）都会被丢弃。
- Replace 期间 in-flight 的旧 URL response 的统计副作用按新 URL 归属，属可接受的有
  限误差；链的续约仍受「轮执行规则」约束。
- 不为消除这些有限竞态引入跨 Download/tracker 锁、barrier 或事务。
- `Trackers.mu` 保护 tiers、active 和调度字段；`everAttempted` 原子访问。
- 轮执行期间持有 tier 列表的深拷贝，Remove/Replace 并发安全。

## Review Checklist

修改 tracker 调度时必须检查：

- 普通添加是否仍立即首次 announce（经 `syncTrackerState` 触发 `Start(0)`）。
- active resume 是否仍调用 `Start(trackerStagger)`，且 stopped resume 不启动。
- 是否有状态转移绕过 `commitStateTransition`/`syncTrackerState` 直接调用 tracker
  API，造成遗漏。
- 是否出现 tracker 外部的 pending map 或 Download 侧 tracker 调度状态。
- 普通轮是否按 tier 顺序尝试：主 tier 成功即停，backup 只在主 tier 整体失败时启用。
- tier 内存在节流中（未到期）的 tracker 时，普通轮是否整轮停止而不是推进到 backup。
- 事件轮（`started`/`completed`）是否不节流；backup 首次启用是否收到 `started`。
- 轮 in-flight 时，`completed` 是否保留且 `started/stopped` 是否收敛为最新状态。
- 普通 announce 是否不会替换或越过 pending 的生命周期事件。
- 快速 `completed -> stopped -> started` 是否发送 `completed` 后再发送最终 `started`。
- Shutdown 是否对每个曾发出请求的 tracker（含 in-flight）发送 `stopped`。
- Remove 后 late response 是否丢弃 swarm 统计/peer 副作用。
- stopped 是否终止周期续约。
- completion recheck 是否只发送 completed，不追加 started。
- 手动 recheck 是否不发送 completed；链的 active/inactive 是否由 `syncTrackerState`
  统一处理。
- resume 完整性校验是否错误发送 completed。
- 手动 reannounce 是否遵守 `EarliestAnnounce` 且不携带生命周期 event。
- 除已经终止 `Run` 的 Shutdown 外，所有清除 `inFlight` 的路径是否会唤醒 loop。
- Remove、Replace、快速 Stop/Start 和 Shutdown 是否能够收敛。
- 成功后 tracker 是否前移到所在 tier 前端（BEP 12）。

至少运行：

```sh
go test -tags assert ./internal/client/tracker ./internal/download
go test -race -tags assert ./internal/client/tracker
go test -tags 'assert release' ./internal/client/tracker ./internal/download -run '^$'
```
