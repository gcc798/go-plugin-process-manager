# 插件协议

`PluginService` 只包含 GetInfo、Health、SendMessage、ConfigureHost、Shutdown，不含任何时间戳或心跳专属 RPC。消息携带 message_id、correlation_id、plugin_id、message_type、JSON payload、时间、metadata 和 generation。

Master 通过 `Dispense("generic")` 得到 `plugincontract.Client`。配置回调时 Master 从 `GRPCBroker.NextId` 取得 ID，在 `AcceptAndServe` 注册 HostService，再把 ID、plugin_id 和 generation 传给插件。插件端用同一 Broker `Dial` 回连，Heartbeat 随后调用 ReportMessage。

回调服务绑定创建时的 plugin_id/generation，Manager 还会检查它是否仍是当前生效实例。更新后的旧进程即使暂时存活，也不能污染新代次记录。

