package officialaccount

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	officialaccountdownload "github.com/GopeedLab/gopeed/pkg/officialaccount"
	"github.com/gin-gonic/gin"
	xhtml "golang.org/x/net/html"

	"mp_article_batch_downloader/internal/archive"
	result "mp_article_batch_downloader/internal/util"
)

const (
	maxImportRequestBytes = 16 << 10
	maxImportArticleBytes = 12 << 20
	maxImportURLBytes     = 8192
)

var errImportRedirect = errors.New("article redirect is not allowed")

var articleImportSlug = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
var articleBizValue = regexp.MustCompile(`^M[A-Za-z0-9+/=_-]{5,255}$`)

// HandleImportURL reads a WeChat article URL. Public share links support a
// single download; links with session credentials can also register an account.
// The API router restricts this handler to a loopback connection.
func (c *OfficialAccountClient) HandleImportURL(ctx *gin.Context) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxImportRequestBytes)
	var body struct {
		URL string `json:"url"`
	}
	if err := ctx.ShouldBindJSON(&body); err != nil {
		result.Err(ctx, 400, "请输入有效的文章链接")
		return
	}
	articleURL, err := parseArticleImportURL(body.URL)
	if err != nil {
		result.Err(ctx, 400, "仅支持 mp.weixin.qq.com 的 HTTPS 文章链接")
		return
	}
	page, err := fetchImportArticlePage(ctx.Request.Context(), newArticleImportClient(articleURL.Query().Get("__biz")), articleURL)
	if err != nil {
		// HTTP errors can contain the credential-bearing URL, so never return or log them verbatim.
		switch {
		case errors.Is(err, errImportRedirect):
			result.Err(ctx, 400, "文章跳转到不支持的地址")
		case errors.Is(err, errImportPageTooLarge):
			result.Err(ctx, 400, "文章页面过大，无法导入")
		case errors.Is(err, errImportInvalidPage):
			result.Err(ctx, 400, "微信未返回可识别的文章页面")
		default:
			result.Err(ctx, 502, "无法读取微信文章，请检查链接或稍后重试")
		}
		return
	}
	if !officialaccountdownload.IsDownloadableArticleHTML(page.Body) {
		result.Err(ctx, 400, "微信未返回可下载的文章正文。请确认文章能正常打开，再从电脑微信复制新链接")
		return
	}
	account, title, err := parseImportedAccount(articleURL, page.URL, page.Body)
	if err != nil {
		result.Err(ctx, 400, err.Error())
		return
	}
	if account.Key != "" && page.Cookie != "" {
		account.Cookie = page.Cookie
		account.CookieExpiration = time.Now().Add(24 * time.Hour).Unix()
	}
	result.Ok(ctx, c.finishImportedArticle(account, title, articleURL))
}

func (c *OfficialAccountClient) finishImportedArticle(account *OfficialAccount, title string, articleURL *url.URL) gin.H {
	var response gin.H
	if account.Biz == "" || account.Key == "" {
		// A public article does not refresh an existing WeChat session and must
		// not create a sidebar account marked as recently connected.
		response = importedAccountResponse(account, title)
	} else {
		stored, _ := c.storeAccount(*account)
		response = importedAccountResponse(stored, title)
	}
	if articleURL != nil {
		// Match the local library's source identity without returning the copied
		// article URL, which can carry WeChat session credentials.
		if stable := archive.StableURL(articleURL.String()); stable != "" {
			response["source_id"] = archive.ID(stable)
		}
	}
	return response
}

func importedAccountResponse(account *OfficialAccount, title string) gin.H {
	hasLegacyCredentials := account.Biz != "" && account.Uin != "" && account.Key != ""
	hasAuthorCredentials := account.Biz != "" && account.Key != "" &&
		(account.AuthorId != "" || account.CandidateAuthorId != "")
	historyAvailable := hasLegacyCredentials || hasAuthorCredentials
	historyReason := ""
	switch {
	case account.Biz == "":
		historyReason = "微信文章页面未提供公众号标识，只能下载当前文章"
	case account.Key == "":
		historyReason = "公开分享链接未包含微信会话凭证，只能下载当前文章"
	case !historyAvailable:
		historyReason = "链接未提供旧历史接口所需的 uin 或作者入口标识，只能下载当前文章"
	}
	return gin.H{
		"biz":                      account.Biz,
		"nickname":                 account.Nickname,
		"title":                    title,
		"has_author_id":            account.AuthorId != "",
		"single_article_available": true,
		"history_available":        historyAvailable,
		"history_reason":           historyReason,
	}
}

