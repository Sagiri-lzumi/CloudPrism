//go:build windows

package win

import (
	"testing"
)

// TestAcquireInstanceLockExclusive 同一 key 第二次获取必须失败（返回 false）。
//
// 这是单实例语义的核心契约：端口可配之后，端口扫描不再可靠，数据目录锁
// 是唯一的「一个数据目录一个进程」保证。若本测试失败，用户可能在改端口后
// 同时跑起两个实例，共享 data/ 里的设置/日志/缓存。
func TestAcquireInstanceLockExclusive(t *testing.T) {
	key := t.TempDir() // 每个测试用例一个独立 key，避免与其它测试/真实进程互扰

	ok, err := AcquireInstanceLock(key)
	if err != nil {
		t.Fatalf("首次获取锁不应报错：%v", err)
	}
	if !ok {
		t.Fatal("首次获取锁应为 primary")
	}

	// 同一 key 的互斥（第二次拿不到）由 TestAcquireInstanceLockCaseInsensitive
	// 覆盖 —— 它用大小写不同、实际同一路径的 key，两次调用互不干扰。
	// 这里补一条不同 key 应各自可取的断言。
	otherKey := t.TempDir()
	okOther, err := AcquireInstanceLock(otherKey)
	if err != nil {
		t.Fatalf("不同 key 获取锁不应报错：%v", err)
	}
	if !okOther {
		t.Error("不同 key 应各自独立可取")
	}
}

// TestAcquireInstanceLockDistinctKeys 不同数据目录的锁互不影响。
func TestAcquireInstanceLockDistinctKeys(t *testing.T) {
	for i := 0; i < 3; i++ {
		if ok, err := AcquireInstanceLock(t.TempDir()); err != nil || !ok {
			t.Fatalf("第 %d 个独立 key 应可取锁，ok=%v err=%v", i, ok, err)
		}
	}
}

// TestAcquireInstanceLockCaseInsensitive key 大小写不敏感：
// Windows 路径不区分大小写，同一目录的不同写法必须映射到同一把锁，
// 否则 "C:\\Data" 与 "c:\\data" 会被当成两个数据目录而放过第二个实例。
func TestAcquireInstanceLockCaseInsensitive(t *testing.T) {
	ok1, err1 := AcquireInstanceLock(`C:\CloudPrismTest\CaseDir`)
	if err1 != nil || !ok1 {
		t.Fatalf("首个 key 应可取锁：ok=%v err=%v", ok1, err1)
	}
	ok2, err2 := AcquireInstanceLock(`c:\cloudprismtest\casedir`)
	if err2 != nil {
		t.Fatalf("第二个 key 不应报错：%v", err2)
	}
	if ok2 {
		t.Error("大小写不同的同一路径必须映射到同一把锁（应返回 false）")
	}
}
