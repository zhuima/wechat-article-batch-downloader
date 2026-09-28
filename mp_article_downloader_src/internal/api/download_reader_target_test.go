package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GopeedLab/gopeed/pkg/base"
	downloadpkg "github.com/GopeedLab/gopeed/pkg/download"
	"github.com/gin-gonic/gin"
	"mp_article_batch_downloader/internal/archive"
	"mp_article_batch_downloader/internal/officialaccount"
)

func completedArticleTask(t *testing.T, path, name, rawURL string, labels map[string]string) *downloadpkg.Task {
	t.Helper()
	task := summaryTask(t, path, "done", time.Now(), labels)
	task.Protocol = "officialaccount"
	task.Meta.Opts.Name = name
	task.Meta.Req.URL = "officialaccount://" + rawURL
	return task
}

func TestTaskLocalArticleResolverUsesExactSourceForSameTitle(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	firstName, secondName := "同名文章-first", "同名文章-second"
	firstURL := "https://mp.weixin.qq.com/s/first?key=secret-one"
	secondURL := "https://mp.weixin.qq.com/s/second?key=secret-two"
	writeLocalLibraryTriad(t, fixture.account, firstName, "# 正文标题\n\n第一篇")
	writeLocalLibraryTriad(t, fixture.account, secondName, "# 正文标题\n\n第二篇")
	writeLocalLibraryCorpus(t, fixture.account,
		map[string]string{"title": "同名文章", "url": firstURL, "markdown_path": "markdown/" + firstName + ".md"},
		map[string]string{"title": "同名文章", "url": secondURL, "markdown_path": "markdown/" + secondName + ".md"},
	)
	resolver := newTaskLocalArticleResolver(fixture.client)
	first := resolver.resolve(completedArticleTask(t, fixture.account, firstName, firstURL, nil))
	second := resolver.resolve(completedArticleTask(t, fixture.account, secondName, secondURL, nil))
	if first == nil || second == nil || first.Biz != fixture.biz || second.Biz != fixture.biz ||
		first.Title != "同名文章" || second.Title != "同名文章" || first.ID == second.ID ||
		first.ID != localLibraryID(fixture.biz, firstName) || second.ID != localLibraryID(fixture.biz, secondName) {
		t.Fatalf("same-title articles did not resolve to distinct local files: first=%+v second=%+v", first, second)
	}
	if wrongSource := resolver.resolve(completedArticleTask(t, fixture.account, firstName, secondURL, nil)); wrongSource != nil {
		t.Fatalf("task with another article URL claimed the first file: %+v", wrongSource)
	}
	if wrongAccount := resolver.resolve(completedArticleTask(t, fixture.account, firstName, firstURL, map[string]string{"account_biz": "MzOther"})); wrongAccount != nil {
		t.Fatalf("cross-account task claimed a local file: %+v", wrongAccount)
	}
}

func TestTaskLocalArticleResolverRejectsMissingPartialAndInactiveTasks(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	name := "完整文章-old"
	url := "https://mp.weixin.qq.com/s/complete"
	writeLocalLibraryTriad(t, fixture.account, name, "# 完整文章")
	newTask := func() *downloadpkg.Task {
		return completedArticleTask(t, fixture.account, name, url, map[string]string{"account_biz": fixture.biz})
	}
	if got := newTaskLocalArticleResolver(fixture.client).resolve(newTask()); got == nil || got.ID != localLibraryID(fixture.biz, name) {
		t.Fatalf("complete legacy file did not resolve: %+v", got)
	}
	for _, status := range []string{"running", "error", "pause"} {
		task := newTask()
		task.Status = base.Status(status)
		if got := newTaskLocalArticleResolver(fixture.client).resolve(task); got != nil {
			t.Fatalf("%s task exposed a reader target: %+v", status, got)
		}
	}
	if got := newTaskLocalArticleResolver(fixture.client).resolve(completedArticleTask(t, fixture.account, "missing", url, nil)); got != nil {
		t.Fatalf("missing task exposed a reader target: %+v", got)
	}
	textPath := filepath.Join(fixture.account, "text", name+".txt")
	if err := os.Remove(textPath); err != nil {
		t.Fatal(err)
	}
	if got := newTaskLocalArticleResolver(fixture.client).resolve(newTask()); got != nil {
		t.Fatalf("partial export exposed a reader target: %+v", got)
	}
}

