# 智谱 GLM 接入

使用 [zhipu.env.example](zhipu.env.example)，按 [初始化说明](README.md) 与公共模板组合为本地 `.env`。

模板针对智谱标准模型 API：

- 基础地址：`https://open.bigmodel.cn/api/paas/v4`。
- 调用协议：`chat_completions`。
- 聊天模型示例：`glm-5.3-flash`。
- 聊天与摘要思考档位：`high`。
- 摘要模型默认留空，沿用聊天模型。

填写智谱 API Key，并确认账号开通了模板中的模型。程序会在基础地址后追加 `/chat/completions`；不要重复填写完整接口路径，也不要套用其他服务的 `/v1` 路径。[智谱官方对话接口](https://docs.bigmodel.cn/api-reference/模型-api/对话补全)

GLM-5.3 与 GLM-5.3-Flash 的官方模型说明列出 `low/high/max` 三个思考档位。模板显式使用 `high`；若换成其他 GLM 型号，请重新核对该型号支持的参数，聊天和摘要分别配置。[GLM 官方模型说明](https://github.com/zai-org/GLM-5/blob/main/README.md)

如需用 `glm-5.3` 处理摘要，可在本地配置中把 `SUMMARY_MODEL` 设为 `glm-5.3`，并保留它支持的摘要档位。摘要仍使用同一个基础地址和密钥。

其他产品、套餐或代理提供的接入地址与权限请以对应服务说明为准；本模板不替代它们的专用配置。

保存后按 [已有部署的生效步骤](README.md#已有部署换模型或服务) 重新创建 Worker。遇到 `400` 优先核对模型与档位，`401/403` 检查鉴权和权限，`404/405` 检查地址和协议。

[返回模型目录](README.md) · [返回项目首页](../../README.md)
