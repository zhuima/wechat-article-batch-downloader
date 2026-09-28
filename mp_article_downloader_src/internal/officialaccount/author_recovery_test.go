package officialaccount

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const recoveryRefreshURI = "https://mp.weixin.qq.com/s?__biz=MzTest1&mid=123&idx=1&sn=article"

func recoveryPage(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestRecoverAuthorIDFromStoredArticle(t *testing.T) {
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest1", Key: "current-key", RefreshUri: recoveryRefreshURI + "&uin=stale-user&pass_ticket=stale-ticket",
		Nickname: "保留原名称", Cookie: "existing-cookie", Uin: "existing-user",
	}, nil)
	if !c.CanRecoverAuthorIDFromStoredArticle("MzTest1") {
		t.Fatal("recoverable account was not recognized")
	}
	var requests int
	transport := importRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests++
		query := req.URL.Query()
		if req.URL.Host != "mp.weixin.qq.com" || query.Get("__biz") != "MzTest1" || query.Get("key") != "current-key" ||
			query.Get("uin") != "" || query.Get("pass_ticket") != "" || req.Header.Get("Cookie") != "" {
			t.Fatal("recovery request mixed or disclosed saved credentials")
		}
		return recoveryPage(req, `<html><body><script>var biz = "MzTest1"; window.cgiDataNew = {nick_name:"测试号", user_name:"gh_candidate", title:"文章"};</script></body></html>`), nil
	})
	changed, err := c.recoverAuthorIDFromStoredArticle(context.Background(), "MzTest1", transport)
	if err != nil || !changed || requests != 1 {
		t.Fatalf("author recovery failed: changed=%v requests=%d err=%v", changed, requests, err)
	}
	got := accounts["MzTest1"]
	if got.CandidateAuthorId != "gh_candidate" || got.AuthorId != "" || got.AuthorIdVerified ||
		got.Nickname != "保留原名称" || got.Cookie != "existing-cookie" || got.Uin != "existing-user" {
		t.Fatalf("recovery changed unrelated account state: %+v", got)
	}
	if c.CanRecoverAuthorIDFromStoredArticle("MzTest1") {
		t.Fatal("recovery remained eligible after finding an author candidate")
	}
	if data, err := os.ReadFile(mp_json_filepath); err != nil || !strings.Contains(string(data), "gh_candidate") {
		t.Fatalf("recovered candidate was not persisted: %v", err)
	}
}

func TestRecoverAuthorIDFromStoredArticleWithoutAuthor(t *testing.T) {
	c := authorTestClient(t, OfficialAccount{Biz: "MzTest1", Key: "current-key", RefreshUri: recoveryRefreshURI}, nil)
	transport := importRoundTripper(func(req *http.Request) (*http.Response, error) {
		return recoveryPage(req, `<html><body><script>var biz = "MzTest1"; window.cgiDataNew = {nick_name:"测试号", user_name:""};</script></body></html>`), nil
	})
	changed, err := c.recoverAuthorIDFromStoredArticle(context.Background(), "MzTest1", transport)
	if err != nil || changed || accounts["MzTest1"].CandidateAuthorId != "" || accounts["MzTest1"].AuthorId != "" {
		t.Fatalf("empty author field changed account: changed=%v err=%v", changed, err)
	}
}

func TestRecoverExplicitAuthorIDFromStoredArticle(t *testing.T) {
	c := authorTestClient(t, OfficialAccount{Biz: "MzTest1", Key: "current-key", RefreshUri: recoveryRefreshURI}, nil)
	transport := importRoundTripper(func(req *http.Request) (*http.Response, error) {
		return recoveryPage(req, `<html><body><script>var biz = "MzTest1"; window.cgiDataNew = {nick_name:"测试号", authorId:"gh_author", user_name:"gh_candidate"};</script></body></html>`), nil
	})
	changed, err := c.recoverAuthorIDFromStoredArticle(context.Background(), "MzTest1", transport)
	got := accounts["MzTest1"]
	if err != nil || !changed || got.AuthorId != "gh_author" || got.CandidateAuthorId != "gh_candidate" || got.AuthorIdVerified {
		t.Fatalf("explicit author ID was not recovered: changed=%v err=%v account=%+v", changed, err, got)
	}
}

