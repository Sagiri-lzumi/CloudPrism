package cryptox

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// Vault Marker 黄金向量。全部由 WindowsPy 端 VaultMarker.create / verify /
// split_recovery_tail **真实运行后打印**（脚本见 Release/_spike/gen_golden.py，
// 调用的是 vault.py:75-286 的生产代码，不是复刻实现），因此下面这些十六进制
// 常量是「Go 与 Python 产出逐字节相等」的实证，而非 Go 侧自查拼装的结果。
//
// 固定输入：vault_id = 00..0f、salt = 0x33*16、iv = 0x44*16、主密码 "hunter2"。
// 定值而非随机，是因为 GCM 在 (key, nonce, plaintext) 全定时输出完全确定 ——
// 这让「完整字节序列」成为可断言的对象，是最强的兼容证明。
const (
	goldenVaultID       = "000102030405060708090a0b0c0d0e0f"
	goldenVaultSalt     = "33333333333333333333333333333333"
	goldenVaultIV       = "44444444444444444444444444444444"
	goldenVaultPassword = "hunter2"
	goldenVaultName     = "我的密库 Vault-1"

	// v3 + 恢复块：68(前缀) + 55(载荷) + 2(恢复块长度) + 39(恢复块) = 164
	goldenMarkerV3 = "43505249534d5f5641554c54" + "00000003" +
		goldenVaultID + goldenVaultSalt + goldenVaultIV + "00000037" +
		"381d503d3e51269861578b7218cb608cc883ea1c1997e881e3322200950485870aeb4e8a0fe812c17616aa091eb46e28086fa65277e4df" +
		"0027" + goldenRecoveryBlob

	// v3 无恢复块：68 + 55 = 123
	goldenMarkerV3NoRecovery = "43505249534d5f5641554c54" + "00000003" +
		goldenVaultID + goldenVaultSalt + goldenVaultIV + "00000037" +
		"381d503d3e51269861578b7218cb608cc883ea1c1997e881e3322200950485870aeb4e8a0fe812c17616aa091eb46e28086fa65277e4df"

	// v2：与 v3 无恢复块**只差版本号那 4 字节**，载荷完全相同
	goldenMarkerV2 = "43505249534d5f5641554c54" + "00000002" +
		goldenVaultID + goldenVaultSalt + goldenVaultIV + "00000037" +
		"381d503d3e51269861578b7218cb608cc883ea1c1997e881e3322200950485870aeb4e8a0fe812c17616aa091eb46e28086fa65277e4df"

	// v1（名称为空串，但 create 仍写出 NameLen=0 字段）：
	// inner = 19 字节 → 载荷 35 = 0x23 → 全长 103
	goldenMarkerV1 = "43505249534d5f5641554c54" + "00000001" +
		goldenVaultID + goldenVaultSalt + goldenVaultIV + "00000023" +
		"381d503d3e51269861578b7218cb608cc883fe0d1711a0d152899d31c06e8941c20687"

	// 名称用满 32 字符上限。Python 侧实测：32 字符、33 字符、40 字符
	// 三者产出的 Marker **hex 完全相同**（见 TestNameTruncationIsByRunes）
	goldenMarkerName32 = "43505249534d5f5641554c54" + "00000003" +
		goldenVaultID + goldenVaultSalt + goldenVaultIV + "00000083" +
		"381d503d3e51269861578b7218cb608cc8839e1f3c91eab6f032201195138142f11dde4bec208eee2ebda2b6d00b427fecd0991a15c50e8e7f028c935a1c57b5403a1aa21850f7fb6d686fa1686102edfc61268145bcc90f3a086a863f8028fd7d1863b7122083c1cb719ec6c49cd7a60af9fa51289fad741b5f3dd6cd43d34532b2ce"

	// ---------------------------------------------------------------
	// 畸形 / 历史布局。这些无法用 create 产出（它恒写 NameLen 且名称恒为
	// 合法 UTF-8），Python 侧是手工拼装内部明文后再 GCM 加密得到的。
	// 它们专门用于锁定 verify 的「容错解析」分支。
	// ---------------------------------------------------------------

	// 真 v1：内部明文到 VerifyMagic 即结束（17 字节），没有 NameLen 字段
	goldenOddV1NoName = "43505249534d5f5641554c54" + "00000001" +
		goldenVaultID + goldenVaultSalt + goldenVaultIV + "00000021" +
		"381d503d3e51269861578b7218cb608cc84e4517e0c5b0e057d2cad72de883071b"

	// NameLen 声明 100，实际只跟了 2 个名称字节
	goldenOddNameLenOver = "43505249534d5f5641554c54" + "00000003" +
		goldenVaultID + goldenVaultSalt + goldenVaultIV + "00000025" +
		"381d503d3e51269861578b7218cb608cc8839a9bf3d1db6e54db50195df9554a7d8324f8c0"

	// 名称是非法 UTF-8（\xff\xfe）
	goldenOddNameBadUTF8 = "43505249534d5f5641554c54" + "00000003" +
		goldenVaultID + goldenVaultSalt + goldenVaultIV + "00000025" +
		"381d503d3e51269861578b7218cb608cc883fc056f0bf747bb09bb67786a620a31d54a5da2"

	// 内部明文只有 3 字节，装不下 flag(1) + ProtocolVersion(4)
	goldenOddInnerShort = "43505249534d5f5641554c54" + "00000003" +
		goldenVaultID + goldenVaultSalt + goldenVaultIV + "00000013" +
		"381d509514811b7b4091d00c4833614549f308"

	// filename_enc 标志取 0x02（既非 0x00 也非 0x01）
	goldenOddFlagTwo = "43505249534d5f5641554c54" + "00000003" +
		goldenVaultID + goldenVaultSalt + goldenVaultIV + "00000023" +
		"3b1d503d3e51269861578b7218cb608cc883fe53d9a79724320cbff8ee20a161d12163"

	// 恢复块长度声明 100、实际只有 3 字节
	goldenOddRecoveryOver = "43505249534d5f5641554c54" + "00000003" +
		goldenVaultID + goldenVaultSalt + goldenVaultIV + "00000023" +
		"381d503d3e51269861578b7218cb608cc883fe0d1711a0d152899d31c06e8941c20687" +
		"0064616263"

	// 尾部只剩 1 字节，连 uint16 长度字段都读不满
	goldenOddRecoveryOneByte = "43505249534d5f5641554c54" + "00000003" +
		goldenVaultID + goldenVaultSalt + goldenVaultIV + "00000023" +
		"381d503d3e51269861578b7218cb608cc883fe0d1711a0d152899d31c06e8941c20687" +
		"05"

	// ---------------------------------------------------------------
	// 恢复码块：rsalt = 0x77*16（定值）、随机密钥 = 00..09、主密码 "hunter2"
	// rkey 的黄金向量（goldenRecoveryRKey）定义在 kdf_test.go，同包直接引用
	// ---------------------------------------------------------------

	goldenRecoverySecret = "00010203040506070809"
	goldenRecoveryRSalt  = "77777777777777777777777777777777"
	// rsalt(16) ‖ ct(7) ‖ tag(16) = 39 字节
	goldenRecoveryBlob = "77777777777777777777777777777777" +
		"edf9eb454639f66635dcbeb588f99c965521629a63061d"
	// 恢复码 = 随机密钥的 Base32（16 字符），用户离线保存的就是这一串
	goldenRecoveryCode = "AAAQEAYEAUDAOCAJ"
)

