# 模型配置

每家服务的接口地址、协议与参数各放在自己的文件里，选择其中一份即可。

| 服务 | 模板 | 说明 |
| --- | --- | --- |
| OpenAI | [openai.env.example](openai.env.example) | [接入文档](openai.md) |
| 智谱 GLM | [zhipu.env.example](zhipu.env.example) | [接入文档](zhipu.md) |
| 其他兼容服务 | [compatible.env.example](compatible.env.example) | [接入文档](compatible.md) |

根目录的 [.env.example](../../.env.example) 是公共配置，包含 QQ、内部通信、端口和可选工具。模型模板只包含模型字段。两部分组合为本地 `.env` 后，现有启动脚本即可读取；程序不会自动扫描本目录。

## 新部署：生成 .env

以下命令在**项目根目录**运行。把示例中的 `openai` 换成所选模板名：`openai`、`zhipu` 或 `compatible`。命令只供首次初始化，已有 `.env` 时会停止，避免覆盖现有凭据。

### Windows PowerShell 5.1 / 7

```powershell
$provider = 'openai'
if (Test-Path -LiteralPath .env) { throw '.env already exists; edit it instead.' }
$template = Join-Path 'config/models' ($provider + '.env.example')
$common = [IO.File]::ReadAllText((Join-Path $PWD '.env.example'))
$model = [IO.File]::ReadAllText((Join-Path $PWD $template))
$tokenBytes = New-Object byte[] 32
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
try { $rng.GetBytes($tokenBytes) } finally { $rng.Dispose() }
$token = [BitConverter]::ToString($tokenBytes).Replace('-', '').ToLowerInvariant()
$content = ($common.TrimEnd() + "`n`n" + $model) -replace '(?m)^INTERNAL_API_TOKEN=.*$', ('INTERNAL_API_TOKEN=' + $token)
[IO.File]::WriteAllText((Join-Path $PWD '.env'), $content, [Text.UTF8Encoding]::new($false))
notepad .env
```

### Linux / macOS

需要本机提供 `openssl`。令牌只写入本地配置，不输出到终端。

```sh
(
  set -eu
  provider=openai
  test ! -e .env || { echo ".env already exists; edit it instead." >&2; exit 1; }
  template="config/models/${provider}.env.example"
  test -f "$template"
  token=$(openssl rand -hex 32)
  umask 077
  {
    sed "s/^INTERNAL_API_TOKEN=.*/INTERNAL_API_TOKEN=$token/" .env.example
    printf '\n'
    cat "$template"
  } > .env
)
```

## 填写并启动

打开生成的 `.env`：

1. 填写 `MODEL_API_KEY`，确认 `MODEL_NAME`。通用模板还需填写接口地址。
2. 按所选服务的接入文档确认协议与聊天、摘要的思考档位。
3. 扫码模式将 QQ 凭据留空；手动模式填写 `QQ_APP_ID`、`QQ_APP_SECRET`。
4. 按 [项目首页](../../README.md#3-连接-qq-并启动) 启动服务。

不要把真实密钥填入仓库内的模板。运行时 `.env` 已被 Git 忽略。

## 已有部署：换模型或服务

直接编辑现有 `.env` 中的模型字段，用所选模板替换原有模型段。保留 QQ 凭据、内部令牌和其他设置；不要反复追加模板，避免同名变量重复。

修改后重新创建 Worker 容器。扫码模式：

```sh
docker compose -f compose.yaml -f compose.qr.yaml up -d --no-deps worker
```

手动模式省略第二个 `-f`。单独 `docker compose restart` 不会加载新的环境变量。通过 QQ 中的 `/当前模型` 检查生效的模型、协议与档位。

## 共同约定

- `MODEL_BASE_URL` 是接口基础地址，程序会追加 `/responses` 或 `/chat/completions`。
- 模型需要支持工具调用，才能使用历史搜索、自动角色状态更新等功能。
- 程序会发送思考强度参数；“兼容 OpenAI”不代表支持所有参数。
- `SUMMARY_MODEL` 留空沿用聊天模型；摘要与聊天共用接口地址、密钥和协议，不能用此项跨厂商配置。
- `SUMMARY_REASONING_EFFORT` 独立控制摘要思考强度，应选择摘要模型支持的值。
- 程序不会在请求失败后自动更换模型或协议。

[返回项目首页](../../README.md)
