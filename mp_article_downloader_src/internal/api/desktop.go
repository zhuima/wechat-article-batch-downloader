package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GopeedLab/gopeed/pkg/base"
	downloadpkg "github.com/GopeedLab/gopeed/pkg/download"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"mp_article_batch_downloader/internal/archive"
	"mp_article_batch_downloader/internal/officialaccount"
	result "mp_article_batch_downloader/internal/util"
	"mp_article_batch_downloader/pkg/system"
)

type downloadBatchSummary struct {
	Path         string `json:"path"`
	BatchID      string `json:"batch_id"`
	AccountName  string `json:"account_name"`
	ArticleTitle string `json:"article_title"`
	Total        int    `json:"total"`
	Completed    int    `json:"completed"`
	Running      int    `json:"running"`
	Queued       int    `json:"queued"`
	Failed       int    `json:"failed"`
	Paused       int    `json:"paused"`
	Missing      int    `json:"missing"`
	StartedAt    int64  `json:"started_at"`
	FinishedAt   int64  `json:"finished_at"`
	Mode         string `json:"mode,omitempty"`
}

type localArticleStatus struct {
	Total       int      `json:"total"`
	Downloaded  int      `json:"downloaded"`
	ArticleIDs  []string `json:"article_ids"`
	DownloadDir string   `json:"download_dir"`
}

