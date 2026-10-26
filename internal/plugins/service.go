// Package plugins 提供内置插件共享的通用 RPC 服务骨架。
package plugins

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	gen "github.com/gcc798/go-plugin-process-manager/pkg/plugincontract/gen"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// MessageHandler 处理一个插件自有的通用业务消息。
type MessageHandler func(ctx context.Context, message *gen.Message) (string, error)

// Service 实现插件契约中与具体业务无关的部分。
type Service struct {
	gen.UnimplementedPluginServiceServer
	// PluginType 是插件类型。
	PluginType string
	// Version 是插件版本。
	Version string
	// Logger 输出由 go-plugin 转发给 Master 的结构化日志。
	Logger hclog.Logger
	// Handle 负责插件自有消息类型。
	Handle MessageHandler
	// OnConfigured 在 Host Callback 建立后启动插件主动任务。
	OnConfigured func()
	// mu 保护 Broker、回调客户端和实例身份。
	mu sync.RWMutex
	// broker 用于从插件进程回连 Master。
	broker *plugin.GRPCBroker
	// host 是插件主动上报使用的回调客户端。
	host gen.HostServiceClient
	// hostConn 是需要在插件关闭时释放的回调连接。
	hostConn *grpc.ClientConn
	// pluginID 是 Master 分配的实例标识。
	pluginID string
	// generation 是 Master 分配的当前实例代次。
	generation int64
	// done 在插件收到关闭请求时关闭。
	done chan struct{}
	// stopOnce 保证关闭信号只发送一次。
	stopOnce sync.Once
}

// NewService 创建一个通用插件服务。
func NewService(pluginType, version string, logger hclog.Logger) *Service {
	return &Service{PluginType: pluginType, Version: version, Logger: logger, done: make(chan struct{})}
}

// SetBroker 注入 go-plugin 为该插件进程创建的 GRPCBroker。
func (s *Service) SetBroker(broker *plugin.GRPCBroker) {
	s.mu.Lock()
	s.broker = broker
	s.mu.Unlock()
}

// GetInfo 返回插件进程身份信息。
func (s *Service) GetInfo(context.Context, *gen.GetInfoRequest) (*gen.PluginInfo, error) {
	return &gen.PluginInfo{Type: s.PluginType, Version: s.Version, Pid: int64(os.Getpid())}, nil
}

// Health 返回插件业务健康结果。
func (s *Service) Health(context.Context, *gen.HealthRequest) (*gen.HealthResponse, error) {
	return &gen.HealthResponse{Healthy: true, Message: "ready"}, nil
}

// SendMessage 调用当前插件的业务消息处理函数。
func (s *Service) SendMessage(ctx context.Context, request *gen.MessageRequest) (*gen.MessageResponse, error) {
	if request.GetMessage() == nil {
		return nil, fmt.Errorf("消息不能为空")
	}
	if s.Handle == nil {
		return nil, fmt.Errorf("插件未配置消息处理器")
	}
	payload, err := s.Handle(ctx, request.Message)
	response := &gen.MessageResponse{
		MessageId: request.Message.MessageId, CorrelationId: request.Message.CorrelationId,
		PayloadJson: payload, Success: err == nil, RespondedAt: time.Now().Format(time.RFC3339Nano),
	}
	if err != nil {
		response.Error = err.Error()
	}
	return response, nil
}

// ConfigureHost 通过 Broker 回连 Master 的 HostService。
func (s *Service) ConfigureHost(_ context.Context, request *gen.ConfigureHostRequest) (*gen.ConfigureHostResponse, error) {
	s.mu.Lock()
	if s.broker == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("GRPCBroker 尚未初始化")
	}
	if s.hostConn != nil {
		s.hostConn.Close()
	}
	conn, err := s.broker.Dial(request.BrokerId)
	if err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("连接 Master 回调服务: %w", err)
	}
	s.hostConn = conn
	s.host = gen.NewHostServiceClient(conn)
	s.pluginID = request.PluginId
	s.generation = request.Generation
	s.mu.Unlock()
	if s.OnConfigured != nil {
		s.OnConfigured()
	}
	s.Logger.Info("Host Callback 已建立", "component", "plugin", "plugin_id", request.PluginId, "pid", os.Getpid(), "generation", request.Generation)
	return &gen.ConfigureHostResponse{Configured: true}, nil
}

// Shutdown 停止后台任务并释放回调连接。
func (s *Service) Shutdown(context.Context, *gen.ShutdownRequest) (*gen.ShutdownResponse, error) {
	s.stopOnce.Do(func() { close(s.done) })
	s.mu.Lock()
	if s.hostConn != nil {
		s.hostConn.Close()
		s.hostConn = nil
		s.host = nil
	}
	s.mu.Unlock()
	return &gen.ShutdownResponse{Accepted: true}, nil
}

// Callback 返回当前 Host 回调客户端和实例身份。
func (s *Service) Callback() (gen.HostServiceClient, string, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.host, s.pluginID, s.generation
}

// Done 返回插件后台任务退出信号。
func (s *Service) Done() <-chan struct{} {
	return s.done
}
