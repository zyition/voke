//go:build windows

package platform

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	advapi32              = syscall.NewLazyDLL("advapi32.dll")
	procRegSetKeyValue    = advapi32.NewProc("RegSetKeyValueW")
	procRegDeleteKeyValue = advapi32.NewProc("RegDeleteKeyValueW")
)

const (
	hkeyCurrentUser    = uintptr(0x80000001)
	regSz              = uintptr(1)
	runKeyPath         = `Software\Microsoft\Windows\CurrentVersion\Run`
	autostartValueName = "voke"
	// errFileNotFound = ERROR_FILE_NOT_FOUND（2）：RegDeleteKeyValueW 删一个不存在的值
	// 返回它。对照 voke-sys delete_value 把它当幂等成功（「已是关闭态」），其余非 0 才是真失败。
	errFileNotFound = uintptr(2)
)

// SetAutostart 写 / 删 HKCU\...\Run 下名为 "voke" 的 REG_SZ（值为带引号的当前 exe 路径），
// 实现开机自启开关（§3 / 阶段 2）。enabled=false 删除该项。对照 voke-app 的 autostart。
func SetAutostart(enabled bool) error {
	name, err := syscall.UTF16PtrFromString(autostartValueName)
	if err != nil {
		return err
	}
	key, err := syscall.UTF16PtrFromString(runKeyPath)
	if err != nil {
		return err
	}
	if !enabled {
		// 对照 voke-sys/src/autostart.rs delete_value：删除成功（r=0）与「值本就不存在」
		// （ERROR_FILE_NOT_FOUND）都算幂等成功；其余非 0（如安全软件拦 HKCU\Run 写入 →
		// ERROR_ACCESS_DENIED，autostart.rs:58 明确预料到的场景）必须上抛，好让设置窗「回弹」。
		// 旧实现把非 0 一律吞成 nil，会让配置谎报已关闭而实际没删掉。
		r, _, _ := procRegDeleteKeyValue.Call(hkeyCurrentUser, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(name)))
		if r == 0 || r == errFileNotFound {
			return nil
		}
		return syscall.Errno(r)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmdline := `"` + exe + `"` // 带引号：路径可能含空格。
	data, err := syscall.UTF16FromString(cmdline)
	if err != nil {
		return err
	}
	cb := uintptr(len(data) * 2)
	r, _, callErr := procRegSetKeyValue.Call(
		hkeyCurrentUser,
		uintptr(unsafe.Pointer(key)),
		uintptr(unsafe.Pointer(name)),
		regSz,
		uintptr(unsafe.Pointer(&data[0])),
		cb,
	)
	if r != 0 {
		return callErr
	}
	return nil
}
