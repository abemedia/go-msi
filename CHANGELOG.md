# Changelog

## [0.1.0](https://github.com/abemedia/go-msi/compare/v0.0.1...v0.1.0) (2026-11-08)


### Features

* add blob store ([#7](https://github.com/abemedia/go-msi/issues/7)) ([c05aeb9](https://github.com/abemedia/go-msi/commit/c05aeb9aa7450ae84c4eae6ed8c2eff326ec4aef))
* add Microsoft Installer Database implementation ([#5](https://github.com/abemedia/go-msi/issues/5)) ([503db80](https://github.com/abemedia/go-msi/commit/503db809444da246a9480290597994895a5376fc))
* add OLEPS property set reader and writer ([#2](https://github.com/abemedia/go-msi/issues/2)) ([1abb4a0](https://github.com/abemedia/go-msi/commit/1abb4a04705d84aebc90871e9c85c596b315cfe9))
* add streamname codec for MSI table and stream names ([#3](https://github.com/abemedia/go-msi/issues/3)) ([58c5d75](https://github.com/abemedia/go-msi/commit/58c5d7537750000361ff5c5831071ff766280a35))
* add string pool codec ([#4](https://github.com/abemedia/go-msi/issues/4)) ([478bb89](https://github.com/abemedia/go-msi/commit/478bb89f1676ce5fdf3990e256890572dfd219c0))
* **sql:** add MSI SQL scanner and parser ([#10](https://github.com/abemedia/go-msi/issues/10)) ([5dcc355](https://github.com/abemedia/go-msi/commit/5dcc3553b5f9d81feb6689c1059982e6a0bf3f2e))
* **sql:** parse multi-table UPDATE with qualified column names ([#11](https://github.com/abemedia/go-msi/issues/11)) ([dbd8ee7](https://github.com/abemedia/go-msi/commit/dbd8ee7f5f83e864d1878db92f6b741e8d589212))
* **streamname:** add EncodedLen to measure encoded name length without allocating ([#13](https://github.com/abemedia/go-msi/issues/13)) ([79c723c](https://github.com/abemedia/go-msi/commit/79c723ca72f6f1e6b4ca6fae9510e47a0bcb1887))
* **stringpool:** add persistent refcount tracking, move in encoding ([#9](https://github.com/abemedia/go-msi/issues/9)) ([d449d80](https://github.com/abemedia/go-msi/commit/d449d807af22af44e6f39306ac6042b034db668d))


### Bug Fixes

* **blobstore:** fail reads of a deleted blob instead of returning reused extents ([#17](https://github.com/abemedia/go-msi/issues/17)) ([ab03908](https://github.com/abemedia/go-msi/commit/ab03908bb0a052b1ea3328fa3b3ef32fe0de51b6))
* **sql:** drop the comma before PRIMARY KEY and accept qualified INSERT columns ([#14](https://github.com/abemedia/go-msi/issues/14)) ([f362fb7](https://github.com/abemedia/go-msi/commit/f362fb795a6228b4f4941fde51d2531cbb7478ed))
* **sql:** match msi.dll's grammar in the scanner, parser and syntax tree ([#22](https://github.com/abemedia/go-msi/issues/22)) ([5b771b1](https://github.com/abemedia/go-msi/commit/5b771b1abc4f0199d02f87815da438dbfc5900f9))
* **sql:** reject empty quoted identifiers ([#21](https://github.com/abemedia/go-msi/issues/21)) ([6cade2d](https://github.com/abemedia/go-msi/commit/6cade2df2051cfb65d6e6a47ea5f56f5b2f0f8b7))
* **stringpool:** treat empty strings as free slots and reject duplicates ([#20](https://github.com/abemedia/go-msi/issues/20)) ([6b11820](https://github.com/abemedia/go-msi/commit/6b118201c51a047a6531c2909b72079036046413))
