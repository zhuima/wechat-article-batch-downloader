package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"mp_article_batch_downloader/internal/archive"
	"mp_article_batch_downloader/internal/officialaccount"
)

type localLibraryFixture struct {
	client   *APIClient
	biz      string
	account  string
	download string
}

func newLocalLibraryFixture(t *testing.T) localLibraryFixture {
	t.Helper()
	root, download := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "mp.json"), []byte(`{"MzLocal":{"nickname":"本地公众号"},"MzOther":{"nickname":"其他公众号"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	client := &APIClient{engine: gin.New(), cfg: &APIConfig{RootDir: root, DownloadDir: download}, official: &officialaccount.OfficialAccountClient{}}
	client.setupDesktop()
	t.Cleanup(client.archive.Close)
	return localLibraryFixture{client: client, biz: "MzLocal", account: filepath.Join(download, "本地公众号"), download: download}
}

func writeLocalLibraryTriad(t *testing.T, account, base, markdown string) {
	t.Helper()
	for _, format := range []struct{ folder, ext, content string }{
		{"html", ".html", "<p>文章</p>"},
		{"markdown", ".md", markdown},
		{"text", ".txt", "文章"},
	} {
		folder := filepath.Join(account, format.folder)
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, base+format.ext), []byte(format.content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeLocalLibraryCorpus(t *testing.T, account string, records ...map[string]string) {
	t.Helper()
	var lines []byte
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line...)
		lines = append(lines, '\n')
	}
	if err := os.WriteFile(filepath.Join(account, "style_corpus.jsonl"), lines, 0600); err != nil {
		t.Fatal(err)
	}
}

func localLibraryItems(t *testing.T, fixture localLibraryFixture, biz string) []localLibraryItem {
	t.Helper()
	response := localReaderRequest(t, localReaderFixture{client: fixture.client}, "/api/desktop/local-library?biz="+url.QueryEscape(biz))
	if response.Code != http.StatusOK {
		t.Fatalf("local library HTTP %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Articles []localLibraryItem `json:"articles"`
			Total    int                `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != 0 || body.Data.Total != len(body.Data.Articles) {
		t.Fatalf("wrong library response: %s", response.Body.String())
	}
	return body.Data.Articles
}

func TestDesktopLocalLibraryListsAndReadsWithoutHistory(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	name := "LoRA 微调实战：用一张 GPU 训练你的专属大模型"
	markdown := "# " + name + "\n\n正文 **加粗**\n"
	writeLocalLibraryTriad(t, fixture.account, "0001-LoRA-微调实战", markdown)
	writeLocalLibraryTriad(t, fixture.account, name+"-cc610f02-301", markdown)
	items := localLibraryItems(t, fixture, fixture.biz)
	if len(items) != 1 || items[0].Title != name || items[0].Published <= 0 || !localDocumentID.MatchString(items[0].ID) {
		t.Fatalf("duplicate local exports not presented as one article: %+v", items)
	}
	wantedID := localLibraryID(fixture.biz, name+"-cc610f02-301")
	if items[0].ID != wantedID {
		t.Fatalf("did not prefer natural export name: got %s, want %s", items[0].ID, wantedID)
	}
	response := localReaderRequest(t, localReaderFixture{client: fixture.client}, "/api/desktop/article?biz="+fixture.biz+"&id="+items[0].ID)
	if response.Code != http.StatusOK {
		t.Fatalf("local document HTTP %d: %s", response.Code, response.Body.String())
	}
	article := localReaderEnvelope(t, response)
	if article.Data.ID != items[0].ID || article.Data.Title != name || article.Data.Markdown != markdown || !strings.Contains(article.Data.HTML, "<strong>加粗</strong>") {
		t.Fatalf("wrong local document: %+v", article.Data)
	}
	// IDs remain stable when the index is rebuilt after restart.
	if again := localLibraryItems(t, fixture, fixture.biz); len(again) != 1 || again[0].ID != items[0].ID {
		t.Fatalf("unstable local document ID: %+v", again)
	}
}

