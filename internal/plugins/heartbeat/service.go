// Package heartbeat 实现内置主动上报心跳插件。
package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gcc-798/go-plugin-process-manager/internal/plugins"
	gen "github.com/gcc-798/go-plugin-process-manager/pkg/plugincontract/gen"
	"github.com/hashicorp/go-hclog"
)

// Service 组合通用服务与心跳业务状态。
type Service struct {
	*plugins.Service
	// intervalMS 保存当前心跳间隔毫秒数。
	intervalMS atomic.Int64
	// paused 表示心跳是否暂停。
	paused atomic.Bool
	// sequence 保存当前进程内递增序列号。
	sequence atomic.Int64
	// intervalChanged 唤醒循环以立即应用新间隔。
	intervalChanged chan struct{}
	// startOnce 保证回调重复配置时不创建重复心跳循环。
	startOnce sync.Once
	// wait 等待心跳循环退出。
	wait sync.WaitGroup
}

// New 创建心跳插件服务，默认每五秒主动上报一次。
func New(version string, logger hclog.Logger) *Service {
	service := &Service{Service: plugins.NewService("heartbeat", version, logger), intervalChanged: make(chan struct{}, 1)}
	service.intervalMS.Store(5000)
	service.Handle = service.handle
	service.OnConfigured = func() {
		service.startOnce.Do(func() { service.wait.Go(service.reportLoop) })
	}
	return service
}

// handle 处理心跳间隔、暂停、恢复和状态查询消息。
func (s *Service) handle(_ context.Context, message *gen.Message) (string, error) {
	switch message.MessageType {
	case "heartbeat.set_interval":
		var payload struct {
			// IntervalSeconds 是新的心跳间隔秒数。
			IntervalSeconds int64 `json:"interval_seconds"`
		}
		if err := json.Unmarshal([]byte(message.PayloadJson), &payload); err != nil {
			return "", fmt.Errorf("解析心跳间隔: %w", err)
		}
		if payload.IntervalSeconds < 1 {
			return "", fmt.Errorf("interval_seconds 必须大于零")
		}
		s.intervalMS.Store(payload.IntervalSeconds * 1000)
		select {
		case s.intervalChanged <- struct{}{}:
		default:
		}
	case "heartbeat.pause":
		s.paused.Store(true)
	case "heartbeat.resume":
		s.paused.Store(false)
	case "heartbeat.get_stats":
	default:
		return "", fmt.Errorf("未知消息类型 %q", message.MessageType)
	}
	data, err := json.Marshal(map[string]any{
		"interval_seconds": s.intervalMS.Load() / 1000,
		"paused":           s.paused.Load(),
		"sequence":         s.sequence.Load(),
	})
	if err != nil {
		return "", fmt.Errorf("编码心跳状态: %w", err)
	}
	return string(data), nil
}

// reportLoop 按动态间隔触发插件主动上报。
func (s *Service) reportLoop() {
	for {
		timer := time.NewTimer(time.Duration(s.intervalMS.Load()) * time.Millisecond)
		select {
		case <-s.Done():
			timer.Stop()
			return
		case <-s.intervalChanged:
			timer.Stop()
			continue
		case <-timer.C:
			if !s.paused.Load() {
				s.report()
			}
		}
	}
}

// report 向 Master 发起一次独立的 ReportMessage RPC 并记录 ACK。
func (s *Service) report() {
	host, pluginID, generation := s.Callback()
	if host == nil {
		return
	}
	sequence := s.sequence.Add(1)
	now := time.Now()
	payload, _ := json.Marshal(map[string]any{
		"instance_id": fmt.Sprintf("%s-%d", pluginID, generation),
		"sequence":    sequence,
		"pid":         os.Getpid(),
		"time":        now.Format(time.RFC3339Nano),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := host.ReportMessage(ctx, &gen.MessageRequest{Message: &gen.Message{
		MessageId: fmt.Sprintf("heartbeat-%d-%d", generation, sequence),
		PluginId:  pluginID, MessageType: "heartbeat.tick", PayloadJson: string(payload),
		SentAt: now.Format(time.RFC3339Nano), Generation: generation,
	}})
	if err != nil {
		s.Logger.Error("主动上报失败", "component", "plugin", "plugin_id", pluginID, "pid", os.Getpid(), "generation", generation, "error", err)
		return
	}
	s.Logger.Info("收到 Master ACK", "component", "plugin", "plugin_id", pluginID, "pid", os.Getpid(), "generation", generation, "message_id", response.MessageId)
}
