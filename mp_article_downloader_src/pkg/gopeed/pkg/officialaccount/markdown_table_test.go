package officialaccountdownload

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConvertHtmlToMarkdownPreservesWeChatTable(t *testing.T) {
	article := &WechatOfficialArticle{
		Title: "GPU 加速卡选型对照表",
		Content: `<p>表 1　三条线的约束对照。</p>
			<table><thead><tr>
				<th><section><span leaf=""><br/></span></section></th>
				<th><section><span leaf="">训练线</span></section></th>
				<th><section><span leaf="">推理线</span></section></th>
				<th><section><span leaf="">端侧线</span></section></th>
			</tr></thead><tbody>
				<tr><td><section><span leaf="">最先卡住你的是什么</span></section></td>
					<td><b><span leaf="">互联与显存容量</span></b></td>
					<td><b><span leaf="">显存带宽与并发</span></b></td>
					<td><b><span leaf="">功耗墙</span></b></td></tr>
				<tr><td><section><span leaf="">一次要几张</span></section></td>
					<td><section><span leaf="">数十到数千张</span></section></td>
					<td><section><span leaf="">1–8 张</span></section></td>
					<td><section><span leaf="">1 张（板载或插卡）</span></section></td></tr>
				<tr><td><section>说明</section></td>
					<td><section>跨卡<br/>通讯</section><section>多节点</section></td>
					<td><span>吞吐 | 带宽</span></td><td><br/></td></tr>
			</tbody></table><p>表后文字。</p>`,
	}
	path := filepath.Join(t.TempDir(), "article.md")
	if err := (&OfficialAccountDownload{}).ConvertHtmlToMarkdownFile(article, path); err != nil {
		t.Fatal(err)
	}
	markdown, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	md := string(markdown)
	for _, expected := range []string{
		"|  | 训练线 | 推理线 | 端侧线 |",
		"| --- | --- | --- | --- |",
		"| 最先卡住你的是什么 | **互联与显存容量** | **显存带宽与并发** | **功耗墙** |",
		"| 一次要几张 | 数十到数千张 | 1–8 张 | 1 张（板载或插卡） |",
		"| 说明 | 跨卡 / 通讯 / 多节点 | 吞吐 \\| 带宽 |  |",
		"表后文字。",
	} {
		if !strings.Contains(md, expected) {
			t.Fatalf("GFM table missing %q\nMarkdown:\n%s", expected, md)
		}
	}
	if strings.Contains(md, "WECHATBRHOLDER") || strings.Contains(md, "<br>") {
		t.Fatalf("table contains unsupported break markup\nMarkdown:\n%s", md)
	}
	tables, err := ExtractMarkdownTablesFromHTML(article.Content)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 1 || strings.Count(md, tables[0].Markdown) != 1 || !strings.Contains(tables[0].FlattenedMarkdown, "最先卡住你的是什么") {
		t.Fatalf("stored-HTML repair data does not match the exporter: %+v", tables)
	}
}
