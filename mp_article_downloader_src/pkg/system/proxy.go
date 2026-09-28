package system

import "runtime"

type ProxySettings struct {
	Device   string
	Hostname string
	Port     string
}

type HardwarePort struct {
	Device    string
	Port      string
	Interface string
}

func merge_default_settings(p ProxySettings) ProxySettings {
	if p.Device == "" {
		p.Device = "Wi-Fi" // 默认使用 Wi-Fi 设备
		device, err := get_network_interfaces()
		if err == nil {
			p.Device = device.Port
		}
	}
	if p.Hostname == "" {
		p.Hostname = "127.0.0.1"
	}
	if p.Port == "" {
		p.Port = "2023"
	}
	return p

}

func EnableProxy(arg ProxySettings) error {
	return enable_proxy(arg)
}

func DisableProxy(arg ProxySettings) error {
	return disable_proxy(arg)
}

func FetchCurProxy(arg ProxySettings) (*ProxySettings, error) {
	return fetch_cur_proxy(arg)
}

// IsDesktopProxyCaptureActive reports whether the current Windows HTTPS system
// proxy points at the desktop client's local interceptor. It checks live system
// settings rather than the settings requested when the client started.
func IsDesktopProxyCaptureActive() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	current, err := FetchCurProxy(ProxySettings{})
	return err == nil && current != nil && current.Hostname == "127.0.0.1" && current.Port == "2133"
}
