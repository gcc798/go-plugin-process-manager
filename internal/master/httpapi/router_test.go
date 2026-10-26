package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hashicorp/go-hclog"
)

// TestRequestLoggerOutputsURL 验证每次 Gin 请求都会输出包含查询参数的 URL。
func TestRequestLoggerOutputsURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	log := hclog.New(&hclog.LoggerOptions{Output: &output, Level: hclog.Info})
	router := gin.New()
	router.Use(requestLogger(log))
	router.GET("/probe", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := httptest.NewRequest(http.MethodGet, "/probe?value=1", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	logged := output.String()
	if !strings.Contains(logged, "url=/probe?value=1") || !strings.Contains(logged, "status=204") {
		t.Fatalf("请求日志缺少 URL 或状态码: %s", logged)
	}
}