func allowedArticleImportURL(u *url.URL) bool {
	if u == nil || len(u.String()) > maxImportURLBytes || u.Scheme != "https" || !strings.EqualFold(u.Host, "mp.weixin.qq.com") ||
		u.User != nil || u.Opaque != "" || u.EscapedPath() != u.Path {
		return false
	}
	if u.Path == "/s" || u.Path == "/s/" {
		return true
	}
	return strings.HasPrefix(u.Path, "/s/") && articleImportSlug.MatchString(strings.TrimPrefix(u.Path, "/s/"))
}

func parseArticleImportURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxImportURLBytes {
		return nil, errors.New("invalid article URL length")
	}
	u, err := url.Parse(raw)
	if err != nil || !allowedArticleImportURL(u) {
		return nil, errors.New("invalid article URL")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["__biz"]) > 1 {
		return nil, errors.New("invalid article query")
	}
	return u, nil
}

func newArticleImportClient(biz string) *http.Client {
	// Keep cookies from the article and its allowed same-host redirects. A
	// copied URL alone does not include the browser session used by WeChat's
	// author-history endpoint.
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Timeout: 20 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || !allowedArticleImportURL(req.URL) {
				return errImportRedirect
			}
			query, err := url.ParseQuery(req.URL.RawQuery)
			if err != nil || len(query["__biz"]) > 1 {
				return errImportRedirect
			}
			redirectBiz := query.Get("__biz")
			if biz != "" && redirectBiz != "" && redirectBiz != biz {
				return errImportRedirect
			}
			return nil
		},
	}
}

var (
	errImportPageTooLarge = errors.New("article page exceeds size limit")
	errImportInvalidPage  = errors.New("article response is not HTML")
)

type importedArticlePage struct {
	URL    *url.URL
	Body   []byte
	Cookie string
}

func importedArticleCookieHeader(client *http.Client, articleURL *url.URL) string {
	if client == nil || client.Jar == nil || articleURL == nil ||
		articleURL.Scheme != "https" || articleURL.Hostname() != "mp.weixin.qq.com" || articleURL.Port() != "" {
		return ""
	}
	// Preserve cookie path scope: a cookie restricted to /s must not be sent
	// later to /mp/author merely because both paths share the same host.
	authorURL := &url.URL{Scheme: "https", Host: "mp.weixin.qq.com", Path: "/mp/author"}
	var parts []string
	for _, cookie := range client.Jar.Cookies(authorURL) {
		if cookie.Name != "" {
			parts = append(parts, cookie.Name+"="+cookie.Value)
		}
	}
	return strings.Join(parts, "; ")
}

func fetchImportArticlePage(ctx context.Context, client *http.Client, articleURL *url.URL) (*importedArticlePage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, articleURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errImportRedirect) {
			return nil, errImportRedirect
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.Request == nil || resp.Request.URL == nil || !allowedArticleImportURL(resp.Request.URL) {
		return nil, errImportRedirect
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected article HTTP status %d", resp.StatusCode)
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.HasPrefix(contentType, "text/html") && !strings.HasPrefix(contentType, "application/xhtml+xml") {
		return nil, errImportInvalidPage
	}
	if resp.ContentLength > maxImportArticleBytes {
		return nil, errImportPageTooLarge
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, maxImportArticleBytes+1))
	if err != nil {
		return nil, err
	}
	if len(page) > maxImportArticleBytes {
		return nil, errImportPageTooLarge
	}
	return &importedArticlePage{
		URL:    resp.Request.URL,
		Body:   page,
		Cookie: importedArticleCookieHeader(client, resp.Request.URL),
	}, nil
}

