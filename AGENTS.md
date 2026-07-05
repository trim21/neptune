# Instructions for Neptune
- Neptune is a headless BitTorrent client; entrypoint [main.go](../main.go) wires flags/env (via pflag+viper), session path, locking (flock), logging (zerolog), config load (TOML), core client start, JSON-RPC/metrics HTTP server (chi), and graceful shutdown on SIGHUP/SIGINT/SIGQUIT/SIGTERM.
- Build constraint: `windows|darwin|linux && (amd64|arm64)`; CGO is disabled in all builds (`CGO_ENABLED=0`).

## Session & Locking
- Session path defaults to `~/.neptune`; directories `torrents`, `resume`, `logs` are created on startup. A `.lock` file (flock) prevents concurrent runs — if another instance holds the lock, the process exits immediately.
- Torrent files persist at `{session}/torrents/<ih[:2]>/<ih[2:4]>/<hash>.torrent` (hash-based sharding). Resume files at `{session}/resume/<ih[:2]>/<hash>.resume` (bencode-encoded).
- Logs rotate via lumberjack (10MB/3 backups/28 days) at `{session}/logs/app.log` when `log-save-to-file` is enabled.

## Config
- Config comes from TOML ([internal/config/config.go](../internal/config/config.go)) under `[application]` key, merged with CLI flags/env (`NEPTUNE_` prefix, dashes mapped to underscores via `SetEnvKeyReplacer`).
- `Application` struct fields: `DownloadDir` (default `~/downloads`), `MaxHTTPParallel` (100), `P2PPort`, `NumWant`, `GlobalConnectionLimit` (50), `Fallocate` (false).
- Only `P2PPort` is overridable from viper/env; all other fields come from TOML only. Missing config file is tolerated and defaults are applied.
- TOML decoder uses `DisallowUnknownFields()` — unknown keys cause an error.
- Example config: [etc/example/config.toml](../etc/example/config.toml). Example deployment: [etc/example/docker-compose.yaml](../etc/example/docker-compose.yaml) (host networking, NEPTUNE_* env vars, token wiring for flood UI).

## Flags & Env
| Flag | Default | Env | Note |
|---|---|---|---|
| `--session-path` | `~/.neptune` | `NEPTUNE_SESSION_PATH` | |
| `--config-file` | `{session}/config.toml` | `NEPTUNE_CONFIG_FILE` | |
| `--web` | `127.0.0.1:8002` | `NEPTUNE_WEB` | HTTP listen addr |
| `--web-secret-token` | auto-generated | `NEPTUNE_WEB_SECRET_TOKEN` | 32-char URL-safe |
| `--p2p-port` | `50047` | `NEPTUNE_P2P_PORT` | Overrides TOML |
| `--log-json` | false | `NEPTUNE_LOG_JSON` | |
| `--log-level` | `"info"` | `NEPTUNE_LOG_LEVEL` | trace/debug/info/warn/error |
| `--log-save-to-file` | true | `NEPTUNE_LOG_SAVE_TO_FILE` | |
| `--debug` | false | `NEPTUNE_DEBUG` | Enables debug, including pprof routers |
- For new config knobs, add to `Application` struct with TOML tags and sane defaults, and wire in [main.go](../main.go) via viper bindings. Flag/env names use kebab-case/snake_case and `NEPTUNE_` prefix.

## Web Server
- Web server ([internal/web/web.go](../internal/web/web.go)) listens on `--web` with Authorization header token. Routes:
  - `GET /metrics` — Prometheus metrics
  - `GET /healthz` — returns `"."`
  - `GET /debug/version` — version + go build info
  - `GET /debug/neptune/{info_hash}` — tracker/debug text view (rates, trackers, peers, bitmaps, missing pieces)
  - `GET /debug/events` — `net/trace` events
  - `GET /debug/**` — pprof endpoints (only when `--debug`)
  - `POST /json_rpc` — all JSON-RPC methods (NoCache + Auth middleware)
  - `GET /docs/openapi.json` — OpenAPI spec
  - `GET /docs/` — Swagger UI
- Auth middleware compares `Authorization` header constant (`"Authorization"`) against the token; returns JSON-RPC error `-32600` on mismatch.
- Avoid generic HTTP handlers in route setup; mount through chi router with middleware and consistent JSON-RPC error envelopes (see [internal/web/error.go](../internal/web/error.go)).

