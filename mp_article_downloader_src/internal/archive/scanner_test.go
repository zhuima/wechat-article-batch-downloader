package archive

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func awaitStatus(t *testing.T, m *Manager, biz, status string) Scan {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s := m.Get(biz)
		if s.Status == status {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("wanted %s, got %+v", status, m.Get(biz))
	return Scan{}
}
func TestStableURL(t *testing.T) {
	a := StableURL("https://mp.weixin.qq.com/s?__biz=x&amp;mid=1&amp;idx=1&amp;key=SECRET#rd")
	b := StableURL("http://mp.weixin.qq.com/s?idx=1&mid=1&__biz=x&pass_ticket=other")
	if a != b || ID(a) != ID(b) {
		t.Fatal(a, b)
	}
	if StableURL("https://example.com/s") != "" {
		t.Fatal("accepted external URL")
	}
	legacyA := StableURL("http://mp.weixin.qq.com/mp/appmsg/show?__biz=x&appmsgid=1001&itemidx=1&sign=aaa&key=SECRET")
	legacyB := StableURL("http://mp.weixin.qq.com/mp/appmsg/show?__biz=x&appmsgid=1002&itemidx=1&sign=bbb&pass_ticket=SECRET")
	if legacyA == legacyB || ID(legacyA) == ID(legacyB) {
		t.Fatal("collapsed distinct legacy articles", legacyA, legacyB)
	}
	if legacyA != "https://mp.weixin.qq.com/mp/appmsg/show?__biz=x&appmsgid=1001&itemidx=1&sign=aaa" {
		t.Fatal("unexpected legacy URL", legacyA)
	}
}

func TestDownloadURLKeepsHistorySignatureWithoutSessionCredentials(t *testing.T) {
	raw := "https://mp.weixin.qq.com/s?scene=142&amp;__biz=x&amp;mid=1&amp;idx=1&amp;sn=a&amp;chksm=signed%2Bvalue&amp;uin=123&amp;key=SECRET&amp;pass_ticket=SECRET&amp;appmsg_token=SECRET#rd"
	got := DownloadURL(raw)
	if got != "https://mp.weixin.qq.com/s?__biz=x&chksm=signed%2Bvalue&idx=1&mid=1&scene=142&sn=a" {
		t.Fatalf("download URL did not retain the safe history signature: %q", got)
	}
	if stable := StableURL(raw); ID(stable) != ID(StableURL(got)) {
		t.Fatal("download URL changed article identity")
	}
}

func TestRepairURLsMatchesAllCachedArticlesBeforeSaving(t *testing.T) {
	dir := t.TempDir()
	oldA := "https://mp.weixin.qq.com/s?__biz=x&idx=1&mid=1&sn=a"
	oldB := "https://mp.weixin.qq.com/s?__biz=x&idx=1&mid=2&sn=b"
	m := New(dir, nil)
	previous := &Scan{Options: Options{Biz: "x", Mode: "all"}, Status: "complete", Message: "原有列表", Pages: 8, Articles: []Article{
		{ID: ID(oldA), Title: "第一篇", URL: oldA, Published: 10},
		{ID: ID(oldB), Title: "第二篇", URL: oldB, Published: 9},
	}}
	m.scans["x"] = previous
	if err := m.save(previous); err != nil {
		t.Fatal(err)
	}
	fresh := []Article{
		{ID: ID(oldB), URL: oldB + "&chksm=sig-b&scene=142&key=SECRET"},
		{ID: ID(oldA), URL: oldA + "&chksm=sig-a&scene=142&key=SECRET"},
		{URL: "https://mp.weixin.qq.com/s?__biz=x&idx=1&mid=3&sn=c&chksm=sig-c"},
	}
	if _, err := m.RepairURLs("x", fresh[:1]); err == nil {
		t.Fatal("accepted partial author list")
	}
	if got := m.Get("x"); got.Articles[0].URL != oldA || got.Articles[1].URL != oldB {
		t.Fatal("partial repair changed cached links")
	}
	repaired, err := m.RepairURLs("x", fresh)
	if err != nil || repaired != 2 {
		t.Fatalf("repair result: count=%d err=%v", repaired, err)
	}
	got := New(dir, nil).Get("x")
	if got.Status != "complete" || got.Message != "原有列表" || got.Pages != 8 || len(got.Articles) != 2 ||
		got.Articles[0].Title != "第一篇" || got.Articles[1].Title != "第二篇" ||
		got.Articles[0].ID != ID(oldA) || got.Articles[1].ID != ID(oldB) ||
		!strings.Contains(got.Articles[0].URL, "chksm=sig-a") || !strings.Contains(got.Articles[1].URL, "chksm=sig-b") ||
		strings.Contains(got.Articles[0].URL, "SECRET") {
		t.Fatalf("repair did not preserve scan or sanitize URLs: %+v", got)
	}
}
func TestPartialFailureResumesPersistedOffset(t *testing.T) {
	dir := t.TempDir()
	var fail atomic.Bool
	fail.Store(true)
	fetch := func(biz string, offset int) (Page, error) {
		if offset == 0 {
			return Page{Articles: []Article{{ID: "a", Title: "one"}}, More: true, Next: 10}, nil
		}
		if fail.Load() {
			return Page{}, errors.New("expired")
		}
		return Page{Articles: []Article{{ID: "a"}, {ID: "b", Title: "two"}}, Next: 20}, nil
	}
	m := New(dir, fetch)
	m.delay = time.Millisecond
	if e := m.Start(Options{Biz: "x", Mode: "all"}); e != nil {
		t.Fatal(e)
	}
	s := awaitStatus(t, m, "x", "paused")
	if len(s.Articles) != 1 || s.Offset != 10 {
		t.Fatalf("lost checkpoint: %+v", s)
	}
	if s.Message == "" || strings.Contains(s.Message, "expired") {
		t.Fatalf("technical error leaked into the UI: %+v", s)
	}
	fail.Store(false)
	m2 := New(dir, fetch)
	if e := m2.Start(Options{Biz: "x", Mode: "all", Resume: true}); e != nil {
		t.Fatal(e)
	}
	s = awaitStatus(t, m2, "x", "complete")
	if len(s.Articles) != 2 {
		t.Fatal(s)
	}
}

func TestAuthorListEndDoesNotClaimPublisherHistoryComplete(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, func(string, int) (Page, error) {
		return Page{Source: "author", Articles: []Article{{ID: "a", Title: "作者文章"}}, ReadPages: 2}, nil
	})
	if err := m.Start(Options{Biz: "x", Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	scan := awaitStatus(t, m, "x", "partial")
	if scan.Source != "author" || scan.ErrorCode != "publisher_history_unverified" || scan.Pages != 2 || len(scan.Articles) != 1 {
		t.Fatalf("author-only scan was marked complete or lost articles: %+v", scan)
	}
	reloaded := New(dir, nil).Get("x")
	if reloaded.Status != "partial" || reloaded.Source != "author" || len(reloaded.Articles) != 1 {
		t.Fatalf("partial source was not persisted: %+v", reloaded)
	}
	if summary := m.Summaries()["x"]; summary.Source != "author" || summary.Status != "partial" {
		t.Fatalf("summary hid incomplete coverage: %+v", summary)
	}
}

func TestCandidateUnverifiedPauseKeepsSavedArticles(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, func(string, int) (Page, error) {
		return Page{}, &HistoryFailure{Code: "candidate_unverified"}
	})
	previous := &Scan{Options: Options{Biz: "x", Mode: "all"}, Status: "paused", Pages: 2,
		Articles: []Article{{ID: "saved", Title: "已保存文章"}}}
	m.scans["x"] = previous
	if err := m.save(previous); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(Options{Biz: "x", Resume: true}); err != nil {
		t.Fatal(err)
	}
	got := awaitStatus(t, m, "x", "paused")
	if got.ErrorCode != "candidate_unverified" || got.Pages != 2 || len(got.Articles) != 1 || got.Articles[0].ID != "saved" {
		t.Fatal("candidate first-page error erased saved progress")
	}
	restored := New(dir, nil).Get("x")
	if restored.ErrorCode != got.ErrorCode || len(restored.Articles) != 1 {
		t.Fatal("candidate first-page error was not safely persisted")
	}
}

func TestAuthorPartialFailureKeepsArticlesAndRetriesCursor(t *testing.T) {
	dir := t.TempDir()
	var fail atomic.Bool
	fail.Store(true)
	fetch := func(_ string, offset int) (Page, error) {
		if offset != 0 {
			t.Fatalf("author retry used a legacy offset: %d", offset)
		}
		if fail.Load() {
			return Page{ReadPages: 2, Articles: []Article{{ID: "a", Title: "first"}}}, &HistoryFailure{Code: "network"}
		}
		return Page{ReadPages: 3, Articles: []Article{{ID: "a", Title: "first"}, {ID: "b", Title: "second"}}}, nil
	}
	m := New(dir, fetch)
	if err := m.Start(Options{Biz: "x", Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	paused := awaitStatus(t, m, "x", "paused")
	if len(paused.Articles) != 1 || paused.Pages != 2 || paused.Offset != 0 || paused.ErrorCode != "network" {
		t.Fatalf("partial author pages were not saved: %+v", paused)
	}
	fail.Store(false)
	m2 := New(dir, fetch)
	if err := m2.Start(Options{Biz: "x", Mode: "all", Resume: true}); err != nil {
		t.Fatal(err)
	}
	completed := awaitStatus(t, m2, "x", "complete")
	if len(completed.Articles) != 2 || completed.Pages != 3 || completed.Articles[0].ID != "a" || completed.Articles[1].ID != "b" {
		t.Fatalf("author retry did not deduplicate partial pages: %+v", completed)
	}
}

func TestHistoryFailureExplainsNextActionWithoutExposingCredentials(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"credentials_missing", "公开链接没有历史访问凭证"},
		{"credentials_expired", "从电脑微信复制新的文章链接并在客户端重新导入"},
		{"network", "网络请求失败"},
		{"invalid_response", "历史数据格式异常"},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			m := New(t.TempDir(), func(string, int) (Page, error) {
				return Page{}, &HistoryFailure{Code: tc.code, Detail: "网络请求失败"}
			})
			if err := m.Start(Options{Biz: "x", Mode: "all"}); err != nil {
				t.Fatal(err)
			}
			s := awaitStatus(t, m, "x", "paused")
			if s.ErrorCode != tc.code || !strings.Contains(s.Message, tc.want) || strings.Contains(s.Message, "key=SECRET") {
				t.Fatalf("unexpected scan error: %+v", s)
			}
		})
	}
}
func TestDateRangeAndRecentLimit(t *testing.T) {
	for _, mode := range []string{"date", "recent"} {
		m := New(t.TempDir(), func(string, int) (Page, error) {
			return Page{Articles: []Article{{ID: "a", Published: 30}, {ID: "b", Published: 20}, {ID: "c", Published: 10}}}, nil
		})
		if err := m.Start(Options{Biz: "x", Mode: mode, Limit: 2, After: 15, Before: 25}); err != nil {
			t.Fatal(err)
		}
		s := awaitStatus(t, m, "x", "complete")
		want := 2
		if mode == "date" {
			want = 1
		}
		if len(s.Articles) != want {
			t.Fatal(s)
		}
	}
}
func TestPauseInFlightPreservesPage(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{})
	m := New(t.TempDir(), func(string, int) (Page, error) {
		close(entered)
		<-gate
		return Page{Articles: []Article{{ID: "a"}}, Next: 10}, nil
	})
	if e := m.Start(Options{Biz: "x", Mode: "all"}); e != nil {
		t.Fatal(e)
	}
	<-entered
	m.Pause("x")
	close(gate)
	s := awaitStatus(t, m, "x", "paused")
	if len(s.Articles) != 0 || s.Offset != 0 {
		t.Fatal(s)
	}
}
func TestBadOffsetNotComplete(t *testing.T) {
	m := New(t.TempDir(), func(string, int) (Page, error) { return Page{More: true, Next: 0}, nil })
	_ = m.Start(Options{Biz: "x", Mode: "all"})
	awaitStatus(t, m, "x", "paused")
}

func TestLegacyFetchErrorLoadsAsFriendlyPausedScan(t *testing.T) {
	dir := t.TempDir()
	old := Scan{Options: Options{Biz: "x", Mode: "all"}, Status: "error", Message: "读取未完成：unexpected end of JSON input。请在微信重新打开文章后继续。", Offset: 10}
	b, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	s := New(dir, nil).Get("x")
	if s.Status != "paused" || strings.Contains(s.Message, "JSON") || s.Offset != 10 {
		t.Fatalf("legacy error was not migrated: %+v", s)
	}
}
func TestDiskFailureDoesNotLaunchScan(t *testing.T) {
	p := t.TempDir() + "/file"
	_ = os.WriteFile(p, []byte("x"), 0600)
	m := New(p, func(string, int) (Page, error) { t.Error("must not fetch"); return Page{}, nil })
	if m.Start(Options{Biz: "x", Mode: "all"}) == nil {
		t.Fatal("ignored persistence failure")
	}
}
