//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

// 进程 CPU 时间查询：GetProcessTimes（kernel32，纯 syscall 无 cgo）。
// 对照 Python perf_monitor.py:107-110 的 os.times()（user+system 聚合）。
var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetProcessTimes = kernel32.NewProc("GetProcessTimes")
)

// ProcessCPUTime 返回当前进程累计 CPU 时间（user + system，秒）。
//
// 调用方用两次采样的差值除以墙钟差得到占用率；GetProcessTimes 统计
// 进程全生命周期，含已退出线程的 CPU 时间（与 os.times 语义一致）。
// 失败时返回 0（调用方按「无数据」处理，不 panic）。
func ProcessCPUTime() float64 {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0
	}
	var creation, exit, kernel, user syscall.Filetime
	r1, _, _ := procGetProcessTimes.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&creation)),
		uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if r1 == 0 {
		return 0
	}
	return filetimeToSeconds(user) + filetimeToSeconds(kernel)
}

// filetimeToSeconds 把 FILETIME（100ns 计数）换算为秒。
//
// 不能直接用 syscall.Filetime.Nanoseconds()：其内部先减 1601→1970 的
// epoch 偏移再乘 100，kernel/user 为 0（进程刚启动尚无 CPU 计时）时该
// 减法乘溢出 int64 取模环绕成 ≈6.8e18 的天文假值（探针实测 raw=0 →
// ns=6802270473709551616）。这里用 uint64 组合原始 64 位后直接乘除，
// 全程无符号溢出风险；对合法时间值两者结果一致。
func filetimeToSeconds(ft syscall.Filetime) float64 {
	u := uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
	return float64(u) * 100 / 1e9
}
