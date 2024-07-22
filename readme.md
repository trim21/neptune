# Neptune

A BitTorrent client

## Install

## Docker

Pre-built docker
image [`ghcr.io/trim21/neptune`](https://github.com/trim21/neptune/pkgs/container/neptune). 

Platform `linux/amd64` and `linux/arm64` are supported.

Full docker compose example can be found at [./docker-compose.yaml](./etc/example/)

## Build

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

Licensed under GPL v3.
