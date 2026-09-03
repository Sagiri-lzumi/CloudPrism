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

// 文件名加密黄金向量：key = 0x5A*32、nonce = 00..0b（12 字节定值），
// 由 WindowsPy 端 AES.new(key, MODE_GCM, nonce) + b32_encode_nopad 真实运行后
// 打印（filename.py:71-86）。定值 nonce 只用于对拍 —— 生产路径的 nonce 每次随机。
const (
	goldenFilenameKey   = "5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a"
	goldenFilenameNonce = "000102030405060708090a0b"

	// 中文 + ASCII 混合、含空格与括号（云盘上最常见的麻烦字符组合）
	goldenFilenameMixed    = "测试-file (1).txt"
	goldenFilenameMixedEnc = "AAAQEAYEAUDAOCAJBIF2WFFGRWIVNE5KM5MKRTUSCZ6CN7RTUMR6M7525SYX6AN2DQTKHARPXFHA"

	// 单字符：密文最短的非空情形（1 + 16 = 17 字节 → 28 个 Base32 字符）
	goldenFilenameSingle    = "a"
	goldenFilenameSingleEnc = "AAAQEAYEAUDAOCAJBIFSYPCUP25TKA3LQ5ANBESVS3ZVVIA"

	// 空文件名：只有 nonce + 标签（28 字节 → 45 个字符），密文段长度为 0
	goldenFilenameEmpty    = ""
	goldenFilenameEmptyEnc = "AAAQEAYEAUDAOCAJBIFTT3Z4TO2FZFUX6XVSEIPOY5OOA"

	// 明文是非法 UTF-8 字节（Python 侧手工加密 b"\xff\xfe\xfd" 得到，
	// 绕过 FilenameCipher.encrypt 的 str 入参）。Go 的 string() 转换不校验
	// UTF-8，若不显式补 utf8.Valid 就会比 Python 多「成功」一条路径。
	goldenFilenameBadUTF8Enc = "AAAQEAYEAUDAOCAJBIF3EX6QBSPUVKTSYA6OMU2YSRSWZFWADU"
)

// goldenB32Pairs 是 0..10 字节全余数类的 Base32 定值对拍表，
// 由 Python 端 b32_encode_nopad 逐条打印（filename.py:25-35）。
//
// 覆盖到 10 字节是因为 Base32 每 5 字节编成 8 字符，余数 1..4 分别产出
// 2/4/5/7 个字符 —— 这四类是「去填充 / 补填充」规则唯一会出分歧的地方。
var goldenB32Pairs = []struct {
	dataHex string
	encoded string
}{
	{"", ""},
	{"00", "AA"},
	{"0001", "AAAQ"},
	{"000102", "AAAQE"},
	{"00010203", "AAAQEAY"},
	{"0001020304", "AAAQEAYE"},
	{"000102030405", "AAAQEAYEAU"},
	{"00010203040506", "AAAQEAYEAUDA"},
	{"0001020304050607", "AAAQEAYEAUDAO"},
	{"000102030405060708", "AAAQEAYEAUDAOCA"},
	{"00010203040506070809", "AAAQEAYEAUDAOCAJ"},
}

// TestB32EncodeGolden 双向锁定 Base32 编解码：
// 编码必须逐字符等于 Python 输出，解码必须还原原字节。
//
// 两端不一致的后果是「Go 端上传的文件名 Python 端列不出来」，
// 而且从任何一端的单测里都看不出来，只能靠这种定值对拍。
func TestB32EncodeGolden(t *testing.T) {
	for _, tc := range goldenB32Pairs {
		raw := mustHex(t, tc.dataHex)

		if got := B32EncodeNoPad(raw); got != tc.encoded {
			t.Errorf("B32EncodeNoPad(%x) = %q，Python 端为 %q", raw, got, tc.encoded)
		}
		got, err := B32DecodeNoPad(tc.encoded)
		if err != nil {
			t.Fatalf("B32DecodeNoPad(%q) 失败: %v", tc.encoded, err)
		}
		if !bytes.Equal(got, raw) {
			t.Errorf("B32DecodeNoPad(%q) = %x，应为 %x", tc.encoded, got, raw)
		}
	}
}

