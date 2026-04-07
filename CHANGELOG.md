# Changelog

## [0.6.0](https://github.com/IceRhymers/databricks-codex/compare/v0.5.0...v0.6.0) (2026-04-07)


### Features

* dispatch Homebrew formula update on release ([25494d1](https://github.com/IceRhymers/databricks-codex/commit/25494d17425f77db32db00fbff232b4f1a772b65))
* dispatch Homebrew formula update on release ([ae8186e](https://github.com/IceRhymers/databricks-codex/commit/ae8186ee0f71a2345677573cea130ad393944c76))


### Bug Fixes

* correct YAML syntax in release.yml ([1eb76e6](https://github.com/IceRhymers/databricks-codex/commit/1eb76e6dbf31556db88772b2fe24b9f0ce0c5b1b))
* correct YAML syntax in release.yml (missing newline before update-homebrew job) ([35c1db0](https://github.com/IceRhymers/databricks-codex/commit/35c1db011baa02d426a93f76636bcda7ad2331b5))

## [0.5.0](https://github.com/IceRhymers/databricks-codex/compare/v0.4.2...v0.5.0) (2026-04-07)


### Features

* add --headless flag for proxy-only startup ([0680bac](https://github.com/IceRhymers/databricks-codex/commit/0680bace3712513b802f3fe1340060ca4897182a)), closes [#25](https://github.com/IceRhymers/databricks-codex/issues/25)


### Bug Fixes

* replace filelock with sync.Mutex, delete lock.go ([1cb1c81](https://github.com/IceRhymers/databricks-codex/commit/1cb1c8178f76f45f535d27476dedc955fea70f14))
* use api_key in config.toml so headless mode works without env vars ([1a4b793](https://github.com/IceRhymers/databricks-codex/commit/1a4b793789e86c8fe303df31d3a42c3c87f9900c))
* use api_key in config.toml so headless mode works without env vars ([2026cc9](https://github.com/IceRhymers/databricks-codex/commit/2026cc99a65a43ffcc6a7ade401861c0330d8c64))
