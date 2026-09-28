package echo

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ltaoo/echo/cert"
)

func testSNIConnectHandler(t *testing.T, plugins ...*Plugin) *ConnectHandler {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := cert.NewManager(ca, key)
	if err != nil {
		t.Fatal(err)
	}
	loader, err := NewPluginLoader(plugins)
	if err != nil {
		t.Fatal(err)
	}
	requestHandler := NewHTTPHandler(loader)
	requestHandler.Transport.Proxy = nil
	handler := &ConnectHandler{CertManager: manager, PluginLoader: loader, HTTPHandler: requestHandler, InterceptOnlyMatched: true}
	t.Cleanup(func() {
		handler.mitmServers.Range(func(_, value any) bool {
			_ = value.(*MitmServer).Listener.Close()
			return true
		})
		requestHandler.Transport.CloseIdleConnections()
	})
	return handler
}

func testIPTunnel(t *testing.T, handler *ConnectHandler, port string) net.Conn {
	t.Helper()
	client, proxy := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go handler.handleIPTunnelWithSNI(proxy, "127.0.0.1", port)
	_ = client.SetDeadline(time.Now().Add(8 * time.Second))
	return client
}

func testTunnelHTTP(t *testing.T, raw net.Conn, sni, host string) string {
	t.Helper()
	conn := tls.Client(raw, &tls.Config{ServerName: sni, InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}})
	defer conn.Close()
	if err := conn.Handshake(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://"+host+"/s", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Close = true
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestIPConnectMatchingSNIReachesResponsePlugin(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "origin")
	}))
	defer origin.Close()
	originURL, _ := url.Parse(origin.URL)
	_, portText, _ := net.SplitHostPort(originURL.Host)
	port, _ := strconv.Atoi(portText)
	var captured int32
	handler := testSNIConnectHandler(t, &Plugin{
		Match:  "qq.com",
		Target: &TargetConfig{Protocol: "http", Host: "127.0.0.1", Port: port},
		OnResponse: func(ctx *Context) {
			atomic.AddInt32(&captured, 1)
			ctx.SetResponseBody("captured")
		},
	})
	if got := testTunnelHTTP(t, testIPTunnel(t, handler, portText), "mp.weixin.qq.com", "mp.weixin.qq.com"); got != "captured" {
		t.Fatalf("matching SNI missed response plugin: %q", got)
	}
	if atomic.LoadInt32(&captured) != 1 {
		t.Fatal("response hook did not run exactly once")
	}
}

func TestIPConnectOtherAndMissingSNIRemainDirect(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	defer origin.Close()
	originURL, _ := url.Parse(origin.URL)
	_, port, _ := net.SplitHostPort(originURL.Host)
	var captured int32
	handler := testSNIConnectHandler(t, &Plugin{Match: "qq.com", OnResponse: func(*Context) {
		atomic.AddInt32(&captured, 1)
	}})
	for _, tc := range []struct{ name, sni string }{
		{"other SNI", "other.example"},
		{"missing SNI", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testTunnelHTTP(t, testIPTunnel(t, handler, port), tc.sni, "mp.weixin.qq.com"); got != "direct" {
				t.Fatalf("unmatched tunnel was not forwarded to original IP: %q", got)
			}
		})
	}
	if atomic.LoadInt32(&captured) != 0 {
		t.Fatal("unmatched SNI invoked a response plugin")
	}
}

func TestIPConnectNonTLSKeepsPeekedBytes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		buf := make([]byte, len("plain-marker"))
		if _, err := io.ReadFull(conn, buf); err == nil {
			_, err = conn.Write(buf)
		}
		done <- err
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	handler := testSNIConnectHandler(t, &Plugin{Match: "qq.com"})
	client := testIPTunnel(t, handler, port)
	if _, err := client.Write([]byte("plain-marker")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("plain-marker"))
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "plain-marker" || <-done != nil {
		t.Fatal("non-TLS direct tunnel lost buffered data")
	}
}

func TestIPConnectInspectionScopeAndBypass(t *testing.T) {
	for _, tc := range []struct {
		host, port string
		want       bool
	}{
		{"127.0.0.1", "443", true}, {"::1", "443", true},
		{"mp.weixin.qq.com", "443", false}, {"127.0.0.1", "80", false},
	} {
		if got := isIPPort443(tc.host, tc.port); got != tc.want {
			t.Fatalf("inspection scope for %s:%s = %v", tc.host, tc.port, got)
		}
	}
	handler := testSNIConnectHandler(t, &Plugin{Match: "qq.com"}, &Plugin{Match: "mp.weixin.qq.com", Bypass: true})
	if handler.interceptableSNIHost("mp.weixin.qq.com") != "" ||
		handler.interceptableSNIHost("evilqq.com") != "" ||
		handler.interceptableSNIHost("qq.com.evil.example") != "" ||
		handler.interceptableSNIHost("not a host") != "" ||
		handler.interceptableSNIHost("sub.qq.com") != "sub.qq.com" {
		t.Fatal("SNI matching ignored a bypass or domain boundary")
	}
	if !strings.HasPrefix(handler.interceptableSNIHost("SUB.QQ.COM"), "sub.") {
		t.Fatal("DNS case normalization failed")
	}
}
