//go:build windows

package system

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

func signals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func set_sys_proc_attr_for_daemon() *syscall.SysProcAttr {
	return nil
}

func terminate_process(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func is_process_running(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	status, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && status == uint32(windows.WAIT_TIMEOUT)
}
