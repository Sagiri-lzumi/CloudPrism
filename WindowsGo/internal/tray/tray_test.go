package tray

import "testing"

// 「退出」菜单项必须**先结束托盘消息循环**，再通知调用方。
//
// 这条断言守的是一个「不点菜单就观测不到」的致命 bug（2026-09-21 踩过）：
// 只通知调用方而不调 systray.Quit，systray.Run 永不返回 ⇒ 主 goroutine 卡死、
// 进程退不掉、托盘图标不消失，用户侧表现为「点『退出』毫无反应」。
// 故用 quitLoop 这个测试缝把它钉死 —— 回退修复（改回只调 onQuit）本测试必红。
func TestClickQuitEndsLoopBeforeNotifying(t *testing.T) {
	orig := quitLoop
	t.Cleanup(func() { quitLoop = orig })

	var order []string
	quitLoop = func() { order = append(order, "quitLoop") }
	clickQuit(func() { order = append(order, "onQuit") })

	want := []string{"quitLoop", "onQuit"}
	if len(order) != len(want) {
		t.Fatalf("调用序列 = %v，期望 %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("调用序列 = %v，期望 %v", order, want)
		}
	}
}

// onQuit 为 nil 时也不能出事：消息循环仍必须被结束（否则挂死）。
func TestClickQuitWithoutCallbackStillEndsLoop(t *testing.T) {
	orig := quitLoop
	t.Cleanup(func() { quitLoop = orig })

	called := 0
	quitLoop = func() { called++ }
	clickQuit(nil)

	if called != 1 {
		t.Fatalf("quitLoop 调用次数 = %d，期望 1", called)
	}
}
