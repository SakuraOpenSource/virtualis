package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SakuraOpenSource/virtualis/internal/runtime"
	"github.com/gin-gonic/gin"
)

// 远端伪造 X-Forwarded-For 不能改变 ClientIP：gin 默认信任一切代理头，
// 必须显式只信任回环代理（与 Levis 主程序同一口径），否则被控注册时
// 上报的 IP 可被任意伪造。
func TestTrustedProxiesRestricted(t *testing.T) {
	rt := runtime.New(t.TempDir())
	eng, closeFn := New(rt, false)
	t.Cleanup(closeFn)

	// 借一个真实路由挂探针读取 ClientIP（bootstrap 恒可用）。
	var got string
	eng.GET("/__probe_client_ip", func(c *gin.Context) {
		got = c.ClientIP()
		c.Status(http.StatusOK)
	})

	for _, forwarded := range []string{"", "1.2.3.4"} {
		req := httptest.NewRequest(http.MethodGet, "/__probe_client_ip", nil)
		req.RemoteAddr = "192.0.2.9:1234"
		if forwarded != "" {
			req.Header.Set("X-Forwarded-For", forwarded)
		}
		rec := httptest.NewRecorder()
		eng.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("探针应返回 200，实际 %d", rec.Code)
		}
		if got != "192.0.2.9" {
			t.Fatalf("伪造 X-Forwarded-For=%q 后 ClientIP 应仍为直连地址 192.0.2.9，实际 %q", forwarded, got)
		}
	}
}
