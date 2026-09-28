//go:build windows

package system

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

type windowsProxySnapshot struct {
	Enabled       uint64 `json:"enabled"`
	EnabledExists bool   `json:"enabled_exists"`
	Server        string `json:"server"`
	ServerExists  bool   `json:"server_exists"`
	ServerType    uint32 `json:"server_type"`
	Owner         string `json:"owner"`
}

func snapshotPath() string {
	if dir := os.Getenv("MP_ARCHIVE_DATA"); dir != "" {
		return filepath.Join(dir, "proxy-snapshot.json")
	}
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "MPArticleDownloader", "proxy-snapshot.json")
}

func readWindowsProxy(k registry.Key) (windowsProxySnapshot, error) {
	var state windowsProxySnapshot
	value, _, err := k.GetIntegerValue("ProxyEnable")
	if err == nil {
		state.Enabled, state.EnabledExists = value, true
	} else if !errors.Is(err, registry.ErrNotExist) {
		return state, fmt.Errorf("读取系统代理开关失败: %w", err)
	}
	server, valueType, err := k.GetStringValue("ProxyServer")
	if err == nil {
		state.Server, state.ServerType, state.ServerExists = server, valueType, true
	} else if !errors.Is(err, registry.ErrNotExist) {
		return state, fmt.Errorf("读取系统代理地址失败: %w", err)
	}
	return state, nil
}

