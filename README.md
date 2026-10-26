# Go Plugin Process Manager

这是一个使用 HashiCorp `go-plugin`、gRPC 与 `GRPCBroker` 构建的多进程插件管理工程。Master 提供本机 HTTP 管理面，Timestamp 与 Heartbeat 是两个正式内置插件进程；项目覆盖动态启停、双向通信、健康检查、崩溃恢复、热更新、结构化日志和 JSON 审计，可直接作为生产化模板继续扩展。

## 架构

```text
curl / HTTP
     │
     ▼
Master ── 生命周期/健康/重启/更新 ──► go-plugin Client ── gRPC ──► PluginService
  │                                                        │
  │◄──────────── HostService.ReportMessage ── GRPCBroker ──┘
  │
  ├── stdout + dist/logs/master.log + GET /api/logs
  └── dist/data/{plugins,runtime,events,interactions,restarts}.json
```

`SendMessage` 的返回是 Master 发起 RPC 的响应，保存在一条 `kind=REQUEST_RESPONSE` 记录中。Heartbeat 调用 `HostService.ReportMessage` 才是插件发起的新业务调用，保存为 `kind=ACTIVE_REPORT`；ACK 不会伪装成另一条主动上报。

Master 日志与 go-plugin 转发的插件日志进入同一输出，但用 `component=master|plugin`、`plugin_id`、`generation` 和可用时的 `pid` 区分。

## 目录

- `api/proto/plugin/v1`：通用 PluginService 与 HostService 契约。
- `pkg/plugincontract`：go-plugin 握手、gRPC 代理和 Broker 回调搭桥。
- `internal/master`：进程管理、HTTP、JSON 存储、日志和模型。
- `internal/plugins`：两个插件的通用服务与各自业务。
- `cmd`：Master 和插件进程入口。
- `scripts`：可重复执行的 curl 集成测试。
- `docs`：协议、生命周期、恢复和设计说明。

## 环境与构建

需要 Go 1.25+；集成测试还需要 Bash、curl、jq、lsof 和常见 POSIX 进程工具。macOS Apple Silicon 与 Linux 均可直接编译。

```bash
make build
make run-master
```

默认监听 `127.0.0.1:16060`。Master 位于 `dist/master`，因此默认数据、日志和允许执行的插件目录分别是 `dist/data`、`dist/logs` 与 `dist/plugins`，不依赖启动时工作目录。可用 `-data-dir`、`-log-dir`、`-plugin-dir`、`-listen` 覆盖。

## 快速体验

```bash
curl -s http://127.0.0.1:16060/api/monitor/overview | jq

curl -s -X POST http://127.0.0.1:16060/api/plugins \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"timestamp-001\",\"type\":\"timestamp\",\"version\":\"1.0.0\",\"binary_path\":\"$PWD/dist/plugins/timestamp/v1.0.0/timestamp-plugin\"}" | jq

curl -s -X POST http://127.0.0.1:16060/api/plugins/timestamp-001/start | jq
curl -s -X POST http://127.0.0.1:16060/api/plugins/timestamp-001/messages \
  -H 'Content-Type: application/json' \
  -d '{"message_type":"timestamp.generate","payload":{}}' | jq
```

更新到 v2：

```bash
curl -s -X POST http://127.0.0.1:16060/api/plugins/timestamp-001/update \
  -H 'Content-Type: application/json' \
  -d "{\"version\":\"2.0.0\",\"binary_path\":\"$PWD/dist/plugins/timestamp/v2.0.0/timestamp-plugin\"}" | jq
```

模拟崩溃：先从 `GET /api/plugins/timestamp-001` 读取 PID，再执行 `kill -9 PID`，随后查询 `/api/plugins/timestamp-001/restarts`。用户执行 Stop 会把 desired state 改成 STOPPED，因此不会自动拉起。

## HTTP API

| 方法 | 路径 | 用途 |
|---|---|---|
| POST / GET | `/api/plugins` | 注册 / 列表 |
| GET / DELETE | `/api/plugins/:id` | 详情 / 删除已停止实例 |
| POST | `/api/plugins/:id/start` | 启动 |
| POST | `/api/plugins/:id/stop` | 停止 |
| POST | `/api/plugins/:id/restart` | 重启；停止状态下等价于启动 |
| POST | `/api/plugins/:id/update` | 先验证候选实例再切换 |
| POST | `/api/plugins/:id/messages` | Master 主动业务调用 |
| GET | `/api/plugins/:id/health` | 三维健康状态 |
| GET | `/api/plugins/:id/interactions` | 插件交互历史 |
| GET | `/api/plugins/:id/events` | 插件事件 |
| GET | `/api/plugins/:id/restarts` | 重启历史 |
| GET | `/api/monitor/overview` | 汇总 |
| GET | `/api/monitor/plugins` | 全部运行快照 |
| GET | `/api/events` | 全局事件 |
| GET | `/api/interactions` | 全局交互 |
| GET | `/api/logs` | 集中日志 |

列表支持 `limit`。事件还支持 `plugin_id`、`event_type`；交互支持 `plugin_id`、`direction`、`initiator`、`kind`；日志支持 `plugin_id`、`level`。

## 日志、数据与测试

```bash
tail -f dist/logs/master.log
curl -s 'http://127.0.0.1:16060/api/logs?plugin_id=timestamp-001&limit=100' | jq
jq . dist/data/interactions.json

go test ./...
go test -race ./...
make test-integration
```

`scripts/test-all.sh` 在 16160 端口启动自己拥有的临时 Master，结束时只停止该 PID，不会清理用户已有实例。单项脚本可通过 `BASE_URL` 指向已启动 Master。

清理手工创建的实例时先对相关 ID 调用 Stop/Delete，再停止 Master；不要用宽泛的 `pkill`。更深入的实现说明见 [docs/interview-notes.md](docs/interview-notes.md)。