var desktopInvalidFilenameChars = regexp.MustCompile("[\\\\/:*?\"<>|\\x00-\\x1f\\x7f]")
var desktopReservedFilename = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(\..*)?$`)
var desktopArticleID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var desktopArticleTitleIDSuffix = regexp.MustCompile(`-[0-9a-fA-F]{16}$`)
var desktopSingleArticleRandomSuffix = regexp.MustCompile(`-[0-9a-fA-F]{8}-[0-9a-fA-F]{3}$`)

// Keep this identical to safeName in the desktop UI. These names are the
// actual on-disk names used when the UI queues an article.
func desktopSafeName(input string) string {
	value := desktopInvalidFilenameChars.ReplaceAllString(input, "_")
	value = strings.Trim(strings.TrimSpace(value), ". ")
	runes := []rune(value)
	if len(runes) > 70 {
		value = string(runes[:70])
	}
	if value == "" {
		value = "公众号"
	}
	if desktopReservedFilename.MatchString(value) {
		value = "_" + value
	}
	return value
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func regularNonemptyFileWithin(root, candidate string) bool {
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || !pathWithin(root, resolved) {
		return false
	}
	info, err := os.Stat(resolved)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func accountNicknameOnDisk(rootDir, biz string) string {
	data, err := os.ReadFile(filepath.Join(rootDir, "mp.json"))
	if err != nil {
		return ""
	}
	var accounts map[string]json.RawMessage
	if json.Unmarshal(data, &accounts) != nil {
		return ""
	}
	var account struct {
		Nickname string `json:"nickname"`
	}
	if json.Unmarshal(accounts[biz], &account) != nil {
		return ""
	}
	return account.Nickname
}

func localDownloadStatus(scan archive.Scan, rootDir, downloadDir, biz string) localArticleStatus {
	status := localArticleStatus{Total: len(scan.Articles), ArticleIDs: []string{}, DownloadDir: downloadDir}
	if nickname := accountNicknameOnDisk(rootDir, biz); nickname != "" {
		status.DownloadDir = filepath.Join(downloadDir, desktopSafeName(nickname))
	}
	root, err := filepath.EvalSymlinks(downloadDir)
	if err != nil {
		return status
	}
	entries, err := os.ReadDir(downloadDir)
	if err != nil {
		return status
	}
	type accountDir struct {
		path    string
		matches int
	}
	dirs := make([]*accountDir, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(downloadDir, entry.Name())
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || !pathWithin(root, resolved) {
			continue
		}
		valid := true
		for _, format := range []string{"html", "markdown", "text"} {
			formatDir, err := filepath.EvalSymlinks(filepath.Join(candidate, format))
			if err != nil || !pathWithin(root, formatDir) {
				valid = false
				break
			}
			info, err := os.Stat(formatDir)
			if err != nil || !info.IsDir() {
				valid = false
				break
			}
		}
		if valid {
			dirs = append(dirs, &accountDir{path: candidate})
		}
	}
	for _, article := range scan.Articles {
		if !desktopArticleID.MatchString(article.ID) {
			continue
		}
		name := desktopSafeName(article.Title) + "-" + article.ID
		for _, dir := range dirs {
			files := []string{
				filepath.Join(dir.path, "html", name+".html"),
				filepath.Join(dir.path, "markdown", name+".md"),
				filepath.Join(dir.path, "text", name+".txt"),
			}
			if regularNonemptyFileWithin(root, files[0]) && regularNonemptyFileWithin(root, files[1]) && regularNonemptyFileWithin(root, files[2]) {
				status.ArticleIDs = append(status.ArticleIDs, article.ID)
				dir.matches++
				break
			}
		}
	}
	status.Downloaded = len(status.ArticleIDs)
	if status.Downloaded > 0 {
		best := dirs[0]
		for _, dir := range dirs[1:] {
			if dir.matches > best.matches {
				best = dir
			}
		}
		status.DownloadDir = best.path
	}
	return status
}

func summarizeDownloadBatches(tasks []*downloadpkg.Task, taskErrors map[string]string) []downloadBatchSummary {
	type batchKey struct{ path, id string }
	groups := map[batchKey]*downloadBatchSummary{}
	counts := map[batchKey]int{}
	for _, task := range tasks {
		if task == nil || task.Meta == nil || task.Meta.Opts == nil {
			continue
		}
		path := filepath.Clean(task.Meta.Opts.Path)
		if path == "." || path == "" {
			continue
		}
		labels := map[string]string{}
		if task.Meta.Req != nil && task.Meta.Req.Labels != nil {
			labels = task.Meta.Req.Labels
		}
		batchID := labels["batch_id"]
		key := batchKey{path: path, id: batchID}
		summary := groups[key]
		if summary == nil {
			summary = &downloadBatchSummary{Path: path, BatchID: batchID, Mode: labels["download_mode"]}
			groups[key] = summary
		}
		if accountName := strings.TrimSpace(labels["account_name"]); accountName != "" {
			summary.AccountName = accountName
		} else if summary.AccountName == "" {
			summary.AccountName = filepath.Base(path)
		}
		if articleTitle := strings.TrimSpace(labels["article_title"]); articleTitle != "" {
			summary.ArticleTitle = articleTitle
		} else if summary.ArticleTitle == "" {
			name := strings.TrimSpace(task.Meta.Opts.Name)
			withoutID := desktopArticleTitleIDSuffix.ReplaceAllString(name, "")
			if withoutID == name && labels["batch_total"] == "1" {
				// Direct single-article imports use the first 12 characters of
				// a UUID, such as cc610f02-301, when the URL has no mid/idx.
				withoutID = desktopSingleArticleRandomSuffix.ReplaceAllString(name, "")
			}
			if withoutID != "" {
				name = withoutID
			}
			summary.ArticleTitle = name
		}
		if summary.Mode == "" && labels["download_mode"] != "" {
			summary.Mode = labels["download_mode"]
		}
		counts[key]++
		if counts[key] > summary.Total {
			summary.Total = counts[key]
		}
		if hint, err := strconv.Atoi(labels["batch_total"]); err == nil && hint > summary.Total {
			summary.Total = hint
		}
		started := task.CreatedAt.UnixMilli()
		if value, err := strconv.ParseInt(labels["batch_started_at"], 10, 64); err == nil && value > 0 {
			started = value
		}
		if summary.StartedAt == 0 || started < summary.StartedAt {
			summary.StartedAt = started
		}
		updated := task.UpdatedAt.UnixMilli()
		if updated > summary.FinishedAt {
			summary.FinishedAt = updated
		}
		switch string(task.Status) {
		case "done":
			if exists, _ := taskOutputFilesExist(task); exists {
				summary.Completed++
			} else {
				summary.Missing++
			}
		case "running":
			summary.Running++
		case "ready", "wait":
			summary.Queued++
		case "error":
			summary.Failed++
		case "pause":
			// Gopeed restores failed tasks as paused after an app restart but
			// preserves their error. Keep showing those as failures so the batch
			// result does not change merely because the app was reopened.
			if taskErrors[task.ID] != "" {
				summary.Failed++
			} else {
				summary.Paused++
			}
		}
	}
	latest := map[string]*downloadBatchSummary{}
	for _, summary := range groups {
		current := latest[summary.Path]
		if current == nil || summary.StartedAt > current.StartedAt {
			copy := *summary
			latest[summary.Path] = &copy
		}
	}
	result := make([]downloadBatchSummary, 0, len(latest))
	for _, summary := range latest {
		if summary.Total != 1 {
			summary.ArticleTitle = ""
		}
		if summary.Running+summary.Queued > 0 {
			summary.FinishedAt = 0
		}
		result = append(result, *summary)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].StartedAt > result[j].StartedAt })
	return result
}

func parseMsgListPage(r *officialaccount.OfficialMsgListResp) (archive.Page, error) {
	if r == nil {
		return archive.Page{}, fmt.Errorf("微信未返回文章列表")
	}
	p := archive.Page{Source: "publisher", More: r.HasMore != 0, Next: r.NextOffset}
	if strings.TrimSpace(r.MsgList) == "" {
		if r.MsgCount == 0 && !p.More {
			return p, nil
		}
		return archive.Page{}, fmt.Errorf("微信返回的文章列表为空，但本页仍应有文章")
	}
	type item struct {
		Title  string            `json:"title"`
		URL    string            `json:"content_url"`
		Digest string            `json:"digest"`
		Multi  []json.RawMessage `json:"multi_app_msg_item_list"`
	}
	var raw struct {
		List []struct {
			Info struct {
				Time int64 `json:"datetime"`
			} `json:"comm_msg_info"`
			Ext item `json:"app_msg_ext_info"`
		} `json:"list"`
	}
	if e := json.Unmarshal([]byte(r.MsgList), &raw); e != nil {
		return archive.Page{}, fmt.Errorf("微信返回的文章列表不完整：%w", e)
	}
	if r.MsgCount > 0 && len(raw.List) == 0 {
		return archive.Page{}, fmt.Errorf("微信标记本页有 %d 条消息，但文章列表为空", r.MsgCount)
	}
	for _, msg := range raw.List {
		items := []item{msg.Ext}
		for _, b := range msg.Ext.Multi {
			var child item
			if json.Unmarshal(b, &child) == nil {
				items = append(items, child)
			}
		}
		for _, a := range items {
			stable := archive.StableURL(a.URL)
			u := archive.DownloadURL(a.URL)
			if stable == "" || u == "" || a.Title == "" {
				continue
			}
			p.Articles = append(p.Articles, archive.Article{ID: archive.ID(stable), Title: a.Title, URL: u, Digest: a.Digest, Published: msg.Info.Time})
		}
	}
	return p, nil
}

func fetchArchivePage(fetch func(string, int) (*officialaccount.OfficialMsgListResp, error), biz string, offset int) (archive.Page, error) {
	var emptyOffset int
	var emptySeen bool
	for attempt := 0; attempt < 3; attempt++ {
		r, err := fetch(biz, offset)
		if err != nil {
			return archive.Page{}, err
		}
		page, err := parseMsgListPage(r)
		if err == nil {
			// A zero-count page might be the real end or a transient empty reply.
			// Confirm it at the same requested offset before marking a scan complete.
			if r.MsgCount == 0 && !page.More && len(page.Articles) == 0 {
				if emptySeen && r.NextOffset == emptyOffset {
					return page, nil
				}
				emptySeen = true
				emptyOffset = r.NextOffset
			} else {
				return page, nil
			}
		} else {
			emptySeen = false
		}
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * 300 * time.Millisecond)
		}
	}
	return archive.Page{}, &archive.HistoryFailure{Code: "invalid_response", Detail: "微信返回的文章列表不完整"}
}

func classifyDesktopHistoryError(err error) error {
	if err == nil || errors.Is(err, archive.ErrHistoryUnavailable) {
		return err
	}
	var failure *archive.HistoryFailure
	if errors.As(err, &failure) {
		return err
	}
	kind, detail := officialaccount.ClassifyHistoryError(err)
	if kind == "history_unavailable" {
		return archive.ErrHistoryUnavailable
	}
	return &archive.HistoryFailure{Code: kind, Detail: detail}
}

func parseAuthorHistory(history *officialaccount.ArticleHistoryResponse) (archive.Page, error) {
	if history == nil {
		return archive.Page{}, &archive.HistoryFailure{Code: "invalid_response", Detail: "微信未返回作者文章列表"}
	}
	page := archive.Page{Source: "author", ReadPages: history.Pages}
	for _, article := range history.Articles {
		stable := archive.StableURL(article.URL)
		u := archive.DownloadURL(article.URL)
		if stable == "" || u == "" || strings.TrimSpace(article.Title) == "" {
			continue
		}
		page.Articles = append(page.Articles, archive.Article{
			ID:        archive.ID(stable),
			Title:     article.Title,
			URL:       u,
			Published: article.PublishTime,
		})
	}
	return page, nil
}

// A history trace contains only fixed labels and counts. In particular, it
// must not include account IDs, signed URLs, response bodies, or raw errors.
type desktopHistoryTrace struct {
	Phase     string
	Outcome   string
	Source    string
	Pages     int
	Articles  int
	More      bool
	ErrorCode string
}

func fixedDesktopHistoryLabel(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "unknown"
}

func desktopHistoryErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, archive.ErrHistoryUnavailable) {
		return "history_unavailable"
	}
	classified := classifyDesktopHistoryError(err)
	var failure *archive.HistoryFailure
	if errors.As(classified, &failure) {
		return fixedDesktopHistoryLabel(failure.Code,
			"candidate_unverified", "credentials_expired", "credentials_missing",
			"verification_required", "network", "invalid_response", "remote_error")
	}
	return "unknown"
}

func desktopHistoryTraceFor(phase, source string, page archive.Page, err error) desktopHistoryTrace {
	outcome := "page"
	if err != nil {
		outcome = "error"
		if len(page.Articles) > 0 {
			outcome = "partial"
		}
	} else if len(page.Articles) == 0 {
		outcome = "empty"
	}
	pages := page.ReadPages
	if phase == "legacy" && err == nil {
		pages = 1
	}
	return desktopHistoryTrace{
		Phase: phase, Outcome: outcome, Source: source, Pages: pages,
		Articles: len(page.Articles), More: page.More, ErrorCode: desktopHistoryErrorCode(err),
	}
}

func logDesktopHistoryTrace(logger *zerolog.Logger, trace desktopHistoryTrace) {
	phase := fixedDesktopHistoryLabel(trace.Phase, "legacy", "author")
	outcome := fixedDesktopHistoryLabel(trace.Outcome, "page", "empty", "partial", "error", "skipped")
	source := fixedDesktopHistoryLabel(trace.Source, "publisher", "author", "none")
	code := fixedDesktopHistoryLabel(trace.ErrorCode,
		"", "candidate_unverified", "credentials_expired", "credentials_missing",
		"verification_required", "network", "invalid_response", "remote_error",
		"history_unavailable", "cursor_incompatible", "unknown")
	// The foreground PowerShell process shows this line as each scan stage ends.
	// These labels have been allowlisted above; counts are the only dynamic data.
	fmt.Printf("[mp history] phase=%s outcome=%s source=%s pages=%d articles=%d more=%t error_code=%s\n",
		phase, outcome, source, trace.Pages, trace.Articles, trace.More, code)
	if logger == nil {
		return
	}
	entry := logger.Info()
	if outcome == "error" || outcome == "partial" {
		entry = logger.Warn()
	}
	entry.Str("phase", phase).
		Str("outcome", outcome).
		Str("source_scope", source).
		Int("pages", trace.Pages).
		Int("articles", trace.Articles).
		Bool("has_more", trace.More).
		Str("error_code", code).
		Msg("archive history")
}

func fetchDesktopArchivePage(
	fetchLegacy func(string, int) (*officialaccount.OfficialMsgListResp, error),
	fetchAuthor func(string) (*officialaccount.ArticleHistoryResponse, error),
	biz string,
	offset int,
	canTryAuthor bool,
	traces ...func(desktopHistoryTrace),
) (archive.Page, error) {
	emit := func(trace desktopHistoryTrace) {
		for _, report := range traces {
			if report != nil {
				report(trace)
			}
		}
	}
	finishAuthor := func(page archive.Page, err error) (archive.Page, error) {
		emit(desktopHistoryTraceFor("author", "author", page, err))
		return page, err
	}
	page, legacyErr := fetchArchivePage(fetchLegacy, biz, offset)
	emit(desktopHistoryTraceFor("legacy", "publisher", page, legacyErr))
	// The author API starts from its own cursor, so it can only replace the
	// legacy first page. A legacy -3 may mean only profile_ext/getmsg is stale;
	// a missing Uin also need not prevent an author page backed by Key and an
	// author ID from working. The legacy endpoint can return HTML, an HTTP
	// error, or a network error for the same usable author session. Only a
	// nonempty validated author list is success.
	if offset != 0 || legacyErr == nil && (page.More || len(page.Articles) != 0) {
		if offset != 0 && legacyErr != nil {
			emit(desktopHistoryTrace{Phase: "author", Outcome: "skipped", Source: "none", ErrorCode: "cursor_incompatible"})
		}
		return page, classifyDesktopHistoryError(legacyErr)
	}
	if !canTryAuthor {
		emit(desktopHistoryTrace{Phase: "author", Outcome: "skipped", Source: "none", ErrorCode: "history_unavailable"})
		if legacyErr != nil {
			return archive.Page{}, classifyDesktopHistoryError(legacyErr)
		}
		return archive.Page{}, archive.ErrHistoryUnavailable
	}
	history, authorErr := fetchAuthor(biz)
	if authorErr != nil {
		if history != nil && len(history.Articles) > 0 {
			// The author cursor may fail only after several usable pages. Let
			// the scan persist those articles before reporting the interruption.
			if partial, parseErr := parseAuthorHistory(history); parseErr == nil && len(partial.Articles) > 0 {
				return finishAuthor(partial, classifyDesktopHistoryError(authorErr))
			}
		}
		if legacyErr != nil && errors.Is(authorErr, officialaccount.ErrAuthorHistoryUnavailable) &&
			!errors.Is(authorErr, officialaccount.ErrCandidateAuthorHistoryRejected) {
			emit(desktopHistoryTraceFor("author", "author", archive.Page{}, authorErr))
			return archive.Page{}, classifyDesktopHistoryError(legacyErr)
		}
		return finishAuthor(archive.Page{}, classifyDesktopHistoryError(authorErr))
	}
	page, authorErr = parseAuthorHistory(history)
	if authorErr != nil {
		return finishAuthor(archive.Page{}, classifyDesktopHistoryError(authorErr))
	}
	if len(page.Articles) == 0 {
		if history != nil && len(history.Articles) != 0 {
			return finishAuthor(page, &archive.HistoryFailure{Code: "invalid_response", Detail: "微信作者列表中的文章无法归档"})
		}
		if legacyErr != nil {
			emit(desktopHistoryTraceFor("author", "author", page, nil))
			return archive.Page{}, classifyDesktopHistoryError(legacyErr)
		}
		emit(desktopHistoryTraceFor("author", "author", page, nil))
		return archive.Page{}, archive.ErrHistoryUnavailable
	}
	return finishAuthor(page, nil)
}

func (c *APIClient) setupDesktop() {
	c.engine.POST("/api/desktop/bridge-probe", loopbackOnly(c.handleBridgeProbe))
	c.engine.GET("/api/desktop/bridge-probe", loopbackOnly(c.handleBridgeProbeResult))
	c.engine.POST("/api/desktop/shutdown", func(ctx *gin.Context) {
		origin := ctx.GetHeader("Origin")
		if !localRequest(ctx) || origin != "" && origin != "http://"+ctx.Request.Host {
			ctx.Status(403)
			return
		}
		if c.cfg.Shutdown == nil {
			ctx.Status(503)
			return
		}
		result.Ok(ctx, nil)
		go c.cfg.Shutdown()
	})
	c.archive = archive.New(filepath.Join(c.cfg.RootDir, "scans"), func(biz string, offset int) (archive.Page, error) {
		canTryAuthor := c.official.HasAuthorHistoryCredentials(biz) || c.official.CanRecoverAuthorIDFromStoredArticle(biz)
		fetchAuthor := func(targetBiz string) (*officialaccount.ArticleHistoryResponse, error) {
			if !c.official.HasAuthorHistoryCredentials(targetBiz) {
				if _, recoverErr := c.official.RecoverAuthorIDFromStoredArticle(targetBiz); recoverErr != nil {
					return nil, recoverErr
				}
			}
			return c.official.FetchArticleHistory(targetBiz)
		}
		page, err := fetchDesktopArchivePage(c.official.FetchMsgList, fetchAuthor, biz, offset, canTryAuthor,
			func(trace desktopHistoryTrace) { logDesktopHistoryTrace(c.logger, trace) })
		return page, err
	})
	c.engine.POST("/api/desktop/queue", func(ctx *gin.Context) {
		var items []DownloadTaskPayload
		if ctx.ShouldBindJSON(&items) != nil || len(items) > 50 {
			result.Err(ctx, 400, "每批最多 50 篇文章")
			return
		}
		created, skipped, failed, blocked := 0, 0, 0, 0
		createdIDs := make([]string, 0, len(items))
		verificationRequired := false
		for _, item := range items {
			batchID := strings.TrimSpace(item.Extra["batch_id"])
			// Serialize the verification marker with each task creation. Once a
			// batch is blocked, a later 50-item request cannot restart its queue.
			c.batchVerificationMu.Lock()
			_, isBlocked := c.blockedBatches[batchID]
			if batchID != "" && isBlocked {
				blocked++
				verificationRequired = true
				c.batchVerificationMu.Unlock()
				continue
			}
			id, err := c.createDownloadTask(item)
			c.batchVerificationMu.Unlock()
			if err == nil {
				created++
				createdIDs = append(createdIDs, id)
			} else if e, ok := err.(*taskCreateError); ok && e.code == 409 {
				skipped++
			} else {
				failed++
			}
		}
		c.batchVerificationMu.Lock()
		for _, item := range items {
			if batchID := strings.TrimSpace(item.Extra["batch_id"]); batchID != "" {
				if _, isBlocked := c.blockedBatches[batchID]; isBlocked {
					verificationRequired = true
					break
				}
			}
		}
		c.batchVerificationMu.Unlock()
		result.Ok(ctx, gin.H{"created": created, "created_ids": createdIDs, "skipped": skipped, "failed": failed, "blocked": blocked, "verification_required": verificationRequired})
	})
	c.engine.GET("/api/desktop/info", func(ctx *gin.Context) {
		result.Ok(ctx, gin.H{
			"app":                  "mp-archive-desktop",
			"version":              7,
			"download_dir":         c.cfg.DownloadDir,
			"proxy_capture_active": system.IsDesktopProxyCaptureActive(),
		})
	})
	c.engine.GET("/api/desktop/download-summary", func(ctx *gin.Context) {
		c.taskErrorMu.Lock()
		taskErrors := make(map[string]string, len(c.taskErrors))
		for id, message := range c.taskErrors {
			taskErrors[id] = message
		}
		c.taskErrorMu.Unlock()
		result.Ok(ctx, summarizeDownloadBatches(c.downloader.GetTasks(), taskErrors))
	})
	c.engine.GET("/api/desktop/scan", func(ctx *gin.Context) { result.Ok(ctx, c.archive.Get(ctx.Query("biz"))) })
	c.engine.GET("/api/desktop/scan-summaries", loopbackOnly(func(ctx *gin.Context) {
		result.Ok(ctx, c.archive.Summaries())
	}))
	c.engine.POST("/api/desktop/weread/page", loopbackOnly(c.handleWereadPage))
	c.engine.GET("/api/desktop/weread/scan", loopbackOnly(c.handleWereadScan))
	c.engine.GET("/api/desktop/weread/scan-summaries", loopbackOnly(c.handleWereadScanSummaries))
	c.engine.GET("/api/desktop/local-status", func(ctx *gin.Context) {
		biz := strings.TrimSpace(ctx.Query("biz"))
		if biz == "" {
			result.Err(ctx, 400, "请选择公众号")
			return
		}
		if source := strings.TrimSpace(ctx.Query("source")); source == "weread" {
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
			result.Ok(ctx, localDownloadStatus(stored.Scan, c.cfg.RootDir, c.cfg.DownloadDir, biz))
			return
		} else if source != "" {
			result.Err(ctx, 400, "文章列表来源无效")
			return
		}
		result.Ok(ctx, localDownloadStatus(c.archive.Get(biz), c.cfg.RootDir, c.cfg.DownloadDir, biz))
	})
	c.engine.GET("/api/desktop/local-library", loopbackOnly(c.handleLocalLibrary))
	c.engine.GET("/api/desktop/imported-status", loopbackOnly(c.handleImportedArticleStatus))
	c.engine.GET("/api/desktop/article", loopbackOnly(c.handleLocalArticle))
	c.engine.GET("/api/desktop/article/image", loopbackOnly(c.handleLocalArticleImage))
	c.engine.POST("/api/desktop/scan/repair", func(ctx *gin.Context) {
		origin := ctx.GetHeader("Origin")
		if !localRequest(ctx) || origin != "" && origin != "http://"+ctx.Request.Host {
			ctx.Status(403)
			return
		}
		var request struct {
			Biz string `json:"biz"`
		}
		if ctx.ShouldBindJSON(&request) != nil || strings.TrimSpace(request.Biz) == "" {
			result.Err(ctx, 400, "请选择要更新文章链接的公众号")
			return
		}
		if !c.official.HasAuthorHistoryCredentials(request.Biz) {
			result.Err(ctx, 400, "当前公众号没有可用的作者历史会话，请先从电脑微信导入新文章链接")
			return
		}
		history, err := c.official.FetchArticleHistory(request.Biz)
		if err != nil {
			kind, _ := officialaccount.ClassifyHistoryError(err)
			message := "微信未返回可用的文章历史，请稍后重试"
			switch kind {
			case "credentials_missing", "credentials_expired":
				message = "公众号历史会话已失效，请从电脑微信导入新文章链接"
			case "verification_required":
				message = "微信要求完成访问验证，请在电脑微信完成验证后导入新文章链接"
			case "network":
				message = "读取公众号历史时网络请求失败，请检查网络后重试"
			}
			result.Err(ctx, 400, message)
			return
		}
		page, err := parseAuthorHistory(history)
		if err != nil || len(page.Articles) == 0 {
			result.Err(ctx, 400, "微信未返回可用于更新的文章列表，已保留原有列表")
			return
		}
		repaired, err := c.archive.RepairURLs(request.Biz, page.Articles)
		if err != nil {
			result.Err(ctx, 400, err.Error())
			return
		}
		result.Ok(ctx, gin.H{"repaired": repaired, "source_total": len(page.Articles), "scan": c.archive.Get(request.Biz)})
	})
	c.engine.POST("/api/desktop/scan", func(ctx *gin.Context) {
		var o archive.Options
		if ctx.ShouldBindJSON(&o) != nil {
			result.Err(ctx, 400, "参数无效")
			return
		}
		if e := c.archive.Start(o); e != nil {
			result.Err(ctx, 400, e.Error())
			return
		}
		result.Ok(ctx, c.archive.Get(o.Biz))
	})
	c.engine.POST("/api/desktop/scan/pause", func(ctx *gin.Context) {
		var o archive.Options
		if ctx.ShouldBindJSON(&o) != nil {
			result.Err(ctx, 400, "参数无效")
			return
		}
		c.archive.Pause(o.Biz)
		result.Ok(ctx, nil)
	})
}

func (c *APIClient) loadTaskErrors() {
	c.taskErrors = map[string]string{}
	b, e := os.ReadFile(filepath.Join(c.cfg.RootDir, "task-errors.json"))
	if e == nil {
		_ = json.Unmarshal(b, &c.taskErrors)
	}
	if c.taskErrors == nil {
		c.taskErrors = map[string]string{}
	}
}
func (c *APIClient) recordTaskError(evt *downloadpkg.Event) {
	if evt.Key != downloadpkg.EventKeyError && evt.Key != downloadpkg.EventKeyStart && evt.Key != downloadpkg.EventKeyDone && evt.Key != downloadpkg.EventKeyDelete {
		return
	}
	c.taskErrorMu.Lock()
	defer c.taskErrorMu.Unlock()
	if evt.Err != nil {
		c.taskErrors[evt.Task.ID] = regexp.MustCompile(`https?://[^\s]+`).ReplaceAllString(evt.Err.Error(), "[文章地址]")
	} else {
		delete(c.taskErrors, evt.Task.ID)
	}
	b, _ := json.Marshal(c.taskErrors)
	p := filepath.Join(c.cfg.RootDir, "task-errors.json")
	if os.WriteFile(p+".tmp", b, 0600) == nil {
		_ = os.Rename(p+".tmp", p)
	}
}

