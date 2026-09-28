package officialaccount

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"mp_article_batch_downloader/internal/interceptor"
	"mp_article_batch_downloader/internal/interceptor/proxy"
)

var cspNonceReg = regexp.MustCompile(`'nonce-([^']+)'`)

func proxyArticlePathCategory(req *proxy.ContextReq) string {
	if req == nil || req.URL == nil {
		return "unknown"
	}
	switch req.URL.Path {
	case "/s", "/s/":
		return "article_query"
	}
	if strings.HasPrefix(req.URL.Path, "/s/") && articleImportSlug.MatchString(strings.TrimPrefix(req.URL.Path, "/s/")) {
		return "article_short"
	}
	return "other"
}

func proxyArticleCaptureResult(req *proxy.ContextReq, reason string, captured bool) bool {
	// Both values come from fixed enums. Do not log the URL, query, Cookie, or
	// body: article URLs commonly contain live WeChat session credentials.
	fmt.Printf("[mp capture] reason=%s path=%s\n", reason, proxyArticlePathCategory(req))
	return captured
}

// The proxy has the original WeChat request and response in one place. Read
// account identity here so an article can refresh history credentials even
// when injected JavaScript never executes in the desktop WeChat window.
func captureArticleAccountFromProxy(req *proxy.ContextReq, body []byte) bool {
	if req == nil || req.URL == nil || req.URL.Hostname == nil {
		return proxyArticleCaptureResult(req, "invalid_request", false)
	}
	if req.URL.Hostname() != "mp.weixin.qq.com" {
		return proxyArticleCaptureResult(req, "unsupported_host", false)
	}
	if len(body) == 0 {
		return proxyArticleCaptureResult(req, "empty_body", false)
	}
	if len(body) > maxImportArticleBytes {
		return proxyArticleCaptureResult(req, "body_too_large", false)
	}
	if wechatVerificationTitle.Match(body) || bytes.Contains(body, []byte("wappoc_appmsgcaptcha")) {
		return proxyArticleCaptureResult(req, "verification_required", false)
	}
	articleURL, err := parseArticleImportURL((&url.URL{
		Scheme: "https", Host: "mp.weixin.qq.com", Path: req.URL.Path, RawQuery: req.URL.RawQuery,
	}).String())
	if err != nil {
		return proxyArticleCaptureResult(req, "invalid_article_url", false)
	}
	scripts, _, _, err := articlePageFields(body)
	if err != nil {
		return proxyArticleCaptureResult(req, "page_parse_failed", false)
	}
	pageBiz := firstScriptBiz(scripts)
	if pageBiz == "" {
		return proxyArticleCaptureResult(req, "missing_page_biz", false)
	}
	if requestBiz := articleURL.Query().Get("__biz"); requestBiz != "" && requestBiz != pageBiz {
		return proxyArticleCaptureResult(req, "biz_mismatch", false)
	}
	// The proxy sees the opened article's original request and its HTML response
	// together. WeChat can leave an older session in the URL while publishing
	// the current session as page globals. When the page supplies a key, use
	// its session as a unit; carrying URL uin/pass_ticket into that session
	// would create credentials that never existed together.
	pageKey := firstScriptSessionField(scripts, "key")
	requestKey := articleURL.Query().Get("key")
	parseURL := articleURL
	captureReason := "captured"
	if pageKey != "" {
		identityQuery := url.Values{}
		for _, field := range []string{"__biz", "mid", "idx", "sn"} {
			if value := articleURL.Query().Get(field); value != "" {
				identityQuery.Set(field, value)
			}
		}
		// A matching key binds the URL's explicit author ID to this page
		// session. Keep only that identity field, never stale URL session
		// parameters such as uin or pass_ticket.
		if requestKey != "" && requestKey == pageKey {
			if authorID := articleURL.Query().Get("author_id"); authorID != "" {
				identityQuery.Set("author_id", authorID)
			}
		}
		parseURL = &url.URL{Scheme: "https", Host: "mp.weixin.qq.com", Path: articleURL.Path, RawQuery: identityQuery.Encode()}
		if requestKey != "" && requestKey != pageKey {
			captureReason = "captured_page_session"
		}
	}
	account, _, err := parseImportedAccount(parseURL, parseURL, body)
	if err != nil {
		return proxyArticleCaptureResult(req, "account_parse_failed", false)
	}
	if account.Biz != pageBiz {
		return proxyArticleCaptureResult(req, "biz_mismatch", false)
	}
	if account.Key == "" {
		return proxyArticleCaptureResult(req, "missing_key", false)
	}
	if req.Header != nil && req.Header.Get("Cookie") != "" {
		account.Cookie = req.Header.Get("Cookie")
		account.CookieExpiration = time.Now().Add(24 * time.Hour).Unix()
	}
	(&OfficialAccountClient{}).storeAccount(*account)
	return proxyArticleCaptureResult(req, captureReason, true)
}

func rememberAuthorID(biz, authorID string) {
	biz = strings.TrimSpace(biz)
	authorID = strings.TrimSpace(authorID)
	if biz == "" || authorID == "" {
		return
	}
	changed := false
	acct_mu.Lock()
	if acct := accounts[biz]; acct != nil && acct.AuthorId != authorID {
		acct.AuthorId = authorID
		acct.AuthorIdVerified = false
		accounts[biz] = acct
		changed = true
	}
	acct_mu.Unlock()
	if changed {
		save_accounts()
	}
}