// TestB32OutputHasNoPadding 钉死「去 = 填充」这条协议要求：
// 云端文件名带 = 会被部分网盘当作非法字符或触发重命名。
func TestB32OutputHasNoPadding(t *testing.T) {
	for _, tc := range goldenB32Pairs {
		got := B32EncodeNoPad(mustHex(t, tc.dataHex))
		if strings.ContainsRune(got, '=') {
			t.Errorf("B32EncodeNoPad(%x) 含填充符 = : %q", tc.dataHex, got)
		}
		for _, r := range got {
			if !strings.ContainsRune(protocol.Base32Alphabet, r) {
				t.Errorf("B32EncodeNoPad(%x) 产出字母表外字符 %q", tc.dataHex, r)
			}
		}
	}
}

// TestB32DecodeAcceptsLowercase 对齐 Python 的 s.upper()（filename.py:55）：
// Base32 大小写不敏感，小写输入必须解出同样结果。
//
// 这条有真实场景 —— 用户可能手工把云盘上的密文文件名抄进别处，
// 某些输入法或同步工具会把它转成小写。
func TestB32DecodeAcceptsLowercase(t *testing.T) {
	key := mustHex(t, goldenFilenameKey)

	got, err := DecryptFilename(strings.ToLower(goldenFilenameMixedEnc), key)
	if err != nil {
		t.Fatalf("全小写输入解密失败: %v", err)
	}
	if got != goldenFilenameMixed {
		t.Errorf("全小写输入解出 %q，应为 %q", got, goldenFilenameMixed)
	}

	// 混合大小写同样必须通过：前半段原样、后半段转小写
	mixed := goldenFilenameMixedEnc[:16] + strings.ToLower(goldenFilenameMixedEnc[16:])
	got, err = DecryptFilename(mixed, key)
	if err != nil {
		t.Fatalf("混合大小写输入解密失败: %v", err)
	}
	if got != goldenFilenameMixed {
		t.Errorf("混合大小写输入解出 %q，应为 %q", got, goldenFilenameMixed)
	}

	// 字节层面也必须与大小写无关，否则上面两条只是碰巧过
	upper := mustDecodeForTamper(goldenFilenameMixedEnc)
	lower := mustDecodeForTamper(strings.ToLower(goldenFilenameMixedEnc))
	if !bytes.Equal(upper, lower) {
		t.Errorf("大小写输入解出的字节不同: %x vs %x", upper, lower)
	}
}

// TestB32DecodeRejectsBadInput 验证非法字符必须报错而不是静默产出脏字节：
// 脏字节会被后续 GCM 认证挡下，但错误信息会变成「文件名被篡改」，
// 把真正的病因（字符集不对）掩盖掉。
func TestB32DecodeRejectsBadInput(t *testing.T) {
	for _, bad := range []string{
		"AAAQEAYEAUDAOCAJBIF2WFFGRWIVNE5KM5MKRTUSCZ6CN7RTUMR6M7525SYX6AN2DQTKHARPXFH!", // 非法字符
		"AAAQEAYEAUDAOCAJ=",     // 带填充（NoPadding 解码器不接受）
		"AAAA AAAA",             // 含空白（刻意不做 TrimSpace，见 B32DecodeNoPad 注释）
		"AAAQEAYEAUDAOCAJBIF01", // 0 与 1 不在 Base32 字母表内（易与 Base58 混淆）
	} {
		if _, err := B32DecodeNoPad(bad); err == nil {
			t.Errorf("B32DecodeNoPad(%q) 应当报错，却成功了", bad)
		}
	}
}

