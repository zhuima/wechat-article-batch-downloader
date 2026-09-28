package officialaccount

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	officialaccountdownload "github.com/GopeedLab/gopeed/pkg/officialaccount"
	"mp_article_batch_downloader/internal/archive"
)

func TestParseArticleImportURLAllowsOnlyWeChatArticle(t *testing.T) {
	for _, valid := range []string{
		"https://mp.weixin.qq.com/s?__biz=MzTest&mid=1&idx=1&sn=x",
		"https://mp.weixin.qq.com/s?__biz=MzTest&scene=1",
		"https://mp.weixin.qq.com/s/",
		"https://mp.weixin.qq.com/s/short_slug-1?scene=1",
		"https://mp.weixin.qq.com/s?mid=1",
	} {
		if _, err := parseArticleImportURL(valid); err != nil {
			t.Fatalf("valid article URL rejected: %v", err)
		}
	}
	for _, raw := range []string{
		"http://mp.weixin.qq.com/s?__biz=MzTest",
		"https://mp.weixin.qq.com.evil.example/s?__biz=MzTest",
		"https://user@mp.weixin.qq.com/s?__biz=MzTest",
		"https://mp.weixin.qq.com:443/s?__biz=MzTest",
		"https://mp.weixin.qq.com/mp/profile_ext?__biz=MzTest",
		"https://mp.weixin.qq.com/s%2F?__biz=MzTest",
		"https://mp.weixin.qq.com/s/short/extra",
		"https://mp.weixin.qq.com/s/short%2Fextra",
		"https://mp.weixin.qq.com/s/short.name",
		"https://mp.weixin.qq.com/s/" + strings.Repeat("a", 257),
		"https://mp.weixin.qq.com/s?__biz=first&__biz=second",
		"https://mp.weixin.qq.com/s?x=%ZZ",
	} {
		if _, err := parseArticleImportURL(raw); err == nil {
			t.Errorf("unsafe article URL accepted: %s", raw)
		}
	}
}

func TestParseImportedAccountUsesArticleHTMLAndURLCredentials(t *testing.T) {
	u, err := parseArticleImportURL("https://mp.weixin.qq.com/s?__biz=MzTest&mid=123&idx=1&sn=abc&uin=777&key=secret&pass_ticket=p%2Bq")
	if err != nil {
		t.Fatal(err)
	}
	page := []byte(`<html><body><strong id="js_name">备用名称</strong><script>
window.cgiDataNew = {"nick_name":"元哥\u4e8c号","user_name":"gh_username","authorId":"gh_author","title":"一篇测试文章","round_head_img":"https:\/\/example.com\/avatar"};
var appmsg_token = 'token-1';
</script></body></html>`)
	account, title, err := parseImportedAccount(u, u, page)
	if err != nil {
		t.Fatal(err)
	}
	if account.Biz != "MzTest" || account.Nickname != "元哥二号" || account.AuthorId != "gh_author" || account.AppmsgToken != "token-1" || title != "一篇测试文章" {
		t.Fatalf("article metadata not parsed: %+v", account)
	}
	if account.Uin != "777" || account.Key != "secret" || account.PassTicket != "p+q" {
		t.Fatal("article URL credentials not parsed")
	}
	if account.AvatarURL != "https://example.com/avatar" {
		t.Fatalf("avatar URL = %q", account.AvatarURL)
	}
	if strings.Contains(account.RefreshUri, "secret") || strings.Contains(account.RefreshUri, "pass_ticket") || strings.Contains(account.RefreshUri, "uin=") {
		t.Fatalf("refresh URI leaked credentials: %s", account.RefreshUri)
	}
	if !strings.Contains(account.RefreshUri, "mid=123") {
		t.Fatalf("refresh URI lost article identity: %s", account.RefreshUri)
	}
}

