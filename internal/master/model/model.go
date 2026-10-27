// Package model 定义 Master 持久化和 API 使用的核心数据结构。
package model

import "time"

// PluginState 表示插件实例的生命周期状态。
type PluginState string

const (
	// StateRegistered 表示插件仅完成注册。
	StateRegistered PluginState = "REGISTERED"
	// StateStarting 表示插件正在启动。
	StateStarting PluginState = "STARTING"
	// StateRunning 表示插件正常运行。
	StateRunning PluginState = "RUNNING"
	// StateUnhealthy 表示进程仍在但业务健康检查失败。
	StateUnhealthy PluginState = "UNHEALTHY"
	// StateStopping 表示插件正在停止。
	StateStopping PluginState = "STOPPING"
	// StateStopped 表示插件已经停止。
	StateStopped PluginState = "STOPPED"
	// StateRestarting 表示插件正在重启。
	StateRestarting PluginState = "RESTARTING"
	// StateUpdating 表示插件正在切换版本。
	StateUpdating PluginState = "UPDATING"
	// StateFailed 表示插件达到不可自动恢复状态。
	StateFailed PluginState = "FAILED"
)

const (
	// DesiredRunning 表示用户期望插件持续运行。
	DesiredRunning PluginState = "RUNNING"
	// DesiredStopped 表示用户期望插件保持停止。
	DesiredStopped PluginState = "STOPPED"
)

// RestartPolicy 描述单个插件的自动重启参数。
type RestartPolicy struct {
	// Enabled 表示是否允许异常退出后自动重启。
	Enabled bool `json:"enabled"`
	// MaxRetries 表示连续失败的最大重试次数。
	MaxRetries int `json:"max_retries"`
	// InitialDelaySeconds 表示第一次重启前的等待秒数。
	InitialDelaySeconds int `json:"initial_delay_seconds"`
	// MaxDelaySeconds 表示指数退避的最大等待秒数。
	MaxDelaySeconds int `json:"max_delay_seconds"`
	// StableResetSeconds 表示稳定运行多久后重置连续失败数。
	StableResetSeconds int `json:"stable_reset_seconds"`
}

// PluginDefinition 是可持久化的插件注册信息。
type PluginDefinition struct {
	// ID 是插件实例的唯一标识。
	ID string `json:"id"`
	// Type 是插件实现类型。
	Type string `json:"type"`
	// Version 是期望运行的版本。
	Version string `json:"version"`
	// BinaryPath 是位于允许目录内的插件可执行文件路径。
	BinaryPath string `json:"binary_path"`
	// AutoStart 表示 Master 启动后是否自动拉起该插件。
	AutoStart bool `json:"auto_start"`
	// Restart 保存该实例的自动重启策略。
	Restart RestartPolicy `json:"restart"`
}

// PluginBinary 是允许目录内可注册的插件程序。
type PluginBinary struct {
	// Type 是目录声明的插件类型。
	Type string `json:"type"`
	// Version 是目录声明的插件版本。
	Version string `json:"version"`
	// BinaryPath 是插件程序绝对路径。
	BinaryPath string `json:"binary_path"`
	// RegisteredIDs 是正在使用该程序的实例标识。
	RegisteredIDs []string `json:"registered_ids"`
}

