package api

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stdhtml "html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"mp_article_batch_downloader/internal/archive"
	result "mp_article_batch_downloader/internal/util"
)

// Local IDs are derived from an indexed filename, never accepted as a path.
// The index is rebuilt against the selected account's directory on each read.
var localDocumentID = regexp.MustCompile(`^local_[a-f0-9]{32}$`)
var numberedExportName = regexp.MustCompile(`^[0-9]{4,}-`)

type localLibraryEntry struct {
	article   archive.Article
	files     localArticleFiles
	hash      [32]byte
	name      string
	sourceID  string
	sourceURL string
}

type localLibraryItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Published int64  `json:"published"`
	SourceID  string `json:"source_id,omitempty"`
}

type localExportMetadata struct {
	title     string
	sourceID  string
	sourceURL string
	conflict  bool
}

// The export journal is advisory metadata, never a source of paths to open.
// Only an exact relative markdown_path for a separately verified local triad
// can supply a title or article identity.
func localLibraryExportMetadata(account string) map[string]localExportMetadata {
	metadata := make(map[string]localExportMetadata)
	path, ok := safeLocalLibraryFile(account, "style_corpus", ".jsonl")
	if !ok {
		return metadata
	}
	file, err := os.Open(path)
	if err != nil {
		return metadata
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		var record struct {
			Title        string `json:"title"`
			URL          string `json:"url"`
			MarkdownPath string `json:"markdown_path"`
		}
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Title == "" {
			continue
		}
		path := strings.ReplaceAll(record.MarkdownPath, "\\", "/")
		if !strings.HasPrefix(path, "markdown/") || strings.Contains(path, "../") || strings.Contains(path, "/./") || strings.Contains(path, "//") {
			continue
		}
		stable := safeReaderArticleURL(record.URL)
		if stable == "" {
			continue
		}
		title := []rune(strings.TrimSpace(stdhtml.UnescapeString(record.Title)))
		if len(title) == 0 {
			continue
		}
		if len(title) > 160 {
			title = title[:160]
		}
		candidate := localExportMetadata{title: string(title), sourceID: archive.ID(archive.StableURL(stable)), sourceURL: stable}
		if previous, exists := metadata[path]; exists && (previous.conflict || previous.sourceID != candidate.sourceID) {
			metadata[path] = localExportMetadata{conflict: true}
			continue
		}
		metadata[path] = candidate
	}
	return metadata
}

func localLibraryID(biz, base string) string {
	digest := sha256.Sum256([]byte(biz + "\x00" + base))
	return "local_" + hex.EncodeToString(digest[:16])
}

func localLibraryTitle(markdown []byte, fallback string) string {
	// A Markdown heading is a better display title than an export filename.
	for _, line := range strings.SplitN(strings.TrimPrefix(string(markdown), "\ufeff"), "\n", 40) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			title := []rune(stdhtml.UnescapeString(strings.TrimSpace(strings.TrimPrefix(line, "# "))))
			if len(title) > 0 {
				return string(title[:min(len(title), 160)])
			}
		}
	}
	return stdhtml.UnescapeString(fallback)
}

func localLibraryPreferredName(name string) bool {
	return !numberedExportName.MatchString(name)
}

// localOwnedAccountDir only accepts the exact nickname folder for this biz.
// Colliding nicknames are ambiguous because exports do not record a biz on disk.
func (c *APIClient) localOwnedAccountDir(biz string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(c.cfg.RootDir, "mp.json"))
	if err != nil {
		return "", false
	}
	var accounts map[string]struct {
		Nickname string `json:"nickname"`
	}
	if json.Unmarshal(data, &accounts) != nil || accounts[biz].Nickname == "" {
		return "", false
	}
	name := desktopSafeName(accounts[biz].Nickname)
	for otherBiz, account := range accounts {
		if otherBiz != biz && account.Nickname != "" && desktopSafeName(account.Nickname) == name {
			return "", false
		}
	}
	root, err := filepath.EvalSymlinks(c.cfg.DownloadDir)
	if err != nil {
		return "", false
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", false
	}
	account := filepath.Join(root, name)
	if info, err := os.Lstat(account); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	return resolvedLocalPath(root, account, true)
}

func safeLocalLibraryFormatDir(account, name string) (string, bool) {
	candidate := filepath.Join(account, name)
	if info, err := os.Lstat(candidate); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	return resolvedLocalPath(account, candidate, true)
}