// goldenMarkerV3PayloadLen 是 goldenMarkerV3 里 PayloadLen 字段的取值（0x37 = 55）。
// 前缀(68) + 载荷(55) = 123 就是恢复块尾部的起点，多处截断测试拿它当分界。
const goldenMarkerV3PayloadLen = 0x37

// goldenMarkerV3HeadLen 是 goldenMarkerV3 剥掉恢复块尾部后的长度。
const goldenMarkerV3HeadLen = vaultPrefixLen + goldenMarkerV3PayloadLen

// goldenVaultMeta 构造与黄金向量完全对应的 Metadata。
func goldenVaultMeta(version uint32, name string) VaultMetadata {
	return VaultMetadata{
		Version:         version,
		VaultID:         mustHexTB(goldenVaultID),
		Salt:            mustHexTB(goldenVaultSalt),
		IV:              mustHexTB(goldenVaultIV),
		FilenameEnc:     true,
		ProtocolVersion: protocol.Version,
		Name:            name,
	}
}

// mustHexTB 是 mustHex 的无 *testing.T 版本，供包级 helper 使用。
func mustHexTB(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("mustHexTB: " + err.Error())
	}
	return b
}

// TestCreateMarkerGolden 是本包**最重要**的一条测试：
// 固定 vault_id/salt/iv/名称/主密码下，Go 产出的 Marker 必须与 Python 端
// 逐字节相同（断言完整字节序列的 hex，而不是按字段各自检查）。
//
// 按字段自查只能证明「Go 与自己一致」；只有整串 hex 相等才能证明
// 「Go 与 Python 一致」。这条断言一旦通过，两端就能互开对方建的密库。
func TestCreateMarkerGolden(t *testing.T) {
	recoveryBlob := mustHex(t, goldenRecoveryBlob)

	tests := []struct {
		name     string
		meta     VaultMetadata
		recovery []byte
		want     string
		wantLen  int
	}{
		{"v3 含恢复块", goldenVaultMeta(3, goldenVaultName), recoveryBlob, goldenMarkerV3, 164},
		{"v3 无恢复块", goldenVaultMeta(3, goldenVaultName), nil, goldenMarkerV3NoRecovery, 123},
		// Python 侧 `if recovery_blob:` 对空切片同样为假，故一个字节都不追加
		{"v3 传空恢复块", goldenVaultMeta(3, goldenVaultName), []byte{}, goldenMarkerV3NoRecovery, 123},
		{"v2", goldenVaultMeta(2, goldenVaultName), nil, goldenMarkerV2, 123},
		{"v1 空名称", goldenVaultMeta(1, ""), nil, goldenMarkerV1, 103},
		{"v3 名称用满 32 字符", goldenVaultMeta(3, strings.Repeat("字", 32)), nil, goldenMarkerName32, 199},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CreateMarker(tc.meta, goldenVaultPassword, tc.recovery)
			if err != nil {
				t.Fatalf("CreateMarker 失败: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Errorf("Marker 长度 = %d，Python 端为 %d", len(got), tc.wantLen)
			}
			if hex.EncodeToString(got) != tc.want {
				t.Errorf("Marker 字节与 Python 端不一致\n got = %x\nwant = %s", got, tc.want)
			}
		})
	}
}

