// Package storage 提供使用原子文件替换的本地 JSON 持久化。
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gcc798/go-plugin-process-manager/internal/master/model"
)

const (
	// MaxHistoryRecords 是每类历史记录的默认保留上限。
	MaxHistoryRecords = 10000
)

// Store 管理所有 JSON 文件，并用单锁保证进程内写入串行。
type Store struct {
	// mu 保护所有文件的读改写序列。
	mu sync.Mutex
	// dir 是 JSON 数据目录的绝对路径。
	dir string
}

// New 创建并初始化 JSON 数据存储。
func New(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析数据目录: %w", err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("创建数据目录: %w", err)
	}
	store := &Store{dir: abs}
	for _, name := range []string{"plugins.json", "runtime.json", "events.json", "interactions.json", "restarts.json"} {
		if err := store.initialize(name); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// initialize 在文件缺失时写入空数组，已有损坏文件不会被覆盖。
func (s *Store) initialize(name string) error {
	path := filepath.Join(s.dir, name)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s.atomicWrite(path, []byte("[]\n"))
	}
	if err != nil {
		return fmt.Errorf("读取 %s: %w", name, err)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("JSON 文件 %s 已损坏: %w", name, err)
	}
	return nil
}

// load 读取并反序列化指定文件。
func (s *Store) load(name string, target any) error {
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("解析 %s: %w", name, err)
	}
	return nil
}

// save 序列化并原子替换指定文件；调用方必须持有 s.mu。
func (s *Store) save(name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 %s: %w", name, err)
	}
	return s.atomicWrite(filepath.Join(s.dir, name), append(data, '\n'))
}

// atomicWrite 先同步临时文件再 rename，避免读到半写入内容。
func (s *Store) atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(s.dir, ".json-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Plugins 返回全部插件定义。
func (s *Store) Plugins() ([]model.PluginDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var values []model.PluginDefinition
	return values, s.load("plugins.json", &values)
}

// SavePlugins 同步保存全部插件定义。
func (s *Store) SavePlugins(values []model.PluginDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save("plugins.json", values)
}

// Runtime 返回全部运行快照。
func (s *Store) Runtime() ([]model.RuntimeSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var values []model.RuntimeSnapshot
	return values, s.load("runtime.json", &values)
}

// SaveRuntime 同步保存全部运行快照。
func (s *Store) SaveRuntime(values []model.RuntimeSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save("runtime.json", values)
}

// Events 返回全部生命周期事件。
func (s *Store) Events() ([]model.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var values []model.Event
	return values, s.load("events.json", &values)
}

// AppendEvent 追加一条生命周期事件。
func (s *Store) AppendEvent(value model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var values []model.Event
	if err := s.load("events.json", &values); err != nil {
		return err
	}
	values = boundedAppend(values, value)
	return s.save("events.json", values)
}

// Interactions 返回全部业务交互记录。
func (s *Store) Interactions() ([]model.Interaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var values []model.Interaction
	return values, s.load("interactions.json", &values)
}

// AppendInteraction 追加一条完整 RPC 交互记录。
func (s *Store) AppendInteraction(value model.Interaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var values []model.Interaction
	if err := s.load("interactions.json", &values); err != nil {
		return err
	}
	values = boundedAppend(values, value)
	return s.save("interactions.json", values)
}

// Restarts 返回全部自动重启记录。
func (s *Store) Restarts() ([]model.RestartRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var values []model.RestartRecord
	return values, s.load("restarts.json", &values)
}

// AppendRestart 追加一条自动重启记录。
func (s *Store) AppendRestart(value model.RestartRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var values []model.RestartRecord
	if err := s.load("restarts.json", &values); err != nil {
		return err
	}
	values = boundedAppend(values, value)
	return s.save("restarts.json", values)
}

// boundedAppend 追加元素并裁剪最旧的超限历史。
func boundedAppend[T any](values []T, value T) []T {
	values = append(values, value)
	if len(values) > MaxHistoryRecords {
		values = values[len(values)-MaxHistoryRecords:]
	}
	return values
}
