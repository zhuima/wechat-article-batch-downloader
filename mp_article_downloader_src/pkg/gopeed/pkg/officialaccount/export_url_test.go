package officialaccountdownload

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportArticlePersistsOnlyStablePublicURL(t *testing.T) {
	directory := t.TempDir()
	article := &WechatOfficialArticle{Title: "测试文章", Content: "<p>正文</p>"}
	raw := "https://mp.weixin.qq.com/s?__biz=MzTest&mid=123&idx=1&sn=abc&key=session-secret&pass_ticket=ticket-secret&uin=123&chksm=extra&scene=1"
	if err := (&OfficialAccountDownload{}).ExportArticle(article, raw, directory, "test-article", false); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filepath.Join(directory, "style_corpus.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatalf("no export journal record: %v", scanner.Err())
	}
	var record ArticleExportRecord
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	want := "https://mp.weixin.qq.com/s?__biz=MzTest&chksm=extra&idx=1&mid=123&scene=1&sn=abc"
	if record.URL != want || strings.Contains(string(scanner.Bytes()), "session-secret") ||
		strings.Contains(string(scanner.Bytes()), "ticket-secret") || strings.Contains(string(scanner.Bytes()), "uin=") {
		t.Fatalf("journal did not persist only stable article URL: got=%q", record.URL)
	}
}

func TestExportJournalKeepsSeparateRecordsWithoutValidSourceURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "style_corpus.jsonl")
	for _, title := range []string{"文章一", "文章二"} {
		if err := appendArticleJSONL(path, ArticleExportRecord{Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "\n"); got != 2 {
		t.Fatalf("missing source URL caused export records to collide: got %d", got)
	}
}
