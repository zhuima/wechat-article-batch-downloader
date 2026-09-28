package api

import (
	"bytes"
	"errors"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	officialaccountdownload "github.com/GopeedLab/gopeed/pkg/officialaccount"
	"github.com/gin-gonic/gin"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
	nethtml "golang.org/x/net/html"
	"mp_article_batch_downloader/internal/archive"
	result "mp_article_batch_downloader/internal/util"
)

const maxLocalMarkdownBytes int64 = 8 << 20
const maxLocalHTMLBytes int64 = 32 << 20
const maxLocalImageBytes int64 = 32 << 20

var errLocalArticleMissing = errors.New("本地尚无完整的文章文件")
var errLocalArticleLarge = errors.New("本地 Markdown 文件过大，无法在应用内打开")
var errLocalArticleUnknown = errors.New("文章不在该公众号的本地列表中")
var errLocalArticleInvalid = errors.New("请选择有效的公众号文章")
var localImageName = regexp.MustCompile(`^[a-f0-9]{32}\.(?:jpg|jpeg|png|gif|webp|bmp|avif)$`)

type localArticleFiles struct {
	markdown string
	images   string
}

// resolvedLocalPath checks both the configured download root and the immediate
// expected folder. A symlink to a sibling article or another account is not a
// valid substitute for one of this article's files.
func resolvedLocalPath(parent, candidate string, wantDir bool) (string, bool) {
	path, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false
	}
	path, err = filepath.Abs(path)
	if err != nil || !pathWithin(parent, path) {
		return "", false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() != wantDir {
		return "", false
	}
	if !wantDir && (!info.Mode().IsRegular() || info.Size() == 0) {
		return "", false
	}
	return path, true
}

func localScanArticle(scan archive.Scan, id string) (archive.Article, bool) {
	if !desktopArticleID.MatchString(id) {
		return archive.Article{}, false
	}
	for _, article := range scan.Articles {
		if article.ID == id {
			return article, true
		}
	}
	return archive.Article{}, false
}

func findLocalArticleFiles(downloadDir, preferredAccount string, article archive.Article) (localArticleFiles, error) {
	root, err := filepath.EvalSymlinks(downloadDir)
	if err != nil {
		return localArticleFiles{}, errLocalArticleMissing
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return localArticleFiles{}, errLocalArticleMissing
	}
	entries, err := os.ReadDir(downloadDir)
	if err != nil {
		return localArticleFiles{}, errLocalArticleMissing
	}
	// A renamed account can leave an older export folder behind. Search all
	// account folders, preferring the current nickname when it is available.
	if preferredAccount != "" {
		for i, entry := range entries {
			if entry.Name() == preferredAccount {
				entries[0], entries[i] = entries[i], entries[0]
				break
			}
		}
	}
	base := desktopSafeName(article.Title) + "-" + article.ID
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		account, ok := resolvedLocalPath(root, filepath.Join(downloadDir, entry.Name()), true)
		if !ok {
			continue
		}
		files := localArticleFiles{}
		valid := true
		for _, format := range []struct{ directory, extension string }{
			{"html", ".html"}, {"markdown", ".md"}, {"text", ".txt"},
		} {
			folder, ok := resolvedLocalPath(account, filepath.Join(account, format.directory), true)
			if !ok {
				valid = false
				break
			}
			file, ok := resolvedLocalPath(folder, filepath.Join(folder, base+format.extension), false)
			if !ok {
				valid = false
				break
			}
			if format.directory == "markdown" {
				files.markdown = file
				files.images = filepath.Join(folder, "images")
			}
		}
		if valid {
			return files, nil
		}
	}
	return localArticleFiles{}, errLocalArticleMissing
}

func readLimitedLocalFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errLocalArticleLarge
	}
	if len(data) == 0 {
		return nil, errLocalArticleMissing
	}
	return data, nil
}

