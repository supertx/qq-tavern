# 实例管理 API v1

本接口供部署者远程配置角色、世界书、状态定义和 QQ 命令权限。每个实例连接一个 QQ 机器人，使用独立的数据库、实例 ID 和管理令牌。

## 启用与鉴权

在该实例 `.env` 配置独立的 `ADMIN_API_TOKEN`，至少 32 字符，建议用密码学随机源生成 64 位十六进制串。它必须不同于 `INTERNAL_API_TOKEN`，也不能与其他实例共用。留空则接口关闭；过短或与内部令牌相同会使 Worker 配置检查失败。修改后重建 Worker，单纯 restart 不更新环境变量。

默认地址 `http://127.0.0.1:18710/api/admin/v1`，所有请求携带：

```http
Authorization: Bearer <该实例的 ADMIN_API_TOKEN>
Content-Type: application/json
```

JSON 正文最大 8 MiB，采用 UTF-8，拒绝未知字段、重复键和 `null`。没有正文的 GET、确认或取消请求不要求 Content-Type。响应不缓存。远程调用经受控的 HTTPS 反向代理或私有网络转发到 Worker，默认 Compose 端口仍仅绑定本机；不要把令牌放在 URL 中。

管理令牌具有当前实例的配置权限，应保存在受控环境中，不要嵌入公开网页、客户端代码或共享配置。调用多个实例时，为各实例分别配置地址与令牌；接口仅操作当前实例的数据，不提供宿主机操作。

## 标识、会话与版本

`GET /instance` 返回：

```json
{"instance_id":"持久化的 UUID","bot_id":"当前机器人身份的内部哈希","schema_version":4}
```

`instance_id` 首次迁移生成，重启保持。`bot_id` 不是 QQ AppID。所有会话、角色接口只允许操作当前 Worker 配置的机器人；数据库中旧机器人的数据不通过该接口暴露。

先在目标群 @机器人发送 `/会话信息`，或私聊发送此命令，使会话建立并获得会话 ID 与角色 ID。这是始终可用的只读命令。`GET /scopes` 返回已建立的会话：

```json
{
  "scopes": [
    {"id":"会话哈希","kind":"group","active_role_id":1,"active_role":"默认角色","version":0}
  ]
}
```

`scope` 必须使用 API／QQ 返回的 ID，不是 QQ 群号或成员 QQ 号。不能通过 API 预造任意 QQ 会话。

配置、状态定义、权限和会话角色选择**各自有独立版本**。修改时把对应 GET 返回的 `version` 放入 `expected_version`；成功后版本递增。冲突返回 `409`，应重新读取并让操作者核对，不能盲目覆盖。创建角色不需要版本，重名返回 `409`。导入则把所有目标版本保存在预览中，确认时一起校验。

## 命令组权限

`GET /command-policy` 返回 `groups`、`version` 和 `descriptions`；`PUT /command-policy` 接受部分开关，省略的组保持原状：

```json
{"expected_version":0,"groups":{"role_edit":true,"world_edit":true,"role_switch":true}}
```

| ID | 名称 | QQ 手动修改范围 |
| --- | --- | --- |
| `role_edit` | 角色编辑 | 新建角色、修改或清空人设 |
| `role_switch` | 角色切换 | 切换当前会话角色 |
| `world_edit` | 世界与公共设定 | 世界书、预算、群规则、公共记忆 |
| `personal_memory` | 个人记忆 | 设置、追加、清空自己的记忆 |
| `status_manage` | 状态管理 | 状态项定义、手动设置或重置状态 |
| `context_manage` | 上下文管理 | 手动更新、设置或清空摘要、重置上下文 |

新部署和升级后全部默认关闭。开关保存在 SQLite，重启保留。“全局”仅指**当前实例所有群与私聊**，开放后对这些会话中的所有成员生效，无成员白名单。其他实例不受影响。

普通聊天、只读查询、模型自动状态更新、后台自动摘要继续运行。自然语言记忆按目标类型判断权限；撤销按恢复的实际内容判断；多组导入必须全部获准。保存事务会再次检查权限，已排队的手动摘要、待确认文件不能绕过后来关闭的权限。管理 API 使用独立的 `admin-api` 审计身份，直接配置不受 QQ 开关限制，也不冒充 QQ 成员。

## 角色和状态定义

