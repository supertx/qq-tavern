# 开发与验证

开发需要 Go 1.23+；CI 和容器使用 Go 1.27。安装 `redis-server` 可执行文件以运行完整集成测试：

```sh
go test -race -count=1 -timeout=180s ./...
go vet ./...
go build ./cmd/qq-tavern
npm --prefix onboard ci --ignore-scripts
npm --prefix onboard test
python3 scripts/start_test.py
```

Windows 启动脚本测试使用模拟 Docker，不启动容器：`python scripts/start_windows_test.py`。CI 在 Windows PowerShell 5.1 和 PowerShell 7 下分别验证。

测试使用临时 SQLite、临时 Redis、模拟 QQ HTTP/WebSocket 和模拟模型服务，不需要真实账号或模型费用。扫码辅助工具的开发测试另需 Node.js 22+。CI 安装 Redis、测试两种启动方式并构建应用和登录容器。构建两个镜像后，可运行 `python3 scripts/test_login_docker.py` 验证真实容器的权限和凭据交接；该测试只使用合成凭据，容器禁用网络。环境变量检查可执行 `ROLE=worker qq-tavern check` 或 `ROLE=gateway qq-tavern check`。

发布前需更新 [VERSION](../VERSION)、[变更日志](../CHANGELOG.md)，执行测试、容器构建和来源／凭据检查。自动化测试覆盖模拟 QQ 与模型接口；真实 QQ 接入验收另见 [验证清单](VALIDATION.md)。当前版本为开发版，发布稳定版本前需完成对应环境的验收。


[返回项目首页](../README.md)
