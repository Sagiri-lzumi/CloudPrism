package cryptox

import (
	"bytes"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// TestGCMNonceSizeFork 锁死协议里最易踩的那条分叉：
// Vault Marker 与恢复块用 16 字节 nonce，文件名与缩略图缓存用 12 字节。
//
// Go 标准库的 cipher.NewGCM 只接受 12 字节，因此 Vault 路径必须显式放宽；
// 若哪天有人为了「统一」把 NewGCM16 改成 NewGCM，所有现存密库都会打不开，
// 而症状伪装成「主密码错误」，排查极其困难。
func TestGCMNonceSizeFork(t *testing.T) {
	key := bytes.Repeat([]byte{0x5A}, protocol.KeyLen)

	g16, err := NewGCM16(key)
	if err != nil {
		t.Fatalf("NewGCM16 失败: %v", err)
	}
	g12, err := NewGCM12(key)
	if err != nil {
		t.Fatalf("NewGCM12 失败: %v", err)
	}

	if g16.NonceSize() != protocol.IVLen {
		t.Errorf("NewGCM16 的 nonce 长度 = %d，应为 %d", g16.NonceSize(), protocol.IVLen)
	}
	if g12.NonceSize() != protocol.FilenameNonceLen {
		t.Errorf("NewGCM12 的 nonce 长度 = %d，应为 %d", g12.NonceSize(), protocol.FilenameNonceLen)
	}
	if g16.NonceSize() == g12.NonceSize() {
		t.Error("两种 nonce 长度竟然相同，分叉已被抹平")
	}
	for name, g := range map[string]interface{ Overhead() int }{"GCM16": g16, "GCM12": g12} {
		if g.Overhead() != protocol.GCMTagLen {
			t.Errorf("%s 的标签长度 = %d，应为 %d", name, g.Overhead(), protocol.GCMTagLen)
		}
	}
}

// TestGCMNonceSizesAreNotInterchangeable 从行为上再钉一次：
// 用 12 字节 nonce 去开 16 字节 nonce 封的载荷必须失败，反之亦然。
func TestGCMNonceSizesAreNotInterchangeable(t *testing.T) {
	key := bytes.Repeat([]byte{0x33}, protocol.KeyLen)
	nonce16 := bytes.Repeat([]byte{0x44}, protocol.IVLen)
	nonce12 := bytes.Repeat([]byte{0x44}, protocol.FilenameNonceLen)

	g16, err := NewGCM16(key)
	if err != nil {
		t.Fatal(err)
	}
	g12, err := NewGCM12(key)
	if err != nil {
		t.Fatal(err)
	}

	sealed := g16.Seal(nil, nonce16, []byte("内部明文"), nil)

	if _, err := g12.Open(nil, nonce16[:protocol.FilenameNonceLen], sealed, nil); err == nil {
		t.Error("用 12 字节 nonce 开 16 字节 nonce 的载荷竟然成功了")
	}
	if _, err := g16.Open(nil, append(append([]byte(nil), nonce12...), 0, 0, 0, 0), sealed, nil); err == nil {
		t.Error("把 12 字节 nonce 补零到 16 字节后竟然开成功了")
	}
	// 正向必须成功，否则上面两条失败可能只是因为实现根本不通
	if got, err := g16.Open(nil, nonce16, sealed, nil); err != nil || string(got) != "内部明文" {
		t.Errorf("正确 nonce 下解密失败: got=%q err=%v", got, err)
	}
}

func TestGCMRejectsWrongKeyLength(t *testing.T) {
	for _, keyLen := range []int{0, 16, 24, 31, 33} {
		// AES-128（16 字节）与 AES-192（24 字节）在标准库里是合法密钥长度，
		// 但本协议只用 AES-256，因此这两个值必须被上层挡住而不是被默默接受
		key := bytes.Repeat([]byte{0x11}, keyLen)

		_, err16 := NewGCM16(key)
		_, err12 := NewGCM12(key)

		wantErr := keyLen != protocol.KeyLen
		if (err16 != nil) != wantErr || (err12 != nil) != wantErr {
			t.Errorf("密钥长度 %d：期望「需要报错」=%v，实际 err16=%v err12=%v",
				keyLen, wantErr, err16, err12)
		}
	}
}

// TestGCMSealOrderIsCiphertextThenTag 验证 Go 的 Seal 输出顺序与 Python 的
// ct + tag 拼接一致 —— 这是「不需要在两端之间搬移标签字节」的前提。
func TestGCMSealOrderIsCiphertextThenTag(t *testing.T) {
	key := bytes.Repeat([]byte{0x5A}, protocol.KeyLen)
	nonce := bytes.Repeat([]byte{0x44}, protocol.IVLen)
	plaintext := []byte("内部明文")

	g, err := NewGCM16(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed := g.Seal(nil, nonce, plaintext, nil)

	if len(sealed) != len(plaintext)+protocol.GCMTagLen {
		t.Fatalf("Seal 输出长度 = %d，应为 %d", len(sealed), len(plaintext)+protocol.GCMTagLen)
	}
	// 密文段必须与明文等长（GCM 是流式模式，无填充），且确实被加密过
	if bytes.Equal(sealed[:len(plaintext)], plaintext) {
		t.Fatal("Seal 输出的密文段与明文相同，没有真正加密")
	}
	// 把整段（密文 ‖ 标签）交回 Open 必须还原，证明标签确实在尾部
	got, err := g.Open(nil, nonce, sealed, nil)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("Open 还原结果 = %q，应为 %q", got, plaintext)
	}
	// 篡改最后一个字节（标签）必须导致认证失败
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := g.Open(nil, nonce, tampered, nil); err == nil {
		t.Error("篡改标签后 Open 竟然成功了")
	}
}
