package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"mp_article_batch_downloader/internal/archive"
	result "mp_article_batch_downloader/internal/util"
)

const (
	wereadScopeNote     = "微信读书收录，可能不是公众号完整历史"
	wereadMaxBodyBytes  = 2 << 20
	wereadMaxURLBytes   = 8192
	wereadMaxTitleBytes = 1024
	wereadMaxArticles   = 500
	wereadMaxReviews    = 500
	wereadMaxOffset     = 10_000_000
)

var (
	wereadBizPattern  = regexp.MustCompile(`^M[A-Za-z0-9+/_=-]{5,255}$`)
	wereadBookPattern = regexp.MustCompile(`^MP_WXS_[0-9]{1,32}$`)
	wereadSlugPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	wereadFilePattern = regexp.MustCompile(`^[0-9a-f]{16}\.json$`)
	errWereadConflict = errors.New("微信读书收录页与已保存进度不一致，请读取当前断点后继续")
	errWereadStorage  = errors.New("保存或读取微信读书收录进度失败，原有进度已保留")
)

type wereadPageArticle struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	Published int64  `json:"published"`
}

type wereadPageRequest struct {
	Biz      string              `json:"biz"`
	BookID   string              `json:"book_id"`
	Offset   *int                `json:"offset"`
	Reviews  *int                `json:"reviews"`
	Articles []wereadPageArticle `json:"articles"`
}

// The file format is intentionally separate from RootDir/scans. It also binds
// all pages for one account to one WeRead book and records the last page hash
// so a retry after a lost HTTP response cannot advance the cursor twice.
type wereadStoredScan struct {
	Scan           archive.Scan `json:"scan"`
	BookID         string       `json:"book_id"`
	ReachedEnd     bool         `json:"reached_end"`
	LastPageOffset int          `json:"last_page_offset"`
	LastPageHash   string       `json:"last_page_hash"`
}

type wereadScanResponse struct {
	archive.Scan
	BookID     string `json:"book_id,omitempty"`
	ReachedEnd bool   `json:"reached_end"`
	ScopeNote  string `json:"scope_note"`
}

type wereadScanSummary struct {
	archive.ScanSummary
	BookID     string `json:"book_id"`
	ReachedEnd bool   `json:"reached_end"`
	ScopeNote  string `json:"scope_note"`
}

func (s wereadStoredScan) response() wereadScanResponse {
	return wereadScanResponse{Scan: s.Scan, BookID: s.BookID, ReachedEnd: s.ReachedEnd, ScopeNote: wereadScopeNote}
}

func idleWereadScan(biz string) wereadStoredScan {
	return wereadStoredScan{Scan: archive.Scan{
		Options: archive.Options{Biz: biz, Mode: "all"}, Status: "idle", Source: "weread", Articles: []archive.Article{},
	}}
}

func validWereadBiz(biz string) bool { return wereadBizPattern.MatchString(biz) }

func wereadArticle(input wereadPageArticle, biz string) (archive.Article, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" || len(title) > wereadMaxTitleBytes || !utf8.ValidString(title) ||
		strings.IndexFunc(title, unicode.IsControl) >= 0 {
		return archive.Article{}, errors.New("微信读书文章标题无效")
	}
	if input.Published < 0 || input.Published > time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).Unix() {
		return archive.Article{}, errors.New("微信读书文章发布时间无效")
	}
	raw := strings.TrimSpace(input.URL)
	if raw == "" || len(raw) > wereadMaxURLBytes {
		return archive.Article{}, errors.New("微信读书文章链接无效")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "mp.weixin.qq.com") ||
		u.User != nil || u.Opaque != "" || u.Fragment != "" || u.EscapedPath() != u.Path {
		return archive.Article{}, errors.New("仅接受 mp.weixin.qq.com 的 HTTPS 文章链接")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return archive.Article{}, errors.New("微信读书文章链接参数无效")
	}
	for _, key := range []string{"__biz", "mid", "idx", "sn"} {
		if len(query[key]) > 1 {
			return archive.Article{}, errors.New("微信读书文章链接参数重复")
		}
	}
	if articleBiz := query.Get("__biz"); articleBiz != "" && articleBiz != biz {
		return archive.Article{}, errors.New("微信读书文章链接与公众号不一致")
	}
	switch {
	case u.Path == "/s" || u.Path == "/s/":
		if query.Get("__biz") == "" || query.Get("mid") == "" || query.Get("idx") == "" || query.Get("sn") == "" {
			return archive.Article{}, errors.New("微信读书文章链接缺少文章标识")
		}
	case strings.HasPrefix(u.Path, "/s/") && wereadSlugPattern.MatchString(strings.TrimPrefix(u.Path, "/s/")):
		// Modern /s/<id> links often have no __biz. The native reader binds
		// them to this account through the validated WeRead book ID.
	default:
		return archive.Article{}, errors.New("微信读书文章链接路径无效")
	}
	stable := archive.StableURL(raw)
	clean := archive.DownloadURL(raw)
	if stable == "" || clean == "" || len(clean) > wereadMaxURLBytes {
		return archive.Article{}, errors.New("微信读书文章链接无法归档")
	}
	return archive.Article{ID: archive.ID(stable), Title: title, URL: clean, Published: input.Published}, nil
}

