package protocol

import (
	"encoding/base32"
	"testing"
)

// TestWireFormatConstants 把每个协议常量的取值钉死。
//
// 这不是「断言常量等于常量」的同义反复：这些取值的真源是 WindowsPy 侧的
// constants.py。一旦有人手滑改动（例如为了快把 KDFIterations 调小、
// 或把 FilenameNonceLen「统一」成 16），Go 端产出的密文与密库将与 Python 端
// 不再互通，而症状是「文件打不开」「密库报密码错误」，不是编译错误。
// 每条的对照行号见 constants.go 的注释。
func TestWireFormatConstants(t *testing.T) {
	tests := []struct {
		name string
		got  any
		want any
	}{
		{"Magic", Magic, "CPRISM\x00\x01"},
		{"MagicLen", MagicLen, 8},
		{"Version", Version, uint32(1)},
		{"SaltLen", SaltLen, 16},
		{"IVLen", IVLen, 16},
		{"BlockSize", BlockSize, 16},

		{"KDFIterations", KDFIterations, 200_000},
		{"KeyLen", KeyLen, 32},
		{"KDFAlgo", KDFAlgo, "PBKDF2-HMAC-SHA256"},

		{"VaultMagic", VaultMagic, "CPRISM_VAULT"},
		{"VaultMagicLen", VaultMagicLen, 12},
		{"VaultMarkerName", VaultMarkerName, ".cloudprism_vault"},
		{"SyncIndexName", SyncIndexName, ".cloudprism_index"},
		{"VaultVersion", VaultVersion, uint32(3)},
		{"VaultIDLen", VaultIDLen, 16},
		{"VaultNameMaxLen", VaultNameMaxLen, 32},
		{"VaultVerifyMagic", VaultVerifyMagic, "CPV\x00"},
		{"VaultReservedLen", VaultReservedLen, 8},
		{"GCMTagLen", GCMTagLen, 16},
		{"RecoveryCodeLen", RecoveryCodeLen, 16},
		{"RecoverySecretLen", RecoverySecretLen, 10},
		{"FilenameNonceLen", FilenameNonceLen, 12},

		{"FileExtension", FileExtension, ".cpenc"},
		{"Base32Alphabet", Base32Alphabet, "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s = %v，应为 %v", tc.name, tc.got, tc.want)
			}
		})
	}
}

// TestBase32AlphabetMatchesStdEncoding 验证对外声明的字母表与 cryptox 实际使用的
// base32.StdEncoding 一致。
//
// 这条断言的价值在于它跨了两个事实源：Base32Alphabet 是从 Python 抄来的常量，
// 而文件名编解码走的是 Go 标准库。两者若不一致，Go 端产出的密文文件名
// Python 端就解不出来，且从任何一端的单测里都看不出来。
func TestBase32AlphabetMatchesStdEncoding(t *testing.T) {
	if len(Base32Alphabet) != 32 {
		t.Fatalf("Base32Alphabet 长度应为 32，实为 %d", len(Base32Alphabet))
	}
	for i := range 32 {
		// 单字节 b = i<<3 的高 5 位恰为 i，故编码结果的首字符就是字母表第 i 项
		got := base32.StdEncoding.EncodeToString([]byte{byte(i << 3)})[0]
		if got != Base32Alphabet[i] {
			t.Errorf("Base32Alphabet[%d] = %q，标准库为 %q", i, Base32Alphabet[i], got)
		}
	}
}

// TestLayoutArithmetic 锁定几个由常量推导出的布局算术，
// 防止有人改了长度常量却没意识到会连带改变文件布局。
func TestLayoutArithmetic(t *testing.T) {
	// .cpenc 头部：8+4+4+1+16+1+16+1 = 51
	const wantHeaderLen = 51
	if got := MagicLen + 4 + 4 + 1 + SaltLen + 1 + IVLen + 1; got != wantHeaderLen {
		t.Errorf("文件头长度应为 %d，实为 %d", wantHeaderLen, got)
	}

	// Vault Marker 明文前缀：12+4+16+16+16+4 = 68
	const wantVaultPrefix = 68
	if got := VaultMagicLen + 4 + VaultIDLen + SaltLen + IVLen + 4; got != wantVaultPrefix {
		t.Errorf("Vault 前缀长度应为 %d，实为 %d", wantVaultPrefix, got)
	}

	// 内部明文：1+4+8+4+2 = 19，其中 VerifyMagic 在偏移 13、NameLen 在偏移 17。
	// vault.py 的 docstring 曾把 VerifyMagic 写成偏移 9（其后 NameLen@17、Name@19
	// 又与代码一致，可见 docstring 自身就矛盾）；照 docstring 实现会让所有
	// 现存密库都打不开，且症状伪装成「主密码错误」。这两条断言就是防这个。
	const wantInnerHead = 19
	if got := 1 + 4 + VaultReservedLen + len(VaultVerifyMagic) + 2; got != wantInnerHead {
		t.Errorf("内部明文固定长度应为 %d，实为 %d", wantInnerHead, got)
	}
	if got := 1 + 4 + VaultReservedLen; got != 13 {
		t.Errorf("VerifyMagic 偏移应为 13，实为 %d（以 vault.py:99-106 的拼装顺序为准）", got)
	}
	if got := 1 + 4 + VaultReservedLen + len(VaultVerifyMagic); got != 17 {
		t.Errorf("NameLen 偏移应为 17，实为 %d", got)
	}

	// 恢复码：10 字节随机密钥的 Base32（去填充）恰为 16 字符
	if got := (RecoverySecretLen*8 + 4) / 5; got != RecoveryCodeLen {
		t.Errorf("恢复码字符数应为 %d，实为 %d", RecoveryCodeLen, got)
	}
}
