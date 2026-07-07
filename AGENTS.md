# Instructions for Neptune
- Neptune is a headless BitTorrent client; [main.go](../main.go) is the entrypoint: initialization, HTTP server, graceful shutdown.
- Build: `CGO_ENABLED=0`, targets `linux/darwin/windows` on `amd64/arm64`.

## Session
- 管理 session 目录（默认 `~/.neptune`），包含 torrent 持久化、resume 持久化、日志。
- 通过文件锁防止多实例并发。

## Config
- 配置来源：TOML 文件 + CLI flag + 环境变量（`NEPTUNE_` 前缀）。
- [internal/config/config.go](../internal/config/config.go) 定义配置结构体，缺文件用默认值，未知键报错。

## Flags & Env
| Flag | Default | Env | 说明 |
|---|---|---|---|
| `--session-path` | `~/.neptune` | `NEPTUNE_SESSION_PATH` | session 目录 |
| `--config-file` | `{session}/config.toml` | `NEPTUNE_CONFIG_FILE` | config 文件路径 |
| `--web` | `127.0.0.1:8002` | `NEPTUNE_WEB` | HTTP 监听地址 |
| `--web-secret-token` | auto-generated | `NEPTUNE_WEB_SECRET_TOKEN` | 鉴权 token |
| `--p2p-port` | `50047` | `NEPTUNE_P2P_PORT` | P2P 端口（可覆盖 TOML） |
| `--log-json` | false | `NEPTUNE_LOG_JSON` | JSON 格式日志 |
| `--log-level` | `"info"` | `NEPTUNE_LOG_LEVEL` | 日志级别 |
| `--log-save-to-file` | true | `NEPTUNE_LOG_SAVE_TO_FILE` | 是否写日志文件 |
| `--debug` | false | `NEPTUNE_DEBUG` | 启用 debug/pprof |

## Web Server
- [internal/web/web.go](../internal/web/web.go) 提供 HTTP API，token 鉴权。
- `GET /metrics` — Prometheus 指标
- `GET /healthz` — 健康检查
- `GET /debug/version` — 版本信息
- `GET /debug/neptune/{info_hash}` — 单个下载调试视图
- `POST /json_rpc` — JSON-RPC 入口
- `GET /docs/` — OpenAPI 文档
- `GET /debug/**` — pprof（仅 `--debug`）

## JSON-RPC
- [internal/web/jsonrpc/](../internal/web/jsonrpc/) JSON-RPC 2.0 框架，负责方法注册、参数校验、文档生成。
- 方法实现在 `usecase` 层。

## Core Client
- [internal/client/c.go](../internal/client/c.go) 负责所有下载的增删查、全局连接数限制、文件句柄池。

## Download
- [internal/download/](../internal/download/) 单个下载的全生命周期：文件校验、peer 管理、piece 调度、BEP 协议交互。
- 状态机：Stopped / Downloading / Seeding / Checking / Moving / Error。
- 支持 BEP 6 (Fast)、BEP 10 (Extension)、BEP 11 (PEX)。

## Build System
- [taskfile.yaml](../taskfile.yaml) 定义 `lint` / `gen` / `test` / `build` / `release` 任务。
- 静态链接，Go >= 1.22。

## Build Tags
- `release` vs `!release`：优化路径 vs 开发辅助。
- 平台 tag（`linux`/`darwin`/`windows`）控制系统相关实现。

## Internal Packages (`internal/pkg/`)
- **`as`** — 安全整数转换
- **`assert`** — 运行时断言
- **`bm`** — 线程安全 bitmap（piece 追踪）
- **`crc32c`** — CRC32C 校验
- **`empty`** — 零分配 channel 信号
- **`fallocate`** — 跨平台文件预分配
- **`filepool`** — LRU 文件句柄池
- **`flowrate`** — EMA 速率监控
- **`gfs`** — 上下文感知文件操作
- **`global`** — 编译时常量
- **`global/tasks`** — 任务分发
- **`gslice`** — 泛型 slice 操作
- **`gsync`** — 同步原语
- **`heap`** — 泛型最小堆
- **`mempool`** — 内存池
- **`null`** — 泛型可选值
- **`random`** — 加密安全随机
- **`ro`** — 只读字节视图
- **`sys`** — OS 检测
- **`unsafe`** — 零分配 string/[]byte 互转

## Conventions
- 错误用 `trim21/errgo`。
- 禁止 `sync/atomic`，用 `uber/atomic`。
- import 顺序：标准库 → 第三方 → 本地模块。
- 测试：`-tags assert`。
- 文件命名：`c_*.go` Client、`d_*.go` Download、`p_*.go` Peer。

## Version
- [internal/version](../internal/version) 通过 ldflags 注入版本号，`-v` 输出。

## 其他
- DHT、MSE 暂未实装。
- **Python SDK** ([sdk/python/](../sdk/python/))：同步 JSON-RPC 客户端，新增 RPC 需同步更新 models/client。
- **TypeScript SDK** ([sdk/typescript/](../sdk/typescript/))：异步 JSON-RPC 客户端，新增 RPC 需同步更新 types/client。
