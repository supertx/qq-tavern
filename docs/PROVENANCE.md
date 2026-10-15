# 代码来源与开源边界

记录日期：2026-10-15。此仓库是独立项目，首版以私有仓库交付；公开发布仍需完成独立测试机器人的实际验收。

## 本项目代码

新代码按本项目需求和公开协议独立编写，采用根目录的 [MIT 许可证](../LICENSE)。开发前研究过 `company_bot@a7f0100` 的行为与问题，但未复制其实现文件、测试、Git 历史、服务器配置或用户数据；该旧项目的授权情况不作为本仓库的授权依据。

| 模块 | 实现来源与边界 |
| --- | --- |
| `internal/qq`、`internal/domain` | 根据 QQ 官方消息、鉴权、连接和发送协议独立实现事件标准化、身份标识与传输 |
| `internal/eventbus` | 根据 Redis RESP／Streams 协议独立实现消息队列交互；不引入旧平台队列代码 |
| `internal/model` | 根据公开的 Responses／Chat Completions 接口协议独立实现请求、返回项及工具配对 |
| `internal/state`、`internal/tavern` | 根据本项目角色、世界书、注释、摘要、隔离和命令规格独立实现；无 SillyTavern 源码 |
| `internal/worker`、`internal/netguard` | 根据本项目整轮预算、工具权限、发送去重和外部访问要求独立实现 |
| `internal/config`、`cmd`、构建与测试文件 | 为独立部署新编写，不依赖订单、套餐、控制器、节点代理或线上配置 |
| `onboard`、`scripts/start.sh` | 为本项目独立编写终端扫码与启动流程；通过官方 SDK 公开 API 获取凭据，没有复制旧项目登录服务 |
| Go 外部模块及许可文本 | 通过 Go Modules 引入，保留原许可证；详见 [第三方声明](../THIRD_PARTY_NOTICES.md) |

本项目未将来源或授权不明的旧模块导入后重新标注为 MIT。引用公开协议与概念不代表对相关上游品牌、服务或内容拥有版权，也不代表上游认可或维护本项目。

## 概念与协议资料

酒馆式记忆的概念参考 SillyTavern 的官方文档：

- [Character Design](https://docs.sillytavern.app/usage/core-concepts/characterdesign/)：角色描述、性格与场景。
- [World Info](https://docs.sillytavern.app/usage/core-concepts/worldinfo/)：按关键词或常驻条件选择提示内容。
- [Author’s Note](https://docs.sillytavern.app/usage/core-concepts/authors-note/)：会话中的行为与场景注释。
- [Summarize](https://docs.sillytavern.app/extensions/summarize/)：与长期事实分开的聊天摘要。

本项目没有使用 SillyTavern 的浏览器脚本、插件代码、角色卡素材或文档正文，不运行 SillyTavern 服务，也不宣称完整文件格式或插件兼容。SillyTavern 自身的许可证见其 [上游 LICENSE](https://github.com/SillyTavern/SillyTavern/blob/release/LICENSE)。

协议参考：

- [QQ 机器人官方 API 文档](https://bot.q.qq.com/wiki/develop/api-v2/)与[事件订阅与通知](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/event-emit.html)。
- [OpenAI Responses 迁移说明](https://developers.openai.com/api/docs/guides/migrate-to-responses)与[函数调用说明](https://developers.openai.com/api/docs/guides/function-calling)。
- [Redis RESP 协议](https://redis.io/docs/latest/develop/reference/protocol-spec/)与 [Streams](https://redis.io/docs/latest/develop/data-types/streams/)。

模型服务、QQ 服务、搜索服务和图片／语音服务由部署者选择和配置，其服务条款不由本仓库的 MIT 许可证替代。

## 依赖与交付核对

依赖清单以 `go.mod`、`go.sum`、`go list -m all` 和各目标的 `go list -deps` 为依据。纳入本项目二进制或本项目测试的外部 Go 模块均已找到并保留许可证原文；完整模块图与许可文件摘要见 [MODULE_GRAPH.json](../third_party/licenses/MODULE_GRAPH.json)。上游工具和上游测试依赖单独列出，不误称为本项目的运行依赖。

第三方许可文件来自对应版本的 Go 模块缓存，保持原文；只提取 SQLite 的版权声明块，没有将第三方实现代码复制到自有源码。Redis Docker 镜像是单独的上游组件，不能随本项目重新声明为 MIT。

后续引入代码或依赖时，需要同步更新来源记录、版本与许可证。引入旧项目模块前必须先取得明确授权依据；角色卡、世界书和聊天记录也需要各自的来源与使用授权，不能因为机器人开源而一并公开。

## 扫码 SDK

扫码容器精确依赖 `@tencent-connect/qqbot-connector@1.0.1`。核对官方 npm 发布包内 `package.json` 的 `license: "MIT"` 与 npm 元数据，保留原始声明和包完整性值。该包未附独立 LICENSE 文件；本项目没有补造上游版权声明，另附标准 MIT 许可正文。`qrcode-terminal@0.12.0` 的 Apache-2.0 及嵌套 QRCode MIT 声明原文一并保留。

本次核对的 SDK `1.2.0` 标记为 `UNLICENSED`，因此没有纳入。版本升级必须重新核对许可，不能用本项目 MIT 许可替代上游授权。精确证据见 [npm 依赖清单](../third_party/licenses/npm/PACKAGES.json)、[锁文件](../onboard/package-lock.json)及[第三方声明](../THIRD_PARTY_NOTICES.md)。
