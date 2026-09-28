package safelog

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactCredentialBearingProxyLog(t *testing.T) {
	raw := "[HTTP] GET https://mp.weixin.qq.com/s?__biz=example&key=SECRET&uin=SECRET&pass_ticket=SECRET\n" +
		"[GIN] POST /api/mp/refresh?token=SECRET\nCookie: SECRET\nAuthorization: SECRET\n"
	var output bytes.Buffer
	if _, err := NewWriter(&output).Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	safe := output.String()
	if strings.Contains(safe, "SECRET") || !strings.Contains(safe, "https://mp.weixin.qq.com/s?[REDACTED]") {
		t.Fatalf("request log was not redacted: %q", safe)
	}
}

func TestRedactEchoPathWithConcatenatedRawQuery(t *testing.T) {
	for _, raw := range []string{
		"[PLUGIN] Forwarding kf.qq.com -> http://127.0.0.1:2132/api/mp/refreshtoken=SECRET&offset=0",
		"[PLUGIN] Returning direct response for /skey=SECRET&__biz=example",
		"[PLUGIN] Forwarding mp.weixin.qq.com -> https://mp.weixin.qq.com/suin=SECRET&pass_ticket=SECRET",
	} {
		safe := Redact(raw)
		if strings.Contains(safe, "SECRET") || !strings.Contains(safe, "[REDACTED]") {
			t.Errorf("concatenated query credential was not redacted: %q", safe)
		}
	}
}
