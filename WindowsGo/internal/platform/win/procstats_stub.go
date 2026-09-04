//go:build !windows

// 非 Windows 占位：无进程 CPU 时间能力（本项目仅面向 Windows，
// 占位保证跨平台编译与单测可跑）。
package win

// ProcessCPUTime 在非 Windows 平台恒返回 0。
func ProcessCPUTime() float64 { return 0 }