以下路径均以 `/api/admin/v1` 为前缀：

| 方法与路径 | 行为／请求 |
| --- | --- |
| `GET /scopes/{scope}/roles` | `{"roles":[{"id":1,"name":"默认角色","active":true}]}` |
| `POST /scopes/{scope}/roles` | `{"name":"星灯"}`，返回 201 和角色；不自动切换 |
| `GET /scopes/{scope}/roles/{role}/config` | 读取指定角色配置和版本 |
| `PATCH /scopes/{scope}/roles/{role}/config` | `expected_version` 加明确提供的配置字段 |
| `PUT /scopes/{scope}/active-role` | `{"expected_version":0,"role_id":2}`；版本来自 `/scopes`，返回新角色 ID 和新版本 |
| `GET /scopes/{scope}/roles/{role}/status-schema` | 读取 `definitions` 和 `version` |
| `PUT /scopes/{scope}/roles/{role}/status-schema` | `expected_version` 加完整 `definitions` 数组 |

配置 GET 的返回为已有存储格式：`profile_name`、`character`、`note`、`entries`、`budget_chars`、`version`。写入 PATCH 使用与导入相同的结构，世界书放在 `worldbook` 中，不能把 GET 结果原样提交：

```json
{
  "expected_version": 0,
  "character": {"name": "星灯", "description": "山间书屋的向导"},
  "note": "使用简体中文",
  "worldbook": {
    "budget_chars": 6000,
    "entries": [
      {"title":"世界观","content":"山路随晨雾改变。","constant":true}
    ]
  }
}
```

省略字段保留，`entries` 提供时整体替换，`[]` 清空；条目 ID 由服务生成。`character.name` 为非空时同步改档案名，为 `""` 时仅清空人设名称并保留档案名。限制和默认值见 [导入格式](IMPORT.md)。

状态定义修改是完整替换，建议先 GET，修改所需定义后提交整个数组，保留其余定义：

```json
{
  "expected_version": 0,
  "definitions": [
    {
      "name": "信任度",
      "kind": "number",
      "owner": "member",
      "initial": 10,
      "min": 0,
      "max": 100,
      "max_delta": 3,
      "rule": "遵守承诺时缓慢增加",
      "enabled": true
    }
  ]
}
```

最多 32 项，名称 1～32 字、不得含空白，规则最多 600 字。`kind` 为 `number` 或 `text`，`owner` 为 `member` 或 `role`；数值初始值必须在范围内，文本初始值为 1～200 字。请显式填写 `enabled`。文本定义也可保留 `min/max/max_delta: 0`。已有值的项目不能删除；类型和归属不能改变，范围必须容纳所有已保存值。可使用 `enabled: false` 停用并保留历史。定义 API 不提供伪造成员身份的状态值写入。

## 批量导入

| 方法与路径 | 行为 |
| --- | --- |
| `POST /scopes/{scope}/imports/preview` | 正文为原始 bundle JSON，返回 201 预览 |
| `POST /scopes/{scope}/imports/{id}/commit` | 无正文；确认整个预览，返回 200 回执 |
| `DELETE /scopes/{scope}/imports/{id}` | 取消，返回 204；同一取消可重复调用 |

预览示意：

```json
{
  "id":"预览 UUID",
  "expires_at":"2026-10-20T12:10:00Z",
  "changes":[
    {"name":"星灯","role_id":0,"action":"create","fields":["character.description","worldbook.entries"],"worldbook_entries":2,"version":0}
  ]
}
```

确认回执结构相同，其中 `role_id` 和 `version` 更新为保存后的值。新角色预览 `role_id: 0`；确认不会切换当前角色。有效期 10 分钟，确认时校验所有配置版本并在同一事务中提交，任何冲突整批回滚。重复确认已成功的编号返回原回执。一个会话最多 8 个待确认 API 预览。API 预览只能从 API 确认，不能确认 QQ 成员的预览。详见 [导入操作说明](IMPORT.md)。

## PowerShell 示例（兼容 5.1）

在部署者的受信环境执行。令牌通过环境变量传入，避免写入共享脚本。先设置当前实例的 `$env:ADMIN_API_TOKEN`；示例变量不会自动读取 `.env`。

