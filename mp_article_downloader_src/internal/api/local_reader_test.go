package api

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	officialaccountdownload "github.com/GopeedLab/gopeed/pkg/officialaccount"
	"github.com/gin-gonic/gin"
	"mp_article_batch_downloader/internal/archive"
	"mp_article_batch_downloader/internal/officialaccount"
)

type localReaderFixture struct {
	client    *APIClient
	article   archive.Article
	biz       string
	account   string
	markdown  string
	imageName string
}

func newLocalReaderFixture(t *testing.T, content string) localReaderFixture {
	t.Helper()
	root, downloads := t.TempDir(), t.TempDir()
	biz := "MzReader"
	article := archive.Article{ID: archive.ID("reader-article"), Title: "本地阅读", Published: 1_700_000_000}
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
	if err := os.WriteFile(filepath.Join(root, "mp.json"), []byte(`{"MzReader":{"nickname":"阅读测试"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	account := filepath.Join(downloads, "阅读测试")
	base := desktopSafeName(article.Title) + "-" + article.ID
	for _, format := range []struct{ dir, ext, content string }{
		{"html", ".html", "<p>文章</p>"},
		{"markdown", ".md", content},
		{"text", ".txt", "文章"},
	} {
		folder := filepath.Join(account, format.dir)
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, base+format.ext), []byte(format.content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	client := &APIClient{engine: gin.New(), cfg: &APIConfig{RootDir: root, DownloadDir: downloads}, official: &officialaccount.OfficialAccountClient{}}
	client.setupDesktop()
	t.Cleanup(client.archive.Close)
	return localReaderFixture{client: client, article: article, biz: biz, account: account, markdown: filepath.Join(account, "markdown", base+".md"), imageName: strings.Repeat("a", 32) + ".png"}
}

func localReaderRequest(t *testing.T, fixture localReaderFixture, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()
	fixture.client.engine.ServeHTTP(response, request)
	return response
}

func localReaderEnvelope(t *testing.T, response *httptest.ResponseRecorder) struct {
	Code int `json:"code"`
	Data struct {
		ID            string `json:"id"`
		Title         string `json:"title"`
		Published     int64  `json:"published"`
		Markdown      string `json:"markdown"`
		HTML          string `json:"html"`
		ImagesBaseURL string `json:"images_base_url"`
	} `json:"data"`
} {
	t.Helper()
	var body struct {
		Code int `json:"code"`
		Data struct {
			ID            string `json:"id"`
			Title         string `json:"title"`
			Published     int64  `json:"published"`
			Markdown      string `json:"markdown"`
			HTML          string `json:"html"`
			ImagesBaseURL string `json:"images_base_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestDesktopLocalArticleRendersSafeMarkdownAndReferencedImage(t *testing.T) {
	imageName := strings.Repeat("a", 32) + ".png"
	markdown := "# 阅读测试\n\n正文 **加粗**\n\n![图片](images/" + imageName + ")\n\n[官网](https://example.com)"
	fixture := newLocalReaderFixture(t, markdown)
	imageDir := filepath.Join(fixture.account, "markdown", "images")
	if err := os.MkdirAll(imageDir, 0700); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\nnonempty")
	if err := os.WriteFile(filepath.Join(imageDir, imageName), png, 0600); err != nil {
		t.Fatal(err)
	}
	articleURL := "/api/desktop/article?biz=" + url.QueryEscape(fixture.biz) + "&id=" + fixture.article.ID
	response := localReaderRequest(t, fixture, articleURL)
	if response.Code != http.StatusOK {
		t.Fatalf("article HTTP %d: %s", response.Code, response.Body.String())
	}
	body := localReaderEnvelope(t, response)
	if body.Code != 0 || body.Data.ID != fixture.article.ID || body.Data.Title != fixture.article.Title || body.Data.Published != fixture.article.Published || body.Data.Markdown != markdown {
		t.Fatalf("wrong local article: %+v", body)
	}
	if !strings.Contains(body.Data.HTML, "<strong>加粗</strong>") || !strings.Contains(body.Data.HTML, html.EscapeString(body.Data.ImagesBaseURL+imageName)) || !strings.HasPrefix(body.Data.ImagesBaseURL, "/api/desktop/article/image?") {
		t.Fatalf("local article HTML or image route missing: %+v", body.Data)
	}
	image := localReaderRequest(t, fixture, body.Data.ImagesBaseURL+imageName)
	if image.Code != http.StatusOK || image.Header().Get("Content-Type") != "image/png" || image.Body.String() != string(png) {
		t.Fatalf("local image HTTP %d (%s): %q", image.Code, image.Header().Get("Content-Type"), image.Body.String())
	}
	if image.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("image response allows content sniffing")
	}
}

func TestDesktopLocalArticleRejectsUnsafeLinksAndImages(t *testing.T) {
	imageName := strings.Repeat("b", 32) + ".png"
	markdown := "正文 <script>alert(1)</script>\n\n[危险](javascript:alert%281%29) [文件](file:///C:/secret) [安全](https://example.com)\n\n![外部](https://example.com/image.png) ![内嵌](data:image/svg+xml;base64,abc) ![本地](images/" + imageName + ")"
	fixture := newLocalReaderFixture(t, markdown)
	response := localReaderRequest(t, fixture, "/api/desktop/article?biz="+fixture.biz+"&id="+fixture.article.ID)
	body := localReaderEnvelope(t, response)
	if body.Code != 0 {
		t.Fatalf("reading article failed: %s", response.Body.String())
	}
	for _, forbidden := range []string{"<script", "javascript:", "file:", "data:image", "src=\"https://example.com/image.png"} {
		if strings.Contains(body.Data.HTML, forbidden) {
			t.Fatalf("unsafe content %q survived: %s", forbidden, body.Data.HTML)
		}
	}
	if !strings.Contains(body.Data.HTML, "危险") || !strings.Contains(body.Data.HTML, "安全") || !strings.Contains(body.Data.HTML, html.EscapeString(body.Data.ImagesBaseURL+imageName)) {
		t.Fatalf("safe text/image disappeared: %s", body.Data.HTML)
	}
	for _, name := range []string{strings.Repeat("c", 32) + ".png", "../secret.png", "secret.html"} {
		image := localReaderRequest(t, fixture, body.Data.ImagesBaseURL+url.QueryEscape(name))
		if image.Code != http.StatusNotFound {
			t.Fatalf("unsafe or unreferenced image %q served: HTTP %d", name, image.Code)
		}
	}
}

func TestDesktopLocalArticleRendersPipeTableAndChineseHeadingLinks(t *testing.T) {
	markdown := strings.Join([]string{
		"# 三条线的约束根本不是同一件事",
		"",
		"[跳到训练线](#训练线) · [跳到补充说明](#训练线-1)",
		"",
		"## 训练线",
		"",
		"| 型号 | 显存 | 互联 |",
		"| :--- | ---: | :---: |",
		"| 昇腾 910B | 32GB HBM2 | 灵衢 / HCCS |",
		"| Atlas 900 | 整机级 | 384 卡 |",
		"",
		"## 训练线",
		"",
		"补充说明。",
	}, "\n")
	fixture := newLocalReaderFixture(t, markdown)
	response := localReaderRequest(t, fixture, "/api/desktop/article?biz="+fixture.biz+"&id="+fixture.article.ID)
	if response.Code != http.StatusOK {
		t.Fatalf("article HTTP %d: %s", response.Code, response.Body.String())
	}
	body := localReaderEnvelope(t, response)
	if body.Code != 0 {
		t.Fatalf("reading article failed: %s", response.Body.String())
	}
	rendered := body.Data.HTML
	for _, expected := range []string{
		`<table>`, `</table>`, `<thead>`, `<tbody>`,
		`<a href="#` + url.PathEscape("训练线") + `">跳到训练线</a>`,
		`<a href="#` + url.PathEscape("训练线-1") + `">跳到补充说明</a>`,
		`<h2 id="训练线">训练线</h2>`, `<h2 id="训练线-1">训练线</h2>`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("missing %q in rendered article: %s", expected, rendered)
		}
	}
	for _, cell := range []string{"型号", "显存", "互联", "昇腾 910B", "32GB HBM2", "灵衢 / HCCS", "Atlas 900", "整机级", "384 卡"} {
		if !strings.Contains(rendered, ">"+cell+"</") {
			t.Errorf("table cell %q missing: %s", cell, rendered)
		}
	}
	if !regexp.MustCompile(`(?s)<tr>\s*<th[^>]*>型号</th>\s*<th[^>]*>显存</th>\s*<th[^>]*>互联</th>\s*</tr>`).MatchString(rendered) {
		t.Errorf("table headers were not kept in their columns: %s", rendered)
	}
	if strings.Contains(rendered, `target="_blank"`) {
		t.Errorf("in-document links should stay in the reader: %s", rendered)
	}
}