func normalizeWereadPage(input wereadPageRequest) ([]archive.Article, string, error) {
	if !validWereadBiz(input.Biz) || !wereadBookPattern.MatchString(input.BookID) ||
		input.Offset == nil || input.Reviews == nil || *input.Offset < 0 || *input.Offset > wereadMaxOffset ||
		*input.Reviews < 0 || *input.Reviews > wereadMaxReviews || *input.Reviews > wereadMaxOffset-*input.Offset ||
		len(input.Articles) > wereadMaxArticles || *input.Reviews == 0 && len(input.Articles) != 0 {
		return nil, "", errors.New("微信读书收录页参数无效")
	}
	articles := make([]archive.Article, 0, len(input.Articles))
	seen := make(map[string]bool, len(input.Articles))
	for _, item := range input.Articles {
		article, err := wereadArticle(item, input.Biz)
		if err != nil {
			return nil, "", err
		}
		if !seen[article.ID] {
			seen[article.ID] = true
			articles = append(articles, article)
		}
	}
	page := struct {
		BookID   string            `json:"book_id"`
		Offset   int               `json:"offset"`
		Reviews  int               `json:"reviews"`
		Articles []archive.Article `json:"articles"`
	}{input.BookID, *input.Offset, *input.Reviews, articles}
	encoded, _ := json.Marshal(page)
	hash := sha256.Sum256(encoded)
	return articles, hex.EncodeToString(hash[:]), nil
}

func (c *APIClient) wereadDir() string { return filepath.Join(c.cfg.RootDir, "weread-scans") }

func (c *APIClient) readWereadScan(biz string) (wereadStoredScan, error) {
	if c.cfg == nil || c.cfg.RootDir == "" {
		return wereadStoredScan{}, errWereadStorage
	}
	path := filepath.Join(c.wereadDir(), archive.ID(biz)+".json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return idleWereadScan(biz), nil
	}
	if err != nil {
		return wereadStoredScan{}, errWereadStorage
	}
	var stored wereadStoredScan
	if len(data) > 64<<20 || json.Unmarshal(data, &stored) != nil || stored.Scan.Options.Biz != biz ||
		stored.Scan.Source != "weread" || !wereadBookPattern.MatchString(stored.BookID) ||
		stored.Scan.Offset < 0 || stored.Scan.Offset > wereadMaxOffset {
		return wereadStoredScan{}, errWereadStorage
	}
	if stored.Scan.Articles == nil {
		stored.Scan.Articles = []archive.Article{}
	}
	return stored, nil
}

func (c *APIClient) saveWereadScan(stored wereadStoredScan) error {
	dir := c.wereadDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errWereadStorage
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return errWereadStorage
	}
	file, err := os.CreateTemp(dir, ".page-*.tmp")
	if err != nil {
		return errWereadStorage
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errWereadStorage
	}
	if err := os.Rename(file.Name(), filepath.Join(dir, archive.ID(stored.Scan.Options.Biz)+".json")); err != nil {
		return errWereadStorage
	}
	return nil
}