// TestVerifyMarkerGolden 反向验证：Go 必须能解析 Python 产出的 Marker，
// 且每个字段都与 Python 侧 verify 的返回值一致。
func TestVerifyMarkerGolden(t *testing.T) {
	tests := []struct {
		name        string
		marker      string
		wantVersion uint32
		wantName    string
		wantRecover bool
	}{
		{"v3 含恢复块", goldenMarkerV3, 3, goldenVaultName, true},
		{"v3 无恢复块", goldenMarkerV3NoRecovery, 3, goldenVaultName, false},
		{"v2", goldenMarkerV2, 2, goldenVaultName, false},
		{"v1 空名称", goldenMarkerV1, 1, "", false},
		{"v3 名称用满 32 字符", goldenMarkerName32, 3, strings.Repeat("字", 32), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := VerifyMarker(mustHex(t, tc.marker), goldenVaultPassword)
			if err != nil {
				t.Fatalf("VerifyMarker 失败: %v", err)
			}

			if meta.Version != tc.wantVersion {
				t.Errorf("Version = %d，应为 %d", meta.Version, tc.wantVersion)
			}
			if meta.Name != tc.wantName {
				t.Errorf("Name = %q，应为 %q", meta.Name, tc.wantName)
			}
			if !meta.FilenameEnc {
				t.Error("FilenameEnc = false，应为 true")
			}
			if meta.ProtocolVersion != protocol.Version {
				t.Errorf("ProtocolVersion = %d，应为 %d", meta.ProtocolVersion, protocol.Version)
			}
			if meta.HasRecovery != tc.wantRecover {
				t.Errorf("HasRecovery = %v，应为 %v", meta.HasRecovery, tc.wantRecover)
			}
			// 前缀字段必须原样回读，否则重命名等重写场景会改掉密库身份
			if !bytes.Equal(meta.VaultID, mustHex(t, goldenVaultID)) {
				t.Errorf("VaultID = %x，应为 %s", meta.VaultID, goldenVaultID)
			}
			if !bytes.Equal(meta.Salt, mustHex(t, goldenVaultSalt)) {
				t.Errorf("Salt = %x，应为 %s", meta.Salt, goldenVaultSalt)
			}
			if !bytes.Equal(meta.IV, mustHex(t, goldenVaultIV)) {
				t.Errorf("IV = %x，应为 %s", meta.IV, goldenVaultIV)
			}
		})
	}
}

// TestCreateVerifyRoundTrip 覆盖随机字段的真实生产路径：
// GenerateMetadata 产出的 Metadata 建库后必须能原样校验回来。
func TestCreateVerifyRoundTrip(t *testing.T) {
	for _, filenameEnc := range []bool{false, true} {
		for _, name := range []string{"", "简单", "Mixed-Name_123", strings.Repeat("长", 32)} {
			meta, err := GenerateMetadata(filenameEnc, name)
			if err != nil {
				t.Fatalf("GenerateMetadata 失败: %v", err)
			}

			blob, err := BuildRecoveryBlob(mustHex(t, goldenRecoverySecret), goldenVaultPassword)
			if err != nil {
				t.Fatalf("BuildRecoveryBlob 失败: %v", err)
			}
			data, err := CreateMarker(meta, goldenVaultPassword, blob)
			if err != nil {
				t.Fatalf("CreateMarker 失败: %v", err)
			}

			got, err := VerifyMarker(data, goldenVaultPassword)
			if err != nil {
				t.Fatalf("VerifyMarker 失败: %v", err)
			}
			if got.FilenameEnc != filenameEnc {
				t.Errorf("FilenameEnc = %v，应为 %v", got.FilenameEnc, filenameEnc)
			}
			if got.Name != name {
				t.Errorf("Name = %q，应为 %q", got.Name, name)
			}
			if !got.HasRecovery {
				t.Error("带恢复块建库后 HasRecovery 应为 true")
			}
			if !bytes.Equal(got.VaultID, meta.VaultID) ||
				!bytes.Equal(got.Salt, meta.Salt) ||
				!bytes.Equal(got.IV, meta.IV) {
				t.Error("前缀字段往返后发生了变化")
			}

			// 恢复块必须能原样拆出来并解出主密码
			_, tail := SplitRecoveryTail(data)
			password, err := DecryptRecoveryBlob(tail[2:], mustHex(t, goldenRecoverySecret))
			if err != nil {
				t.Fatalf("DecryptRecoveryBlob 失败: %v", err)
			}
			if password != goldenVaultPassword {
				t.Errorf("恢复块解出 %q，应为 %q", password, goldenVaultPassword)
			}
		}
	}
}

// TestVerifyMarkerWrongPassword 锁定「GCM 标签即密码校验器」这条语义：
// 密码错一个字符就必须失败，且失败原因必须是 ErrVaultPassword ——
// 上层据此显示「主密码错误」，与 Python 侧 verify 返回 None 后的处理一致。
func TestVerifyMarkerWrongPassword(t *testing.T) {
	markers := map[string]string{
		"v3 含恢复块": goldenMarkerV3,
		"v3 无恢复块": goldenMarkerV3NoRecovery,
		"v2":      goldenMarkerV2,
		"v1":      goldenMarkerV1,
	}

	for name, marker := range markers {
		for _, password := range []string{"wrong", "hunter3", "Hunter2", "", "hunter2 "} {
			t.Run(fmt.Sprintf("%s/密码=%q", name, password), func(t *testing.T) {
				_, err := VerifyMarker(mustHex(t, marker), password)
				if !errors.Is(err, ErrVaultPassword) {
					t.Errorf("应返回 ErrVaultPassword，实为 %v", err)
				}
			})
		}
	}
}

