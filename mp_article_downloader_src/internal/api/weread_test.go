package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"mp_article_batch_downloader/internal/archive"
	"mp_article_batch_downloader/internal/officialaccount"
)

type wereadTestResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func wereadTestClient(t *testing.T, root, downloads string) *APIClient {
	t.Helper()
	client := &APIClient{
		engine: gin.New(), cfg: &APIConfig{RootDir: root, DownloadDir: downloads},
		official: &officialaccount.OfficialAccountClient{},
	}
	client.setupDesktop()
	t.Cleanup(client.archive.Close)
	return client
}

func wereadTestCall(t *testing.T, client *APIClient, method, path, body, origin string) (int, wereadTestResponse) {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:12345"
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	client.engine.ServeHTTP(response, request)
	if response.Code == http.StatusForbidden {
		return response.Code, wereadTestResponse{}
	}
	var decoded wereadTestResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("invalid response: HTTP %d: %v", response.Code, err)
	}
	return response.Code, decoded
}

func wereadPageJSON(biz, bookID string, offset, reviews int, articles []wereadPageArticle) string {
	data, _ := json.Marshal(map[string]any{
		"biz": biz, "book_id": bookID, "offset": offset, "reviews": reviews, "articles": articles,
	})
	return string(data)
}

func TestWereadPagePersistsIndependentResumableScan(t *testing.T) {
	root, downloads := t.TempDir(), t.TempDir()
	const biz = "MzWeread1"
	const bookID = "MP_WXS_123456"
	legacy := archive.Scan{
		Options: archive.Options{Biz: biz, Mode: "all"}, Status: "complete", Source: "publisher",
		Articles: []archive.Article{{ID: "legacy", Title: "旧历史文章"}},
	}
	legacyData, _ := json.Marshal(legacy)
	if err := os.MkdirAll(filepath.Join(root, "scans"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scans", archive.ID(biz)+".json"), legacyData, 0600); err != nil {
		t.Fatal(err)
	}
	client := wereadTestClient(t, root, downloads)
	short := "https://mp.weixin.qq.com/s/Short_123?key=private-session&scene=1"
	query := "https://mp.weixin.qq.com/s?__biz=" + biz + "&mid=123&idx=1&sn=signature&pass_ticket=private-ticket"
	first := wereadPageJSON(biz, bookID, 0, 2, []wereadPageArticle{
		{Title: "第一篇", URL: short, Published: 1_700_000_000},
		{Title: "第一篇重复", URL: short, Published: 1_700_000_000},
		{Title: "第二篇", URL: query, Published: 1_700_000_100},
	})
	httpCode, body := wereadTestCall(t, client, http.MethodPost, "/api/desktop/weread/page", first, "")
	if httpCode != http.StatusOK || body.Code != 0 {
		t.Fatalf("first page failed: HTTP %d code %d msg %q", httpCode, body.Code, body.Msg)
	}
	var scan wereadScanResponse
	if err := json.Unmarshal(body.Data, &scan); err != nil {
		t.Fatal(err)
	}
	if scan.Options.Biz != biz || scan.Source != "weread" || scan.Status != "paused" || scan.Offset != 2 ||
		scan.Pages != 1 || len(scan.Articles) != 2 || scan.BookID != bookID || scan.ReachedEnd ||
		!strings.Contains(scan.Message, wereadScopeNote) {
		t.Fatalf("first page scan state = %+v", scan)
	}
	if strings.Contains(string(body.Data), "private-session") || strings.Contains(string(body.Data), "private-ticket") {
		t.Fatal("scan response leaked article session parameters")
	}
	second := wereadPageJSON(biz, bookID, 2, 1, []wereadPageArticle{
		{Title: "第一篇再次出现", URL: "https://mp.weixin.qq.com/s/Short_123?scene=2"},
		{Title: "第三篇", URL: "https://mp.weixin.qq.com/s/Short_456"},
	})
	_, body = wereadTestCall(t, client, http.MethodPost, "/api/desktop/weread/page", second, "")
	if body.Code != 0 || json.Unmarshal(body.Data, &scan) != nil || scan.Offset != 3 || scan.Pages != 2 || len(scan.Articles) != 3 {
		t.Fatalf("second page did not advance by reviews and deduplicate: code %d scan %+v", body.Code, scan)
	}
	last := wereadPageJSON(biz, bookID, 3, 0, []wereadPageArticle{})
	_, body = wereadTestCall(t, client, http.MethodPost, "/api/desktop/weread/page", last, "")
	if body.Code != 0 || json.Unmarshal(body.Data, &scan) != nil || scan.Offset != 3 || scan.Pages != 3 ||
		scan.Status != "partial" || !scan.ReachedEnd || scan.ErrorCode != "publisher_history_unverified" {
		t.Fatalf("terminal page incorrectly marked a full publisher history: code %d scan %+v", body.Code, scan)
	}
	_, body = wereadTestCall(t, client, http.MethodPost, "/api/desktop/weread/page", last, "")
	if body.Code != 0 || json.Unmarshal(body.Data, &scan) != nil || scan.Pages != 3 {
		t.Fatal("retry of the last accepted page advanced the cursor")
	}
	_, body = wereadTestCall(t, client, http.MethodPost, "/api/desktop/weread/page", wereadPageJSON(biz, bookID, 3, 1, nil), "")
	if body.Code != 409 {
		t.Fatalf("new page after terminal marker code = %d", body.Code)
	}
	storedData, err := os.ReadFile(filepath.Join(root, "weread-scans", archive.ID(biz)+".json"))
	if err != nil || strings.Contains(string(storedData), "private-session") || strings.Contains(string(storedData), "private-ticket") ||
		strings.Contains(string(storedData), "cookie") {
		t.Fatal("WeRead scan file is missing or retained a session credential")
	}
	unchanged, err := os.ReadFile(filepath.Join(root, "scans", archive.ID(biz)+".json"))
	if err != nil || string(unchanged) != string(legacyData) {
		t.Fatal("WeRead import changed the existing publisher scan")
	}
	restarted := wereadTestClient(t, root, downloads)
	_, body = wereadTestCall(t, restarted, http.MethodGet, "/api/desktop/weread/scan?biz="+biz, "", "")
	if body.Code != 0 || json.Unmarshal(body.Data, &scan) != nil || scan.Offset != 3 || !scan.ReachedEnd || len(scan.Articles) != 3 {
		t.Fatalf("WeRead progress did not survive restart: code %d scan %+v", body.Code, scan)
	}
	_, body = wereadTestCall(t, restarted, http.MethodGet, "/api/desktop/weread/scan-summaries", "", "")
	var summaries map[string]wereadScanSummary
	if body.Code != 0 || json.Unmarshal(body.Data, &summaries) != nil || summaries[biz].ArticleCount != 3 ||
		summaries[biz].Source != "weread" || !summaries[biz].ReachedEnd || strings.Contains(string(body.Data), "https://") {
		t.Fatalf("WeRead summaries invalid or disclosed links: code %d summary %+v", body.Code, summaries[biz])
	}
	_, body = wereadTestCall(t, restarted, http.MethodGet, "/api/desktop/local-status?biz="+biz, "", "")
	var oldStatus localArticleStatus
	if body.Code != 0 || json.Unmarshal(body.Data, &oldStatus) != nil || oldStatus.Total != 1 {
		t.Fatalf("default local status no longer uses the publisher scan: %+v", oldStatus)
	}
	_, body = wereadTestCall(t, restarted, http.MethodGet, "/api/desktop/local-status?biz="+biz+"&source=weread", "", "")
	var newStatus localArticleStatus
	if body.Code != 0 || json.Unmarshal(body.Data, &newStatus) != nil || newStatus.Total != 3 {
		t.Fatalf("WeRead local status did not select its scan: %+v", newStatus)
	}
}

func TestWereadPageRejectsMismatchWithoutLosingProgress(t *testing.T) {
	root := t.TempDir()
	client := wereadTestClient(t, root, t.TempDir())
	const biz = "MzWeread2"
	const bookID = "MP_WXS_987654"
	first := wereadPageJSON(biz, bookID, 0, 2, []wereadPageArticle{{Title: "文章", URL: "https://mp.weixin.qq.com/s/Short_1"}})
	_, body := wereadTestCall(t, client, http.MethodPost, "/api/desktop/weread/page", first, "")
	if body.Code != 0 {
		t.Fatalf("setup page failed: %s", body.Msg)
	}
	for _, payload := range []string{
		wereadPageJSON(biz, bookID, 3, 1, nil),
		wereadPageJSON(biz, "MP_WXS_111111", 2, 1, nil),
		wereadPageJSON(biz, bookID, 2, 1, []wereadPageArticle{{Title: "其他账号", URL: "https://mp.weixin.qq.com/s?__biz=MzOther1&mid=1&idx=1&sn=x"}}),
	} {
		_, body = wereadTestCall(t, client, http.MethodPost, "/api/desktop/weread/page", payload, "")
		if body.Code != 409 && body.Code != 400 {
			t.Fatalf("invalid page code = %d", body.Code)
		}
	}
	_, body = wereadTestCall(t, client, http.MethodGet, "/api/desktop/weread/scan?biz="+biz, "", "")
	var scan wereadScanResponse
	if body.Code != 0 || json.Unmarshal(body.Data, &scan) != nil || scan.Offset != 2 || scan.Pages != 1 || len(scan.Articles) != 1 {
		t.Fatalf("invalid page changed saved progress: %+v", scan)
	}
}

func TestWereadPageRejectsUnsafeInputAndNonlocalOrigin(t *testing.T) {
	client := wereadTestClient(t, t.TempDir(), t.TempDir())
	const biz = "MzWeread3"
	const bookID = "MP_WXS_333333"
	tests := []string{
		wereadPageJSON(biz, "book/../id", 0, 1, nil),
		wereadPageJSON(biz, bookID, 0, 0, []wereadPageArticle{{Title: "不应出现在末页", URL: "https://mp.weixin.qq.com/s/Short_1"}}),
		wereadPageJSON(biz, bookID, 0, 1, []wereadPageArticle{{Title: "HTTP", URL: "http://mp.weixin.qq.com/s/Short_1"}}),
		wereadPageJSON(biz, bookID, 0, 1, []wereadPageArticle{{Title: "外站", URL: "https://example.com/s/Short_1"}}),
		wereadPageJSON(biz, bookID, 0, 1, []wereadPageArticle{{Title: "缺少 biz", URL: "https://mp.weixin.qq.com/s?mid=1&idx=1&sn=x"}}),
		wereadPageJSON(biz, bookID, 0, 1, []wereadPageArticle{{Title: "越界", URL: "https://mp.weixin.qq.com/s/%2e%2e"}}),
		`{"biz":"` + biz + `","book_id":"` + bookID + `","offset":0,"reviews":1,"articles":[],"cookie":"private-cookie"}`,
	}
	for _, payload := range tests {
		_, response := wereadTestCall(t, client, http.MethodPost, "/api/desktop/weread/page", payload, "")
		if response.Code != 400 {
			t.Fatalf("unsafe page accepted with code %d", response.Code)
		}
	}
	httpCode, _ := wereadTestCall(t, client, http.MethodPost, "/api/desktop/weread/page", wereadPageJSON(biz, bookID, 0, 1, nil), "https://weread.qq.com")
	if httpCode != http.StatusForbidden {
		t.Fatalf("cross-origin page HTTP status = %d", httpCode)
	}
	_, response := wereadTestCall(t, client, http.MethodGet, "/api/desktop/weread/scan?biz="+biz, "", "")
	var scan wereadScanResponse
	if response.Code != 0 || json.Unmarshal(response.Data, &scan) != nil || scan.Status != "idle" || scan.Offset != 0 {
		t.Fatalf("unsafe input created progress: %+v", scan)
	}
}
