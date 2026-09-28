package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/GopeedLab/gopeed/pkg/base"
	downloadpkg "github.com/GopeedLab/gopeed/pkg/download"
	"github.com/gin-gonic/gin"
	"mp_article_batch_downloader/internal/archive"
	result "mp_article_batch_downloader/internal/util"
)

var importedSourceID = regexp.MustCompile(`^[a-f0-9]{16}$`)

type importedArticleStatus struct {
	State        string            `json:"state"`
	LocalArticle *taskLocalArticle `json:"local_article,omitempty"`
}

// An import link's stable source ID is independent of any download task ID.
// In particular, a completed task can be beyond the first page of the task
// list, or its task record can have been deleted while its files remain.
func (c *APIClient) handleImportedArticleStatus(ctx *gin.Context) {
	sourceID := strings.TrimSpace(ctx.Query("source_id"))
	biz := strings.TrimSpace(ctx.Query("biz"))
	if !importedSourceID.MatchString(sourceID) || len(biz) > 128 {
		result.Err(ctx, http.StatusBadRequest, "文章标识无效")
		return
	}
	ctx.Header("Cache-Control", "private, no-store")
	state := importedArticleStatus{State: "none"}
	readerTargets := newTaskLocalArticleResolver(c)
	if c.downloader != nil {
		for _, task := range c.downloader.GetTasks() {
			if !matchesImportedSource(task, sourceID, biz, readerTargets) {
				continue
			}
			if task.Status == base.DownloadStatusDone {
				if article := readerTargets.resolve(task); article != nil {
					result.Ok(ctx, importedArticleStatus{State: "readable", LocalArticle: article})
					return
				}
				if exists, _ := taskOutputFilesExist(task); exists {
					state.State = "saved"
				}
			} else if state.State == "none" && importedTaskActive(task.Status) {
				state.State = "downloading"
			}
		}
	}
	// The export journal and history scan also cover files whose task record
	// has been removed. Both paths use the same verified triad as the reader.
	for _, candidateBiz := range c.importedStatusAccounts(biz) {
		scan := c.archive.Get(candidateBiz)
		for _, article := range scan.Articles {
			stable := archive.StableURL(article.URL)
			if article.ID != sourceID || stable == "" || archive.ID(stable) != sourceID {
				continue
			}
			preferred := ""
			if account, ok := c.localOwnedAccountDir(candidateBiz); ok {
				preferred = filepath.Base(account)
			}
			if files, err := findLocalArticleFiles(c.cfg.DownloadDir, preferred, article); err == nil {
				if _, err := readLimitedLocalFile(files.markdown, maxLocalMarkdownBytes); err != nil {
					continue
				}
				result.Ok(ctx, importedArticleStatus{State: "readable", LocalArticle: &taskLocalArticle{
					Biz: candidateBiz, ID: article.ID, Title: article.Title, Published: article.Published,
				}})
				return
			}
		}
		for _, entry := range c.localLibraryEntries(candidateBiz) {
			if entry.sourceID == sourceID {
				result.Ok(ctx, importedArticleStatus{State: "readable", LocalArticle: &taskLocalArticle{
					Biz: candidateBiz, ID: entry.article.ID, Title: entry.article.Title, Published: entry.article.Published,
				}})
				return
			}
		}
	}
	result.Ok(ctx, state)
}

func importedTaskActive(status base.Status) bool {
	switch status {
	case base.DownloadStatusReady, base.DownloadStatusWait, base.DownloadStatusRunning, base.DownloadStatusPause:
		return true
	default:
		return false
	}
}

func matchesImportedSource(task *downloadpkg.Task, sourceID, biz string, resolver *taskLocalArticleResolver) bool {
	if task == nil || task.Protocol != "officialaccount" || task.Meta == nil || task.Meta.Req == nil {
		return false
	}
	u, stable := taskWeChatURL(task.Meta.Req.URL)
	if u == nil || stable == "" || archive.ID(stable) != sourceID {
		return false
	}
	labelBiz := strings.TrimSpace(task.Meta.Req.Labels["account_biz"])
	urlBiz := u.Query().Get("__biz")
	if labelBiz != "" && urlBiz != "" && labelBiz != urlBiz {
		return false
	}
	if biz == "" {
		return true
	}
	if labelBiz != "" && labelBiz != biz || urlBiz != "" && urlBiz != biz {
		return false
	}
	if resolver.root != "" && task.Meta.Opts != nil {
		if path, ok := resolvedLocalPath(resolver.root, task.Meta.Opts.Path, true); ok {
			if owner := resolver.byPath[taskLocalPathKey(path)]; owner != "" && owner != biz {
				return false
			}
		}
	}
	return true
}

func (c *APIClient) importedStatusAccounts(biz string) []string {
	if biz != "" {
		return []string{biz}
	}
	if c.cfg == nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(c.cfg.RootDir, "mp.json"))
	if err != nil {
		return nil
	}
	var accounts map[string]json.RawMessage
	if json.Unmarshal(data, &accounts) != nil {
		return nil
	}
	ids := make([]string, 0, len(accounts))
	for id := range accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
