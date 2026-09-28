package officialaccount

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"mp_article_batch_downloader/internal/interceptor"
	"mp_article_batch_downloader/internal/interceptor/proxy"
)

type captureProxyContext struct {
	req  *proxy.ContextReq
	body []byte
}

func (c *captureProxyContext) Req() *proxy.ContextReq { return c.req }
func (c *captureProxyContext) Res() *proxy.ContextRes {
	return &proxy.ContextRes{StatusCode: http.StatusOK}
}
func (c *captureProxyContext) Mock(int, map[string]string, string) {}
func (c *captureProxyContext) GetResponseHeader(string) string     { return "text/html; charset=utf-8" }
func (c *captureProxyContext) SetResponseHeader(string, string)    {}
func (c *captureProxyContext) SetResponseBody(string)              {}
func (c *captureProxyContext) GetResponseBody() ([]byte, error)    { return c.body, nil }
func (c *captureProxyContext) SetStatusCode(int)                   {}

func proxyArticleRequest(host, path, query, cookie string) *proxy.ContextReq {
	return &proxy.ContextReq{
		URL:    &proxy.ContextURL{Hostname: func() string { return host }, Path: path, RawQuery: query},
		Header: http.Header{"Cookie": []string{cookie}},
	}
}

func proxyArticlePage(biz, authorID, candidate, key string) []byte {
	return []byte(`<html><body><script>var biz = "` + biz + `"; window.cgiDataNew = {nick_name:"测试公众号", authorId:"` + authorID + `", user_name:"` + candidate + `"}; window.key = "` + key + `";</script></body></html>`)
}

func TestProxyCapturesSameAccountArticleAndRequestCookie(t *testing.T) {
	authorTestClient(t, OfficialAccount{
		Biz: "MzTest1", Key: "old-key", Uin: "old-user", PassTicket: "old-ticket", Cookie: "old-cookie",
	}, nil)
	req := proxyArticleRequest("mp.weixin.qq.com", "/s", "__biz=MzTest1&mid=123&idx=1&key=new-key", "wx_session=fresh")
	if !captureArticleAccountFromProxy(req, proxyArticlePage("MzTest1", "gh_author", "gh_candidate", "new-key")) {
		t.Fatal("valid article response did not refresh account")
	}
	got := accounts["MzTest1"]
	if got.AuthorId != "gh_author" || got.CandidateAuthorId != "gh_candidate" || got.Key != "new-key" ||
		got.Cookie != "wx_session=fresh" || got.Uin != "" || got.PassTicket != "" {
		t.Fatalf("proxy capture mixed sessions or lost author identity: %+v", got)
	}
}

func TestProxyCapturesModernShortArticle(t *testing.T) {
	authorTestClient(t, OfficialAccount{Biz: "MzTest1", Key: "old-key"}, nil)
	req := proxyArticleRequest("mp.weixin.qq.com", "/s/Short_123", "key=new-key", "wx_session=fresh")
	if !captureArticleAccountFromProxy(req, proxyArticlePage("MzTest1", "", "gh_candidate", "new-key")) ||
		accounts["MzTest1"].CandidateAuthorId != "gh_candidate" {
		t.Fatal("modern short article did not supply its account candidate")
	}
}

func TestProxyCapturesPageSessionWithoutMixingStaleURLCredentials(t *testing.T) {
	authorTestClient(t, OfficialAccount{Biz: "MzTest1", Key: "previous-key"}, nil)
	req := proxyArticleRequest("mp.weixin.qq.com", "/s", "__biz=MzTest1&mid=123&idx=1&sn=article-sn&uin=old-user&key=old-key&pass_ticket=old-ticket&appmsg_token=old-token&author_id=gh_old", "wx_session=current")
	page := []byte(`<html><body><script>var biz="MzTest1"; window.cgiDataNew={nick_name:"测试公众号",authorId:"gh_current"}; var uin="new-user"; window.key="new-key"; var pass_ticket="new-ticket"; window.appmsg_token="new-token";</script></body></html>`)
	if !captureArticleAccountFromProxy(req, page) {
		t.Fatal("page session was rejected because the article URL contained an older session")
	}
	got := accounts["MzTest1"]
	if got.Key != "new-key" || got.Uin != "new-user" || got.PassTicket != "new-ticket" ||
		got.AppmsgToken != "new-token" || got.AuthorId != "gh_current" || got.Cookie != "wx_session=current" {
		t.Fatal("proxy did not preserve the page session and author identity")
	}
	if !strings.Contains(got.RefreshUri, "mid=123") || !strings.Contains(got.RefreshUri, "sn=article-sn") ||
		strings.Contains(got.RefreshUri, "old-key") || strings.Contains(got.RefreshUri, "gh_old") {
		t.Fatal("refresh URL reused old session fields or lost the article identity")
	}
}

