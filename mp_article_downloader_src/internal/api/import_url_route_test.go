package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestImportURLLoopbackGuardIgnoresForwardedHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/mp/import_url", loopbackOnly(func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) }))
	for _, test := range []struct {
		remote string
		want   int
	}{
		{"127.0.0.1:12345", http.StatusNoContent},
		{"[::1]:12345", http.StatusNoContent},
		{"203.0.113.1:12345", http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/mp/import_url", nil)
		req.RemoteAddr = test.remote
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != test.want {
			t.Errorf("remote %s: status %d, want %d", test.remote, response.Code, test.want)
		}
	}
}
