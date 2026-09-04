//go:build windows

package win

import (
	"bytes"
	"testing"
)

// TestProtectRoundTrip DPAPI 当前用户级往返：加密 → 解密逐字节还原。
func TestProtectRoundTrip(t *testing.T) {
	// 中文 + 二进制尾巴都要覆盖（凭证 JSON 里正是这类内容）
	cases := [][]byte{
		[]byte(`{"app_id":"dev","access_token":"tok123"}`),
		[]byte("中文凭证内容 ✓"),
		{0x00, 0x01, 0x02, 0xFF, 0xFE},
	}
	for i, raw := range cases {
		ct, err := Protect(raw)
		if err != nil {
			t.Fatalf("case %d Protect 失败: %v", i, err)
		}
		if bytes.Equal(ct, raw) {
			t.Errorf("case %d 密文不应等于明文", i)
		}
		pt, err := Unprotect(ct)
		if err != nil {
			t.Fatalf("case %d Unprotect 失败: %v", i, err)
		}
		if !bytes.Equal(pt, raw) {
			t.Errorf("case %d 往返不一致: %x != %x", i, pt, raw)
		}
	}
}

// TestProtectDistinct 同明文两次加密产物不同（DPAPI 每次注入随机盐）。
func TestProtectDistinct(t *testing.T) {
	a, err := Protect([]byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Protect([]byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Error("同明文两次 DPAPI 加密产物应不同（存在随机盐）")
	}
}

// TestUnprotectGarbage 垃圾数据解密必须报错（Load 层据此兜底 nil）。
func TestUnprotectGarbage(t *testing.T) {
	if _, err := Unprotect([]byte("不是 DPAPI 密文")); err == nil {
		t.Error("垃圾密文应解密失败")
	}
}
