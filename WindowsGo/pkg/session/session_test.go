package session

import (
	"bytes"
	"errors"
	"sync"
	"testing"
)

// TestDeriveKeyConsistency 同一 salt 派生一致、不同 salt 不同（黄金语义
// 已被 cryptox 黄金向量锁定，这里只验证 Session 的缓存与分发正确性）。
func TestDeriveKeyConsistency(t *testing.T) {
	s := New("主密码 p@ss 中文字符")
	defer s.Close()

	saltA := bytes.Repeat([]byte{0x11}, 16)
	saltB := bytes.Repeat([]byte{0x22}, 16)

	k1, err := s.DeriveKey(saltA)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := s.DeriveKey(saltA) // 命中缓存
	if err != nil {
		t.Fatal(err)
	}
	if k1 != k2 {
		t.Error("同一 salt 两次派生应一致（缓存未生效或返回值异常）")
	}
	kb, err := s.DeriveKey(saltB)
	if err != nil {
		t.Fatal(err)
	}
	if k1 == kb {
		t.Error("不同 salt 派生应不同")
	}
}

// TestMasterPasswordRoundTrip 中文密码往返一致。
func TestMasterPasswordRoundTrip(t *testing.T) {
	pw := "云盘主密码 / CloudPass 123"
	s := New(pw)
	defer s.Close()
	if got := s.MasterPassword(); got != pw {
		t.Errorf("MasterPassword 往返不一致：%q != %q", got, pw)
	}
}

// TestCloseZeroize 关闭后密码不可再取、派生报 ErrClosed。
func TestCloseZeroize(t *testing.T) {
	s := New("secret-pass-123")
	// 先派生一条缓存，确保关闭时缓存也被清除（无残留引用）
	salt := bytes.Repeat([]byte{0xAB}, 16)
	if _, err := s.DeriveKey(salt); err != nil {
		t.Fatal(err)
	}
	s.Close()

	if got := s.MasterPassword(); got != "" {
		t.Errorf("关闭后主密码应清空，实得 %q", got)
	}
	if _, err := s.DeriveKey(salt); !errors.Is(err, ErrClosed) {
		t.Errorf("关闭后派生应报 ErrClosed，实得 %v", err)
	}
}

// TestConcurrentDerive 并发派生同一 salt 不炸且结果一致（锁与缓存竞态回归）。
func TestConcurrentDerive(t *testing.T) {
	s := New("concurrent-pw")
	defer s.Close()
	salt := bytes.Repeat([]byte{0x5A}, 16)

	const n = 16
	var wg sync.WaitGroup
	results := make(chan [32]byte, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := s.DeriveKey(salt)
			if err != nil {
				errs <- err
				return
			}
			results <- k
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("并发派生出错: %v", err)
	}
	var first [32]byte
	seen := false
	for k := range results {
		if !seen {
			first = k
			seen = true
			continue
		}
		if k != first {
			t.Fatal("并发派生同一 salt 结果不一致")
		}
	}
}

// TestClearCacheKeepsPassword ClearCache 只清密钥、不清密码。
func TestClearCacheKeepsPassword(t *testing.T) {
	s := New("still-alive")
	defer s.Close()
	salt := bytes.Repeat([]byte{0x77}, 16)
	k1, err := s.DeriveKey(salt)
	if err != nil {
		t.Fatal(err)
	}
	s.ClearCache()
	k2, err := s.DeriveKey(salt)
	if err != nil {
		t.Fatal(err)
	}
	if k1 != k2 {
		t.Error("ClearCache 后重新派生应得到相同密钥（密码未被清）")
	}
	if s.MasterPassword() != "still-alive" {
		t.Error("ClearCache 不应清空主密码")
	}
}
