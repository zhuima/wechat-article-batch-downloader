package officialaccount

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOfficialAccountProxyRedactsLogWithoutChangingRequest(t *testing.T) {
	seen := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.String()
		fmt.Fprint(w, "ok")
	}))
	defer upstream.Close()

	target := upstream.URL + "/article?key=SECRET_KEY&token=SECRET_TOKEN"
	logFile, err := os.CreateTemp(t.TempDir(), "proxy-log-")
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	originalStdout := os.Stdout
	os.Stdout = logFile
	defer func() { os.Stdout = originalStdout }()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/mp/proxy?url="+url.QueryEscape(target), nil)
	(&OfficialAccountClient{}).HandleOfficialAccountProxy(ctx)
	os.Stdout = originalStdout

	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok" {
		t.Fatalf("proxy response = %d %q", recorder.Code, recorder.Body.String())
	}
	if got := <-seen; got != "/article?key=SECRET_KEY&token=SECRET_TOKEN" {
		t.Fatalf("upstream request changed: %q", got)
	}
	logData, err := os.ReadFile(logFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	logged := string(logData)
	if strings.Contains(logged, "SECRET_KEY") || strings.Contains(logged, "SECRET_TOKEN") || !strings.Contains(logged, "?[REDACTED]") {
		t.Fatalf("proxy request log exposed credentials: %q", logged)
	}
}
