package officialaccount

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalizeMsgPaginationUsesForwardOffset(t *testing.T) {
	tests := []struct {
		name   string
		data   OfficialMsgListResp
		offset int
		more   int
	}{
		{name: "wechat flag is stale", data: OfficialMsgListResp{HasMore: 0, MsgCount: 10, NextOffset: 20}, offset: 10, more: 1},
		{name: "empty terminal page", data: OfficialMsgListResp{HasMore: 1, MsgCount: 0, NextOffset: 20}, offset: 20, more: 0},
		{name: "non advancing page", data: OfficialMsgListResp{HasMore: 1, MsgCount: 10, NextOffset: 20}, offset: 20, more: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalizeMsgPagination(&tt.data, tt.offset)
			if tt.data.HasMore != tt.more {
				t.Fatalf("HasMore = %d, want %d", tt.data.HasMore, tt.more)
			}
		})
	}
}

func TestMergeFromKeepsNewestAuthorID(t *testing.T) {
	acct := &OfficialAccount{AuthorId: "old"}
	acct.MergeFrom(&OfficialAccount{AuthorId: "new"})
	if acct.AuthorId != "new" {
		t.Fatalf("AuthorId = %q", acct.AuthorId)
	}
}

func TestCollectArticleHistoryUsesLastMidCursor(t *testing.T) {
	var cursors []string
	history, err := collectArticleHistory(func(cursor string) (*ArticleListResponse, error) {
		cursors = append(cursors, cursor)
		switch cursor {
		case "":
			return &ArticleListResponse{Articles: []Article{{Mid: "3", Title: "three"}, {Mid: "2", Title: "two"}}}, nil
		case "2":
			return &ArticleListResponse{Articles: []Article{{Mid: "2", Title: "duplicate"}, {Mid: "1", Title: "one"}}}, nil
		case "1":
			return &ArticleListResponse{Articles: []Article{}}, nil
		default:
			t.Fatalf("unexpected cursor %q", cursor)
			return nil, nil
		}
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cursors, []string{"", "2", "1"}) {
		t.Fatalf("cursors = %#v", cursors)
	}
	if history.Pages != 3 || len(history.Articles) != 3 {
		t.Fatalf("history = %+v", history)
	}
}

func TestCollectArticleHistoryRejectsStalledCursor(t *testing.T) {
	history, err := collectArticleHistory(func(string) (*ArticleListResponse, error) {
		return &ArticleListResponse{Articles: []Article{{Mid: "same"}}}, nil
	}, 2)
	if err == nil {
		t.Fatal("accepted stalled cursor")
	}
	if history == nil || history.Pages != 2 || len(history.Articles) != 1 || history.Articles[0].Mid != "same" {
		t.Fatalf("previously discovered article was lost: %+v", history)
	}
}

func TestCollectArticleHistoryPreservesCompletedPagesOnFetchFailure(t *testing.T) {
	fetchErr := errors.New("temporary page failure")
	history, err := collectArticleHistory(func(cursor string) (*ArticleListResponse, error) {
		if cursor == "" {
			return &ArticleListResponse{Articles: []Article{{Mid: "newest"}, {Mid: "older"}}}, nil
		}
		if cursor != "older" {
			t.Fatalf("unexpected cursor %q", cursor)
		}
		return nil, fetchErr
	}, 10)
	if !errors.Is(err, fetchErr) {
		t.Fatalf("fetch error = %v", err)
	}
	if history == nil || history.Pages != 1 || len(history.Articles) != 2 || history.Articles[1].Mid != "older" {
		t.Fatalf("completed page was lost: %+v", history)
	}
}

func TestCollectArticleHistoryHasNoPartialResultBeforeFirstPage(t *testing.T) {
	fetchErr := errors.New("first page failed")
	history, err := collectArticleHistory(func(string) (*ArticleListResponse, error) {
		return nil, fetchErr
	}, 10)
	if !errors.Is(err, fetchErr) || history != nil {
		t.Fatalf("first-page failure: history=%+v err=%v", history, err)
	}
}

func TestCollectArticleHistoryPreservesResultsAtSafetyLimit(t *testing.T) {
	history, err := collectArticleHistory(func(cursor string) (*ArticleListResponse, error) {
		if cursor == "" {
			return &ArticleListResponse{Articles: []Article{{Mid: "first"}}}, nil
		}
		t.Fatalf("unexpected cursor %q", cursor)
		return nil, nil
	}, 1)
	if !errors.Is(err, ErrHistoryInvalidResponse) || history == nil || history.Pages != 1 || len(history.Articles) != 1 {
		t.Fatalf("safety-limit result: history=%+v err=%v", history, err)
	}
}

func TestRememberAuthorIDPersistsCapturedRequestValue(t *testing.T) {
	oldPath := mp_json_filepath
	acct_mu.Lock()
	oldAccounts := accounts
	accounts = map[string]*OfficialAccount{"biz": {Biz: "biz"}}
	acct_mu.Unlock()
	mp_json_filepath = filepath.Join(t.TempDir(), "mp.json")
	t.Cleanup(func() {
		mp_json_filepath = oldPath
		acct_mu.Lock()
		accounts = oldAccounts
		acct_mu.Unlock()
	})

	rememberAuthorID("biz", "author")
	acct_mu.RLock()
	got := accounts["biz"].AuthorId
	acct_mu.RUnlock()
	if got != "author" {
		t.Fatalf("AuthorId = %q", got)
	}
	if _, err := os.Stat(mp_json_filepath); err != nil {
		t.Fatal(err)
	}
	rememberAuthorID("biz", "")
	acct_mu.RLock()
	got = accounts["biz"].AuthorId
	acct_mu.RUnlock()
	if got != "author" {
		t.Fatalf("blank capture replaced AuthorId: %q", got)
	}
}
