# 崩溃恢复

监控循环每三秒分别检查：`Client.Exited()` 的进程存活、`ClientProtocol.Ping()` 的 RPC 连通性，以及通用 `Health()` 的业务健康。Ping 成功不代表业务健康；连续三次业务检查失败才进入 UNHEALTHY。

意外退出后只在 desired state 为 RUNNING 且策略启用时重启。退避从 1 秒指数增长并受最大值限制；每次尝试写入 restarts.json。稳定运行达到策略时间后重置连续重试计数。Stop 先改 desired state 并取消监控，所以不会被误判为崩溃；Update 的旧实例也先从当前代理移除。