func TestDesktopLocalArticleHeadingLinksDoNotRestoreRawHTML(t *testing.T) {
	markdown := "## 安全标题\n\n[跳转](#安全标题)\n\n| 内容 |\n| --- |\n| <script>alert(1)</script>文字 |\n\n<div onclick=\"alert(1)\">不可信 HTML</div>"
	fixture := newLocalReaderFixture(t, markdown)
	response := localReaderRequest(t, fixture, "/api/desktop/article?biz="+fixture.biz+"&id="+fixture.article.ID)
	body := localReaderEnvelope(t, response)
	if response.Code != http.StatusOK || body.Code != 0 {
		t.Fatalf("reading article failed: HTTP %d: %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{`id="安全标题"`, `href="#` + url.PathEscape("安全标题") + `"`, `文字</td>`} {
		if !strings.Contains(body.Data.HTML, expected) {
			t.Errorf("missing safe content %q: %s", expected, body.Data.HTML)
		}
	}
	for _, unsafe := range []string{"<script", "onclick=", "<div"} {
		if strings.Contains(body.Data.HTML, unsafe) {
			t.Errorf("raw HTML %q survived: %s", unsafe, body.Data.HTML)
		}
	}
}

func testLocalReaderLegacyTable(t *testing.T, source string) officialaccountdownload.MarkdownTable {
	t.Helper()
	tables, err := officialaccountdownload.ExtractMarkdownTablesFromHTML(source)
	if err != nil || len(tables) != 1 || tables[0].FlattenedMarkdown == "" || tables[0].Markdown == "" || tables[0].Markdown == tables[0].FlattenedMarkdown {
		t.Fatalf("invalid legacy table fixture: tables=%+v, err=%v", tables, err)
	}
	return tables[0]
}

func writeLocalReaderHTML(t *testing.T, fixture localReaderFixture, content string) {
	t.Helper()
	base := strings.TrimSuffix(filepath.Base(fixture.markdown), ".md")
	path := filepath.Join(fixture.account, "html", base+".html")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopLocalArticleRestoresLegacyTableFromVerifiedHTML(t *testing.T) {
	htmlSource := `<html><body><div class="rich_media_content"><p>导语保留。</p><table><thead><tr><th>型号</th><th>显存</th><th>互联</th></tr></thead><tbody><tr><td>昇腾 910B</td><td>32GB HBM2</td><td>灵衢 / HCCS</td></tr><tr><td>Atlas 900</td><td>整机级</td><td>384 卡</td></tr></tbody></table><p>表后文字保留。</p></div></body></html>`
	table := testLocalReaderLegacyTable(t, htmlSource)
	// Old exports can run a flattened table directly after a caption's
	// Markdown hard break; there is no blank line before the table span.
	oldMarkdown := "# 显卡对照\n\n导语保留。  \n" + table.FlattenedMarkdown + "\n\n表后文字保留。\n"
	fixture := newLocalReaderFixture(t, oldMarkdown)
	writeLocalReaderHTML(t, fixture, htmlSource)
	response := localReaderRequest(t, fixture, "/api/desktop/article?biz="+fixture.biz+"&id="+fixture.article.ID)
	body := localReaderEnvelope(t, response)
	if response.Code != http.StatusOK || body.Code != 0 {
		t.Fatalf("reading article failed: HTTP %d: %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"导语保留。", "表后文字保留。", table.Markdown} {
		if !strings.Contains(body.Data.Markdown, expected) {
			t.Errorf("repaired Markdown missing %q: %s", expected, body.Data.Markdown)
		}
	}
	if !strings.Contains(body.Data.HTML, "<table>") || !strings.Contains(body.Data.HTML, ">昇腾 910B</td>") || !strings.Contains(body.Data.HTML, "表后文字保留。") {
		t.Errorf("repaired HTML lost the table or surrounding text: %s", body.Data.HTML)
	}
	onDisk, err := os.ReadFile(fixture.markdown)
	if err != nil || string(onDisk) != oldMarkdown {
		t.Fatalf("reading rewrote the saved Markdown: %v", err)
	}
}

func TestDesktopLocalArticleRestoresLegacyParagraphAndCodeStructure(t *testing.T) {
	oldMarkdown := strings.Join([]string{
		"# 五类故障", "", "先看：第 1 号故障：占位符。内容。第 2 号故障：编造数字。后文。", "", "```", "from llama_cpp import LlamaGrammarimport json", "```", "", "结论。",
	}, "\n")
	fixture := newLocalReaderFixture(t, oldMarkdown)
	htmlSource := `<html><body><div class="rich_media_content"><h1>五类故障</h1><section><span>先看：</span></section><section><span><span style="font-weight: bold;">第 1 号故障：占位符。</span>内容。</span></section><section><span><span style="font-weight: bold;">第 2 号故障：编造数字。</span>后文。</span></section><section class="code-snippet__fix"><pre data-lang="python"><code><span>from llama_cpp import LlamaGrammar</span></code><code><span>import json</span></code></pre></section><section><span>结论。</span></section></div></body></html>`
	writeLocalReaderHTML(t, fixture, htmlSource)
	response := localReaderRequest(t, fixture, "/api/desktop/article?biz="+fixture.biz+"&id="+fixture.article.ID)
	body := localReaderEnvelope(t, response)
	if response.Code != http.StatusOK || body.Code != 0 {
		t.Fatalf("reader HTTP %d: %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{"**第 1 号故障：占位符。**", "**第 2 号故障：编造数字。**", "LlamaGrammar\nimport json", "<strong>第 1 号故障：占位符。</strong>", "<p>先看：</p>"} {
		if !strings.Contains(body.Data.Markdown+body.Data.HTML, expected) {
			t.Errorf("restored reader missing %q: %s", expected, response.Body.String())
		}
	}
	if strings.Contains(body.Data.Markdown, "LlamaGrammarimport json") {
		t.Errorf("code lines remained flattened: %s", body.Data.Markdown)
	}
	if saved, err := os.ReadFile(fixture.markdown); err != nil || string(saved) != oldMarkdown {
		t.Fatalf("reader rewrote original Markdown: %v", err)
	}
}

func TestRestoreLocalMarkdownStructureKeepsOriginalWhenHTMLDiffersOrHasImages(t *testing.T) {
	markdown := []byte("# 已保存正文\n\n不要改变。")
	for name, htmlSource := range map[string]string{
		"different text": `<div class="rich_media_content"><h1>已保存正文</h1><section>另一篇文章。</section></div>`,
		"remote image":   `<div class="rich_media_content"><h1>已保存正文</h1><section>不要改变。</section><img src="https://example.com/photo.png"></div>`,
		"no article":     `<div><h1>已保存正文</h1><section>不要改变。</section></div>`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := restoreLocalMarkdownStructure(markdown, htmlSource); string(got) != string(markdown) {
				t.Fatalf("unverifiable HTML replaced original Markdown: %s", got)
			}
		})
	}
}

func TestDesktopLocalArticleSkipsAmbiguousOrUnmatchedLegacyTables(t *testing.T) {
	htmlSource := `<table><thead><tr><th>模型</th><th>容量</th></tr></thead><tbody><tr><td>甲型号</td><td>32GB</td></tr><tr><td>乙型号</td><td>64GB</td></tr></tbody></table>`
	table := testLocalReaderLegacyTable(t, htmlSource)
	for name, markdown := range map[string]string{
		"ambiguous": "# 测试\n\n" + table.FlattenedMarkdown + "\n\n重复：" + table.FlattenedMarkdown,
		"unmatched": "# 测试\n\n文章中没有这张表。",
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newLocalReaderFixture(t, markdown)
			writeLocalReaderHTML(t, fixture, htmlSource)
			response := localReaderRequest(t, fixture, "/api/desktop/article?biz="+fixture.biz+"&id="+fixture.article.ID)
			body := localReaderEnvelope(t, response)
			if response.Code != http.StatusOK || body.Code != 0 || body.Data.Markdown != markdown {
				t.Fatalf("ambiguous or unmatched table changed the saved article: %s", response.Body.String())
			}
			if strings.Contains(body.Data.HTML, "<table>") {
				t.Fatalf("unmatched table was added to reader: %s", body.Data.HTML)
			}
		})
	}
}

