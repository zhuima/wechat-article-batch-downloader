package api

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/GopeedLab/gopeed/pkg/base"
	downloadpkg "github.com/GopeedLab/gopeed/pkg/download"
	"mp_article_batch_downloader/internal/archive"
)

// A download task can offer an in-app reading link only after its files have
// been matched to the selected account's verified local document index.
type taskLocalArticle struct {
	Biz       string `json:"biz"`
	ID        string `json:"id"`
	Title     string `json:"title"`
	Published int64  `json:"published"`
}

type taskLocalAccount struct {
	path     string
	nickname string
	scan     map[string][]archive.Article
	library  map[string][]localLibraryEntry
	meta     map[string]localExportMetadata
}

type taskLocalArticleResolver struct {
	client   *APIClient
	root     string
	accounts map[string]*taskLocalAccount
	byPath   map[string]string
	loaded   map[string]bool
}

func taskLocalPathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

// Build the account ownership map once per task-list response. A nickname
// collision makes both directories ambiguous, even when a task has a biz
// label: neither account can safely claim the shared export directory.
func newTaskLocalArticleResolver(c *APIClient) *taskLocalArticleResolver {
	r := &taskLocalArticleResolver{client: c, accounts: map[string]*taskLocalAccount{}, byPath: map[string]string{}, loaded: map[string]bool{}}
	if c == nil || c.cfg == nil || c.archive == nil {
		return r
	}
	root, err := filepath.EvalSymlinks(c.cfg.DownloadDir)
	if err != nil {
		return r
	}
	r.root, err = filepath.Abs(root)
	if err != nil {
		return r
	}
	data, err := os.ReadFile(filepath.Join(c.cfg.RootDir, "mp.json"))
	if err != nil {
		return r
	}
	var accounts map[string]struct {
		Nickname string `json:"nickname"`
	}
	if json.Unmarshal(data, &accounts) != nil {
		return r
	}
	names := make(map[string]int, len(accounts))
	for _, account := range accounts {
		if account.Nickname != "" {
			names[taskLocalPathKey(desktopSafeName(account.Nickname))]++
		}
	}
	for biz, account := range accounts {
		if account.Nickname == "" {
			continue
		}
		name := desktopSafeName(account.Nickname)
		if names[taskLocalPathKey(name)] != 1 {
			continue
		}
		candidate := filepath.Join(r.root, name)
		if info, err := os.Lstat(candidate); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		path, ok := resolvedLocalPath(r.root, candidate, true)
		if !ok {
			continue
		}
		r.accounts[biz] = &taskLocalAccount{path: path, nickname: account.Nickname}
		r.byPath[taskLocalPathKey(path)] = biz
	}
	return r
}

func taskWeChatURL(raw string) (*url.URL, string) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "officialaccount://") {
		raw = raw[len("officialaccount://"):]
		if !strings.HasPrefix(strings.ToLower(raw), "https://") && !strings.HasPrefix(strings.ToLower(raw), "http://") {
			raw = "https://" + raw
		}
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Hostname(), "mp.weixin.qq.com") {
		return nil, ""
	}
	return u, archive.StableURL(raw)
}

func (r *taskLocalArticleResolver) loadAccount(biz string, account *taskLocalAccount) {
	if r.loaded[biz] {
		return
	}
	r.loaded[biz] = true
	account.scan = make(map[string][]archive.Article)
	for _, article := range r.client.archive.Get(biz).Articles {
		if desktopArticleID.MatchString(article.ID) {
			name := desktopSafeName(article.Title) + "-" + article.ID
			account.scan[name] = append(account.scan[name], article)
		}
	}
	account.library = make(map[string][]localLibraryEntry)
	for _, entry := range r.client.localLibraryEntries(biz) {
		account.library[entry.name] = append(account.library[entry.name], entry)
	}
	account.meta = localLibraryExportMetadata(account.path)
}

func (r *taskLocalArticleResolver) resolve(task *downloadpkg.Task) *taskLocalArticle {
	if r.root == "" || task == nil || task.Protocol != "officialaccount" || task.Status != base.DownloadStatusDone ||
		task.Meta == nil || task.Meta.Req == nil || task.Meta.Opts == nil {
		return nil
	}
	name := task.Meta.Opts.Name
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return nil
	}
	path, ok := resolvedLocalPath(r.root, task.Meta.Opts.Path, true)
	if !ok {
		return nil
	}
	articleURL, stable := taskWeChatURL(task.Meta.Req.URL)
	if articleURL == nil || stable == "" {
		return nil
	}
	labels := task.Meta.Req.Labels
	labelBiz := strings.TrimSpace(labels["account_biz"])
	urlBiz := articleURL.Query().Get("__biz")
	if labelBiz != "" && urlBiz != "" && labelBiz != urlBiz {
		return nil
	}
	biz := labelBiz
	if biz == "" {
		biz = urlBiz
	}
	if biz == "" {
		biz = r.byPath[taskLocalPathKey(path)]
	}
	account := r.accounts[biz]
	if account == nil || taskLocalPathKey(account.path) != taskLocalPathKey(path) {
		return nil
	}
	if labelBiz == "" && urlBiz == "" {
		if accountName := strings.TrimSpace(labels["account_name"]); accountName != "" &&
			accountName != account.nickname && desktopSafeName(accountName) != filepath.Base(account.path) {
			return nil
		}
	}
	for _, format := range []struct{ directory, extension string }{
		{"html", ".html"}, {"markdown", ".md"}, {"text", ".txt"},
	} {
		folder, ok := safeLocalLibraryFormatDir(account.path, format.directory)
		if !ok {
			return nil
		}
		file, ok := safeLocalLibraryFile(folder, name, format.extension)
		if !ok {
			return nil
		}
		if format.directory == "markdown" {
			if info, err := os.Stat(file); err != nil || info.Size() > maxLocalMarkdownBytes {
				return nil
			}
		}
	}
	r.loadAccount(biz, account)
	// History downloads use a filename ending in the immutable scan ID.
	// This must match the task's source URL when the scan has a URL.
	if scanned := account.scan[name]; len(scanned) != 0 {
		if len(scanned) != 1 {
			return nil
		}
		article := scanned[0]
		if source := archive.StableURL(article.URL); source != "" && source != stable {
			return nil
		}
		return &taskLocalArticle{Biz: biz, ID: article.ID, Title: article.Title, Published: article.Published}
	}
	// Direct imports and older exports have no scan ID. The local index verifies
	// the three files and records the exact Markdown path, so a shared title
	// cannot accidentally select another article.
	if metadata := account.meta["markdown/"+name+".md"]; metadata.conflict {
		return nil
	}
	entries := account.library[name]
	if len(entries) != 1 {
		return nil
	}
	entry := entries[0]
	if entry.sourceID != "" && entry.sourceID != archive.ID(stable) {
		return nil
	}
	return &taskLocalArticle{Biz: biz, ID: entry.article.ID, Title: entry.article.Title, Published: entry.article.Published}
}
