// Package httpapi 暴露插件管理、监控和审计 HTTP API。
package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/gcc798/go-plugin-process-manager/internal/master/logger"
	"github.com/gcc798/go-plugin-process-manager/internal/master/manager"
	"github.com/gcc798/go-plugin-process-manager/internal/master/model"
	"github.com/gin-gonic/gin"
	"github.com/hashicorp/go-hclog"
)

const (
	// DefaultLimit 是列表接口默认返回条数。
	DefaultLimit = 100
	// MaxLimit 是列表接口允许的最大返回条数。
	MaxLimit = 1000
	// MaxUploadRequestBytes 为二进制内容预留 multipart 元数据空间后的请求上限。
	MaxUploadRequestBytes int64 = manager.MaxPluginBinaryBytes + 1<<20
	// StreamHistoryLimit 是单次实时快照携带的每类历史记录上限。
	StreamHistoryLimit = 100
	// StreamHeartbeatInterval 是 SSE 保活事件间隔。
	StreamHeartbeatInterval = 15 * time.Second
)

// dashboardAssets 保存随 Master 二进制发布的看板静态资源。
//
//go:embed dashboard/*
var dashboardAssets embed.FS

// dashboardContent 是去掉嵌入目录前缀后的只读文件系统。
var dashboardContent = mustDashboardContent()

// API 持有 HTTP 请求所需的业务依赖。
type API struct {
	// manager 执行插件管理操作。
	manager *manager.Manager
	// logs 提供最近日志查询。
	logs *logger.Buffer
}

// registerRequest 是插件注册请求。
type registerRequest struct {
	// ID 是插件实例唯一标识。
	ID string `json:"id" binding:"required"`
	// Type 是插件实现类型。
	Type string `json:"type" binding:"required"`
	// Version 是插件版本。
	Version string `json:"version" binding:"required"`
	// BinaryPath 是允许目录内的可执行文件路径。
	BinaryPath string `json:"binary_path" binding:"required"`
	// AutoStart 表示 Master 重启后是否自动启动。
	AutoStart bool `json:"auto_start"`
	// Restart 保存自动重启参数。
	Restart model.RestartPolicy `json:"restart"`
}

// updateRequest 是动态更新请求。
type updateRequest struct {
	// Version 是目标插件版本。
	Version string `json:"version" binding:"required"`
	// BinaryPath 是目标版本可执行文件路径。
	BinaryPath string `json:"binary_path" binding:"required"`
}

// messageRequest 是 Master 主动发送的业务消息请求。
type messageRequest struct {
	// MessageType 是由插件解释的消息类型。
	MessageType string `json:"message_type" binding:"required"`
	// Payload 是透传给插件的任意 JSON 值。
	Payload json.RawMessage `json:"payload"`
	// TimeoutMS 是本次业务 RPC 的可选超时毫秒数。
	TimeoutMS int `json:"timeout_ms"`
}

// NewRouter 创建包含全部 V1 路由和请求日志的 Gin Handler。
func NewRouter(pluginManager *manager.Manager, logs *logger.Buffer, log hclog.Logger) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	api := &API{manager: pluginManager, logs: logs}
	router := gin.New()
	router.Use(requestLogger(log), gin.Recovery())
	router.GET("/", func(c *gin.Context) { c.Redirect(http.StatusTemporaryRedirect, "/dashboard/") })
	router.StaticFS("/dashboard", dashboardContent)
	router.POST("/api/plugins", api.register)
	router.GET("/api/plugins", api.listPlugins)
	router.GET("/api/plugin-binaries", api.pluginBinaries)
	router.GET("/api/plugins/:id", api.getPlugin)
	router.DELETE("/api/plugins/:id", api.deletePlugin)
	router.POST("/api/plugins/:id/start", api.start)
	router.POST("/api/plugins/:id/stop", api.stop)
	router.POST("/api/plugins/:id/restart", api.restart)
	router.POST("/api/plugins/:id/update", api.update)
	router.POST("/api/plugins/:id/update/upload", api.uploadUpdate)
	router.POST("/api/plugins/:id/messages", api.sendMessage)
	router.GET("/api/plugins/:id/health", api.health)
	router.GET("/api/plugins/:id/interactions", api.pluginInteractions)
	router.GET("/api/plugins/:id/events", api.pluginEvents)
	router.GET("/api/plugins/:id/restarts", api.pluginRestarts)
	router.GET("/api/monitor/overview", api.overview)
	router.GET("/api/monitor/plugins", api.monitorPlugins)
	router.GET("/api/monitor/stream", api.monitorStream)
	router.GET("/api/events", api.events)
	router.GET("/api/interactions", api.interactions)
	router.GET("/api/logs", api.queryLogs)
	return router
}

