# 原理与源码面试笔记

1. **go-plugin 与标准库 plugin**：标准库 `plugin.Open` 把共享对象装入同一地址空间，平台和 ABI 限制较强；本项目的 HashiCorp go-plugin 启动独立可执行文件，通过 RPC 通信，崩溃隔离更明确。
2. **为什么独立进程**：可以单独 Kill、观察 PID、限制故障范围并用不同版本二进制更新，代价是序列化、超时与进程清理。
3. **如何启动**：`manager.spawn` 构造 `exec.Cmd` 和 `plugin.NewClient`，`Client()` 读取插件握手信息并建立 gRPC 通道。
4. **HandshakeConfig**：双方共享协议版本和 magic cookie。cookie 防止用户直接误运行插件，不是认证或密钥管理。
5. **RPC/gRPC 流程**：插件 `plugin.Serve` 注册 GRPCPlugin；Master 完成握手后 `Dispense` 得到封装生成客户端的 `plugincontract.Client`。
6. **GRPCPlugin 与 Dispense**：`GRPCServer` 在插件端注册生成服务，`GRPCClient` 在宿主端包装连接；Dispense 按逻辑名取回这个代理。
7. **Plugin 主动通信**：Heartbeat 不是回传 SendMessage 的响应，而是在定时循环中调用 HostService.ReportMessage。
8. **GRPCBroker**：它在现有插件协议旁协商额外连接。Master `AcceptAndServe` 暴露 HostService，插件收到 broker ID 后 `Dial` 回连。
9. **RPC 与业务健康**：Ping 只证明协议连接可用，Health 才表示插件业务 ready；`RuntimeSnapshot` 还独立保留 process_alive。
10. **go-plugin 的进程能力**：它负责启动、握手、协议连接、`Exited` 与 `Kill`，但不提供本项目需要的状态机、更新政策和审计。
11. **为什么业务层自动重启**：重试次数、退避、desired state 和 FAILED 状态都是业务策略，go-plugin 不知道何时应该恢复。
12. **避免重复 Start**：每实例 `operation` 互斥，锁内再次检查 `instance != nil`；不同插件仍可并行。
13. **Stop 与自动重启**：Stop 先设置 desired=STOPPED、取消实例监控，再回收进程；崩溃处理再次核对 desired state。
14. **动态更新避免中断**：先完整启动候选并做 Info/Health 验证，`calls` 写锁等待在途请求，切换后才杀旧进程。
15. **Master 崩溃后的 Plugin**：正常信号路径会清理；SIGKILL 不可捕获。V1 不重附着，启动时不信任 runtime.json 的旧 PID。
16. **JSON 安全与局限**：同文件写入互斥、临时文件同步后原子 rename；无跨文件事务，有全量重写和容量上限。
17. **RPC 阻塞**：HTTP 消息使用 context timeout，健康和首次验证默认两秒；插件慢请求监听 context 取消。
18. **不能共享 Go 对象**：独立进程有不同虚拟地址空间；接口调用必须编码成 protobuf 并经 IPC 传递。
19. **多进程与 Goroutine**：Goroutine 共享堆且调度轻；插件进程有独立内存、PID、信号和故障域，创建与通信成本更高。
20. **扩展第三方插件**：保持通用消息契约，增加签名/来源校验、能力声明、资源限制、沙箱、兼容版本协商与更可靠的注册发布流程，而不是把第三方业务方法塞进统一 proto。

