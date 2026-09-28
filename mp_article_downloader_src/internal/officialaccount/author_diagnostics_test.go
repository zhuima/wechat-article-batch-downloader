package officialaccount

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func captureAuthorDiagnostics(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = writer
	defer func() {
		os.Stdout = previous
		reader.Close()
		writer.Close()
	}()
	run()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = previous
	logged, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(logged)
}

func TestAuthorDiagnosticsTracePagesWithoutSessionValues(t *testing.T) {
	const marker = "PRIVATE_SESSION_MARKER"
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest", CandidateAuthorId: "gh_" + marker, Uin: marker, Key: marker, PassTicket: marker,
	}, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Query().Get("action") {
		case "show":
			response := authorResponse("<html><title>作者</title></html>", false)
			response.Header.Add("Set-Cookie", "author_session="+marker+"; Path=/; HttpOnly")
			return response, nil
		case "get_articles":
			if req.URL.Query().Get("from_article_id") == "" {
				return authorResponse(`{"ret":0,"base_resp":{"ret":0},"articles":[{"__biz":"MzTest","mid":"123","title":"文章","url":"https://mp.weixin.qq.com/s?__biz=MzTest&mid=123&key=`+marker+`"}]}`, false), nil
			}
			return authorResponse(`{"ret":0,"base_resp":{"ret":0},"articles":[]}`, false), nil
		}
		t.Fatal("unexpected author request")
		return nil, nil
	})
	logged := captureAuthorDiagnostics(t, func() {
		history, err := c.FetchArticleHistory("MzTest")
		if err != nil || history == nil || len(history.Articles) != 1 {
			t.Fatal("author history traversal failed")
		}
	})
	for _, expected := range []string{
		"phase=cookie outcome=refresh", "phase=author_show outcome=received", "phase=cookie outcome=received",
		"phase=articles_first outcome=nonempty", "phase=candidate outcome=accepted",
		"phase=pagination outcome=advanced", "phase=articles_next outcome=empty", "phase=pagination outcome=end",
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("missing fixed diagnostic %s", expected)
		}
	}
	if strings.Contains(logged, marker) || strings.Contains(logged, "author_id=") || strings.Contains(logged, "Cookie:") || strings.Contains(logged, "https://") {
		t.Fatal("author diagnostic disclosed request or session data")
	}
}

func TestAuthorListVerificationHasOwnSafeDiagnostic(t *testing.T) {
	const marker = "PRIVATE_SESSION_MARKER"
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest", AuthorId: "gh_" + marker, Key: marker, Cookie: "session=" + marker,
		CookieExpiration: time.Now().Add(time.Hour).Unix(),
	}, func(*http.Request) (*http.Response, error) {
		return authorResponse("<html><title>验证</title><body>wappoc_appmsgcaptcha</body></html>", false), nil
	})
	logged := captureAuthorDiagnostics(t, func() {
		_, err := c.FetchArticleHistory("MzTest")
		if !errors.Is(err, ErrHistoryVerificationRequired) {
			t.Fatal("author list challenge was not classified as verification")
		}
	})
	if !strings.Contains(logged, "phase=articles_first outcome=verification") || strings.Contains(logged, marker) {
		t.Fatal("verification diagnostic is missing or disclosed credentials")
	}
}

func TestAuthorReturnCodeDiagnosticContainsOnlyNumbers(t *testing.T) {
	const marker = "PRIVATE_SESSION_MARKER"
	c := authorTestClient(t, OfficialAccount{
		Biz: "MzTest", CandidateAuthorId: "gh_" + marker, Key: marker, Cookie: "session=" + marker,
		CookieExpiration: time.Now().Add(time.Hour).Unix(),
	}, func(*http.Request) (*http.Response, error) {
		return authorResponse(`{"ret":-1,"base_resp":{"ret":0},"errmsg":"`+marker+`","articles":[]}`, false), nil
	})
	logged := captureAuthorDiagnostics(t, func() {
		_, err := c.FetchArticleHistory("MzTest")
		kind, _ := ClassifyHistoryError(err)
		if kind != "candidate_unverified" {
			t.Fatalf("candidate first-page failure was classified as %q", kind)
		}
	})
	if !strings.Contains(logged, "phase=articles_first outcome=remote_error ret=-1 base_ret=0") ||
		strings.Contains(logged, marker) || strings.Contains(logged, "https://") || strings.Contains(logged, "Cookie:") {
		t.Fatal("author return-code diagnostic omitted codes or disclosed a session value")
	}
}
