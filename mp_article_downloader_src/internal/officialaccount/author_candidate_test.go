package officialaccount

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	result "mp_article_batch_downloader/internal/util"
)

func authorResponse(body string, cookie bool) *http.Response {
	header := http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}
	if cookie {
		header.Add("Set-Cookie", "author_session=ready; Path=/; HttpOnly")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func authorTestClient(t *testing.T, acct OfficialAccount, transport importRoundTripper) *OfficialAccountClient {
	t.Helper()
	oldAccounts, oldPath := accounts, mp_json_filepath
	accounts = map[string]*OfficialAccount{acct.Biz: &acct}
	mp_json_filepath = filepath.Join(t.TempDir(), "mp.json")
	t.Cleanup(func() { accounts, mp_json_filepath = oldAccounts, oldPath })
	return &OfficialAccountClient{authorHTTPClient: &http.Client{Transport: transport, Timeout: time.Second}}
}

func TestCandidateAuthorIDRequiresMatchingArticleBiz(t *testing.T) {
	var showCalls, listCalls int
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest", CandidateAuthorId: "gh_candidate", Uin: "user", Key: "secret-key", PassTicket: "ticket", AppmsgToken: "token",
	}, func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("author_id") != "gh_candidate" {
			t.Fatal("candidate author ID was omitted from author request")
		}
		switch req.URL.Query().Get("action") {
		case "show":
			showCalls++
			return authorResponse("<html><title>作者</title></html>", true), nil
		case "get_articles":
			listCalls++
			if !strings.Contains(req.Header.Get("Referer"), "author_id=gh_candidate") {
				t.Fatal("author list referer does not use the candidate")
			}
			if req.URL.Query().Get("from_article_id") == "" {
				return authorResponse(`{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzTest","mid":"123","title":"文章","url":"https://mp.weixin.qq.com/s/Short_123"}]}`, false), nil
			}
			if req.URL.Query().Get("from_article_id") != "123" {
				t.Fatal("unexpected author history cursor")
			}
			return authorResponse(`{"ret":0,"base_resp":{"ret":0},"articles":[]}`, false), nil
		default:
			t.Fatal("unexpected author request")
			return nil, nil
		}
	})
	history, err := c.FetchArticleHistory("MzTest")
	if err != nil {
		t.Fatal(err)
	}
	if showCalls != 1 || listCalls != 2 || history.Pages != 2 || len(history.Articles) != 1 {
		t.Fatalf("unexpected history result: show=%d list=%d pages=%d articles=%d", showCalls, listCalls, history.Pages, len(history.Articles))
	}
	if accounts["MzTest"].AuthorId != "gh_candidate" || !accounts["MzTest"].AuthorIdVerified {
		t.Fatal("validated candidate was not promoted")
	}
	if _, err := os.Stat(mp_json_filepath); err != nil {
		t.Fatal("validated account was not persisted")
	}
}

