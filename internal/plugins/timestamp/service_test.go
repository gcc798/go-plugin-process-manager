package timestamp

import (
	"context"
	"encoding/json"
	"testing"

	gen "github.com/gcc798/go-plugin-process-manager/pkg/plugincontract/gen"
)

// TestHandleVersions 验证两个时间戳版本的响应差异。
func TestHandleVersions(t *testing.T) {
	message := &gen.Message{PluginId: "timestamp-test", MessageType: "timestamp.generate", PayloadJson: "{}"}
	for _, version := range []string{"1.0.0", "2.0.0"} {
		payload, err := handle(t.Context(), version, message)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(payload), &result); err != nil {
			t.Fatal(err)
		}
		_, formatted := result["formatted_time"]
		if formatted != (version == "2.0.0") {
			t.Fatalf("版本 %s 的 formatted_time 存在性错误", version)
		}
	}
}

// TestHandleUnknownMessage 验证未知消息类型返回明确错误。
func TestHandleUnknownMessage(t *testing.T) {
	_, err := handle(context.Background(), "1.0.0", &gen.Message{MessageType: "unknown"})
	if err == nil {
		t.Fatal("未知消息应失败")
	}
}
