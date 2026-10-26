// Package plugincontract 封装 Master 与插件共享的 go-plugin gRPC 契约。
package plugincontract

import (
	"context"

	gen "github.com/gcc-798/go-plugin-process-manager/pkg/plugincontract/gen"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

const (
	// PluginName 是 go-plugin 插件集合中的统一逻辑名称。
	PluginName = "generic"
	// MagicCookieKey 是宿主与插件握手使用的环境变量名。
	MagicCookieKey = "GO_PLUGIN_PROCESS_MANAGER"
	// MagicCookieValue 是宿主与插件握手使用的固定校验值。
	MagicCookieValue = "go-plugin-process-manager-v1"
)

// Handshake 是 Master 与插件共享的启动握手配置。
var Handshake = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   MagicCookieKey,
	MagicCookieValue: MagicCookieValue,
}

// BrokerAware 由需要主动回调 Master 的插件服务实现。
type BrokerAware interface {
	// SetBroker 注入当前插件进程的 GRPCBroker。
	SetBroker(broker *plugin.GRPCBroker)
}

// GRPCPlugin 把生成的 gRPC 服务接入 HashiCorp go-plugin。
type GRPCPlugin struct {
	plugin.NetRPCUnsupportedPlugin
	// Impl 是运行在插件进程中的通用服务实现。
	Impl gen.PluginServiceServer
}

// GRPCServer 在插件进程的 gRPC 服务端注册通用插件服务。
func (p *GRPCPlugin) GRPCServer(broker *plugin.GRPCBroker, server *grpc.Server) error {
	if aware, ok := p.Impl.(BrokerAware); ok {
		aware.SetBroker(broker)
	}
	gen.RegisterPluginServiceServer(server, p.Impl)
	return nil
}

// GRPCClient 构造供 Master 调用的通用插件客户端。
func (p *GRPCPlugin) GRPCClient(_ context.Context, broker *plugin.GRPCBroker, conn *grpc.ClientConn) (any, error) {
	return &Client{rpc: gen.NewPluginServiceClient(conn), broker: broker}, nil
}

// Client 是 Master 侧的插件 RPC 代理。
type Client struct {
	// rpc 是 Protobuf 生成的插件服务客户端。
	rpc gen.PluginServiceClient
	// broker 用于建立由插件回连 Master 的独立 gRPC 通道。
	broker *plugin.GRPCBroker
}

// GetInfo 查询插件类型、版本和 PID。
func (c *Client) GetInfo(ctx context.Context) (*gen.PluginInfo, error) {
	return c.rpc.GetInfo(ctx, &gen.GetInfoRequest{})
}

// Health 查询插件业务健康状态。
func (c *Client) Health(ctx context.Context) (*gen.HealthResponse, error) {
	return c.rpc.Health(ctx, &gen.HealthRequest{})
}

// SendMessage 将通用业务消息发送给插件并返回同一 RPC 的响应。
func (c *Client) SendMessage(ctx context.Context, message *gen.Message) (*gen.MessageResponse, error) {
	return c.rpc.SendMessage(ctx, &gen.MessageRequest{Message: message})
}

// ConfigureHost 启动 Master 回调服务，并让插件通过 GRPCBroker 回连。
func (c *Client) ConfigureHost(ctx context.Context, pluginID string, generation int64, host gen.HostServiceServer) error {
	brokerID := c.broker.NextId()
	go c.broker.AcceptAndServe(brokerID, func(options []grpc.ServerOption) *grpc.Server {
		server := grpc.NewServer(options...)
		gen.RegisterHostServiceServer(server, host)
		return server
	})
	_, err := c.rpc.ConfigureHost(ctx, &gen.ConfigureHostRequest{
		BrokerId: brokerID, PluginId: pluginID, Generation: generation,
	})
	return err
}

// Shutdown 请求插件停止自身的后台任务；进程回收仍由 Master 负责。
func (c *Client) Shutdown(ctx context.Context) error {
	_, err := c.rpc.Shutdown(ctx, &gen.ShutdownRequest{})
	return err
}
