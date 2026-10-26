// Package timestamp 实现内置时间戳插件的业务消息。
package timestamp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/gcc798/go-plugin-process-manager/internal/plugins"
	gen "github.com/gcc798/go-plugin-process-manager/pkg/plugincontract/gen"
	"github.com/hashicorp/go-hclog"
)

// New 创建指定版本的时间戳插件服务。
func New(version string, logger hclog.Logger) *plugins.Service {
	service := plugins.NewService("timestamp", version, logger)
	service.Handle = func(ctx context.Context, message *gen.Message) (string, error) {
		return handle(ctx, version, message)
	}
	return service
}

// handle 处理时间戳生成和慢请求消息。
func handle(ctx context.Context, version string, message *gen.Message) (string, error) {
	if message.MessageType == "timestamp.slow" {
		var payload struct {
			// DelayMS 是模拟阻塞的毫秒数。
			DelayMS int `json:"delay_ms"`
		}
		if err := json.Unmarshal([]byte(message.PayloadJson), &payload); err != nil {
			return "", fmt.Errorf("解析慢请求参数: %w", err)
		}
		timer := time.NewTimer(time.Duration(payload.DelayMS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
		}
	} else if message.MessageType != "timestamp.generate" {
		return "", fmt.Errorf("未知消息类型 %q", message.MessageType)
	}
	now := time.Now()
	result := map[string]any{
		"plugin_id":    message.PluginId,
		"version":      version,
		"timestamp_ms": now.UnixMilli(),
	}
	if version == "2.0.0" {
		result["formatted_time"] = now.Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("编码时间戳响应 %s: %w", strconv.Quote(message.MessageId), err)
	}
	return string(data), nil
}
