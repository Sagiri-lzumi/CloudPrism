package cryptox

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// 黄金向量由 WindowsPy 端真实运行 Kdf 后打印得到，不是 Go 侧自查拼装。
// 复现方式（仓库根目录）：
//
//	cd WindowsPy
//	..\.venv\Scripts\python.exe -c "from cloudprism.crypto.kdf import Kdf; print(Kdf.derive_key('test', bytes(16)).hex())"
//
// 第一组同时是 WindowsPy/tests/vectors.py:13-16 里既有的跨端向量，
// 也是阶段 2 spike S4 已经验证通过的那一条。
const (
	// pw="test"，salt=0x00*16
	goldenKDFKey = "188492f1d0c361353e6e9c33acc423f6cee47b05f2f547dafda40984b2615257"
	// pw="主密码-pässwörd-日本語"，salt=000102...0f
	goldenKDFKeyMultibyte = "ff9125a5d8c7c5b13842aec9d3d6b99bcc6f947bc5f9d6c49d63448612204b80"
	// DeriveKeyRaw(pw=000102...09, salt=0x77*16)，即恢复块的 rkey
	goldenRecoveryRKey = "ed0d7ca05474d93ad17bf561348473e9ae7ca0b9b21c3ab979deee344643290f"
)

// mustHex 解码黄金向量常量。入参全部是写在本包测试文件里的十六进制字面量，
// 解码失败只可能是常量本身抄错，因此直接 Fatal。
// 参数用 testing.TB 以便基准测试复用同一个助手。
func mustHex(tb testing.TB, s string) []byte {
	tb.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		tb.Fatalf("黄金向量十六进制解码失败: %v", err)
	}
	return b
}

func TestDeriveKeyGolden(t *testing.T) {
	tests := []struct {
		name     string
		password string
		salt     string
		want     string
	}{
		{"零盐基准向量", "test", "00000000000000000000000000000000", goldenKDFKey},
		// 非 ASCII 主密码：验证 UTF-8 编码路径与 Python 的 .encode("utf-8") 一致。
		// 这一条能抓住「按 UTF-16 或本地代码页编码口令」这类跨端不兼容缺陷。
		{"多字节主密码", "主密码-pässwörd-日本語", "000102030405060708090a0b0c0d0e0f", goldenKDFKeyMultibyte},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveKey(tc.password, mustHex(t, tc.salt))
			if hex.EncodeToString(got[:]) != tc.want {
				t.Fatalf("DeriveKey 黄金向量不匹配\n got = %x\nwant = %s", got, tc.want)
			}
		})
	}
}

func TestDeriveKeyRawGolden(t *testing.T) {
	// 恢复码路径：rkey = PBKDF2(10 字节随机密钥, rsalt)，参数与主密码路径完全一致
	got := DeriveKeyRaw(mustHex(t, "00010203040506070809"), mustHex(t, "77777777777777777777777777777777"))
	if hex.EncodeToString(got[:]) != goldenRecoveryRKey {
		t.Fatalf("DeriveKeyRaw 黄金向量不匹配\n got = %x\nwant = %s", got, goldenRecoveryRKey)
	}
}

func TestDeriveKeyMatchesDeriveKeyRaw(t *testing.T) {
	// DeriveKey 必须等价于「先 UTF-8 编码再走 DeriveKeyRaw」。
	// 两条路径若分叉，主密码与恢复块会派生出不同密钥，
	// 表现为「恢复码正确却解不出主密码」。
	salt := mustHex(t, "000102030405060708090a0b0c0d0e0f")
	const password = "hunter2-主密码"

	viaString := DeriveKey(password, salt)
	viaBytes := DeriveKeyRaw([]byte(password), salt)
	if viaString != viaBytes {
		t.Fatalf("两条派生路径结果不一致\n string = %x\n bytes  = %x", viaString, viaBytes)
	}
}

func TestDeriveKeySaltSensitivity(t *testing.T) {
	// 防「参数写反但恰好命中黄金值」的假阳性：盐必须真正参与派生。
	base := DeriveKey("test", make([]byte, protocol.SaltLen))

	shifted := make([]byte, protocol.SaltLen)
	shifted[protocol.SaltLen-1] = 0x01
	if DeriveKey("test", shifted) == base {
		t.Error("盐改变后输出未变，说明盐参数未真正参与派生")
	}
	if DeriveKey("tess", make([]byte, protocol.SaltLen)) == base {
		t.Error("口令改变后输出未变，说明口令参数未真正参与派生")
	}
}

func TestDeriveKeyOutputLength(t *testing.T) {
	got := DeriveKey("test", bytes.Repeat([]byte{0xAB}, protocol.SaltLen))
	if len(got) != protocol.KeyLen {
		t.Fatalf("派生长度应为 %d，实为 %d", protocol.KeyLen, len(got))
	}
}