// TestVerifyMarkerRejectsMalformed 覆盖前缀层面的畸形输入。
//
// Python 侧靠 BytesIO.read 读不满返回 None 天然免疫越界；Go 的切片越界会
// **panic**，因此这些输入在 Go 侧是真实可达的崩溃点，必须逐条拦住。
func TestVerifyMarkerRejectsMalformed(t *testing.T) {
	full := mustHex(t, goldenMarkerV3)

	t.Run("魔数不符", func(t *testing.T) {
		bad := append([]byte(nil), full...)
		copy(bad, "CPRISM_VAULX") // 只改最后一个魔数字节
		if _, err := VerifyMarker(bad, goldenVaultPassword); !errors.Is(err, ErrVaultMagic) {
			t.Errorf("应返回 ErrVaultMagic，实为 %v", err)
		}
	})

	// 截断点落在载荷之内（n < 123）时必须被拦下，且不许 panic。
	t.Run("载荷被截断", func(t *testing.T) {
		for n := range goldenMarkerV3HeadLen {
			_, err := VerifyMarker(full[:n], goldenVaultPassword)
			// 截断后可能是「数据不足」也可能是「魔数不完整」，两者都算正确拦截，
			// 唯独不许 panic，更不许校验成功
			if err == nil {
				t.Fatalf("截断到 %d 字节竟然校验成功了", n)
			}
			if !errors.Is(err, ErrVaultShort) && !errors.Is(err, ErrVaultMagic) {
				t.Errorf("截断到 %d 字节返回了未预期的错误 %v", n, err)
			}
		}
	})

	// 只截掉恢复块尾部（n >= 123）时 verify 必须照常成功：恢复块是「额外能力」
	// 而非开库前提，若因它残缺就拒绝开库，用户看到的会是「主密码错误」这类
	// 完全指错方向的提示。
	//
	// hasRecovery 的探测条件是 tail >= 2 字节且 recoveryLen <= len(tail)-2，
	// 因此只有完整的 41 字节尾部（2 + 39）会被认出来，其余一律降级为 false。
	t.Run("截掉恢复块尾部仍可开库", func(t *testing.T) {
		for n := goldenMarkerV3HeadLen; n <= len(full); n++ {
			meta, err := VerifyMarker(full[:n], goldenVaultPassword)
			if err != nil {
				t.Fatalf("截断到 %d 字节应仍可开库，实为 %v", n, err)
			}
			if want := n == len(full); meta.HasRecovery != want {
				t.Errorf("截断到 %d 字节时 HasRecovery = %v，应为 %v", n, meta.HasRecovery, want)
			}
			// 载荷未被触碰，其余字段一个都不许变
			if meta.Version != 3 || meta.Name != goldenVaultName || !meta.FilenameEnc {
				t.Errorf("截断到 %d 字节时字段被污染: %+v", n, meta)
			}
		}
	})

	t.Run("PayloadLen 被篡改成天文数字", func(t *testing.T) {
		// 这是最容易 panic 的一处：PayloadLen 取自文件本身，
		// 不与真实长度核对就切片会直接越界
		bad := append([]byte(nil), full...)
		bad[64], bad[65], bad[66], bad[67] = 0xFF, 0xFF, 0xFF, 0xFF
		if _, err := VerifyMarker(bad, goldenVaultPassword); !errors.Is(err, ErrVaultShort) {
			t.Errorf("应返回 ErrVaultShort，实为 %v", err)
		}
	})

	t.Run("PayloadLen 小于标签长度", func(t *testing.T) {
		bad := append([]byte(nil), full...)
		bad[64], bad[65], bad[66], bad[67] = 0, 0, 0, 8
		if _, err := VerifyMarker(bad, goldenVaultPassword); !errors.Is(err, ErrVaultShort) {
			t.Errorf("应返回 ErrVaultShort，实为 %v", err)
		}
	})

	t.Run("空输入", func(t *testing.T) {
		if _, err := VerifyMarker(nil, goldenVaultPassword); !errors.Is(err, ErrVaultShort) {
			t.Errorf("应返回 ErrVaultShort，实为 %v", err)
		}
	})
}

// TestVerifyMarkerTolerantParsing 锁定 verify 的「容错解析」分支。
//
// wantPython 字段记录的是 **Python 侧对同一输入的真实反应**（实测，非推测）：
// "ok" 表示返回 Metadata，"raise:struct.error" 表示抛出未捕获异常。
// Go 把后者折叠成 ErrVaultShort —— 可观测行为不变（两端都不会在载荷畸形时
// 返回一个「成功」的 metadata），但 Go 不会让异常逃出到调用方。
func TestVerifyMarkerTolerantParsing(t *testing.T) {
	tests := []struct {
		name         string
		marker       string
		wantPython   string // Python 侧实测反应
		wantErr      error  // Go 侧预期错误（nil 表示应成功）
		wantVersion  uint32
		wantName     string
		wantFileEnc  bool
		wantRecovery bool
	}{
		{
			name: "真 v1（内部明文无 NameLen 字段）", marker: goldenOddV1NoName,
			wantPython: "ok", wantVersion: 1, wantName: "", wantFileEnc: true,
		},
		{
			name: "NameLen 声明 100 实际只有 2 字节", marker: goldenOddNameLenOver,
			wantPython: "ok", wantVersion: 3, wantName: "", wantFileEnc: true,
		},
		{
			name: "名称是非法 UTF-8", marker: goldenOddNameBadUTF8,
			wantPython: "ok", wantVersion: 3, wantName: "", wantFileEnc: true,
		},
		{
			name: "内部明文只有 3 字节", marker: goldenOddInnerShort,
			wantPython: "raise:struct.error", wantErr: ErrVaultShort,
		},
		{
			// Python 侧是 `inner[0] == 0x01`，0x02 得 False；Go 同样写法
			name: "filename_enc 标志取 0x02", marker: goldenOddFlagTwo,
			wantPython: "ok", wantVersion: 3, wantName: "", wantFileEnc: false,
		},
		{
			name: "恢复块长度声明超出实际尾部", marker: goldenOddRecoveryOver,
			wantPython: "ok", wantVersion: 3, wantName: "", wantFileEnc: true, wantRecovery: false,
		},
		{
			name: "尾部只剩 1 字节", marker: goldenOddRecoveryOneByte,
			wantPython: "ok", wantVersion: 3, wantName: "", wantFileEnc: true, wantRecovery: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := VerifyMarker(mustHex(t, tc.marker), goldenVaultPassword)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("应返回 %v（Python 侧为 %s），实为 %v", tc.wantErr, tc.wantPython, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyMarker 失败（Python 侧为 %s）: %v", tc.wantPython, err)
			}

			if meta.Version != tc.wantVersion {
				t.Errorf("Version = %d，应为 %d", meta.Version, tc.wantVersion)
			}
			if meta.Name != tc.wantName {
				t.Errorf("Name = %q，应为 %q（异常布局必须回退空名称而非阻断校验）",
					meta.Name, tc.wantName)
			}
			if meta.FilenameEnc != tc.wantFileEnc {
				t.Errorf("FilenameEnc = %v，应为 %v", meta.FilenameEnc, tc.wantFileEnc)
			}
			if meta.HasRecovery != tc.wantRecovery {
				t.Errorf("HasRecovery = %v，应为 %v", meta.HasRecovery, tc.wantRecovery)
			}
			if meta.ProtocolVersion != protocol.Version {
				t.Errorf("ProtocolVersion = %d，应为 %d", meta.ProtocolVersion, protocol.Version)
			}
		})
	}
}

