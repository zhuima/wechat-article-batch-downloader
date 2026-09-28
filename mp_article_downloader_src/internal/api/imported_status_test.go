package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GopeedLab/gopeed/pkg/base"
	downloadpkg "github.com/GopeedLab/gopeed/pkg/download"
	"github.com/gin-gonic/gin"
	"mp_article_batch_downloader/internal/archive"
	"mp_article_batch_downloader/internal/officialaccount"
)

func requestImportedStatus(t *testing.T, client *APIClient, sourceID, biz string) (int, importedArticleStatus) {
	t.Helper()
	path := "/api/desktop/imported-status?source_id=" + url.QueryEscape(sourceID)
	if biz != "" {
		path += "&biz=" + url.QueryEscape(biz)
	}
	response := localReaderRequest(t, localReaderFixture{client: client}, path)
	var body struct {
		Code int                   `json:"code"`
		Data importedArticleStatus `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Code, body.Data
}

func attachImportedStatusTasks(t *testing.T, client *APIClient, tasks ...*downloadpkg.Task) {
	t.Helper()
	storage := downloadpkg.NewBoltStorage(t.TempDir())
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.Setup([]string{"task", "save", "config"}); err != nil {
		t.Fatal(err)
	}
	for i, task := range tasks {
		task.ID = "import-status-task-" + strconv.Itoa(i)
		task.Progress = &downloadpkg.Progress{}
		if err := storage.Put("task", task.ID, task); err != nil {
			t.Fatal(err)
		}
	}
	downloader := downloadpkg.NewDownloader(&downloadpkg.DownloaderConfig{Storage: storage, StorageDir: t.TempDir()})
	if err := downloader.Setup(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = downloader.Close() })
	client.downloader = downloader
}

func TestImportedStatusFindsCompletedLibraryAfterTaskRemoved(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	sourceURL := "https://mp.weixin.qq.com/s/article-one?key=session-secret&pass_ticket=hidden"
	sourceID := archive.ID(archive.StableURL(sourceURL))
	base := "文章一-2026"
	writeLocalLibraryTriad(t, fixture.account, base, "# 文章一\n\n内容")
	writeLocalLibraryCorpus(t, fixture.account, map[string]string{
		"title": "文章一", "url": sourceURL, "markdown_path": "markdown/" + base + ".md",
	})
	code, got := requestImportedStatus(t, fixture.client, sourceID, "")
	if code != 0 || got.State != "readable" || got.LocalArticle == nil ||
		got.LocalArticle.Biz != fixture.biz || got.LocalArticle.ID != localLibraryID(fixture.biz, base) || got.LocalArticle.Title != "文章一" {
		t.Fatalf("completed library not linked: code=%d status=%+v", code, got)
	}
	response := localReaderRequest(t, localReaderFixture{client: fixture.client}, "/api/desktop/imported-status?source_id="+sourceID)
	if strings.Contains(response.Body.String(), "session-secret") || strings.Contains(response.Body.String(), "pass_ticket") || strings.Contains(response.Body.String(), sourceURL) {
		t.Fatal("imported-status exposed the source URL or credentials")
	}
	if code, got = requestImportedStatus(t, fixture.client, sourceID, "MzOther"); code != 0 || got.State != "none" {
		t.Fatalf("another account claimed imported article: code=%d status=%+v", code, got)
	}
	if code, got = requestImportedStatus(t, fixture.client, "not-a-source-id", ""); code != 400 || got.State != "" {
		t.Fatalf("malformed source was accepted: code=%d status=%+v", code, got)
	}
}

func TestImportedStatusFindsCompletedScanWithoutTask(t *testing.T) {
	root, downloads := t.TempDir(), t.TempDir()
	biz := "MzScanImport"
	sourceURL := "https://mp.weixin.qq.com/s?__biz=MzScanImport&mid=123&idx=1&sn=article&key=secret"
	sourceID := archive.ID(archive.StableURL(sourceURL))
	article := archive.Article{ID: sourceID, URL: sourceURL, Title: "历史文章", Published: 1_700_000_000}
	bytes, err := json.Marshal(archive.Scan{Options: archive.Options{Biz: biz}, Articles: []archive.Article{article}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scans"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scans", archive.ID(biz)+".json"), bytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mp.json"), []byte(`{"MzScanImport":{"nickname":"历史账号"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	name := desktopSafeName(article.Title) + "-" + article.ID
	writeLocalLibraryTriad(t, filepath.Join(downloads, "历史账号"), name, "# 历史文章\n\n内容")
	client := &APIClient{engine: gin.New(), cfg: &APIConfig{RootDir: root, DownloadDir: downloads}, official: &officialaccount.OfficialAccountClient{}}
	client.setupDesktop()
	t.Cleanup(client.archive.Close)
	code, got := requestImportedStatus(t, client, sourceID, biz)
	if code != 0 || got.State != "readable" || got.LocalArticle == nil || got.LocalArticle.ID != sourceID || got.LocalArticle.Biz != biz {
		t.Fatalf("completed scan not linked: code=%d status=%+v", code, got)
	}
	if err := os.Remove(filepath.Join(downloads, "历史账号", "text", name+".txt")); err != nil {
		t.Fatal(err)
	}
	if code, got = requestImportedStatus(t, client, sourceID, biz); code != 0 || got.State != "none" {
		t.Fatalf("partial scan export was offered as readable: code=%d status=%+v", code, got)
	}
}

func TestImportedStatusUsesAllTasksAndPrefersReadable(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	sourceURL := "https://mp.weixin.qq.com/s/old-task?key=secret"
	sourceID := archive.ID(archive.StableURL(sourceURL))
	name := "旧任务文章"
	writeLocalLibraryTriad(t, fixture.account, name, "# 旧任务文章")
	writeLocalLibraryCorpus(t, fixture.account, map[string]string{
		"title": name, "url": sourceURL, "markdown_path": "markdown/" + name + ".md",
	})
	oldDone := completedArticleTask(t, fixture.account, name, sourceURL, map[string]string{"account_biz": fixture.biz})
	oldDone.CreatedAt = time.Now().Add(-48 * time.Hour)
	// The completed task falls beyond the default first page of task/list.
	tasks := []*downloadpkg.Task{oldDone}
	duplicate := completedArticleTask(t, fixture.account, "待下载", sourceURL, map[string]string{"account_biz": fixture.biz})
	duplicate.Status = base.DownloadStatusPause
	duplicate.CreatedAt = time.Now().Add(time.Hour)
	tasks = append(tasks, duplicate)
	for i := 0; i < 30; i++ {
		other := completedArticleTask(t, fixture.account, "missing", "https://mp.weixin.qq.com/s/other-"+strconv.Itoa(i), nil)
		other.Status = base.DownloadStatusPause
		other.CreatedAt = time.Now().Add(time.Duration(i) * time.Minute)
		tasks = append(tasks, other)
	}
	attachImportedStatusTasks(t, fixture.client, tasks...)
	code, got := requestImportedStatus(t, fixture.client, sourceID, fixture.biz)
	if code != 0 || got.State != "readable" || got.LocalArticle == nil || got.LocalArticle.ID != localLibraryID(fixture.biz, name) {
		t.Fatalf("older completed task was not linked: code=%d status=%+v", code, got)
	}
}

func TestImportedStatusRejectsTaskFromDifferentAccount(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	if err := os.MkdirAll(fixture.account, 0700); err != nil {
		t.Fatal(err)
	}
	sourceURL := "https://mp.weixin.qq.com/s/shared-link"
	sourceID := archive.ID(archive.StableURL(sourceURL))
	foreign := completedArticleTask(t, fixture.account, "待下载", sourceURL, map[string]string{"account_biz": "MzOther"})
	foreign.Status = base.DownloadStatusPause
	attachImportedStatusTasks(t, fixture.client, foreign)
	if code, got := requestImportedStatus(t, fixture.client, sourceID, fixture.biz); code != 0 || got.State != "none" {
		t.Fatalf("task with another account label was linked: code=%d status=%+v", code, got)
	}
}

func TestImportedStatusSavedAndQueuedStates(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	sourceURL := "https://mp.weixin.qq.com/s/queued-import"
	sourceID := archive.ID(archive.StableURL(sourceURL))
	// Files exist, but the unowned directory cannot supply a safe reader target.
	unowned := filepath.Join(fixture.download, "未登记目录")
	writeLocalLibraryTriad(t, unowned, "完成文章", "# 完成文章")
	done := completedArticleTask(t, unowned, "完成文章", sourceURL, nil)
	queued := completedArticleTask(t, fixture.account, "其他任务", sourceURL, nil)
	queued.Status = base.DownloadStatusPause
	attachImportedStatusTasks(t, fixture.client, done, queued)
	code, got := requestImportedStatus(t, fixture.client, sourceID, "")
	if code != 0 || got.State != "saved" || got.LocalArticle != nil {
		t.Fatalf("completed files should win over queued duplicate: code=%d status=%+v", code, got)
	}
	if err := os.Remove(filepath.Join(unowned, "text", "完成文章.txt")); err != nil {
		t.Fatal(err)
	}
	if code, got = requestImportedStatus(t, fixture.client, sourceID, ""); code != 0 || got.State != "downloading" {
		t.Fatalf("queued import was not detected: code=%d status=%+v", code, got)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/desktop/imported-status?source_id="+sourceID, nil)
	request.RemoteAddr = "192.0.2.1:12345"
	response := httptest.NewRecorder()
	fixture.client.engine.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-loopback imported-status request = HTTP %d", response.Code)
	}
}
