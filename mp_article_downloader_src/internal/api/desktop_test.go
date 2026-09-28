package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GopeedLab/gopeed/pkg/base"
	downloadpkg "github.com/GopeedLab/gopeed/pkg/download"
	"github.com/gin-gonic/gin"
	"mp_article_batch_downloader/internal/archive"
	downloaderclient "mp_article_batch_downloader/internal/downloader"
	"mp_article_batch_downloader/internal/officialaccount"
)

func TestDesktopInfoAdvertisesCurrentProtocolVersion(t *testing.T) {
	client := &APIClient{
		engine:   gin.New(),
		cfg:      &APIConfig{RootDir: t.TempDir(), DownloadDir: t.TempDir()},
		official: &officialaccount.OfficialAccountClient{},
	}
	client.setupDesktop()
	defer client.archive.Close()

	response := httptest.NewRecorder()
	client.engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/desktop/info", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("desktop info HTTP status = %d", response.Code)
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			App                string `json:"app"`
			Version            int    `json:"version"`
			ProxyCaptureActive *bool  `json:"proxy_capture_active"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != 0 || body.Data.App != "mp-archive-desktop" || body.Data.Version != 7 {
		t.Fatalf("desktop protocol response = %+v", body)
	}
	if body.Data.ProxyCaptureActive == nil {
		t.Fatal("desktop info does not include proxy_capture_active")
	}
}

func TestDesktopScanSummariesRestoreSavedCountsWithoutArticleLinks(t *testing.T) {
	root := t.TempDir()
	scan := archive.Scan{
		Options: archive.Options{Biz: "MzSaved", Mode: "all"}, Status: "complete", Pages: 4,
		Articles: []archive.Article{{ID: "one", URL: "https://mp.weixin.qq.com/s?key=private-value"}, {ID: "two"}},
	}
	data, err := json.Marshal(scan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scans"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scans", archive.ID("MzSaved")+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	paused := archive.Scan{Options: archive.Options{Biz: "MzPaused", Mode: "all"}, Status: "paused",
		ErrorCode: "candidate_unverified", Message: "private-message-marker", Articles: []archive.Article{}}
	data, err = json.Marshal(paused)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scans", archive.ID("MzPaused")+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	client := &APIClient{engine: gin.New(), cfg: &APIConfig{RootDir: root, DownloadDir: t.TempDir()}, official: &officialaccount.OfficialAccountClient{}}
	client.setupDesktop()
	defer client.archive.Close()
	request := httptest.NewRequest(http.MethodGet, "/api/desktop/scan-summaries", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	client.engine.ServeHTTP(response, request)
	var body struct {
		Code int                            `json:"code"`
		Data map[string]archive.ScanSummary `json:"data"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Code != 0 {
		t.Fatalf("scan summary response was invalid: HTTP %d", response.Code)
	}
	got := body.Data["MzSaved"]
	if got.Status != "complete" || got.Pages != 4 || got.ArticleCount != 2 {
		t.Fatalf("saved scan summary was not restored: %+v", got)
	}
	if pausedSummary := body.Data["MzPaused"]; pausedSummary.Status != "paused" || pausedSummary.ErrorCode != "candidate_unverified" || pausedSummary.ArticleCount != 0 {
		t.Fatalf("paused scan summary lost its fixed error code: %+v", pausedSummary)
	}
	if strings.Contains(response.Body.String(), "private-value") || strings.Contains(response.Body.String(), "private-message-marker") || strings.Contains(response.Body.String(), "https://") {
		t.Fatal("scan summary exposed an article link")
	}
}