func parseImportedAccount(articleURL, resolvedURL *url.URL, page []byte) (*OfficialAccount, string, error) {
	query := articleURL.Query()
	resolvedQuery := resolvedURL.Query()
	if len(page) == 0 {
		return nil, "", errors.New("微信返回的文章页面为空")
	}
	scripts, nicknameFromDOM, titleFromDOM, err := articlePageFields(page)
	if err != nil {
		return nil, "", errors.New("无法解析微信文章页面")
	}
	nickname := firstScriptField(scripts, "nick_name", "nickname")
	if nickname == "" {
		nickname = nicknameFromDOM
	}
	if nickname == "" {
		return nil, "", errors.New("无法从文章识别公众号名称")
	}
	scriptBiz := firstScriptBiz(scripts)
	biz := firstNonempty(query.Get("__biz"), resolvedQuery.Get("__biz"), scriptBiz)
	for _, value := range []string{query.Get("__biz"), resolvedQuery.Get("__biz"), scriptBiz} {
		if value != "" && value != biz {
			return nil, "", errors.New("微信文章的公众号标识不一致")
		}
	}
	// The original macOS flow reads these globals from the opened article.
	// A Windows URL import has only the fetched page and its final URL. Never
	// reuse credentials from a URL that redirected, or combine conflicting
	// values from the final URL and the HTML returned for that same request.
	session := importedArticleSession(resolvedURL, scripts)
	resolvedAuthorID := resolvedQuery.Get("author_id")
	if articleURL.String() == resolvedURL.String() {
		resolvedAuthorID = firstNonempty(resolvedAuthorID, query.Get("author_id"))
	}
	// Match the upstream injector's source of truth. An unrelated script can
	// contain an author_id for a different article or widget; promoting that
	// field to this account's author ID sends /mp/author to the wrong author.
	pageAuthorID := firstCGIDataStringField(scripts, "authorId", "author_id")
	authorID := firstNonempty(pageAuthorID, resolvedAuthorID)
	if pageAuthorID != "" && resolvedAuthorID != "" && pageAuthorID != resolvedAuthorID {
		authorID = ""
	}
	rejectedPageAuthorID := ""
	if pageAuthorID == "" && resolvedAuthorID == "" {
		rejectedPageAuthorID = firstScriptField(scripts, "authorId", "author_id")
	}
	title := safeImportedTitle(firstNonempty(firstScriptField(scripts, "title", "msg_title"), titleFromDOM))
	refreshQuery := url.Values{}
	for _, field := range []string{"__biz", "mid", "idx", "sn"} {
		value := firstNonempty(resolvedQuery.Get(field), query.Get(field))
		if field == "__biz" {
			value = biz
		}
		if value != "" {
			refreshQuery.Set(field, value)
		}
	}
	refreshURL := &url.URL{Scheme: "https", Host: "mp.weixin.qq.com", Path: resolvedURL.Path, RawQuery: refreshQuery.Encode()}
	return &OfficialAccount{
		Biz:                  biz,
		Nickname:             nickname,
		AvatarURL:            firstScriptField(scripts, "round_head_img", "hd_head_img"),
		AuthorId:             authorID,
		RejectedPageAuthorId: rejectedPageAuthorID,
		// The account username is only a candidate. It is not necessarily a
		// WeChat author ID; the author API must confirm it belongs to this biz.
		CandidateAuthorId: firstCGIDataStringField(scripts, "user_name"),
		Uin:               session.uin,
		Key:               session.key,
		PassTicket:        session.passTicket,
		AppmsgToken:       session.appmsgToken,
		RefreshUri:        refreshURL.String(),
	}, title, nil
}

type articleImportSession struct {
	uin, key, passTicket, appmsgToken string
}

