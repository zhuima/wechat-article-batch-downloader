package officialaccount

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestCompleteAuthorHistoryRestoresEffectiveState(t *testing.T) {
	oldCaptureTime := time.Now().Add(-time.Hour).Unix()
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest", Key: "current-key", AuthorId: "gh_verified", AuthorIdVerified: true,
		Cookie: "author_session=ready", CookieExpiration: time.Now().Add(time.Hour).Unix(),
		IsEffective: false, UpdateTime: oldCaptureTime,
	}, func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("from_article_id") == "" {
			return authorResponse(`{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzTest","mid":"123","title":"文章","url":"https://mp.weixin.qq.com/s/Short_123"}]}`, false), nil
		}
		return authorResponse(`{"ret":0,"base_resp":{"ret":0},"articles":[]}`, false), nil
	})
	history, err := c.FetchArticleHistory("MzTest")
	if err != nil || history == nil || len(history.Articles) != 1 || !accounts["MzTest"].IsEffective {
		t.Fatalf("successful author history did not restore account status: history=%v err=%v", history != nil, err)
	}
	if accounts["MzTest"].UpdateTime != oldCaptureTime || accounts["MzTest"].HistoryValidatedAt < time.Now().Add(-time.Minute).Unix() {
		t.Fatal("history validation did not preserve capture time or refresh its own timestamp")
	}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/mp/list?page=1&page_size=10", nil)
	c.HandleFetchList(ctx)
	var list struct {
		Data struct {
			List []struct {
				IsEffective bool `json:"is_effective"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil || len(list.Data.List) != 1 || !list.Data.List[0].IsEffective {
		t.Fatal("account became stale again immediately after validated author traversal")
	}
}

func TestStoreAccountRetainsVerifiedAuthorIDAcrossKeyOnlyImport(t *testing.T) {
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest", Key: "old-key", Uin: "old-user", PassTicket: "old-ticket",
		AppmsgToken: "old-token", Cookie: "old-cookie", CookieExpiration: time.Now().Add(time.Hour).Unix(),
		AuthorId: "gh_verified", AuthorIdVerified: true, CandidateAuthorId: "gh_old_candidate",
		HistoryValidatedAt: time.Now().Unix(),
	}, nil)
	updated, _ := c.storeAccount(OfficialAccount{Biz: "MzTest", Key: "new-key"})
	if updated.Key != "new-key" || updated.AuthorId != "gh_verified" || !updated.AuthorIdVerified {
		t.Fatalf("key-only import lost verified author identity: %+v", updated)
	}
	if updated.Uin != "" || updated.PassTicket != "" || updated.AppmsgToken != "" ||
		updated.Cookie != "" || updated.CookieExpiration != 0 || updated.CandidateAuthorId != "" || updated.HistoryValidatedAt != 0 {
		t.Fatal("key-only import reused old session credentials")
	}
	if !c.HasAuthorHistoryCredentials("MzTest") {
		t.Fatal("history should remain available with the new key and verified author ID")
	}
}

func TestStoreAccountCandidateDoesNotDowngradeVerifiedAuthorID(t *testing.T) {
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest", Key: "current-key", AuthorId: "explicit-author",
		AuthorIdVerified: true,
	}, nil)
	updated, _ := c.storeAccount(OfficialAccount{
		Biz: "MzTest", Key: "current-key", CandidateAuthorId: "gh_account_username",
	})
	if updated.AuthorId != "explicit-author" || !updated.AuthorIdVerified ||
		updated.CandidateAuthorId != "gh_account_username" {
		t.Fatal("candidate username replaced a verified explicit author ID")
	}
}

func TestStoreAccountDoesNotRetainUnverifiedAuthorIDAcrossSessions(t *testing.T) {
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest", Key: "old-key", AuthorId: "possibly-wrong", CandidateAuthorId: "candidate",
	}, nil)
	updated, _ := c.storeAccount(OfficialAccount{Biz: "MzTest", Key: "new-key"})
	if updated.AuthorId != "" || updated.AuthorIdVerified || updated.CandidateAuthorId != "" {
		t.Fatalf("unverified author identity crossed sessions: %+v", updated)
	}
}

func TestLegacyAccountWithoutHistoryValidationTimeStillLoads(t *testing.T) {
	var account OfficialAccount
	if err := json.Unmarshal([]byte(`{"biz":"MzLegacy","update_time":1700000000,"is_effective":true}`), &account); err != nil {
		t.Fatal(err)
	}
	if account.Biz != "MzLegacy" || account.UpdateTime != 1700000000 || account.HistoryValidatedAt != 0 {
		t.Fatal("older account JSON changed while loading")
	}
}
