package officialaccount

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"mp_article_batch_downloader/internal/interceptor/proxy"
)

// nativeHistoryTrace summarizes a request made by WeChat itself. It accepts
// only one configured account and prints fixed labels, booleans, and counts.
// Neither the signed request URL nor the article response is retained here.
func nativeHistoryTrace(req *proxy.ContextReq, status int, contentType string, body []byte, targetBiz string) string {
	if req == nil || req.URL == nil || req.URL.Hostname == nil ||
		req.URL.Hostname() != "mp.weixin.qq.com" || req.URL.Path != "/mp/profile_ext" || targetBiz == "" {
		return ""
	}
	query, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil || query.Get("__biz") != targetBiz {
		return ""
	}
	action := query.Get("action")
	if action != "home" && action != "getmsg" {
		return ""
	}
	contentKind := "other"
	switch {
	case strings.Contains(strings.ToLower(contentType), "json"):
		contentKind = "json"
	case strings.Contains(strings.ToLower(contentType), "html"):
		contentKind = "html"
	}
	hasHeader := func(name string) bool { return req.Header != nil && strings.TrimSpace(req.Header.Get(name)) != "" }
	base := fmt.Sprintf("[mp native] action=%s http=%d content=%s uin=%t key=%t ticket=%t cookie=%t ua=%t poc=%t scene=%t is_ok=%t",
		action, status, contentKind, query.Get("uin") != "", query.Get("key") != "",
		query.Get("pass_ticket") != "", hasHeader("Cookie"), hasHeader("User-Agent"),
		query.Get("poc_token") != "", query.Get("scene") != "", query.Get("is_ok") != "")
	if action != "getmsg" {
		return base
	}
	var response struct {
		Ret      *int   `json:"ret"`
		MsgCount int    `json:"msg_count"`
		HasMore  int    `json:"can_msg_continue"`
		Next     int    `json:"next_offset"`
		MsgList  string `json:"general_msg_list"`
	}
	if len(body) == 0 || len(body) > 4<<20 || json.Unmarshal(body, &response) != nil || response.Ret == nil {
		return base + " parsed=false"
	}
	requestedOffset, parseErr := strconv.Atoi(query.Get("offset"))
	nextForward := parseErr == nil && response.Next > requestedOffset
	return fmt.Sprintf("%s parsed=true ret=%d msg_count=%d has_more=%d next_forward=%t list_present=%t",
		base, *response.Ret, response.MsgCount, response.HasMore, nextForward,
		strings.TrimSpace(response.MsgList) != "")
}
