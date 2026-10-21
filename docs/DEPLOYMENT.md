# 部署与维护

## 状态与日志

扫码模式使用两个 Compose 文件：

```sh
docker compose -f compose.yaml -f compose.qr.yaml ps
docker compose -f compose.yaml -f compose.qr.yaml logs --tail=100 gateway worker
```

手动模式省略 `-f compose.qr.yaml`。Windows 下 Compose 命令相同，本文中的 `./scripts/start.sh` 替换为 `.\scripts\start.cmd`。

## 连接与凭据

默认端口仅绑定本机：Gateway `18700`、Worker `18710`，Worker 同时提供健康检查和可选管理 API。用不同 Compose 项目名和端口可部署多套，数据卷没有固定全局名称。不要让两套 Gateway 使用同一真实机器人并发接收事件。

扫码模式将凭据保存在项目独立的 `qq-private` 卷，文件权限为 `0600`；Gateway 只读挂载完整凭据，Worker 只读挂载 `qq-public` 卷中的 AppID。二维码只显示在前台终端，登录容器关闭 Docker 日志记录。不要分享二维码、凭据卷或 `.env`。

需要更换机器人时运行 `./scripts/start.sh qr --rebind`。脚本会先停止 Gateway 和 Worker；取消扫码或授权失败不会覆盖原绑定。失败后运行 `./scripts/start.sh qr` 可用原绑定恢复服务。身份变更后使用新的机器人作用域，旧历史仍保留在原作用域。不要同时运行两个启动脚本。

若登录容器被强制杀死，重试时可能提示登录锁已存在。先用下面第一条命令确认没有仍在运行的 `qq-login` 容器，再执行第二条清理锁并重试。仅删除锁，不删除绑定或历史卷：

```sh
docker compose -f compose.yaml -f compose.qr.yaml --profile setup ps -a
docker compose -f compose.yaml -f compose.qr.yaml run --rm --no-deps --entrypoint node qq-login -e 'require("node:fs").rmSync("/binding/private/.login-lock", {recursive:true,force:true})'
./scripts/start.sh qr
```

扫码部署执行 Compose 启停、升级和重建时始终使用 `-f compose.yaml -f compose.qr.yaml`；漏掉覆盖文件会改回手动凭据模式。切回手动模式前填写 `.env` 的 QQ 凭据，再执行 `./scripts/start.sh manual`。

## 备份与升级

备份前停止该项目服务，再备份三个业务数据卷；扫码模式额外备份两个绑定卷。SQLite 文件可能含 WAL，需要整体保留。恢复时使用对应版本镜像。数据库会拒绝高于支持版本的迁移号；升级前备份，不能用旧程序直接读新数据库。

当前 Worker 数据库迁移至 **schema 4**：早期数据库先迁移多角色档案和最近消息，再增加实例 ID、命令权限、导入暂存、角色选择版本及管理审计。原有人设、历史、摘要、个人记忆和状态保留；未命名角色显示“默认角色”。**首次升级后全部 QQ 手动修改命令组关闭**，需部署者使用管理 API 按需重新开放。后续重启保留已保存的权限。

升级前停止 worker 并完整备份其数据卷。回滚必须同时恢复旧镜像与升级前数据库，QQ 绑定卷可继续使用。导入暂存与管理审计随 SQLite 一起备份，包含角色设定；备份按实例分别保管。已有数据库备份只用于恢复原实例，不用于创建新实例，否则会复制实例 ID 和业务数据。

## 同机部署多个实例

每个实例使用一个独立部署目录、一份 `.env`、一个 QQ 机器人。目录中的代码可来自同一版本，镜像 `qq-tavern:0.2.1-dev` 可共用；运行配置、网络和数据卷必须独立。

例如部署目录为 `D:\qq-instances\instance-a` 与 `D:\qq-instances\instance-b`。在每个目录按模型配置文档初始化 `.env`，再分别填写：

| 配置 | 实例 A | 实例 B |
| --- | --- | --- |
| `COMPOSE_PROJECT_NAME` | `qq-instance-a` | `qq-instance-b` |
| `GATEWAY_PORT` | `18700` | `18701` |
| `WORKER_PORT` | `18710` | `18711` |
| `ADMIN_API_TOKEN` | 为 A 独立生成 | 为 B 独立生成 |
| `INTERNAL_API_TOKEN` | 为 A 独立生成，与管理令牌不同 | 为 B 独立生成，与管理令牌不同 |
| QQ 身份 | 实例 A 的机器人 | 实例 B 的机器人 |

