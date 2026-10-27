package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gcc798/go-plugin-process-manager/internal/master/model"
	"github.com/gcc798/go-plugin-process-manager/internal/master/storage"
	"github.com/hashicorp/go-hclog"
)

// TestRestartDelay 验证指数退避增长并受最大值限制。
func TestRestartDelay(t *testing.T) {
	policy := model.RestartPolicy{InitialDelaySeconds: 1, MaxDelaySeconds: 5}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	for index, expected := range want {
		if actual := RestartDelay(policy, index+1); actual != expected {
			t.Fatalf("第 %d 次退避 = %v，期望 %v", index+1, actual, expected)
		}
	}
}

// TestSaveUploadedBinary 验证上传内容被写入允许目录并设置执行权限。
func TestSaveUploadedBinary(t *testing.T) {
	root := t.TempDir()
	pluginManager := &Manager{allowedRoot: root}
	binaryPath, err := pluginManager.saveUploadedBinary("upload-test", "2.0.0", strings.NewReader("binary"), 16)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "binary" {
		t.Fatalf("上传内容错误: %q", content)
	}
	info, err := os.Stat(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatal("上传文件缺少执行权限")
	}
}

// TestSaveUploadedBinaryRejectsOversize 验证超过限制的上传不会留下正式文件。
func TestSaveUploadedBinaryRejectsOversize(t *testing.T) {
	root := t.TempDir()
	pluginManager := &Manager{allowedRoot: root}
	if _, err := pluginManager.saveUploadedBinary("upload-test", "2.0.0", strings.NewReader("oversize"), 4); err == nil {
		t.Fatal("超限上传应失败")
	}
}

// TestPluginBinaries 验证插件目录中的不同版本程序都能被控制台发现。
func TestPluginBinaries(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		filepath.Join(root, "heartbeat", "v1.0.0", "heartbeat-plugin"),
		filepath.Join(root, "timestamp", "v1.0.0", "timestamp-plugin"),
		filepath.Join(root, "timestamp", "v2.0.0", "timestamp-plugin"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("binary"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "timestamp", "v2.0.0", "README"), []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	pluginManager := &Manager{allowedRoot: root}
	binaries, err := pluginManager.PluginBinaries()
	if err != nil {
		t.Fatal(err)
	}
	if len(binaries) != 3 || binaries[0].Type != "heartbeat" || binaries[2].Version != "2.0.0" {
		t.Fatalf("发现的插件程序错误: %#v", binaries)
	}
}

// TestSubscribe 验证状态变化通知能够到达并可安全取消。
func TestSubscribe(t *testing.T) {
	pluginManager := &Manager{subscriptions: make(map[chan struct{}]struct{})}
	updates, unsubscribe := pluginManager.Subscribe()
	pluginManager.notify()
	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("未收到实时状态通知")
	}
	unsubscribe()
	pluginManager.notify()
}

// TestRestartDelayDefaults 验证非法零值策略仍不会形成高频重启。
func TestRestartDelayDefaults(t *testing.T) {
	if actual := RestartDelay(model.RestartPolicy{}, 1); actual != time.Second {
		t.Fatalf("零值策略退避 = %v", actual)
	}
}

// TestRegisterStateTransition 验证注册操作写入 REGISTERED 状态且不会把历史 PID 当成存活进程。
func TestRegisterStateTransition(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "plugin")
	if err := os.WriteFile(binary, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := storage.New(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	pluginManager, err := New(store, hclog.NewNullLogger(), root)
	if err != nil {
		t.Fatal(err)
	}
	definition := model.PluginDefinition{ID: "state-test", Type: "timestamp", Version: "1.0.0", BinaryPath: binary}
	if err := pluginManager.Register(definition); err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := pluginManager.Snapshot(definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ObservedState != model.StateRegistered || snapshot.ProcessAlive || snapshot.PID != 0 {
		t.Fatalf("注册状态错误: %#v", snapshot)
	}
}
