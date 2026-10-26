package manager

import (
	"os"
	"path/filepath"
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
