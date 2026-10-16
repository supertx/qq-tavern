# 其他兼容接口

使用 [compatible.env.example](compatible.env.example)，按 [初始化说明](README.md) 与公共模板组合为本地 `.env`。这是待填写的通用模板，不表示所有服务都已通过兼容测试。

填写服务商提供的基础地址、API Key 与模型 ID，并选择它实际支持的协议：

| MODEL_API_MODE | 程序追加的路径 |
| --- | --- |
| `responses` | `/responses` |
| `chat_completions` | `/chat/completions` |

例如，服务商给出的完整路径为 `https://example.com/v1/chat/completions`，则基础地址填写 `https://example.com/v1`。实际基础路径以服务商文档为准，不要一律添加 `/v1`。

## 需要兼容的请求行为

当前实现会发送工具定义、工具调用结果和思考强度；Chat Completions 使用 `reasoning_effort` 与 `max_completion_tokens`，Responses 使用 `reasoning.effort` 与 `max_output_tokens`。输出上限为 32768 token。

因此，只有文本补全兼容但不支持工具调用、思考强度或这些请求字段的接口，可能返回错误。程序不会自动删除不支持的字段，也不会自动回退到另一协议。原生使用其他协议的接口需要由兼容服务转换。

模板的 `medium` 是待确认的初始值；请同时调整 `REASONING_EFFORT` 与 `SUMMARY_REASONING_EFFORT`，选择实际模型支持的档位。当前程序允许 `none/minimal/low/medium/high/xhigh/max`，服务商可能只支持其中一部分。

## 验证

重建 Worker 后，在 QQ 中检查 `/当前模型`，再发送一条普通对话。查询指令只读取配置，成功显示不等于上游模型已经调用成功。真实调用问题通过 Worker 日志中的 `error_kind`、`http_status` 定位，详见 [配置参考](../../docs/CONFIGURATION.md)。

[返回模型目录](README.md) · [返回项目首页](../../README.md)
