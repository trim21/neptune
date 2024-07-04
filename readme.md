# Tyr
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Ftrim21%2Ftyr.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2Ftrim21%2Ftyr?ref=badge_shield)


A BitTorrent client

## development

This project use [go-task](https://taskfile.dev/) to manage pre-defined scripts.

After you install go-task.

`task init` install go binary tools

`task test` run tests

`task lint` run linter

`task dev --watch` start client in watch mode, process will automatically restart if any go code
changed.

`task build` build a client in release mode.


## License
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Ftrim21%2Ftyr.svg?type=large)](https://app.fossa.com/projects/git%2Bgithub.com%2Ftrim21%2Ftyr?ref=badge_large)