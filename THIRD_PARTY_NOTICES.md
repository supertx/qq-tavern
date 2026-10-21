# 第三方软件声明

核对日期：2026-10-16。本项目自有新增代码的 MIT 许可证不替代下列组件的许可证。分发本项目二进制时，请同时保留本文件和 `third_party/licenses/` 中的完整声明。

## 二进制与本项目测试使用的 Go 模块

依据 `go list -m all`、Linux amd64/arm64 的 `go list -deps ./cmd/qq-tavern`，以及 Darwin arm64 的运行／测试依赖输出核对。Linux 目标使用 11 个外部模块；Darwin 额外使用 `go-isatty`、`go-strftime`，合计 13 个。本项目的 `go list -deps -test ./...` 没有额外的第三方测试模块。

许可证标识根据对应版本的实际声明归类。目录中的文本保留原样；嵌套组件另列，不能仅凭模块根许可证概括所有内容。

| 模块 | 版本 | 许可／声明 | 原文 |
| --- | --- | --- | --- |
| `github.com/gorilla/websocket` | `v1.5.3` | BSD-2-Clause | [LICENSE](third_party/licenses/github.com__gorilla__websocket@v1.5.3/LICENSE) |
| `modernc.org/sqlite` | `v1.36.3` | Go 驱动 BSD-3-Clause；SQLite 引擎附原始版权放弃与祝福声明（SPDX: blessing） | [LICENSE](third_party/licenses/modernc.org__sqlite@v1.36.3/LICENSE)、[SQLite 声明](third_party/licenses/modernc.org__sqlite@v1.36.3/SQLITE-ENGINE-NOTICE) |
| `github.com/dustin/go-humanize` | `v1.0.1` | MIT | [LICENSE](third_party/licenses/github.com__dustin__go-humanize@v1.0.1/LICENSE) |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause | [LICENSE](third_party/licenses/github.com__google__uuid@v1.6.0/LICENSE) |
| `github.com/mattn/go-isatty` | `v0.0.20` | MIT；Darwin 依赖 | [LICENSE](third_party/licenses/github.com__mattn__go-isatty@v0.0.20/LICENSE) |
| `github.com/ncruces/go-strftime` | `v0.1.9` | MIT；Darwin 依赖 | [LICENSE](third_party/licenses/github.com__ncruces__go-strftime@v0.1.9/LICENSE) |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` | BSD-3-Clause | [LICENSE](third_party/licenses/github.com__remyoudompheng__bigfft@v0.0.0-20230129092748-24d4a6f8daec/LICENSE) |
| `golang.org/x/exp` | `v0.0.0-20230315142452-642cacee5cc0` | BSD-3-Clause | [LICENSE](third_party/licenses/golang.org__x__exp@v0.0.0-20230315142452-642cacee5cc0/LICENSE) |
| `golang.org/x/image` | `v0.25.0` | BSD-3-Clause，附专利授权 | [LICENSE](third_party/licenses/golang.org__x__image@v0.25.0/LICENSE)、[PATENTS](third_party/licenses/golang.org__x__image@v0.25.0/PATENTS) |
| `golang.org/x/sys` | `v0.31.0` | BSD-3-Clause | [LICENSE](third_party/licenses/golang.org__x__sys@v0.31.0/LICENSE) |
| `modernc.org/libc` | `v1.61.13` | 主体 BSD-3-Clause；musl、netdb 及其他声明见下文 | [LICENSE](third_party/licenses/modernc.org__libc@v1.61.13/LICENSE) |
| `modernc.org/mathutil` | `v1.7.1` | BSD-3-Clause | [LICENSE](third_party/licenses/modernc.org__mathutil@v1.7.1/LICENSE) |
| `modernc.org/memory` | `v1.8.2` | BSD-3-Clause，含 Go、mmap-go 来源声明 | [LICENSE](third_party/licenses/modernc.org__memory@v1.8.2/LICENSE) |

额外保留的嵌套声明：

- `modernc.org/libc`：[Go 来源声明](third_party/licenses/modernc.org__libc@v1.61.13/LICENSE-GO)、[musl 完整版权及许可说明](third_party/licenses/modernc.org__libc@v1.61.13/COPYRIGHT-MUSL)、[honnef.co/go/netdb 的 MIT 许可证](third_party/licenses/modernc.org__libc@v1.61.13/honnef.co__go__netdb__LICENSE)。musl 主体为 MIT，原文还列出其第三方来源与许可说明。
- `modernc.org/memory`：[Go 来源声明](third_party/licenses/modernc.org__memory@v1.8.2/LICENSE-GO)、[mmap-go 的 BSD-3-Clause 声明](third_party/licenses/modernc.org__memory@v1.8.2/LICENSE-MMAP-GO)。
- Go 标准库及运行时：[BSD-3-Clause 许可证](third_party/licenses/go/LICENSE)与[专利授权](third_party/licenses/go/PATENTS)，从核对时的 Go 1.27.1 工具链保留。具体发布二进制的 Go 版本以 `go version -m` 为准。

SQLite 声明仅提取对应模块生成文件中的完整许可注释块，没有将该文件实现复制进本项目源码。其余模块许可文件均直接来自对应版本的模块缓存。文件来源路径及 SHA-256 摘要记录在 [MODULE_GRAPH.json](third_party/licenses/MODULE_GRAPH.json)。

## 模块图中的上游工具／测试依赖

以下 20 个模块出现在 `go list -m all` 的模块图中，但不在上述目标的本项目运行或本项目测试包依赖集中。本项目没有复制、编译或分发它们的源码；列出版本用于说明模块图与实际链接集合的区别，不据此对其许可作未经核对的推断。

| 模块 | 版本 |
| --- | --- |
| `github.com/chzyer/readline` | `v1.5.1` |
| `github.com/google/go-cmp` | `v0.5.8` |
| `github.com/google/pprof` | `v0.0.0-20250317173921-a4b03ec1a45e` |
| `github.com/ianlancetaylor/demangle` | `v0.0.0-20240312041847-bd984b5ce465` |
| `github.com/kballard/go-shellquote` | `v0.0.0-20180428030007-95032a82bc51` |
| `golang.org/x/mod` | `v0.19.0` |
| `golang.org/x/sync` | `v0.7.0` |
| `golang.org/x/text` | `v0.23.0` |
| `golang.org/x/tools` | `v0.23.0` |
| `lukechampine.com/uint128` | `v1.2.0` |
| `modernc.org/cc/v3` | `v3.41.0` |
| `modernc.org/cc/v4` | `v4.24.4` |
| `modernc.org/ccgo/v3` | `v3.17.0` |
| `modernc.org/ccgo/v4` | `v4.23.16` |
| `modernc.org/fileutil` | `v1.3.0` |
| `modernc.org/gc/v2` | `v2.6.3` |
| `modernc.org/opt` | `v0.1.4` |
| `modernc.org/sortutil` | `v1.2.1` |
| `modernc.org/strutil` | `v1.2.1` |
| `modernc.org/token` | `v1.1.0` |

若后续添加测试、生成器、目标平台或工具导致它们进入本项目实际分发集合，应重新读取相应版本的许可证并补充原文，不能沿用“未纳入”结论。

## 扫码容器的 npm 依赖

仅登录辅助容器运行这些依赖，Go Gateway／Worker 不加载它们。版本、下载来源、npm 完整性值及许可证据摘要见 [PACKAGES.json](third_party/licenses/npm/PACKAGES.json)，实际安装由 [package-lock.json](onboard/package-lock.json) 固定，禁用 npm 生命周期脚本。

| 包 | 版本 | 许可与保留内容 |
| --- | --- | --- |
| `@tencent-connect/qqbot-connector` | `1.0.1` | 官方发布包的 [package.json](third_party/licenses/npm/@tencent-connect__qqbot-connector@1.0.1/package.json) 明确声明 MIT；包内未附独立 LICENSE 或版权人文本，另保留[标准 MIT 许可正文](third_party/licenses/npm/MIT-permission.txt)，不虚构版权声明 |
| `qrcode-terminal` | `0.12.0` | [Apache-2.0 完整许可及 QRCode for JavaScript 的 MIT 版权声明](third_party/licenses/npm/qrcode-terminal@0.12.0/LICENSE)，QRCode 作者为 Kazuhiko Arase；MIT 许可正文见上方链接 |

SDK 包来源：[官方 npm 包](https://www.npmjs.com/package/@tencent-connect/qqbot-connector/v/1.0.1)。本次核对的新版 `1.2.0` 声明 `UNLICENSED`，本项目不打包该版本。扫码 SDK 是上游发布的构建产物，保持原样通过 npm 引入，没有复制其实现到本项目自有源码。

## 容器和外部组件

- Compose 中的 `redis:7.4-alpine` 是单独拉取和运行的上游镜像。Redis 7.4 使用 RSALv2 或 SSPLv1，详见 [Redis 官方许可证说明](https://redis.io/legal/licenses/)与 [7.4 版本完整许可](https://github.com/redis/redis/blob/7.4/LICENSE.txt)。本项目 MIT 许可不覆盖 Redis，也不将 Redis 7.4 描述为 BSD 版本。
- 构建镜像 `golang:1.27-alpine` 属于上游工具链和 Alpine 发行版，不作为本项目源码重新许可。最终应用镜像使用 `scratch`，只带应用、证书、数据目录和许可材料。
- 从 Alpine 复制的 Mozilla CA 证书数据沿用 MPL-2.0，已保留 [MPL-2.0 全文](third_party/licenses/mozilla/MPL-2.0.txt)，原文来自 [Mozilla 官方许可文本](https://www.mozilla.org/media/MPL/2.0/index.txt)。Alpine 的 [ca-certificates-bundle 包信息](https://pkgs.alpinelinux.org/package/edge/main/x86_64/ca-certificates-bundle)将包整体标记为 MPL-2.0 AND MIT；本项目最终镜像只复制 `/etc/ssl/certs/ca-certificates.crt`，没有复制证书更新工具。实际构建使用的包版本记录在镜像内 `/licenses/build-packages.txt`。证书数据及构建来源可通过 [Alpine ca-certificates 打包仓库](https://gitlab.alpinelinux.org/alpine/aports/-/tree/master/main/ca-certificates)及其中标明的 Mozilla 来源取得。
- 登录镜像基于 `node:22-alpine`，Node.js、其捆绑组件和 Alpine 包保留上游许可；镜像内另保存 Node.js 对应版本的完整 LICENSE。
- QQ、模型、SearXNG、图片和语音服务是外部服务或单独部署的软件，其许可及服务条款由对应上游提供。配置接口地址不代表本项目包含这些服务的源码。

## 更新方法

```sh
go list -m all
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go list -deps ./cmd/qq-tavern
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go list -deps ./cmd/qq-tavern
go list -deps -test ./...
```

更新依赖后，核对完整模块图与实际包集合；将纳入组件的原始 LICENSE、NOTICE、COPYRIGHT 等声明保存到对应版本目录，更新 SHA-256 和本文件。发布包和镜像应一起携带这些声明。