// mustDashboardContent 创建供 Gin 使用的看板文件系统，嵌入资源异常时立即终止启动。
func mustDashboardContent() http.FileSystem {
	content, err := fs.Sub(dashboardAssets, "dashboard")
	if err != nil {
		panic("加载内嵌看板资源失败: " + err.Error())
	}
	return http.FS(content)
}

// requestLogger 在每次 Gin 请求完成后输出 URL、状态码和耗时。
func requestLogger(log hclog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		log.Info("HTTP 请求", "component", "master", "method", c.Request.Method, "url", c.Request.URL.RequestURI(), "status", c.Writer.Status(), "duration_ms", time.Since(started).Milliseconds(), "client_ip", c.ClientIP())
	}
}

// register 校验请求并注册一个插件实例。
func (a *API) register(c *gin.Context) {
	var request registerRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	definition := model.PluginDefinition{ID: request.ID, Type: request.Type, Version: request.Version, BinaryPath: request.BinaryPath, AutoStart: request.AutoStart, Restart: request.Restart}
	if err := a.manager.Register(definition); err != nil {
		writeManagerError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"plugin": definition})
}

// listPlugins 返回插件定义及其最近运行快照。
func (a *API) listPlugins(c *gin.Context) {
	definitions := a.manager.Definitions()
	snapshots := a.manager.Snapshots()
	c.JSON(http.StatusOK, gin.H{"plugins": definitions, "runtime": snapshots})
}

// pluginBinaries 返回允许目录内可注册的插件程序。
func (a *API) pluginBinaries(c *gin.Context) {
	binaries, err := a.manager.PluginBinaries()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"binaries": binaries})
}

// getPlugin 返回一个插件的定义与运行快照。
func (a *API) getPlugin(c *gin.Context) {
	definition, snapshot, err := a.manager.Snapshot(c.Param("id"))
	if err != nil {
		writeManagerError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"plugin": definition, "runtime": snapshot})
}

