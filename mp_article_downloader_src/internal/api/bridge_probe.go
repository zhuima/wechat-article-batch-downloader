package api

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	result "mp_article_batch_downloader/internal/util"
)

// This temporary diagnostic accepts only fixed labels and numeric outcomes.
// The native response, article metadata, URLs and WeChat session never cross
// the bridge into the desktop service.
type bridgeProbeReport struct {
	Biz          string `json:"biz"`
	Status       string `json:"status"`
	BaseRet      *int   `json:"base_ret"`
	JSAPIRet     *int   `json:"jsapi_ret"`
	ServiceRet   *int   `json:"service_ret"`
	ArticleCount int    `json:"article_count"`
	HasOffset    bool   `json:"has_offset"`
	IsEnd        bool   `json:"is_end"`
	RecordedAt   int64  `json:"recorded_at,omitempty"`
}

var bridgeProbeState struct {
	sync.Mutex
	Last bridgeProbeReport
	Seen bool
}

var bridgeProbeBiz = regexp.MustCompile(`^[A-Za-z0-9+/=]{6,64}$`)

var bridgeProbeStatuses = map[string]bool{
	"missing_username":   true,
	"bridge_unavailable": true,
	"timeout":            true,
	"permission_denied":  true,
	"not_implemented":    true,
	"bridge_error":       true,
	"invalid_json":       true,
	"service_error":      true,
	"unexpected_shape":   true,
	"invoke_exception":   true,
	"ok":                 true,
}

func bridgeProbeTarget() string {
	if os.Getenv("MP_ARCHIVE_BRIDGE_PROBE") != "1" {
		return ""
	}
	return os.Getenv("MP_ARCHIVE_BRIDGE_PROBE_BIZ")
}

func (c *APIClient) handleBridgeProbe(ctx *gin.Context) {
	target := bridgeProbeTarget()
	if target == "" || !bridgeProbeBiz.MatchString(target) {
		ctx.Status(http.StatusNotFound)
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 1024)
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	var report bridgeProbeReport
	if decoder.Decode(&report) != nil || decoder.Decode(new(any)) != io.EOF ||
		report.Biz != target || !bridgeProbeStatuses[report.Status] ||
		report.ArticleCount < 0 || report.ArticleCount > 1000 || report.RecordedAt != 0 {
		ctx.Status(http.StatusBadRequest)
		return
	}
	bridgeProbeState.Lock()
	defer bridgeProbeState.Unlock()
	if bridgeProbeState.Seen {
		ctx.Status(http.StatusConflict)
		return
	}
	report.RecordedAt = time.Now().Unix()
	bridgeProbeState.Last = report
	bridgeProbeState.Seen = true
	result.Ok(ctx, gin.H{"stored": true})
}

func (c *APIClient) handleBridgeProbeResult(ctx *gin.Context) {
	if bridgeProbeTarget() == "" {
		ctx.Status(http.StatusNotFound)
		return
	}
	bridgeProbeState.Lock()
	defer bridgeProbeState.Unlock()
	result.Ok(ctx, gin.H{"seen": bridgeProbeState.Seen, "result": bridgeProbeState.Last})
}