func rememberAuthorIDForSession(biz, key, authorID string) {
	biz, key, authorID = strings.TrimSpace(biz), strings.TrimSpace(key), strings.TrimSpace(authorID)
	if biz == "" || key == "" || authorID == "" {
		return
	}
	changed := false
	acct_mu.Lock()
	if acct := accounts[biz]; acct != nil && acct.Key == key && !acct.AuthorIdVerified && acct.AuthorId != authorID {
		updated := *acct
		updated.AuthorId = authorID
		accounts[biz] = &updated
		changed = true
	}
	acct_mu.Unlock()
	if changed {
		save_accounts()
	}
}

func CreateOfficialAccountInterceptorPlugin(cfg *OfficialAccountConfig, files *interceptor.ChannelInjectedFiles) *proxy.Plugin {
	traceNativeHistory := os.Getenv("MP_ARCHIVE_NATIVE_HISTORY_TRACE") == "1"
	traceTargetBiz := strings.TrimSpace(os.Getenv("MP_ARCHIVE_NATIVE_HISTORY_TRACE_BIZ"))
	return &proxy.Plugin{
		Match: "qq.com",
		OnRequest: func(ctx proxy.Context) {
			if ctx.Req().URL.Hostname() != "mp.weixin.qq.com" {
				return
			}
			query, err := url.ParseQuery(ctx.Req().URL.RawQuery)
			if err != nil {
				return
			}
			rememberAuthorIDForSession(query.Get("__biz"), query.Get("key"), query.Get("author_id"))
		},
		OnResponse: func(ctx proxy.Context) {
			resp_content_type := strings.ToLower(ctx.GetResponseHeader("Content-Type"))
			hostname := ctx.Req().URL.Hostname()
			if traceNativeHistory && traceTargetBiz != "" && hostname == "mp.weixin.qq.com" &&
				ctx.Req().URL.Path == "/mp/profile_ext" {
				query, parseErr := url.ParseQuery(ctx.Req().URL.RawQuery)
				if parseErr == nil && query.Get("__biz") == traceTargetBiz &&
					(query.Get("action") == "home" || query.Get("action") == "getmsg") {
					body, err := ctx.GetResponseBody()
					if err == nil {
						status := 0
						if response := ctx.Res(); response != nil {
							status = response.StatusCode
						}
						if line := nativeHistoryTrace(ctx.Req(), status, resp_content_type, body, traceTargetBiz); line != "" {
							fmt.Println(line)
						}
					}
				}
			}
			if !cfg.Disabled && hostname == "mp.weixin.qq.com" && strings.Contains(resp_content_type, "text/html") {
				fmt.Printf("[mp inject] path=%s\n", proxyArticlePathCategory(ctx.Req()))
				resp_body, err := ctx.GetResponseBody()
				if err != nil {
					return
				}
				if response := ctx.Res(); response != nil && (response.StatusCode == 0 || response.StatusCode == 200) {
					captureArticleAccountFromProxy(ctx.Req(), resp_body)
				}
				variables := map[string]interface{}{}
				html := string(resp_body)
				csp := ctx.GetResponseHeader("Content-Security-Policy-Report-Only")
				script_attr := ""
				if match := cspNonceReg.FindStringSubmatch(csp); len(match) > 1 {
					script_attr = fmt.Sprintf(` nonce="%s" reportloaderror`, match[1])
				}
				inserted_scripts := ""
				if cfg.DebugShowError {
					/** 调试时显示不含敏感链接的限时提示 */
					script_error := fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSError)
					inserted_scripts += script_error
				}
				cfg_byte, _ := json.Marshal(cfg)
				script_config := fmt.Sprintf(`<script%s>var __wx_channels_config__ = %s</script>`, script_attr, string(cfg_byte))
				inserted_scripts += script_config
				variable_byte, _ := json.Marshal(variables)
				script_variable := fmt.Sprintf(`<script%s>var WXVariable = %s;</script>`, script_attr, string(variable_byte))
				inserted_scripts += script_variable
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSMitt)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSTimelessReactive)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSTimelessUtils)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSTimelessUI)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSTimelessKit)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSTimelessHeadless)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSTimelessIcons)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSTimelessProviderWeb)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSFloatingUICore)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSFloatingUIDOM)
				inserted_scripts += fmt.Sprintf(`<style>%s</style>`, files.CSSWeui)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSWeui)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSWui)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSEventBus)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSUtils)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSComponents)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSDownloader)
				inserted_scripts += fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSWechatOfficialAccount)
				if cfg.PagespyEnabled {
					/** 在线调试 */
					script_pagespy := fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSPageSpy)
					script_pagespy2 := fmt.Sprintf(`<script%s>%s</script>`, script_attr, files.JSDebug)
					inserted_scripts += script_pagespy + script_pagespy2
				}
				html = strings.Replace(html, "</body>", inserted_scripts+"</body>", 1)
				ctx.SetResponseBody(html)
				return
			}
		},
	}
}