func TestRestoreLocalMarkdownTablesDoesNotDuplicateCurrentTable(t *testing.T) {
	htmlSource := `<table><thead><tr><th>模型</th><th>容量</th></tr></thead><tbody><tr><td>甲型号</td><td>32GB</td></tr></tbody></table>`
	table := testLocalReaderLegacyTable(t, htmlSource)
	markdown := []byte("# 测试\n\n" + table.Markdown + "\n\n引用原始文本：" + table.FlattenedMarkdown)
	if got := restoreLocalMarkdownTables(markdown, htmlSource); string(got) != string(markdown) {
		t.Fatalf("already formatted table was changed: %s", got)
	}
}

func TestDesktopLocalArticleRestoredTableStillSanitizesLinks(t *testing.T) {
	htmlSource := `<table><thead><tr><th>模型</th><th>链接</th></tr></thead><tbody><tr><td>甲型号</td><td><a href="javascript:alert(1)">点我</a><script>alert(1)</script></td></tr><tr><td>乙型号</td><td><a href="https://example.com">安全链接</a></td></tr></tbody></table>`
	table := testLocalReaderLegacyTable(t, htmlSource)
	fixture := newLocalReaderFixture(t, "# 测试\n\n"+table.FlattenedMarkdown)
	writeLocalReaderHTML(t, fixture, htmlSource)
	response := localReaderRequest(t, fixture, "/api/desktop/article?biz="+fixture.biz+"&id="+fixture.article.ID)
	body := localReaderEnvelope(t, response)
	if response.Code != http.StatusOK || body.Code != 0 || !strings.Contains(body.Data.HTML, "<table>") {
		t.Fatalf("safe table was not restored: %s", response.Body.String())
	}
	if strings.Contains(body.Data.HTML, "javascript:") || strings.Contains(body.Data.HTML, "<script") {
		t.Fatalf("unsafe restored table content reached HTML: %s", body.Data.HTML)
	}
}

