# Neptune

A headless BitTorrent client.

## Install

Only 64-bit systems are supported.

### Pre-built Binary

Pre-built static binaries are available in [GitHub Releases](https://github.com/trim21/neptune/releases).

Pre-built static binaries have zero system library dependency and do not require glibc. Minimum OS version requirements by the Go toolchain:

- **Linux**: kernel >= 3.1
- **Windows**: Windows 10 / Windows Server 2016 or higher
- **macOS**: macOS 12 Monterey or newer

### Docker

Pre-built Docker image: [`ghcr.io/trim21/neptune`](https://github.com/trim21/neptune/pkgs/container/neptune)

Platforms: `linux/amd64`, `linux/arm64`.

#### Persistence

Neptune stores data in two directories:

| Path | Purpose | Persist? |
|---|---|---|
| Session path (default `~/.neptune`) | Torrent files, resume data, logs, config | **Yes** — must be persisted |
| Download dir (default `~/downloads`) | Downloaded files (root of per-torrent data dirs) | **Yes** — your downloaded data |

The session directory contains these sub-directories:

| Sub-directory | Content |
|---|---|
| `torrents/` | Saved `.torrent` files (hash-sharded: `{ih[:2]}/{ih[2:4]}/{hash}.torrent`) |
| `resume/` | Resume state for each torrent (`{ih[:2]}/{hash}.resume`) |
| `logs/` | Application logs (rotated: 10MB / 3 backups / 28 days) |
| `config.toml` | Config file (optional, defaults used if absent) |
| `.lock` | Flock-based lock file to prevent concurrent instances |

#### Docker Compose Example

```yaml
services:
  neptune:
    image: ghcr.io/trim21/neptune:master
    init: true
    environment:
      NEPTUNE_WEB_SECRET_TOKEN: "a-secret-token-change-me"
      NEPTUNE_SESSION_PATH:    "/var/lib/neptune"
      NEPTUNE_CONFIG_FILE:     "/var/lib/config/neptune.toml"
      # Optional overrides (all have sensible defaults):
      # NEPTUNE_WEB:             "0.0.0.0:8002"
      # NEPTUNE_P2P_PORT:        "50047"
      # NEPTUNE_LOG_LEVEL:       "info"
      # NEPTUNE_LOG_JSON:        "false"
      # NEPTUNE_LOG_SAVE_TO_FILE:"true"
      # NEPTUNE_DEBUG:           "false"
    network_mode: host # required for P2P connectivity
    healthcheck:
      test: [
        "CMD",
        "/usr/local/bin/wget",
        "--spider",
        "--no-verbose",
        "http://127.0.0.1:8002/healthz",
      ]
      interval: 10s
      timeout: 3s
      start_period: 10s
    volumes:
      # Session data (torrents, resume, logs) — persist this
      - ./data/neptune/:/var/lib/neptune/

      # Downloaded files — persist this
      - ./data/downloads/:/downloads/

      # Config file — mount read-only
      - ./config.toml:/var/lib/config/neptune.toml:ro

  flood:
    image: ghcr.io/trim21/flood:neptune
    network_mode: host
    command:
      - --port=4008
      - --noauth
      - --neptune-url=http://127.0.0.1:8002/json_rpc
      - --neptune-token=a-secret-token-change-me
    volumes:
      - ./data/neptune/:/var/lib/neptune/
      - ./data/downloads/:/downloads/
```

Key points for deployment:

1. **Network mode** must be `host` — Neptune needs direct access to all ports for P2P communication.
2. **Web secret token** (`NEPTUNE_WEB_SECRET_TOKEN`) secures the JSON-RPC API. Set it to a strong random value and keep it consistent across restarts. If left empty, a new token is generated on every startup.
3. **Volume mounts** — at minimum, persist the session directory and your downloads directory. The config file is optional but recommended.
4. **The Flood volume mounts** must mirror Neptune's so Flood can read torrent files for the Web UI.

#### Config File

Create a `config.toml` next to your compose file:

```toml
[application]
download-dir = "/downloads"
p2p-port = 50047
fallocate = true
# optional overrides:
# max-http-parallel = 100
# num-want = 50
# global-connections-limit = 50
# global-upload-slots = 64
# global-download-speed-limit = 0  # bytes/sec, 0 = unlimited
# global-upload-speed-limit = 0    # bytes/sec, 0 = unlimited
```

All config keys are optional:

| Key | Default | Description |
|---|---|---|
| `download-dir` | `~/downloads` | Root directory for downloaded files |
| `max-http-parallel` | `100` | Max concurrent HTTP tracker announce requests |
| `p2p-port` | `50047` | P2P listen port (also overridable via `NEPTUNE_P2P_PORT`) |
| `num-want` | `50` | Number of peers to request from tracker |
| `global-connections-limit` | `50` | Hard limit on total P2P connections |
| `global-upload-slots` | `max(4×conn-limit, 64)` | Hard limit on upload slots across all torrents |
| `global-download-speed-limit` | `0` (unlimited) | Download speed limit in bytes/sec |
| `global-upload-speed-limit` | `0` (unlimited) | Upload speed limit in bytes/sec |
| `fallocate` | `false` | Pre-allocate file space on disk |

### Build from Source

Install Go >= 1.25 and [go-task](https://taskfile.dev/).

```sh
git clone https://github.com/trim21/neptune.git
cd neptune

# Build for your platform (release mode):
task release:linux:amd64   # or release:linux:arm64, release:darwin:amd64, etc.
```

Available build targets:

- `release:linux:amd64`
- `release:linux:arm64`
- `release:darwin:amd64`
- `release:darwin:arm64`
- `release:windows:amd64`
- `release:windows:arm64`

## Usage

Run `./neptune --help` to see all flags.

| Flag | Default | Env | Description |
|---|---|---|---|
| `--session-path` | `~/.neptune` | `NEPTUNE_SESSION_PATH` | Session data directory |
| `--config-file` | `{session}/config.toml` | `NEPTUNE_CONFIG_FILE` | Path to config file |
| `--web` | `127.0.0.1:8002` | `NEPTUNE_WEB` | HTTP listen address |
| `--web-secret-token` | auto-generated | `NEPTUNE_WEB_SECRET_TOKEN` | API auth token (32 chars) |
| `--p2p-port` | `50047` | `NEPTUNE_P2P_PORT` | P2P listen port (overrides config) |
| `--log-json` | `false` | `NEPTUNE_LOG_JSON` | Log as JSON |
| `--log-level` | `info` | `NEPTUNE_LOG_LEVEL` | trace/debug/info/warn/error |
| `--log-save-to-file` | `true` | `NEPTUNE_LOG_SAVE_TO_FILE` | Write logs to file |
| `--debug` | `false` | `NEPTUNE_DEBUG` | Enable debug mode (pprof) |

## Development

```sh
task lint          # run linter
task test          # run tests
task gen           # run code generation (stringer, protobuf)
task dev --watch   # build and auto-restart on code changes
task release       # build release binaries for all platforms
task --list-all    # list all available tasks
```

## License

This project is mixed licensed. Most code is GPL v3. Some files are derived from other projects and retain their original licenses:

- Files copied from [anacrolix/torrent](https://github.com/anacrolix/torrent): **MPL-2.0**
- Files in `internal/web/jsonrpc/` derived from [swaggest/jsonrpc](https://github.com/swaggest/jsonrpc): **MIT**

License information is noted in each file header.