func TestDesktopLocalLibraryUsesExactExportIdentityAndTitle(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	base := "第十二章　分布式训练-729c36f1-8c4"
	writeLocalLibraryTriad(t, fixture.account, base, "# 代码 12-1　DP vs DDP 概念对比\n\n正文")
	writeLocalLibraryTriad(t, fixture.account, "另一篇", "# 另一篇\n\n正文")
	sourceURL := "https://mp.weixin.qq.com/s/xnRIezwPCspMWisPaq0Vwg?key=session-secret&pass_ticket=other-secret"
	writeLocalLibraryCorpus(t, fixture.account,
		map[string]string{"title": "第十二章　分布式训练", "url": sourceURL, "markdown_path": "markdown/" + base + ".md"},
		map[string]string{"title": "错误关联", "url": "https://mp.weixin.qq.com/s/other", "markdown_path": "markdown/missing.md"},
	)
	items := localLibraryItems(t, fixture, fixture.biz)
	if len(items) != 2 {
		t.Fatalf("expected both complete files, got %+v", items)
	}
	// Importing the same permalink without the old session parameters must
	// resolve to the identity recorded by the original download.
	wantSourceID := archive.ID(archive.StableURL("https://mp.weixin.qq.com/s/xnRIezwPCspMWisPaq0Vwg"))
	var matched localLibraryItem
	for _, item := range items {
		if item.Title == "第十二章　分布式训练" {
			matched = item
		} else if item.Title != "另一篇" || item.SourceID != "" {
			t.Fatalf("unrelated file gained export metadata: %+v", item)
		}
	}
	if matched.ID == "" || matched.SourceID != wantSourceID {
		t.Fatalf("export identity was not joined to its local file: %+v", matched)
	}
	response := localReaderRequest(t, localReaderFixture{client: fixture.client}, "/api/desktop/article?biz="+fixture.biz+"&id="+matched.ID)
	if response.Code != http.StatusOK || localReaderEnvelope(t, response).Data.Title != matched.Title {
		t.Fatalf("reader did not use original export title: %s", response.Body.String())
	}
	listing := localReaderRequest(t, localReaderFixture{client: fixture.client}, "/api/desktop/local-library?biz="+fixture.biz)
	if strings.Contains(listing.Body.String(), "session-secret") || strings.Contains(listing.Body.String(), "pass_ticket") || strings.Contains(listing.Body.String(), sourceURL) {
		t.Fatal("local library exposed the source URL or session credentials")
	}
}

func TestDesktopLocalLibraryDecodesEscapedExportTitle(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	base := "文章标题-04c2cdce-5f7"
	writeLocalLibraryTriad(t, fixture.account, base, "正文")
	writeLocalLibraryCorpus(t, fixture.account, map[string]string{
		"title":         "从 &quot;错误&quot; 到正确",
		"url":           "https://mp.weixin.qq.com/s/escaped-title",
		"markdown_path": "markdown/" + base + ".md",
	})
	items := localLibraryItems(t, fixture, fixture.biz)
	if len(items) != 1 || items[0].Title != `从 "错误" 到正确` {
		t.Fatalf("library title was not decoded: %+v", items)
	}
	response := localReaderRequest(t, localReaderFixture{client: fixture.client}, "/api/desktop/article?biz="+fixture.biz+"&id="+items[0].ID)
	if title := localReaderEnvelope(t, response).Data.Title; title != items[0].Title {
		t.Fatalf("reader title %q differs from library title %q", title, items[0].Title)
	}
	if title := localLibraryTitle([]byte("# &quot;Markdown 标题&quot;"), "fallback"); title != `"Markdown 标题"` {
		t.Fatalf("Markdown heading title was not decoded: %q", title)
	}
}

func TestDesktopLocalLibraryWithholdsAmbiguousExportIdentity(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	writeLocalLibraryTriad(t, fixture.account, "复用文件名", "# 文件内标题")
	writeLocalLibraryCorpus(t, fixture.account,
		map[string]string{"title": "文章甲", "url": "https://mp.weixin.qq.com/s/first", "markdown_path": "markdown/复用文件名.md"},
		map[string]string{"title": "文章乙", "url": "https://mp.weixin.qq.com/s/second", "markdown_path": "markdown/复用文件名.md"},
	)
	items := localLibraryItems(t, fixture, fixture.biz)
	if len(items) != 1 || items[0].Title != "文件内标题" || items[0].SourceID != "" {
		t.Fatalf("ambiguous journal records claimed a local article: %+v", items)
	}
}

func TestDesktopLocalLibraryKeepsDifferentSourcesWithSameMarkdown(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	writeLocalLibraryTriad(t, fixture.account, "文章甲", "# 相同正文")
	writeLocalLibraryTriad(t, fixture.account, "文章乙", "# 相同正文")
	writeLocalLibraryCorpus(t, fixture.account,
		map[string]string{"title": "文章甲", "url": "https://mp.weixin.qq.com/s/first", "markdown_path": "markdown/文章甲.md"},
		map[string]string{"title": "文章乙", "url": "https://mp.weixin.qq.com/s/second", "markdown_path": "markdown/文章乙.md"},
	)
	items := localLibraryItems(t, fixture, fixture.biz)
	if len(items) != 2 || items[0].SourceID == "" || items[1].SourceID == "" || items[0].SourceID == items[1].SourceID {
		t.Fatalf("different articles with identical Markdown were collapsed: %+v", items)
	}
}