func importedArticleSession(resolvedURL *url.URL, scripts []string) articleImportSession {
	// A redirect can drop or replace session parameters. Only the final URL
	// identifies the page whose HTML we actually parsed.
	query := resolvedURL.Query()
	fromURL := articleImportSession{
		uin:         strings.TrimSpace(query.Get("uin")),
		key:         strings.TrimSpace(query.Get("key")),
		passTicket:  strings.TrimSpace(query.Get("pass_ticket")),
		appmsgToken: strings.TrimSpace(query.Get("appmsg_token")),
	}
	fromPage := articleImportSession{
		uin:         firstScriptSessionField(scripts, "uin"),
		key:         firstScriptSessionField(scripts, "key"),
		passTicket:  firstScriptSessionField(scripts, "pass_ticket"),
		appmsgToken: firstScriptSessionField(scripts, "appmsg_token"),
	}
	if sessionFieldConflicts(fromURL.uin, fromPage.uin) ||
		sessionFieldConflicts(fromURL.key, fromPage.key) ||
		sessionFieldConflicts(fromURL.passTicket, fromPage.passTicket) ||
		sessionFieldConflicts(fromURL.appmsgToken, fromPage.appmsgToken) {
		// The article can still be downloaded. The session is unsafe for a
		// historical request, so do not register it as a connected account.
		return articleImportSession{}
	}
	return articleImportSession{
		uin:         firstNonempty(fromPage.uin, fromURL.uin),
		key:         firstNonempty(fromPage.key, fromURL.key),
		passTicket:  firstNonempty(fromPage.passTicket, fromURL.passTicket),
		appmsgToken: firstNonempty(fromPage.appmsgToken, fromURL.appmsgToken),
	}
}

func sessionFieldConflicts(a, b string) bool { return a != "" && b != "" && a != b }

// Only assignments to the globals used by build_article_credentials() are
// accepted. A generic object property named "key" is not a WeChat session key.
func firstScriptSessionField(scripts []string, name string) string {
	pattern := `(?i)(?:^|[^\w$\.])(?:(?:var|let|const)\s+|window\.)` + regexp.QuoteMeta(name) + `\s*=\s*("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')`
	re := regexp.MustCompile(pattern)
	for _, script := range scripts {
		for _, match := range re.FindAllStringSubmatch(script, -1) {
			if value := unquoteArticleJSString(match[1]); value != "" {
				return value
			}
		}
	}
	return ""
}

