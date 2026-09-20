// 模块路径与仓库地址对齐，便于未来以 go module 形式被 Android 端复用。
//
// v33 起彻底移除 Wails：唯一 UI 形态为 HTTP server + 系统托盘 + 浏览器前端。
// 依赖仅 x/sys（纯 syscall：DPAPI/COM 对话框/托盘）+ x/image（缩略图）。
module github.com/Sagiri-lzumi/cloudprism/windowsgo

go 1.27

require golang.org/x/sys v0.47.0

require (
	github.com/getlantern/systray v1.2.2
	golang.org/x/image v0.45.0
)

require (
	github.com/getlantern/context v0.0.0-20190109183933-c447772a6520 // indirect
	github.com/getlantern/errors v0.0.0-20190325191628-abdb3e3e36f7 // indirect
	github.com/getlantern/golog v0.0.0-20190830074920-4ef2e798c2d7 // indirect
	github.com/getlantern/hex v0.0.0-20190417191902-c6586a6fe0b7 // indirect
	github.com/getlantern/hidden v0.0.0-20190325191715-f02dbb02be55 // indirect
	github.com/getlantern/ops v0.0.0-20190325191751-d70cb0d6f85f // indirect
	github.com/go-stack/stack v1.8.0 // indirect
	github.com/oxtoacart/bpool v0.0.0-20190530202638-03653db5a59c // indirect
)