// Only the complete export's exact sibling HTML can be used to repair an old
// Markdown file. The HTML is parsed for table data, never rendered directly.
func localReaderHTMLSibling(markdownPath string) (string, bool) {
	if filepath.Ext(markdownPath) != ".md" || filepath.Base(filepath.Dir(markdownPath)) != "markdown" {
		return "", false
	}
	if info, err := os.Lstat(markdownPath); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	account := filepath.Dir(filepath.Dir(markdownPath))
	htmlCandidate := filepath.Join(account, "html")
	if info, err := os.Lstat(htmlCandidate); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	htmlDir, ok := resolvedLocalPath(account, htmlCandidate, true)
	if !ok {
		return "", false
	}
	base := strings.TrimSuffix(filepath.Base(markdownPath), ".md")
	if base == "" {
		return "", false
	}
	candidate := filepath.Join(htmlDir, base+".html")
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxLocalHTMLBytes {
		return "", false
	}
	return resolvedLocalPath(htmlDir, candidate, false)
}

type localTableRepair struct {
	start int
	end   int
	value string
}

// Earlier exports flattened HTML table cells into one Markdown paragraph. Use
// only unambiguous exact matches, keeping all other saved text untouched.
func restoreLocalMarkdownTables(markdown []byte, htmlContent string) []byte {
	tables, err := officialaccountdownload.ExtractMarkdownTablesFromHTML(htmlContent)
	if err != nil || len(tables) == 0 {
		return markdown
	}
	original := string(markdown)
	repairs := make([]localTableRepair, 0, len(tables))
	for _, table := range tables {
		old, replacement := table.FlattenedMarkdown, table.Markdown
		if old == "" || replacement == "" || old == replacement || strings.Contains(original, replacement) || strings.Count(original, old) != 1 {
			continue
		}
		start := strings.Index(original, old)
		repairs = append(repairs, localTableRepair{start: start, end: start + len(old), value: replacement})
	}
	if len(repairs) == 0 {
		return markdown
	}
	sort.Slice(repairs, func(i, j int) bool { return repairs[i].start < repairs[j].start })
	ambiguous := make([]bool, len(repairs))
	for index := 1; index < len(repairs); index++ {
		if repairs[index].start < repairs[index-1].end {
			ambiguous[index-1], ambiguous[index] = true, true
		}
	}
	var repaired strings.Builder
	last := 0
	for index, repair := range repairs {
		// Two extracted tables claiming the same bytes cannot be attributed
		// safely; leave that span as the saved Markdown contained it.
		if ambiguous[index] {
			continue
		}
		repaired.WriteString(original[last:repair.start])
		repaired.WriteString("\n\n")
		repaired.WriteString(repair.value)
		repaired.WriteString("\n\n")
		last = repair.end
	}
	if last == 0 {
		return markdown
	}
	repaired.WriteString(original[last:])
	return []byte(repaired.String())
}

// The old exporter joined adjacent WeChat <section> paragraphs and the <code>
// lines into single Markdown lines. A complete export also has a same-named
// HTML sibling with that structure intact. Rebuild only when both renderings
// contain exactly the same non-whitespace text and neither has images to
// remap. Keep the saved Markdown and its assets as the fallback.
func restoreLocalMarkdownStructure(markdown []byte, htmlContent string) []byte {
	articleBody, ok := localReaderArticleBody(htmlContent)
	if !ok {
		return markdown
	}
	oldText, oldImages, err := localReaderVisibleText(markdown)
	if err != nil || len(oldImages) > 0 {
		return markdown
	}
	rebuilt, err := officialaccountdownload.ConvertArticleHTMLToMarkdown(articleBody)
	if err != nil || len(rebuilt) == 0 || len(rebuilt) > int(maxLocalMarkdownBytes) ||
		strings.Count(rebuilt, "\n\n") <= bytes.Count(markdown, []byte("\n\n")) {
		return markdown
	}
	newText, newImages, err := localReaderVisibleText([]byte(rebuilt))
	if err != nil || len(newImages) > 0 || oldText == "" || oldText != newText {
		return markdown
	}
	return []byte(rebuilt)
}