func (c *APIClient) importWereadPage(input wereadPageRequest) (wereadScanResponse, error) {
	articles, pageHash, err := normalizeWereadPage(input)
	if err != nil {
		return wereadScanResponse{}, err
	}
	c.wereadMu.Lock()
	defer c.wereadMu.Unlock()
	stored, err := c.readWereadScan(input.Biz)
	if err != nil {
		return wereadScanResponse{}, err
	}
	if stored.BookID != "" && stored.BookID != input.BookID {
		return stored.response(), errWereadConflict
	}
	if stored.LastPageHash != "" && stored.LastPageOffset == *input.Offset && stored.LastPageHash == pageHash {
		return stored.response(), nil
	}
	if stored.ReachedEnd || *input.Offset != stored.Scan.Offset {
		return stored.response(), errWereadConflict
	}
	next := stored
	next.BookID = input.BookID
	next.Scan.Pages++
	next.Scan.Offset = *input.Offset + *input.Reviews
	next.Scan.Updated = time.Now().UnixMilli()
	next.LastPageOffset = *input.Offset
	next.LastPageHash = pageHash
	seen := make(map[string]bool, len(next.Scan.Articles)+len(articles))
	for _, article := range next.Scan.Articles {
		seen[article.ID] = true
	}
	for _, article := range articles {
		if !seen[article.ID] {
			seen[article.ID] = true
			next.Scan.Articles = append(next.Scan.Articles, article)
		}
	}
	if *input.Reviews == 0 {
		next.ReachedEnd = true
		next.Scan.Status = "partial"
		next.Scan.ErrorCode = "publisher_history_unverified"
		next.Scan.Message = fmt.Sprintf("%s。已到收录列表末页，保存 %d 篇文章。", wereadScopeNote, len(next.Scan.Articles))
	} else {
		next.Scan.Status = "paused"
		next.Scan.Message = fmt.Sprintf("%s。已保存 %d 篇文章，下页从 offset %d 继续。", wereadScopeNote, len(next.Scan.Articles), next.Scan.Offset)
	}
	if err := c.saveWereadScan(next); err != nil {
		return stored.response(), err
	}
	return next.response(), nil
}

func (c *APIClient) handleWereadPage(ctx *gin.Context) {
	if origin := ctx.GetHeader("Origin"); origin != "" && origin != "http://"+ctx.Request.Host {
		ctx.Status(http.StatusForbidden)
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, wereadMaxBodyBytes)
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	var input wereadPageRequest
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		result.Err(ctx, 400, "微信读书收录页数据无效")
		return
	}
	scan, err := c.importWereadPage(input)
	if err == nil {
		result.Ok(ctx, scan)
		return
	}
	if errors.Is(err, errWereadConflict) || errors.Is(err, errWereadStorage) {
		code := 409
		if errors.Is(err, errWereadStorage) {
			code = 500
		}
		ctx.JSON(http.StatusOK, result.Response{Code: code, Msg: err.Error(), Data: scan})
		return
	}
	result.Err(ctx, 400, err.Error())
}

func (c *APIClient) handleWereadScan(ctx *gin.Context) {
	biz := strings.TrimSpace(ctx.Query("biz"))
	if !validWereadBiz(biz) {
		result.Err(ctx, 400, "公众号标识无效")
		return
	}
	c.wereadMu.Lock()
	stored, err := c.readWereadScan(biz)
	c.wereadMu.Unlock()
	if err != nil {
		result.Err(ctx, 500, errWereadStorage.Error())
		return
	}
	result.Ok(ctx, stored.response())
}

func (c *APIClient) handleWereadScanSummaries(ctx *gin.Context) {
	if c.cfg == nil || c.cfg.RootDir == "" {
		result.Err(ctx, 500, errWereadStorage.Error())
		return
	}
	c.wereadMu.Lock()
	defer c.wereadMu.Unlock()
	entries, err := os.ReadDir(c.wereadDir())
	if errors.Is(err, os.ErrNotExist) {
		result.Ok(ctx, map[string]wereadScanSummary{})
		return
	}
	if err != nil {
		result.Err(ctx, 500, errWereadStorage.Error())
		return
	}
	summaries := make(map[string]wereadScanSummary)
	for _, entry := range entries {
		if !wereadFilePattern.MatchString(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(c.wereadDir(), entry.Name()))
		if err != nil || len(data) > 64<<20 {
			result.Err(ctx, 500, errWereadStorage.Error())
			return
		}
		var stored wereadStoredScan
		if json.Unmarshal(data, &stored) != nil || !validWereadBiz(stored.Scan.Options.Biz) ||
			entry.Name() != archive.ID(stored.Scan.Options.Biz)+".json" ||
			stored.Scan.Source != "weread" || !wereadBookPattern.MatchString(stored.BookID) {
			result.Err(ctx, 500, errWereadStorage.Error())
			return
		}
		summaries[stored.Scan.Options.Biz] = wereadScanSummary{
			ScanSummary: archive.ScanSummary{
				Status: stored.Scan.Status, Source: stored.Scan.Source, ErrorCode: stored.Scan.ErrorCode,
				Pages: stored.Scan.Pages, ArticleCount: len(stored.Scan.Articles),
			},
			BookID: stored.BookID, ReachedEnd: stored.ReachedEnd, ScopeNote: wereadScopeNote,
		}
	}
	result.Ok(ctx, summaries)
}