func TestProxyCapturesURLAuthorIDOnlyWhenPageKeyMatches(t *testing.T) {
	tests := []struct {
		name, pageKey, wantAuthor, existingBiz string
	}{
		{name: "first article with same key", pageKey: "current-key", wantAuthor: "explicit-author", existingBiz: "MzOther1"},
		{name: "different key", pageKey: "new-key", wantAuthor: "", existingBiz: "MzTest1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authorTestClient(t, OfficialAccount{Biz: tt.existingBiz, Key: "previous-key"}, nil)
			req := proxyArticleRequest("mp.weixin.qq.com", "/s", "__biz=MzTest1&mid=123&key=current-key&author_id=explicit-author", "")
			page := proxyArticlePage("MzTest1", "", "gh_candidate", tt.pageKey)
			if !captureArticleAccountFromProxy(req, page) {
				t.Fatal("valid article response was not captured")
			}
			got := accounts["MzTest1"]
			if got.AuthorId != tt.wantAuthor || got.CandidateAuthorId != "gh_candidate" || got.Key != tt.pageKey {
				t.Fatalf("unexpected author capture: author=%q candidate=%q key_matches=%v", got.AuthorId, got.CandidateAuthorId, got.Key == tt.pageKey)
			}
		})
	}
}

func TestProxyNewPageKeyClearsMissingOldSessionFields(t *testing.T) {
	authorTestClient(t, OfficialAccount{
		Biz: "MzTest1", Nickname: "已保存公众号", Key: "old-key", Uin: "old-user",
		PassTicket: "old-ticket", AppmsgToken: "old-token", Cookie: "old-cookie",
		AuthorId: "gh_verified", AuthorIdVerified: true,
	}, nil)
	req := proxyArticleRequest("mp.weixin.qq.com", "/s", "__biz=MzTest1&uin=url-user&key=url-key&pass_ticket=url-ticket", "")
	if !captureArticleAccountFromProxy(req, proxyArticlePage("MzTest1", "", "", "new-key")) {
		t.Fatal("new page key was not captured")
	}
	got := accounts["MzTest1"]
	if got.Key != "new-key" || got.Uin != "" || got.PassTicket != "" || got.AppmsgToken != "" || got.Cookie != "" {
		t.Fatal("new page key retained an old session field")
	}
	if got.AuthorId != "gh_verified" || !got.AuthorIdVerified || got.Nickname == "" {
		t.Fatal("new page session lost verified account identity")
	}
}

func TestProxyUsesURLSessionWhenPageHasNoKey(t *testing.T) {
	authorTestClient(t, OfficialAccount{Biz: "MzTest1", Key: "previous-key"}, nil)
	req := proxyArticleRequest("mp.weixin.qq.com", "/s", "__biz=MzTest1&uin=url-user&key=url-key&pass_ticket=url-ticket&author_id=gh_url", "wx_session=current")
	if !captureArticleAccountFromProxy(req, proxyArticlePage("MzTest1", "", "", "")) {
		t.Fatal("URL session fallback was rejected when the page had no key")
	}
	got := accounts["MzTest1"]
	if got.Uin != "url-user" || got.Key != "url-key" || got.PassTicket != "url-ticket" || got.AuthorId != "gh_url" {
		t.Fatal("URL session fallback lost credentials")
	}
}

func TestProxyRejectsCrossAccountResponses(t *testing.T) {
	tests := []struct {
		name string
		req  *proxy.ContextReq
		page []byte
	}{
		{"other page biz", proxyArticleRequest("mp.weixin.qq.com", "/s", "__biz=MzTest1&key=new-key", "fresh"), proxyArticlePage("MzOther1", "gh_other", "", "new-key")},
		{"other host", proxyArticleRequest("evil.example", "/s", "__biz=MzTest1&key=new-key", "fresh"), proxyArticlePage("MzTest1", "gh_author", "", "new-key")},
		{"other path", proxyArticleRequest("mp.weixin.qq.com", "/mp/author", "__biz=MzTest1&key=new-key", "fresh"), proxyArticlePage("MzTest1", "gh_author", "", "new-key")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authorTestClient(t, OfficialAccount{Biz: "MzTest1", Key: "old-key", AuthorId: "gh_existing", Cookie: "old-cookie"}, nil)
			if captureArticleAccountFromProxy(tt.req, tt.page) {
				t.Fatal("unsafe response changed account")
			}
			got := accounts["MzTest1"]
			if got.Key != "old-key" || got.AuthorId != "gh_existing" || got.Cookie != "old-cookie" {
				t.Fatalf("unsafe response overwrote account: %+v", got)
			}
		})
	}
}

