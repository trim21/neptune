## Why

当前 session 状态以每个 torrent 一个 `.resume` 文件（bencode 格式）持久化在 `{session}/resume/{xx}/` 目录下。上千个 torrent 时目录碎片化、启动加载需要 `Walk` + 逐文件读取；`WriteFile` 直接覆盖写入不原子，进程崩溃可能损坏状态。SQLite 提供单文件存储、原子事务写入和可查询能力，为后续按标签/字段检索功能打基础。

## What Changes

- 新增 SQLite 存储层，resume 数据（下载状态、bitfield、tags、custom、trackers、selected files、速度限制、统计等）从 `.resume` 文件迁移到 `{session}/session.db`，每个 torrent 一行。
- `.torrent` 元数据文件保持文件系统存储，不变。
- 启动时自动迁移旧 `.resume` 文件：读取并导入 SQLite，成功后清理旧文件；迁移失败不影响其余 torrent 加载。
- `saveResume`/`SaveResume` 改为写 SQLite（WAL 模式，upsert 单行）。
- 启动加载逻辑从目录 `Walk` 改为查询 SQLite。
- 新增依赖 `modernc.org/sqlite`（pure-Go 驱动，满足 `CGO_ENABLED=0` 跨平台构建约束）。
- 外部行为不变：JSON-RPC API、下载状态机、resume 字段集合均无变化。**无 BREAKING 变更。**

## Capabilities

### New Capabilities
- `session-store`: session 持久化存储——resume 数据的 SQLite 存储、加载、迁移与生命周期管理

### Modified Capabilities
（无现有 spec）

## Impact

- `internal/download/resume.go` — resume 结构体序列化/反序列化改为 SQLite 读写
- `internal/download/from_resume.go` — `LoadFromResume` 数据源从字节变为存储层记录
- `internal/client/start.go` — 启动加载从 `filepath.Walk` 改为查询存储层；新增迁移逻辑
- `internal/client/download.go` — `UnmarshalResume` 适配新数据源
- `internal/session/session.go` — 持有存储层实例，`ResumePath` 语义变更
- `go.mod` — 新增 `modernc.org/sqlite` 及间接依赖
- 构建系统：确认 `CGO_ENABLED=0` 交叉编译不受影响
