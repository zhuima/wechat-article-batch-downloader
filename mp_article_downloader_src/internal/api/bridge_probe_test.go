package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBridgeProbeStoresOnlySafeOneShotDiagnostic(t *testing.T) {
	t.Setenv("MP_ARCHIVE_BRIDGE_PROBE", "1")
	t.Setenv("MP_ARCHIVE_BRIDGE_PROBE_BIZ", "MzTest")
	bridgeProbeState.Lock()
	bridgeProbeState.Seen = false
	bridgeProbeState.Last = bridgeProbeReport{}
	bridgeProbeState.Unlock()

	client := &APIClient{engine: gin.New()}
	client.engine.POST("/api/desktop/bridge-probe", loopbackOnly(client.handleBridgeProbe))
	client.engine.GET("/api/desktop/bridge-probe", loopbackOnly(client.handleBridgeProbeResult))
	post := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/desktop/bridge-probe", strings.NewReader(body))
		request.RemoteAddr = "127.0.0.1:12345"
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		client.engine.ServeHTTP(response, request)
		return response
	}
	if response := post(`{"biz":"MzTest","status":"ok","article_count":7,"cookie":"secret"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected-field report was accepted: HTTP %d", response.Code)
	}
	if response := post(`{"biz":"MzTest","status":"ok","article_count":7,"base_ret":0,"jsapi_ret":0,"has_offset":true}`); response.Code != http.StatusOK {
		t.Fatalf("safe report was rejected: HTTP %d", response.Code)
	}
	if response := post(`{"biz":"MzTest","status":"ok","article_count":9}`); response.Code != http.StatusConflict {
		t.Fatalf("second report was accepted: HTTP %d", response.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/desktop/bridge-probe", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	client.engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"article_count":7`) ||
		strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("unsafe or missing diagnostic response: HTTP %d", response.Code)
	}
}