func TestDesktopLocalArticleRequiresScanAndCompleteExport(t *testing.T) {
	fixture := newLocalReaderFixture(t, "# 本地文章")
	for _, path := range []string{
		"/api/desktop/article?biz=" + fixture.biz + "&id=other",
		"/api/desktop/article?biz=other&id=" + fixture.article.ID,
	} {
		response := localReaderRequest(t, fixture, path)
		if body := localReaderEnvelope(t, response); body.Code != 404 {
			t.Fatalf("article outside scan available: %s", response.Body.String())
		}
	}
	base := desktopSafeName(fixture.article.Title) + "-" + fixture.article.ID
	if err := os.Remove(filepath.Join(fixture.account, "text", base+".txt")); err != nil {
		t.Fatal(err)
	}
	response := localReaderRequest(t, fixture, "/api/desktop/article?biz="+fixture.biz+"&id="+fixture.article.ID)
	if body := localReaderEnvelope(t, response); body.Code != 404 {
		t.Fatalf("incomplete export available: %s", response.Body.String())
	}
}

func TestDesktopLocalArticleRejectsOutsideSymlinksAndLargeMarkdown(t *testing.T) {
	fixture := newLocalReaderFixture(t, "# 本地文章")
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("# 不应读取"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.markdown); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, fixture.markdown); err == nil {
		response := localReaderRequest(t, fixture, "/api/desktop/article?biz="+fixture.biz+"&id="+fixture.article.ID)
		if body := localReaderEnvelope(t, response); body.Code != 404 {
			t.Fatalf("outside symlink was read: %s", response.Body.String())
		}
		if err := os.Remove(fixture.markdown); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(fixture.markdown, []byte(strings.Repeat("x", int(maxLocalMarkdownBytes)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	response := localReaderRequest(t, fixture, "/api/desktop/article?biz="+fixture.biz+"&id="+fixture.article.ID)
	if body := localReaderEnvelope(t, response); body.Code != 413 {
		t.Fatalf("oversized Markdown available: %s", response.Body.String())
	}
}

func TestDesktopLocalArticleImageRejectsOutsideSymlink(t *testing.T) {
	name := strings.Repeat("d", 32) + ".png"
	fixture := newLocalReaderFixture(t, "![图片](images/"+name+")")
	imageDir := filepath.Join(fixture.account, "markdown", "images")
	if err := os.MkdirAll(imageDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("\x89PNG\r\n\x1a\nprivate"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(imageDir, name)); err != nil {
		t.Skipf("this Windows installation does not permit symlink tests: %v", err)
	}
	imageURL := "/api/desktop/article/image?biz=" + fixture.biz + "&id=" + fixture.article.ID + "&name=" + name
	response := localReaderRequest(t, fixture, imageURL)
	if response.Code != http.StatusNotFound {
		t.Fatalf("image symlink outside the download directory served: HTTP %d", response.Code)
	}
}

func TestDesktopLocalArticleRequiresLoopback(t *testing.T) {
	fixture := newLocalReaderFixture(t, "# 本地文章")
	request := httptest.NewRequest(http.MethodGet, "/api/desktop/article?biz="+fixture.biz+"&id="+fixture.article.ID, nil)
	request.RemoteAddr = "192.0.2.100:12345"
	response := httptest.NewRecorder()
	fixture.client.engine.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote reader request HTTP %d", response.Code)
	}
}
