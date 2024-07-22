# Neptune

A headless BitTorrent client focus on performance.

## Install

Only 64bit system are supported.

### Pre-build Binary

There will be pre-built static binary in GitHub release when first version released.

Pre-built static binaries have zero system library dependency and does not require glibc.

But there are still some there are still minimal OS version requirements by golang toolchain.

Linux: kernel >= v3.1, no glibc requirements.

Windows: Windows 10 and higher or Windows Server 2016 and higher.

MacOS: Catalina 10.15 or newer.

### Docker

pre-built docker
image [`ghcr.io/trim21/neptune`](https://github.com/trim21/neptune/pkgs/container/neptune).

Platform `linux/amd64` and `linux/arm64` are supported.

Full docker compose example can be found at [./docker-compose.yaml](./etc/example/)

### Build

At first, you need to install go>=1.22 from <https://go.dev/> and go-task
from https://taskfile.dev/

Then clone this repo, use task to build release binary.

task support these targets:

- release:windows:arm64
- release:windows:amd64
- release:linux:amd64
- release:linux:arm64
- release:darwin:arm64
- release:darwin:amd64

for example, for linux system running on amd64, use `task release:linux:amd64` to build.

## Development

This project use [go-task](https://taskfile.dev/) to manage pre-defined scripts.

After you install go-task, use `task --list-all` to see all tasks.

for example:

`task lint` run linter
`task test` run tests

`task dev --watch` start client in watch mode, process will automatically restart if any go code
changed.

`task release` build a client in release mode.

## License

This project is mixed licensed.

Most code are licensed under GPL v3,
but some code are copied from [anacrolix/torrent](https://github.com/anacrolix/torrent), these
files are licensed under MPL-2.0.

There are also some files in internal/web/jsonrpc are copied
from <https://github.com/swaggest/jsonrpc>, these files are licensed under MIT.

You will find license in each file header.
