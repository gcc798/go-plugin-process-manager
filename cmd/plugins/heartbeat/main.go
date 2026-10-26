// heartbeat-plugin 是内置主动上报心跳插件进程入口。
package main

import (
	"flag"
	"os"

	"github.com/gcc798/go-plugin-process-manager/internal/plugins/heartbeat"
	"github.com/gcc798/go-plugin-process-manager/pkg/plugincontract"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
)

// main 创建心跳服务并交给 go-plugin 托管。
func main() {
	version := flag.String("version", "1.0.0", "插件版本")
	flag.Parse()
	logger := hclog.New(&hclog.LoggerOptions{Name: "heartbeat-plugin", Level: hclog.Info, Output: os.Stderr, JSONFormat: true})
	service := heartbeat.New(*version, logger)
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: plugincontract.Handshake,
		Plugins:         map[string]plugin.Plugin{plugincontract.PluginName: &plugincontract.GRPCPlugin{Impl: service}},
		GRPCServer:      plugin.DefaultGRPCServer,
		Logger:          logger,
	})
}