func localReaderArticleBody(content string) (string, bool) {
	document, err := nethtml.Parse(strings.NewReader(content))
	if err != nil {
		return "", false
	}
	var article *nethtml.Node
	var matches int
	var visit func(*nethtml.Node)
	visit = func(node *nethtml.Node) {
		if node.Type == nethtml.ElementNode {
			for _, attribute := range node.Attr {
				if attribute.Key == "class" && localReaderHasClass(attribute.Val, "rich_media_content") {
					article = node
					matches++
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	if matches != 1 || article.FirstChild == nil || localReaderContainsImage(article) {
		return "", false
	}
	var body bytes.Buffer
	for child := article.FirstChild; child != nil; child = child.NextSibling {
		if err := nethtml.Render(&body, child); err != nil {
			return "", false
		}
	}
	return body.String(), true
}

func localReaderHasClass(classes, wanted string) bool {
	for _, class := range strings.Fields(classes) {
		if class == wanted {
			return true
		}
	}
	return false
}

func localReaderContainsImage(node *nethtml.Node) bool {
	if node.Type == nethtml.ElementNode && node.Data == "img" {
		return true
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if localReaderContainsImage(child) {
			return true
		}
	}
	return false
}

func localReaderVisibleText(markdown []byte) (string, map[string]struct{}, error) {
	rendered, images, err := renderLocalMarkdown(markdown, "")
	if err != nil {
		return "", nil, err
	}
	document, err := nethtml.Parse(strings.NewReader(rendered))
	if err != nil {
		return "", nil, err
	}
	var visible strings.Builder
	var visit func(*nethtml.Node)
	visit = func(node *nethtml.Node) {
		if node.Type == nethtml.TextNode {
			for _, character := range node.Data {
				if !unicode.IsSpace(character) {
					visible.WriteRune(character)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	return visible.String(), images, nil
}

func safeReaderLink(destination []byte) bool {
	raw := strings.TrimSpace(string(destination))
	if raw == "" || strings.ContainsAny(raw, "\x00\r\n\t\\") {
		return false
	}
	if strings.HasPrefix(raw, "#") {
		return true
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Host == "" && parsed.Scheme != "mailto" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https", "http":
		return parsed.Host != ""
	case "mailto":
		return parsed.Opaque != "" || parsed.Path != ""
	default:
		return false
	}
}

func sanitizeLocalMarkdownNode(parent ast.Node, source []byte, imageBase string, images map[string]struct{}) {
	for node := parent.FirstChild(); node != nil; {
		next := node.NextSibling()
		switch value := node.(type) {
		case *ast.Image:
			destination := string(value.Destination)
			name := strings.TrimPrefix(destination, "images/")
			if !strings.HasPrefix(destination, "images/") || !localImageName.MatchString(name) {
				parent.ReplaceChild(parent, node, ast.NewString(node.Text(source)))
				break
			}
			images[name] = struct{}{}
			value.Destination = []byte(imageBase + url.QueryEscape(name))
		case *ast.Link:
			if !safeReaderLink(value.Destination) {
				parent.ReplaceChild(parent, node, ast.NewString(node.Text(source)))
				break
			}
			if strings.HasPrefix(strings.ToLower(string(value.Destination)), "http") {
				value.SetAttributeString("rel", []byte("noopener noreferrer"))
				value.SetAttributeString("target", []byte("_blank"))
			}
			sanitizeLocalMarkdownNode(node, source, imageBase, images)
		case *ast.AutoLink:
			if !safeReaderLink(value.URL(source)) {
				parent.ReplaceChild(parent, node, ast.NewString(value.Label(source)))
			}
		case *ast.RawHTML, *ast.HTMLBlock:
			parent.RemoveChild(parent, node)
		default:
			sanitizeLocalMarkdownNode(node, source, imageBase, images)
		}
		node = next
	}
}

// Goldmark's built-in auto heading IDs discard non-ASCII letters. Keep Unicode
// letters here so links to headings in Chinese articles resolve in the reader.
func localHeadingSlug(title []byte) string {
	var slug strings.Builder
	separator := false
	for _, character := range string(title) {
		switch {
		case unicode.IsLetter(character) || unicode.IsNumber(character) || unicode.IsMark(character):
			if separator && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			separator = false
			slug.WriteRune(unicode.ToLower(character))
		case unicode.IsSpace(character) || character == '-' || character == '_':
			separator = true
		}
	}
	if slug.Len() == 0 {
		return "heading"
	}
	return slug.String()
}

func addLocalHeadingIDs(parent ast.Node, source []byte, used map[string]struct{}) {
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		if heading, ok := node.(*ast.Heading); ok {
			base := localHeadingSlug(heading.Text(source))
			id := base
			for suffix := 1; ; suffix++ {
				if _, exists := used[id]; !exists {
					break
				}
				id = base + "-" + strconv.Itoa(suffix)
			}
			used[id] = struct{}{}
			heading.SetAttributeString("id", []byte(id))
		}
		addLocalHeadingIDs(node, source, used)
	}
}

func renderLocalMarkdown(markdown []byte, imageBase string) (string, map[string]struct{}, error) {
	engine := goldmark.New(goldmark.WithExtensions(extension.GFM))
	document := engine.Parser().Parse(text.NewReader(markdown))
	images := make(map[string]struct{})
	sanitizeLocalMarkdownNode(document, markdown, imageBase, images)
	addLocalHeadingIDs(document, markdown, make(map[string]struct{}))
	var rendered bytes.Buffer
	if err := engine.Renderer().Render(&rendered, markdown, document); err != nil {
		return "", nil, err
	}
	return rendered.String(), images, nil
}

type localArticleView struct {
	article   archive.Article
	files     localArticleFiles
	markdown  []byte
	html      string
	images    map[string]struct{}
	baseURL   string
	sourceURL string
}

// Expose only a public article link. The non-credential chksm and scene
// parameters can be needed to open a history article, whereas older scan and
// export records may contain short-lived WeChat session credentials.
func safeReaderArticleURL(raw string) string {
	publicURL := archive.DownloadURL(raw)
	if publicURL == "" {
		return ""
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "mp.weixin.qq.com") ||
		u.User != nil || u.Opaque != "" || u.EscapedPath() != u.Path {
		return ""
	}
	if u.Path == "/s" || u.Path == "/s/" {
		query := u.Query()
		if query.Get("__biz") != "" && query.Get("mid") != "" && query.Get("idx") != "" {
			return u.String()
		}
		return ""
	}
	if u.Path == "/mp/appmsg/show" {
		query := u.Query()
		if query.Get("__biz") != "" && query.Get("appmsgid") != "" && query.Get("itemidx") != "" {
			return u.String()
		}
		return ""
	}
	if strings.HasPrefix(u.Path, "/s/") {
		slug := strings.TrimPrefix(u.Path, "/s/")
		if len(slug) > 0 && len(slug) <= 256 && strings.IndexFunc(slug, func(r rune) bool {
			return !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || r == '-')
		}) == -1 {
			return u.String()
		}
	}
	return ""
}

// Older local files can predate the export journal. A completed task is a
// fallback only when its verified reader target is this exact local document.
func (c *APIClient) localReaderTaskURL(biz, id string) string {
	if c.downloader == nil {
		return ""
	}
	resolver := newTaskLocalArticleResolver(c)
	var found string
	for _, task := range c.downloader.GetTasks() {
		article := resolver.resolve(task)
		if article == nil || article.Biz != biz || article.ID != id || task.Meta == nil || task.Meta.Req == nil {
			continue
		}
		u, _ := taskWeChatURL(task.Meta.Req.URL)
		if u == nil {
			continue
		}
		candidate := safeReaderArticleURL(u.String())
		if candidate == "" || !strings.HasPrefix(id, "local_") && archive.ID(archive.StableURL(candidate)) != id {
			continue
		}
		if found != "" && found != candidate {
			return ""
		}
		found = candidate
	}
	return found
}

func (c *APIClient) loadLocalArticle(biz, id string) (localArticleView, error) {
	biz, id = strings.TrimSpace(biz), strings.TrimSpace(id)
	if biz == "" || !desktopArticleID.MatchString(id) {
		return localArticleView{}, errLocalArticleInvalid
	}
	article, found := localScanArticle(c.archive.Get(biz), id)
	var files localArticleFiles
	sourceURL := ""
	if found {
		sourceURL = safeReaderArticleURL(article.URL)
		if sourceURL != "" && archive.ID(archive.StableURL(sourceURL)) != article.ID {
			sourceURL = ""
		}
		preferredAccount := ""
		if nickname := accountNicknameOnDisk(c.cfg.RootDir, biz); nickname != "" {
			preferredAccount = desktopSafeName(nickname)
		}
		var err error
		files, err = findLocalArticleFiles(c.cfg.DownloadDir, preferredAccount, article)
		if err != nil {
			return localArticleView{}, errLocalArticleMissing
		}
	} else if localDocumentID.MatchString(id) {
		for _, entry := range c.localLibraryEntries(biz) {
			if entry.article.ID == id {
				article, files, found = entry.article, entry.files, true
				sourceURL = entry.sourceURL
				break
			}
		}
		if !found {
			return localArticleView{}, errLocalArticleUnknown
		}
	} else {
		return localArticleView{}, errLocalArticleUnknown
	}
	markdown, err := readLimitedLocalFile(files.markdown, maxLocalMarkdownBytes)
	if err != nil {
		if errors.Is(err, errLocalArticleLarge) {
			return localArticleView{}, errLocalArticleLarge
		}
		return localArticleView{}, errLocalArticleMissing
	}
	if htmlPath, ok := localReaderHTMLSibling(files.markdown); ok {
		if htmlContent, err := readLimitedLocalFile(htmlPath, maxLocalHTMLBytes); err == nil {
			markdown = restoreLocalMarkdownStructure(markdown, string(htmlContent))
			markdown = restoreLocalMarkdownTables(markdown, string(htmlContent))
		}
	}
	imageBase := "/api/desktop/article/image?biz=" + url.QueryEscape(biz) + "&id=" + url.QueryEscape(id) + "&name="
	html, images, err := renderLocalMarkdown(markdown, imageBase)
	if err != nil {
		return localArticleView{}, err
	}
	if sourceURL == "" {
		sourceURL = c.localReaderTaskURL(biz, id)
	}
	article.Title = stdhtml.UnescapeString(article.Title)
	return localArticleView{article: article, files: files, markdown: markdown, html: html, images: images, baseURL: imageBase, sourceURL: sourceURL}, nil
}

func (c *APIClient) handleLocalArticle(ctx *gin.Context) {
	view, err := c.loadLocalArticle(ctx.Query("biz"), ctx.Query("id"))
	if err != nil {
		switch {
		case errors.Is(err, errLocalArticleInvalid):
			result.Err(ctx, 400, err.Error())
		case errors.Is(err, errLocalArticleLarge):
			result.Err(ctx, 413, err.Error())
		case errors.Is(err, errLocalArticleMissing), errors.Is(err, errLocalArticleUnknown):
			result.Err(ctx, 404, err.Error())
		default:
			result.Err(ctx, 500, "本地文章解析失败")
		}
		return
	}
	ctx.Header("Cache-Control", "private, no-store")
	result.Ok(ctx, gin.H{
		"id":              view.article.ID,
		"title":           view.article.Title,
		"published":       view.article.Published,
		"markdown":        string(view.markdown),
		"html":            view.html,
		"images_base_url": view.baseURL,
		"url":             view.sourceURL,
	})
}

func (c *APIClient) handleLocalArticleImage(ctx *gin.Context) {
	name := ctx.Query("name")
	if !localImageName.MatchString(name) {
		ctx.Status(http.StatusNotFound)
		return
	}
	view, err := c.loadLocalArticle(ctx.Query("biz"), ctx.Query("id"))
	if err != nil {
		ctx.Status(http.StatusNotFound)
		return
	}
	if _, referenced := view.images[name]; !referenced {
		ctx.Status(http.StatusNotFound)
		return
	}
	markdownDir := filepath.Dir(view.files.markdown)
	imageDir, valid := resolvedLocalPath(markdownDir, view.files.images, true)
	if !valid {
		ctx.Status(http.StatusNotFound)
		return
	}
	imagePath, valid := resolvedLocalPath(imageDir, filepath.Join(imageDir, name), false)
	if !valid {
		ctx.Status(http.StatusNotFound)
		return
	}
	data, err := readLimitedLocalFile(imagePath, maxLocalImageBytes)
	if err != nil {
		ctx.Status(http.StatusNotFound)
		return
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "image/avif":
	default:
		ctx.Status(http.StatusNotFound)
		return
	}
	ctx.Header("Cache-Control", "private, no-store")
	ctx.Header("X-Content-Type-Options", "nosniff")
	ctx.Data(http.StatusOK, mime, data)
}