// RuntimeSnapshot 是插件最近一次运行状态快照，不用于进程重附着。
type RuntimeSnapshot struct {
	// PluginID 是插件实例标识。
	PluginID string `json:"plugin_id"`
	// DesiredState 是用户期望状态。
	DesiredState PluginState `json:"desired_state"`
	// ObservedState 是 Master 最近观测状态。
	ObservedState PluginState `json:"observed_state"`
	// PID 是最近一次插件进程号。
	PID int `json:"pid"`
	// Version 是当前生效版本。
	Version string `json:"version"`
	// Generation 是每次成功创建实例时递增的代次。
	Generation int64 `json:"generation"`
	// ProcessAlive 表示最近观测到进程存活。
	ProcessAlive bool `json:"process_alive"`
	// RPCConnected 表示最近观测到 go-plugin RPC 通道可用。
	RPCConnected bool `json:"rpc_connected"`
	// Healthy 表示最近业务健康检查结果。
	Healthy bool `json:"healthy"`
	// LastObservedAt 是最近更新观测状态的时间。
	LastObservedAt time.Time `json:"last_observed_at"`
	// LastHealthCheckAt 是最近业务健康检查时间。
	LastHealthCheckAt time.Time `json:"last_health_check_at"`
	// LastStartedAt 是最近启动成功时间。
	LastStartedAt time.Time `json:"last_started_at"`
	// LastStoppedAt 是最近停止完成时间。
	LastStoppedAt time.Time `json:"last_stopped_at"`
	// LastInteractionAt 是最近业务通信时间。
	LastInteractionAt time.Time `json:"last_interaction_at"`
	// LastInteractionDirection 是最近业务通信方向。
	LastInteractionDirection string `json:"last_interaction_direction"`
	// LastInteractionType 是最近业务消息类型。
	LastInteractionType string `json:"last_interaction_type"`
	// LastSentAt 是最近发送业务消息的时间。
	LastSentAt time.Time `json:"last_sent_at"`
	// LastReceivedAt 是最近收到业务消息的时间。
	LastReceivedAt time.Time `json:"last_received_at"`
	// RestartCount 是累计自动重启次数。
	RestartCount int `json:"restart_count"`
	// LastError 是最近错误说明。
	LastError string `json:"last_error,omitempty"`
}

// Event 是一条生命周期或状态变化记录。
type Event struct {
	// EventID 是事件唯一标识。
	EventID string `json:"event_id"`
	// PluginID 是相关插件标识。
	PluginID string `json:"plugin_id,omitempty"`
	// Type 是事件类型。
	Type string `json:"event_type"`
	// OccurredAt 是事件发生时间。
	OccurredAt time.Time `json:"occurred_at"`
	// Details 保存事件的结构化补充信息。
	Details map[string]any `json:"details,omitempty"`
}

// Interaction 表示一次业务 RPC 的完整语义记录。
type Interaction struct {
	// MessageID 是请求消息标识。
	MessageID string `json:"message_id"`
	// CorrelationID 是跨消息关联标识。
	CorrelationID string `json:"correlation_id,omitempty"`
	// PluginID 是目标或来源插件标识。
	PluginID string `json:"plugin_id"`
	// Generation 是产生记录的插件代次。
	Generation int64 `json:"generation"`
	// Direction 表示业务请求的传输方向。
	Direction string `json:"direction"`
	// Initiator 表示本次 RPC 的发起方。
	Initiator string `json:"initiator"`
	// Kind 区分请求响应调用和插件主动上报调用。
	Kind string `json:"kind"`
	// MessageType 是插件自定义业务消息类型。
	MessageType string `json:"message_type"`
	// RequestPayload 保存请求载荷。
	RequestPayload string `json:"request_payload,omitempty"`
	// ResponsePayload 保存同一 RPC 的响应载荷，不单独记作上报。
	ResponsePayload string `json:"response_payload,omitempty"`
	// SentAt 是调用开始时间。
	SentAt time.Time `json:"sent_at"`
	// CompletedAt 是调用结束时间。
	CompletedAt time.Time `json:"completed_at"`
	// DurationMS 是调用耗时毫秒数。
	DurationMS int64 `json:"duration_ms"`
	// Status 是调用结果状态。
	Status string `json:"status"`
	// Error 是失败说明。
	Error string `json:"error,omitempty"`
}

// RestartRecord 表示一次自动重启尝试。
type RestartRecord struct {
	// PluginID 是插件实例标识。
	PluginID string `json:"plugin_id"`
	// OldPID 是崩溃进程号。
	OldPID int `json:"old_pid"`
	// NewPID 是重启成功后的进程号。
	NewPID int `json:"new_pid"`
	// Reason 是重启原因。
	Reason string `json:"reason"`
	// Attempt 是本轮连续重试次数。
	Attempt int `json:"attempt"`
	// StartedAt 是尝试开始时间。
	StartedAt time.Time `json:"started_at"`
	// CompletedAt 是尝试完成时间。
	CompletedAt time.Time `json:"completed_at"`
	// Success 表示尝试是否成功。
	Success bool `json:"success"`
	// Error 是失败说明。
	Error string `json:"error,omitempty"`
}
