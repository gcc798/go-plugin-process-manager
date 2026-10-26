// timestamp-plugin 是内置时间戳插件进程入口。
package main

import (
	"flag"
	"os"

	"github.com/gcc-798/go-plugin-process-manager/internal/plugins/timestamp"
	"github.com/gcc-798/go-plugin-process-manager/pkg/plugincontract"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
)

// main 根据构建或启动参数选择版本并交给 go-plugin 托管。
func main() {
	version := flag.String("version", "1.0.0", "插件版本")
	flag.Parse()
	logger := hclog.New(&hclog.LoggerOptions{Name: "timestamp-plugin", Level: hclog.Info, Output: os.Stderr, JSONFormat: true})
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: plugincontract.Handshake,
		Plugins:         map[string]plugin.Plugin{plugincontract.PluginName: &plugincontract.GRPCPlugin{Impl: timestamp.New(*version, logger)}},
		GRPCServer:      plugin.DefaultGRPCServer,
		Logger:          logger,
	})
}