```powershell
$base = 'http://127.0.0.1:18710/api/admin/v1'
$headers = @{ Authorization = "Bearer $env:ADMIN_API_TOKEN" }
Invoke-RestMethod "$base/instance" -Headers $headers
$policy = Invoke-RestMethod "$base/command-policy" -Headers $headers
$body = @{ expected_version = $policy.version; groups = @{ role_edit = $true; world_edit = $true; role_switch = $true } } | ConvertTo-Json -Depth 10
Invoke-RestMethod "$base/command-policy" -Method Put -Headers $headers -ContentType 'application/json' -Body ([Text.Encoding]::UTF8.GetBytes($body))

# 从 /会话信息 或下列列表中选择目标会话，不要直接猜群号。
Invoke-RestMethod "$base/scopes" -Headers $headers
$scopeId = '替换为目标会话ID'
$preview = Invoke-RestMethod "$base/scopes/$scopeId/imports/preview" -Method Post -Headers $headers -ContentType 'application/json' -InFile '.\docs\examples\tavern.bundle.json'
$preview | ConvertTo-Json -Depth 10
```

检查上面的变更清单，再执行确认；取消时使用第二条，二选一：

```powershell
Invoke-RestMethod "$base/scopes/$scopeId/imports/$($preview.id)/commit" -Method Post -Headers $headers
# Invoke-RestMethod "$base/scopes/$scopeId/imports/$($preview.id)" -Method Delete -Headers $headers
```

导入后选择角色（把 `$roleId` 改为角色列表返回的目标 ID）：

```powershell
Invoke-RestMethod "$base/scopes/$scopeId/roles" -Headers $headers
$roleId = 2
$scope = (Invoke-RestMethod "$base/scopes" -Headers $headers).scopes | Where-Object { $_.id -eq $scopeId }
$body = @{ role_id = $roleId; expected_version = $scope.version } | ConvertTo-Json
Invoke-RestMethod "$base/scopes/$scopeId/active-role" -Method Put -Headers $headers -ContentType 'application/json' -Body $body
```

## curl 示例（POSIX shell）

以下命令在项目目录运行；`ADMIN_API_TOKEN` 由受信环境注入。Windows 使用上面的 PowerShell 示例，或明确调用 `curl.exe` 并调整变量语法。

```sh
base=http://127.0.0.1:18710/api/admin/v1
curl --fail-with-body -H "Authorization: Bearer $ADMIN_API_TOKEN" "$base/command-policy"
curl --fail-with-body -H "Authorization: Bearer $ADMIN_API_TOKEN" "$base/scopes"
scope_id='替换为目标会话ID'
curl --fail-with-body -H "Authorization: Bearer $ADMIN_API_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @docs/examples/tavern.bundle.json \
  "$base/scopes/$scope_id/imports/preview"
```

核对预览后，填入返回的编号：

```sh
preview_id='替换为预览ID'
curl --fail-with-body -X POST -H "Authorization: Bearer $ADMIN_API_TOKEN" \
  "$base/scopes/$scope_id/imports/$preview_id/commit"
```

修改权限时先把最新版本及开关存入 `policy.json`，然后：

```sh
curl --fail-with-body -X PUT -H "Authorization: Bearer $ADMIN_API_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @policy.json "$base/command-policy"
```

## 统一错误

```json
{"error":{"code":"version_conflict","message":"settings changed; retry with the latest version"}}
```

| HTTP | code | 处理 |
| --- | --- | --- |
| 400 | `invalid_request` | 根据 message 修改字段、格式或状态定义 |
| 401 | `unauthorized` | 使用当前实例管理令牌；内部令牌无效 |
| 403 | `forbidden` | 操作权限不满足 |
| 404 | `disabled` / `not_found` | 接口未启用，或会话／角色／预览不属于当前实例、机器人或入口 |
| 409 | `version_conflict` | 版本过期或角色重名，重新读取／预览 |
| 410 | `import_expired` | 预览过期或已取消，重新预览 |
| 413 | `too_large` | 正文超过 8 MiB |
| 500 / 503 | `internal_error` / `unavailable` | 数据库异常或请求超时，检查服务日志 |

后台管理审计保存在 `tavern_admin_audit`；角色配置与定义变更也写入已有修订记录，操作者显示“管理接口”。当前未提供审计查询接口。

[返回项目首页](../README.md) · [多实例部署](DEPLOYMENT.md#同机部署多个实例)
