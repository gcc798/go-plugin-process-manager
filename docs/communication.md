# 双向通信语义

Master 调用 Plugin `SendMessage` 并收到返回，是一个 RPC 请求响应。`interactions.json` 仅增加一条 `kind=REQUEST_RESPONSE`、`initiator=MASTER` 记录，请求和响应 payload 分列保存。

Heartbeat 定时调用 Master `ReportMessage` 是另一条真正由 Plugin 发起的 RPC。它写入 `kind=ACTIVE_REPORT`、`initiator=PLUGIN` 记录；Master ACK 保存在该条记录的 response_payload 中，不产生虚假的第二条业务上报。

因此可分别查询：

```bash
curl -s '127.0.0.1:16060/api/interactions?kind=REQUEST_RESPONSE'
curl -s '127.0.0.1:16060/api/interactions?kind=ACTIVE_REPORT'
```