实例 A `.env` 节选（占位令牌必须替换，不要照抄上线）：

```dotenv
COMPOSE_PROJECT_NAME=qq-instance-a
GATEWAY_PORT=18700
WORKER_PORT=18710
ADMIN_API_TOKEN=<独立生成的A管理令牌>
INTERNAL_API_TOKEN=<独立生成的A内部令牌>
QQ_APP_ID=
QQ_APP_SECRET=
```

实例 B `.env` 节选：

```dotenv
COMPOSE_PROJECT_NAME=qq-instance-b
GATEWAY_PORT=18701
WORKER_PORT=18711
ADMIN_API_TOKEN=<独立生成的B管理令牌>
INTERNAL_API_TOKEN=<独立生成的B内部令牌>
QQ_APP_ID=
QQ_APP_SECRET=
```

PowerShell 5.1 可用以下代码生成一枚令牌；**每个字段分别运行一次**，保存在对应实例的本地 `.env` 或受控的密钥存储中：

```powershell
$tokenBytes = New-Object byte[] 32
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($tokenBytes)
$rng.Dispose()
[BitConverter]::ToString($tokenBytes).Replace('-', '').ToLowerInvariant()
```

Linux/macOS 每次执行 `openssl rand -hex 32` 生成一枚。管理令牌至少 32 字符，留空关闭接口；内部令牌始终必填。轮换令牌时，更新该实例的 `.env` 后重建对应容器，并同步更新调用脚本中的配置。

分别从两个目录运行启动脚本，顺序扫码绑定对应机器人：

```powershell
Set-Location D:\qq-instances\instance-a
.\scripts\start.cmd qr
Set-Location D:\qq-instances\instance-b
.\scripts\start.cmd qr
```

Linux/macOS 使用各自目录的 `./scripts/start.sh qr`；手动模式填写不同 QQ 凭据并使用 `manual`。启动后保持 Compose 项目名稳定，改变项目名会指向另一套卷，不是迁移数据。

Compose 按项目名生成 `qq-instance-a_default`、`qq-instance-b_default` 等独立网络；各自包含 Redis、Gateway、Worker，以及对应的 `redis-data`、`gateway-data`、`worker-data` 数据卷。扫码模式再各自拥有 `qq-private` 和 `qq-public` 绑定卷。不要给这些网络或卷配置相同的固定全局 `name`，不要将两个实例的数据目录挂载到同一宿主机路径。

“实例内全局权限”意味着：开放 A 的 `world_edit` 后，A 的所有群和私聊成员都可编辑各自会话的世界书；B 的开关和设定完全不变。A 的管理令牌不能调用 B。每个实例的会话数据仍按机器人、群／私聊、角色隔离。

使用 [管理 API](ADMIN_API.md) 时，分别指定实例地址与对应令牌。以上配置对应的本机地址为 `127.0.0.1:18710`、`127.0.0.1:18711`；从容器或另一台机器调用时，需要配置受控的服务路由／HTTPS 代理，容器自己的 `127.0.0.1` 不代表宿主机。令牌不得写入公开网页或共享脚本。

### 部署后的隔离验收

1. 分别向 A、B 的 `/instance` 发请求，确认实例 ID 不同、机器人标识对应各自绑定；用 A 令牌访问 B 应返回 `401`。
2. 在两个机器人下分别发送 `/会话信息`。默认尝试 `/角色 描述 测试` 应被拒绝，普通聊天与查询可用。
3. 仅开放 A 的某一命令组，确认 A 在群聊和私聊中生效，B 仍拒绝；重启 A 后权限保持。
4. 通过 API 给 A 导入示例并确认，检查 B 的角色列表和设定未变。
5. 在开放必要权限的测试实例私聊发送 JSON，群内引用 JSON 并 @发送 `/导入`，检查预览、确认、重复确认、取消、权限关闭后的拒绝。再发送图片确认图片能力正常。

最后一步必须使用真实 QQ 附件投递验证；QQ 未投递附件时以 API 导入，并记录机器人类型、消息类型和具体缺失字段，不能把模拟附件测试当作真实收发验收。

## 修改模型配置后生效

编辑现有 `.env` 中的模型字段后，扫码模式执行：

```sh
docker compose -f compose.yaml -f compose.qr.yaml up -d --no-deps worker
```

手动模式省略第二个 `-f`。单独 `docker compose restart` 不会载入新的环境变量。应用代码更新时应先构建镜像；数据迁移升级前先停止 worker 并完整备份数据卷。

[返回项目首页](../README.md) · [模型接入](../config/models/README.md)