## JSON-RPC
- Custom JSON-RPC 2.0 framework in [internal/web/jsonrpc/](../internal/web/jsonrpc/) — `Handler` with methods map, go-playground/validator integration, and OpenAPI reflector.
- 10 registered methods: `system.ping`, `transfer_summary`, `torrent.add`, `torrent.get`, `torrent.list`, `torrent.files`, `torrent.peers`, `torrent.trackers`, `torrent.move`, `torrent.remove`.
- Handlers built with `usecase.NewInteractor`, registered on the shared `Handler` via `u.SetName()` then `h.Add(u)`.
- Standard error codes: ParseError (-32700), InvalidRequest (-32600), MethodNotFound (-32601), InvalidParams (-32602), InternalError (-32603).
- **When adding RPCs**: define request/response structs with json tags and `validate:"required"` tags; info-hash fields must enforce 40-hex length (`len(req.InfoHash) != sha1.Size*2`); return errors via `CodeError(code, err)` in [internal/web/error.go](../internal/web/error.go) to set `AppErrCode`; register a unique method name string.
- Request structs use pointer types (`*SomeRequest`) with `required:"true"` validation. Response structs embed core domain types. `torrent.add` accepts `torrent_file` as raw bytes (base64-encoded by client).
- OpenAPI spec built with `swaggest/openapi-go`, secured via API key header definition. Description embedded from [internal/web/description.md](../internal/web/description.md).

## Core Client
- `Client` ([internal/core/c.go](../internal/core/c.go)) manages downloads via `downloadMap map[metainfo.Hash]*Download` and `downloads []*Download` guarded by `sync.RWMutex`.
- Global connection limit via `semaphore.Weighted`, file handles via `filepool.FilePool` (LRU, 5000 entries, 5-min TTL).
- **`AddTorrent()`**: saves .torrent file (hash-sharded path), creates `Download`, appends to sorted slice, adds to map, spawns `go d.Init(false)`.
- **`RemoveTorrent()`**: removes from map/slice, saves resume, cancels download context, closes all peers, removes resume file, optionally deletes data files, prunes empty parent directories.
- When touching torrent storage paths, preserve hash-based sharding (`<ih[:2]>/<ih[2:4]>`) and resume clean-up logic.

## Download State & Concurrency
- `State` bitmask (generated stringer via `//go:generate stringer -type=State`): `Stopped`(1), `Downloading`(2), `Seeding`(4), `Checking`(8), `Moving`(16), `Error`(32).
- State wait pattern: `wait(state)` blocks via channel-based `gsync.Cond` until `d.GetState() & state != 0`.
- Concurrency model: `d.m sync.RWMutex` for state, `d.peers *xsync.Map[netip.AddrPort, *Peer]` for peers, buffered channels (cap 1) for throttled signaling (`scheduleRequestSignal`, `scheduleResponseSignal`, `pendingPeersSignal`, `buildNetworkPieces`, `pexAdd`, `pexDrop`, `ResChan`).
- Background goroutines via `startBackground()`: `backgroundResHandler`, `backgroundReqScheduler`, `handleConnectionChange`, `backgroundReqHandler`, `unchokeLoop`, `backgroundTrackerHandler`, `connectToPeers`, `pexAdd/pexDrop`, `optimisticUnchokeLoop`.
- **Scheduling**: rarest-first (priority = `(availability+1)*3`), partial pieces first, endgame mode triggered when remaining < 100 MiB. Chunks tracked per-piece via `bm.Bitmap`; response heap buffers up to 1000 chunks before flushing contiguous sequences to disk. Piece verification uses SHA-1.
- **Peer protocol**: full BitTorrent wire spec + BEP 6 (Fast Extension), BEP 10 (Extension Protocol including lt_donthave), BEP 11 (PEX). PeerID prefix: `-NE00X0-`. Slow start doubles pipeline size on piece completion; snubbing for timeout peers (>30s). Connection history LRU (10-min TTL).

## Build System
- Builds use go-task ([taskfile.yaml](../taskfile.yaml)):
  - `task lint` — `golangci-lint run --fix`
  - `task gen` — `go generate ./...` (runs stringer for `State` and proto `Message`)
  - `task test` — `go test -count=1 -coverprofile=coverage.txt -covermode=atomic -tags assert ./...`
  - `task build` — dev build to `dist/neptune.exe`
  - `task dev --watch` — builds to `dist/dev/neptune.exe` and restarts on code change
  - `task release` — builds static `CGO_ENABLED=0` binaries for all platforms with `-tags release` and version ldflags
- Binary ldflags: `-s -X 'neptune/internal/version.Ref=<git describe>' -X 'neptune/internal/version.BuildDate=<UTC ISO8601>'`
- Development requires Go >= 1.22 and go-task installed; no system package installs are needed.

## Build Tags & Release/Dev Split
- `release` vs `!release`: the `release` tag activates optimized code paths; `!release` enables development helpers.
- Dev-only (`!release`): `as` panics on overflow (release: no-op cast); `assert` panics on failure (release: no-op); `global/tasks` uses raw `go` (release: `ants` goroutine pool, size 20).
- Platform tags: `linux`, `darwin`, `windows` for fallocate and gfs copy implementations.
- Generated stringer outputs go into the source tree and must be committed.

