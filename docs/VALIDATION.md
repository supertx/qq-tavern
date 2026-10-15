# 首版验收记录

日期：2026-10-15（Asia/Shanghai）。版本：0.1.0-dev。

## 已执行

- `go test -race -json -count=1 -timeout=180s ./...`：72 项测试／子测试通过，0 跳过；包含真实临时 Redis、SQLite 和模拟 QQ HTTP/WebSocket／Responses 服务。
- `go vet ./...`：通过。
- `gofmt -l cmd internal tests`：无待格式化文件。
- `docker compose config --quiet`：使用明确的虚构测试配置通过，不接入真实服务。
- `docker build -t qq-tavern:0.1.0-dev .`：通过，Linux 容器二进制可运行。
- 镜像 `version` 返回 `0.1.0-dev`；无订单平台配置的 `check` 返回 `configuration valid for worker`。
- 跟踪文件凭据模式检查、纳入模块的许可文件 SHA-256 核对：通过，脚本为 `scripts/check_release.py`。

模拟端到端链路覆盖：QQ WebSocket 事件进入 Gateway、独立 Gateway／Worker 队列实例、Redis 路由、群记忆保存、另一成员读取、Responses 工具调用与加密条目回放、QQ HTTP 回复、私聊、跨群和个人记忆隔离、重复事件抑制。

边界回归覆盖：CAS 并发修改、重启、撤销权限、观察／回复同一源消息去重、摘要检查点及失败保留、整轮上下文裁剪、模型切换、三档思考强度、拒答和不完整工具参数、发送时间预留、403 请求去重、SSRF、媒体大小限制，以及媒体发送结果不确定后的失败提示抑制。

## 验收边界

尚无独立测试 QQ 机器人，按本次安排只执行本地与模拟验收。真实 QQ 权限、实际模型供应商兼容性、真实图片／语音质量和公网部署效果尚未验证。

本项目保持私有开发版。未部署到现有服务器，未迁移生产记录，未替换已有机器人。未来公开稳定版前需补真实机器人验收，并核对实际发布提交的 CI 结果。