func TestDesktopLocalLibraryRequiresOwnCompleteFiles(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	writeLocalLibraryTriad(t, fixture.account, "完整", "# 完整")
	writeLocalLibraryTriad(t, filepath.Join(fixture.download, "其他公众号"), "仅其他账号", "# 仅其他账号")
	writeLocalLibraryTriad(t, fixture.account, "缺文本", "# 缺文本")
	if err := os.Remove(filepath.Join(fixture.account, "text", "缺文本.txt")); err != nil {
		t.Fatal(err)
	}
	items := localLibraryItems(t, fixture, fixture.biz)
	if len(items) != 1 || items[0].Title != "完整" {
		t.Fatalf("included another account or incomplete export: %+v", items)
	}
	for _, path := range []string{
		"/api/desktop/article?biz=MzOther&id=" + items[0].ID,
		"/api/desktop/article?biz=MzLocal&id=..%2F..%2Fsecret",
		"/api/desktop/article?biz=MzLocal&id=" + localLibraryID("MzLocal", "缺文本"),
	} {
		response := localReaderRequest(t, localReaderFixture{client: fixture.client}, path)
		var body struct {
			Code int `json:"code"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Code == 0 {
			t.Fatalf("unindexed document accepted: %s => %s", path, response.Body.String())
		}
	}
}

func TestDesktopLocalLibraryOmitsScanLinkedDocuments(t *testing.T) {
	fixture := newLocalReaderFixture(t, "# 本地阅读")
	response := localReaderRequest(t, fixture, "/api/desktop/local-library?biz="+fixture.biz)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"articles":[]`) {
		t.Fatalf("scan-linked document duplicated in local library: %s", response.Body.String())
	}
}

func TestDesktopLocalLibraryRejectsSymlinks(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	outside := t.TempDir()
	writeLocalLibraryTriad(t, outside, "外部", "# 外部")
	if err := os.Symlink(outside, fixture.account); err != nil {
		t.Skipf("this Windows installation does not permit symlink tests: %v", err)
	}
	if items := localLibraryItems(t, fixture, fixture.biz); len(items) != 0 {
		t.Fatalf("symlinked account folder indexed: %+v", items)
	}
	if err := os.Remove(fixture.account); err != nil {
		t.Fatal(err)
	}
	writeLocalLibraryTriad(t, fixture.account, "本地", "# 本地")
	if err := os.Remove(filepath.Join(fixture.account, "markdown", "本地.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "markdown", "外部.md"), filepath.Join(fixture.account, "markdown", "本地.md")); err != nil {
		t.Fatal(err)
	}
	if items := localLibraryItems(t, fixture, fixture.biz); len(items) != 0 {
		t.Fatalf("symlinked Markdown indexed: %+v", items)
	}
}

func TestDesktopLocalLibraryReferencedImagesOnly(t *testing.T) {
	fixture := newLocalLibraryFixture(t)
	name := strings.Repeat("e", 32) + ".png"
	writeLocalLibraryTriad(t, fixture.account, "带图", "# 带图\n\n![图](images/"+name+")")
	imageDir := filepath.Join(fixture.account, "markdown", "images")
	if err := os.MkdirAll(imageDir, 0700); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\nimage")
	if err := os.WriteFile(filepath.Join(imageDir, name), png, 0600); err != nil {
		t.Fatal(err)
	}
	items := localLibraryItems(t, fixture, fixture.biz)
	if len(items) != 1 {
		t.Fatalf("expected image article, got %+v", items)
	}
	imageURL := "/api/desktop/article/image?biz=" + fixture.biz + "&id=" + items[0].ID + "&name=" + name
	image := localReaderRequest(t, localReaderFixture{client: fixture.client}, imageURL)
	if image.Code != http.StatusOK || image.Header().Get("Content-Type") != "image/png" || image.Body.String() != string(png) {
		t.Fatalf("referenced local image HTTP %d: %s", image.Code, image.Body.String())
	}
	other := localReaderRequest(t, localReaderFixture{client: fixture.client}, strings.Replace(imageURL, name, strings.Repeat("f", 32)+".png", 1))
	if other.Code != http.StatusNotFound {
		t.Fatalf("unreferenced local image HTTP %d", other.Code)
	}
}