func safeLocalLibraryFile(folder, base, extension string) (string, bool) {
	candidate := filepath.Join(folder, base+extension)
	if info, err := os.Lstat(candidate); err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	return resolvedLocalPath(folder, candidate, false)
}

func (c *APIClient) localLibraryEntries(biz string) []localLibraryEntry {
	entries := make([]localLibraryEntry, 0)
	account, ok := c.localOwnedAccountDir(biz)
	if !ok {
		return entries
	}
	markdownDir, ok := safeLocalLibraryFormatDir(account, "markdown")
	if !ok {
		return entries
	}
	htmlDir, ok := safeLocalLibraryFormatDir(account, "html")
	if !ok {
		return entries
	}
	textDir, ok := safeLocalLibraryFormatDir(account, "text")
	if !ok {
		return entries
	}
	metadata := localLibraryExportMetadata(account)
	scanNames := make(map[string]struct{})
	for _, article := range c.archive.Get(biz).Articles {
		if desktopArticleID.MatchString(article.ID) {
			scanNames[desktopSafeName(article.Title)+"-"+article.ID] = struct{}{}
		}
	}
	files, err := os.ReadDir(markdownDir)
	if err != nil {
		return entries
	}
	for _, file := range files {
		name := file.Name()
		if !strings.HasSuffix(name, ".md") || file.IsDir() {
			continue
		}
		base := strings.TrimSuffix(name, ".md")
		if base == "" {
			continue
		}
		if _, inScan := scanNames[base]; inScan {
			continue
		}
		markdownPath, validMarkdown := safeLocalLibraryFile(markdownDir, base, ".md")
		_, validHTML := safeLocalLibraryFile(htmlDir, base, ".html")
		_, validText := safeLocalLibraryFile(textDir, base, ".txt")
		if !validMarkdown || !validHTML || !validText {
			continue
		}
		markdown, err := readLimitedLocalFile(markdownPath, maxLocalMarkdownBytes)
		if err != nil {
			continue
		}
		fileInfo, err := os.Stat(markdownPath)
		if err != nil {
			continue
		}
		title := localLibraryTitle(markdown, base)
		sourceID := ""
		sourceURL := ""
		if record, found := metadata["markdown/"+name]; found && !record.conflict {
			title = record.title
			sourceID = record.sourceID
			sourceURL = record.sourceURL
		}
		entries = append(entries, localLibraryEntry{
			article:   archive.Article{ID: localLibraryID(biz, base), Title: title, Published: fileInfo.ModTime().Unix()},
			files:     localArticleFiles{markdown: markdownPath, images: filepath.Join(markdownDir, "images")},
			hash:      sha256.Sum256(markdown),
			name:      base,
			sourceID:  sourceID,
			sourceURL: sourceURL,
		})
	}
	// Several import paths can export the same article under different names.
	// Keep the human-readable filename, with stable ordering across restarts.
	sort.Slice(entries, func(i, j int) bool {
		if (entries[i].sourceID != "") != (entries[j].sourceID != "") {
			return entries[i].sourceID != ""
		}
		left, right := localLibraryPreferredName(entries[i].name), localLibraryPreferredName(entries[j].name)
		if left != right {
			return left
		}
		return entries[i].name < entries[j].name
	})
	unique := entries[:0]
	seen := make(map[[32]byte]map[string]struct{})
	for _, entry := range entries {
		if sources := seen[entry.hash]; sources != nil {
			if _, duplicate := sources[entry.sourceID]; duplicate || entry.sourceID == "" {
				continue
			}
		}
		if seen[entry.hash] == nil {
			seen[entry.hash] = make(map[string]struct{})
		}
		seen[entry.hash][entry.sourceID] = struct{}{}
		unique = append(unique, entry)
	}
	return unique
}

func (c *APIClient) handleLocalLibrary(ctx *gin.Context) {
	biz := strings.TrimSpace(ctx.Query("biz"))
	if biz == "" || len(biz) > 128 {
		result.Err(ctx, 400, "请选择有效的公众号")
		return
	}
	entries := c.localLibraryEntries(biz)
	items := make([]localLibraryItem, 0, len(entries))
	for _, entry := range entries {
		items = append(items, localLibraryItem{ID: entry.article.ID, Title: entry.article.Title, Published: entry.article.Published, SourceID: entry.sourceID})
	}
	ctx.Header("Cache-Control", "private, no-store")
	result.Ok(ctx, gin.H{"articles": items, "total": len(items)})
}
