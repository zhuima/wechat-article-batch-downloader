package api

import (
	"errors"
	"strings"
	"testing"
)

func TestSafeArticleProbeErrorNeverReturnsCredentialURL(t *testing.T) {
	const secretURL = "https://mp.weixin.qq.com/s?__biz=public&uin=private-uin&key=private-key&pass_ticket=private-ticket"
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"verification", errors.New("Get \"" + secretURL + "\": 微信要求完成访问验证"), "微信要求完成访问验证，请在微信中打开文章后重试"},
		{"deleted", errors.New("Get \"" + secretURL + "\": 文章已被发布者删除"), "文章已被发布者删除"},
		{"network", errors.New("Get \"" + secretURL + "\": connection reset by peer"), "无法读取文章，请稍后重试"},
		{"timeout", errors.New("Get \"" + secretURL + "\": context deadline exceeded"), "访问微信文章超时，请稍后重试"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeArticleProbeError(tt.err)
			if got != tt.want {
				t.Fatalf("safeArticleProbeError() = %q; want %q", got, tt.want)
			}
			for _, secret := range []string{secretURL, "private-uin", "private-key", "private-ticket"} {
				if strings.Contains(got, secret) {
					t.Fatalf("probe error leaked %q: %q", secret, got)
				}
			}
		})
	}
}
