// Package archive owns resumable history scans, independently of the article webview.
package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrHistoryUnavailable means the opened article does not provide a usable
// account history entry point. Retrying the same article will not help.
var ErrHistoryUnavailable = errors.New("微信未提供该公众号的历史列表入口")

// HistoryFailure carries a stable error code and a deliberately safe detail.
// It never stores request URLs, cookies, or WeChat's untrusted errmsg.
type HistoryFailure struct {
	Code   string
	Detail string
}

func (e *HistoryFailure) Error() string {
	if e.Detail != "" {
		return e.Detail
	}
	return e.Code
}

type Article struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Digest    string `json:"digest"`
	Published int64  `json:"published"`
}
type Options struct {
	Biz    string `json:"biz"`
	Mode   string `json:"mode"`
	Limit  int    `json:"limit"`
	After  int64  `json:"after"`
	Before int64  `json:"before"`
	Resume bool   `json:"resume"`
}
type Scan struct {
	Options   Options   `json:"options"`
	Status    string    `json:"status"`
	Source    string    `json:"source,omitempty"`
	Message   string    `json:"message"`
	ErrorCode string    `json:"error_code,omitempty"`
	Offset    int       `json:"offset"`
	Pages     int       `json:"pages"`
	Articles  []Article `json:"articles"`
	Updated   int64     `json:"updated"`
}
type ScanSummary struct {
	Status       string `json:"status"`
	Source       string `json:"source,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	Pages        int    `json:"pages"`
	ArticleCount int    `json:"article_count"`
}
type Page struct {
	Articles  []Article
	Source    string
	More      bool
	Next      int
	ReadPages int
}
type Fetch func(string, int) (Page, error)
type Manager struct {
	mu    sync.Mutex
	scans map[string]*Scan
	stops map[string]chan struct{}
	dir   string
	fetch Fetch
	delay time.Duration
}

func New(dir string, fetch Fetch) *Manager {
	m := &Manager{scans: map[string]*Scan{}, stops: map[string]chan struct{}{}, dir: dir, fetch: fetch, delay: 800 * time.Millisecond}
	entries, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, path := range entries {
		b, e := os.ReadFile(path)
		if e != nil {
			continue
		}
		var s Scan
		if json.Unmarshal(b, &s) == nil && s.Options.Biz != "" {
			if s.Status == "running" {
				s.Status = "paused"
				s.Message = "上次读取已中断，可从断点继续"
			} else if s.Status == "error" && strings.HasPrefix(s.Message, "读取未完成：") {
				// Old versions showed low-level fetch errors directly in the UI.
				s.Status = "paused"
				s.Message = "读取进度已保存。请在微信重新打开文章后继续读取。"
			}
			m.scans[s.Options.Biz] = &s
		}
	}
	return m
}
func (m *Manager) save(s *Scan) error {
	s.Updated = time.Now().UnixMilli()
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	p := filepath.Join(m.dir, ID(s.Options.Biz)+".json")
	if err = os.WriteFile(p+".tmp", b, 0600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}
func (m *Manager) Get(biz string) Scan {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.scans[biz]
	if s == nil {
		return Scan{Status: "idle", Articles: []Article{}}
	}
	copy := *s
	copy.Articles = append([]Article{}, s.Articles...)
	return copy
}
func (m *Manager) Summaries() map[string]ScanSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	summaries := make(map[string]ScanSummary, len(m.scans))
	for biz, scan := range m.scans {
		summaries[biz] = ScanSummary{Status: scan.Status, Source: scan.Source, ErrorCode: scan.ErrorCode, Pages: scan.Pages, ArticleCount: len(scan.Articles)}
	}
	return summaries
}
func (m *Manager) Start(o Options) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if o.Biz == "" {
		return fmt.Errorf("请先在微信里打开公众号文章")
	}
	if o.Resume {
		if previous := m.scans[o.Biz]; previous != nil {
			o = previous.Options
			o.Resume = true
		}
	}
	if o.Mode != "all" && o.Mode != "recent" && o.Mode != "date" {
		return fmt.Errorf("请选择读取范围")
	}
	if o.Mode == "recent" && (o.Limit < 1 || o.Limit > 10000) {
		return fmt.Errorf("篇数须在 1–10000 之间")
	}
	if o.Mode == "date" && (o.After <= 0 || o.Before < o.After) {
		return fmt.Errorf("日期范围无效")
	}
	if len(m.stops) > 0 {
		return fmt.Errorf("已有公众号正在读取，请先暂停")
	}
	s := m.scans[o.Biz]
	if !o.Resume || s == nil {
		s = &Scan{Options: o, Articles: []Article{}}
		m.scans[o.Biz] = s
	}
	if o.Resume && s.Status == "complete" {
		return fmt.Errorf("已完成读取，请重新读取以获取新增文章")
	}
	s.Status = "running"
	s.Message = "正在读取历史文章"
	s.ErrorCode = ""
	if err := m.save(s); err != nil {
		s.Status = "error"
		return err
	}
	stop := make(chan struct{})
	m.stops[o.Biz] = stop
	go m.run(s, stop)
	return nil
}
func (m *Manager) Pause(biz string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch := m.stops[biz]; ch != nil {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.stops {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}
func (m *Manager) run(s *Scan, stop chan struct{}) {
	defer func() { m.mu.Lock(); delete(m.stops, s.Options.Biz); m.mu.Unlock() }()
	finish := func(status, code, msg string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		s.Status = status
		s.ErrorCode = code
		s.Message = msg
		_ = m.save(s)
	}
	seen := map[string]bool{}
	for _, a := range s.Articles {
		seen[a.ID] = true
	}
	mergeArticles := func(articles []Article) {
		for _, a := range articles {
			if a.ID == "" {
				a.ID = ID(a.URL)
			}
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			if s.Options.Mode == "date" && (a.Published < s.Options.After || a.Published > s.Options.Before) {
				continue
			}
			if s.Options.Mode == "recent" && len(s.Articles) >= s.Options.Limit {
				break
			}
			s.Articles = append(s.Articles, a)
		}
	}
	for {
		select {
		case <-stop:
			finish("paused", "", "已暂停，继续时从已保存的断点读取")
			return
		default:
		}
		p, err := m.fetch(s.Options.Biz, s.Offset)
		if err != nil {
			// The author cursor can fail after returning earlier, account-checked
			// pages. Save those articles before pausing; a later retry can read
			// from the beginning and deduplicate them by stable article ID.
			if len(p.Articles) > 0 {
				m.mu.Lock()
				if p.Source == "author" || p.Source == "publisher" {
					s.Source = p.Source
				}
				if p.ReadPages > s.Pages {
					s.Pages = p.ReadPages
				}
				mergeArticles(p.Articles)
				saveErr := m.save(s)
				m.mu.Unlock()
				if saveErr != nil {
					finish("error", "storage_error", "保存读取进度失败："+saveErr.Error())
					return
				}
			}
			if errors.Is(err, ErrHistoryUnavailable) {
				if len(s.Articles) > 0 {
					finish("paused", "history_unavailable", fmt.Sprintf("已保存 %d 篇文章。微信暂时没有返回后续列表，可稍后继续读取。", len(s.Articles)))
				} else {
					finish("paused", "history_unavailable", "微信未提供该公众号的历史列表入口。可下载刚导入的文章，或导入同一公众号的其他文章重试。")
				}
				return
			}
			prefix := fmt.Sprintf("已保存 %d 篇文章和读取进度。", len(s.Articles))
			var failure *HistoryFailure
			if errors.As(err, &failure) {
				switch failure.Code {
				case "candidate_unverified":
					finish("paused", failure.Code, prefix+"微信本次未返回可确认的作者文章列表，无法判断是否已读完。可稍后继续读取，或导入同公众号另一篇文章后再试。")
				case "credentials_missing":
					finish("paused", failure.Code, prefix+"已识别公众号，但公开链接没有历史访问凭证。可先下载这篇；批量读取需从电脑微信复制带会话参数的文章链接，并在客户端重新导入。")
				case "credentials_expired":
					finish("paused", failure.Code, prefix+"公众号访问凭证已过期。请从电脑微信复制新的文章链接并在客户端重新导入，再继续读取。")
				case "verification_required":
					finish("paused", failure.Code, prefix+"微信要求完成访问验证。请在电脑微信中完成验证，再复制新的文章链接导入。")
				case "network":
					reason := failure.Detail
					if reason == "" {
						reason = "网络请求失败"
					}
					finish("paused", failure.Code, prefix+"读取历史时"+reason+"。请检查网络后继续读取。")
				case "invalid_response":
					finish("paused", failure.Code, prefix+"微信返回的历史数据格式异常，请稍后继续读取；若反复出现请查看程序日志。")
				default:
					finish("paused", "remote_error", prefix+"微信历史接口返回错误，无法确认已读完。请稍后继续读取；若反复出现请查看程序日志。")
				}
				return
			}
			finish("paused", "unknown", prefix+"历史读取失败，无法确认已读完。请稍后继续读取；若反复出现请查看程序日志。")
			return
		}
		select {
		case <-stop:
			finish("paused", "", "已暂停，当前页将于继续时重新读取")
			return
		default:
		}
		m.mu.Lock()
		if p.Source == "author" || p.Source == "publisher" {
			s.Source = p.Source
		}
		pagesRead := p.ReadPages
		if pagesRead < 1 {
			pagesRead = 1
		}
		if s.Offset == 0 && pagesRead > 1 {
			// An author-history retry starts at its own cursor and can repeat
			// pages already counted on a failed attempt.
			if pagesRead > s.Pages {
				s.Pages = pagesRead
			}
		} else {
			s.Pages += pagesRead
		}
		mergeArticles(p.Articles)
		done := !p.More || s.Options.Mode == "recent" && len(s.Articles) >= s.Options.Limit
		badOffset := p.More && p.Next <= s.Offset
		if !badOffset {
			s.Offset = p.Next
		}
		s.Message = fmt.Sprintf("已读取 %d 页，找到 %d 篇文章", s.Pages, len(s.Articles))
		err = m.save(s)
		m.mu.Unlock()
		if err != nil {
			finish("error", "storage_error", "保存读取进度失败："+err.Error())
			return
		}
		if done {
			if s.Source == "author" {
				finish("partial", "publisher_history_unverified", fmt.Sprintf("已保存作者文章列表中的 %d 篇文章；公众号完整历史尚未确认。", len(s.Articles)))
				return
			}
			finish("complete", "", fmt.Sprintf("已完成所选范围的读取，共 %d 篇文章", len(s.Articles)))
			return
		}
		if badOffset {
			finish("paused", "invalid_response", "微信暂时没有返回下一页，已保存读取进度，稍后可继续读取。")
			return
		}
		select {
		case <-stop:
			finish("paused", "", "已暂停，可从断点继续")
			return
		case <-time.After(m.delay):
		}
	}
}
func ID(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:])[:16] }

// StableURL is the article identity. Keep this separate from the URL used to
// fetch the article: WeChat's author history now includes a chksm signature
// that must be retained for the article request, but can change independently
// of the article's identity.
func StableURL(raw string) string {
	u, e := url.Parse(html.UnescapeString(raw))
	if e != nil {
		return ""
	}
	if u.Hostname() != "mp.weixin.qq.com" {
		return ""
	}
	u.Scheme = "https"
	u.Fragment = ""
	q := u.Query()
	stable := url.Values{}
	keys := []string{"__biz", "mid", "idx", "sn"}
	if u.Path == "/mp/appmsg/show" {
		keys = []string{"__biz", "appmsgid", "itemidx", "sign"}
	}
	for _, k := range keys {
		if q.Get(k) != "" {
			stable.Set(k, q.Get(k))
		}
	}
	u.RawQuery = stable.Encode()
	return u.String()
}

// DownloadURL retains the non-credential parameters needed to open an article
// returned by WeChat's history API. Session credentials are deliberately never
// persisted in scan files or exposed to the desktop UI.
func DownloadURL(raw string) string {
	stable := StableURL(raw)
	if stable == "" {
		return ""
	}
	original, err := url.Parse(html.UnescapeString(raw))
	if err != nil {
		return ""
	}
	u, err := url.Parse(stable)
	if err != nil {
		return ""
	}
	if u.Path == "/s" || u.Path == "/s/" {
		q := u.Query()
		originalQuery := original.Query()
		for _, key := range []string{"chksm", "scene"} {
			if value := originalQuery.Get(key); value != "" {
				q.Set(key, value)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// RepairURLs updates only download links whose stable identity already exists
// in this scan. Every cached article must match exactly one validated source
// article before anything is changed. A newer author list may include posts
// published after the scan; those are ignored so order and selection survive.
func (m *Manager) RepairURLs(biz string, source []Article) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.scans[biz]
	if s == nil || len(s.Articles) == 0 {
		return 0, fmt.Errorf("没有可修复的历史文章，请先读取文章列表")
	}
	if s.Status == "running" {
		return 0, fmt.Errorf("正在读取文章，请等待读取结束后再更新链接")
	}
	fresh := make(map[string]string, len(source))
	for _, article := range source {
		stable := StableURL(article.URL)
		if stable == "" {
			return 0, fmt.Errorf("新文章列表包含无法识别的链接，已保留原有列表")
		}
		u, _ := url.Parse(stable)
		if u.Query().Get("__biz") != biz {
			return 0, fmt.Errorf("新文章列表与当前公众号不一致，已保留原有列表")
		}
		if article.ID != "" && article.ID != ID(stable) {
			return 0, fmt.Errorf("新文章列表的文章标识不一致，已保留原有列表")
		}
		if _, duplicate := fresh[stable]; duplicate {
			return 0, fmt.Errorf("新文章列表包含重复文章，已保留原有列表")
		}
		fresh[stable] = DownloadURL(article.URL)
	}
	updated := *s
	updated.Articles = append([]Article(nil), s.Articles...)
	repaired := 0
	for i := range updated.Articles {
		article := &updated.Articles[i]
		stable := StableURL(article.URL)
		if stable == "" || article.ID != ID(stable) {
			return 0, fmt.Errorf("已保存列表的文章标识不一致，已保留原有列表")
		}
		url, ok := fresh[stable]
		if !ok || url == "" {
			return 0, fmt.Errorf("新文章列表未覆盖全部已保存文章，已保留原有列表")
		}
		if article.URL != url {
			article.URL = url
			repaired++
		}
	}
	if repaired == 0 {
		return 0, nil
	}
	if err := m.save(&updated); err != nil {
		return 0, fmt.Errorf("保存新文章链接失败：%w", err)
	}
	m.scans[biz] = &updated
	return repaired, nil
}
func SafeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, s)
	r := []rune(strings.Trim(s, " ."))
	if len(r) > 70 {
		r = r[:70]
	}
	if len(r) == 0 {
		return "公众号"
	}
	return string(r)
}
