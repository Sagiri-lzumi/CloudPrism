package secret

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDPAPI 是 Scheme=DPAPI 的假加密器：与真实 DPAPI 一样保证
// 「密文 != 明文」，用于验证前缀分支与加解密接线。
type fakeDPAPI struct{}

func (fakeDPAPI) Protect(d []byte) ([]byte, error) {
	out := make([]byte, len(d))
	for i, b := range d {
		out[i] = b ^ 0xA5
	}
	return out, nil
}

func (fakeDPAPI) Unprotect(d []byte) ([]byte, error) {
	out := make([]byte, len(d))
	for i, b := range d {
		out[i] = b ^ 0xA5
	}
	return out, nil
}

func (fakeDPAPI) Scheme() string { return "DPAPI" }

// failProtector 模拟「有 DPAPI 能力但加密失败」，验证自动降级 PLAIN。
type failProtector struct{}

func (failProtector) Protect([]byte) ([]byte, error)     { return nil, os.ErrInvalid }
func (failProtector) Unprotect(d []byte) ([]byte, error) { return d, nil }
func (failProtector) Scheme() string                     { return "DPAPI" }

func TestPlainRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	f := NewFile(p, nil) // nil → PLAIN

	if _, ok := f.Load(); ok {
		t.Fatal("文件不存在时不应读出秘密")
	}
	if err := f.Save("abc123"); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	got, ok := f.Load()
	if !ok || got != "abc123" {
		t.Fatalf("往返不一致: got=%q ok=%v", got, ok)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "PLAIN:") {
		t.Fatalf("明文兜底应写 PLAIN: 前缀，实得 %.8s", raw)
	}
	// 内容必须可 base64 解码且不含明文原样字节串
	if _, err := base64.StdEncoding.DecodeString(string(raw[len("PLAIN:"):])); err != nil {
		t.Fatalf("载荷应可 base64 解码: %v", err)
	}
}

func TestDPAPIRoundTripAndPrefix(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	f := NewFile(p, fakeDPAPI{})

	if err := f.Save("s3cr3t-token"); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(raw), "DPAPI:") {
		t.Fatalf("应写 DPAPI: 前缀，实得 %.8s", raw)
	}
	// 明文不得出现在落盘内容里
	if strings.Contains(string(raw), "s3cr3t-token") {
		t.Fatal("密文中不应出现明文")
	}
	got, ok := f.Load()
	if !ok || got != "s3cr3t-token" {
		t.Fatalf("往返不一致: got=%q ok=%v", got, ok)
	}
}

// TestDPAPIFileUnreadableByPlain 验证：DPAPI 密文在无 DPAPI 能力的进程里
// 必须解不开（返回 ok=false），而不是把密文当明文吐出来。
func TestDPAPIFileUnreadableByPlain(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	if err := NewFile(p, fakeDPAPI{}).Save("v"); err != nil {
		t.Fatal(err)
	}
	if got, ok := NewFile(p, nil).Load(); ok {
		t.Fatalf("PLAIN 进程不应解开 DPAPI 密文，实得 %q", got)
	}
}

func TestLoadRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"未知前缀":     "PLAINISH:abcd",
		"空文件":      "",
		"坏 base64": "PLAIN:!!!not-base64!!!",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, "t-"+name)
			if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, ok := NewFile(p, nil).Load(); ok {
				t.Fatalf("损坏文件不应读出秘密，实得 %q", got)
			}
		})
	}
}

func TestSaveFallsBackToPlainOnProtectFailure(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	f := NewFile(p, failProtector{})
	if err := f.Save("fallback"); err != nil {
		t.Fatalf("加密失败时应降级而非报错，实得 %v", err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(raw), "PLAIN:") {
		t.Fatalf("加密失败应降级 PLAIN，实得 %.8s", raw)
	}
	if got, ok := NewFile(p, nil).Load(); !ok || got != "fallback" {
		t.Fatalf("降级后的值应可用 PLAIN 读回: got=%q ok=%v", got, ok)
	}
}

func TestClearIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	f := NewFile(p, nil)
	f.Clear() // 不存在时不应 panic / 报错
	if err := f.Save("x"); err != nil {
		t.Fatal(err)
	}
	f.Clear()
	if _, ok := f.Load(); ok {
		t.Fatal("Clear 后不应还能读出")
	}
	f.Clear() // 再删一次仍应安全
}

func TestNewTokenShapeAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken 失败: %v", err)
		}
		if len(tok) != 32 {
			t.Fatalf("令牌应为 32 位 hex，实得 %d 位: %q", len(tok), tok)
		}
		for _, c := range tok {
			if !strings.ContainsRune("0123456789abcdef", c) {
				t.Fatalf("令牌含非 hex 字符: %q", tok)
			}
		}
		if seen[tok] {
			t.Fatalf("令牌重复（随机源不可用）: %q", tok)
		}
		seen[tok] = true
	}
}