func TestProxyWithoutSessionKeyDoesNotOverwriteAccount(t *testing.T) {
	authorTestClient(t, OfficialAccount{Biz: "MzTest1", Key: "old-key", AuthorId: "gh_existing", Cookie: "old-cookie"}, nil)
	req := proxyArticleRequest("mp.weixin.qq.com", "/s", "__biz=MzTest1&mid=123", "new-cookie")
	plugin := CreateOfficialAccountInterceptorPlugin(&OfficialAccountConfig{}, &interceptor.ChannelInjectedFiles{})
	plugin.OnRequest(&captureProxyContext{req: proxyArticleRequest("mp.weixin.qq.com", "/s", "__biz=MzTest1&author_id=gh_other", "new-cookie")})
	if captureArticleAccountFromProxy(req, proxyArticlePage("MzTest1", "gh_other", "", "")) {
		t.Fatal("page without a key was treated as a new session")
	}
	got := accounts["MzTest1"]
	if got.Key != "old-key" || got.AuthorId != "gh_existing" || got.Cookie != "old-cookie" {
		t.Fatalf("keyless page overwrote account: %+v", got)
	}
}

func TestProxyResponseCaptureDoesNotLogCookieOrURL(t *testing.T) {
	authorTestClient(t, OfficialAccount{Biz: "MzTest1", Key: "old-key"}, nil)
	ctx := &captureProxyContext{
		req:  proxyArticleRequest("mp.weixin.qq.com", "/s", "__biz=MzTest1&key=secret-key", "wx_session=private-cookie"),
		body: proxyArticlePage("MzTest1", "gh_author", "", "secret-key"),
	}
	plugin := CreateOfficialAccountInterceptorPlugin(&OfficialAccountConfig{}, &interceptor.ChannelInjectedFiles{})
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previousStdout := os.Stdout
	os.Stdout = writer
	defer func() {
		os.Stdout = previousStdout
		reader.Close()
		writer.Close()
	}()
	plugin.OnResponse(ctx)
	writer.Close()
	os.Stdout = previousStdout
	logged, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), "private-cookie") || strings.Contains(string(logged), "secret-key") ||
		strings.Contains(string(logged), "__biz=") {
		t.Fatalf("proxy response log disclosed request credentials: %q", logged)
	}
	if accounts["MzTest1"].Cookie != "wx_session=private-cookie" {
		t.Fatal("proxy response did not capture the current request cookie")
	}
}

func TestProxyCaptureReasonCodesDoNotExposeRequestOrPage(t *testing.T) {
	const secret = "SENSITIVE_MARKER"
	tests := []struct {
		name, path, query, body, reason, category string
		captured                                  bool
	}{
		{"verification", "/s", "__biz=MzTest1&key=" + secret, `<html>wappoc_appmsgcaptcha ` + secret + `</html>`, "verification_required", "article_query", false},
		{"missing page biz", "/s", "__biz=MzTest1&key=" + secret, string(proxyArticlePage("", "gh_author", "", secret)), "missing_page_biz", "article_query", false},
		{"cross account", "/s", "__biz=MzTest1&key=" + secret, string(proxyArticlePage("MzOther1", "gh_author", "", secret)), "biz_mismatch", "article_query", false},
		{"parse failed", "/s", "__biz=MzTest1&key=" + secret, `<html><script>var biz="MzTest1";window.cgiDataNew={user_name:"gh_author"};window.key="` + secret + `";</script></html>`, "account_parse_failed", "article_query", false},
		{"missing key", "/s", "__biz=MzTest1", string(proxyArticlePage("MzTest1", "gh_author", "", "")), "missing_key", "article_query", false},
		{"page session replaces stale URL key", "/s", "__biz=MzTest1&key=" + secret, string(proxyArticlePage("MzTest1", "gh_author", "", "different-key")), "captured_page_session", "article_query", true},
		{"short link captured", "/s/Short_123", "key=" + secret, string(proxyArticlePage("MzTest1", "gh_author", "", secret)), "captured", "article_short", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authorTestClient(t, OfficialAccount{Biz: "MzTest1", Key: "old-key"}, nil)
			req := proxyArticleRequest("mp.weixin.qq.com", tt.path, tt.query, "wx_cookie="+secret)
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			previousStdout := os.Stdout
			os.Stdout = writer
			captured := captureArticleAccountFromProxy(req, []byte(tt.body))
			writer.Close()
			os.Stdout = previousStdout
			logged, readErr := io.ReadAll(reader)
			reader.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if captured != tt.captured || !strings.Contains(string(logged), "reason="+tt.reason+" path="+tt.category) ||
				strings.Contains(string(logged), secret) || strings.Contains(string(logged), "__biz=") ||
				strings.Contains(string(logged), "wx_cookie") || strings.Contains(string(logged), "gh_author") {
				t.Fatalf("unsafe or incorrect capture diagnostic: captured=%v log=%q", captured, logged)
			}
		})
	}
}