func TestCandidateAuthorIDRejectsUnverifiedFirstPage(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: `{"ret":0,"base_resp":{"ret":0},"articles":[]}`},
		{name: "other account", body: `{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzOther","mid":"1","title":"文章","url":"https://mp.weixin.qq.com/s?__biz=MzOther&mid=1"}]}`},
		{name: "other account short link", body: `{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzOther","mid":"1","title":"文章","url":"https://mp.weixin.qq.com/s/Short_123"}]}`},
		{name: "missing account identity", body: `{"ret":0,"base_resp":{"ret":0},"articles":[{"mid":"1","title":"文章","url":"https://mp.weixin.qq.com/s?mid=1"}]}`},
		{name: "missing account identity short link", body: `{"ret":0,"base_resp":{"ret":0},"articles":[{"mid":"1","title":"文章","url":"https://mp.weixin.qq.com/s/Short_123"}]}`},
		{name: "conflicting url", body: `{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzTest","mid":"1","title":"文章","url":"https://mp.weixin.qq.com/s?__biz=MzOther&mid=1"}]}`},
		{name: "conflicting short link query", body: `{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzTest","mid":"1","title":"文章","url":"https://mp.weixin.qq.com/s/Short_123?__biz=MzOther"}]}`},
		{name: "protocol relative url", body: `{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzTest","mid":"1","title":"文章","url":"//evil.example/s?__biz=MzTest&mid=1"}]}`},
		{name: "empty url", body: `{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzTest","mid":"1","title":"文章","url":""}]}`},
		{name: "empty title", body: `{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzTest","mid":"1","url":"https://mp.weixin.qq.com/s?__biz=MzTest&mid=1"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := authorTestClient(t, OfficialAccount{
				Biz: "MzTest", CandidateAuthorId: "gh_candidate", Key: "secret-key", Cookie: "author_session=ready", CookieExpiration: time.Now().Add(time.Hour).Unix(),
			}, func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("action") != "get_articles" {
					t.Fatal("unexpected author show request")
				}
				return authorResponse(tt.body, false), nil
			})
			_, err := c.fetchArticleList("MzTest", "")
			if !errors.Is(err, ErrAuthorHistoryUnavailable) || accounts["MzTest"].AuthorId != "" {
				t.Fatalf("unverified candidate was accepted: %v", err)
			}
		})
	}
}

func TestCandidateAuthorIDWaitsForEntireHistory(t *testing.T) {
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest", CandidateAuthorId: "gh_candidate", Key: "secret-key", Cookie: "ready", CookieExpiration: time.Now().Add(time.Hour).Unix(),
	}, func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("from_article_id") == "" {
			return authorResponse(`{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzTest","mid":"2","title":"文章二","url":"https://mp.weixin.qq.com/s?__biz=MzTest&mid=2"}]}`, false), nil
		}
		return authorResponse(`{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzOther","mid":"1","title":"文章一","url":"https://mp.weixin.qq.com/s?__biz=MzOther&mid=1"}]}`, false), nil
	})
	_, err := c.FetchArticleHistory("MzTest")
	if !errors.Is(err, ErrAuthorHistoryUnavailable) || accounts["MzTest"].AuthorId != "" {
		t.Fatalf("candidate was promoted before every page matched: %v", err)
	}
}

func TestCandidateAuthorIDVerificationAndExpiryDoNotPromote(t *testing.T) {
	t.Run("verification page with cookie", func(t *testing.T) {
		c := authorTestClient(t, OfficialAccount{Biz: "MzTest", CandidateAuthorId: "gh_candidate", Key: "secret-key"}, func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("action") != "show" {
				t.Fatal("verification page should stop before article request")
			}
			return authorResponse("<html><title>验证</title></html>", true), nil
		})
		_, err := c.fetchArticleList("MzTest", "")
		if err == nil || accounts["MzTest"].AuthorId != "" || accounts["MzTest"].Cookie != "" {
			t.Fatalf("verification page was accepted: %v", err)
		}
	})
	t.Run("expired session", func(t *testing.T) {
		c := authorTestClient(t, OfficialAccount{
			Biz: "MzTest", CandidateAuthorId: "gh_candidate", Key: "secret-key", Cookie: "old", CookieExpiration: time.Now().Add(time.Hour).Unix(),
		}, func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("action") == "show" {
				return authorResponse("<html><title>作者</title></html>", true), nil
			}
			return authorResponse(`{"ret":-3,"base_resp":{"ret":-3},"articles":[]}`, false), nil
		})
		_, err := c.fetchArticleList("MzTest", "")
		code, _, _, ok := codedErrorOf(err)
		if !ok || code != result.CodeAccountExpired || accounts["MzTest"].AuthorId != "" {
			t.Fatalf("expired session was accepted: %v", err)
		}
	})
}

