# Instructions for Neptune
- Neptune is a headless BitTorrent client; [main.go](../main.go) is the entrypoint: initialization, HTTP server, graceful shutdown.
- Build: `CGO_ENABLED=0`, targets `linux/darwin/windows` on `amd64/arm64`.

## 项目结构
```
main.go                 入口
internal/
  client/               Client：下载生命周期管理
    store/              torrent 文件存储
    tracker/            Tracker announce/scrape
  config/               TOML 配置加载
  download/             单个下载：peer 管理、piece 调度、BEP 协议
  meta/                 torrent info 相关
  metainfo/             torrent 文件解析
  piece_store/          文件读写
  proto/                BitTorrent wire protocol
  session/              Session 上下文
  web/                  HTTP server + JSON-RPC
  pkg/                  通用工具包
    as/ assert/ bm/ empty/ fallocate/ filepool/ flowrate/
    gfs/ global/ gslice/ gsync/ heap/ mempool/ null/
    random/ ratelimit/ ro/ sys/ unsafe/
  version/              版本号
sdk/
  python/               Python JSON-RPC 客户端
  typescript/            TypeScript JSON-RPC 客户端
e2e/                    端到端测试
etc/
  example/              Docker Compose / TOML 示例
```

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

### Peer 类型双态（interface vs concrete）
`Peer` 类型通过 build tag 在两种形态间切换：

| Build | `Peer` 定义 | 文件 |
|---|---|---|
| `!release` (dev/test) | `type Peer = PeerInterface` | [peer_dev.go](../internal/download/peer_dev.go) |
| `release` | `type Peer = *peerImpl` | [peer_release.go](../internal/download/peer_release.go) |

**目的**：dev 模式用 interface 支持 mock 测试；release 模式用 concrete pointer 消除虚函数调用开销。

**规则**：
- 跨模块边界（如 `Download.peers`、`peerList`）统一用 `Peer` 类型，不在代码里硬编码 `*peerImpl` 或 `PeerInterface`。
- 新增 peer 方法时，先加到 `PeerInterface`，再在 `*peerImpl` 和 `*mockPeer` 分别实现。
- 禁止用类型断言 `p.(*peerImpl)` 绕过接口——这会在 release build 正常工作但违背设计意图，且 `go build -tags '!release'` 下也不安全。
- `*mockPeer`（[mock_peer_test.go](../internal/download/mock_peer_test.go)）必须实现完整的 `PeerInterface`，新增方法加空实现即可。

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
- import 顺序：标准库 → 第三方 → 本地模块。
- 测试：`-tags assert`。

## Version
- [internal/version](../internal/version) 通过 ldflags 注入版本号，`-v` 输出。

## 其他
- DHT 暂未实装。
- **Python SDK** ([sdk/python/](../sdk/python/))：同步 JSON-RPC 客户端，新增 RPC 需同步更新 models/client。
- **TypeScript SDK** ([sdk/typescript/](../sdk/typescript/))：异步 JSON-RPC 客户端，新增 RPC 需同步更新 types/client。
