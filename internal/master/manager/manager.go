// Package manager 实现插件注册、生命周期、健康、恢复、更新和通信。
package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gcc798/go-plugin-process-manager/internal/master/model"
	"github.com/gcc798/go-plugin-process-manager/internal/master/storage"
	"github.com/gcc798/go-plugin-process-manager/pkg/plugincontract"
	gen "github.com/gcc798/go-plugin-process-manager/pkg/plugincontract/gen"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
)

const (
	// DefaultHealthInterval 是自动健康检查间隔。
	DefaultHealthInterval = 3 * time.Second
	// DefaultRPCTimeout 是普通 RPC 和健康检查超时。
	DefaultRPCTimeout = 2 * time.Second
	// DefaultFailureThreshold 是进入 UNHEALTHY 的连续失败次数。
	DefaultFailureThreshold = 3
	// TimestampSampleInterval 是 Master 自动请求时间戳的间隔。
	TimestampSampleInterval = 5 * time.Second
	// MaxPluginBinaryBytes 是单个上传插件二进制的最大字节数。
	MaxPluginBinaryBytes int64 = 100 << 20
)

// idPattern 限制插件 ID，避免日志注入和路径语义混入标识。
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// versionPattern 限制上传版本目录名，避免路径穿越和异常文件名。
var versionPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,63}$`)

// ErrNotFound 表示插件实例不存在。
var ErrNotFound = errors.New("插件不存在")

// ErrConflict 表示生命周期操作与当前状态冲突。
var ErrConflict = errors.New("生命周期操作冲突")

// instance 保存单个正在运行的 go-plugin 客户端。
type instance struct {
	// client 负责 OS 子进程生命周期。
	client *plugin.Client
	// protocol 提供 go-plugin RPC Ping。
	protocol plugin.ClientProtocol
	// rpc 是通用插件业务代理。
	rpc *plugincontract.Client
	// pid 是操作系统进程号。
	pid int
	// version 是插件自报版本。
	version string
	// generation 是本次进程代次。
	generation int64
	// cancel 停止该实例的监控和采样任务。
	cancel context.CancelFunc
}

// runtime 组合持久化快照和仅存在于当前 Master 进程的句柄。
type runtime struct {
	// operation 串行化 Start、Stop、Restart 和 Update。
	operation sync.Mutex
	// calls 阻止更新或停止切断正在执行的业务调用。
	calls sync.RWMutex
	// mu 保护 snapshot、instance 和 failureCount。
	mu sync.RWMutex
	// definition 是插件注册定义。
	definition model.PluginDefinition
	// snapshot 是最近运行状态。
	snapshot model.RuntimeSnapshot
	// instance 是当前生效进程；停止时为 nil。
	instance *instance
	// failureCount 是连续业务健康失败数。
	failureCount int
	// restartAttempt 是当前连续自动重试次数。
	restartAttempt int
}

// Manager 管理所有插件实例和持久化审计信息。
type Manager struct {
	// mu 保护 runtimes 映射。
	mu sync.RWMutex
	// runtimes 按插件 ID 保存运行时。
	runtimes map[string]*runtime
	// store 保存定义、快照和历史。
	store *storage.Store
	// logger 输出 Master 及 go-plugin 转发日志。
	logger hclog.Logger
	// allowedRoot 是允许执行插件二进制的目录。
	allowedRoot string
	// rootCtx 在 Master 退出时取消所有后台任务。
	rootCtx context.Context
	// rootCancel 触发 Master 全局退出。
	rootCancel context.CancelFunc
	// subscriptionMu 保护实时状态订阅集合。
	subscriptionMu sync.Mutex
	// subscriptions 保存 SSE 客户端对应的非阻塞通知通道。
	subscriptions map[chan struct{}]struct{}
}

// New 从 JSON 存储恢复注册定义；旧 PID 只作为历史，不认定为存活。
func New(store *storage.Store, logger hclog.Logger, allowedRoot string) (*Manager, error) {
	root, err := filepath.Abs(allowedRoot)
	if err != nil {
		return nil, fmt.Errorf("解析插件目录: %w", err)
	}
	definitions, err := store.Plugins()
	if err != nil {
		return nil, err
	}
	previous, err := store.Runtime()
	if err != nil {
		return nil, err
	}
	previousByID := make(map[string]model.RuntimeSnapshot, len(previous))
	for _, snapshot := range previous {
		previousByID[snapshot.PluginID] = snapshot
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{runtimes: make(map[string]*runtime), store: store, logger: logger, allowedRoot: root, rootCtx: ctx, rootCancel: cancel, subscriptions: make(map[chan struct{}]struct{})}
	for _, definition := range definitions {
		snapshot := previousByID[definition.ID]
		snapshot.PluginID = definition.ID
		snapshot.PID = 0
		snapshot.ProcessAlive = false
		snapshot.RPCConnected = false
		snapshot.Healthy = false
		snapshot.ObservedState = model.StateStopped
		snapshot.DesiredState = model.DesiredStopped
		snapshot.LastObservedAt = time.Now()
		manager.runtimes[definition.ID] = &runtime{definition: definition, snapshot: snapshot}
	}
	if err := manager.persistRuntime(); err != nil {
		return nil, err
	}
	return manager, nil
}

// StartAutoStart 启动注册时声明为自动启动的插件。
func (m *Manager) StartAutoStart() {
	for _, definition := range m.Definitions() {
		if definition.AutoStart {
			go func(id string) {
				if err := m.Start(context.Background(), id); err != nil {
					m.logger.Error("自动启动插件失败", "component", "master", "plugin_id", id, "error", err)
				}
			}(definition.ID)
		}
	}
}

// Register 校验并持久化一个新插件定义。
func (m *Manager) Register(definition model.PluginDefinition) error {
	if !idPattern.MatchString(definition.ID) {
		return fmt.Errorf("插件 ID 格式无效")
	}
	if definition.Type == "" || definition.Version == "" {
		return fmt.Errorf("type 和 version 不能为空")
	}
	path, err := m.validateBinary(definition.BinaryPath)
	if err != nil {
		return err
	}
	definition.BinaryPath = path
	if definition.Restart.MaxRetries == 0 {
		definition.Restart = model.RestartPolicy{Enabled: true, MaxRetries: 5, InitialDelaySeconds: 1, MaxDelaySeconds: 30, StableResetSeconds: 60}
	}
	m.mu.Lock()
	if _, exists := m.runtimes[definition.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("%w: 插件 %s 已存在", ErrConflict, definition.ID)
	}
	m.runtimes[definition.ID] = &runtime{definition: definition, snapshot: model.RuntimeSnapshot{
		PluginID: definition.ID, DesiredState: model.DesiredStopped, ObservedState: model.StateRegistered, Version: definition.Version, LastObservedAt: time.Now(),
	}}
	m.mu.Unlock()
	if err := m.persistDefinitions(); err != nil {
		m.mu.Lock()
		delete(m.runtimes, definition.ID)
		m.mu.Unlock()
		return err
	}
	m.event(definition.ID, "PLUGIN_REGISTERED", map[string]any{"type": definition.Type, "version": definition.Version})
	return m.persistRuntime()
}

// Delete 删除一个已停止的插件定义。
func (m *Manager) Delete(id string) error {
	runtime, err := m.lookup(id)
	if err != nil {
		return err
	}
	runtime.operation.Lock()
	defer runtime.operation.Unlock()
	runtime.mu.RLock()
	running := runtime.instance != nil
	state := runtime.snapshot.ObservedState
	runtime.mu.RUnlock()
	if running || (state != model.StateStopped && state != model.StateRegistered && state != model.StateFailed) {
		return fmt.Errorf("%w: 只能删除已停止插件", ErrConflict)
	}
	m.mu.Lock()
	delete(m.runtimes, id)
	m.mu.Unlock()
	if err := m.persistDefinitions(); err != nil {
		m.mu.Lock()
		m.runtimes[id] = runtime
		m.mu.Unlock()
		return err
	}
	m.event(id, "PLUGIN_DELETED", nil)
	return m.persistRuntime()
}

// Start 启动指定插件，重复启动会返回冲突。
func (m *Manager) Start(ctx context.Context, id string) error {
	runtime, err := m.lookup(id)
	if err != nil {
		return err
	}
	runtime.operation.Lock()
	defer runtime.operation.Unlock()
	return m.startLocked(ctx, runtime, false)
}

// startLocked 在生命周期锁内启动并切换新实例。
func (m *Manager) startLocked(ctx context.Context, runtime *runtime, restarting bool) error {
	runtime.mu.Lock()
	if runtime.instance != nil {
		runtime.mu.Unlock()
		return fmt.Errorf("%w: 插件已经运行", ErrConflict)
	}
	runtime.snapshot.DesiredState = model.DesiredRunning
	if restarting {
		runtime.snapshot.ObservedState = model.StateRestarting
	} else {
		runtime.snapshot.ObservedState = model.StateStarting
	}
	runtime.snapshot.LastObservedAt = time.Now()
	definition := runtime.definition
	generation := runtime.snapshot.Generation + 1
	runtime.mu.Unlock()
	m.persistRuntimeLogged()
	m.logger.Info("启动插件", "component", "master", "plugin_id", definition.ID, "plugin_type", definition.Type, "version", definition.Version, "generation", generation)
	created, err := m.spawn(ctx, definition, generation)
	if err != nil {
		runtime.mu.Lock()
		runtime.snapshot.ObservedState = model.StateFailed
		runtime.snapshot.ProcessAlive = false
		runtime.snapshot.RPCConnected = false
		runtime.snapshot.Healthy = false
		runtime.snapshot.LastError = err.Error()
		runtime.snapshot.LastObservedAt = time.Now()
		runtime.mu.Unlock()
		m.event(definition.ID, "PLUGIN_START_FAILED", map[string]any{"error": err.Error()})
		m.persistRuntimeLogged()
		return err
	}
	instanceCtx, cancel := context.WithCancel(m.rootCtx)
	created.cancel = cancel
	runtime.mu.Lock()
	runtime.instance = created
	runtime.snapshot.PID = created.pid
	runtime.snapshot.Version = created.version
	runtime.snapshot.Generation = generation
	runtime.snapshot.ProcessAlive = true
	runtime.snapshot.RPCConnected = true
	runtime.snapshot.Healthy = true
	runtime.snapshot.ObservedState = model.StateRunning
	runtime.snapshot.LastStartedAt = time.Now()
	runtime.snapshot.LastObservedAt = time.Now()
	runtime.snapshot.LastError = ""
	runtime.failureCount = 0
	runtime.mu.Unlock()
	if err := m.persistRuntime(); err != nil {
		created.cancel()
		created.client.Kill()
		runtime.mu.Lock()
		runtime.instance = nil
		runtime.snapshot.PID = 0
		runtime.snapshot.ProcessAlive = false
		runtime.snapshot.RPCConnected = false
		runtime.snapshot.Healthy = false
		runtime.snapshot.ObservedState = model.StateFailed
		runtime.snapshot.LastError = err.Error()
		runtime.mu.Unlock()
		m.persistRuntimeLogged()
		return fmt.Errorf("保存启动状态: %w", err)
	}
	m.event(definition.ID, "PLUGIN_STARTED", map[string]any{"pid": created.pid, "generation": generation, "version": created.version})
	m.logger.Info("插件运行正常", "component", "master", "plugin_id", definition.ID, "pid", created.pid, "generation", generation, "state", model.StateRunning)
	go m.monitor(instanceCtx, runtime, created)
	if definition.Type == "timestamp" {
		go m.timestampSampler(instanceCtx, definition.ID, generation)
	}
	return nil
}

// spawn 创建 OS 子进程，完成 go-plugin 握手、回调配置和首次健康验证。
func (m *Manager) spawn(ctx context.Context, definition model.PluginDefinition, generation int64) (*instance, error) {
	command := exec.Command(definition.BinaryPath, "-version", definition.Version)
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: plugincontract.Handshake,
		Plugins:         map[string]plugin.Plugin{plugincontract.PluginName: &plugincontract.GRPCPlugin{}},
		Cmd:             command, AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC}, Managed: true,
		Logger: m.logger.Named("plugin").With("component", "plugin", "plugin_id", definition.ID, "generation", generation),
	})
	protocol, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("go-plugin 握手失败: %w", err)
	}
	dispensed, err := protocol.Dispense(plugincontract.PluginName)
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("获取 RPC 代理: %w", err)
	}
	rpc, ok := dispensed.(*plugincontract.Client)
	if !ok {
		client.Kill()
		return nil, fmt.Errorf("RPC 代理类型错误 %T", dispensed)
	}
	checkCtx, cancel := context.WithTimeout(ctx, DefaultRPCTimeout)
	defer cancel()
	info, err := rpc.GetInfo(checkCtx)
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("查询插件信息: %w", err)
	}
	if info.Type != definition.Type || info.Version != definition.Version {
		client.Kill()
		return nil, fmt.Errorf("插件身份不匹配: 得到 %s/%s", info.Type, info.Version)
	}
	callback := &hostCallback{manager: m, pluginID: definition.ID, generation: generation}
	if err := rpc.ConfigureHost(checkCtx, definition.ID, generation, callback); err != nil {
		client.Kill()
		return nil, fmt.Errorf("建立 Host Callback: %w", err)
	}
	health, err := rpc.Health(checkCtx)
	if err != nil || health == nil || !health.Healthy {
		client.Kill()
		return nil, fmt.Errorf("首次业务健康检查失败: %v %v", health, err)
	}
	pid := int(info.Pid)
	return &instance{client: client, protocol: protocol, rpc: rpc, pid: pid, version: info.Version, generation: generation}, nil
}

// Stop 设置期望状态为停止，并优雅回收当前插件进程。
func (m *Manager) Stop(ctx context.Context, id string) error {
	runtime, err := m.lookup(id)
	if err != nil {
		return err
	}
	runtime.operation.Lock()
	defer runtime.operation.Unlock()
	return m.stopLocked(ctx, runtime, true)
}

// stopLocked 在生命周期锁内停止实例，用户停止会禁止自动重启。
func (m *Manager) stopLocked(ctx context.Context, runtime *runtime, userRequested bool) error {
	runtime.calls.Lock()
	defer runtime.calls.Unlock()
	runtime.mu.Lock()
	current := runtime.instance
	if current == nil {
		runtime.mu.Unlock()
		return fmt.Errorf("%w: 插件未运行", ErrConflict)
	}
	if userRequested {
		runtime.snapshot.DesiredState = model.DesiredStopped
	}
	runtime.snapshot.ObservedState = model.StateStopping
	runtime.snapshot.LastObservedAt = time.Now()
	runtime.mu.Unlock()
	if err := m.persistRuntime(); err != nil {
		return fmt.Errorf("保存停止状态: %w", err)
	}
	current.cancel()
	shutdownCtx, cancel := context.WithTimeout(ctx, DefaultRPCTimeout)
	_ = current.rpc.Shutdown(shutdownCtx)
	cancel()
	current.client.Kill()
	runtime.mu.Lock()
	if runtime.instance == current {
		runtime.instance = nil
	}
	runtime.snapshot.PID = 0
	runtime.snapshot.ProcessAlive = false
	runtime.snapshot.RPCConnected = false
	runtime.snapshot.Healthy = false
	runtime.snapshot.ObservedState = model.StateStopped
	runtime.snapshot.LastStoppedAt = time.Now()
	runtime.snapshot.LastObservedAt = time.Now()
	runtime.mu.Unlock()
	m.event(runtime.definition.ID, "PLUGIN_STOPPED", map[string]any{"pid": current.pid})
	if err := m.persistRuntime(); err != nil {
		return fmt.Errorf("保存停止完成状态: %w", err)
	}
	m.logger.Info("插件进程已退出", "component", "master", "plugin_id", runtime.definition.ID, "pid", current.pid)
	return nil
}

// Restart 手动停止并重新创建插件进程。
func (m *Manager) Restart(ctx context.Context, id string) error {
	runtime, err := m.lookup(id)
	if err != nil {
		return err
	}
	runtime.operation.Lock()
	defer runtime.operation.Unlock()
	runtime.mu.RLock()
	running := runtime.instance != nil
	runtime.mu.RUnlock()
	if !running {
		return m.startLocked(ctx, runtime, true)
	}
	if err := m.stopLocked(ctx, runtime, false); err != nil {
		return err
	}
	return m.startLocked(ctx, runtime, true)
}

// Update 先验证新实例，再原子切换业务代理；失败时保留旧实例。
func (m *Manager) Update(ctx context.Context, id, version, binaryPath string) error {
	runtime, err := m.lookup(id)
	if err != nil {
		return err
	}
	path, err := m.validateBinary(binaryPath)
	if err != nil {
		return err
	}
	runtime.operation.Lock()
	defer runtime.operation.Unlock()
	runtime.mu.Lock()
	old := runtime.instance
	if old == nil {
		runtime.mu.Unlock()
		return fmt.Errorf("%w: 更新要求插件正在运行", ErrConflict)
	}
	oldDefinition := runtime.definition
	newDefinition := oldDefinition
	newDefinition.Version = version
	newDefinition.BinaryPath = path
	generation := runtime.snapshot.Generation + 1
	runtime.snapshot.ObservedState = model.StateUpdating
	runtime.snapshot.LastObservedAt = time.Now()
	runtime.mu.Unlock()
	m.event(id, "PLUGIN_UPDATE_STARTED", map[string]any{"old_version": oldDefinition.Version, "new_version": version})
	candidate, err := m.spawn(ctx, newDefinition, generation)
	if err != nil {
		runtime.mu.Lock()
		runtime.snapshot.ObservedState = model.StateRunning
		runtime.snapshot.LastError = err.Error()
		runtime.mu.Unlock()
		m.event(id, "PLUGIN_UPDATE_FAILED", map[string]any{"error": err.Error()})
		m.persistRuntimeLogged()
		return err
	}
	instanceCtx, cancel := context.WithCancel(m.rootCtx)
	candidate.cancel = cancel
	runtime.calls.Lock()
	runtime.mu.Lock()
	runtime.definition = newDefinition
	runtime.instance = candidate
	runtime.snapshot.Version = version
	runtime.snapshot.Generation = generation
	runtime.snapshot.PID = candidate.pid
	runtime.snapshot.ObservedState = model.StateRunning
	runtime.snapshot.ProcessAlive = true
	runtime.snapshot.RPCConnected = true
	runtime.snapshot.Healthy = true
	runtime.snapshot.LastObservedAt = time.Now()
	runtime.snapshot.LastStartedAt = time.Now()
	runtime.snapshot.LastError = ""
	runtime.mu.Unlock()
	if err := m.persistDefinitions(); err != nil {
		runtime.mu.Lock()
		runtime.definition = oldDefinition
		runtime.instance = old
		runtime.snapshot.Version = old.version
		runtime.snapshot.Generation = old.generation
		runtime.snapshot.PID = old.pid
		runtime.snapshot.LastError = err.Error()
		runtime.mu.Unlock()
		runtime.calls.Unlock()
		candidate.client.Kill()
		return err
	}
	if err := m.persistRuntime(); err != nil {
		runtime.mu.Lock()
		runtime.definition = oldDefinition
		runtime.instance = old
		runtime.snapshot.Version = old.version
		runtime.snapshot.Generation = old.generation
		runtime.snapshot.PID = old.pid
		runtime.snapshot.LastError = err.Error()
		runtime.mu.Unlock()
		_ = m.persistDefinitions()
		runtime.calls.Unlock()
		candidate.client.Kill()
		return fmt.Errorf("保存更新状态: %w", err)
	}
	runtime.calls.Unlock()
	old.cancel()
	old.client.Kill()
	m.event(id, "PLUGIN_UPDATED", map[string]any{"old_pid": old.pid, "new_pid": candidate.pid, "version": version})
	m.logger.Info("插件版本切换成功", "component", "master", "plugin_id", id, "version", version, "pid", candidate.pid, "generation", generation)
	go m.monitor(instanceCtx, runtime, candidate)
	if newDefinition.Type == "timestamp" {
		go m.timestampSampler(instanceCtx, id, generation)
	}
	return nil
}

// UploadAndUpdate 将上传内容安全写入允许目录，并使用该二进制更新运行中的插件。
func (m *Manager) UploadAndUpdate(ctx context.Context, id, version string, source io.Reader) (string, error) {
	if _, err := m.lookup(id); err != nil {
		return "", err
	}
	if !versionPattern.MatchString(version) {
		return "", fmt.Errorf("版本格式无效")
	}
	binaryPath, err := m.saveUploadedBinary(id, version, source, MaxPluginBinaryBytes)
	if err != nil {
		return "", err
	}
	if err := m.Update(ctx, id, version, binaryPath); err != nil {
		if removeErr := os.Remove(binaryPath); removeErr != nil {
			return "", errors.Join(err, fmt.Errorf("清理失败上传文件: %w", removeErr))
		}
		return "", err
	}
	m.event(id, "PLUGIN_BINARY_UPLOADED", map[string]any{"version": version, "binary_path": binaryPath})
	return binaryPath, nil
}

// saveUploadedBinary 限制上传大小，并通过同目录临时文件原子写入可执行文件。
func (m *Manager) saveUploadedBinary(id, version string, source io.Reader, maxBytes int64) (string, error) {
	directory := filepath.Join(m.allowedRoot, "uploads", id, version)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", fmt.Errorf("创建插件上传目录: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".plugin-upload-*")
	if err != nil {
		return "", fmt.Errorf("创建插件上传临时文件: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	written, writeErr := io.Copy(temporary, io.LimitReader(source, maxBytes+1))
	if writeErr == nil && written > maxBytes {
		writeErr = fmt.Errorf("插件二进制超过 %d 字节限制", maxBytes)
	}
	if writeErr == nil {
		writeErr = temporary.Sync()
	}
	if closeErr := temporary.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return "", fmt.Errorf("写入插件二进制: %w", writeErr)
	}
	if err := os.Chmod(temporaryPath, 0o700); err != nil {
		return "", fmt.Errorf("设置插件执行权限: %w", err)
	}
	finalPath := filepath.Join(directory, "plugin-"+newID("bin"))
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return "", fmt.Errorf("发布插件二进制: %w", err)
	}
	return finalPath, nil
}

// SendMessage 发起 Master→Plugin 业务 RPC，并将响应合并到同一条交互记录。
func (m *Manager) SendMessage(ctx context.Context, id, messageType, payload string) (*gen.MessageResponse, error) {
	runtime, err := m.lookup(id)
	if err != nil {
		return nil, err
	}
	runtime.calls.RLock()
	defer runtime.calls.RUnlock()
	runtime.mu.RLock()
	current := runtime.instance
	runtime.mu.RUnlock()
	if current == nil {
		return nil, fmt.Errorf("%w: 插件未运行", ErrConflict)
	}
	messageID := newID("msg")
	started := time.Now()
	message := &gen.Message{MessageId: messageID, PluginId: id, MessageType: messageType, PayloadJson: payload, SentAt: started.Format(time.RFC3339Nano), Generation: current.generation}
	m.logger.Info("发送业务消息", "component", "master", "plugin_id", id, "pid", current.pid, "generation", current.generation, "message_id", messageID, "direction", "MASTER_TO_PLUGIN", "type", messageType)
	response, callErr := current.rpc.SendMessage(ctx, message)
	completed := time.Now()
	record := model.Interaction{MessageID: messageID, PluginID: id, Generation: current.generation, Direction: "MASTER_TO_PLUGIN", Initiator: "MASTER", Kind: "REQUEST_RESPONSE", MessageType: messageType, RequestPayload: payload, SentAt: started, CompletedAt: completed, DurationMS: completed.Sub(started).Milliseconds(), Status: "SUCCESS"}
	if callErr != nil {
		record.Status = "FAILED"
		record.Error = callErr.Error()
	} else if response == nil {
		record.Status = "FAILED"
		record.Error = "插件返回空响应"
		callErr = errors.New(record.Error)
	} else {
		record.ResponsePayload = response.PayloadJson
		if !response.Success {
			record.Status = "FAILED"
			record.Error = response.Error
			callErr = errors.New(response.Error)
		}
	}
	if err := m.store.AppendInteraction(record); err != nil {
		if callErr == nil {
			callErr = fmt.Errorf("保存交互记录: %w", err)
		}
	} else {
		m.notify()
	}
	m.touchInteraction(runtime, "MASTER_TO_PLUGIN", messageType, started, completed)
	m.logger.Info("收到业务响应", "component", "master", "plugin_id", id, "pid", current.pid, "generation", current.generation, "message_id", messageID, "direction", "PLUGIN_TO_MASTER", "duration_ms", record.DurationMS, "status", record.Status)
	return response, callErr
}

// hostCallback 将插件主动上报绑定到创建该回调服务的实例身份。
type hostCallback struct {
	gen.UnimplementedHostServiceServer
	// manager 接收并持久化上报。
	manager *Manager
	// pluginID 是该回调连接允许使用的插件标识。
	pluginID string
	// generation 是该回调连接允许使用的代次。
	generation int64
}

// ReportMessage 校验连接身份，并记录真正由插件发起的独立业务调用。
func (h *hostCallback) ReportMessage(_ context.Context, request *gen.MessageRequest) (*gen.MessageResponse, error) {
	message := request.GetMessage()
	if message == nil || message.PluginId != h.pluginID || message.Generation != h.generation {
		return nil, fmt.Errorf("插件上报身份无效")
	}
	return h.manager.report(message)
}

// report 验证上报来自当前生效实例，旧 generation 会被拒绝。
func (m *Manager) report(message *gen.Message) (*gen.MessageResponse, error) {
	runtime, err := m.lookup(message.PluginId)
	if err != nil {
		return nil, err
	}
	runtime.mu.RLock()
	current := runtime.instance
	runtime.mu.RUnlock()
	if current == nil || current.generation != message.Generation {
		return nil, fmt.Errorf("拒绝非当前实例上报")
	}
	started := time.Now()
	sentAt, parseErr := time.Parse(time.RFC3339Nano, message.SentAt)
	if parseErr != nil {
		sentAt = started
	}
	m.logger.Info("收到插件主动上报", "component", "master", "plugin_id", message.PluginId, "pid", current.pid, "generation", message.Generation, "message_id", message.MessageId, "direction", "PLUGIN_TO_MASTER", "type", message.MessageType)
	completed := time.Now()
	record := model.Interaction{MessageID: message.MessageId, CorrelationID: message.CorrelationId, PluginID: message.PluginId, Generation: message.Generation, Direction: "PLUGIN_TO_MASTER", Initiator: "PLUGIN", Kind: "ACTIVE_REPORT", MessageType: message.MessageType, RequestPayload: message.PayloadJson, ResponsePayload: `{"ack":true}`, SentAt: sentAt, CompletedAt: completed, DurationMS: completed.Sub(started).Milliseconds(), Status: "SUCCESS"}
	if err := m.store.AppendInteraction(record); err != nil {
		return nil, fmt.Errorf("保存主动上报: %w", err)
	}
	m.notify()
	m.touchInteraction(runtime, "PLUGIN_TO_MASTER", message.MessageType, sentAt, completed)
	response := &gen.MessageResponse{MessageId: message.MessageId, CorrelationId: message.CorrelationId, PayloadJson: `{"ack":true}`, Success: true, RespondedAt: completed.Format(time.RFC3339Nano)}
	m.logger.Info("已发送插件 ACK", "component", "master", "plugin_id", message.PluginId, "pid", current.pid, "generation", message.Generation, "message_id", message.MessageId, "direction", "MASTER_TO_PLUGIN")
	return response, nil
}

// touchInteraction 更新监控接口使用的最近业务交互信息。
func (m *Manager) touchInteraction(runtime *runtime, direction, messageType string, sentAt, receivedAt time.Time) {
	runtime.mu.Lock()
	runtime.snapshot.LastInteractionAt = time.Now()
	runtime.snapshot.LastInteractionDirection = direction
	runtime.snapshot.LastInteractionType = messageType
	runtime.snapshot.LastSentAt = sentAt
	runtime.snapshot.LastReceivedAt = receivedAt
	runtime.mu.Unlock()
	m.persistRuntimeLogged()
}

// monitor 区分进程、RPC 与业务健康，并检测意外退出。
func (m *Manager) monitor(ctx context.Context, runtime *runtime, watched *instance) {
	ticker := time.NewTicker(DefaultHealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if watched.client.Exited() {
				m.handleCrash(runtime, watched)
				return
			}
			m.checkHealth(runtime, watched)
		}
	}
}

// checkHealth 分别记录进程存活、RPC Ping 和业务 Health 的结果。
func (m *Manager) checkHealth(runtime *runtime, watched *instance) {
	ctx, cancel := context.WithTimeout(m.rootCtx, DefaultRPCTimeout)
	defer cancel()
	pingErr := watched.protocol.Ping()
	health, healthErr := watched.rpc.Health(ctx)
	now := time.Now()
	runtime.mu.Lock()
	if runtime.instance != watched {
		runtime.mu.Unlock()
		return
	}
	runtime.snapshot.ProcessAlive = !watched.client.Exited()
	runtime.snapshot.RPCConnected = pingErr == nil
	runtime.snapshot.LastHealthCheckAt = now
	runtime.snapshot.LastObservedAt = now
	wasHealthy := runtime.snapshot.Healthy
	healthy := pingErr == nil && healthErr == nil && health != nil && health.Healthy
	if healthy {
		runtime.failureCount = 0
		if runtime.definition.Restart.StableResetSeconds > 0 && time.Since(runtime.snapshot.LastStartedAt) >= time.Duration(runtime.definition.Restart.StableResetSeconds)*time.Second {
			runtime.restartAttempt = 0
		}
		runtime.snapshot.Healthy = true
		runtime.snapshot.ObservedState = model.StateRunning
		runtime.snapshot.LastError = ""
	} else {
		runtime.failureCount++
		runtime.snapshot.LastError = fmt.Sprintf("rpc_ping=%v business_health=%v", pingErr, healthErr)
		if runtime.failureCount >= DefaultFailureThreshold {
			runtime.snapshot.Healthy = false
			runtime.snapshot.ObservedState = model.StateUnhealthy
		}
	}
	nowHealthy := runtime.snapshot.Healthy
	runtime.mu.Unlock()
	if wasHealthy != nowHealthy {
		eventType := "PLUGIN_HEALTH_RECOVERED"
		if !nowHealthy {
			eventType = "PLUGIN_UNHEALTHY"
		}
		m.event(runtime.definition.ID, eventType, nil)
	}
	m.persistRuntimeLogged()
}

// handleCrash 在生命周期锁内确认当前实例后执行指数退避重启。
func (m *Manager) handleCrash(runtime *runtime, crashed *instance) {
	runtime.operation.Lock()
	defer runtime.operation.Unlock()
	runtime.mu.Lock()
	if runtime.instance != crashed {
		runtime.mu.Unlock()
		return
	}
	runtime.instance = nil
	runtime.snapshot.ProcessAlive = false
	runtime.snapshot.RPCConnected = false
	runtime.snapshot.Healthy = false
	runtime.snapshot.PID = 0
	runtime.snapshot.LastError = "插件进程异常退出"
	runtime.snapshot.LastObservedAt = time.Now()
	desired := runtime.snapshot.DesiredState
	policy := runtime.definition.Restart
	runtime.mu.Unlock()
	m.logger.Error("检测到插件异常退出", "component", "master", "plugin_id", runtime.definition.ID, "pid", crashed.pid, "generation", crashed.generation)
	m.event(runtime.definition.ID, "PLUGIN_CRASHED", map[string]any{"pid": crashed.pid, "generation": crashed.generation})
	if desired != model.DesiredRunning || !policy.Enabled {
		runtime.mu.Lock()
		runtime.snapshot.ObservedState = model.StateFailed
		runtime.mu.Unlock()
		m.persistRuntimeLogged()
		return
	}
	for runtime.restartAttempt < policy.MaxRetries {
		runtime.restartAttempt++
		attempt := runtime.restartAttempt
		delay := RestartDelay(policy, attempt)
		runtime.mu.Lock()
		runtime.snapshot.ObservedState = model.StateRestarting
		runtime.mu.Unlock()
		m.logger.Warn("准备自动重启", "component", "master", "plugin_id", runtime.definition.ID, "attempt", attempt, "delay", delay)
		select {
		case <-m.rootCtx.Done():
			return
		case <-time.After(delay):
		}
		started := time.Now()
		err := m.startLocked(m.rootCtx, runtime, true)
		record := model.RestartRecord{PluginID: runtime.definition.ID, OldPID: crashed.pid, Reason: "unexpected_exit", Attempt: attempt, StartedAt: started, CompletedAt: time.Now(), Success: err == nil}
		if err == nil {
			runtime.mu.Lock()
			record.NewPID = runtime.snapshot.PID
			runtime.snapshot.RestartCount++
			runtime.mu.Unlock()
			_ = m.store.AppendRestart(record)
			m.event(runtime.definition.ID, "PLUGIN_RESTARTED", map[string]any{"old_pid": crashed.pid, "new_pid": record.NewPID, "attempt": attempt})
			m.persistRuntimeLogged()
			return
		}
		record.Error = err.Error()
		_ = m.store.AppendRestart(record)
	}
	runtime.mu.Lock()
	runtime.snapshot.ObservedState = model.StateFailed
	runtime.snapshot.LastError = "自动重启次数已耗尽"
	runtime.mu.Unlock()
	m.event(runtime.definition.ID, "PLUGIN_RESTART_EXHAUSTED", nil)
	m.persistRuntimeLogged()
}

// RestartDelay 计算带上限的指数退避时长。
func RestartDelay(policy model.RestartPolicy, attempt int) time.Duration {
	initial := max(policy.InitialDelaySeconds, 1)
	maximum := max(policy.MaxDelaySeconds, initial)
	seconds := initial
	for range max(attempt-1, 0) {
		seconds = min(seconds*2, maximum)
	}
	return time.Duration(seconds) * time.Second
}

// timestampSampler 定时发送 timestamp.generate；这些都是 Master 发起的请求响应。
func (m *Manager) timestampSampler(ctx context.Context, id string, generation int64) {
	ticker := time.NewTicker(TimestampSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runtime, err := m.lookup(id)
			if err != nil {
				return
			}
			runtime.mu.RLock()
			current := runtime.instance
			runtime.mu.RUnlock()
			if current == nil || current.generation != generation {
				return
			}
			callCtx, cancel := context.WithTimeout(ctx, DefaultRPCTimeout)
			_, err = m.SendMessage(callCtx, id, "timestamp.generate", "{}")
			cancel()
			if err != nil {
				m.logger.Warn("自动时间戳请求失败", "component", "master", "plugin_id", id, "generation", generation, "error", err)
			}
		}
	}
}

// Shutdown 停止接受后台工作，并逐个清理当前 Master 创建的插件进程。
func (m *Manager) Shutdown(ctx context.Context) {
	m.rootCancel()
	for _, definition := range m.Definitions() {
		runtime, err := m.lookup(definition.ID)
		if err != nil {
			continue
		}
		runtime.mu.RLock()
		running := runtime.instance != nil
		runtime.mu.RUnlock()
		if running {
			if err := m.Stop(ctx, definition.ID); err != nil {
				m.logger.Error("退出时停止插件失败", "component", "master", "plugin_id", definition.ID, "error", err)
			}
		}
	}
}

// Snapshot 返回指定插件的定义和运行状态副本。
func (m *Manager) Snapshot(id string) (model.PluginDefinition, model.RuntimeSnapshot, error) {
	runtime, err := m.lookup(id)
	if err != nil {
		return model.PluginDefinition{}, model.RuntimeSnapshot{}, err
	}
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	return runtime.definition, runtime.snapshot, nil
}

// Snapshots 返回全部运行状态，并按插件 ID 排序。
func (m *Manager) Snapshots() []model.RuntimeSnapshot {
	m.mu.RLock()
	values := make([]model.RuntimeSnapshot, 0, len(m.runtimes))
	for _, runtime := range m.runtimes {
		runtime.mu.RLock()
		values = append(values, runtime.snapshot)
		runtime.mu.RUnlock()
	}
	m.mu.RUnlock()
	slices.SortFunc(values, func(a, b model.RuntimeSnapshot) int { return compareString(a.PluginID, b.PluginID) })
	return values
}

// Definitions 返回全部插件定义副本。
func (m *Manager) Definitions() []model.PluginDefinition {
	m.mu.RLock()
	values := make([]model.PluginDefinition, 0, len(m.runtimes))
	for _, runtime := range m.runtimes {
		runtime.mu.RLock()
		values = append(values, runtime.definition)
		runtime.mu.RUnlock()
	}
	m.mu.RUnlock()
	slices.SortFunc(values, func(a, b model.PluginDefinition) int { return compareString(a.ID, b.ID) })
	return values
}

// PluginBinaries 返回插件目录中符合 type/version/binary 结构的可执行程序。
func (m *Manager) PluginBinaries() ([]model.PluginBinary, error) {
	registered := make(map[string][]string)
	for _, definition := range m.Definitions() {
		registered[definition.BinaryPath] = append(registered[definition.BinaryPath], definition.ID)
	}
	types, err := os.ReadDir(m.allowedRoot)
	if os.IsNotExist(err) {
		return []model.PluginBinary{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取插件目录: %w", err)
	}
	var binaries []model.PluginBinary
	for _, pluginType := range types {
		if !pluginType.IsDir() || pluginType.Name() == "uploads" || !idPattern.MatchString(pluginType.Name()) {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(m.allowedRoot, pluginType.Name()))
		if err != nil {
			return nil, fmt.Errorf("读取插件类型目录: %w", err)
		}
		for _, version := range versions {
			versionValue := strings.TrimPrefix(version.Name(), "v")
			if !version.IsDir() || !versionPattern.MatchString(versionValue) {
				continue
			}
			files, err := os.ReadDir(filepath.Join(m.allowedRoot, pluginType.Name(), version.Name()))
			if err != nil {
				return nil, fmt.Errorf("读取插件版本目录: %w", err)
			}
			for _, file := range files {
				info, err := file.Info()
				if err != nil {
					return nil, fmt.Errorf("读取插件程序: %w", err)
				}
				if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
					continue
				}
				path := filepath.Join(m.allowedRoot, pluginType.Name(), version.Name(), file.Name())
				binaries = append(binaries, model.PluginBinary{Type: pluginType.Name(), Version: versionValue, BinaryPath: path, RegisteredIDs: registered[path]})
			}
		}
	}
	slices.SortFunc(binaries, func(a, b model.PluginBinary) int {
		return compareString(a.Type+"\x00"+a.Version+"\x00"+a.BinaryPath, b.Type+"\x00"+b.Version+"\x00"+b.BinaryPath)
	})
	return binaries, nil
}

// Events 返回持久化的全部生命周期事件。
func (m *Manager) Events() ([]model.Event, error) {
	return m.store.Events()
}

// Interactions 返回持久化的全部业务交互记录。
func (m *Manager) Interactions() ([]model.Interaction, error) {
	return m.store.Interactions()
}

// Restarts 返回持久化的全部自动重启记录。
func (m *Manager) Restarts() ([]model.RestartRecord, error) {
	return m.store.Restarts()
}

// Subscribe 创建实时状态变化订阅，并返回用于释放订阅的函数。
func (m *Manager) Subscribe() (<-chan struct{}, func()) {
	updates := make(chan struct{}, 1)
	m.subscriptionMu.Lock()
	m.subscriptions[updates] = struct{}{}
	m.subscriptionMu.Unlock()
	return updates, func() {
		m.subscriptionMu.Lock()
		delete(m.subscriptions, updates)
		m.subscriptionMu.Unlock()
	}
}

// Done 在 Master 开始关闭时结束，供长连接及时退出。
func (m *Manager) Done() <-chan struct{} {
	return m.rootCtx.Done()
}

// notify 非阻塞通知所有实时订阅者重新读取最新快照。
func (m *Manager) notify() {
	m.subscriptionMu.Lock()
	defer m.subscriptionMu.Unlock()
	for updates := range m.subscriptions {
		select {
		case updates <- struct{}{}:
		default:
		}
	}
}

// compareString 返回字符串排序所需的三态比较值。
func compareString(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// lookup 查找运行时对象。
func (m *Manager) lookup(id string) (*runtime, error) {
	m.mu.RLock()
	runtime := m.runtimes[id]
	m.mu.RUnlock()
	if runtime == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return runtime, nil
}

// validateBinary 确认可执行文件存在且位于预先允许的插件目录。
func (m *Manager) validateBinary(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(m.allowedRoot, abs)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("binary_path 必须位于 %s", m.allowedRoot)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("检查插件文件: %w", err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("插件文件不可执行")
	}
	return abs, nil
}

// event 同步记录审计事件；失败会写 ERROR 日志。
func (m *Manager) event(pluginID, eventType string, details map[string]any) {
	event := model.Event{EventID: newID("evt"), PluginID: pluginID, Type: eventType, OccurredAt: time.Now(), Details: details}
	if err := m.store.AppendEvent(event); err != nil {
		m.logger.Error("保存事件失败", "component", "master", "plugin_id", pluginID, "event", eventType, "error", err)
		return
	}
	m.notify()
}

// persistDefinitions 保存当前全部插件定义。
func (m *Manager) persistDefinitions() error {
	return m.store.SavePlugins(m.Definitions())
}

// persistRuntime 保存当前全部运行快照。
func (m *Manager) persistRuntime() error {
	if err := m.store.SaveRuntime(m.Snapshots()); err != nil {
		return err
	}
	m.notify()
	return nil
}

// persistRuntimeLogged 保存运行快照并记录不能向调用方传播的后台错误。
func (m *Manager) persistRuntimeLogged() {
	if err := m.persistRuntime(); err != nil {
		m.logger.Error("保存运行快照失败", "component", "master", "error", err)
	}
}

// newID 生成无需外部依赖的随机审计标识。
func newID(prefix string) string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(value[:])
}
