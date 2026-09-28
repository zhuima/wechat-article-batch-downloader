package interceptor

import (
	"io"
	stdlog "log"
	"os"
	"strings"
	"testing"
)

func TestProxyDebugLoggingRedactsEchoRequestAfterRestart(t *testing.T) {
	previousOutput := stdlog.Writer()
	previousStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	defer func() {
		os.Stderr = previousStderr
		stdlog.SetOutput(previousOutput)
		reader.Close()
		writer.Close()
	}()

	for range 2 {
		configureProxyLogging(true)
		stdlog.Print("[HTTPS MITM] GET https://mp.weixin.qq.com/s?__biz=MzTest1&key=SESSION_MARKER&uin=UIN_MARKER&pass_ticket=TICKET_MARKER (Host: mp.weixin.qq.com)")
		stdlog.Print("Cookie: COOKIE_MARKER")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	logged := string(output)
	for _, secret := range []string{"SESSION_MARKER", "UIN_MARKER", "TICKET_MARKER", "COOKIE_MARKER"} {
		if strings.Contains(logged, secret) {
			t.Fatal("proxy debug log disclosed a session credential")
		}
	}
	if strings.Count(logged, "[HTTPS MITM]") != 2 || strings.Count(logged, "[REDACTED]") < 4 {
		t.Fatal("proxy debug diagnostics were not retained with redaction")
	}
}
