//go:build windows

// 平台 URL 打开能力：ShellExecuteW 的极薄封装。
//
// 用途：v33 起程序以纯 Web 形态运行，启动后用它把界面地址交给系统默认
// 浏览器打开；打开外部链接（如更新页）也走这里。
// 纯 syscall（shell32 导出），与包内其余实现同一路线，不依赖 cgo。
package win

import (
	"syscall"
	"unsafe"
)

var (
	shell32        = syscall.NewLazyDLL("shell32.dll")
	procShellExecW = shell32.NewProc("ShellExecuteW")
)

// OpenURL 用系统默认浏览器打开 url（等价资源管理器的「打开方式」）。
//
// ShellExecuteW 返回的 HINSTANCE 值 >32 表示成功；≤32 是错误码
// （SE_ERR_*），此处把 errno 一并包装返回供调用方留日志。
func OpenURL(url string) error {
	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := syscall.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	// hwnd=0（无父窗）、lpParameters/lpDirectory=nil、nShowCmd=SW_SHOW(5)
	r1, _, callErr := procShellExecW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(target)),
		0,
		0,
		5,
	)
	if uintptr(r1) <= 32 {
		if callErr != nil {
			return callErr
		}
		return syscall.Errno(uintptr(r1))
	}
	return nil
}
