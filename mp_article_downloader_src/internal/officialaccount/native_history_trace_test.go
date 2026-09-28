package officialaccount

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeHistoryTraceSummarizesTargetGetmsgWithoutSecrets(t *testing.T) {
	const targetBiz = "MzTraceTarget"
	const secret = "SYNTHETIC_PRIVATE_MARKER"
	req := proxyArticleRequest("mp.weixin.qq.com", "/mp/profile_ext",
		"action=getmsg&__biz="+targetBiz+"&offset=10&uin="+secret+"&key="+secret+
			"&pass_ticket="+secret+"&poc_token="+secret+"&scene=124&is_ok=1",
		"wx_session="+secret)
	req.Header.Set("User-Agent", "synthetic-ua-"+secret)
	body, err := json.Marshal(map[string]any{
		"ret":              0,
		"errmsg":           secret,
		"msg_count":        7,
		"can_msg_continue": 0,
		"next_offset":      17,
		"general_msg_list": `{"list":[{"app_msg_ext_info":{"title":"文章标题` + secret + `","content_url":"https://mp.weixin.qq.com/s?key=` + secret + `&sn=` + secret + `"}}]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := nativeHistoryTrace(req, 200, "application/json; charset=utf-8", body, targetBiz)
	want := "[mp native] action=getmsg http=200 content=json uin=true key=true ticket=true cookie=true ua=true poc=true scene=true is_ok=true parsed=true ret=0 msg_count=7 has_more=0 next_forward=true list_present=true"
	if got != want {
		t.Fatalf("unexpected fixed history summary: got %q, want %q", got, want)
	}
	for _, forbidden := range []string{secret, targetBiz, "__biz=", "https://", "文章标题", "next_offset=17"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("history summary disclosed request or response content: %q", forbidden)
		}
	}
}

func TestNativeHistoryTraceReportsBlockedAndMalformedResponsesSafely(t *testing.T) {
	const secret = "SYNTHETIC_PRIVATE_MARKER"
	req := proxyArticleRequest("mp.weixin.qq.com", "/mp/profile_ext",
		"action=getmsg&__biz=MzTraceTarget&offset=17&key="+secret, "wx_session="+secret)
	tests := []struct {
		name, contentType, body, want string
		status                        int
	}{
		{
			name:        "empty first page",
			contentType: "application/json",
			body:        `{"ret":0,"msg_count":0,"can_msg_continue":0,"next_offset":17,"general_msg_list":""}`,
			status:      200,
			want:        "[mp native] action=getmsg http=200 content=json uin=false key=true ticket=false cookie=true ua=false poc=false scene=false is_ok=false parsed=true ret=0 msg_count=0 has_more=0 next_forward=false list_present=false",
		},
		{
			name:        "rejected JSON",
			contentType: "application/json",
			body:        `{"ret":-3,"errmsg":"` + secret + `","msg_count":0,"can_msg_continue":0,"next_offset":0}`,
			status:      200,
			want:        "[mp native] action=getmsg http=200 content=json uin=false key=true ticket=false cookie=true ua=false poc=false scene=false is_ok=false parsed=true ret=-3 msg_count=0 has_more=0 next_forward=false list_present=false",
		},
		{
			name:        "HTML verification",
			contentType: "text/html",
			body:        `<html><body>` + secret + `</body></html>`,
			status:      403,
			want:        "[mp native] action=getmsg http=403 content=html uin=false key=true ticket=false cookie=true ua=false poc=false scene=false is_ok=false parsed=false",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nativeHistoryTrace(req, tt.status, tt.contentType, []byte(tt.body), "MzTraceTarget")
			if got != tt.want || strings.Contains(got, secret) || strings.Contains(got, "errmsg") {
				t.Fatalf("unsafe or incorrect history summary: %q", got)
			}
		})
	}
}

func TestNativeHistoryTraceSummarizesProfileHomeWithoutBody(t *testing.T) {
	req := proxyArticleRequest("mp.weixin.qq.com", "/mp/profile_ext",
		"action=home&__biz=MzTraceTarget&key=SYNTHETIC_PRIVATE_MARKER", "wx_session=SYNTHETIC_PRIVATE_MARKER")
	got := nativeHistoryTrace(req, 200, "text/html; charset=utf-8", []byte(`<html>SYNTHETIC_PRIVATE_MARKER</html>`), "MzTraceTarget")
	want := "[mp native] action=home http=200 content=html uin=false key=true ticket=false cookie=true ua=false poc=false scene=false is_ok=false"
	if got != want {
		t.Fatalf("unexpected fixed profile summary: got %q, want %q", got, want)
	}
}

func TestNativeHistoryTraceIgnoresOtherTraffic(t *testing.T) {
	tests := []struct {
		name, host, path, query, targetBiz string
	}{
		{"different host", "example.com", "/mp/profile_ext", "action=getmsg&__biz=MzTraceTarget", "MzTraceTarget"},
		{"different path", "mp.weixin.qq.com", "/s", "action=getmsg&__biz=MzTraceTarget", "MzTraceTarget"},
		{"different account", "mp.weixin.qq.com", "/mp/profile_ext", "action=getmsg&__biz=MzOtherAccount", "MzTraceTarget"},
		{"other action", "mp.weixin.qq.com", "/mp/profile_ext", "action=not_getmsg&__biz=MzTraceTarget", "MzTraceTarget"},
		{"no target", "mp.weixin.qq.com", "/mp/profile_ext", "action=getmsg&__biz=MzTraceTarget", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := proxyArticleRequest(tt.host, tt.path, tt.query, "wx_session=private")
			if got := nativeHistoryTrace(req, 200, "application/json", []byte(`{"ret":0}`), tt.targetBiz); got != "" {
				t.Fatalf("unrelated traffic was traced: %q", got)
			}
		})
	}
}
