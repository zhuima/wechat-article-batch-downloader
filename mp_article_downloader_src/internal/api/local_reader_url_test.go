package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"mp_article_batch_downloader/internal/archive"
	"mp_article_batch_downloader/internal/officialaccount"
)

func readerSourceURL(t *testing.T, client *APIClient, biz, id string) (string, string) {
	t.Helper()
	response := localReaderRequest(t, localReaderFixture{client: client}, "/api/desktop/article?biz="+biz+"&id="+id)
	if response.Code != http.StatusOK {
		t.Fatalf("reader HTTP %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != 0 {
		t.Fatalf("reader error: %s", response.Body.String())
	}
	return body.Data.URL, response.Body.String()
}

func TestLocalReaderReturnsCredentialFreeURLFromOldExportJournal(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	base := "保留原文链接"
	raw := "https://mp.weixin.qq.com/s/source_slug?key=old-key&pass_ticket=old-ticket&uin=old-uin&scene=1"
	stable := archive.StableURL(raw)
	writeLocalLibraryTriad(t, fixture.account, base, "# 保留原文链接\n\n正文")
	writeLocalLibraryCorpus(t, fixture.account, map[string]string{
		"title": base, "url": raw, "markdown_path": "markdown/" + base + ".md",
	})
	got, body := readerSourceURL(t, fixture.client, fixture.biz, localLibraryID(fixture.biz, base))
	if got != stable || strings.Contains(body, "old-key") || strings.Contains(body, "old-ticket") || strings.Contains(body, "old-uin") {
		t.Fatalf("reader exposed unsafe original URL: got=%q", got)
	}
	if got := safeReaderArticleURL("https://evil.example/s/source_slug?key=secret"); got != "" {
		t.Fatalf("non-WeChat URL accepted: %q", got)
	}
	if got := safeReaderArticleURL("https://mp.weixin.qq.com/s?__biz=MzOnly&key=secret"); got != "" {
		t.Fatalf("article link without stable article identity accepted: %q", got)
	}
}

func TestLocalReaderReturnsCredentialFreeScanURL(t *testing.T) {
	root, downloads := t.TempDir(), t.TempDir()
	biz := "MzArticleSource"
	raw := "https://mp.weixin.qq.com/s?__biz=MzArticleSource&mid=123&idx=1&sn=article&chksm=signature&scene=1&key=old-key&pass_ticket=old-ticket"
	stable := archive.StableURL(raw)
	publicURL := archive.DownloadURL(raw)
	article := archive.Article{ID: archive.ID(stable), Title: "历史原文", URL: raw, Published: 1_700_000_000}
	scan, err := json.Marshal(archive.Scan{Options: archive.Options{Biz: biz}, Articles: []archive.Article{article}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scans"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scans", archive.ID(biz)+".json"), scan, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mp.json"), []byte(`{"MzArticleSource":{"nickname":"历史原文账号"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeLocalLibraryTriad(t, filepath.Join(downloads, "历史原文账号"), desktopSafeName(article.Title)+"-"+article.ID, "# 历史原文\n\n正文")
	client := &APIClient{engine: gin.New(), cfg: &APIConfig{RootDir: root, DownloadDir: downloads}, official: &officialaccount.OfficialAccountClient{}}
	client.setupDesktop()
	t.Cleanup(client.archive.Close)
	got, body := readerSourceURL(t, client, biz, article.ID)
	if got != publicURL || !strings.Contains(got, "chksm=signature") || !strings.Contains(got, "scene=1") || strings.Contains(body, "old-key") || strings.Contains(body, "old-ticket") {
		t.Fatalf("scan source URL was not sanitized: got=%q", got)
	}
}

func TestLocalReaderBackfillsURLFromVerifiedCompletedTask(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	base := "旧版导出"
	raw := "https://mp.weixin.qq.com/s/task_slug?key=old-key&pass_ticket=old-ticket"
	writeLocalLibraryTriad(t, fixture.account, base, "# 旧版导出\n\n正文")
	task := completedArticleTask(t, fixture.account, base, raw, map[string]string{"account_biz": fixture.biz})
	attachImportedStatusTasks(t, fixture.client, task)
	got, body := readerSourceURL(t, fixture.client, fixture.biz, localLibraryID(fixture.biz, base))
	if got != archive.StableURL(raw) || strings.Contains(body, "old-key") || strings.Contains(body, "old-ticket") {
		t.Fatalf("completed task did not safely backfill source URL: got=%q", got)
	}
}
