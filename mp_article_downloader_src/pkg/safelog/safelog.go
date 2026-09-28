package safelog

import (
	"io"
	"regexp"
	"strings"
)

var loggedURL = regexp.MustCompile(`(?i)(?:https?|wss?)://[^\s"'<>]+`)

// Echo sometimes logs URL.Path + URL.RawQuery without the '?' separator.
// Match credential names even when they are attached directly to a path.
var credentialPair = regexp.MustCompile(`(?i)((?:key|uin|pass_ticket|appmsg_token|token|session_key))=[^&\s"'<>]+`)

// The standard logger prepends a timestamp, so authentication headers may
// appear after that prefix rather than at the start of a line.
var credentialHeader = regexp.MustCompile(`(?im)(\b(?:cookie|authorization|proxy-authorization)\s*:\s*)[^\r\n]+`)

// Redact removes temporary WeChat query parameters and authentication headers
// from request diagnostics. It must never be used to alter an actual request.
func Redact(line string) string {
	line = loggedURL.ReplaceAllStringFunc(line, func(raw string) string {
		if pos := strings.IndexByte(raw, '?'); pos >= 0 {
			return raw[:pos] + "?[REDACTED]"
		}
		return raw
	})
	line = credentialPair.ReplaceAllString(line, "$1=[REDACTED]")
	return credentialHeader.ReplaceAllString(line, "$1[REDACTED]")
}

type writer struct{ next io.Writer }

func (w writer) Write(p []byte) (int, error) {
	safe := Redact(string(p))
	n, err := io.WriteString(w.next, safe)
	if err == nil && n != len(safe) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func NewWriter(next io.Writer) io.Writer { return writer{next: next} }