// deletePlugin 删除一个已停止插件。
func (a *API) deletePlugin(c *gin.Context) {
	if err := a.manager.Delete(c.Param("id")); err != nil {
		writeManagerError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// start 启动一个插件。
func (a *API) start(c *gin.Context) {
	if err := a.manager.Start(c.Request.Context(), c.Param("id")); err != nil {
		writeManagerError(c, err)
		return
	}
	a.writeCurrent(c)
}

// stop 停止一个插件并关闭自动恢复意图。
func (a *API) stop(c *gin.Context) {
	if err := a.manager.Stop(c.Request.Context(), c.Param("id")); err != nil {
		writeManagerError(c, err)
		return
	}
	a.writeCurrent(c)
}

// restart 手动重启一个插件。
func (a *API) restart(c *gin.Context) {
	if err := a.manager.Restart(c.Request.Context(), c.Param("id")); err != nil {
		writeManagerError(c, err)
		return
	}
	a.writeCurrent(c)
}

// update 验证并切换新版本插件。
func (a *API) update(c *gin.Context) {
	var request updateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if err := a.manager.Update(c.Request.Context(), c.Param("id"), request.Version, request.BinaryPath); err != nil {
		writeManagerError(c, err)
		return
	}
	a.writeCurrent(c)
}

// uploadUpdate 接收受限大小的插件二进制，并在验证通过后动态切换版本。
func (a *API) uploadUpdate(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxUploadRequestBytes)
	if err := c.Request.ParseMultipartForm(8 << 20); err != nil {
		if _, oversized := errors.AsType[*http.MaxBytesError](err); oversized {
			fail(c, http.StatusRequestEntityTooLarge, err)
			return
		}
		fail(c, http.StatusBadRequest, err)
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	version := c.Request.FormValue("version")
	files := c.Request.MultipartForm.File["binary"]
	if version == "" || len(files) != 1 {
		fail(c, http.StatusBadRequest, errors.New("version 和唯一 binary 文件不能为空"))
		return
	}
	if files[0].Size > manager.MaxPluginBinaryBytes {
		fail(c, http.StatusRequestEntityTooLarge, errors.New("插件二进制超过上传限制"))
		return
	}
	file, err := files[0].Open()
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	defer file.Close()
	binaryPath, err := a.manager.UploadAndUpdate(c.Request.Context(), c.Param("id"), version, file)
	if err != nil {
		writeManagerError(c, err)
		return
	}
	_, snapshot, err := a.manager.Snapshot(c.Param("id"))
	if err != nil {
		writeManagerError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"binary_path": binaryPath, "runtime": snapshot})
}

// sendMessage 向当前插件实例发送一条通用业务消息。
func (a *API) sendMessage(c *gin.Context) {
	var request messageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	payload := request.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	timeout := manager.DefaultRPCTimeout
	if request.TimeoutMS > 0 {
		timeout = time.Duration(request.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()
	response, err := a.manager.SendMessage(ctx, c.Param("id"), request.MessageType, string(payload))
	if err != nil {
		fail(c, http.StatusBadGateway, err)
		return
	}
	c.JSON(http.StatusOK, response)
}

// health 返回进程、RPC 和业务健康三个独立维度。
func (a *API) health(c *gin.Context) {
	_, snapshot, err := a.manager.Snapshot(c.Param("id"))
	if err != nil {
		writeManagerError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"process_alive": snapshot.ProcessAlive, "rpc_connected": snapshot.RPCConnected, "healthy": snapshot.Healthy, "last_health_check_at": snapshot.LastHealthCheckAt})
}

// overview 返回 Master 的插件数量与状态汇总。
func (a *API) overview(c *gin.Context) {
	snapshots := a.manager.Snapshots()
	running := 0
	healthy := 0
	for _, snapshot := range snapshots {
		if snapshot.ProcessAlive {
			running++
		}
		if snapshot.Healthy {
			healthy++
		}
	}
	c.JSON(http.StatusOK, gin.H{"status": "RUNNING", "plugin_count": len(snapshots), "process_alive_count": running, "healthy_count": healthy, "observed_at": time.Now()})
}

// monitorPlugins 返回全部插件的实时运行快照。
func (a *API) monitorPlugins(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"plugins": a.manager.Snapshots()})
}

// monitorStream 通过 SSE 在状态、交互或事件变化时推送完整控制台快照。
func (a *API) monitorStream(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	updates, unsubscribe := a.manager.Subscribe()
	defer unsubscribe()
	if err := a.writeStreamSnapshot(c); err != nil {
		c.SSEvent("stream_error", gin.H{"error": err.Error()})
		c.Writer.Flush()
	}
	heartbeat := time.NewTicker(StreamHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-a.manager.Done():
			return
		case <-updates:
			if err := a.writeStreamSnapshot(c); err != nil {
				c.SSEvent("stream_error", gin.H{"error": err.Error()})
				c.Writer.Flush()
			}
		case now := <-heartbeat.C:
			c.SSEvent("heartbeat", gin.H{"sent_at": now})
			c.Writer.Flush()
		}
	}
}

// writeStreamSnapshot 读取实时状态和有限历史，并写入一个 SSE snapshot 事件。
func (a *API) writeStreamSnapshot(c *gin.Context) error {
	binaries, err := a.manager.PluginBinaries()
	if err != nil {
		return err
	}
	interactions, err := a.manager.Interactions()
	if err != nil {
		return err
	}
	events, err := a.manager.Events()
	if err != nil {
		return err
	}
	restarts, err := a.manager.Restarts()
	if err != nil {
		return err
	}
	plugins := a.manager.Snapshots()
	alive := 0
	healthy := 0
	for _, plugin := range plugins {
		if plugin.ProcessAlive {
			alive++
		}
		if plugin.Healthy {
			healthy++
		}
	}
	c.SSEvent("snapshot", gin.H{
		"observed_at": time.Now(), "plugin_count": len(plugins), "process_alive_count": alive, "healthy_count": healthy,
		"plugins": plugins, "available_binaries": binaries, "interactions": tailValues(interactions, StreamHistoryLimit), "events": tailValues(events, StreamHistoryLimit), "restarts": tailValues(restarts, StreamHistoryLimit),
	})
	c.Writer.Flush()
	return nil
}

