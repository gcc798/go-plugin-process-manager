# gRPC 与 Protobuf 代码生成说明

本文说明协议源文件、生成产物、手写实现以及可重复的生成命令。生成代码已经提交到仓库，普通构建不要求本机安装 `protoc`；只有修改 `.proto` 契约后才需要重新生成。

## 文件边界

以下文件是手写的协议源文件：

- `api/proto/plugin/v1/plugin.proto`：定义 `PluginService`、通用消息、插件信息、健康、回调配置和关闭消息。
- `api/proto/plugin/v1/host.proto`：定义 Master 暴露给插件的 `HostService.ReportMessage`。

以下文件由工具生成，文件头包含 `Code generated ... DO NOT EDIT`，禁止手工修改：

| 生成文件 | 生成器 | 内容 |
|---|---|---|
| `pkg/plugincontract/gen/plugin.pb.go` | `protoc-gen-go` | Plugin 协议消息结构、字段访问器、反射描述和序列化元数据 |
| `pkg/plugincontract/gen/plugin_grpc.pb.go` | `protoc-gen-go-grpc` | `PluginServiceClient`、`PluginServiceServer`、RPC handler 和服务描述 |
| `pkg/plugincontract/gen/host.pb.go` | `protoc-gen-go` | Host 协议文件描述以及对通用消息的依赖关系 |
| `pkg/plugincontract/gen/host_grpc.pb.go` | `protoc-gen-go-grpc` | `HostServiceClient`、`HostServiceServer`、RPC handler 和服务描述 |

以下代码是手写实现，不是 gRPC 工具生成的：

- `pkg/plugincontract/contract.go`：把生成的 Client/Server 接入 HashiCorp `go-plugin` 和 `GRPCBroker`。
- `internal/plugins/service.go`：实现 `PluginServiceServer` 的 GetInfo、Health、SendMessage、ConfigureHost 和 Shutdown。
- `internal/plugins/timestamp/service.go`：实现 Timestamp 的 `MessageHandler`。
- `internal/plugins/heartbeat/service.go`：实现 Heartbeat 的 `MessageHandler` 和主动上报循环。
- `internal/master/manager/manager.go`：实现 `HostServiceServer.ReportMessage`、身份校验、记录和 ACK。

## MessageHandler 分派方式

公共服务只有一个 `MessageHandler`：

```go
type MessageHandler func(ctx context.Context, message *gen.Message) (string, error)
```

公共 `SendMessage` 负责校验请求、调用 `Handle`、包装 `MessageResponse`。每个插件在构造时注入自己的处理函数：

```text
PluginService.SendMessage
        │
        ▼
plugins.Service.SendMessage
        │
        ├── Timestamp Handle → timestamp.generate / timestamp.slow
        └── Heartbeat Handle → set_interval / pause / resume / get_stats
```

因此 `message_type` 的业务分派属于具体插件，公共 gRPC 契约只负责传输，不需要了解所有插件类型。

## 工具版本

当前提交的生成文件由以下版本生成：

```text
protoc             33.0（生成文件头显示为 v6.33.0）
protoc-gen-go      v1.36.11
protoc-gen-go-grpc v1.6.1
```

安装 Go 生成器：

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.1
```

还需要安装 Protocol Buffers Compiler，并确保 `protoc`、`protoc-gen-go` 和 `protoc-gen-go-grpc` 都在 `PATH` 中。

## 生成命令

在仓库根目录执行：

```bash
make generate
```

该目标实际执行：

```bash
protoc -I . \
  --go_out=. \
  --go_opt=module=github.com/gcc798/go-plugin-process-manager \
  --go-grpc_out=. \
  --go-grpc_opt=module=github.com/gcc798/go-plugin-process-manager \
  api/proto/plugin/v1/plugin.proto \
  api/proto/plugin/v1/host.proto
```

`go_package` 和 `--go_opt=module` 共同保证生成文件落在 `pkg/plugincontract/gen`，并使用正式模块导入路径。

## 修改协议后的检查

修改 `.proto` 后执行：

```bash
make generate
gofmt -w pkg/plugincontract/gen
go test ./...
go test -race ./...
git diff -- api/proto/plugin/v1 pkg/plugincontract/gen
```

代码评审时应同时看到 `.proto` 和对应生成文件变化；只有生成文件变化而协议源文件没有变化，通常意味着生成环境或工具版本不一致。

