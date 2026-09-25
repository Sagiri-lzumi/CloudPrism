package cache

import (
	"sync"
	"testing"
	"time"
)

// TestPurgePutConcurrentNoDeadlock 钉死缓存层的锁序（Store.mu → entry.mu）。
//
// 回归的是 ABBA 死锁：Put 曾「持 entry.mu → 取 Store.mu」（记账 + 淘汰），
// 而 Purge 是「持 Store.mu → 取 entry.mu 逐条删除」。两条路径正好反向，
// 打开两个 goroutine 互等即成永久死锁 —— 触发条件恰恰是最常见的用法：
// 一边播放（Put 在填块）、一边在设置页点「清空缓存」。
//
// 测试用两个并发循环互相撞，配 20s 看门狗：修复前必然卡死其中一方。
func TestPurgePutConcurrentNoDeadlock(t *testing.T) {
	s, _ := openTest(t, 1, 0) // 不限容量，避免淘汰干扰（要撞的是锁序本身）

	const (
		total = 2 << 20 // 2 MiB > 阈值 ⇒ 分块布局，路径更长
		win   = 256 << 10
	)
	data := payload(total, 3)

	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})

	// 写方：反复按窗口填块（每轮都触发 ensureEntry/flushMeta/enforceLimit）
	go func() {
		defer wg.Done()
		<-start
		for round := 0; round < 40; round++ {
			putWindows(t, s, "A/movie.mp4", "影片.mp4", total, data)
		}
	}()
	// 清方：反复清空（每轮都要持 Store.mu 再逐条取 entry.mu）
	go func() {
		defer wg.Done()
		<-start
		for round := 0; round < 40; round++ {
			if err := s.Purge(); err != nil {
				t.Errorf("Purge 失败: %v", err)
				return
			}
		}
	}()

	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Purge 与 Put 并发时死锁：锁序被写反（必须 Store.mu → entry.mu）")
	}

	// 收尾后仍可用：能建条目、能读回
	s.Put("A/after.bin", "after.bin", int64(len(data)), 0, data)
	if _, ok := s.Fetch("A/after.bin", int64(len(data)), 0, int64(len(data))-1); !ok {
		t.Fatal("死锁排查后缓存应仍可正常读写")
	}
}