func (c *APIClient) pauseBatchOnVerification(evt *downloadpkg.Event) {
	batchID := batchVerificationID(evt)
	if batchID == "" {
		return
	}
	c.batchVerificationMu.Lock()
	if c.blockedBatches == nil {
		c.blockedBatches = make(map[string]struct{})
	}
	c.blockedBatches[batchID] = struct{}{}
	c.batchVerificationMu.Unlock()

	// This listener runs before Gopeed advances the queue on an error. Pause
	// waiting tasks now; active article exporters cannot be interrupted safely.
	ids := make([]string, 0)
	for _, task := range c.downloader.GetTasks() {
		if task == nil || task.ID == evt.Task.ID || task.Meta == nil || task.Meta.Req == nil {
			continue
		}
		if task.Meta.Req.Labels["batch_id"] == batchID {
			ids = append(ids, task.ID)
		}
	}
	if len(ids) > 0 {
		_ = c.downloader.Pause(&downloadpkg.TaskFilter{
			IDs:      ids,
			Statuses: []base.Status{base.DownloadStatusWait, base.DownloadStatusReady},
		})
	}
}

func batchVerificationID(evt *downloadpkg.Event) string {
	if evt == nil || evt.Key != downloadpkg.EventKeyError || evt.Task == nil || evt.Err == nil || evt.Task.Meta == nil || evt.Task.Meta.Req == nil {
		return ""
	}
	labels := evt.Task.Meta.Req.Labels
	message := evt.Err.Error()
	if labels["batch_id"] == "" || (!strings.Contains(message, "访问验证") && !strings.Contains(message, "凭证已失效")) {
		return ""
	}
	return labels["batch_id"]
}