func writeWindowsProxy(k registry.Key, state windowsProxySnapshot) error {
	if state.ServerExists {
		var err error
		if state.ServerType == registry.EXPAND_SZ {
			err = k.SetExpandStringValue("ProxyServer", state.Server)
		} else {
			err = k.SetStringValue("ProxyServer", state.Server)
		}
		if err != nil {
			return err
		}
	} else if err := k.DeleteValue("ProxyServer"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	if state.EnabledExists {
		return k.SetDWordValue("ProxyEnable", uint32(state.Enabled))
	}
	if err := k.DeleteValue("ProxyEnable"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

const (
	internetOptionSettingsChanged = 39
	internetOptionRefresh         = 37
	windowMessageSettingChange    = 0x001A
	hwndBroadcast                 = 0xffff
	smtoAbortIfHung               = 0x0002
	proxyBroadcastTimeoutMillis   = 2000
)

func windowsCallError(operation string, callErr error) error {
	if callErr == nil || errors.Is(callErr, syscall.Errno(0)) {
		return fmt.Errorf("%s 调用失败", operation)
	}
	return fmt.Errorf("%s 调用失败: %w", operation, callErr)
}

func internetSetOption(option uintptr) error {
	proc := syscall.NewLazyDLL("wininet.dll").NewProc("InternetSetOptionW")
	if err := proc.Find(); err != nil {
		return fmt.Errorf("查找 InternetSetOptionW 失败: %w", err)
	}
	result, _, callErr := proc.Call(0, option, 0, 0)
	if result == 0 {
		return windowsCallError("InternetSetOptionW", callErr)
	}
	return nil
}

func broadcastProxyChanged() error {
	proc := syscall.NewLazyDLL("user32.dll").NewProc("SendMessageTimeoutW")
	if err := proc.Find(); err != nil {
		return fmt.Errorf("查找 SendMessageTimeoutW 失败: %w", err)
	}
	section, err := syscall.UTF16PtrFromString(internetSettingsKey)
	if err != nil {
		return err
	}
	var messageResult uintptr
	result, _, callErr := proc.Call(
		hwndBroadcast, windowMessageSettingChange, 0, uintptr(unsafe.Pointer(section)),
		smtoAbortIfHung, proxyBroadcastTimeoutMillis, uintptr(unsafe.Pointer(&messageResult)),
	)
	runtime.KeepAlive(section)
	if result == 0 {
		return windowsCallError("SendMessageTimeoutW", callErr)
	}
	return nil
}

func notifyProxyChangedWith(setOption func(uintptr) error, broadcast func() error, reportBroadcastError func(error)) error {
	var failures []error
	if err := setOption(internetOptionSettingsChanged); err != nil {
		failures = append(failures, err)
	}
	if err := setOption(internetOptionRefresh); err != nil {
		failures = append(failures, err)
	}
	if err := broadcast(); err != nil {
		// A hung window can make HWND_BROADCAST report a timeout even after
		// other windows received the setting change. Keep the proxy usable.
		if reportBroadcastError != nil {
			reportBroadcastError(err)
		}
	}
	return errors.Join(failures...)
}

func notifyProxyChanged() error {
	return notifyProxyChangedWith(internetSetOption, broadcastProxyChanged, func(err error) {
		fmt.Fprintf(os.Stderr, "广播系统代理变更未完成: %v\n", err)
	})
}

func restoreWindowsProxy(force bool) error {
	data, err := os.ReadFile(snapshotPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var snapshot windowsProxySnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Owner == "" {
		return fmt.Errorf("系统代理备份无效: %v", err)
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	current, err := readWindowsProxy(k)
	if err != nil {
		return err
	}
	// Preserve changes made by another proxy application while this one ran.
	var notifyErr error
	if force || current.Enabled == 1 && current.Server == snapshot.Owner {
		if err := writeWindowsProxy(k, snapshot); err != nil {
			return fmt.Errorf("恢复系统代理失败: %w", err)
		}
		notifyErr = notifyProxyChanged()
	}
	return errors.Join(notifyErr, os.Remove(snapshotPath()))
}

func enable_proxy(args ProxySettings) error {
	args = merge_default_settings(args)
	owner := net.JoinHostPort(args.Hostname, args.Port)
	if err := restoreWindowsProxy(false); err != nil {
		return err
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开系统代理设置失败: %w", err)
	}
	defer k.Close()
	state, err := readWindowsProxy(k)
	if err != nil {
		return err
	}
	if state.Enabled == 1 && state.Server == owner {
		return fmt.Errorf("系统代理已指向 %s；请先关闭已有实例", owner)
	}
	state.Owner = owner
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(snapshotPath()), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(snapshotPath(), data, 0600); err != nil {
		return err
	}
	desired := windowsProxySnapshot{Enabled: 1, EnabledExists: true, Server: owner, ServerExists: true}
	if err := writeWindowsProxy(k, desired); err != nil {
		_ = restoreWindowsProxy(true)
		return fmt.Errorf("设置系统代理失败: %w", err)
	}
	if err := notifyProxyChanged(); err != nil {
		return errors.Join(fmt.Errorf("通知系统代理变更失败: %w", err), restoreWindowsProxy(true))
	}
	return nil
}

func disable_proxy(args ProxySettings) error { return restoreWindowsProxy(false) }

func readOwnedWindowsProxySnapshot(owner string) error {
	data, err := os.ReadFile(snapshotPath())
	if err != nil {
		return fmt.Errorf("找不到本次启动的系统代理备份: %w", err)
	}
	var snapshot windowsProxySnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.Owner != owner {
		return fmt.Errorf("系统代理备份不属于本次启动")
	}
	return nil
}

// EnsureDesktopProxy reasserts the proxy owned by this desktop session without
// replacing the original settings snapshot. Clash Verge can update Windows
// Internet Settings shortly after the archive client starts.
func EnsureDesktopProxy(args ProxySettings) error {
	args = merge_default_settings(args)
	owner := net.JoinHostPort(args.Hostname, args.Port)
	if err := readOwnedWindowsProxySnapshot(owner); err != nil {
		return err
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开系统代理设置失败: %w", err)
	}
	defer k.Close()
	current, err := readWindowsProxy(k)
	if err != nil {
		return err
	}
	return ensureWindowsProxy(current, owner, func(state windowsProxySnapshot) error {
		return writeWindowsProxy(k, state)
	}, notifyProxyChanged)
}

func ensureWindowsProxy(current windowsProxySnapshot, owner string, write func(windowsProxySnapshot) error, notify func() error) error {
	if current.Enabled != 1 || current.Server != owner {
		if err := write(windowsProxySnapshot{Enabled: 1, EnabledExists: true, Server: owner, ServerExists: true}); err != nil {
			return fmt.Errorf("重新接入本机代理失败: %w", err)
		}
	}
	return notify()
}

func fetch_cur_proxy(args ProxySettings) (*ProxySettings, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	state, err := readWindowsProxy(k)
	if err != nil || state.Enabled == 0 || !state.ServerExists {
		return nil, err
	}
	host, port, err := parse_proxy_server_value(state.Server)
	if err != nil || host == "" || port == "" {
		return nil, err
	}
	return &ProxySettings{Hostname: host, Port: port}, nil
}

func get_network_interfaces() (*HardwarePort, error) {
	return nil, errors.New("not supported")
}

func parse_proxy_server_value(value string) (string, string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", "", nil
	}
	parts := strings.Split(raw, ";")
	// WeChat article pages use HTTPS. A different HTTP proxy does not mean
	// article traffic reaches our interceptor.
	candidate := pick_proxy_candidate(parts, "https=")
	if candidate == "" {
		for _, part := range parts {
			if strings.Contains(part, "=") {
				return "", "", fmt.Errorf("HTTPS 系统代理未配置")
			}
		}
	}
	if candidate == "" {
		candidate = raw
	}
	if strings.HasPrefix(candidate, "[") {
		return net.SplitHostPort(candidate)
	}
	idx := strings.LastIndex(candidate, ":")
	if idx <= 0 || idx == len(candidate)-1 {
		return "", "", fmt.Errorf("解析系统代理地址失败: %s", candidate)
	}
	return candidate[:idx], candidate[idx+1:], nil
}

func pick_proxy_candidate(parts []string, prefix string) string {
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), prefix) {
			return strings.TrimSpace(part[len(prefix):])
		}
	}
	return ""
}