// TestSplitRecoveryTailGolden 用 Python 侧实测的 (head 长度, tail hex) 逐条对拍。
//
// 这条函数的用途是重命名密库时**原样搬走**恢复块：重写 Marker 只改内部明文里的
// 名称，恢复块必须字节不动，否则用户手里的恢复码会立即失效。
func TestSplitRecoveryTailGolden(t *testing.T) {
	tests := []struct {
		name       string
		marker     string
		wantHead   int
		wantTail   string
		wantPython string // Python 侧 split_recovery_tail 的实测结果
	}{
		{"v3 含恢复块", goldenMarkerV3, 123, goldenRecoveryTailHex, "123 | 与 Go 相同"},
		{"v3 无恢复块", goldenMarkerV3NoRecovery, 123, "", "123 | 空"},
		{"v2", goldenMarkerV2, 123, "", "123 | 空"},
		{"v1", goldenMarkerV1, 103, "", "103 | 空"},
		{"真 v1 无 NameLen", goldenOddV1NoName, 101, "", "101 | 空"},
		{"NameLen 越界", goldenOddNameLenOver, 105, "", "105 | 空"},
		{"名称非法 UTF-8", goldenOddNameBadUTF8, 105, "", "105 | 空"},
		{"内部明文过短", goldenOddInnerShort, 87, "", "87 | 空"},
		{"标志取 0x02", goldenOddFlagTwo, 103, "", "103 | 空"},
		{"恢复块长度越界", goldenOddRecoveryOver, 103, "0064616263", "103 | 0064616263"},
		{"尾部仅 1 字节", goldenOddRecoveryOneByte, 103, "05", "103 | 05"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := mustHex(t, tc.marker)
			head, tail := SplitRecoveryTail(data)

			if len(head) != tc.wantHead {
				t.Errorf("head 长度 = %d，Python 端为 %d", len(head), tc.wantHead)
			}
			if hex.EncodeToString(tail) != tc.wantTail {
				t.Errorf("tail = %x，Python 端为 %s", tail, tc.wantTail)
			}
			// 主体 + 尾部必须无损还原原文件（这是「原样搬走」的最低要求）
			if !bytes.Equal(append(append([]byte(nil), head...), tail...), data) {
				t.Error("head ‖ tail 与原文件不相等，拆分过程丢了字节")
			}
		})
	}
}

// goldenRecoveryTailHex 是 v3 Marker 的恢复块尾部：recoveryLen(2) + blob(39)。
const goldenRecoveryTailHex = "0027" + goldenRecoveryBlob

// TestSplitRecoveryTailOnGarbage 确认畸形输入下宁可不动：
// 返回 (原文件, nil) 而不是产出一个被截断的 Marker。
func TestSplitRecoveryTailOnGarbage(t *testing.T) {
	for _, input := range [][]byte{
		nil,
		{},
		make([]byte, 71),                // 恰好 prefix(68) + 3，未达 prefix+4 的守卫
		bytes.Repeat([]byte{0xFF}, 200), // PayloadLen 读出 4GiB 量级，headEnd 远超实际长度
	} {
		head, tail := SplitRecoveryTail(input)
		if len(tail) != 0 {
			t.Errorf("长度 %d 的垃圾输入竟然拆出了 %d 字节尾部", len(input), len(tail))
		}
		if !bytes.Equal(head, input) {
			t.Errorf("长度 %d 的垃圾输入下 head 未原样返回", len(input))
		}
	}

	// 全零且刚好越过守卫（72 字节）：PayloadLen 读出为 0 → headEnd = 68，
	// 于是尾部是 data[68:72] 这 4 个零字节。这是 Python 侧同一分支的如实复刻
	// 而非 bug —— 真实 Marker 的载荷至少含 16 字节 GCM 标签，永不走到这里。
	t.Run("PayloadLen 为 0", func(t *testing.T) {
		input := make([]byte, vaultPrefixLen+4)
		head, tail := SplitRecoveryTail(input)
		if len(head) != vaultPrefixLen {
			t.Errorf("head 长度 = %d，应为 %d", len(head), vaultPrefixLen)
		}
		if len(tail) != 4 {
			t.Errorf("tail 长度 = %d，应为 4", len(tail))
		}
		if !bytes.Equal(append(append([]byte(nil), head...), tail...), input) {
			t.Error("head ‖ tail 与原文件不相等，拆分过程丢了字节")
		}
	})
}

// TestBuildRecoveryBlobGolden 用定值 rsalt 对拍恢复块字节。
//
// 生产路径的 rsalt 每次随机（见 BuildRecoveryBlob），这里走包内的
// buildRecoveryBlobWithSalt 才能与 Python 输出逐字节比较。
func TestBuildRecoveryBlobGolden(t *testing.T) {
	secret := mustHex(t, goldenRecoverySecret)
	rsalt := mustHex(t, goldenRecoveryRSalt)

	// 先隔离 KDF 层：rkey 必须与 Python 侧 derive_key_raw 的结果一致，
	// 否则下面恢复块的字节比较失败时分不清是 KDF 还是 GCM 的锅
	// Go 不允许对函数返回的数组值直接切片（不可寻址），必须先落到变量
	rkey := DeriveKeyRaw(secret, rsalt)
	if got := hex.EncodeToString(rkey[:]); got != goldenRecoveryRKey {
		t.Errorf("rkey = %s，Python 端为 %s", got, goldenRecoveryRKey)
	}

	got, err := buildRecoveryBlobWithSalt(secret, goldenVaultPassword, rsalt)
	if err != nil {
		t.Fatalf("buildRecoveryBlobWithSalt 失败: %v", err)
	}
	if hex.EncodeToString(got) != goldenRecoveryBlob {
		t.Errorf("恢复块字节与 Python 端不一致\n got = %x\nwant = %s", got, goldenRecoveryBlob)
	}
	if len(got) != protocol.SaltLen+len(goldenVaultPassword)+protocol.GCMTagLen {
		t.Errorf("恢复块长度 = %d，应为 %d", len(got),
			protocol.SaltLen+len(goldenVaultPassword)+protocol.GCMTagLen)
	}
}