func TestParseImportedAccountReadsSamePageGlobalsUsedByInjector(t *testing.T) {
	u, err := parseArticleImportURL("https://mp.weixin.qq.com/s/article_slug")
	if err != nil {
		t.Fatal(err)
	}
	page := []byte(`<html><body><script>
var biz = "MzExampleID123==";
window.cgiDataNew = {"nick_name":"公众号","title":"当前文章","content_noencode":"<p>正文</p>","authorId":"gh_author","user_name":"gh_username"};
var uin = "777";
window.key = "session-key";
var pass_ticket = "pass-ticket";
window.appmsg_token = "appmsg-token";
</script></body></html>`)
	account, title, err := parseImportedAccount(u, u, page)
	if err != nil {
		t.Fatal(err)
	}
	if account.Biz != "MzExampleID123==" || account.Nickname != "公众号" || title != "当前文章" || account.AuthorId != "gh_author" {
		t.Fatal("article identity was not parsed from the page")
	}
	if account.Uin != "777" || account.Key != "session-key" || account.PassTicket != "pass-ticket" || account.AppmsgToken != "appmsg-token" {
		t.Fatal("the page session fields used by the upstream injector were not imported")
	}
	if importedAccountResponse(account, title)["history_available"] != true {
		t.Fatal("a complete page session was not recognized as history capable")
	}
}

func TestParseImportedAccountDoesNotMistakeObjectKeyForSession(t *testing.T) {
	u, _ := parseArticleImportURL("https://mp.weixin.qq.com/s/article_slug")
	page := []byte(`<html><body><script>var biz = "MzExampleID123=="; window.cgiDataNew = {"nick_name":"公众号","title":"当前文章","content_noencode":"<p>正文</p>","key":"unrelated"};</script></body></html>`)
	account, title, err := parseImportedAccount(u, u, page)
	if err != nil {
		t.Fatal(err)
	}
	if account.Key != "" || importedAccountResponse(account, title)["history_available"] != false {
		t.Fatal("an unrelated object property was accepted as a session credential")
	}
}

func TestParseImportedAccountDiscardsConflictingPageSession(t *testing.T) {
	for _, tt := range []struct {
		name, script string
	}{
		{"key", `var uin = "777"; window.key = "different";`},
		{"uin", `var uin = "different"; window.key = "session-key";`},
		{"pass ticket", `var uin = "777"; window.key = "session-key"; var pass_ticket = "different";`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u, _ := parseArticleImportURL("https://mp.weixin.qq.com/s?__biz=MzTest&uin=777&key=session-key&pass_ticket=pass-ticket")
			page := []byte(`<html><body><strong id="js_name">公众号</strong><script>` + tt.script + `</script></body></html>`)
			account, title, err := parseImportedAccount(u, u, page)
			if err != nil {
				t.Fatal(err)
			}
			if account.Key != "" || account.Uin != "" || account.PassTicket != "" || importedAccountResponse(account, title)["history_available"] != false {
				t.Fatal("conflicting URL and page values were combined into a history session")
			}
		})
	}
}

func TestParseImportedAccountDoesNotReuseCredentialLostOnRedirect(t *testing.T) {
	requested, _ := parseArticleImportURL("https://mp.weixin.qq.com/s?__biz=MzTest&uin=777&key=old-session")
	resolved, _ := parseArticleImportURL("https://mp.weixin.qq.com/s/article_slug?__biz=MzTest")
	page := []byte(`<html><body><strong id="js_name">公众号</strong><script>window.cgiDataNew = {"title":"当前文章","content_noencode":"<p>正文</p>"};</script></body></html>`)
	account, title, err := parseImportedAccount(requested, resolved, page)
	if err != nil {
		t.Fatal(err)
	}
	if account.Key != "" || account.Uin != "" || importedAccountResponse(account, title)["history_available"] != false {
		t.Fatal("a redirect reused session fields that were absent from the fetched page")
	}
}