func safeImportedTitle(raw string) string {
	var out strings.Builder
	count := 0
	for _, r := range strings.TrimSpace(raw) {
		if r < ' ' || r == 0x7f {
			continue
		}
		out.WriteRune(r)
		count++
		if count >= 200 {
			break
		}
	}
	return strings.TrimSpace(out.String())
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func articlePageFields(page []byte) ([]string, string, string, error) {
	doc, err := xhtml.Parse(strings.NewReader(string(page)))
	if err != nil {
		return nil, "", "", err
	}
	var preferredScripts, otherScripts []string
	var nickname, title string
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode {
			if node.Data == "script" && node.FirstChild != nil {
				script := node.FirstChild.Data
				if strings.Contains(script, "cgiDataNew") || strings.Contains(script, "cgiData") {
					preferredScripts = append(preferredScripts, script)
				} else {
					otherScripts = append(otherScripts, script)
				}
			}
			if nickname == "" && hasHTMLAttr(node, "id", "js_name") {
				nickname = strings.TrimSpace(htmlText(node))
			}
			if title == "" && (hasHTMLAttr(node, "id", "activity-name") || node.Data == "title") {
				title = strings.TrimSpace(htmlText(node))
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	return append(preferredScripts, otherScripts...), nickname, title, nil
}

func hasHTMLAttr(node *xhtml.Node, key, value string) bool {
	for _, attr := range node.Attr {
		if attr.Key == key && attr.Val == value {
			return true
		}
	}
	return false
}

func htmlText(node *xhtml.Node) string {
	var out strings.Builder
	var visit func(*xhtml.Node)
	visit = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			out.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return out.String()
}

func firstScriptField(scripts []string, names ...string) string {
	for _, name := range names {
		pattern := `(?i)(?:^|[^\w$])(?:window\.)?["']?` + regexp.QuoteMeta(name) + `["']?\s*(?::|=)\s*("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')`
		re := regexp.MustCompile(pattern)
		for _, script := range scripts {
			for _, match := range re.FindAllStringSubmatch(script, -1) {
				if value := unquoteArticleJSString(match[1]); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func firstCGIDataStringField(scripts []string, names ...string) string {
	for _, objectName := range []string{"cgiData", "cgiDataNew"} {
		pattern := `(?i)(?:^|[^\w$\.])(?:window\.)?` + objectName + `\s*=\s*`
		re := regexp.MustCompile(pattern)
		for _, script := range scripts {
			for _, match := range re.FindAllStringIndex(script, -1) {
				if !articleJSCodeAt(script, match[1]) {
					continue
				}
				var fields map[string]json.RawMessage
				decoder := json.NewDecoder(strings.NewReader(script[match[1]:]))
				if decoder.Decode(&fields) == nil {
					for _, name := range names {
						var value string
						if json.Unmarshal(fields[name], &value) == nil {
							if value = strings.TrimSpace(value); value != "" {
								return value
							}
						}
					}
					continue
				}
				// WeChat also emits JavaScript object literals with unquoted keys,
				// single-quoted values and trailing commas. Read only literal string
				// properties at the top level of this assigned cgiData object.
				if value := firstJSObjectStringField(script[match[1]:], names...); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

// A cgiData-looking assignment inside a string or comment is page content,
// not a page global. In particular, article HTML can quote JavaScript code.
func articleJSCodeAt(source string, offset int) bool {
	for i := 0; i < offset; {
		switch {
		case source[i] == '\'' || source[i] == '"' || source[i] == '`':
			end, ok := scanArticleJSString(source, i)
			if !ok || end > offset {
				return false
			}
			i = end
		case strings.HasPrefix(source[i:], "//"):
			end := strings.IndexByte(source[i+2:], '\n')
			if end < 0 || i+2+end >= offset {
				return false
			}
			i += end + 3
		case strings.HasPrefix(source[i:], "/*"):
			end := strings.Index(source[i+2:], "*/")
			if end < 0 || i+end+4 > offset {
				return false
			}
			i += end + 4
		default:
			i++
		}
	}
	return true
}

func firstJSObjectStringField(source string, names ...string) string {
	i := skipArticleJSTrivia(source, 0)
	if i >= len(source) || source[i] != '{' {
		return ""
	}
	i++
	fields := make(map[string]string, len(names))
	closed := false
	for i < len(source) {
		i = skipArticleJSTrivia(source, i)
		if i >= len(source) {
			break
		}
		if source[i] == '}' {
			closed = true
			break
		}
		key, next, ok := articleJSObjectKey(source, i)
		if ok {
			next = skipArticleJSTrivia(source, next)
			if next < len(source) && source[next] == ':' {
				valueStart := skipArticleJSTrivia(source, next+1)
				if valueStart < len(source) && (source[valueStart] == '\'' || source[valueStart] == '"') {
					if end, quoted := scanArticleJSString(source, valueStart); quoted {
						// A concatenation or other expression is not a literal field.
						after := skipArticleJSTrivia(source, end)
						if after < len(source) && (source[after] == ',' || source[after] == '}') {
							if value := unquoteArticleJSString(source[valueStart:end]); value != "" {
								fields[key] = value
							}
						}
					}
				}
				i = valueStart
			}
		}
		// Skip complete values, including nested objects and arrays. A
		// similarly named property inside one of them is never promoted.
		if ok && i < next {
			i = next
		}
		i = skipArticleJSValue(source, i)
		if i >= len(source) {
			break
		}
		if source[i] == '}' {
			closed = true
			break
		}
		i++ // comma
	}
	if !closed {
		return ""
	}
	for _, name := range names {
		if value := fields[name]; value != "" {
			return value
		}
	}
	return ""
}

func articleJSObjectKey(source string, i int) (string, int, bool) {
	if i >= len(source) {
		return "", i, false
	}
	if source[i] == '\'' || source[i] == '"' {
		end, ok := scanArticleJSString(source, i)
		if !ok {
			return "", i, false
		}
		return unquoteArticleJSString(source[i:end]), end, true
	}
	start := i
	for i < len(source) && (source[i] == '_' || source[i] == '$' || source[i] >= 'a' && source[i] <= 'z' || source[i] >= 'A' && source[i] <= 'Z' || i > start && source[i] >= '0' && source[i] <= '9') {
		i++
	}
	return source[start:i], i, i > start
}

func skipArticleJSTrivia(source string, i int) int {
	for i < len(source) {
		switch {
		case source[i] == ' ' || source[i] == '\t' || source[i] == '\r' || source[i] == '\n':
			i++
		case strings.HasPrefix(source[i:], "//"):
			i += 2
			for i < len(source) && source[i] != '\n' {
				i++
			}
		case strings.HasPrefix(source[i:], "/*"):
			if end := strings.Index(source[i+2:], "*/"); end >= 0 {
				i += end + 4
			} else {
				return len(source)
			}
		default:
			return i
		}
	}
	return i
}

func scanArticleJSString(source string, i int) (int, bool) {
	if i >= len(source) || source[i] != '\'' && source[i] != '"' && source[i] != '`' {
		return i, false
	}
	quote := source[i]
	for i++; i < len(source); i++ {
		if source[i] == '\\' {
			i++
			continue
		}
		if source[i] == quote {
			return i + 1, true
		}
		if quote != '`' && (source[i] == '\n' || source[i] == '\r') {
			return i, false
		}
	}
	return len(source), false
}

func skipArticleJSValue(source string, i int) int {
	var nested []byte
	previous := byte(':')
	for i < len(source) {
		if next := skipArticleJSTrivia(source, i); next != i {
			i = next
			continue
		}
		switch source[i] {
		case '\'', '"', '`':
			end, ok := scanArticleJSString(source, i)
			if !ok {
				return len(source)
			}
			i = end
			previous = 'x'
			continue
		case '/':
			if strings.ContainsRune("([{=,:!?&|;", rune(previous)) {
				if end, ok := scanArticleJSRegex(source, i); ok {
					i = end
					previous = 'x'
					continue
				}
			}
		case '{', '[', '(':
			nested = append(nested, source[i])
		case '}', ']', ')':
			if len(nested) == 0 {
				if source[i] == '}' {
					return i
				}
				return len(source)
			}
			opening := nested[len(nested)-1]
			if source[i] == '}' && opening != '{' || source[i] == ']' && opening != '[' || source[i] == ')' && opening != '(' {
				return len(source)
			}
			nested = nested[:len(nested)-1]
		case ',':
			if len(nested) == 0 {
				return i
			}
		}
		previous = source[i]
		i++
	}
	return i
}

func scanArticleJSRegex(source string, i int) (int, bool) {
	if i >= len(source) || source[i] != '/' {
		return i, false
	}
	inClass := false
	for i++; i < len(source); i++ {
		switch source[i] {
		case '\\':
			i++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				i++
				for i < len(source) && (source[i] >= 'a' && source[i] <= 'z' || source[i] >= 'A' && source[i] <= 'Z') {
					i++
				}
				return i, true
			}
		case '\r', '\n':
			return i, false
		}
	}
	return len(source), false
}

// Article pages can contain snippets such as "__biz=\" + biz + \"".
// Only a plausible literal account ID should be used as the page identity.
func firstScriptBiz(scripts []string) string {
	for _, name := range []string{"biz", "__biz"} {
		pattern := `(?i)(?:^|[^\w$])(?:window\.)?["']?` + regexp.QuoteMeta(name) + `["']?\s*(?::|=)\s*("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')`
		re := regexp.MustCompile(pattern)
		for _, script := range scripts {
			for _, match := range re.FindAllStringSubmatch(script, -1) {
				value := unquoteArticleJSString(match[1])
				if articleBizValue.MatchString(value) {
					return value
				}
			}
		}
	}
	return ""
}

func unquoteArticleJSString(quoted string) string {
	if len(quoted) < 2 {
		return ""
	}
	if quoted[0] == '\'' {
		inner := quoted[1 : len(quoted)-1]
		inner = strings.ReplaceAll(inner, `\'`, `'`)
		quoted = `"` + strings.ReplaceAll(inner, `"`, `\"`) + `"`
	}
	quoted = strings.ReplaceAll(quoted, `\/`, `/`)
	if value, err := strconv.Unquote(quoted); err == nil {
		return strings.TrimSpace(stdhtml.UnescapeString(value))
	}
	var value string
	if json.Unmarshal([]byte(quoted), &value) == nil {
		return strings.TrimSpace(stdhtml.UnescapeString(value))
	}
	return ""
}