func TestDecryptRecoveryBlobGolden(t *testing.T) {
	blob := mustHex(t, goldenRecoveryBlob)
	secret := mustHex(t, goldenRecoverySecret)

	got, err := DecryptRecoveryBlob(blob, secret)
	if err != nil {
		t.Fatalf("DecryptRecoveryBlob 失败: %v", err)
	}
	if got != goldenVaultPassword {
		t.Errorf("解出 %q，应为 %q", got, goldenVaultPassword)
	}
}

// TestDecryptRecoveryBlobErrors 覆盖恢复块的三类失败。
//
// 界面上 ErrRecoverySecret 与 ErrRecoveryFormat 都应显示为「恢复码无效」，
// 不透露具体原因；区分它们只为让日志可诊断。
func TestDecryptRecoveryBlobErrors(t *testing.T) {
	blob := mustHex(t, goldenRecoveryBlob)

	t.Run("随机密钥错误", func(t *testing.T) {
		if _, err := DecryptRecoveryBlob(blob, make([]byte, protocol.RecoverySecretLen)); !errors.Is(err, ErrRecoverySecret) {
			t.Errorf("应返回 ErrRecoverySecret，实为 %v", err)
		}
	})

	t.Run("密钥长度不符", func(t *testing.T) {
		// PBKDF2 对口令长度不设限，因此这仍然是一次合法的 GCM 尝试，
		// 结果是认证失败而不是格式错误
		if _, err := DecryptRecoveryBlob(blob, []byte("short")); !errors.Is(err, ErrRecoverySecret) {
			t.Errorf("应返回 ErrRecoverySecret，实为 %v", err)
		}
	})

	t.Run("长度不足", func(t *testing.T) {
		// 最短合法布局 = rsalt(16) + 至少 1 字节密文 + tag(16) = 33
		for n := range 33 {
			if _, err := DecryptRecoveryBlob(blob[:n], mustHex(t, goldenRecoverySecret)); !errors.Is(err, ErrRecoveryFormat) {
				t.Errorf("长度 %d 应返回 ErrRecoveryFormat，实为 %v", n, err)
			}
		}
	})

	t.Run("恢复块被篡改", func(t *testing.T) {
		bad := append([]byte(nil), blob...)
		bad[len(bad)-1] ^= 0x01
		if _, err := DecryptRecoveryBlob(bad, mustHex(t, goldenRecoverySecret)); !errors.Is(err, ErrRecoverySecret) {
			t.Errorf("应返回 ErrRecoverySecret，实为 %v", err)
		}
	})
}

// TestBuildRecoveryBlobUsesRandomSalt 确认生产路径的 rsalt 每次随机：
// 同一 (密钥, 主密码) 两次构造必须产出不同 blob，否则恢复块之间会互相泄露
// 「这两个密库用了同一个主密码」。
func TestBuildRecoveryBlobUsesRandomSalt(t *testing.T) {
	secret := mustHex(t, goldenRecoverySecret)

	seen := make(map[string]struct{}, 8)
	seenSalt := make(map[string]struct{}, 8)
	for range 8 {
		blob, err := BuildRecoveryBlob(secret, goldenVaultPassword)
		if err != nil {
			t.Fatal(err)
		}
		key := hex.EncodeToString(blob)
		if _, dup := seen[key]; dup {
			t.Fatalf("重复构造产出了相同的恢复块，rsalt 未随机")
		}
		seen[key] = struct{}{}

		// 每次都必须能解回主密码
		got, err := DecryptRecoveryBlob(blob, secret)
		if err != nil || got != goldenVaultPassword {
			t.Fatalf("随机 rsalt 下往返失败: got=%q err=%v", got, err)
		}
		// 布局必须是 rsalt(16) ‖ ct(len(pw)) ‖ tag(16)，长度可精确预测
		if want := protocol.SaltLen + len(goldenVaultPassword) + protocol.GCMTagLen; len(blob) != want {
			t.Fatalf("恢复块长度 = %d，应为 %d", len(blob), want)
		}
		// 单独断言 rsalt 段两两不同：整串比较若失败无法区分「随机源没用上」
		// 还是「GCM 输出退化」，把随机盐单拎出来才能定位到具体哪一层
		saltHex := hex.EncodeToString(blob[:protocol.SaltLen])
		if _, dup := seenSalt[saltHex]; dup {
			t.Fatalf("重复构造产出了相同的 rsalt %s", saltHex)
		}
		seenSalt[saltHex] = struct{}{}
	}
}

