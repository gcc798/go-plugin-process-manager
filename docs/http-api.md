# HTTP API

Master 默认监听 `http://127.0.0.1:16060`。请求和响应使用 JSON；成功删除返回 `204 No Content`，其他成功操作返回 JSON。错误响应统一为：

```json
{
  "error": "错误说明"
}
```

Master 内嵌管理控制台：`http://127.0.0.1:16060/dashboard/`。控制台与 API 同源运行，不需要单独构建或部署前端资源，可执行启停、重启、数据查看和上传更新。

控制台会扫描 `-plugin-dir` 下的 `类型/版本/可执行文件` 结构。尚未注册的程序显示为 `AVAILABLE / UNREGISTERED`，可直接执行“注册并启动”；注册实例与磁盘程序是两个不同概念。

常见状态码：

| 状态码 | 含义 |
|---|---|
| `200` | 查询或操作成功 |
| `201` | 插件注册成功 |
| `204` | 插件删除成功 |
| `400` | 请求参数、路径或插件文件无效 |
| `404` | 插件不存在 |
| `409` | 当前生命周期状态与操作冲突 |
| `502` | Master 调用插件业务 RPC 失败 |
| `500` | Master 内部或持久化错误 |

## 注册插件

可先通过 `GET /api/plugin-binaries` 查询插件目录中的可注册程序。

```http
POST /api/plugins
Content-Type: application/json
```

```json
{
  "id": "timestamp-001",
  "type": "timestamp",
  "version": "1.0.0",
  "binary_path": "/absolute/path/dist/plugins/timestamp/v1.0.0/timestamp-plugin",
  "auto_start": false,
  "restart": {
    "enabled": true,
    "max_retries": 5,
    "initial_delay_seconds": 1,
    "max_delay_seconds": 30,
    "stable_reset_seconds": 60
  }
}
```

`binary_path` 必须是 `-plugin-dir` 允许目录内的绝对或可解析路径，并且文件必须可执行。

## 生命周期操作

```text
POST   /api/plugins/:id/start
POST   /api/plugins/:id/stop
POST   /api/plugins/:id/restart
DELETE /api/plugins/:id
```

只有已停止、已注册或失败状态的插件可以删除。重复 Start 或对未运行实例执行 Stop 会返回 `409`。

## 发送业务消息

```http
POST /api/plugins/:id/messages
Content-Type: application/json
```

```json
{
  "message_type": "timestamp.generate",
  "payload": {},
  "timeout_ms": 2000
}
```

`message_type` 和 `payload` 由目标插件解释。`timeout_ms` 可省略，默认使用 Master 的 RPC 超时。

Timestamp 支持：

- `timestamp.generate`
- `timestamp.slow`，payload 为 `{"delay_ms": 3000}`

Heartbeat 支持：

- `heartbeat.set_interval`，payload 为 `{"interval_seconds": 1}`
- `heartbeat.pause`
- `heartbeat.resume`
- `heartbeat.get_stats`

## 更新插件

```http
POST /api/plugins/:id/update
Content-Type: application/json
```

```json
{
  "version": "2.0.0",
  "binary_path": "/absolute/path/dist/plugins/timestamp/v2.0.0/timestamp-plugin"
}
```

Master 会先启动候选进程，验证类型、版本和业务健康，再切换当前实例。候选启动或验证失败时旧实例继续运行。

也可以直接上传二进制并更新：

```http
POST /api/plugins/:id/update/upload
Content-Type: multipart/form-data
```

表单字段：

- `version`：目标版本，仅允许字母、数字、点、下划线、加号和连字符，最长 64 字符。
- `binary`：插件可执行文件，最大 100 MiB。

```bash
curl -fsS -X POST \
  -F 'version=2.0.0' \
  -F 'binary=@./dist/plugins/timestamp/v2.0.0/timestamp-plugin' \
  http://127.0.0.1:16060/api/plugins/timestamp-001/update/upload | jq
```

Master 忽略客户端文件名，将内容写入 `-plugin-dir` 下的受控目录。文件完成同步和原子发布后才进入既有候选实例验证及切换流程；更新失败时会保留旧实例并清理失败上传。

## SSE 实时流

```http
GET /api/monitor/stream
Accept: text/event-stream
```

连接建立后立即发送 `snapshot` 事件；插件状态、消息交互或生命周期事件变化时继续推送，空闲期间每 15 秒发送 `heartbeat` 保活事件。`snapshot` 包含：

- 插件数量、存活数和健康数。
- 全部插件运行快照。
- 最近 100 条消息交互。
- 最近 100 条生命周期事件。
- 最近 100 条重启记录。

```bash
curl -N http://127.0.0.1:16060/api/monitor/stream
```

## 查询接口

```text
GET /api/plugins
GET /api/plugins/:id
GET /api/plugins/:id/health
GET /api/monitor/overview
GET /api/monitor/plugins
GET /api/plugins/:id/interactions
GET /api/plugins/:id/events
GET /api/plugins/:id/restarts
GET /api/interactions
GET /api/events
GET /api/logs
```

所有历史列表支持 `limit`，最大值为 1000。附加过滤条件：

| 接口 | 过滤参数 |
|---|---|
| `/api/events` | `plugin_id`、`event_type` |
| `/api/interactions` | `plugin_id`、`direction`、`initiator`、`kind` |
| `/api/logs` | `plugin_id`、`level` |

区分两种业务通信：

```bash
curl -fsS 'http://127.0.0.1:16060/api/interactions?kind=REQUEST_RESPONSE' | jq
curl -fsS 'http://127.0.0.1:16060/api/interactions?kind=ACTIVE_REPORT' | jq
```

`REQUEST_RESPONSE` 表示 Master 发起的业务调用；`ACTIVE_REPORT` 表示插件调用 HostService 主动上报。RPC 响应或 ACK 保存在原交互记录中，不会再生成一条主动上报。

## 请求日志

每次 HTTP 请求完成后都会写入统一日志，包含：

```text
component=master method=GET url=/api/monitor/overview status=200 duration_ms=0 client_ip=127.0.0.1
```

`url` 包含查询参数。不要把密码、访问令牌或其他敏感信息放在 URL 中。

