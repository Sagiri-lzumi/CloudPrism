package interop

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// wrongPassword 是一个必定不等于夹具主密码的取值。
//
// 刻意不用 "wrong" 这类短词：若某端把空密码或短密码走了特殊分支，
// 用长且含非 ASCII 的取值才能确保测的是「正常的密码校验失败」路径。
const wrongPassword = "绝非正确的主密码-not-the-password"

// metadataFrom 按夹具记录的字段构造 VaultMetadata。
//
// Name 用 NameIn（**未截断**的原始输入），这样 CreateMarker 必须自己完成
// 按字符数截断才能与 Python 产物逐字节相等 —— 截断规则差异会当场暴露。
func metadataFrom(t *testing.T, vv vaultVector) cryptox.VaultMetadata {
	t.Helper()

	return cryptox.VaultMetadata{
		Version:         vv.Version,
		VaultID:         mustHex(t, vv.VaultID, vv.Slug+" 的 vault_id"),
		Salt:            mustHex(t, vv.Salt, vv.Slug+" 的 salt"),
		IV:              mustHex(t, vv.IV, vv.Slug+" 的 iv"),
		FilenameEnc:     vv.Expect.FilenameEnc,
		ProtocolVersion: vv.ProtocolVersion,
		Name:            vv.NameIn,
	}
}

// TestVerifyPythonVaultMarkers 校验 Python 产出的 Vault Marker 并逐字段比对。
//
// GCM 标签通过即代表主密码正确、且内部明文布局两端一致。任何一个字段
// 错位（例如把 VerifyMagic 放到 docstring 声称的偏移 9 而非代码实际的 13）
// 都会让标签校验直接失败，症状伪装成「主密码错误」—— 这正是 vault.py
// 文档偏移缺陷最危险的地方，故本测试是开库能力的第一道闸。
func TestVerifyPythonVaultMarkers(t *testing.T) {
	v := loadVectors(t)

	for _, vv := range v.Vaults {
		t.Run(vv.Slug, func(t *testing.T) {
			data := readFixture(t, vv.Path)

			assertSHA256(t, vv.Slug+" Marker 夹具", sha256Hex(data), vv.BytesSHA256)
			if len(data) != vv.Length {
				t.Fatalf("Marker 长度 = %d，夹具声明 %d", len(data), vv.Length)
			}

			meta, err := cryptox.VerifyMarker(data, vv.Password)
			if err != nil {
				t.Fatalf("校验 Python 产出的 Marker 失败: %v", err)
			}

			if meta.Version != vv.Expect.Version {
				t.Errorf("version = %d，应为 %d", meta.Version, vv.Expect.Version)
			}
			if meta.Name != vv.Expect.Name {
				t.Errorf("name = %q，应为 %q", meta.Name, vv.Expect.Name)
			}
			if meta.FilenameEnc != vv.Expect.FilenameEnc {
				t.Errorf("filename_enc = %v，应为 %v", meta.FilenameEnc, vv.Expect.FilenameEnc)
			}
			if meta.ProtocolVersion != vv.Expect.ProtocolVersion {
				t.Errorf("protocol_version = %d，应为 %d", meta.ProtocolVersion, vv.Expect.ProtocolVersion)
			}
			if meta.HasRecovery != vv.Expect.HasRecovery {
				t.Errorf("has_recovery = %v，应为 %v", meta.HasRecovery, vv.Expect.HasRecovery)
			}
			if got, want := hex.EncodeToString(meta.VaultID), vv.Expect.VaultID; got != want {
				t.Errorf("vault_id = %s，应为 %s", got, want)
			}
			if got, want := hex.EncodeToString(meta.Salt), vv.Expect.Salt; got != want {
				t.Errorf("salt = %s，应为 %s", got, want)
			}
			if got, want := hex.EncodeToString(meta.IV), vv.Expect.IV; got != want {
				t.Errorf("iv = %s，应为 %s", got, want)
			}

			// 前缀字段必须与夹具顶层记录一致（两处来源相同，交叉核对可
			// 发现「VerifyMarker 读了偏移但返回了另一个字段」这类低级错误）
			if got, want := hex.EncodeToString(meta.Salt), vv.Salt; got != want {
				t.Errorf("salt 与夹具顶层记录不符：%s vs %s", got, want)
			}
			if meta.Version != vv.Version {
				t.Errorf("version 与夹具顶层记录不符：%d vs %d", meta.Version, vv.Version)
			}
		})
	}
}