// TestRecoveryCodeIsBase32OfSecret 把「用户手里的恢复码」与「随机密钥」的关系钉死：
// 恢复码 = 10 字节随机密钥的 Base32（去填充）= 恰好 16 字符。
//
// 界面上按 XXXX-XXXX-XXXX-XXXX 分组展示；解码前要去掉 - 与空格再转大写。
// 这条链路一旦错位，用户抄对了恢复码也开不了库，而且无从排查。
func TestRecoveryCodeIsBase32OfSecret(t *testing.T) {
	secret := mustHex(t, goldenRecoverySecret)

	code := B32EncodeNoPad(secret)
	if code != goldenRecoveryCode {
		t.Errorf("恢复码 = %q，Python 端为 %q", code, goldenRecoveryCode)
	}
	if len(code) != protocol.RecoveryCodeLen {
		t.Errorf("恢复码长度 = %d，应为 %d", len(code), protocol.RecoveryCodeLen)
	}

	// 模拟用户手抄：分组 + 空格 + 小写
	grouped := strings.ToLower(code[0:4] + "-" + code[4:8] + " " + code[8:12] + "-" + code[12:16])
	cleaned := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(grouped))

	got, err := B32DecodeNoPad(cleaned)
	if err != nil {
		t.Fatalf("清洗后的恢复码解码失败: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Errorf("清洗后的恢复码解出 %x，应为 %x", got, secret)
	}
	if len(got) != protocol.RecoverySecretLen {
		t.Errorf("恢复密钥长度 = %d，应为 %d", len(got), protocol.RecoverySecretLen)
	}

	// 恢复码必须真能解开恢复块（端到端，而不只是 Base32 往返）
	password, err := DecryptRecoveryBlob(mustHex(t, goldenRecoveryBlob), got)
	if err != nil {
		t.Fatalf("用恢复码解恢复块失败: %v", err)
	}
	if password != goldenVaultPassword {
		t.Errorf("解出 %q，应为 %q", password, goldenVaultPassword)
	}
}

// TestNameTruncationIsByRunes 锁定一条两端极易分叉的细节：
// 名称截断按**字符数**而不是字节数。
//
// Python 侧是 meta.name[:32]（字符切片）之后才 UTF-8 编码；Go 若按字节截，
// 全中文名称下会差 3 倍，而且会切出非法 UTF-8 —— 两端产出的 Marker 将不再
// 逐字节相等，名称在界面上也会显示成乱码。
//
// Python 实测：32 字符、33 字符、40 字符三者产出的 Marker hex **完全相同**。
func TestNameTruncationIsByRunes(t *testing.T) {
	want := strings.Repeat("字", protocol.VaultNameMaxLen)

	for _, chars := range []int{32, 33, 40, 100} {
		meta := goldenVaultMeta(3, strings.Repeat("字", chars))
		data, err := CreateMarker(meta, goldenVaultPassword, nil)
		if err != nil {
			t.Fatalf("%d 字符: CreateMarker 失败: %v", chars, err)
		}
		if hex.EncodeToString(data) != goldenMarkerName32 {
			t.Errorf("%d 字符: Marker 与 Python 端 32 字符产物不一致\n got = %x\nwant = %s",
				chars, data, goldenMarkerName32)
		}

		got, err := VerifyMarker(data, goldenVaultPassword)
		if err != nil {
			t.Fatalf("%d 字符: VerifyMarker 失败: %v", chars, err)
		}
		if got.Name != want {
			t.Errorf("%d 字符: 截断后名称 = %q，应为 %q", chars, got.Name, want)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"", 32, ""},
		{"abc", 32, "abc"},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc"},
		{"字字字", 2, "字字"},
		{"a字b", 2, "a字"},
		{"😀😀😀", 2, "😀😀"}, // 4 字节 rune 同样按字符计
		{"abc", 0, ""},
		{"abc", -1, ""},
		// 混合宽度：截断点必须落在 rune 边界上，产出恒为合法 UTF-8
		{"a字😀b", 3, "a字😀"},
	}

	for _, tc := range tests {
		if got := truncateRunes(tc.in, tc.max); got != tc.want {
			t.Errorf("truncateRunes(%q, %d) = %q，应为 %q", tc.in, tc.max, got, tc.want)
		}
	}
}

// TestCreateMarkerValidatesFields 确认构造前的长度校验真的会拦住坏输入。
//
// Python 侧靠 struct.pack 与切片拼接自然成型，不做这些检查；但 Go 的 copy
// 会**静默截断**，长度不符就会拼出一个布局错位的 Marker —— 那是种
// 「能写出去、却永远打不开」的坏文件，必须在构造前拦住。
func TestCreateMarkerValidatesFields(t *testing.T) {
	tests := []struct {
		name string
		meta VaultMetadata
	}{
		{"VaultID 过短", VaultMetadata{Version: 3, VaultID: make([]byte, 15), Salt: mustHex(t, goldenVaultSalt), IV: mustHex(t, goldenVaultIV)}},
		{"VaultID 过长", VaultMetadata{Version: 3, VaultID: make([]byte, 17), Salt: mustHex(t, goldenVaultSalt), IV: mustHex(t, goldenVaultIV)}},
		{"Salt 过短", VaultMetadata{Version: 3, VaultID: mustHex(t, goldenVaultID), Salt: make([]byte, 15), IV: mustHex(t, goldenVaultIV)}},
		{"IV 过短", VaultMetadata{Version: 3, VaultID: mustHex(t, goldenVaultID), Salt: mustHex(t, goldenVaultSalt), IV: make([]byte, 12)}},
		{"字段全空", VaultMetadata{Version: 3}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CreateMarker(tc.meta, goldenVaultPassword, nil); !errors.Is(err, ErrVaultField) {
				t.Errorf("应返回 ErrVaultField，实为 %v", err)
			}
		})
	}

	// 正向对照：字段齐全时必须成功，否则上面的失败可能只是因为实现根本不通
	if _, err := CreateMarker(goldenVaultMeta(3, "x"), goldenVaultPassword, nil); err != nil {
		t.Errorf("字段齐全时不应报错，实为 %v", err)
	}
}

// TestCreateMarkerRejectsOversizedRecoveryBlob 确认恢复块长度超出 uint16
// 上限时报错，而不是让 PutUint16 静默截断成一个错误长度。
func TestCreateMarkerRejectsOversizedRecoveryBlob(t *testing.T) {
	_, err := CreateMarker(goldenVaultMeta(3, "x"), goldenVaultPassword, make([]byte, 0x10000))
	if !errors.Is(err, ErrVaultField) {
		t.Errorf("应返回 ErrVaultField，实为 %v", err)
	}
}

