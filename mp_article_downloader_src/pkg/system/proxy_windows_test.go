//go:build windows

package system

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNotifyProxyChangedChecksBothWinINetCallsAndBroadcast(t *testing.T) {
	optionErr := errors.New("WinINet failed")
	broadcastErr := errors.New("window broadcast failed")
	var calls []string
	var reported error
	err := notifyProxyChangedWith(func(option uintptr) error {
		switch option {
		case internetOptionSettingsChanged:
			calls = append(calls, "settings changed")
			return optionErr
		case internetOptionRefresh:
			calls = append(calls, "refresh")
			return nil
		default:
			t.Fatalf("unexpected WinINet option %d", option)
			return nil
		}
	}, func() error {
		calls = append(calls, "broadcast")
		return broadcastErr
	}, func(err error) {
		reported = err
	})
	if !reflect.DeepEqual(calls, []string{"settings changed", "refresh", "broadcast"}) {
		t.Fatalf("proxy change notification order: %v", calls)
	}
	if !errors.Is(err, optionErr) || errors.Is(err, broadcastErr) || !errors.Is(reported, broadcastErr) {
		t.Fatalf("WinINet failure and broadcast diagnostic were mixed: err=%v reported=%v", err, reported)
	}
}

func TestBroadcastTimeoutDoesNotFailProxyNotification(t *testing.T) {
	timeoutErr := errors.New("broadcast timed out")
	var reported error
	err := notifyProxyChangedWith(func(uintptr) error { return nil }, func() error { return timeoutErr }, func(err error) {
		reported = err
	})
	if err != nil || !errors.Is(reported, timeoutErr) {
		t.Fatalf("broadcast timeout stopped proxy startup: err=%v reported=%v", err, reported)
	}
}

func TestEnsureWindowsProxyNotifiesEvenWhenAlreadyOwned(t *testing.T) {
	owner := "127.0.0.1:2133"
	var writes, notifications int
	write := func(state windowsProxySnapshot) error {
		writes++
		if state.Enabled != 1 || state.Server != owner {
			t.Fatalf("unexpected proxy settings: %+v", state)
		}
		return nil
	}
	notify := func() error { notifications++; return nil }
	if err := ensureWindowsProxy(windowsProxySnapshot{Enabled: 1, Server: owner}, owner, write, notify); err != nil {
		t.Fatal(err)
	}
	if writes != 0 || notifications != 1 {
		t.Fatalf("owned proxy was not re-announced: writes=%d notifications=%d", writes, notifications)
	}
	if err := ensureWindowsProxy(windowsProxySnapshot{Enabled: 0}, owner, write, notify); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || notifications != 2 {
		t.Fatalf("proxy update was not announced: writes=%d notifications=%d", writes, notifications)
	}
}

func TestParseWindowsHTTPSProxyServer(t *testing.T) {
	tests := []struct {
		name, value, host, port string
		wantErr                 bool
	}{
		{name: "single proxy", value: "127.0.0.1:2133", host: "127.0.0.1", port: "2133"},
		{name: "HTTPS selection", value: "http=127.0.0.1:2133;https=127.0.0.1:7897", host: "127.0.0.1", port: "7897"},
		{name: "HTTPS case and spaces", value: "HTTP=127.0.0.1:7897; HTTPS=127.0.0.1:2133", host: "127.0.0.1", port: "2133"},
		{name: "HTTP only", value: "http=127.0.0.1:2133", wantErr: true},
		{name: "SOCKS only", value: "socks=127.0.0.1:2133", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := parse_proxy_server_value(tt.value)
			if (err != nil) != tt.wantErr || host != tt.host || port != tt.port {
				t.Fatalf("parse_proxy_server_value(%q) = %q, %q, %v", tt.value, host, port, err)
			}
		})
	}
}

func TestReadOwnedWindowsProxySnapshot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MP_ARCHIVE_DATA", dir)
	if err := readOwnedWindowsProxySnapshot("127.0.0.1:2133"); err == nil {
		t.Fatal("accepted missing proxy snapshot")
	}
	path := filepath.Join(dir, "proxy-snapshot.json")
	if err := os.WriteFile(path, []byte(`{"owner":"127.0.0.1:2133","enabled":0,"server":"127.0.0.1:7897"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := readOwnedWindowsProxySnapshot("127.0.0.1:2133"); err != nil {
		t.Fatalf("rejected owned proxy snapshot: %v", err)
	}
	if err := readOwnedWindowsProxySnapshot("127.0.0.1:9999"); err == nil {
		t.Fatal("accepted another proxy owner's snapshot")
	}
}
