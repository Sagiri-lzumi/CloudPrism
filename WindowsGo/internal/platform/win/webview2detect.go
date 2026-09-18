//go:build windows

// Package win 汇集 Windows 平台专属的 syscall 封装。
//
// 本包是 CloudPrism Go 端唯一允许直接触碰 Windows API 的位置：上层 pkg/*
// 只依赖这里导出的语义化函数，从而保证 pkg/ 保持 GUI 与平台无关，
// 未来 Android 端可整体抽出复用（对照 WindowsPy 把 DPAPI 直接写在
// storage/baidu_backend.py 里、无法跨平台复用的做法）。
//
// 全部实现走纯 syscall，不依赖 cgo —— 本机无 gcc，且 CGO_ENABLED=0
// 是阶段 2 spike 已验证通过的构建前提。
package win

import (
	"os"

	"golang.org/x/sys/windows"
)

// FatalMessage 弹出阻塞式错误框。
//
// GUI 子系统程序（-H windowsgui）没有控制台，启动期致命错误若不弹窗
// 就表现为「双击无反应」，用户完全无法自助排查。
func FatalMessage(title, text string) {
	caption, err1 := windows.UTF16PtrFromString(title)
	body, err2 := windows.UTF16PtrFromString(text)
	if err1 != nil || err2 != nil {
		// 文本含 NUL 之类极端情况：退化为 stderr，至少留痕
		os.Stderr.WriteString("CloudPrism 致命错误: " + text + "\n")
		return
	}
	// MB_ICONERROR(0x10) | MB_SYSTEMMODAL(0x1000)：系统模态确保窗口
	// 一定浮在最前，不会被主窗口遮住而看起来像「程序没反应」
	_, _ = windows.MessageBox(0, body, caption, 0x10|0x1000)
}