// TestRebuildPythonVaultMarkerByteForByte 用定值 vault_id/salt/iv 在 Go 侧
// 重建 Marker，断言与 Python 产出逐字节相同。
//
// 恢复块由 Python 侧生成（内含随机盐）后固化在夹具里，Go 原样传入
// CreateMarker —— 随机性被冻结在夹具中，因此带恢复块的 Marker 同样能做
// 字节级比对。这条断言覆盖了 Marker 的完整布局：明文前缀 68 字节、
// 内部明文的 flag/protocol/reserved/VerifyMagic/NameLen/Name 拼装顺序、
// GCM-16 的 nonce 用法、以及尾部 recoveryLen + blob 的追加规则。
func TestRebuildPythonVaultMarkerByteForByte(t *testing.T) {
	v := loadVectors(t)

	for _, vv := range v.Vaults {
		t.Run(vv.Slug, func(t *testing.T) {
			want := readFixture(t, vv.Path)
			blob := mustHex(t, vv.RecoveryBlob, vv.Slug+" 的恢复块")

			// 恢复块为空 ⟺ has_recovery 为假，两者必须同真同假
			if (len(blob) > 0) != vv.Expect.HasRecovery {
				t.Fatalf("夹具自相矛盾：恢复块 %d 字节，has_recovery = %v", len(blob), vv.Expect.HasRecovery)
			}

			got, err := cryptox.CreateMarker(metadataFrom(t, vv), vv.Password, blob)
			if err != nil {
				t.Fatalf("CreateMarker 失败: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("Go 重建的 Marker 与 Python 产出逐字节不等\n  %s", firstDiff(got, want))
			}
		})
	}
}

// TestVaultWrongPasswordIsRejected 断言错误主密码被明确拒绝。
//
// 必须区分 ErrVaultPassword 与其它错误：上层要把它呈现为「主密码错误」，
// 而格式损坏应呈现为「密库文件损坏」。两类失败混成一个 error，用户就会
// 在文件损坏时反复重输密码。
func TestVaultWrongPasswordIsRejected(t *testing.T) {
	v := loadVectors(t)

	for _, vv := range v.Vaults {
		t.Run(vv.Slug, func(t *testing.T) {
			data := readFixture(t, vv.Path)

			_, err := cryptox.VerifyMarker(data, wrongPassword)
			if err == nil {
				t.Fatal("错误主密码竟然校验通过")
			}
			if !errors.Is(err, cryptox.ErrVaultPassword) {
				t.Errorf("错误主密码应返回 ErrVaultPassword，实为 %v", err)
			}

			// 空密码同样必须被拒：某些实现会把空串走短路分支
			if _, err := cryptox.VerifyMarker(data, ""); !errors.Is(err, cryptox.ErrVaultPassword) {
				t.Errorf("空密码应返回 ErrVaultPassword，实为 %v", err)
			}
		})
	}
}

// TestVaultSplitRecoveryTailMatchesPython 比对恢复块尾部的切分结果。
//
// RenameVault 依赖它「原样保留恢复块」：切分点算错一位，改名后的密库
// 就永久失去恢复能力，而用户当时不会看到任何异常。
func TestVaultSplitRecoveryTailMatchesPython(t *testing.T) {
	v := loadVectors(t)

	for _, vv := range v.Vaults {
		t.Run(vv.Slug, func(t *testing.T) {
			data := readFixture(t, vv.Path)

			head, tail := cryptox.SplitRecoveryTail(data)
			if len(head) != vv.Split.HeadLen {
				t.Errorf("head 长度 = %d，Python 侧为 %d", len(head), vv.Split.HeadLen)
			}
			if got, want := hex.EncodeToString(tail), vv.Split.TailHex; got != want {
				t.Errorf("tail = %s，Python 侧为 %s", got, want)
			}
			// 切分必须无损：head ‖ tail 还原出原文件
			if !bytes.Equal(append(append([]byte(nil), head...), tail...), data) {
				t.Error("head ‖ tail 与原文件不等，切分丢了字节")
			}
		})
	}
}

// TestRecoveryBlobRoundTrip 覆盖恢复块的完整生命周期。
//
// 恢复码是用户忘记主密码时的唯一出路，且**服务端零参与**（离线保存），
// 一旦两端规则不一致就没有任何补救机会 —— 因此这里把编码、解码、
// 加密、解密、尾部封装全部钉死。
func TestRecoveryBlobRoundTrip(t *testing.T) {
	v := loadVectors(t)

	seen := 0
	for _, vv := range v.Vaults {
		if vv.RecoveryBlob == "" {
			continue
		}
		seen++
		t.Run(vv.Slug, func(t *testing.T) {
			blob := mustHex(t, vv.RecoveryBlob, vv.Slug+" 的恢复块")
			secret := mustHex(t, vv.RecoverySecret, vv.Slug+" 的恢复密钥")

			// 1. 恢复码 <-> 随机密钥：Base32 无填充，10 字节 ↔ 16 字符
			if len(secret) != protocol.RecoverySecretLen {
				t.Errorf("恢复密钥长度 = %d，应为 %d", len(secret), protocol.RecoverySecretLen)
			}
			if len(vv.RecoveryCode) != protocol.RecoveryCodeLen {
				t.Errorf("恢复码长度 = %d，应为 %d", len(vv.RecoveryCode), protocol.RecoveryCodeLen)
			}
			decoded, err := cryptox.B32DecodeNoPad(vv.RecoveryCode)
			if err != nil {
				t.Fatalf("解码恢复码失败: %v", err)
			}
			if !bytes.Equal(decoded, secret) {
				t.Errorf("恢复码解出 %x，应为 %x", decoded, secret)
			}
			if got := cryptox.B32EncodeNoPad(secret); got != vv.RecoveryCode {
				t.Errorf("恢复密钥编码为 %q，应为 %q", got, vv.RecoveryCode)
			}

			// 2. 用恢复密钥解开恢复块，必须得到主密码本身
			got, err := cryptox.DecryptRecoveryBlob(blob, secret)
			if err != nil {
				t.Fatalf("解密恢复块失败: %v", err)
			}
			if got != vv.Password {
				t.Errorf("恢复块解出 %q，应为 %q", got, vv.Password)
			}

			// 3. 错误的恢复密钥必须被 GCM 标签拦下，且报的是「恢复码错误」
			wrongSecret := make([]byte, len(secret))
			copy(wrongSecret, secret)
			wrongSecret[0] ^= 0xFF
			if _, err := cryptox.DecryptRecoveryBlob(blob, wrongSecret); !errors.Is(err, cryptox.ErrRecoverySecret) {
				t.Errorf("错误恢复密钥应返回 ErrRecoverySecret，实为 %v", err)
			}

			// 4. Marker 尾部 = uint16be(len(blob)) ‖ blob
			wantTail := make([]byte, 2+len(blob))
			binary.BigEndian.PutUint16(wantTail, uint16(len(blob)))
			copy(wantTail[2:], blob)
			if got, want := vv.Split.TailHex, hex.EncodeToString(wantTail); got != want {
				t.Errorf("Marker 尾部 = %s，应为 %s", got, want)
			}
		})
	}

	if seen == 0 {
		t.Fatal("夹具里没有任何带恢复块的 Marker")
	}
}

// TestVaultNameTruncationIsByRunes 钉死「名称按字符数而非字节数截断」。
//
// Python 侧 vault.py:96 是 meta.name[:32]，切的是**字符**；Go 若按字节截，
// 全中文名称下会切坏 UTF-8 且长度差三倍，两端 Marker 不再逐字节相等。
// 这条差异在纯 ASCII 名称下完全测不出来，故夹具专门放了一个 40 个汉字
// 的用例（见 gen_vectors.py 的 VAULT_CASES）。
func TestVaultNameTruncationIsByRunes(t *testing.T) {
	v := loadVectors(t)

	var trunc *vaultVector
	for i := range v.Vaults {
		if utf8.RuneCountInString(v.Vaults[i].NameIn) > protocol.VaultNameMaxLen {
			trunc = &v.Vaults[i]
			break
		}
	}
	if trunc == nil {
		t.Fatal("夹具里没有超长名称用例，按字符截断的规则失去覆盖")
	}

	t.Logf("用例 %s：输入 %d 字符 / %d 字节", trunc.Slug,
		utf8.RuneCountInString(trunc.NameIn), len(trunc.NameIn))

	// Python 侧 verify 回来的名称必须正好是上限个字符，且是多字节字符
	if got := utf8.RuneCountInString(trunc.Expect.Name); got != protocol.VaultNameMaxLen {
		t.Fatalf("截断后名称 = %d 字符，应为 %d", got, protocol.VaultNameMaxLen)
	}
	// 字节数必须明显大于字符数，否则说明截的是字节而不是字符
	if byteLen := len(trunc.Expect.Name); byteLen <= protocol.VaultNameMaxLen {
		t.Errorf("截断后名称只有 %d 字节，说明是按字节截的（多字节名称下应远大于 %d）",
			byteLen, protocol.VaultNameMaxLen)
	}
	if !utf8.ValidString(trunc.Expect.Name) {
		t.Error("截断后的名称不是合法 UTF-8，说明切在了多字节字符中间")
	}
	// 截断结果必须是输入的前缀
	if !strings.HasPrefix(trunc.NameIn, trunc.Expect.Name) {
		t.Error("截断后的名称不是原始输入的前缀")
	}

	// 用未截断的原始输入重建 Marker，必须与 Python 产物逐字节相同
	want := readFixture(t, trunc.Path)
	got, err := cryptox.CreateMarker(metadataFrom(t, *trunc), trunc.Password,
		mustHex(t, trunc.RecoveryBlob, trunc.Slug+" 的恢复块"))
	if err != nil {
		t.Fatalf("CreateMarker 失败: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("超长名称下 Go 重建的 Marker 与 Python 不等\n  %s", firstDiff(got, want))
	}

	// Go 自己截断的结果也必须与 Python 一致
	meta, err := cryptox.VerifyMarker(got, trunc.Password)
	if err != nil {
		t.Fatalf("校验自己刚构造的 Marker 失败: %v", err)
	}
	if meta.Name != trunc.Expect.Name {
		t.Errorf("Go 截断结果 = %q，Python 为 %q", meta.Name, trunc.Expect.Name)
	}
}

// TestDecryptPythonFilenames 解密 Python 侧 FilenameCipher.encrypt 的产物。
//
// 覆盖 CJK、空格、括号、多点号与 emoji（4 字节 UTF-8）。文件名一旦解错，
// 用户看到的就是乱码目录树，且没有任何报错。
func TestDecryptPythonFilenames(t *testing.T) {
	v := loadVectors(t)

	fnKey := mustHex(t, v.FilenameKey.Key, "文件名密钥")
	if len(fnKey) != protocol.KeyLen {
		t.Fatalf("文件名密钥长度 = %d，应为 %d", len(fnKey), protocol.KeyLen)
	}

	for i, nv := range v.Filenames {
		t.Run(nv.Plain, func(t *testing.T) {
			got, err := cryptox.DecryptFilename(nv.Enc, fnKey)
			if err != nil {
				t.Fatalf("第 %d 条文件名解密失败: %v", i, err)
			}
			if got != nv.Plain {
				t.Errorf("第 %d 条解出 %q，应为 %q", i, got, nv.Plain)
			}

			// 编码产物必须是去填充 Base32，且长度符合 nonce+ct+tag 的结构
			if !isBase32NoPad(nv.Enc) {
				t.Errorf("第 %d 条编码 %q 含 Base32 字母表外字符", i, nv.Enc)
			}
			raw, err := cryptox.B32DecodeNoPad(nv.Enc)
			if err != nil {
				t.Fatalf("第 %d 条 Base32 解码失败: %v", i, err)
			}
			if want := protocol.FilenameNonceLen + len(nv.Plain) + protocol.GCMTagLen; len(raw) != want {
				t.Errorf("第 %d 条解码后 %d 字节，应为 nonce(%d)+明文(%d)+tag(%d)=%d",
					i, len(raw), protocol.FilenameNonceLen, len(nv.Plain), protocol.GCMTagLen, want)
			}
		})
	}
}

// TestFilenameRejectsTampering 断言被篡改的文件名密文解不开。
//
// GCM 的认证标签是这里唯一的完整性保障：云盘侧改名、截断、位翻转都
// 必须表现为「解不开 → 回退显示密文名」，而不是显示一个错误的名字。
func TestFilenameRejectsTampering(t *testing.T) {
	v := loadVectors(t)

	fnKey := mustHex(t, v.FilenameKey.Key, "文件名密钥")
	nv := v.Filenames[0]

	raw, err := cryptox.B32DecodeNoPad(nv.Enc)
	if err != nil {
		t.Fatalf("Base32 解码失败: %v", err)
	}

	cases := []struct {
		name string
		enc  string
	}{
		// 三段分别翻转：nonce / 密文 / 标签，任一字节变动都必须让 GCM 失败
		{"nonce 位翻转", flipByteB32(raw, 0)},
		{"密文位翻转", flipByteB32(raw, protocol.FilenameNonceLen)},
		{"标签位翻转", flipByteB32(raw, len(raw)-1)},
		// 云盘侧截断文件名：尾部少两个 Base32 字符
		{"整体截断", nv.Enc[:len(nv.Enc)-2]},
		{"空字符串", ""},
		// 含 Base32 字母表外字符：解码阶段就该失败，不得走到 GCM
		{"非法字符", nv.Enc[:4] + "!" + nv.Enc[5:]},
	}

	for _, c := range cases {
		if _, err := cryptox.DecryptFilename(c.enc, fnKey); err == nil {
			t.Errorf("%s：篡改后的文件名竟然解密成功", c.name)
		}
	}

	// 正确的密钥换一把也必须失败
	otherKey := make([]byte, len(fnKey))
	copy(otherKey, fnKey)
	otherKey[0] ^= 0xFF
	if _, err := cryptox.DecryptFilename(nv.Enc, otherKey); err == nil {
		t.Error("换了密钥竟然解密成功")
	}
}

// flipByteB32 复制一份、把第 i 字节取反，再编码回去填充 Base32。
//
// 返回 Base32 而不是原始字节，是为了让用例表里的每一项都能直接送给
// DecryptFilename，不必在调用处再做一次编码（混着两种形态极易写错）。
func flipByteB32(src []byte, i int) string {
	out := make([]byte, len(src))
	copy(out, src)
	out[i] ^= 0xFF
	return cryptox.B32EncodeNoPad(out)
}
