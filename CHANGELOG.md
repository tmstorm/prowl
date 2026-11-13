# Changelog

## [0.3.0](https://github.com/tmstorm/prowl/compare/v0.2.0...v0.3.0) (2026-11-13)


### ⚠ BREAKING CHANGES

* **agent:** `local_commands` is removed. To let reviews run tests, set an image under `[agent.run_images]`, e.g. `"*" = "golang:1.27"`. This needs Docker.

### Features

* **agent:** let review agents run tests in a container ([cc1a6de](https://github.com/tmstorm/prowl/commit/cc1a6dee93f040e7d8f2c2c99e2671f5d209b43e))
* **tui:** flag breaking releases and show update news in pink ([c870017](https://github.com/tmstorm/prowl/commit/c870017eb2ee10de3455faa79642b98f04743b83))


### Bug Fixes

* **core:** approve the head when an agent draft predates a push ([#25](https://github.com/tmstorm/prowl/issues/25)) ([e43ad14](https://github.com/tmstorm/prowl/commit/e43ad140b8ccc5a0211eb8d84dec8cad5c0e1a90))
* **platform:** stop console windows flashing on Windows while reviews run ([#24](https://github.com/tmstorm/prowl/issues/24)) ([06b75ae](https://github.com/tmstorm/prowl/commit/06b75ae2c0cf66224a65e3a2dec7bf111f70ae3c))
* **tui:** list a push's commits newest first ([#29](https://github.com/tmstorm/prowl/issues/29)) ([6d7e585](https://github.com/tmstorm/prowl/commit/6d7e585eb429e8781741c094691b8790c5c0e9c5))

## [0.2.0](https://github.com/tmstorm/prowl/compare/v0.1.0...v0.2.0) (2026-11-13)


### Features

* **install:** check dependencies first and offer a Nerd Font ([#14](https://github.com/tmstorm/prowl/issues/14)) ([45c01ad](https://github.com/tmstorm/prowl/commit/45c01adc30a8e7f78184eed85500ee620de74ddb))
* prowl uninstall removes what prowl put on the machine ([#18](https://github.com/tmstorm/prowl/issues/18)) ([1c8376a](https://github.com/tmstorm/prowl/commit/1c8376a159293b18d543fe2e96989b74da17020d))


### Bug Fixes

* delete review refs when a PR closes and prune orphaned ones ([#20](https://github.com/tmstorm/prowl/issues/20)) ([5a43f0c](https://github.com/tmstorm/prowl/commit/5a43f0c4f4b31e3a595b3b4c40a7c94f44229a59))
* **service:** start prowl watch at login on Windows without admin ([#19](https://github.com/tmstorm/prowl/issues/19)) ([28359c8](https://github.com/tmstorm/prowl/commit/28359c8d25d67705ad0969ba802d9c3f5021a617))
* show the update settings and give prowl's tips their own place ([#15](https://github.com/tmstorm/prowl/issues/15)) ([3c264fe](https://github.com/tmstorm/prowl/commit/3c264feadd757bdf435232f44c4b2301acd6252a))

## 0.1.0 (2026-11-12)


### Features

* prowl ([89eb125](https://github.com/tmstorm/prowl/commit/89eb1257ff1a163aba69a57dd4c98481803255f6))
* prowl update, update checks and signed releases ([#7](https://github.com/tmstorm/prowl/issues/7)) ([8901131](https://github.com/tmstorm/prowl/commit/890113198db2dda2584d5c14c03d9cf12284ba07))


### Bug Fixes

* **findings:** print each reviewer's note and handle --help ([#8](https://github.com/tmstorm/prowl/issues/8)) ([7a3f052](https://github.com/tmstorm/prowl/commit/7a3f05257a2cbca705b09f8aa9ba7888d9fb0ced))
* keep hand-written release notes and tell agents a note isn't a finding ([#9](https://github.com/tmstorm/prowl/issues/9)) ([30aae2d](https://github.com/tmstorm/prowl/commit/30aae2da2d87bdebe5d273fb5037b6db0774515a))