// TestB32RemainderDivergenceFromPython 固化与 Python 在 Base32 余数上的行为差异。
//
// 造数据的手法：把 n 字节原数据编成 Base32 后再追加一个 'B'，
// 于是字符数 % 8 会遍历到所有余数类。下表的两端行为均为**实测值**
// （Python 侧跑 Release/_spike/gen_golden.py 同源的 b32_decode_nopad，
// Go 侧跑本包），不是对两边标准库行为的推测。
//
// 两类结论：
//   - 余数 ∈ {0,2,4,5,7}：两端都接受，且解出的字节**逐字节相同**——
//     这是真正的兼容面，必须硬断言。
//   - 余数 ∈ {1,3,6}：Python 抛 `binascii.Error: Incorrect padding`，
//     Go 则丢弃末尾不完整的量子后成功解码。
//
// 后一类只断言不变式而不断言具体字节数：截断多少是 Go 标准库的内部细节，
// 钉死它会让一次无害的标准库升级把测试打红。真正需要成立的是两条：
// ① 宽容解码**只会少给字节，绝不会给出错的字节**（结果恒为原数据的前缀）；
// ② 这类脏输入过不了下游的 DecryptFilename。
func TestB32RemainderDivergenceFromPython(t *testing.T) {
	tests := []struct {
		n    int
		rem  int
		want string // Python 也接受时的共识字节；空串表示 Python 抛错
	}{
		{n: 0, rem: 1},
		{n: 1, rem: 3},
		{n: 2, rem: 5, want: "000100"},
		{n: 3, rem: 6},
		{n: 4, rem: 0, want: "0001020301"},
		{n: 5, rem: 1},
		{n: 6, rem: 3},
		{n: 7, rem: 5, want: "0001020304050600"},
		{n: 8, rem: 6},
		{n: 9, rem: 0, want: "00010203040506070801"},
		{n: 10, rem: 1},
		{n: 11, rem: 3},
	}

	key := mustHex(t, goldenFilenameKey)

	for _, tc := range tests {
		raw := make([]byte, tc.n)
		for i := range raw {
			raw[i] = byte(i)
		}
		input := B32EncodeNoPad(raw) + "B"

		if got := len(input) % 8; got != tc.rem {
			t.Fatalf("n=%d: 用例本身算错了，字符数 %d 的余数是 %d 而非 %d",
				tc.n, len(input), got, tc.rem)
		}

		t.Run(fmt.Sprintf("n=%d/rem=%d", tc.n, tc.rem), func(t *testing.T) {
			got, err := B32DecodeNoPad(input)

			if tc.want != "" {
				// 两端都接受：必须逐字节相同，否则就是真的不兼容了
				if err != nil {
					t.Fatalf("两端都应接受，Go 却报错 %v", err)
				}
				if hex.EncodeToString(got) != tc.want {
					t.Errorf("解出 %x，Python 端为 %s", got, tc.want)
				}
				return
			}

			// Python 抛错的余数：Go 宽容，但只允许「少给」不允许「给错」
			if err != nil {
				t.Fatalf("Go 意外报错 %v（预期宽容解码）", err)
			}
			if len(got) > len(raw) || !bytes.Equal(got, raw[:len(got)]) {
				t.Errorf("宽容解码必须产出原数据的前缀：got=%x raw=%x", got, raw)
			}
			// 脏输入必须仍然过不了下游，否则宽容就变成了可被利用的缺陷
			if _, derr := DecryptFilename(input, key); derr == nil {
				t.Error("脏输入竟然解密成功了")
			}
		})
	}
}

// TestB32TrailingBitsAreNotValidated 固化第二条与 Python **一致**的行为：
// 尾部废弃位不校验，因此多个不同的 Base32 串可以解出同一字节序列。
//
// 实测（两端逐项相同）：以 A/B/C 结尾均解出末字节 0x4e 且能正常解密；
// 以 Q/R 结尾解出 0x4f，GCM 认证失败。
// 若有人为了「严格」给 Go 补上废弃位校验，Go 就会拒绝 Python 能解的文件名。
func TestB32TrailingBitsAreNotValidated(t *testing.T) {
	// 去掉末字符得到「量子前缀」，再逐个接上待测尾字符。
	// Go 不支持常量字符串切片，故只能在函数内算。
	stem := goldenFilenameMixedEnc[:len(goldenFilenameMixedEnc)-1]
	key := mustHex(t, goldenFilenameKey)

	tests := []struct {
		tail      string
		wantPlain string // 空串表示应当解密失败
	}{
		{"A", goldenFilenameMixed},
		{"B", goldenFilenameMixed}, // 与 A 同解：低 4 位是废弃位
		{"C", goldenFilenameMixed},
		{"Q", ""}, // 高位变了 → 标签字节真的变了 → 认证失败
		{"R", ""},
	}

	for _, tc := range tests {
		t.Run("tail="+tc.tail, func(t *testing.T) {
			got, err := DecryptFilename(stem+tc.tail, key)
			if tc.wantPlain == "" {
				if !errors.Is(err, ErrFilenameAuth) {
					t.Errorf("应返回 ErrFilenameAuth，实为 got=%q err=%v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("解密失败: %v", err)
			}
			if got != tc.wantPlain {
				t.Errorf("解出 %q，应为 %q", got, tc.wantPlain)
			}
		})
	}
}

// TestEncryptFilenameGolden 是最强的兼容证明：
// 定 key + 定 nonce 下，Go 的输出必须与 Python 端**逐字符相等**。
func TestEncryptFilenameGolden(t *testing.T) {
	key := mustHex(t, goldenFilenameKey)
	nonce := mustHex(t, goldenFilenameNonce)

	tests := []struct {
		name  string
		plain string
		want  string
	}{
		{"中英混合含空格括号", goldenFilenameMixed, goldenFilenameMixedEnc},
		{"单字符", goldenFilenameSingle, goldenFilenameSingleEnc},
		{"空文件名", goldenFilenameEmpty, goldenFilenameEmptyEnc},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encryptFilenameWithNonce(tc.plain, key, nonce)
			if err != nil {
				t.Fatalf("encryptFilenameWithNonce 失败: %v", err)
			}
			if got != tc.want {
				t.Errorf("密文与 Python 端不一致\n got = %s\nwant = %s", got, tc.want)
			}
		})
	}
}

