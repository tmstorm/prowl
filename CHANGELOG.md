# Changelog

## [0.9.0](https://github.com/tmstorm/prowl/compare/v0.8.0...v0.9.0) (2026-11-26)


### Features

* **tui:** add a bebop theme ([#101](https://github.com/tmstorm/prowl/issues/101)) ([b280e95](https://github.com/tmstorm/prowl/commit/b280e950eb84af1bd9e213d932b86772231f53c6))


### Bug Fixes

* **review:** print the findings of a review prowl moved to your PR while it ran ([#105](https://github.com/tmstorm/prowl/issues/105)) ([1dd44b1](https://github.com/tmstorm/prowl/commit/1dd44b13730307a740d2942846821c3d63925218))
* **service:** keep the background service current across updates ([#104](https://github.com/tmstorm/prowl/issues/104)) ([b9b4f63](https://github.com/tmstorm/prowl/commit/b9b4f636427664ae3e82c5509738cfb503d813b5))
* **watch:** stop the TUI and prowl watch doing the same background work ([#103](https://github.com/tmstorm/prowl/issues/103)) ([e5e56a7](https://github.com/tmstorm/prowl/commit/e5e56a76b518574277c17fcf476fe72ac6b60069))

## [0.8.0](https://github.com/tmstorm/prowl/compare/v0.7.1...v0.8.0) (2026-11-23)


### Features

* **review:** cancel a running AI review or extension ([#100](https://github.com/tmstorm/prowl/issues/100)) ([3a71dc6](https://github.com/tmstorm/prowl/commit/3a71dc6f1d6c800388b21792c52519ca37e2e55a))


### Bug Fixes

* **slack:** stop prowl watch taking down the reviewing emoji ([#98](https://github.com/tmstorm/prowl/issues/98)) ([ebc69d0](https://github.com/tmstorm/prowl/commit/ebc69d072b98f54c3feaf4df4e3d075c2b51df18))

## [0.7.1](https://github.com/tmstorm/prowl/compare/v0.7.0...v0.7.1) (2026-11-20)


### Bug Fixes

* **slack:** put the reviewing emoji up the moment you start on a PR ([#95](https://github.com/tmstorm/prowl/issues/95)) ([9eb4d15](https://github.com/tmstorm/prowl/commit/9eb4d15d5ad2ee749ee271c561dba47a1c3ffc8a))
* **tui:** stop counting the PR under the cursor in tabs it isn't in ([#97](https://github.com/tmstorm/prowl/issues/97)) ([06bab88](https://github.com/tmstorm/prowl/commit/06bab88edb10b40d04e79275c2dc78e70f8e3430))

## [0.7.0](https://github.com/tmstorm/prowl/compare/v0.6.0...v0.7.0) (2026-11-19)


### Features

* findings for the author: work through what was raised on your own PR ([#91](https://github.com/tmstorm/prowl/issues/91)) ([728c0dc](https://github.com/tmstorm/prowl/commit/728c0dcfc736af83eff530514cd801aaf7419654))
* **list:** print what needs you as JSON with --json ([#92](https://github.com/tmstorm/prowl/issues/92)) ([5ae3de2](https://github.com/tmstorm/prowl/commit/5ae3de2ca5a888ddfe9695194c2d5826ba0a67a9))
* log what prowl does, and read it with prowl logs ([#88](https://github.com/tmstorm/prowl/issues/88)) ([b4d38a8](https://github.com/tmstorm/prowl/commit/b4d38a8ad791f99b93b86ff80373a2982c97ef64))
* performance review, the built-in perf extension ([#89](https://github.com/tmstorm/prowl/issues/89)) ([4584784](https://github.com/tmstorm/prowl/commit/458478443a7b0b33b648b2f3e0b8febbad10cfe4))
* review extensions ([#87](https://github.com/tmstorm/prowl/issues/87)) ([4a526cf](https://github.com/tmstorm/prowl/commit/4a526cf1f5472c31739475ceb3d409cb09892c23))
* set extensions and perf in the options ([#90](https://github.com/tmstorm/prowl/issues/90)) ([3811c88](https://github.com/tmstorm/prowl/commit/3811c88922016180476c41ab5baec059665e5c09))


### Bug Fixes

* **agent:** cap the run tool's caches and clean up old ones ([#85](https://github.com/tmstorm/prowl/issues/85)) ([99482dc](https://github.com/tmstorm/prowl/commit/99482dc88d1999eebc20f37b4842a53f157ef21f))
* **slack:** let you change the client ID in the options ([#86](https://github.com/tmstorm/prowl/issues/86)) ([e4c9174](https://github.com/tmstorm/prowl/commit/e4c91747722ab8e04a25a59b4c98ca3049135979))
* **update:** write the man pages on every update ([#82](https://github.com/tmstorm/prowl/issues/82)) ([f0e553d](https://github.com/tmstorm/prowl/commit/f0e553d941036f582c5580f4669d22abe18743c6))

## [0.6.0](https://github.com/tmstorm/prowl/compare/v0.5.0...v0.6.0) (2026-11-18)


### ⚠ BREAKING CHANGES

* **tui:** three keys moved. `v` now starts a line selection in a PR, so press `V` to mark a file viewed. `yy` now copies the cursor's line of a PR, so press `ys` to copy the Slack line.

### Features

* build prowl's docs into the binary ([#43](https://github.com/tmstorm/prowl/issues/43)) ([bca08b6](https://github.com/tmstorm/prowl/commit/bca08b6edf453cb44a361678e56c040544ca7edc))
* man pages for the prowl command and its config ([#77](https://github.com/tmstorm/prowl/issues/77)) ([7225556](https://github.com/tmstorm/prowl/commit/7225556fd97d7027124e6daf0c5fb768e65096da))
* open the docs at a topic with :help, K and prowl docs TOPIC ([#51](https://github.com/tmstorm/prowl/issues/51)) ([f56ad54](https://github.com/tmstorm/prowl/commit/f56ad54a07d6dbea288843281b4d718176b764b5))
* **tui:** move every menu and the docs viewer the same way ([#73](https://github.com/tmstorm/prowl/issues/73)) ([e5c10ec](https://github.com/tmstorm/prowl/commit/e5c10ec4ce22992eed3bf8a51bca9ce6941342af))
* **tui:** select and copy lines in the docs viewer ([#55](https://github.com/tmstorm/prowl/issues/55)) ([fb7f866](https://github.com/tmstorm/prowl/commit/fb7f866d3e1c2534c7e94c40b86904e6316b6553))
* **tui:** select and copy text from a PR ([c1acb95](https://github.com/tmstorm/prowl/commit/c1acb9552aa5bed7009dc2cd45289740c7d49713))


### Bug Fixes

* **agent:** count the bots setting as bots in a PR's history ([#60](https://github.com/tmstorm/prowl/issues/60)) ([fb76cb0](https://github.com/tmstorm/prowl/commit/fb76cb0edcc3de5cb551b8b9e5d34b133f6baf03))
* **agent:** let a revise or chat draft a reply on any unresolved thread ([#71](https://github.com/tmstorm/prowl/issues/71)) ([c5b2192](https://github.com/tmstorm/prowl/commit/c5b2192cd5bc1dab9b6a49fcf91c6fb22fcadea4))
* **agent:** test only what changed on a local re-review ([#45](https://github.com/tmstorm/prowl/issues/45)) ([9aefb5f](https://github.com/tmstorm/prowl/commit/9aefb5fb6bf7d89017a5003d83e7daedfab35888))
* **cli:** print a command's help for -h instead of running it ([#61](https://github.com/tmstorm/prowl/issues/61)) ([c962e90](https://github.com/tmstorm/prowl/commit/c962e902915723137d221331c85bf11831870021))
* **cli:** read every command's flags one way, and check logins in prowl watch as the TUI does ([4a47e96](https://github.com/tmstorm/prowl/commit/4a47e96d033703e9cb335e579f6089a3ca9e7306))
* **config:** refuse an unknown group_by, chat_mode or theme ([4a47e96](https://github.com/tmstorm/prowl/commit/4a47e96d033703e9cb335e579f6089a3ca9e7306))
* **gh:** post thread replies at once instead of in a pending review ([#59](https://github.com/tmstorm/prowl/issues/59)) ([28a4930](https://github.com/tmstorm/prowl/commit/28a4930ecc5134a32a4cf67c7f25c0239edd17a2))
* **inbox:** keep a PR out of re-approve right after you approve it ([#52](https://github.com/tmstorm/prowl/issues/52)) ([ed5b1af](https://github.com/tmstorm/prowl/commit/ed5b1af90e4d7ee333445686aa10b1fb1bc4ef91))
* **tui:** call stale_after's threshold due, not overdue ([#65](https://github.com/tmstorm/prowl/issues/65)) ([fc05dc4](https://github.com/tmstorm/prowl/commit/fc05dc4073387d6a1d47c31a4808d2ad7c0366ce))
* **tui:** don't crash on &lt;leader&gt;O with no PR selected ([#44](https://github.com/tmstorm/prowl/issues/44)) ([a8327e3](https://github.com/tmstorm/prowl/commit/a8327e3723af6a35fee77501e4125c587ea14f76))
* **tui:** don't offer the chat window choice on Windows ([#66](https://github.com/tmstorm/prowl/issues/66)) ([af1686f](https://github.com/tmstorm/prowl/commit/af1686ff177d4fcf2c84595f4b9e4511a314381a))
* **tui:** draw the cursor mark in the gutter, not over the first character ([#50](https://github.com/tmstorm/prowl/issues/50)) ([1969771](https://github.com/tmstorm/prowl/commit/1969771bf6570e322759723d74d1871f5e6dbc9c))
* **tui:** give editors and agent sessions the terminal, not a pipe ([#49](https://github.com/tmstorm/prowl/issues/49)) ([cb21b42](https://github.com/tmstorm/prowl/commit/cb21b42fe155add80050e44deb7985ccab4ea080))
* **tui:** keep release news up when an onboarding tip comes or goes ([d40556d](https://github.com/tmstorm/prowl/commit/d40556d4f6c49322680a54ffef18be9e1f3ad522))
* **tui:** say just now instead of now ago ([#62](https://github.com/tmstorm/prowl/issues/62)) ([3725812](https://github.com/tmstorm/prowl/commit/37258124d3152be7028eb37296bc5a2e4e61cac3))
* **tui:** say the key list shows only a few leader keys ([#54](https://github.com/tmstorm/prowl/issues/54)) ([0013a5c](https://github.com/tmstorm/prowl/commit/0013a5cfd7e83f426aea60c7f21bcebb656d7260))
* **tui:** scroll three lines on every wheel notch ([#47](https://github.com/tmstorm/prowl/issues/47)) ([2102312](https://github.com/tmstorm/prowl/commit/2102312c34dc754b6a1d2d1c78f3dd8ea0eab775))
* **tui:** show &lt; and &gt; in release notes as written ([#79](https://github.com/tmstorm/prowl/issues/79)) ([5b29893](https://github.com/tmstorm/prowl/commit/5b29893efb60b5c4ff5b936f4f64b9d8521e61fa))
* **tui:** show CONFIRM in the statusline for every yes-or-no question ([#63](https://github.com/tmstorm/prowl/issues/63)) ([04cb23d](https://github.com/tmstorm/prowl/commit/04cb23d1978568c8a758d3203c3bca5f4eb30d14))
* **tui:** show the leader you use in the reviewers and Slack names hints ([#64](https://github.com/tmstorm/prowl/issues/64)) ([c8ada3a](https://github.com/tmstorm/prowl/commit/c8ada3a164da290531eb913b49d6341083041f7c))


### Performance Improvements

* **tui:** keep a markdown renderer per width ([#67](https://github.com/tmstorm/prowl/issues/67)) ([9c19c49](https://github.com/tmstorm/prowl/commit/9c19c4922ab43c9fe6f03ca0004326a391a24d5c))

## [0.5.0](https://github.com/tmstorm/prowl/compare/v0.4.0...v0.5.0) (2026-11-17)


### Features

* **slack:** sign in to Slack from prowl's accounts ([#41](https://github.com/tmstorm/prowl/issues/41)) ([388fed1](https://github.com/tmstorm/prowl/commit/388fed11a8ecba6210655fd3656c86e1c4022731))


### Bug Fixes

* **slack:** renew the Slack token when it expires ([#39](https://github.com/tmstorm/prowl/issues/39)) ([c49c367](https://github.com/tmstorm/prowl/commit/c49c367cb91099a096044d05fd3656303e34cda0))
* **tui:** keep status messages to one line ([#42](https://github.com/tmstorm/prowl/issues/42)) ([92ed04e](https://github.com/tmstorm/prowl/commit/92ed04e5fd5b953c5fa64350e8e7f201796c7fea))

## [0.4.0](https://github.com/tmstorm/prowl/compare/v0.3.0...v0.4.0) (2026-11-17)


### Features

* **slack:** bring PRs shared in Slack into prowl ([#36](https://github.com/tmstorm/prowl/issues/36)) ([824a8c2](https://github.com/tmstorm/prowl/commit/824a8c240cb4360aaabd1bdfe2caec3caa933d47))
* **tui:** count a release's new features in the update tip ([#38](https://github.com/tmstorm/prowl/issues/38)) ([90d8040](https://github.com/tmstorm/prowl/commit/90d80400b13d06dc133104ad71b019942e0ba8d1))
* **tui:** pick reviewers from a list when asking for reviews ([#35](https://github.com/tmstorm/prowl/issues/35)) ([28a43ab](https://github.com/tmstorm/prowl/commit/28a43ab284aa74614dc20c3813b16225887a23b3))


### Bug Fixes

* **platform:** stop finished link openers and reviews lingering as zombies ([#34](https://github.com/tmstorm/prowl/issues/34)) ([668a56f](https://github.com/tmstorm/prowl/commit/668a56f05f65b3d88cf96cce30ced7fd5621dc7c))


### Performance Improvements

* **agent:** test only what changed on a re-review ([#37](https://github.com/tmstorm/prowl/issues/37)) ([f933a62](https://github.com/tmstorm/prowl/commit/f933a628c27bfc647b41e2f8c1638443c4a41d6b))
* show a posted review and a sync sooner ([#31](https://github.com/tmstorm/prowl/issues/31)) ([05a0beb](https://github.com/tmstorm/prowl/commit/05a0beb053299820e9efabd5b7b895ee5eb6e891))

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
