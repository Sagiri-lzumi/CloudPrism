// Package tray 提供系统托盘：打开界面 / 锁定密库 / 退出。
//
// 基于 github.com/getlantern/systray（纯 syscall，Windows 上不依赖 cgo；
// fyshos/systray 为其活跃 fork，API 相同）。systray.Run 要求主 goroutine
// 运行（Windows 消息循环），故 main.go 在主线程调用 tray.Run，HTTP server
// 在独立 goroutine。
package tray

import (
	_ "embed"

	"github.com/getlantern/systray"
)

//go:embed icon.ico
var iconBytes []byte

// Run 启动托盘消息循环（阻塞直到 Quit 调用）。onReady 里建菜单：
//
//	打开界面 → onOpen
//	锁定密库 → onLock
//	退出     → onQuit
func Run(onOpen, onLock, onQuit func()) {
	systray.Run(func() {
		systray.SetTitle("CloudPrism")
		systray.SetTooltip("CloudPrism 端到端加密云盘")
		systray.SetIcon(iconBytes)
		mOpen := systray.AddMenuItem("打开界面", "在浏览器中打开 CloudPrism 界面")
		mLock := systray.AddMenuItem("锁定密库", "锁定当前连接的密库")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("退出", "退出 CloudPrism")
		go func() {
			for {
				select {
				case <-mOpen.ClickedCh:
					if onOpen != nil {
						onOpen()
					}
				case <-mLock.ClickedCh:
					if onLock != nil {
						onLock()
					}
				case <-mQuit.ClickedCh:
					if onQuit != nil {
						onQuit()
					}
					return
				}
			}
		}()
	}, func() {
		// onExit：清理（无窗口，仅收尾）
	})
}

// Quit 移除托盘图标（退出流程调用；在 onQuit 回调里执行则主线程自然退出）。
func Quit() { systray.Quit() }