func TestRecoverAuthorIDFromStoredArticleRejectsOtherAccount(t *testing.T) {
	c := authorTestClient(t, OfficialAccount{Biz: "MzTest1", Key: "current-key", RefreshUri: recoveryRefreshURI}, nil)
	transport := importRoundTripper(func(req *http.Request) (*http.Response, error) {
		return recoveryPage(req, `<html><body><script>var biz = "MzOther1"; window.cgiDataNew = {nick_name:"其他号", user_name:"gh_other"};</script></body></html>`), nil
	})
	changed, err := c.recoverAuthorIDFromStoredArticle(context.Background(), "MzTest1", transport)
	if changed || !errors.Is(err, ErrHistoryInvalidResponse) || accounts["MzTest1"].CandidateAuthorId != "" {
		t.Fatalf("other account supplied an author ID: changed=%v err=%v", changed, err)
	}
}

func TestRecoverAuthorIDFromStoredArticleRequiresCurrentSnapshot(t *testing.T) {
	c := authorTestClient(t, OfficialAccount{Biz: "MzTest1", Key: "old-key", RefreshUri: recoveryRefreshURI}, nil)
	transport := importRoundTripper(func(req *http.Request) (*http.Response, error) {
		acct_mu.Lock()
		changed := *accounts["MzTest1"]
		changed.Key = "new-key"
		accounts["MzTest1"] = &changed
		acct_mu.Unlock()
		return recoveryPage(req, `<html><body><script>var biz = "MzTest1"; window.cgiDataNew = {nick_name:"测试号", user_name:"gh_candidate"};</script></body></html>`), nil
	})
	changed, err := c.recoverAuthorIDFromStoredArticle(context.Background(), "MzTest1", transport)
	if err != nil || changed || accounts["MzTest1"].CandidateAuthorId != "" {
		t.Fatalf("stale request overwrote new session: changed=%v err=%v", changed, err)
	}
}

func TestRecoverAuthorIDFromStoredArticleRejectsUnsafeURLAndHidesHTTPError(t *testing.T) {
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest1", Key: "secret-key", RefreshUri: "https://mp.weixin.qq.com/s?__biz=MzOther1&mid=123",
	}, nil)
	if c.CanRecoverAuthorIDFromStoredArticle("MzTest1") {
		t.Fatal("cross-account saved URL was eligible for recovery")
	}
	called := false
	transport := importRoundTripper(func(req *http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New(req.URL.String() + " cookie=secret")
	})
	changed, err := c.recoverAuthorIDFromStoredArticle(context.Background(), "MzTest1", transport)
	if changed || err != nil || called {
		t.Fatalf("cross-account saved URL triggered a request: changed=%v err=%v", changed, err)
	}
	acct_mu.Lock()
	updated := *accounts["MzTest1"]
	updated.RefreshUri = recoveryRefreshURI
	accounts["MzTest1"] = &updated
	acct_mu.Unlock()
	changed, err = c.recoverAuthorIDFromStoredArticle(context.Background(), "MzTest1", transport)
	if changed || !called || !errors.Is(err, ErrHistoryNetworkFailure) ||
		strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "https://") {
		t.Fatalf("network error disclosed credentials: changed=%v err=%v", changed, err)
	}
}