func TestTaskLocalArticleResolverMapsCompletedBatchToScanID(t *testing.T) {
	root, downloads := t.TempDir(), t.TempDir()
	biz, nickname := "MzBatch", "阅读测试"
	sourceURL := "https://mp.weixin.qq.com/s?__biz=MzBatch&mid=2247483786&idx=1&sn=source"
	article := archive.Article{ID: archive.ID(archive.StableURL(sourceURL)), Title: "历史文章", URL: sourceURL, Published: 1_700_000_000}
	scan := archive.Scan{Options: archive.Options{Biz: biz, Mode: "all"}, Status: "complete", Articles: []archive.Article{article}}
	scanBytes, err := json.Marshal(scan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scans"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scans", archive.ID(biz)+".json"), scanBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mp.json"), []byte(`{"MzBatch":{"nickname":"阅读测试"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	account := filepath.Join(downloads, nickname)
	name := desktopSafeName(article.Title) + "-" + article.ID
	writeLocalLibraryTriad(t, account, name, "# 历史文章\n\n正文")
	client := &APIClient{engine: gin.New(), cfg: &APIConfig{RootDir: root, DownloadDir: downloads}, official: &officialaccount.OfficialAccountClient{}}
	client.setupDesktop()
	t.Cleanup(client.archive.Close)
	// Older batch tasks have no account_biz; the unique mp.json nickname and
	// verified output directory still identify their owning account.
	task := completedArticleTask(t, account, name, sourceURL, map[string]string{"account_name": nickname, "article_id": "2247483786_1"})
	got := newTaskLocalArticleResolver(client).resolve(task)
	if got == nil || got.Biz != biz || got.ID != article.ID || got.Title != article.Title {
		t.Fatalf("completed scan task did not resolve by scan ID: %+v", got)
	}
	wrongURL := completedArticleTask(t, account, name, "https://mp.weixin.qq.com/s?__biz=MzBatch&mid=2247483787&idx=1&sn=other", nil)
	if got := newTaskLocalArticleResolver(client).resolve(wrongURL); got != nil {
		t.Fatalf("different source URL claimed the scan article: %+v", got)
	}
	// A completed task in another folder must not claim the scan's verified
	// files, even when it carries the same article ID and account label.
	otherAccount := filepath.Join(downloads, "其他账号")
	writeLocalLibraryTriad(t, otherAccount, name, "# 历史文章")
	task.Meta.Opts.Path = otherAccount
	if got := newTaskLocalArticleResolver(client).resolve(task); got != nil {
		t.Fatalf("cross-account scan task exposed a reader target: %+v", got)
	}
	// The actual task-list API must expose the verified target with the same
	// JSON shape used by the download-center UI.
	task.Meta.Opts.Path = account
	task.ID = "completed-batch-task"
	task.Progress = &downloadpkg.Progress{}
	storage := downloadpkg.NewBoltStorage(t.TempDir())
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.Setup([]string{"task", "save", "config"}); err != nil {
		t.Fatal(err)
	}
	if err := storage.Put("task", task.ID, task); err != nil {
		t.Fatal(err)
	}
	downloader := downloadpkg.NewDownloader(&downloadpkg.DownloaderConfig{Storage: storage, StorageDir: t.TempDir()})
	if err := downloader.Setup(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = downloader.Close() })
	client.downloader = downloader
	client.engine.GET("/api/task/list", client.handleFetchTaskList)
	request := httptest.NewRequest(http.MethodGet, "/api/task/list?status=done&page=1&page_size=20", nil)
	response := httptest.NewRecorder()
	client.engine.ServeHTTP(response, request)
	var body struct {
		Code int `json:"code"`
		Data struct {
			List []struct {
				LocalArticle *taskLocalArticle `json:"local_article"`
				FilesExist   bool              `json:"files_exist"`
			} `json:"list"`
		} `json:"data"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil ||
		body.Code != 0 || len(body.Data.List) != 1 || !body.Data.List[0].FilesExist ||
		body.Data.List[0].LocalArticle == nil || *body.Data.List[0].LocalArticle != *got {
		t.Fatalf("task-list API lost the verified reading target: HTTP %d, body %s", response.Code, response.Body.String())
	}
}
