package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gcc798/go-plugin-process-manager/internal/master/model"
)

// TestStoreRoundTrip 验证定义、快照和交互记录能够完整持久化。
func TestStoreRoundTrip(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	definitions := []model.PluginDefinition{{ID: "test", Type: "timestamp", Version: "1.0.0", BinaryPath: "/tmp/test"}}
	if err := store.SavePlugins(definitions); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Plugins()
	if err != nil || len(loaded) != 1 || loaded[0].ID != "test" {
		t.Fatalf("定义往返失败: %#v, %v", loaded, err)
	}
	interaction := model.Interaction{MessageID: "msg-1", PluginID: "test", Kind: "REQUEST_RESPONSE", SentAt: time.Now()}
	if err := store.AppendInteraction(interaction); err != nil {
		t.Fatal(err)
	}
	interactions, err := store.Interactions()
	if err != nil || len(interactions) != 1 || interactions[0].Kind != "REQUEST_RESPONSE" {
		t.Fatalf("交互记录往返失败: %#v, %v", interactions, err)
	}
}

// TestStoreRejectsCorruption 验证损坏历史不会被初始化逻辑静默覆盖。
func TestStoreRejectsCorruption(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugins.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err == nil {
		t.Fatal("损坏 JSON 应返回错误")
	}
}
