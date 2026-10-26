// Package logger 创建同时写终端、文件和有界内存缓冲的结构化日志器。
package logger

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hashicorp/go-hclog"
)

const (
	// MaxBufferedLines 是 HTTP 日志查询保留的最大行数。
	MaxBufferedLines = 2000
)

// Buffer 保存最近的完整日志行。
type Buffer struct {
	// mu 保护 lines 和 pending。
	mu sync.Mutex
	// lines 是已经写完换行符的日志行。
	lines []string
	// pending 保存跨 Write 调用的未完成行。
	pending []byte
}

// Write 接收 hclog 输出并按行存入有界缓冲。
func (b *Buffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(data)
	b.pending = append(b.pending, data...)
	for {
		index := bytes.IndexByte(b.pending, '\n')
		if index < 0 {
			break
		}
		b.lines = append(b.lines, string(b.pending[:index]))
		b.pending = b.pending[index+1:]
	}
	if len(b.lines) > MaxBufferedLines {
		b.lines = append([]string(nil), b.lines[len(b.lines)-MaxBufferedLines:]...)
	}
	return original, nil
}

// Query 返回按插件和级别过滤后的最新日志行。
func (b *Buffer) Query(pluginID, level string, limit int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	result := make([]string, 0, limit)
	pluginNeedle := "plugin_id=" + pluginID
	levelNeedle := "[" + strings.ToUpper(level) + "]"
	for index := len(b.lines) - 1; index >= 0 && len(result) < limit; index-- {
		line := b.lines[index]
		if pluginID != "" && !strings.Contains(line, pluginNeedle) && !strings.Contains(line, `"plugin_id":"`+pluginID+`"`) {
			continue
		}
		if level != "" && !strings.Contains(strings.ToUpper(line), levelNeedle) && !strings.Contains(strings.ToUpper(line), `"LEVEL":"`+strings.ToUpper(level)+`"`) {
			continue
		}
		result = append(result, line)
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

// New 创建 Master 日志器、查询缓冲和文件关闭函数。
func New(dir, level string) (hclog.Logger, *Buffer, func() error, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, nil, fmt.Errorf("创建日志目录: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "master.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("打开日志文件: %w", err)
	}
	buffer := &Buffer{}
	parsed := hclog.LevelFromString(level)
	if parsed == hclog.NoLevel {
		parsed = hclog.Info
	}
	log := hclog.New(&hclog.LoggerOptions{
		Name: "master", Level: parsed, Output: io.MultiWriter(os.Stdout, file, buffer), IncludeLocation: false,
	})
	return log, buffer, file.Close, nil
}
