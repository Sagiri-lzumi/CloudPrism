// Package tray 提供系统托盘：打开界面 / 锁定密库 / 退出。
//
// 基于 github.com/getlantern/systray（纯 syscall，Windows 上不依赖 cgo；
// fyshos/systray 为其活跃 fork，API 相同）。systray.Run 要求主 goroutine
// 运行（Windows 消息循环），故 main.go 在主线程调用 tray.Run，HTTP server
// 在独立 goroutine。
//
// 退出语义（2026-09-21 修）：systray.Run **只在 systray.Quit() 被调用时返回**
// （Quit → PostMessage(WM_CLOSE) → WM_DESTROY → PostQuitMessage →
// GetMessage 返回 0 → nativeLoop 退出）。只通知调用方而不结束消息循环，主
// goroutine 就永远卡在 tray.Run 里，调用方「等 tray.Run 返回后继续收尾」这一步
// 永远不成立 —— 表现为点「退出」毫无反应、进程与托盘图标都还在、HTTP 服务照常
// （用户报的正是这个）。故「退出」菜单项由本包自己负责结束消息循环。
package tray

import (
	_ "embed"

	"github.com/getlantern/systray"
)

//go:embed icon.ico
var iconBytes []byte

// quitLoop 结束托盘消息循环（= systray.Run 的唯一返回条件）。
//
// 抽成变量只为留一个测试缝：这个 bug 的现场是「点菜单毫无反应」，不点根本观测
// 不到，做成变量后可在单测里断言「点退出必须先结束循环」（见 tray_test.go）。
// systray.Quit 内部是 sync.Once，重复调用安全。
var quitLoop = systray.Quit

// clickQuit 处理「退出」菜单项：**先**结束消息循环，再通知调用方。
//
// 顺序不能反：若先调 onQuit 且它中途 panic 或提前返回，消息循环就再没人结束，
// 进程会挂死 —— 这正是「把结束循环的责任推给调用方」时出的问题。
func clickQuit(onQuit func()) {
	quitLoop()
	if onQuit != nil {
		onQuit()
	}
}

// Run 启动托盘消息循环（阻塞直到 Quit 调用）。onReady 里建菜单：
//
//	打开界面 → onOpen
//	锁定密库 → onLock
//	退出     → onQuit（本包已先结束消息循环，onQuit 只需通知调用方收尾）
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
					clickQuit(onQuit)
					return
				}
			}
		}()
	}, func() {
		// onExit：清理（无窗口，仅收尾）
	})
}

// Quit 结束托盘消息循环（返回后 systray.Run 即返回）。
//
// 任意退出路径都可调用，重复调用安全（systray 内部 sync.Once）。
// 托盘「退出」菜单项已由本包自行调用，调用方不必在 onQuit 里再调一次。
func Quit() { quitLoop() }
