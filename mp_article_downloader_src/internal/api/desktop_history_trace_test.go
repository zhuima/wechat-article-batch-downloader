package api

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"mp_article_batch_downloader/internal/officialaccount"
)

func TestDesktopHistoryTraceRecordsSourceScopeAndCounts(t *testing.T) {
	var traces []desktopHistoryTrace
	page, err := fetchDesktopArchivePage(
		func(string, int) (*officialaccount.OfficialMsgListResp, error) {
			return &officialaccount.OfficialMsgListResp{}, nil
		},
		func(string) (*officialaccount.ArticleHistoryResponse, error) {
			return &officialaccount.ArticleHistoryResponse{Pages: 2, Articles: []officialaccount.Article{{
				Title: "文章", URL: "https://mp.weixin.qq.com/s?__biz=secret&mid=1&idx=1&sn=signed&key=secret-key",
			}}}, nil
		},
		"secret-biz", 0, true,
		func(trace desktopHistoryTrace) { traces = append(traces, trace) },
	)
	if err != nil || page.Source != "author" || len(page.Articles) != 1 {
		t.Fatalf("unexpected author page: page=%+v err=%v", page, err)
	}
	if len(traces) != 2 || traces[0].Phase != "legacy" || traces[0].Outcome != "empty" || traces[0].Source != "publisher" || traces[0].Pages != 1 ||
		traces[1].Phase != "author" || traces[1].Outcome != "page" || traces[1].Source != "author" || traces[1].Pages != 2 || traces[1].Articles != 1 {
		t.Fatalf("unexpected trace sequence: %+v", traces)
	}
	var output bytes.Buffer
	logger := zerolog.New(&output)
	for _, trace := range traces {
		logDesktopHistoryTrace(&logger, trace)
	}
	for _, secret := range []string{"secret-biz", "secret-key", "signed", "文章", "__biz", "key="} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("history diagnostic exposed %q: %s", secret, output.String())
		}
	}
}

func TestDesktopHistoryTraceNeverLogsRawErrorOrUnknownLabels(t *testing.T) {
	var output bytes.Buffer
	logger := zerolog.New(&output)
	unsafe := "https://mp.weixin.qq.com/s?__biz=private&key=secret-cookie"
	logDesktopHistoryTrace(&logger, desktopHistoryTrace{
		Phase: unsafe, Outcome: unsafe, Source: unsafe, ErrorCode: unsafe,
	})
	var traces []desktopHistoryTrace
	_, _ = fetchDesktopArchivePage(
		func(string, int) (*officialaccount.OfficialMsgListResp, error) { return nil, errors.New(unsafe) },
		func(string) (*officialaccount.ArticleHistoryResponse, error) {
			t.Fatal("author path should be skipped")
			return nil, nil
		},
		"private", 0, false,
		func(trace desktopHistoryTrace) {
			traces = append(traces, trace)
			logDesktopHistoryTrace(&logger, trace)
		},
	)
	if len(traces) != 2 || traces[0].Outcome != "error" || traces[1].Outcome != "skipped" {
		t.Fatalf("unexpected failed trace sequence: %+v", traces)
	}
	if strings.Contains(output.String(), "private") || strings.Contains(output.String(), "secret-cookie") || strings.Contains(output.String(), "https://") {
		t.Fatalf("raw history error entered diagnostic log: %s", output.String())
	}
	if !strings.Contains(output.String(), `"phase":"unknown"`) || !strings.Contains(output.String(), `"error_code":"unknown"`) {
		t.Fatalf("unrecognized labels were not replaced: %s", output.String())
	}
}
