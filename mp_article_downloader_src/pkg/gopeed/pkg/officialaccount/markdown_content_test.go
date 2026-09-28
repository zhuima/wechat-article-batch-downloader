package officialaccountdownload

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestConvertArticleHTMLToMarkdownPreservesWeChatArticleStructure(t *testing.T) {
	content := `<h1>二、五类真实故障：第一印象是&#34;完全不可用&#34;</h1>
		<section data-layout-id="14"><span>先看一批真实输出：</span></section>
		<section data-layout-id="15"><span><span style="font-weight: bold;">第 1 号故障：占位符。</span>模型在数字位置输出 [X.XXX]。</span></section>
		<section data-layout-id="16"><span><span style="font-weight: 700;">第 2 号故障：编造逼真数字。</span>我们加了规则。</span></section>
		<ul><li><section><span>第一条</span></section></li><li><section><span>第二条</span></section></li></ul>
		<section class="code-snippet__fix code-snippet__js"><ul class="code-snippet__line-index"><li></li><li></li><li></li></ul><pre data-lang="python"><code><span class="code-snippet__keyword">from</span>&nbsp;llama_cpp&nbsp;<span>import</span>&nbsp;LlamaGrammar</code><code>SCHEMA = {</code><code>&nbsp;&nbsp;&nbsp;&nbsp;"type": "object",</code></pre></section>
		<section data-layout-id="17"><span>代码之后的段落。</span></section>
		<img src="data:image/png;base64,AAAA"/>`
	markdown, err := ConvertArticleHTMLToMarkdown(content)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`# 二、五类真实故障：第一印象是"完全不可用"`,
		"先看一批真实输出：\n\n**第 1 号故障：占位符。** 模型在数字位置输出",
		"**第 2 号故障：编造逼真数字。** 我们加了规则。",
		"- 第一条",
		"- 第二条",
		"from llama_cpp import LlamaGrammar\nSCHEMA = {\n    \"type\": \"object\",",
		"代码之后的段落。",
	} {
		if !strings.Contains(markdown, expected) {
			t.Errorf("missing %q\nMarkdown:\n%s", expected, markdown)
		}
	}
	if strings.Contains(markdown, "base64,") || strings.Contains(markdown, "WECHATBRHOLDER") || strings.Contains(markdown, "code-snippet__line-index") {
		t.Fatalf("conversion leaked image data or editor markup:\n%s", markdown)
	}
}

func TestFetchArticleDecodesTitleEntities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`<script>window.cgiDataNew = {"title":"Qwen 从&quot;编造数字&quot;到零差错","content_noencode":"<p>正文</p>"};</script>`))
	}))
	defer server.Close()
	article, err := (&OfficialAccountDownload{}).FetchArticle(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if article.Title != `Qwen 从"编造数字"到零差错` {
		t.Fatalf("unexpected title after one entity decode: %q", article.Title)
	}
}

func TestConvertHtmlToMarkdownFilePreservesWeChatArticleStructure(t *testing.T) {
	article := &WechatOfficialArticle{Content: `<section data-layout-id="1">第一段。</section><section data-layout-id="2"><span style="font-weight: bold;">第二段。</span></section>`}
	path := t.TempDir() + "/article.md"
	if err := (&OfficialAccountDownload{}).ConvertHtmlToMarkdownFile(article, path); err != nil {
		t.Fatal(err)
	}
	markdown, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), "第一段。\n\n**第二段。**") {
		t.Fatalf("downloaded Markdown lost paragraph or bold structure: %s", markdown)
	}
}
