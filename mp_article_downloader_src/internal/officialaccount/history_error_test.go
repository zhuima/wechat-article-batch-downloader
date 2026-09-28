package officialaccount

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	result "mp_article_batch_downloader/internal/util"
)

func TestFetchMsgListRejectsPublicLinkWithoutSession(t *testing.T) {
	oldAccounts := accounts
	accounts = map[string]*OfficialAccount{
		"MzPublic": {Biz: "MzPublic", Nickname: "公开文章"},
	}
	t.Cleanup(func() { accounts = oldAccounts })
	logger := zerolog.Nop()
	c := &OfficialAccountClient{logger: &logger}
	_, err := c.FetchMsgList("MzPublic", 0)
	if !errors.Is(err, ErrHistoryCredentialsMissing) {
		t.Fatalf("missing session was not identified: %v", err)
	}
	if kind, _ := ClassifyHistoryError(err); kind != "credentials_missing" {
		t.Fatalf("unexpected failure kind %q", kind)
	}
}

func TestHistoryErrorClassificationDoesNotExposeRequestCredentials(t *testing.T) {
	secret := "https://mp.weixin.qq.com/mp/profile_ext?key=SECRET&uin=SECRET"
	cases := []struct {
		err  error
		kind string
	}{
		{newCodedError(result.CodeAccountExpired, "凭证已过期", ErrHistoryCredentialsExpired), "credentials_expired"},
		{newCodedError(result.CodeFetchMsgFailed, "网络请求失败", errors.Join(ErrHistoryNetworkFailure, fmt.Errorf("Get %s: no such host", secret))), "network"},
		{newCodedError(result.CodeDataParseFailed, "解析失败", ErrHistoryInvalidResponse), "invalid_response"},
		{ErrAuthorHistoryUnavailable, "history_unavailable"},
	}
	for _, tc := range cases {
		kind, detail := ClassifyHistoryError(tc.err)
		if kind != tc.kind || strings.Contains(detail, "SECRET") || strings.Contains(detail, "https://") {
			t.Fatalf("unsafe classification: kind=%q detail=%q", kind, detail)
		}
	}
}

func TestAuthorHistoryMayUseKeyWithoutLegacyUin(t *testing.T) {
	if !hasAuthorHistoryCredentials(&OfficialAccount{Key: "secret", AuthorId: "author"}) {
		t.Fatal("author history was incorrectly gated on legacy Uin")
	}
	if hasAuthorHistoryCredentials(&OfficialAccount{AuthorId: "author"}) {
		t.Fatal("author history should not claim to have credentials without Key")
	}
	c := authorTestClient(t, OfficialAccount{Biz: "MzTest", AuthorId: "author"}, func(*http.Request) (*http.Response, error) {
		t.Fatal("missing Key must stop before an HTTP request")
		return nil, nil
	})
	_, err := c.FetchArticleHistory("MzTest")
	if !errors.Is(err, ErrHistoryCredentialsMissing) {
		t.Fatalf("missing Key was not identified: %v", err)
	}
}

func TestAuthorHistoryUserMessageExplainsSafeFailure(t *testing.T) {
	secret := "https://mp.weixin.qq.com/mp/author?key=SECRET&uin=SECRET"
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("微信作者入口返回 HTTP 500"), "HTTP 500"},
		{fmt.Errorf("微信作者列表返回错误 -1"), "错误 -1"},
		{ErrHistoryCredentialsExpired, "失效"},
		{errors.Join(ErrAuthorHistoryUnavailable, ErrCandidateAuthorHistoryRejected), "不属于这个公众号"},
		{fmt.Errorf("Get %s: rejected", secret), "读取失败"},
	}
	for i, tc := range cases {
		message := authorHistoryUserMessage(tc.err)
		if !strings.Contains(message, tc.want) || strings.Contains(message, "SECRET") || strings.Contains(message, "https://") {
			t.Fatalf("case %d: unsafe or unhelpful author error message: %q", i, message)
		}
	}
}
