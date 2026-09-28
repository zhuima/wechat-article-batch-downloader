package officialaccount

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strings"
)

// CanRecoverAuthorIDFromStoredArticle reports whether a saved article can be
// used to look for the author identifier that older imports missed. It makes
// no network request and never returns the credential-bearing article URL.
func (c *OfficialAccountClient) CanRecoverAuthorIDFromStoredArticle(biz string) bool {
	acct_mu.RLock()
	acct := accounts[biz]
	var key, refreshURI string
	if acct != nil && acct.Biz == biz && acct.AuthorId == "" && acct.CandidateAuthorId == "" {
		key, refreshURI = acct.Key, acct.RefreshUri
	}
	acct_mu.RUnlock()
	_, ok := storedArticleRecoveryURL(refreshURI, biz, key)
	return ok
}

// RecoverAuthorIDFromStoredArticle revisits a previously imported article to
// recover an author ID or candidate username. The history endpoint still has
// to validate the recovered identity against its returned articles.
func (c *OfficialAccountClient) RecoverAuthorIDFromStoredArticle(biz string) (bool, error) {
	return c.recoverAuthorIDFromStoredArticle(context.Background(), biz, nil)
}

// The transport parameter is a test seam. The production call uses a fresh
// client with the same redirect limits as an ordinary article import.
func (c *OfficialAccountClient) recoverAuthorIDFromStoredArticle(ctx context.Context, biz string, transport http.RoundTripper) (bool, error) {
	return c.recoverAuthorIDFromStoredArticleWithPolicy(ctx, biz, transport, false)
}

// A rejected username candidate may be replaced only by a different explicit
// author ID from the saved article. The same candidate must not be retried.
func (c *OfficialAccountClient) recoverExplicitAuthorIDAfterCandidateFailure(ctx context.Context, biz string, transport http.RoundTripper) (bool, error) {
	return c.recoverAuthorIDFromStoredArticleWithPolicy(ctx, biz, transport, true)
}

func (c *OfficialAccountClient) recoverAuthorIDFromStoredArticleWithPolicy(ctx context.Context, biz string, transport http.RoundTripper, explicitOnly bool) (bool, error) {
	acct_mu.RLock()
	acct := accounts[biz]
	var key, refreshURI, candidate string
	if acct != nil && acct.Biz == biz && acct.AuthorId == "" &&
		(explicitOnly && acct.CandidateAuthorId != "" || !explicitOnly && acct.CandidateAuthorId == "") {
		key, refreshURI, candidate = acct.Key, acct.RefreshUri, acct.CandidateAuthorId
	}
	acct_mu.RUnlock()
	articleURL, ok := storedArticleRecoveryURL(refreshURI, biz, key)
	if !ok {
		return false, nil
	}

	client := newArticleImportClient(biz)
	if transport != nil {
		client.Transport = transport
	}
	page, err := fetchImportArticlePage(ctx, client, articleURL)
	if err != nil {
		// net/http errors may contain the complete request URL and its key.
		return false, ErrHistoryNetworkFailure
	}
	if wechatVerificationTitle.Match(page.Body) || bytes.Contains(page.Body, []byte("wappoc_appmsgcaptcha")) {
		return false, ErrHistoryVerificationRequired
	}
	scripts, _, _, err := articlePageFields(page.Body)
	if err != nil || firstScriptBiz(scripts) != biz {
		return false, ErrHistoryInvalidResponse
	}
	parsed, _, err := parseImportedAccount(articleURL, page.URL, page.Body)
	if err != nil || parsed.Biz != biz || parsed.Key != key {
		return false, ErrHistoryInvalidResponse
	}
	if explicitOnly && (parsed.AuthorId == "" || parsed.AuthorId == candidate) {
		return false, nil
	}
	if !explicitOnly && parsed.AuthorId == "" && parsed.CandidateAuthorId == "" {
		return false, nil
	}

	acct_mu.Lock()
	current := accounts[biz]
	if current == nil || current.Biz != biz || current.Key != key || current.RefreshUri != refreshURI ||
		current.AuthorId != "" || current.CandidateAuthorId != candidate {
		acct_mu.Unlock()
		return false, nil
	}
	updated := *current
	updated.AuthorId = parsed.AuthorId
	if !explicitOnly {
		updated.CandidateAuthorId = parsed.CandidateAuthorId
	}
	updated.AuthorIdVerified = false
	accounts[biz] = &updated
	acct_mu.Unlock()
	save_accounts()
	return true, nil
}

// Build a request from the saved account identity and the current key. Older
// stored URLs may contain unrelated query parameters, so only article identity
// fields are copied before adding the key from the same account snapshot.
func storedArticleRecoveryURL(refreshURI, biz, key string) (*url.URL, bool) {
	if strings.TrimSpace(biz) == "" || strings.TrimSpace(key) == "" || strings.TrimSpace(refreshURI) == "" {
		return nil, false
	}
	savedURL, err := parseArticleImportURL(refreshURI)
	if err != nil || savedURL.Query().Get("__biz") != biz {
		return nil, false
	}
	query := url.Values{"__biz": {biz}, "key": {key}}
	for _, field := range []string{"mid", "idx", "sn"} {
		if value := savedURL.Query().Get(field); value != "" {
			query.Set(field, value)
		}
	}
	return &url.URL{Scheme: "https", Host: "mp.weixin.qq.com", Path: savedURL.Path, RawQuery: query.Encode()}, true
}