func TestDecryptFilenameGolden(t *testing.T) {
	key := mustHex(t, goldenFilenameKey)

	tests := []struct {
		name    string
		encoded string
		want    string
	}{
		{"中英混合含空格括号", goldenFilenameMixedEnc, goldenFilenameMixed},
		{"单字符", goldenFilenameSingleEnc, goldenFilenameSingle},
		{"空文件名", goldenFilenameEmptyEnc, goldenFilenameEmpty},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecryptFilename(tc.encoded, key)
			if err != nil {
				t.Fatalf("DecryptFilename 失败: %v", err)
			}
			if got != tc.want {
				t.Errorf("解出 %q，Python 端明文为 %q", got, tc.want)
			}
		})
	}
}

// TestDecryptFilenameRejectsInvalidUTF8 对齐 Python 的 .decode("utf-8")
// 抛 UnicodeDecodeError（filename.py:108）。
//
// Go 的 string(b) 不做任何校验，因此必须显式判 utf8.Valid，
// 否则 Go 端会「成功」返回一串乱码文件名，比 Python 端多出一条脏路径。
func TestDecryptFilenameRejectsInvalidUTF8(t *testing.T) {
	_, err := DecryptFilename(goldenFilenameBadUTF8Enc, mustHex(t, goldenFilenameKey))
	if !errors.Is(err, ErrFilenameUTF8) {
		t.Errorf("非法 UTF-8 明文应返回 ErrFilenameUTF8，实为 %v", err)
	}
}

// TestEncryptDecryptRoundTrip 覆盖随机 nonce 的生产路径。
//
// 用例集刻意包含各类真实网盘上会出现的名字：超长名、纯 emoji、
// 含路径分隔符与 Windows 保留字符、首尾空白。
func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := mustHex(t, goldenFilenameKey)

	names := []string{
		"",
		"a",
		"测试-file (1).txt",
		strings.Repeat("长文件名", 60) + ".mp4",
		"😀🎬🔒.mkv",
		`含\斜杠/与:星*号?引"<>|.rmvb`,
		"  首尾空白  ",
		"\u0000含控制字符",
		strings.Repeat("x", 255),
	}

	for _, name := range names {
		encoded, err := EncryptFilename(name, key)
		if err != nil {
			t.Fatalf("EncryptFilename(%q) 失败: %v", name, err)
		}
		got, err := DecryptFilename(encoded, key)
		if err != nil {
			t.Fatalf("DecryptFilename(%q) 失败: %v", name, err)
		}
		if got != name {
			t.Errorf("往返不一致\n got = %q\nwant = %q", got, name)
		}
	}
}

