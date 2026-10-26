# 生命周期

状态包括 REGISTERED、STARTING、RUNNING、UNHEALTHY、STOPPING、STOPPED、RESTARTING、UPDATING、FAILED，并同时保存 desired/observed state。

每个实例的 `operation` 锁串行化 Start、Stop、Restart、Update；`calls` 读写锁让普通业务请求完成后再切换或停止代理。不同插件持有不同锁，因此可以并行启动。状态写入口集中在 Manager。

更新先拉起候选进程，核验类型/版本、建立 Callback 并检查业务健康；之后才切换当前代理并保存定义。定义持久化失败会恢复旧代理并终止候选。切换成功后取消旧实例后台任务并 Kill 旧进程。

