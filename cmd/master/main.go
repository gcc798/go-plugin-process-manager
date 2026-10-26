// master 是插件进程管理器 HTTP 服务入口。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gcc-798/go-plugin-process-manager/internal/master/httpapi"
	masterlogger "github.com/gcc-798/go-plugin-process-manager/internal/master/logger"
	"github.com/gcc-798/go-plugin-process-manager/internal/master/manager"
	"github.com/gcc-798/go-plugin-process-manager/internal/master/storage"
)

const (
	// DefaultListenAddress 是仅绑定本机的默认管理地址。
	DefaultListenAddress = "127.0.0.1:16060"
	// ShutdownTimeout 是 HTTP 和插件进程优雅退出的总超时。
	ShutdownTimeout = 10 * time.Second
)

// main 初始化持久化、日志、进程管理器和 HTTP 服务，并处理退出信号。
func main() {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "获取 Master 路径失败:", err)
		os.Exit(1)
	}
	base := filepath.Dir(executable)
	listen := flag.String("listen", DefaultListenAddress, "HTTP 监听地址")
	dataDir := flag.String("data-dir", filepath.Join(base, "data"), "JSON 数据目录")
	logDir := flag.String("log-dir", filepath.Join(base, "logs"), "日志目录")
	pluginDir := flag.String("plugin-dir", filepath.Join(base, "plugins"), "允许执行插件的根目录")
	logLevel := flag.String("log-level", "INFO", "日志级别")
	flag.Parse()
	store, err := storage.New(*dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "初始化存储失败:", err)
		os.Exit(1)
	}
	log, buffer, closeLog, err := masterlogger.New(*logDir, *logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "初始化日志失败:", err)
		os.Exit(1)
	}
	defer closeLog()
	pluginManager, err := manager.New(store, log, *pluginDir)
	if err != nil {
		log.Error("初始化插件管理器失败", "component", "master", "error", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: *listen, Handler: httpapi.NewRouter(pluginManager, buffer), ReadHeaderTimeout: 5 * time.Second}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	log.Info("Master HTTP 服务已启动", "component", "master", "address", *listen, "data_dir", *dataDir, "plugin_dir", *pluginDir)
	pluginManager.StartAutoStart()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case received := <-signals:
		log.Info("收到退出信号", "component", "master", "signal", received.String())
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP 服务异常退出", "component", "master", "error", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	_ = server.Shutdown(ctx)
	pluginManager.Shutdown(ctx)
	log.Info("Master 已安全退出", "component", "master")
}