// TestEncryptFilenameNonceIsRandom 确认 nonce 每次随机：
// 同一文件名两次加密必须产出不同密文，否则同名文件在云端会暴露
// 「这两个文件内容相同」的信息（文件名加密的意义就没了）。
func TestEncryptFilenameNonceIsRandom(t *testing.T) {
	key := mustHex(t, goldenFilenameKey)

	seen := make(map[string]struct{}, 8)
	for range 8 {
		encoded, err := EncryptFilename(goldenFilenameMixed, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[encoded]; dup {
			t.Fatalf("同一文件名重复加密产出了相同密文 %s，nonce 未随机", encoded)
		}
		seen[encoded] = struct{}{}

		// 每次都必须能解回原名
		if got, err := DecryptFilename(encoded, key); err != nil || got != goldenFilenameMixed {
			t.Fatalf("随机 nonce 下往返失败: got=%q err=%v", got, err)
		}
	}
}

// TestDecryptFilenameErrors 覆盖三类失败，每类都必须落到对应的 error 上：
// 上层据此决定「回退展示密文名」还是「报密钥错误」。
func TestDecryptFilenameErrors(t *testing.T) {
	key := mustHex(t, goldenFilenameKey)
	otherKey := bytes.Repeat([]byte{0x77}, protocol.KeyLen)

	tests := []struct {
		name    string
		encoded string
		key     []byte
		want    error
	}{
		// nonce(12) + tag(16) = 28 字节是下限，少一个字节就装不下
		{"比 nonce+tag 还短", B32EncodeNoPad(make([]byte, 27)), key, ErrFilenameTooShort},
		{"恰好 nonce+tag（空明文）", B32EncodeNoPad(make([]byte, 28)), key, ErrFilenameAuth},
		{"密钥不符", goldenFilenameMixedEnc, otherKey, ErrFilenameAuth},
		{"密文被篡改", tamperCiphertext(goldenFilenameMixedEnc), key, ErrFilenameAuth},
		{"标签被篡改", tamperTag(goldenFilenameMixedEnc), key, ErrFilenameAuth},
		{"Base32 非法字符", "!!!!", key, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecryptFilename(tc.encoded, tc.key)
			if tc.want == nil {
				if err == nil {
					t.Error("应当报错，却成功了")
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("应返回 %v，实为 %v", tc.want, err)
			}
		})
	}
}

// TestEncryptFilenameRejectsWrongKeyLength 确认 GCM-12 也走 newAES256 的长度校验，
// 而不是把 16 字节密钥静默降级成 AES-128 —— 那会写出 Python 端解不开的文件名。
func TestEncryptFilenameRejectsWrongKeyLength(t *testing.T) {
	for _, keyLen := range []int{0, 16, 24, 31, 33} {
		key := bytes.Repeat([]byte{0x11}, keyLen)
		if _, err := EncryptFilename("x.txt", key); err == nil {
			t.Errorf("密钥长度 %d 应当被拒绝，却成功了", keyLen)
		}
		if _, err := DecryptFilename(goldenFilenameSingleEnc, key); err == nil {
			t.Errorf("密钥长度 %d 解密应当被拒绝，却成功了", keyLen)
		}
	}
}

// tamperCiphertext 改动密文段的一个字节后重新编码，用于模拟「云端文件名被改过」。
//
// 刻意在**解码后的字节**上动手而不是直接改字符：Base32 末字符的低位
// 可能是废弃位（见 TestB32TrailingBitsAreNotValidated），改字符未必改到数据，
// 会得到一个「看似在测篡改、实际什么也没改」的假用例。
func tamperCiphertext(encoded string) string {
	raw := mustDecodeForTamper(encoded)
	// 密文段紧跟在 12 字节 nonce 之后
	raw[protocol.FilenameNonceLen] ^= 0x01
	return B32EncodeNoPad(raw)
}

// tamperTag 改动 GCM 标签的末字节后重新编码。
// 标签末字节位于数据位而非废弃位（已实测），因此篡改必然生效。
func tamperTag(encoded string) string {
	raw := mustDecodeForTamper(encoded)
	raw[len(raw)-1] ^= 0x01
	return B32EncodeNoPad(raw)
}

// mustDecodeForTamper 解码黄金向量，失败即测试本身写错了。
// 不用 *testing.T 入参，好让上述两个 helper 能直接写在表驱动用例的字段里。
func mustDecodeForTamper(encoded string) []byte {
	raw, err := B32DecodeNoPad(encoded)
	if err != nil {
		panic("tamper: 黄金向量解码失败: " + err.Error())
	}
	return raw
}