## Internal Packages (`internal/pkg/`)
All reusable utilities live here — prefer these over new third-party packages:
- **`as`** — Safe integer conversions (dev panics on overflow, release no-op).
- **`assert`** — Runtime assertions (dev panics, release no-op). Imported by test tag.
- **`bm`** — Thread-safe bitmap wrapper around `kelindar/bitmap` for piece tracking; big-endian byte order for BT protocol.
- **`crc32c`** — CRC32-C (Castagnoli) checksum.
- **`empty`** — Zero-allocation empty struct for channel signaling.
- **`fallocate`** — Platform-specific file preallocation (Linux `syscall.Fallocate`, Darwin `fstore_t`, Windows `SetFileInformationByHandle`).
- **`filepool`** — LRU file handle pool (`hashicorp/golang-lru/v2` expirable LRU).
- **`flowrate`** — EMA-based transfer rate monitoring (`Monitor` with instant/current/peak/average rates, `io.Reader` wrapper).
- **`gfs`** — Context-aware file utilities: `Copy()`, `SmartCopy()` (hardlink with copy fallback on Linux), `CopyReaderAt()`, `pruneEmptyDirectories`, cancellable `Reader`.
- **`global`** — Compile-time constants: `UserAgent` (versioned in release), `Dev` flag, `ConnTimeout` (1 min), `Dial()` (conntrack-wrapped).
- **`global/tasks`** — Task dispatch: dev uses raw `go`, release uses `ants` goroutine pool.
- **`gslice`** — Generic `Remove[T comparable]()` preserving order.
- **`gsync`** — Channel-based `Cond` (selectable), `EmptyLock` (no-op), `AtomicUint`, generic `Map` wrapper with `Size()`, generic `Pool` wrapper.
- **`heap`** — Generic binary min-heap with `Lesser[T]` interface.
- **`mempool`** — Wrapper around `valyala/bytebufferpool`.
- **`null`** — Generic `Null[T]` with `Set` bool, type aliases for primitives, JSON/bencode marshaling.
- **`random`** — Crypto-random utilities: `Bytes()`, `URLSafeStr()`, `PrintableBytes()`.
- **`ro`** — Read-only byte view (`RO`), same size as string, `Len()/At()/Copy()/EqualString()/EqualBytes()/Less()`.
- **`sys`** — OS detection constants (`IsMacos`, `IsWindows`, `IsLinux`) and Linux `KernelVersion()`.
- **`unsafe`** — Zero-allocation `Bytes(string) []byte` / `Str([]byte) string`.

## Dependencies & Conventions
- Error wrapping: `trim21/errgo` with stack traces; see [internal/web/error.go](../internal/web/error.go) for JSON-RPC `CodeError()`.
- Metrics: `prometheus/client_golang` with GoCollector + cgo metrics; outbound HTTP uses `trim21/conntrack` for tracing.
- Resource limits: `automaxprocs` + `automemlimit` auto-tune GOMAXPROCS and GOMEMLIMIT on Linux via [main.go](../main.go).
- **Linting** ([.golangci.yaml](../.golangci.yaml)): `depguard` bans `sync/atomic` (use `uber/atomic`), `gci` formatter orders imports (std → default → localmodule), `gofmt` rewrites `interface{}` → `any`.
- Testing: use `-tags assert` for test runs; `testify` is available for assertions. Test fixtures in `testdata/` directories.
- Directory conventions: `internal/` for all non-public code, `internal/pkg/` for reusable utilities, `etc/example/` for deployment examples, `dist/` for build artifacts (gitignored).
- File naming in core: `c_*.go` for Client, `d_*.go` for Download, `p_*.go` for Peer methods.

## Version
- [internal/version](../internal/version) provides `Version` string; `-v/--version` prints `version.Print()` with ref/build date injected via `-X` ldflags at build time.
- `Ref` from `git describe --first-parent --all`, `BuildDate` in UTC ISO8601, `Revision` from `debug.ReadBuildInfo()`.
- Release builds set `MAJOR/MINOR/PATCH` via `release.go` (build tag); dev builds set all to 0.

## Additional Notes
- Keep Authorization header name as `"Authorization"`; schema enforces API key security in OpenAPI via header definition.
- Runtime directories and locks are user-writable; do not assume elevated permissions or system-level installs.
- DHT implementation is a stub ([internal/core/dht.go](../internal/core/dht.go)); MSE encryption files are `.tmp` placeholders ([internal/core/mse/](../internal/core/mse/)).
- **Python SDK** ([sdk/python/](../sdk/python/)): sync JSON-RPC client using `httpx` + `pydantic`. `NeptuneClient` wraps all RPC methods with typed request/response dataclasses in [models.py](../sdk/python/src/neptune_sdk/models.py). When adding a new RPC method, also add the corresponding client method + request/response models here. Run `uv run pytest` to test.
- **TypeScript SDK** ([sdk/typescript/](../sdk/typescript/)): async JSON-RPC client using `fetch`. `NeptuneClient.call()` provides fully type-safe method dispatch via `NeptuneMethodMap` in [client.ts](../sdk/typescript/src/client.ts). When adding a new RPC method, add its entry to `NeptuneMethodMap` and the corresponding types in [types.ts](../sdk/typescript/src/types.ts). Run `pnpm tsc --noEmit` to type-check.
