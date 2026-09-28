# Local Echo fork

This directory is copied from `github.com/ltaoo/echo` v0.11.1 and retains its
upstream LICENSE. The root Go module uses it through a `replace` directive.

The local change in `connect.go` inspects a bounded TLS ClientHello only when
an otherwise unmatched CONNECT targets an IP address on port 443. It promotes
the tunnel to MITM only if a valid SNI host matches an enabled, non-bypass
plugin. Other tunnels keep their original destination and buffered bytes.

`connect_sni_test.go` covers target SNI interception and direct forwarding for
other SNI, no SNI, and non-TLS traffic. Recheck this behavior when upgrading
Echo because it depends on CONNECT routing before the HTTP plugin callbacks.