func TestCandidateFirstPageMinusOneRecoversOnlyDifferentExplicitAuthorID(t *testing.T) {
	var candidateCalls, explicitCalls, recoveryCalls, showCalls int
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest1", Key: "current-key", CandidateAuthorId: "gh_candidate", RefreshUri: recoveryRefreshURI,
		Cookie: "article_session=ready", CookieExpiration: time.Now().Add(time.Hour).Unix(),
	}, func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/s" {
			recoveryCalls++
			return recoveryPage(req, `<html><body><script>var biz = "MzTest1"; window.cgiDataNew = {nick_name:"测试号", authorId:"gh_explicit", user_name:"gh_candidate", title:"当前文章"};</script></body></html>`), nil
		}
		if req.URL.Query().Get("action") == "show" {
			showCalls++
			return authorResponse("<html><title>作者</title></html>", true), nil
		}
		if req.Header.Get("Cookie") != "article_session=ready" {
			t.Fatal("recovery discarded the current article session cookie")
		}
		switch req.URL.Query().Get("author_id") {
		case "gh_candidate":
			candidateCalls++
			return authorResponse(`{"ret":-1,"base_resp":{"ret":0},"articles":[]}`, false), nil
		case "gh_explicit":
			explicitCalls++
			if req.URL.Query().Get("from_article_id") == "" {
				return authorResponse(`{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzTest1","mid":"456","title":"文章","url":"https://mp.weixin.qq.com/s?__biz=MzTest1&mid=456&idx=1"}]}`, false), nil
			}
			return authorResponse(`{"ret":0,"base_resp":{"ret":0},"articles":[]}`, false), nil
		}
		t.Fatal("unexpected author ID")
		return nil, nil
	})
	history, err := c.FetchArticleHistory("MzTest1")
	if err != nil || history == nil || len(history.Articles) != 1 || history.Pages != 2 ||
		candidateCalls != 1 || recoveryCalls != 1 || explicitCalls != 2 || showCalls != 0 {
		t.Fatalf("one-time explicit recovery failed: history=%v err=%v candidate=%d recovery=%d explicit=%d show=%d", history != nil, err, candidateCalls, recoveryCalls, explicitCalls, showCalls)
	}
	if got := accounts["MzTest1"]; got.AuthorId != "gh_explicit" || !got.AuthorIdVerified || got.Cookie != "article_session=ready" {
		t.Fatal("recovered author identity or article cookie was not retained")
	}
}

func TestCandidateRecoveryFailureKeepsOriginalErrorAndSession(t *testing.T) {
	for _, scenario := range []struct {
		name string
		page func(*http.Request) (*http.Response, error)
	}{
		{"verification", func(req *http.Request) (*http.Response, error) {
			return recoveryPage(req, "<html><title>验证</title></html>"), nil
		}},
		{"same candidate", func(req *http.Request) (*http.Response, error) {
			return recoveryPage(req, `<html><body><script>var biz = "MzTest1"; window.cgiDataNew = {nick_name:"测试号", authorId:"gh_candidate", user_name:"gh_candidate"};</script></body></html>`), nil
		}},
		{"redirect without session", func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("key") != "" {
				return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{recoveryRefreshURI}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
			}
			return recoveryPage(req, `<html><body><script>var biz = "MzTest1"; window.cgiDataNew = {nick_name:"测试号", authorId:"gh_explicit"};</script></body></html>`), nil
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var candidateCalls, recoveryCalls int
			c := authorTestClient(t, OfficialAccount{
				Biz: "MzTest1", Key: "current-key", CandidateAuthorId: "gh_candidate", RefreshUri: recoveryRefreshURI,
				Cookie: "article_session=ready", CookieExpiration: time.Now().Add(time.Hour).Unix(),
			}, func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/s" {
					recoveryCalls++
					return scenario.page(req)
				}
				candidateCalls++
				if req.URL.Query().Get("author_id") != "gh_candidate" || req.URL.Query().Get("from_article_id") != "" {
					t.Fatal("unusable recovery caused another author-list request")
				}
				return authorResponse(`{"ret":-1,"base_resp":{"ret":0},"articles":[]}`, false), nil
			})
			_, err := c.FetchArticleHistory("MzTest1")
			kind, _ := ClassifyHistoryError(err)
			if kind != "candidate_unverified" || candidateCalls != 1 || recoveryCalls < 1 ||
				accounts["MzTest1"].AuthorId != "" || accounts["MzTest1"].CandidateAuthorId != "gh_candidate" ||
				accounts["MzTest1"].Cookie != "article_session=ready" {
				t.Fatalf("failed recovery replaced the original state: kind=%q candidate=%d recovery=%d", kind, candidateCalls, recoveryCalls)
			}
			if message := authorHistoryUserMessage(err); !strings.Contains(message, "本次未返回") || strings.Contains(message, "不属于") {
				t.Fatalf("fallback panel message misrepresented candidate response: %q", message)
			}
		})
	}
}