func TestParseImportedAccountUsesDOMNameWithoutRequiringKey(t *testing.T) {
	page := []byte(`<html><body><h1 id="activity-name">当前文章</h1><strong id="js_name">公众号&amp;朋友</strong></body></html>`)
	for _, test := range []struct {
		raw, wantName string
	}{
		{"https://mp.weixin.qq.com/s/?__biz=MzTest&key=secret&author_id=gh_from_url", "公众号&朋友"},
		{"https://mp.weixin.qq.com/s?__biz=MzTest", "公众号&朋友"},
	} {
		u, err := parseArticleImportURL(test.raw)
		if err != nil {
			t.Fatal(err)
		}
		account, title, err := parseImportedAccount(u, u, page)
		if err != nil {
			t.Fatalf("parseImportedAccount(%q): account=%+v, err=%v", test.raw, account, err)
		}
		if account.Nickname != test.wantName || title != "当前文章" {
			t.Fatalf("unexpected account: %+v", account)
		}
		if strings.Contains(test.raw, "key=") && account.AuthorId != "gh_from_url" {
			t.Fatalf("explicit author ID was lost: %+v", account)
		}
		if !strings.Contains(test.raw, "key=") && importedAccountResponse(account, title)["history_available"] != false {
			t.Fatal("public link was marked as history capable")
		}
	}
}

