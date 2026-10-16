# OpenAI 接入

使用 [openai.env.example](openai.env.example)，按 [初始化说明](README.md) 与公共模板组合为本地 `.env`。

模板使用 `https://api.openai.com/v1` 与 `responses` 协议。填写你自己的 API Key 和账号可用的模型 ID；模型需要支持 Responses 和函数工具调用。接口基础地址不应带 `/responses` 后缀。

聊天与摘要的模板档位均为 `medium`。支持的思考档位因模型而异，请按所选模型文档调整。OpenAI 的推理模型文档将模型支持能力与档位分开说明，不能把某一模型的设置套用到所有模型。[官方推理模型文档](https://developers.openai.com/api/docs/guides/reasoning)

`SUMMARY_MODEL` 留空时沿用聊天模型。需要独立摘要模型时，填写同一账号、同一接口可调用的模型 ID，并同步确认 `SUMMARY_REASONING_EFFORT`。

保存后按 [已有部署的生效步骤](README.md#已有部署换模型或服务) 重新创建 Worker，再在 QQ 中输入 `/当前模型`。

常见问题：

- `401/403`：检查 API Key 与模型访问权限。
- `404/405`：检查基础地址和调用协议。
- `400`：检查模型是否支持工具调用、请求参数与选择的思考档位。

[返回模型目录](README.md) · [返回项目首页](../../README.md)