func TestRejectedAuthorListPreservesCookieRefreshFailure(t *testing.T) {
	tests := []struct {
		name     string
		show     func() (*http.Response, error)
		wantKind string
		wantText string
	}{
		{
			name: "author page HTTP 500", wantKind: "remote_error", wantText: "HTTP 500",
			show: func() (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("error"))}, nil
			},
		},
		{
			name: "verification page", wantKind: "verification_required", wantText: "验证",
			show: func() (*http.Response, error) {
				return authorResponse("<html><title>验证</title></html>", false), nil
			},
		},
		{
			name: "network failure", wantKind: "network", wantText: "网络",
			show: func() (*http.Response, error) {
				return nil, errors.New("dial failed")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var listCalls, showCalls int
			c := authorTestClient(t, OfficialAccount{
				Biz: "MzTest", AuthorId: "gh_author", Key: "secret-key", Cookie: "old", CookieExpiration: time.Now().Add(time.Hour).Unix(),
			}, func(req *http.Request) (*http.Response, error) {
				switch req.URL.Query().Get("action") {
				case "get_articles":
					listCalls++
					return authorResponse(`{"ret":-3,"base_resp":{"ret":-3},"articles":[]}`, false), nil
				case "show":
					showCalls++
					return tt.show()
				default:
					t.Fatal("unexpected author request")
					return nil, nil
				}
			})
			_, err := c.fetchArticleList("MzTest", "")
			kind, _ := ClassifyHistoryError(err)
			message := authorHistoryUserMessage(err)
			if kind != tt.wantKind || !strings.Contains(message, tt.wantText) {
				t.Fatalf("refresh failure was misclassified: kind=%q message=%q err=%v", kind, message, err)
			}
			if strings.Contains(message, "secret-key") || strings.Contains(message, "https://") {
				t.Fatalf("user message disclosed request credentials: %q", message)
			}
			if listCalls != 1 || showCalls != 1 {
				t.Fatalf("unexpected retry sequence: list=%d show=%d", listCalls, showCalls)
			}
		})
	}
}

func TestExplicitAuthorIDWinsAndNetworkErrorHidesCredentials(t *testing.T) {
	t.Run("explicit author", func(t *testing.T) {
		c := authorTestClient(t, OfficialAccount{Biz: "MzTest", AuthorId: "explicit", CandidateAuthorId: "gh_candidate", Key: "secret-key"}, func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("author_id") != "explicit" {
				t.Fatal("candidate replaced explicit author ID")
			}
			if req.URL.Query().Get("action") == "show" {
				return authorResponse("<html><title>作者</title></html>", true), nil
			}
			return authorResponse(`{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzTest","mid":"1","title":"文章","url":"https://mp.weixin.qq.com/s?__biz=MzTest&mid=1"}]}`, false), nil
		})
		if _, err := c.fetchArticleList("MzTest", ""); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("safe network error", func(t *testing.T) {
		c := authorTestClient(t, OfficialAccount{Biz: "MzTest", CandidateAuthorId: "gh_candidate", Key: "secret-key"}, func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		})
		_, err := c.fetchArticleList("MzTest", "")
		if err == nil || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "https://") {
			t.Fatalf("network error disclosed request credentials: %v", err)
		}
	})
}

func TestUnverifiedExplicitAuthorIDRejectsOtherAccount(t *testing.T) {
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest", AuthorId: "legacy-author", Key: "secret-key", Cookie: "ready", CookieExpiration: time.Now().Add(time.Hour).Unix(),
	}, func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("author_id") != "legacy-author" || req.URL.Query().Get("action") != "get_articles" {
			t.Fatal("unexpected author request")
		}
		return authorResponse(`{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzOther","mid":"1","title":"其他公众号文章","url":"https://mp.weixin.qq.com/s/Short_123"}]}`, false), nil
	})
	_, err := c.fetchArticleList("MzTest", "")
	if !errors.Is(err, ErrAuthorHistoryUnavailable) || !errors.Is(err, ErrCandidateAuthorHistoryRejected) {
		t.Fatalf("unverified author ID accepted another account: %v", err)
	}
}