func TestImportedArticleOnlyClaimsDownloadWhenExporterCanParseContent(t *testing.T) {
	u, err := parseArticleImportURL("https://mp.weixin.qq.com/s/public_slug")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, page string
		want       bool
	}{
		{"DOM metadata without exporter data", `<html><body><h1 id="activity-name">文章</h1><strong id="js_name">公众号</strong></body></html>`, false},
		{"empty exporter data", `<html><body><strong id="js_name">公众号</strong><script>window.cgiDataNew = {"title":"文章","nick_name":"公众号"};</script></body></html>`, false},
		{"article body", `<html><body><script>window.cgiDataNew = {"title":"文章","nick_name":"公众号","content_noencode":"<p>正文</p>"};</script></body></html>`, true},
		{"verification page", `<html><body><script>window.cgiDataNew = {"title":"文章","nick_name":"公众号","content_noencode":"<p>正文</p>"};</script><p>poc_token</p></body></html>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := []byte(tc.page)
			if _, _, err := parseImportedAccount(u, u, page); err != nil {
				t.Fatalf("import metadata should be recognizable: %v", err)
			}
			if got := officialaccountdownload.IsDownloadableArticleHTML(page); got != tc.want {
				t.Fatalf("downloadable = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestShortArticleURLCanImportWithoutBizOrKey(t *testing.T) {
	u, err := parseArticleImportURL("https://mp.weixin.qq.com/s/public_slug?scene=1")
	if err != nil {
		t.Fatal(err)
	}
	page := []byte(`<html><body><strong id="js_name">测试公众号</strong><script>var biz = "MzTest"; window.cgiDataNew = {"title":"短链接文章","nick_name":"测试公众号"};</script></body></html>`)
	account, title, err := parseImportedAccount(u, u, page)
	if err != nil {
		t.Fatal(err)
	}
	if account.Biz != "MzTest" || account.Key != "" || title != "短链接文章" {
		t.Fatalf("short article parsed incorrectly: %+v title=%q", account, title)
	}
	response := importedAccountResponse(account, title)
	if response["single_article_available"] != true || response["history_available"] != false || response["history_reason"] == "" {
		t.Fatalf("public short link response misstates its capabilities: %+v", response)
	}
}

func TestShortArticleIgnoresConcatenatedBizSnippet(t *testing.T) {
	u, _ := parseArticleImportURL("https://mp.weixin.qq.com/s/public_slug")
	page := []byte(`<html><body><script>
var decorated = "__biz=\" + biz + \"";
var biz = "MzExampleID123==";
window.cgiDataNew = {"nick_name":"测试公众号","title":"当前文章"};
</script></body></html>`)
	account, _, err := parseImportedAccount(u, u, page)
	if err != nil || account.Biz != "MzExampleID123==" {
		t.Fatalf("literal biz was not selected: account=%+v err=%v", account, err)
	}
}

func TestShortArticleWithoutBizRemainsSingleArticleOnly(t *testing.T) {
	u, _ := parseArticleImportURL("https://mp.weixin.qq.com/s/public_slug")
	page := []byte(`<html><body><strong id="js_name">测试公众号</strong><h1 id="activity-name">短链接文章</h1></body></html>`)
	account, title, err := parseImportedAccount(u, u, page)
	if err != nil {
		t.Fatal(err)
	}
	response := importedAccountResponse(account, title)
	if response["biz"] != "" || response["single_article_available"] != true || response["history_available"] != false || response["history_reason"] == "" {
		t.Fatalf("unidentified article was misreported: %+v", response)
	}
}

func TestArticleImportRejectsConflictingAccountIdentity(t *testing.T) {
	requested, _ := parseArticleImportURL("https://mp.weixin.qq.com/s?__biz=MzOne1&key=secret")
	resolved, _ := parseArticleImportURL("https://mp.weixin.qq.com/s?__biz=MzTwo2")
	page := []byte(`<html><body><strong id="js_name">测试公众号</strong><script>var biz = "MzOne1";</script></body></html>`)
	if _, _, err := parseImportedAccount(requested, resolved, page); err == nil {
		t.Fatal("conflicting redirect account accepted")
	}
	resolved, _ = parseArticleImportURL("https://mp.weixin.qq.com/s?__biz=MzOne1")
	page = []byte(`<html><body><strong id="js_name">测试公众号</strong><script>var biz = "MzTwo2";</script></body></html>`)
	if _, _, err := parseImportedAccount(requested, resolved, page); err == nil {
		t.Fatal("conflicting page account accepted")
	}
}

func TestArticleUsernameIsNotAuthorID(t *testing.T) {
	u, _ := parseArticleImportURL("https://mp.weixin.qq.com/s?__biz=MzTest&key=secret")
	page := []byte(`<html><body><script>window.cgiDataNew = {"nick_name":"公众号","user_name":"gh_username","author_id":""}; window.appmsg_token = "token";</script></body></html>`)
	account, _, err := parseImportedAccount(u, u, page)
	if err != nil {
		t.Fatal(err)
	}
	if account.AuthorId != "" || account.CandidateAuthorId != "gh_username" || account.AppmsgToken != "token" {
		t.Fatalf("username was misclassified as author ID: %+v", account)
	}
}

func TestArticleImportReadsAuthorFieldsFromJavaScriptCGIData(t *testing.T) {
	u, _ := parseArticleImportURL("https://mp.weixin.qq.com/s/article_slug?__biz=MzTest")
	page := []byte(`<html><body><script>
window.cgiDataNew = {
  nick_name: '公众号',
  title: '当前文章',
  unrelated: {authorId: 'gh_nested', user_name: 'gh_nested'},
  list: [{user_name: 'gh_in_list'}],
  authorId: 'gh_actual_author',
  user_name: 'gh_actual_account',
};
</script></body></html>`)
	account, _, err := parseImportedAccount(u, u, page)
	if err != nil {
		t.Fatal(err)
	}
	if account.AuthorId != "gh_actual_author" || account.CandidateAuthorId != "gh_actual_account" {
		t.Fatalf("top-level JavaScript author fields were not parsed: author=%q candidate=%q", account.AuthorId, account.CandidateAuthorId)
	}
}

func TestCGIDataJavaScriptParserOnlyReadsTopLevelLiterals(t *testing.T) {
	for _, tc := range []struct {
		name, script, want string
	}{
		{
			name: "nested fields and comments",
			script: `window.cgiDataNew = {
  nested: {user_name: 'gh_wrong'},
  array: [{user_name: 'gh_wrong_too'}],
  // user_name: 'gh_comment',
  text: "user_name: 'gh_in_string', }",
  /* user_name: 'gh_block_comment' */
  user_name: 'gh_right',
};`,
			want: "gh_right",
		},
		{
			name:   "quoted property and escaped value",
			script: `window.cgiDataNew = {'user_name': 'gh_\u0061ccount',};`,
			want:   "gh_account",
		},
		{
			name:   "regex value before author field",
			script: `window.cgiDataNew = {pattern: /[},]/, user_name: 'gh_right'};`,
			want:   "gh_right",
		},
		{
			name:   "quoted and commented assignments are ignored",
			script: "var quoted = \"cgiDataNew = {user_name: 'gh_quoted'}\"; // cgiDataNew = {user_name: 'gh_comment'}\nwindow.cgiDataNew = {user_name: 'gh_right'};",
			want:   "gh_right",
		},
		{
			name:   "expression is not a literal",
			script: `window.cgiDataNew = {user_name: 'gh_' + suffix, nested: {user_name: 'gh_wrong'}};`,
		},
		{
			name:   "unrelated object is ignored",
			script: `window.otherWidget = {user_name: 'gh_wrong'}; window.cgiDataNew = {title: 'article'};`,
		},
		{
			name:   "incomplete object is ignored",
			script: `window.cgiDataNew = {user_name: 'gh_wrong'`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstCGIDataStringField([]string{tc.script}, "user_name"); got != tc.want {
				t.Fatalf("user_name = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestArticleImportDoesNotUseUnrelatedAuthorID(t *testing.T) {
	u, _ := parseArticleImportURL("https://mp.weixin.qq.com/s?__biz=MzTest&key=secret")
	page := []byte(`<html><body>
	<script>window.cgiDataNew = {"nick_name":"公众号","user_name":"gh_account"};</script>
	<script>window.otherWidget = {"author_id":"unrelated"};</script>
	</body></html>`)
	account, _, err := parseImportedAccount(u, u, page)
	if err != nil {
		t.Fatal(err)
	}
	if account.AuthorId != "" || account.CandidateAuthorId != "gh_account" {
		t.Fatal("an unrelated page field was treated as this account's author ID")
	}
}

func TestReimportClearsOnlyMatchingUnverifiedLegacyAuthorID(t *testing.T) {
	u, _ := parseArticleImportURL("https://mp.weixin.qq.com/s?__biz=MzTest&key=session")
	page := []byte(`<html><body>
	<script>window.cgiDataNew = {"nick_name":"公众号","user_name":"gh_account"};</script>
	<script>window.otherWidget = {"author_id":"unrelated"};</script>
	</body></html>`)
	for _, verified := range []bool{false, true} {
		name := "unverified"
		if verified {
			name = "verified"
		}
		t.Run(name, func(t *testing.T) {
			oldAccounts, oldPath := accounts, mp_json_filepath
			accounts = map[string]*OfficialAccount{
				"MzTest": {Biz: "MzTest", Key: "session", AuthorId: "unrelated", AuthorIdVerified: verified},
			}
			mp_json_filepath = filepath.Join(t.TempDir(), "mp.json")
			t.Cleanup(func() { accounts, mp_json_filepath = oldAccounts, oldPath })
			account, title, err := parseImportedAccount(u, u, page)
			if err != nil {
				t.Fatal(err)
			}
			c := &OfficialAccountClient{wait_chan_map: make(map[string]chan *OfficialAccount)}
			c.finishImportedArticle(account, title, u)
			got := accounts["MzTest"]
			if verified && (got.AuthorId != "unrelated" || !got.AuthorIdVerified) {
				t.Fatal("a verified author ID was discarded")
			}
			if !verified && (got.AuthorId != "" || got.CandidateAuthorId != "gh_account") {
				t.Fatal("the legacy importer’s unrelated author ID survived reimport")
			}
		})
	}
}

func TestImportResponseDoesNotExposeArticleURLOrCredentials(t *testing.T) {
	response := importedAccountResponse(&OfficialAccount{
		Biz: "MzTest", Nickname: "测试公众号", AuthorId: "author", Key: "secret", PassTicket: "ticket", Uin: "777",
	}, "当前文章")
	for _, key := range []string{"url", "key", "pass_ticket", "uin", "appmsg_token", "refresh_uri"} {
		if _, exposed := response[key]; exposed {
			t.Fatalf("import response exposes %s", key)
		}
	}
	if response["biz"] != "MzTest" || response["nickname"] != "测试公众号" || response["title"] != "当前文章" || response["has_author_id"] != true || response["history_available"] != true {
		t.Fatalf("import response lost public fields: %+v", response)
	}
}

func TestImportedResponseUsesStableSourceIdentity(t *testing.T) {
	c := &OfficialAccountClient{}
	account := &OfficialAccount{Biz: "MzTest", Nickname: "测试公众号"}
	for _, paths := range [][2]string{
		{
			"https://mp.weixin.qq.com/s/xnRIezwPCspMWisPaq0Vwg?key=first-secret&pass_ticket=first-ticket",
			"https://mp.weixin.qq.com/s/xnRIezwPCspMWisPaq0Vwg?key=second-secret&pass_ticket=second-ticket",
		},
		{
			"https://mp.weixin.qq.com/s?__biz=MzTest&mid=123&idx=1&sn=article&key=first-secret",
			"https://mp.weixin.qq.com/s?sn=article&idx=1&mid=123&__biz=MzTest&key=second-secret",
		},
	} {
		firstURL, err := parseArticleImportURL(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		secondURL, err := parseArticleImportURL(paths[1])
		if err != nil {
			t.Fatal(err)
		}
		first := c.finishImportedArticle(account, "导入的文章标题", firstURL)
		second := c.finishImportedArticle(account, "导入的文章标题", secondURL)
		want := archive.ID(archive.StableURL(firstURL.String()))
		if first["source_id"] != want || second["source_id"] != want {
			t.Fatalf("source identity changed with session credentials: first=%v second=%v", first["source_id"], second["source_id"])
		}
		encoded, err := json.Marshal(first)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "first-secret") || strings.Contains(string(encoded), "first-ticket") || strings.Contains(string(encoded), "mp.weixin.qq.com") {
			t.Fatal("import response exposed a credential-bearing article URL")
		}
	}
}

func TestImportResponseRecognizesLegacyHistoryCredentials(t *testing.T) {
	legacy := importedAccountResponse(&OfficialAccount{Biz: "MzTest", Uin: "user", Key: "session"}, "当前文章")
	if legacy["history_available"] != true || legacy["history_reason"] != "" {
		t.Fatalf("legacy history path was incorrectly disabled: %+v", legacy)
	}
	author := importedAccountResponse(&OfficialAccount{Biz: "MzTest", Key: "session", CandidateAuthorId: "gh_candidate"}, "当前文章")
	if author["history_available"] != true || author["history_reason"] != "" {
		t.Fatalf("author history path was incorrectly disabled: %+v", author)
	}
	missing := importedAccountResponse(&OfficialAccount{Biz: "MzTest", Key: "session"}, "当前文章")
	if missing["history_available"] != false || missing["history_reason"] == "" {
		t.Fatalf("insufficient history credentials were accepted: %+v", missing)
	}
}

func TestPublicImportDoesNotRegisterOrRefreshAccount(t *testing.T) {
	oldAccounts, oldPath := accounts, mp_json_filepath
	accounts = map[string]*OfficialAccount{"MzExisting": {Biz: "MzExisting", Key: "old-key", IsEffective: false, UpdateTime: 1}}
	mp_json_filepath = filepath.Join(t.TempDir(), "mp.json")
	t.Cleanup(func() { accounts, mp_json_filepath = oldAccounts, oldPath })
	c := &OfficialAccountClient{wait_chan_map: make(map[string]chan *OfficialAccount)}
	public := c.finishImportedArticle(&OfficialAccount{Biz: "MzExisting", Nickname: "公开文章"}, "文章", nil)
	if public["history_available"] != false || accounts["MzExisting"].Key != "old-key" || accounts["MzExisting"].IsEffective || accounts["MzExisting"].UpdateTime != 1 {
		t.Fatalf("public link changed an existing account: response=%+v account=%+v", public, accounts["MzExisting"])
	}
	c.finishImportedArticle(&OfficialAccount{Biz: "MzPublic", Nickname: "新公开文章"}, "文章", nil)
	if accounts["MzPublic"] != nil {
		t.Fatal("public link was registered as a connected account")
	}
	credentialed := c.finishImportedArticle(&OfficialAccount{Biz: "MzConnected", Nickname: "已连接", Uin: "user", Key: "new-key"}, "文章", nil)
	if credentialed["history_available"] != true || accounts["MzConnected"] == nil || !accounts["MzConnected"].IsEffective {
		t.Fatalf("credentialed import did not preserve registration: response=%+v", credentialed)
	}
}

type importRoundTripper func(*http.Request) (*http.Response, error)

func (f importRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestArticleImportRejectsCrossSiteRedirectBeforeRequest(t *testing.T) {
	u, _ := url.Parse("https://mp.weixin.qq.com/s?__biz=MzTest&key=secret")
	requests := 0
	client := newArticleImportClient("MzTest")
	client.Transport = importRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://example.com/s?__biz=MzTest"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})
	_, err := fetchImportArticlePage(context.Background(), client, u)
	if !errors.Is(err, errImportRedirect) || requests != 1 {
		t.Fatalf("cross-site redirect followed: err=%v requests=%d", err, requests)
	}
}

func TestArticleImportFollowsOnlySameSiteArticleRedirect(t *testing.T) {
	u, _ := parseArticleImportURL("https://mp.weixin.qq.com/s/public_slug?scene=1")
	requests := 0
	client := newArticleImportClient("")
	client.Transport = importRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://mp.weixin.qq.com/s?__biz=MzTest&mid=1&idx=1"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(`<html><body><strong id="js_name">测试公众号</strong><script>var biz = "MzTest";</script></body></html>`)),
			Request:    req,
		}, nil
	})
	page, err := fetchImportArticlePage(context.Background(), client, u)
	if err != nil || requests != 2 || page.URL.Query().Get("__biz") != "MzTest" {
		t.Fatalf("same-site redirect failed: err=%v requests=%d page=%+v", err, requests, page)
	}
	account, _, err := parseImportedAccount(u, page.URL, page.Body)
	if err != nil || account.Biz != "MzTest" || account.Key != "" {
		t.Fatalf("redirected public article misparsed: account=%+v err=%v", account, err)
	}
}

func TestArticleImportKeepsOnlyCookiesEligibleForAuthorEndpoint(t *testing.T) {
	u, _ := parseArticleImportURL("https://mp.weixin.qq.com/s/short_slug")
	client := newArticleImportClient("")
	requests := 0
	client.Transport = importRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header: http.Header{
					"Location":   []string{"https://mp.weixin.qq.com/s?__biz=MzTest"},
					"Set-Cookie": []string{"redirect_session=one; Path=/; Secure; HttpOnly"},
				},
				Body:    io.NopCloser(strings.NewReader("")),
				Request: req,
			}, nil
		}
		if !strings.Contains(req.Header.Get("Cookie"), "redirect_session=one") {
			t.Fatal("same-host redirect lost its session cookie")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/html; charset=utf-8"},
				"Set-Cookie": []string{
					"author_session=two; Path=/mp; Secure; HttpOnly",
					"article_only=three; Path=/s; Secure",
					"other_domain=bad; Domain=evil.example; Path=/; Secure",
				},
			},
			Body:    io.NopCloser(strings.NewReader("<html><body>article</body></html>")),
			Request: req,
		}, nil
	})
	page, err := fetchImportArticlePage(context.Background(), client, u)
	if err != nil || requests != 2 {
		t.Fatalf("article fetch failed: err=%v requests=%d", err, requests)
	}
	for _, part := range []string{"redirect_session=one", "author_session=two"} {
		if !strings.Contains(page.Cookie, part) {
			t.Fatal("author-compatible cookie was lost")
		}
	}
	for _, part := range []string{"article_only=", "other_domain=", "Path=", "HttpOnly"} {
		if strings.Contains(page.Cookie, part) {
			t.Fatal("ineligible cookie or Set-Cookie attribute was stored")
		}
	}
}

func TestArticleImportRejectsRedirectToOtherAccount(t *testing.T) {
	u, _ := parseArticleImportURL("https://mp.weixin.qq.com/s?__biz=MzOne&key=secret")
	requests := 0
	client := newArticleImportClient("MzOne")
	client.Transport = importRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"https://mp.weixin.qq.com/s?__biz=MzTwo"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})
	_, err := fetchImportArticlePage(context.Background(), client, u)
	if !errors.Is(err, errImportRedirect) || requests != 1 {
		t.Fatalf("cross-account redirect followed: err=%v requests=%d", err, requests)
	}
}

func TestArticleImportLimitsResponseSize(t *testing.T) {
	u, _ := url.Parse("https://mp.weixin.qq.com/s?__biz=MzTest&key=secret")
	client := newArticleImportClient("MzTest")
	client.Transport = importRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxImportArticleBytes+1))),
			Request:    req,
		}, nil
	})
	_, err := fetchImportArticlePage(context.Background(), client, u)
	if !errors.Is(err, errImportPageTooLarge) {
		t.Fatalf("oversized article accepted: %v", err)
	}
}

func TestStoreAccountDoesNotMixSessions(t *testing.T) {
	oldAccounts, oldPath := accounts, mp_json_filepath
	accounts = make(map[string]*OfficialAccount)
	mp_json_filepath = filepath.Join(t.TempDir(), "mp.json")
	t.Cleanup(func() { accounts, mp_json_filepath = oldAccounts, oldPath })
	c := &OfficialAccountClient{wait_chan_map: make(map[string]chan *OfficialAccount)}
	c.storeAccount(OfficialAccount{Biz: "MzTest", Nickname: "旧名称", Uin: "777", Key: "key-1", PassTicket: "pass-1", Cookie: "old-cookie", CookieExpiration: time.Now().Add(time.Hour).Unix(), AuthorId: "old-author", AppmsgToken: "old-token"})
	updated, _ := c.storeAccount(OfficialAccount{Biz: "MzTest", Nickname: "新名称", AuthorId: "gh_author", Key: "key-2"})
	if updated.Nickname != "新名称" || updated.AuthorId != "gh_author" || updated.Key != "key-2" ||
		updated.Uin != "" || updated.PassTicket != "" || updated.Cookie != "" || updated.CookieExpiration != 0 || updated.AppmsgToken != "" {
		t.Fatal("new session inherited stale credentials")
	}
	if data, err := os.ReadFile(mp_json_filepath); err != nil || !strings.Contains(string(data), "gh_author") {
		t.Fatalf("account was not persisted: err=%v", err)
	}
	merged, _ := c.storeAccount(OfficialAccount{Biz: "MzTest", Key: "key-2", Uin: "new-user", PassTicket: "new-ticket"})
	if merged.Uin != "new-user" || merged.PassTicket != "new-ticket" || merged.AuthorId != "gh_author" {
		t.Fatal("fields from the same session were not merged")
	}
}

func TestMissingAuthorHistoryIDReturnsSentinelWithoutFetching(t *testing.T) {
	oldAccounts := accounts
	accounts = map[string]*OfficialAccount{"MzTest": {Biz: "MzTest", Key: "secret"}}
	t.Cleanup(func() { accounts = oldAccounts })
	c := &OfficialAccountClient{}
	_, err := c.fetchArticleList("MzTest", "")
	if !errors.Is(err, ErrAuthorHistoryUnavailable) {
		t.Fatalf("missing author ID returned %v", err)
	}
}