// TestVerifyMarkerDoesNotAliasInput 锁定一条容易被无声破坏的内存契约：
// 返回的 VaultID / Salt / IV 必须是副本，不别名入参。
//
// 调用方传进来的很可能是池化缓冲或马上要被复用的下载缓冲；
// 若返回子切片，上层会在毫不知情的情况下读到脏数据 —— 症状是
// 「密库偶尔打不开、重开一次又好了」，极难定位。
func TestVerifyMarkerDoesNotAliasInput(t *testing.T) {
	data := mustHex(t, goldenMarkerV3)
	original := append([]byte(nil), data...)

	meta, err := VerifyMarker(data, goldenVaultPassword)
	if err != nil {
		t.Fatal(err)
	}

	// 把入参缓冲整个覆盖掉（模拟池化缓冲被下一次使用改写）
	for i := range data {
		data[i] = 0xFF
	}

	if !bytes.Equal(meta.VaultID, original[16:32]) {
		t.Errorf("VaultID 被入参改写污染: %x", meta.VaultID)
	}
	if !bytes.Equal(meta.Salt, original[32:48]) {
		t.Errorf("Salt 被入参改写污染: %x", meta.Salt)
	}
	if !bytes.Equal(meta.IV, original[48:64]) {
		t.Errorf("IV 被入参改写污染: %x", meta.IV)
	}
}

// TestGenerateMetadata 确认新建 Metadata 的字段规格与随机性。
func TestGenerateMetadata(t *testing.T) {
	seen := make(map[string]struct{}, 8)

	for _, filenameEnc := range []bool{false, true} {
		meta, err := GenerateMetadata(filenameEnc, "测试密库")
		if err != nil {
			t.Fatalf("GenerateMetadata 失败: %v", err)
		}

		if meta.Version != protocol.VaultVersion {
			t.Errorf("Version = %d，应为 %d", meta.Version, protocol.VaultVersion)
		}
		if meta.ProtocolVersion != protocol.Version {
			t.Errorf("ProtocolVersion = %d，应为 %d", meta.ProtocolVersion, protocol.Version)
		}
		if meta.FilenameEnc != filenameEnc {
			t.Errorf("FilenameEnc = %v，应为 %v", meta.FilenameEnc, filenameEnc)
		}
		if meta.Name != "测试密库" {
			t.Errorf("Name = %q，应为 %q", meta.Name, "测试密库")
		}
		if meta.HasRecovery {
			t.Error("新建 Metadata 的 HasRecovery 应为 false")
		}
		if len(meta.VaultID) != protocol.VaultIDLen {
			t.Errorf("VaultID 长度 = %d，应为 %d", len(meta.VaultID), protocol.VaultIDLen)
		}
		if len(meta.Salt) != protocol.SaltLen {
			t.Errorf("Salt 长度 = %d，应为 %d", len(meta.Salt), protocol.SaltLen)
		}
		if len(meta.IV) != protocol.IVLen {
			t.Errorf("IV 长度 = %d，应为 %d", len(meta.IV), protocol.IVLen)
		}
		// 三个字段都必须真的填了随机数，不能是零值
		for name, field := range map[string][]byte{
			"VaultID": meta.VaultID, "Salt": meta.Salt, "IV": meta.IV,
		} {
			if bytes.Equal(field, make([]byte, len(field))) {
				t.Errorf("%s 全为 0，未填充随机数", name)
			}
		}

		key := hex.EncodeToString(meta.VaultID) + hex.EncodeToString(meta.Salt) + hex.EncodeToString(meta.IV)
		if _, dup := seen[key]; dup {
			t.Fatal("两次生成产出了相同的随机字段")
		}
		seen[key] = struct{}{}
	}
}

// TestMarkerPrefixLayout 把明文前缀的偏移逐字段钉死。
//
// 这是对 goldenMarkerV3 的**结构化**补充：整串 hex 相等能证明兼容，
// 但一旦哪天真的不等了，这条测试能立刻指出是哪个字段错位。
func TestMarkerPrefixLayout(t *testing.T) {
	data := mustHex(t, goldenMarkerV3)

	if got := string(data[0:12]); got != protocol.VaultMagic {
		t.Errorf("偏移 0 的魔数 = %q，应为 %q", got, protocol.VaultMagic)
	}
	if got := data[12:16]; !bytes.Equal(got, []byte{0, 0, 0, 3}) {
		t.Errorf("偏移 12 的 Version = %x，应为 00000003（大端）", got)
	}
	if got := data[16:32]; !bytes.Equal(got, mustHex(t, goldenVaultID)) {
		t.Errorf("偏移 16 的 VaultID = %x", got)
	}
	if got := data[32:48]; !bytes.Equal(got, mustHex(t, goldenVaultSalt)) {
		t.Errorf("偏移 32 的 Salt = %x", got)
	}
	if got := data[48:64]; !bytes.Equal(got, mustHex(t, goldenVaultIV)) {
		t.Errorf("偏移 48 的 IV = %x", got)
	}
	// 载荷 = inner(39) + tag(16) = 55 = 0x37
	if got := data[64:68]; !bytes.Equal(got, []byte{0, 0, 0, 0x37}) {
		t.Errorf("偏移 64 的 PayloadLen = %x，应为 00000037", got)
	}
	// 恢复块长度字段紧随载荷：39 = 0x27
	if got := data[123:125]; !bytes.Equal(got, []byte{0, 0x27}) {
		t.Errorf("偏移 123 的 RecoveryLen = %x，应为 0027", got)
	}
	if len(data) != 164 {
		t.Errorf("全长 = %d，应为 164", len(data))
	}
}
