# 部署与维护

## 状态与日志

扫码模式使用两个 Compose 文件：

```sh
docker compose -f compose.yaml -f compose.qr.yaml ps
docker compose -f compose.yaml -f compose.qr.yaml logs --tail=100 gateway worker
```

手动模式省略 `-f compose.qr.yaml`。Windows 下 Compose 命令相同，本文中的 `./scripts/start.sh` 替换为 `.\scripts\start.cmd`。

## 连接与凭据

默认健康检查端口仅绑定本机：Gateway `18700`、Worker `18710`。用不同 Compose 项目名和端口可部署多套，数据卷没有固定全局名称。不要让两套 Gateway 使用同一真实机器人并发接收事件。

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

首次升级到多角色版本时，worker 数据库迁移为版本 2，原有人设与历史归入第一个角色档案，保留原名；未命名时显示“默认角色”。升级前停止 worker 并完整备份其数据卷。回滚必须同时恢复旧镜像与升级前数据库，QQ 绑定卷可继续使用。


## 修改模型配置后生效

编辑现有 `.env` 中的模型字段后，扫码模式执行：

```sh
docker compose -f compose.yaml -f compose.qr.yaml up -d --no-deps worker
```

手动模式省略第二个 `-f`。单独 `docker compose restart` 不会载入新的环境变量。应用代码更新时应先构建镜像；数据迁移升级前先停止 worker 并完整备份数据卷。

[返回项目首页](../README.md) · [模型接入](../config/models/README.md)