func TestDesktopLocalStatusChecksDiskAndSurvivesRestart(t *testing.T) {
	root, downloads := t.TempDir(), t.TempDir()
	biz := "MzStatus"
	articles := []archive.Article{
		{ID: archive.ID("article-one"), Title: "第一篇：文章"},
		{ID: archive.ID("article-two"), Title: "第二篇"},
	}
	scan := archive.Scan{Options: archive.Options{Biz: biz, Mode: "all"}, Status: "complete", Articles: articles}
	data, err := json.Marshal(scan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scans"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scans", archive.ID(biz)+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mp.json"), []byte(`{"MzStatus":{"nickname":"每天/晒白牙"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(downloads, "每天_晒白牙")
	write := func(article archive.Article, html, markdown, plain string) {
		t.Helper()
		name := desktopSafeName(article.Title) + "-" + article.ID
		for _, file := range []struct{ format, ext, content string }{
			{"html", ".html", html}, {"markdown", ".md", markdown}, {"text", ".txt", plain},
		} {
			folder := filepath.Join(dir, file.format)
			if err := os.MkdirAll(folder, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(folder, name+file.ext), []byte(file.content), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(articles[0], "<p>正文</p>", "# 正文", "正文")
	write(articles[1], "<p>正文</p>", "", "正文")
	requestStatus := func() localArticleStatus {
		t.Helper()
		client := &APIClient{engine: gin.New(), cfg: &APIConfig{RootDir: root, DownloadDir: downloads}, official: &officialaccount.OfficialAccountClient{}}
		client.setupDesktop()
		defer client.archive.Close()
		response := httptest.NewRecorder()
		client.engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/desktop/local-status?biz="+biz, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("local status HTTP %d: %s", response.Code, response.Body.String())
		}
		var body struct {
			Code int                `json:"code"`
			Data localArticleStatus `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Code != 0 {
			t.Fatalf("local status error: %s", response.Body.String())
		}
		return body.Data
	}
	first := requestStatus()
	if first.Total != 2 || first.Downloaded != 1 || len(first.ArticleIDs) != 1 || first.ArticleIDs[0] != articles[0].ID || first.DownloadDir != dir {
		t.Fatalf("incorrect disk status: %+v", first)
	}
	write(articles[1], "<p>正文</p>", "# 正文", "正文")
	restarted := requestStatus()
	if restarted.Total != 2 || restarted.Downloaded != 2 || len(restarted.ArticleIDs) != 2 {
		t.Fatalf("completed exports were lost after restart: %+v", restarted)
	}
	if err := os.Remove(filepath.Join(dir, "text", desktopSafeName(articles[0].Title)+"-"+articles[0].ID+".txt")); err != nil {
		t.Fatal(err)
	}
	missing := requestStatus()
	if missing.Downloaded != 1 || len(missing.ArticleIDs) != 1 || missing.ArticleIDs[0] != articles[1].ID {
		t.Fatalf("missing export still marked downloaded: %+v", missing)
	}
}

func TestDesktopLocalStatusIgnoresFilesOutsideDownloadDirectory(t *testing.T) {
	root, downloads, outside := t.TempDir(), t.TempDir(), t.TempDir()
	article := archive.Article{ID: archive.ID("outside"), Title: "不应计入"}
	name := desktopSafeName(article.Title) + "-" + article.ID
	for _, format := range []struct{ dir, ext string }{{"html", ".html"}, {"markdown", ".md"}, {"text", ".txt"}} {
		folder := filepath.Join(outside, format.dir)
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, name+format.ext), []byte("正文"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	accountDir := filepath.Join(downloads, "linked-account")
	if err := os.MkdirAll(accountDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"html", "markdown", "text"} {
		if err := os.Symlink(filepath.Join(outside, format), filepath.Join(accountDir, format)); err != nil {
			// Creating symlinks is privilege-gated on some Windows installations.
			t.Skipf("cannot create symlink: %v", err)
		}
	}
	status := localDownloadStatus(archive.Scan{Articles: []archive.Article{article}}, root, downloads, "x")
	if status.Total != 1 || status.Downloaded != 0 || len(status.ArticleIDs) != 0 {
		t.Fatalf("external files counted as downloads: %+v", status)
	}
}

func TestDesktopScanRepairRejectsCrossOrigin(t *testing.T) {
	client := &APIClient{
		engine:   gin.New(),
		cfg:      &APIConfig{RootDir: t.TempDir(), DownloadDir: t.TempDir()},
		official: &officialaccount.OfficialAccountClient{},
	}
	client.setupDesktop()
	defer client.archive.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/desktop/scan/repair", strings.NewReader(`{"biz":"x"}`))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Origin", "https://other.example")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	client.engine.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin repair request returned HTTP %d", response.Code)
	}
}

func TestParseMsgListPageEmptyTerminal(t *testing.T) {
	// A successful getmsg response can have no general_msg_list on its final page.
	page, err := parseMsgListPage(&officialaccount.OfficialMsgListResp{MsgCount: 0, HasMore: 0, NextOffset: 10})
	if err != nil {
		t.Fatalf("empty terminal page failed: %v", err)
	}
	if page.More || len(page.Articles) != 0 {
		t.Fatalf("unexpected terminal page: %+v", page)
	}
}

func TestParseMsgListPageDoesNotAcceptMissingArticles(t *testing.T) {
	for _, response := range []officialaccount.OfficialMsgListResp{
		{MsgCount: 1, HasMore: 1, NextOffset: 10},
		{MsgCount: 1, HasMore: 1, NextOffset: 10, MsgList: `{"list":[`},
		{MsgCount: 1, HasMore: 0, NextOffset: 10, MsgList: `{"list":[]}`},
	} {
		if _, err := parseMsgListPage(&response); err == nil {
			t.Fatalf("accepted incomplete article list: %+v", response)
		}
	}
}

func TestParseAuthorHistoryProducesArchivePage(t *testing.T) {
	page, err := parseAuthorHistory(&officialaccount.ArticleHistoryResponse{
		Pages: 2,
		Articles: []officialaccount.Article{
			{Mid: "1", Title: "第一篇", URL: "https://mp.weixin.qq.com/s?__biz=x&mid=1&idx=1&sn=a&chksm=signed&scene=142&key=secret", PublishTime: 1700000000},
			{Mid: "2", Title: "无效地址", URL: "https://example.com/article"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.ReadPages != 2 || len(page.Articles) != 1 || page.Articles[0].Published != 1700000000 {
		t.Fatalf("unexpected page: %+v", page)
	}
	if strings.Contains(page.Articles[0].URL, "key=") {
		t.Fatalf("credential leaked into stable URL: %s", page.Articles[0].URL)
	}
	if !strings.Contains(page.Articles[0].URL, "chksm=signed") || !strings.Contains(page.Articles[0].URL, "scene=142") ||
		page.Articles[0].ID != archive.ID(archive.StableURL(page.Articles[0].URL)) {
		t.Fatalf("download link lost its signature or identity: %+v", page.Articles[0])
	}
}

func TestDesktopArchiveFallsBackToAuthorCursorHistory(t *testing.T) {
	legacyCalls := 0
	authorCalls := 0
	page, err := fetchDesktopArchivePage(
		func(string, int) (*officialaccount.OfficialMsgListResp, error) {
			legacyCalls++
			return &officialaccount.OfficialMsgListResp{}, nil
		},
		func(string) (*officialaccount.ArticleHistoryResponse, error) {
			authorCalls++
			return &officialaccount.ArticleHistoryResponse{Pages: 2, Articles: []officialaccount.Article{{
				Mid: "1", Title: "来自作者列表", URL: "https://mp.weixin.qq.com/s?__biz=x&mid=1&idx=1&sn=a",
			}}}, nil
		},
		"x",
		0,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if legacyCalls != 2 || authorCalls != 1 || page.ReadPages != 2 || len(page.Articles) != 1 {
		t.Fatalf("fallback failed: page=%+v legacy=%d author=%d", page, legacyCalls, authorCalls)
	}
}

func TestDesktopArchiveTriesAuthorHistoryAfterLegacySessionExpires(t *testing.T) {
	legacyCalls, authorCalls := 0, 0
	page, err := fetchDesktopArchivePage(
		func(string, int) (*officialaccount.OfficialMsgListResp, error) {
			legacyCalls++
			return nil, officialaccount.ErrHistoryCredentialsExpired
		},
		func(string) (*officialaccount.ArticleHistoryResponse, error) {
			authorCalls++
			return &officialaccount.ArticleHistoryResponse{Pages: 2, Articles: []officialaccount.Article{{
				Title: "来自作者列表", URL: "https://mp.weixin.qq.com/s?__biz=x&mid=1&idx=1&sn=a",
			}}}, nil
		},
		"x", 0, true,
	)
	if err != nil || legacyCalls != 1 || authorCalls != 1 || len(page.Articles) != 1 || page.Articles[0].Title != "来自作者列表" {
		t.Fatalf("legacy -3 did not fall back to valid author history: page=%+v err=%v legacy=%d author=%d", page, err, legacyCalls, authorCalls)
	}
}

func TestDesktopArchiveTriesAuthorHistoryAfterLegacyEndpointErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"invalid JSON", officialaccount.ErrHistoryInvalidResponse},
		{"network failure", officialaccount.ErrHistoryNetworkFailure},
		{"verification page", officialaccount.ErrHistoryVerificationRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authorCalls := 0
			page, err := fetchDesktopArchivePage(
				func(string, int) (*officialaccount.OfficialMsgListResp, error) { return nil, tc.err },
				func(string) (*officialaccount.ArticleHistoryResponse, error) {
					authorCalls++
					return &officialaccount.ArticleHistoryResponse{Pages: 2, Articles: []officialaccount.Article{{
						Title: "作者文章", URL: "https://mp.weixin.qq.com/s?__biz=x&mid=2&idx=1&sn=b",
					}}}, nil
				},
				"x", 0, true,
			)
			if err != nil || authorCalls != 1 || len(page.Articles) != 1 || page.Articles[0].Title != "作者文章" {
				t.Fatalf("usable author list not returned after legacy error: page=%+v err=%v calls=%d", page, err, authorCalls)
			}
		})
	}
}

func TestDesktopArchiveReturnsAuthorPagesBeforeLaterFailure(t *testing.T) {
	page, err := fetchDesktopArchivePage(
		func(string, int) (*officialaccount.OfficialMsgListResp, error) {
			return nil, officialaccount.ErrHistoryCredentialsMissing
		},
		func(string) (*officialaccount.ArticleHistoryResponse, error) {
			return &officialaccount.ArticleHistoryResponse{Pages: 2, Articles: []officialaccount.Article{{
				Title: "已读取的文章", URL: "https://mp.weixin.qq.com/s?__biz=x&mid=2&idx=1&sn=b",
			}}}, officialaccount.ErrHistoryNetworkFailure
		},
		"x", 0, true,
	)
	var failure *archive.HistoryFailure
	if len(page.Articles) != 1 || page.ReadPages != 2 || !errors.As(err, &failure) || failure.Code != "network" {
		t.Fatalf("partial author list was lost or marked complete: page=%+v err=%v", page, err)
	}
}

func TestDesktopArchiveKeepsExpiredErrorWithoutUsableAuthorHistory(t *testing.T) {
	for _, tc := range []struct {
		name        string
		fetchAuthor func(string) (*officialaccount.ArticleHistoryResponse, error)
		canTry      bool
	}{
		{"missing author ID", func(string) (*officialaccount.ArticleHistoryResponse, error) {
			return nil, officialaccount.ErrAuthorHistoryUnavailable
		}, false},
		{"empty author list", func(string) (*officialaccount.ArticleHistoryResponse, error) {
			return &officialaccount.ArticleHistoryResponse{Pages: 1}, nil
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := fetchDesktopArchivePage(
				func(string, int) (*officialaccount.OfficialMsgListResp, error) {
					return nil, officialaccount.ErrHistoryCredentialsExpired
				}, tc.fetchAuthor, "x", 0, tc.canTry,
			)
			var failure *archive.HistoryFailure
			if len(page.Articles) != 0 || !errors.As(err, &failure) || failure.Code != "credentials_expired" {
				t.Fatalf("expired legacy credentials were mistaken for completed history: page=%+v err=%v", page, err)
			}
		})
	}
}

func TestDesktopArchiveReportsRejectedAuthorCandidate(t *testing.T) {
	_, err := fetchDesktopArchivePage(
		func(string, int) (*officialaccount.OfficialMsgListResp, error) {
			return nil, officialaccount.ErrHistoryCredentialsExpired
		},
		func(string) (*officialaccount.ArticleHistoryResponse, error) {
			return nil, errors.Join(officialaccount.ErrAuthorHistoryUnavailable, officialaccount.ErrCandidateAuthorHistoryRejected)
		},
		"x", 0, true,
	)
	if !errors.Is(err, archive.ErrHistoryUnavailable) {
		t.Fatalf("rejected candidate should report no usable author entry: %v", err)
	}
}

func TestDesktopArchiveDoesNotRestartAuthorCursorAtLaterLegacyOffset(t *testing.T) {
	authorCalls := 0
	_, err := fetchDesktopArchivePage(
		func(string, int) (*officialaccount.OfficialMsgListResp, error) {
			return nil, officialaccount.ErrHistoryCredentialsExpired
		},
		func(string) (*officialaccount.ArticleHistoryResponse, error) {
			authorCalls++
			return &officialaccount.ArticleHistoryResponse{}, nil
		},
		"x", 10, true,
	)
	var failure *archive.HistoryFailure
	if authorCalls != 0 || !errors.As(err, &failure) || failure.Code != "credentials_expired" {
		t.Fatalf("later offset crossed into incompatible author cursor: calls=%d err=%v", authorCalls, err)
	}
}

func TestDesktopArchiveDoesNotAcceptTwoEmptySources(t *testing.T) {
	_, err := fetchDesktopArchivePage(
		func(string, int) (*officialaccount.OfficialMsgListResp, error) {
			return &officialaccount.OfficialMsgListResp{}, nil
		},
		func(string) (*officialaccount.ArticleHistoryResponse, error) {
			return &officialaccount.ArticleHistoryResponse{Pages: 1}, nil
		},
		"x",
		0,
		true,
	)
	if err == nil {
		t.Fatal("accepted empty legacy and author histories as complete")
	}
}

func TestDesktopArchiveDoesNotUseAuthorPathForPublicLinkWithoutSession(t *testing.T) {
	authorCalls := 0
	_, err := fetchDesktopArchivePage(
		func(string, int) (*officialaccount.OfficialMsgListResp, error) {
			return nil, officialaccount.ErrHistoryCredentialsMissing
		},
		func(string) (*officialaccount.ArticleHistoryResponse, error) {
			authorCalls++
			return nil, nil
		},
		"x", 0, false,
	)
	var failure *archive.HistoryFailure
	if !errors.As(err, &failure) || failure.Code != "credentials_missing" || authorCalls != 0 {
		t.Fatalf("missing session classified as %v, author calls %d", err, authorCalls)
	}
}

func TestDesktopArchiveAuthorPathCanWorkWithoutLegacyUin(t *testing.T) {
	page, err := fetchDesktopArchivePage(
		func(string, int) (*officialaccount.OfficialMsgListResp, error) {
			return nil, officialaccount.ErrHistoryCredentialsMissing
		},
		func(string) (*officialaccount.ArticleHistoryResponse, error) {
			return &officialaccount.ArticleHistoryResponse{Pages: 2, Articles: []officialaccount.Article{{
				Title: "作者文章", URL: "https://mp.weixin.qq.com/s?__biz=x&mid=2&idx=1&sn=b",
			}}}, nil
		},
		"x", 0, true,
	)
	if err != nil || len(page.Articles) != 1 || page.Articles[0].Title != "作者文章" {
		t.Fatalf("author path was blocked by missing legacy Uin: page=%+v err=%v", page, err)
	}
}

func TestArchiveCompletesAfterOneArticleAndEmptyTerminal(t *testing.T) {
	first := `{"list":[{"comm_msg_info":{"datetime":1700000000},"app_msg_ext_info":{"title":"第一篇","content_url":"https://mp.weixin.qq.com/s?__biz=x&mid=1&idx=1&sn=a&chksm=signed&scene=142&key=SECRET"}}]}`
	m := archive.New(t.TempDir(), func(_ string, offset int) (archive.Page, error) {
		return fetchArchivePage(func(_ string, offset int) (*officialaccount.OfficialMsgListResp, error) {
			switch offset {
			case 0:
				return &officialaccount.OfficialMsgListResp{MsgCount: 1, HasMore: 1, NextOffset: 10, MsgList: first}, nil
			case 10:
				return &officialaccount.OfficialMsgListResp{MsgCount: 0, HasMore: 0, NextOffset: 10}, nil
			default:
				return nil, errors.New("unexpected offset")
			}
		}, "x", offset)
	})
	defer m.Close()
	if err := m.Start(archive.Options{Biz: "x", Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		scan := m.Get("x")
		if scan.Status == "complete" {
			if len(scan.Articles) != 1 || scan.Articles[0].Title != "第一篇" || scan.Pages != 2 ||
				!strings.Contains(scan.Articles[0].URL, "chksm=signed") || strings.Contains(scan.Articles[0].URL, "SECRET") {
				t.Fatalf("unexpected scan result: %+v", scan)
			}
			return
		}
		if scan.Status == "error" {
			t.Fatalf("scan failed: %+v", scan)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("scan did not complete: %+v", m.Get("x"))
}

func TestFetchArchivePageRetriesMalformedList(t *testing.T) {
	calls := 0
	page, err := fetchArchivePage(func(string, int) (*officialaccount.OfficialMsgListResp, error) {
		calls++
		if calls == 1 {
			return &officialaccount.OfficialMsgListResp{MsgCount: 1, HasMore: 1, NextOffset: 10, MsgList: `{"list":[`}, nil
		}
		return &officialaccount.OfficialMsgListResp{MsgCount: 0, HasMore: 0, NextOffset: 10}, nil
	}, "x", 10)
	if err != nil || page.More || calls != 3 {
		t.Fatalf("retry failed: page=%+v err=%v calls=%d", page, err, calls)
	}
}

func TestFetchArchivePageRecoversFromTransientEmptyPage(t *testing.T) {
	calls := 0
	article := `{"list":[{"app_msg_ext_info":{"title":"下一篇","content_url":"https://mp.weixin.qq.com/s?__biz=x&mid=2&idx=1&sn=b"}}]}`
	page, err := fetchArchivePage(func(string, int) (*officialaccount.OfficialMsgListResp, error) {
		calls++
		if calls == 1 {
			return &officialaccount.OfficialMsgListResp{MsgCount: 0, HasMore: 0, NextOffset: 10}, nil
		}
		return &officialaccount.OfficialMsgListResp{MsgCount: 1, HasMore: 1, NextOffset: 20, MsgList: article}, nil
	}, "x", 10)
	if err != nil || !page.More || len(page.Articles) != 1 || page.Articles[0].Title != "下一篇" || calls != 2 {
		t.Fatalf("transient empty page truncated the scan: page=%+v err=%v calls=%d", page, err, calls)
	}
}

func TestFetchArchivePageRejectsUnconfirmedEmptyPage(t *testing.T) {
	calls := 0
	_, err := fetchArchivePage(func(string, int) (*officialaccount.OfficialMsgListResp, error) {
		calls++
		return &officialaccount.OfficialMsgListResp{MsgCount: 0, HasMore: 0, NextOffset: 10 + calls}, nil
	}, "x", 10)
	if err == nil || calls != 3 {
		t.Fatalf("unconfirmed empty page was accepted: err=%v calls=%d", err, calls)
	}
}

func summaryTask(t *testing.T, path, status string, created time.Time, labels map[string]string) *downloadpkg.Task {
	t.Helper()
	payload := map[string]any{
		"status":    status,
		"createdAt": created,
		"updatedAt": created.Add(time.Minute),
		"meta": map[string]any{
			"req":  map[string]any{"url": "officialaccount://test", "labels": labels},
			"opts": map[string]any{"path": path},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var task downloadpkg.Task
	if err = json.Unmarshal(b, &task); err != nil {
		t.Fatal(err)
	}
	return &task
}

func TestSummarizeDownloadBatchesKeepsRestoredFailure(t *testing.T) {
	created := time.Unix(300, 0)
	task := summaryTask(t, "/downloads/account", "pause", created, map[string]string{"batch_id": "batch"})
	summaries := summarizeDownloadBatches([]*downloadpkg.Task{task}, map[string]string{task.ID: "文章已被微信限制，正文无法查看"})
	if len(summaries) != 1 || summaries[0].Failed != 1 || summaries[0].Paused != 0 {
		t.Fatalf("restored failure was misclassified: %+v", summaries)
	}
}

func TestSummarizeDownloadBatchesUsesOriginalSingleArticleMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "保存目录_截断后的公众号名")
	task := summaryTask(t, path, "running", time.Now(), map[string]string{
		"batch_id":      "single",
		"batch_total":   "1",
		"account_name":  "原始公众号名称：不限于目录安全字符",
		"article_title": "文章原始标题：完整且不截断 / 第一篇",
	})
	task.Meta.Opts.Name = "文章原始标题_截断-0123456789abcdef"
	summaries := summarizeDownloadBatches([]*downloadpkg.Task{task}, nil)
	if len(summaries) != 1 {
		t.Fatalf("got %d summaries, want 1", len(summaries))
	}
	summary := summaries[0]
	if summary.Total != 1 || summary.AccountName != "原始公众号名称：不限于目录安全字符" || summary.ArticleTitle != "文章原始标题：完整且不截断 / 第一篇" {
		t.Fatalf("single article lost original metadata: %+v", summary)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["account_name"] != summary.AccountName || fields["article_title"] != summary.ArticleTitle {
		t.Fatalf("summary API field names changed: %s", encoded)
	}
}

func TestSummarizeDownloadBatchesRecoversLegacySingleArticle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "旧公众号目录")
	task := summaryTask(t, path, "error", time.Now(), map[string]string{"batch_id": "old-single"})
	task.Meta.Opts.Name = "曾经下载的文章-0123456789abcdef"
	summaries := summarizeDownloadBatches([]*downloadpkg.Task{task}, nil)
	if len(summaries) != 1 || summaries[0].AccountName != "旧公众号目录" || summaries[0].ArticleTitle != "曾经下载的文章" {
		t.Fatalf("legacy task did not recover display names from path and filename: %+v", summaries)
	}
}

func TestSummarizeDownloadBatchesRecoversLegacyDirectArticleRandomSuffix(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"LoRA 微调实战-cc610f02-301", "LoRA 微调实战"},
		{"PyTorch 入门-291f9352-0c1", "PyTorch 入门"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := summaryTask(t, filepath.Join(t.TempDir(), "公众号"), "error", time.Now(), map[string]string{
				"batch_id": "direct", "batch_total": "1",
			})
			task.Meta.Opts.Name = tc.name
			summaries := summarizeDownloadBatches([]*downloadpkg.Task{task}, nil)
			if len(summaries) != 1 || summaries[0].ArticleTitle != tc.want {
				t.Fatalf("legacy direct article title was not recovered: %+v", summaries)
			}
		})
	}

	// A batch task without the single-article marker may have this text as a
	// legitimate ending, so leave it intact rather than guessing.
	task := summaryTask(t, filepath.Join(t.TempDir(), "公众号"), "error", time.Now(), map[string]string{"batch_id": "batch"})
	task.Meta.Opts.Name = "专题-cc610f02-301"
	summaries := summarizeDownloadBatches([]*downloadpkg.Task{task}, nil)
	if len(summaries) != 1 || summaries[0].ArticleTitle != task.Meta.Opts.Name {
		t.Fatalf("unmarked task title was stripped: %+v", summaries)
	}
}

func TestSummarizeDownloadBatchesDoesNotPresentOneArticleAsBatchTitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "文件夹名")
	first := summaryTask(t, path, "running", time.Now(), map[string]string{
		"batch_id":      "multi",
		"account_name":  "公众号名",
		"article_title": "第一篇",
	})
	second := summaryTask(t, path, "error", time.Now().Add(time.Second), map[string]string{
		"batch_id":      "multi",
		"account_name":  "公众号名",
		"article_title": "第二篇",
	})
	summaries := summarizeDownloadBatches([]*downloadpkg.Task{first, second}, nil)
	if len(summaries) != 1 || summaries[0].Total != 2 || summaries[0].AccountName != "公众号名" || summaries[0].ArticleTitle != "" {
		t.Fatalf("multi-article batch has an ambiguous article title: %+v", summaries)
	}

	// Batch totals can be declared before the later tasks enter the queue.
	first.Meta.Req.Labels["batch_total"] = "5"
	summaries = summarizeDownloadBatches([]*downloadpkg.Task{first}, nil)
	if len(summaries) != 1 || summaries[0].Total != 5 || summaries[0].ArticleTitle != "" {
		t.Fatalf("declared multi-article batch has an ambiguous article title: %+v", summaries)
	}
}

func TestBatchVerificationStopsBothDownloadModes(t *testing.T) {
	task := summaryTask(t, "/downloads/account", "error", time.Now(), map[string]string{"batch_id": "batch", "download_mode": "fast"})
	for _, mode := range []string{"fast", "safe"} {
		task.Meta.Req.Labels["download_mode"] = mode
		for _, message := range []string{"微信公众号凭证已失效或触发访问验证", "微信要求完成访问验证，请在微信中打开文章后重试"} {
			event := &downloadpkg.Event{Key: downloadpkg.EventKeyError, Task: task, Err: errors.New(message)}
			if got := batchVerificationID(event); got != "batch" {
				t.Fatalf("mode %q: got batch %q for %q", mode, got, message)
			}
		}
	}
	task.Meta.Req.Labels["batch_id"] = ""
	event := &downloadpkg.Event{Key: downloadpkg.EventKeyError, Task: task, Err: errors.New("微信要求完成访问验证")}
	if got := batchVerificationID(event); got != "" {
		t.Fatalf("unlabelled task triggered batch pause: %q", got)
	}
}

func TestDesktopQueueReportsBlockedBatchWithoutCreatingTasks(t *testing.T) {
	d := downloadpkg.NewDownloader(&downloadpkg.DownloaderConfig{StorageDir: t.TempDir()})
	client := &APIClient{
		engine:     gin.New(),
		cfg:        &APIConfig{RootDir: t.TempDir(), DownloadDir: t.TempDir()},
		official:   &officialaccount.OfficialAccountClient{},
		downloader: d,
	}
	client.setupDesktop()
	defer client.archive.Close()
	failed := summaryTask(t, "/downloads/account", "error", time.Now(), map[string]string{"batch_id": "blocked-batch", "download_mode": "safe"})
	client.pauseBatchOnVerification(&downloadpkg.Event{Key: downloadpkg.EventKeyError, Task: failed, Err: errors.New("微信要求完成访问验证")})
	request := `[ { "URL": "officialaccount://https://mp.weixin.qq.com/s/test", "Extra": { "batch_id": "blocked-batch" } } ]`
	response := httptest.NewRecorder()
	client.engine.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/desktop/queue", strings.NewReader(request)))
	if response.Code != http.StatusOK {
		t.Fatalf("queue HTTP status = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Created              int      `json:"created"`
			CreatedIDs           []string `json:"created_ids"`
			Blocked              int      `json:"blocked"`
			VerificationRequired bool     `json:"verification_required"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != 0 || body.Data.Created != 0 || len(body.Data.CreatedIDs) != 0 || body.Data.Blocked != 1 || !body.Data.VerificationRequired {
		t.Fatalf("blocked batch was misreported: %+v", body)
	}
}

func TestDesktopQueueReturnsCreatedTaskIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("test download"))
	}))
	defer server.Close()
	root := t.TempDir()
	storage := downloadpkg.NewMemStorage()
	if err := storage.Setup([]string{"task", "save", "config"}); err != nil {
		t.Fatal(err)
	}
	d := downloadpkg.NewDownloader(&downloadpkg.DownloaderConfig{
		Storage:               storage,
		StorageDir:            root,
		DownloaderStoreConfig: &base.DownloaderStoreConfig{MaxRunning: 1, DownloadDir: root, Proxy: &base.DownloaderProxyConfig{}},
	})
	defer d.Close()
	client := &APIClient{
		engine:        gin.New(),
		cfg:           &APIConfig{RootDir: root, DownloadDir: root},
		official:      &officialaccount.OfficialAccountClient{},
		downloader:    d,
		downloader_ws: downloaderclient.NewDownloaderClient(),
	}
	client.setupDesktop()
	defer client.archive.Close()
	request, err := json.Marshal([]DownloadTaskPayload{{URL: server.URL, Filename: "pilot.txt", Dir: "account", Extra: map[string]string{"batch_id": "pilot"}}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	client.engine.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/desktop/queue", strings.NewReader(string(request))))
	if response.Code != http.StatusOK {
		t.Fatalf("queue HTTP status = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Created    int      `json:"created"`
			CreatedIDs []string `json:"created_ids"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != 0 || body.Data.Created != 1 || len(body.Data.CreatedIDs) != 1 || d.GetTask(body.Data.CreatedIDs[0]) == nil {
		t.Fatalf("created task IDs do not match the queue: %+v", body)
	}
}

func TestSummarizeDownloadBatchesUsesLatestBatchAndTotalHint(t *testing.T) {
	old := time.Unix(100, 0)
	latest := time.Unix(200, 0)
	labels := map[string]string{"batch_id": "new", "batch_total": "5", "batch_started_at": "200000"}
	summaries := summarizeDownloadBatches([]*downloadpkg.Task{
		summaryTask(t, "/downloads/account", "done", old, nil),
		summaryTask(t, "/downloads/account", "running", latest, labels),
		summaryTask(t, "/downloads/account", "error", latest.Add(time.Second), labels),
	}, nil)
	if len(summaries) != 1 {
		t.Fatalf("got %d summaries", len(summaries))
	}
	s := summaries[0]
	if s.BatchID != "new" || s.Total != 5 || s.Running != 1 || s.Failed != 1 || s.Completed != 0 {
		t.Fatalf("unexpected summary: %+v", s)
	}
	if s.StartedAt != 200000 || s.FinishedAt != 0 {
		t.Fatalf("unexpected timing: %+v", s)
	}
}