// tailValues 返回切片末尾至多 limit 条记录。
func tailValues[T any](values []T, limit int) []T {
	if len(values) <= limit {
		return values
	}
	return values[len(values)-limit:]
}

// events 返回经过 plugin_id 和 event_type 过滤的事件。
func (a *API) events(c *gin.Context) {
	values, err := a.manager.Events()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	pluginID := c.Query("plugin_id")
	eventType := c.Query("event_type")
	values = filterLimit(values, c, func(value model.Event) bool {
		return (pluginID == "" || value.PluginID == pluginID) && (eventType == "" || value.Type == eventType)
	})
	c.JSON(http.StatusOK, gin.H{"events": values})
}

// pluginEvents 返回指定插件的生命周期事件。
func (a *API) pluginEvents(c *gin.Context) {
	c.Request.URL.RawQuery = addPluginFilter(c.Request.URL.Query().Encode(), c.Param("id"))
	a.events(c)
}

// interactions 返回可按插件、方向、发起方和语义类型过滤的通信历史。
func (a *API) interactions(c *gin.Context) {
	values, err := a.manager.Interactions()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	pluginID := c.Query("plugin_id")
	direction := c.Query("direction")
	initiator := c.Query("initiator")
	kind := c.Query("kind")
	values = filterLimit(values, c, func(value model.Interaction) bool {
		return (pluginID == "" || value.PluginID == pluginID) && (direction == "" || value.Direction == direction) && (initiator == "" || value.Initiator == initiator) && (kind == "" || value.Kind == kind)
	})
	c.JSON(http.StatusOK, gin.H{"interactions": values})
}

// pluginInteractions 返回指定插件的通信历史。
func (a *API) pluginInteractions(c *gin.Context) {
	c.Request.URL.RawQuery = addPluginFilter(c.Request.URL.Query().Encode(), c.Param("id"))
	a.interactions(c)
}

// pluginRestarts 返回指定插件的自动重启记录。
func (a *API) pluginRestarts(c *gin.Context) {
	values, err := a.manager.Restarts()
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	id := c.Param("id")
	values = filterLimit(values, c, func(value model.RestartRecord) bool { return value.PluginID == id })
	c.JSON(http.StatusOK, gin.H{"restarts": values})
}

// queryLogs 返回 Master 与转发插件日志的统一有界视图。
func (a *API) queryLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"logs": a.logs.Query(c.Query("plugin_id"), c.Query("level"), queryLimit(c))})
}

// writeCurrent 返回当前插件运行快照。
func (a *API) writeCurrent(c *gin.Context) {
	_, snapshot, err := a.manager.Snapshot(c.Param("id"))
	if err != nil {
		writeManagerError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"runtime": snapshot})
}

// fail 输出统一 JSON 错误响应。
func fail(c *gin.Context, status int, err error) {
	c.AbortWithStatusJSON(status, gin.H{"error": err.Error()})
}

// writeManagerError 把领域错误映射为 HTTP 状态码。
func writeManagerError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, manager.ErrNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, manager.ErrConflict) {
		status = http.StatusConflict
	} else {
		status = http.StatusBadRequest
	}
	fail(c, status, err)
}

// queryLimit 解析并限制列表返回条数。
func queryLimit(c *gin.Context) int {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(DefaultLimit)))
	if err != nil || limit <= 0 {
		return DefaultLimit
	}
	return min(limit, MaxLimit)
}

// filterLimit 从最新记录向前过滤，并保持返回结果的时间正序。
func filterLimit[T any](values []T, c *gin.Context, keep func(T) bool) []T {
	limit := queryLimit(c)
	result := make([]T, 0, min(limit, len(values)))
	for index := len(values) - 1; index >= 0 && len(result) < limit; index-- {
		if keep(values[index]) {
			result = append(result, values[index])
		}
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

// addPluginFilter 在保留既有查询参数的同时绑定插件 ID。
func addPluginFilter(rawQuery, pluginID string) string {
	if rawQuery == "" {
		return "plugin_id=" + pluginID
	}
	return rawQuery + "&plugin_id=" + pluginID
}
