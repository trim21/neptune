## Context

现状（见 proposal.md - Why）：resume 状态以每 torrent 一个 bencode `.resume` 文件持久化在 `{session}/resume/{xx}/`，由 `internal/download/resume.go` 序列化（`MarshalBinary`）、`from_resume.go` 反序列化（`LoadFromResume`）；启动时 `internal/client/start.go` 通过 `filepath.Walk` 加载，`saveResume` 在下载循环中被频繁调用。`.torrent` 元数据文件保持不变。

硬约束：构建要求 `CGO_ENABLED=0`，目标 `linux/darwin/windows × amd64/arm64`，因此 SQLite 驱动必须为 pure-Go。

## Goals / Non-Goals

**Goals:**
- resume 状态单文件存储于 `{session}/session.db`，字段语义与现状完全一致
- 启动加载与周期保存路径改为数据库读写，行为不回归
- 旧 `.resume` 文件一次性迁移，无数据丢失
- 写入原子性优于现在的 `WriteFile` 直接覆盖

**Non-Goals:**
- `.torrent` 文件不迁移（保持文件系统存储）
- 不提供基于数据库的查询/检索 API（标签过滤等留给未来功能）
- 不改变 JSON-RPC API 与下载状态机
- 不做写入节流/批处理优化（先保持现有调用频率语义）

## Decisions

### 1. 驱动：`database/sql` + `modernc.org/sqlite`

pure-Go 驱动，满足 `CGO_ENABLED=0` 约束；`database/sql` 是标准库接口，零学习成本。备选：`mattn/go-sqlite3`（CGO，违反构建约束，排除）；`zombiezen.com/go/sqlite`（modernc 之上的现代封装，API 更简洁但引入非标准库抽象，收益不足以替代标准 `database/sql`）。

### 2. Schema：单表，数组字段 JSON 编码

`resume` 表以 `info_hash TEXT PRIMARY KEY` 为键，列与 `Resume` 结构体一一对应：`base_path TEXT`、`bitfield BLOB`、`tags`/`custom`/`trackers`/`selected_files`/`file_paths` 为 JSON 编码的 TEXT（`selected_files` 为 NULL 表示全选）、整数列 `download_speed_limit`/`upload_speed_limit`/`add_at`/`completed_at`/`downloaded`/`uploaded`/`corrupted`/`state`/`piece_pick_strategy`/`queue_weight`、`tracker_key TEXT`。

备选：规范化子表（tags/trackers/files 拆表）。当前语义是整行存取、字段从不单独查询，JSON 列更简单且迁移映射直接；拆分只在需要按子字段查询时才有价值，属于未来优化。

### 3. Schema 版本化 migration（`PRAGMA user_version`）

数据库 schema 从一开始就通过版本化 migration 演进，而不是每次启动执行一条幂等 `CREATE TABLE`。migration 以 `internal/session/store/migrations/NNNN_name.sql` 文件组织，`go:embed` 打包进二进制，按文件名前缀版本号排序。打开数据库时读取 `PRAGMA user_version`，对每个高于当前版本的 migration 依次执行（每个 migration 一个事务，成功后更新 `user_version`）。中途失败时未完成的 migration 在下一次打开时重跑，语句保持幂等（`CREATE TABLE IF NOT EXISTS`）。新增 schema 变更只需添加下一个编号的 SQL 文件。备选：无版本机制的直接建表——表结构稳定时更简单，但 schema 演进必然发生，事后补 migration 成本更高；Go 代码内联 migration 列表——少一个目录但历史 SQL 与代码耦合，可读性差。

### 4. Store 按需打开连接，不常驻

`Store` 只持有数据库文件路径，每个操作方法内部打开连接（含 pragma 配置与 migration）、执行、关闭。理由：SQLite 打开开销低（毫秒级），避免长连接持有文件句柄与 WAL 状态；崩溃时无需处理常驻连接。`journal_mode=WAL` 持久化在数据库文件头，`busy_timeout`/`synchronous` 为连接级 pragma，每次打开重新设置。备选：常驻连接池（`MaxOpenConns=1`）——调用更少，但长持有连接，与"崩溃安全 + 轻资源"的目标相悖。

### 5. 迁移：启动时一次扫描，成功才删

`Client.Start()` 在加载数据库之前先扫描 `{session}/resume/**/*.resume`：每个文件用现有 bencode 解析逻辑解码，`Upsert` 成功后才删除旧文件；损坏文件跳过并记警告（一次性事件，不应成为升级门槛，与数据库内数据损坏的严格处理区分开）。迁移完成后删除 `resume` 目录（若为空）。

### 6. 删除路径同步

移除 torrent 时，现有删除 `.resume` 文件的逻辑改为 `Store.Delete(infoHash)`。

## Risks / Trade-offs

- [modernc.org/sqlite 增大二进制体积（约 10MB+）] → 静态链接 Go 二进制体积本已较大，可接受
- [WAL 产生 `session.db-wal`/`session.db-shm` 辅助文件] → SQLite 标准行为，正常关闭时合并；不影响 session 目录其他文件
- [迁移中断导致旧文件已删但数据未入库] → 按"先写入成功、后删除"的顺序执行，且 `Upsert` 在同一事务内完成
- [`saveResume` 高频写入成为瓶颈] → 单行 upsert 开销低于文件写；若实测出现锁竞争，再考虑节流（当前不预优化）
- [回滚到旧版本会丢失迁移后的状态] → 旧版本不读 `session.db`；降级需从备份恢复或重新添加 torrent

## Migration Plan

1. 发布包含本变更的版本，首次启动自动迁移（见 Decisions 5），无需用户操作
2. 迁移为一次性；旧 `.resume` 文件删除后不保留副本（写入已确认成功）
3. 回滚：备份 session 目录或接受重新添加 torrent 的成本

## Open Questions

- `saveResume` 在下载循环中的实际调用频率是否会导致 WAL 文件增长过快——实现后实测观察，不影响本设计结论。
