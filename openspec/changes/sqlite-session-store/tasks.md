## 1. 依赖与存储层

- [x] 1.1 在 `go.mod` 添加 `modernc.org/sqlite` 依赖，确认 `CGO_ENABLED=0` 下 linux/darwin/windows 可编译
- [x] 1.2 新建 `internal/session/store` 包：将 `resume` 结构体定义从 `internal/download/resume.go` 移入，建立版本化 migration 机制——`migrations/NNNN_name.sql` 文件 + `go:embed` 打包，按文件名版本号排序，每迁移一个事务，`PRAGMA user_version` 驱动
- [x] 1.3 实现 `Store`（只持有数据库路径，每次操作按需打开连接）：打开 `{session}/session.db`（不存在则创建），配置 WAL、`busy_timeout`、`synchronous=NORMAL`，执行待定 migration
- [x] 1.4 实现 `Store.Upsert` / `Store.All` / `Store.Delete` / `Store.Count`，含行 <-> 结构体映射与 `database/sql` 参数绑定，每个方法独立打开/关闭连接
- [x] 1.5 单测：`Store` CRUD 往返一致、空库创建、JSON 列空值（`selected_files` NULL）处理、migration 幂等重跑（新建库直接到最新版本、旧版本库升级、失败后重试）

## 2. 读写路径改造

- [x] 2.1 `internal/download/resume.go`：`saveResume` 从写文件改为 `Store.Upsert`；`MarshalBinary` 拆出"采集状态"逻辑供 store 层复用
- [x] 2.2 `internal/download/from_resume.go`：`LoadFromResume` 改为从 `resume` 结构体（非 bencode 字节）构造 `Download`，保留现有校验逻辑（info hash 匹配、selected files 范围、bitfield 校验、file path 恢复）
- [x] 2.3 `internal/session/session.go`：`Session` 持有 `*store.Store`，`New` 时初始化；`ResumePath` 语义调整为数据库文件路径或移除
- [x] 2.4 `internal/client/start.go`：加载路径从 `filepath.Walk` 改为 `Store.All()` 循环构造下载，保持原有 announce stagger 计数语义
- [x] 2.5 `internal/client/download.go`：`UnmarshalResume` 适配新数据源（或由 `All()` 结果替代）

## 3. 启动迁移

- [x] 3.1 实现迁移函数：扫描 `{session}/resume/**/*.resume`，用现有 bencode 解析逻辑解码每个文件，`Upsert` 成功后删除旧文件
- [x] 3.2 损坏文件跳过并记警告日志，不影响其余文件迁移；无旧文件时直接跳过迁移
- [x] 3.3 迁移完成后清理空的 `resume` 目录；迁移只触发一次（迁移完成后不再检测）
- [x] 3.4 单测：迁移成功删除旧文件、损坏文件跳过、重复启动不重复迁移

## 4. 删除路径

- [x] 4.1 移除 torrent 的路径（`internal/client` 或 `internal/download` 中删除 `.resume` 文件处）改为 `Store.Delete(infoHash)`

## 5. 验证

- [x] 5.1 端到端验证：构造含旧 `.resume` 文件的 session 目录，启动后状态恢复、旧文件被清理；无旧文件时正常启动
- [x] 5.2 全量 `go test ./... -tags assert` 通过；`CGO_ENABLED=0` 交叉编译 linux/darwin/windows × amd64/arm64 通过
- [x] 5.3 崩溃安全性验证：写入过程中 kill 进程，重启后数据库可正常加载
