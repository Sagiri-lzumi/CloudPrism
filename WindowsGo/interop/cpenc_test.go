package interop

import (
	"bytes"
	"encoding/hex"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// parallelThreshold 是 Python 侧走多核并行加密的最小文件尺寸。
//
// 对照 WindowsPy/src/cloudprism/core/encryptor.py:100 的
// `if max_workers <= 1 or total < 4 * 1024 * 1024`。写死在这里是为了
// 断言 hash_only 夹具确实踩在并行分支上 —— 若 Python 侧调高了门槛而
// 夹具没重生成，这些用例会退化成「又测了一遍单核路径」，静默失去价值。
const parallelThreshold = 4 * 1024 * 1024

// TestDecryptPythonCpenc 解密 Python 真实生产路径产出的密文，断言还原为原明文。
//
// 这是最基础的方向 A 断言。夹具由 Encryptor.encrypt_and_upload +
// LocalFolderBackend 产出，因此覆盖的是产品实际会写进云盘的字节，
// 而不是某个测试专用简化实现。
func TestDecryptPythonCpenc(t *testing.T) {
	v := loadVectors(t)

	for _, fv := range v.Files {
		if fv.CipherPath == nil {
			continue // hash_only 条目不落盘，由 TestParallelPathFixturesMatchPythonHash 覆盖
		}
		t.Run(fv.Slug, func(t *testing.T) {
			data := readFixture(t, *fv.CipherPath)

			// 先确认夹具文件本身没被改过：否则后面的失败无法归因
			assertSHA256(t, fv.Slug+" 密文夹具", sha256Hex(data), fv.CipherSHA256)
			if want := int64(fv.HeaderLength) + fv.Size; int64(len(data)) != want {
				t.Fatalf("密文长度 = %d，应为 头(%d) + 明文(%d) = %d",
					len(data), fv.HeaderLength, fv.Size, want)
			}

			hdr, plain := decryptCpenc(t, v.Meta.MasterPassword, data)
			assertHeaderMatches(t, fv.Slug, hdr, fv)

			want := expectedPlain(t, fv)
			if !bytes.Equal(plain, want) {
				t.Fatalf("%s: 解密结果与明文不符（首个差异见下）\n  %s",
					fv.Slug, firstDiff(plain, want))
			}
			assertSHA256(t, fv.Slug+" 解密结果", sha256Hex(plain), fv.PlainSHA256)
		})
	}
}

// TestRebuildPythonCpencByteForByte 用定值 salt/iv 在 Go 侧重建容器，
// 断言与 Python 产出**逐字节相同**。
//
// 这是比「解得开」强得多的断言：CTR 是对称异或，任何一端把密钥流搞错
// 都可能碰巧被另一端「解回来」；而字节级相等意味着两端的头部布局、
// KDF、计数器算术、分块拼接全都一致，没有互相抵消的错误。
//
// Python 侧的 IV 恒随机且不可注入（encryptor.py:82），故做法是产出后从
// 头部读回 (salt, iv) 记进 vectors.json，Go 再用同一组输入重建。
func TestRebuildPythonCpencByteForByte(t *testing.T) {
	v := loadVectors(t)

	for _, fv := range v.Files {
		if fv.CipherPath == nil {
			continue
		}
		t.Run(fv.Slug, func(t *testing.T) {
			want := readFixture(t, *fv.CipherPath)
			salt := mustHex(t, fv.Salt, fv.Slug+" 的 salt")
			iv := mustHex(t, fv.IV, fv.Slug+" 的 iv")

			got := buildCpenc(t, v.Meta.MasterPassword, expectedPlain(t, fv), salt, iv, fv.Flags, fv.Version)

			if len(got) != len(want) {
				t.Fatalf("%s: Go 重建长度 = %d，Python 产出 = %d", fv.Slug, len(got), len(want))
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s: Go 重建的容器与 Python 产出逐字节不等\n  %s", fv.Slug, firstDiff(got, want))
			}
		})
	}
}

// TestParallelPathFixturesMatchPythonHash 覆盖 Python 的多核并行加密分支。
//
// 并行分段曾有一个 P0 缺陷：segment_size 用 ceil 算出后未按 16 字节对齐，
// 各段独立从块首生成密钥流，于是非对齐段整体错位 offset%16 字节，
// 密文永久损坏而解密**不报错**（CTR 无完整性校验），表现为大文件静默变垃圾。
// 修复见 encryptor.py:205-210。
//
// 这三个夹具的尺寸都刻意取非 16 倍数且 >= 4MiB，正是当初触发缺陷的形态。
// Go 侧用 1MiB 对齐分段重建，比对 cipher sha256 —— 哈希相等等价于字节相等，
// 而大文件不落盘让 testdata 保持在数百 KiB 量级。
func TestParallelPathFixturesMatchPythonHash(t *testing.T) {
	v := loadVectors(t)

	seen := 0
	for _, fv := range v.Files {
		if fv.Kind != "hash_only" {
			continue
		}
		seen++
		t.Run(fv.Slug, func(t *testing.T) {
			// 确认夹具真的踩在并行分支上，否则本测试失去意义
			if fv.MaxWorkers <= 1 || fv.Size < parallelThreshold {
				t.Fatalf("%s: max_workers=%d size=%d 不会触发并行路径（门槛 %d），夹具与生成器不同步",
					fv.Slug, fv.MaxWorkers, fv.Size, parallelThreshold)
			}
			if fv.CipherPath != nil || fv.PlainPath != nil {
				t.Fatalf("%s: hash_only 条目不应落盘夹具文件", fv.Slug)
			}

			plain := recipePlain(t, fv.Recipe, fv.Size)
			if got := sha256Hex(plain); got != fv.PlainSHA256 {
				t.Fatalf("%s: 配方重建的明文 sha256 = %s，夹具 = %s（配方实现不一致）",
					fv.Slug, got, fv.PlainSHA256)
			}

			salt := mustHex(t, fv.Salt, fv.Slug+" 的 salt")
			iv := mustHex(t, fv.IV, fv.Slug+" 的 iv")
			cipher := buildCpenc(t, v.Meta.MasterPassword, plain, salt, iv, fv.Flags, fv.Version)

			// 正向：Go 的产物必须与 Python 并行路径的产物哈希相同
			assertSHA256(t, fv.Slug+" 重建密文", sha256Hex(cipher), fv.CipherSHA256)

			// 反向：把 Go 的产物再解开，必须还原出同一份明文。
			// 两个方向都过，才排除了「两端犯了同一个错因而哈希相同」的可能。
			hdr, decrypted := decryptCpenc(t, v.Meta.MasterPassword, cipher)
			assertHeaderMatches(t, fv.Slug, hdr, fv)
			assertSHA256(t, fv.Slug+" 回解明文", sha256Hex(decrypted), fv.PlainSHA256)
		})
	}

	if seen == 0 {
		t.Fatal("夹具里没有任何 hash_only 条目，并行路径失去覆盖")
	}
	t.Logf("并行路径夹具 %d 组（尺寸均非 16 倍数且 >= %d 字节）", seen, parallelThreshold)
}

// TestRandomAccessRangeMatchesPython 复刻 Decryptor.decrypt_range_to_bytes 的
// 随机访问语义，与 Python 侧产出的区间明文比对。
//
// 随机访问是流式代理、缩略图与文本预览的共同地基：它必须能在任意非对齐
// 起点上取到正确字节。用例覆盖对齐起点(0)、非对齐起点(3)、中段(1000)、
// 以及末尾非对齐(end == 明文总长)四种形态。
func TestRandomAccessRangeMatchesPython(t *testing.T) {
	v := loadVectors(t)

	checked := 0
	for _, fv := range v.Files {
		if len(fv.Ranges) == 0 || fv.CipherPath == nil {
			continue
		}
		t.Run(fv.Slug, func(t *testing.T) {
			data := readFixture(t, *fv.CipherPath)
			hdr, err := cryptox.ParseBytes(data)
			if err != nil {
				t.Fatalf("解析文件头失败: %v", err)
			}

			for _, rv := range fv.Ranges {
				got := decryptRange(t, v.Meta.MasterPassword, data, hdr, rv.Start, rv.End)
				want := mustHex(t, rv.ExpectHex, "区间期望值")

				if !bytes.Equal(got, want) {
					t.Errorf("区间 [%d,%d) 不符：\n  Go 侧 = %s\n  夹具  = %s",
						rv.Start, rv.End, hex.EncodeToString(got), rv.ExpectHex)
					continue
				}
				// 长度必须等于区间长度，防止「切片切歪了但内容碰巧前缀相同」
				if int64(len(got)) != rv.End-rv.Start {
					t.Errorf("区间 [%d,%d) 返回 %d 字节，应为 %d", rv.Start, rv.End, len(got), rv.End-rv.Start)
				}
				checked++
			}
		})
	}

	if checked == 0 {
		t.Fatal("夹具里没有任何随机访问用例")
	}
}

// TestLeafNameReflectsFilenameEncryption 断言云端叶子名与 filename_enc 开关一致。
//
// 密库开启文件名加密后，云盘上的叶子名是 Base32(nonce‖ct‖tag) 而非展示名，
// 这也是流式代理无法从 URL 路径推断 MIME、必须改走令牌表的原因。
// 这里验证 Go 能把 Python 产出的叶子名解回展示名 —— 目录树显示原名的前提。
func TestLeafNameReflectsFilenameEncryption(t *testing.T) {
	v := loadVectors(t)

	fnKey := mustHex(t, v.FilenameKey.Key, "文件名密钥")
	encCount, plainCount := 0, 0

	for _, fv := range v.Files {
		if !strings.HasSuffix(fv.Leaf, protocol.FileExtension) {
			t.Errorf("%s: 叶子名 %q 没有以 %q 结尾", fv.Slug, fv.Leaf, protocol.FileExtension)
		}

		if !fv.FilenameEnc {
			// 未开启加密：叶子名就是展示名 + 扩展名，一目了然
			if want := fv.DisplayName + protocol.FileExtension; fv.Leaf != want {
				t.Errorf("%s: filename_enc=false 时叶子名 = %q，应为 %q", fv.Slug, fv.Leaf, want)
			}
			plainCount++
			continue
		}

		encCount++
		stem := strings.TrimSuffix(fv.Leaf, protocol.FileExtension)
		if stem == fv.DisplayName {
			t.Fatalf("%s: filename_enc=true 但叶子名仍是明文展示名，加密没生效", fv.Slug)
		}
		// Base32 字母表不含小写与非 ASCII，可据此确认叶子名确实是编码产物
		if !isBase32NoPad(stem) {
			t.Errorf("%s: 叶子名主体 %q 不是合法的去填充 Base32", fv.Slug, stem)
		}

		got, err := cryptox.DecryptFilename(stem, fnKey)
		if err != nil {
			t.Fatalf("%s: 解密叶子名失败: %v", fv.Slug, err)
		}
		if got != fv.DisplayName {
			t.Errorf("%s: 叶子名解出 %q，夹具声明的展示名是 %q", fv.Slug, got, fv.DisplayName)
		}
	}

	if encCount == 0 || plainCount == 0 {
		t.Errorf("filename_enc 覆盖不全：加密 %d 条、未加密 %d 条（两种形态都必须有）", encCount, plainCount)
	}
}

// TestEveryFixtureFileIsAccountedFor 反向核对 testdata 目录里没有孤儿文件。
//
// 生成器每次运行都会先清空 plain/ cpenc/ vault/ 三个子目录，但 testdata/go/
// 由 Go 侧维护、不受生成器管辖。这条测试确保 vectors.json 引用的每个文件
// 都真实存在，且 Python 侧目录下没有未被引用的残留 —— 残留夹具会让人
// 误以为某个用例还在生效。
func TestEveryFixtureFileIsAccountedFor(t *testing.T) {
	v := loadVectors(t)

	referenced := map[string]bool{"vectors.json": true}
	for _, fv := range v.Files {
		if fv.PlainPath != nil {
			referenced[*fv.PlainPath] = true
		}
		if fv.CipherPath != nil {
			referenced[*fv.CipherPath] = true
		}
	}
	for _, vv := range v.Vaults {
		referenced[vv.Path] = true
	}

	for _, sub := range []string{"plain", "cpenc", "vault"} {
		entries, err := readDirNames(sub)
		if err != nil {
			t.Fatalf("列目录 %s 失败: %v", sub, err)
		}
		for _, name := range entries {
			rel := path.Join(sub, name)
			if !referenced[rel] {
				t.Errorf("testdata/%s 未被 vectors.json 引用（孤儿夹具，请重跑 gen_vectors.py）", rel)
			}
		}
	}

	// 反向：引用的文件必须真的能读到
	for rel := range referenced {
		if rel == "vectors.json" {
			continue
		}
		if _, err := readFixtureBytes(rel); err != nil {
			t.Errorf("vectors.json 引用了不存在的夹具 %s: %v", rel, err)
		}
	}
}

// firstDiff 定位两个字节序列的首个差异，避免失败信息里倒出几 MB 的 hex。
func firstDiff(got, want []byte) string {
	n := min(len(got), len(want))
	for i := 0; i < n; i++ {
		if got[i] != want[i] {
			lo := max(0, i-4)
			hi := min(n, i+5)
			return "首个差异在偏移 " + itoa(i) +
				"：Go=" + hex.EncodeToString(got[lo:hi]) +
				" 夹具=" + hex.EncodeToString(want[lo:hi])
		}
	}
	if len(got) != len(want) {
		return "前 " + itoa(n) + " 字节相同，长度不同：Go=" + itoa(len(got)) + " 夹具=" + itoa(len(want))
	}
	return "无差异"
}

// isBase32NoPad 判断字符串是否只含 RFC 4648 Base32 字母表字符（不含填充 =）。
func isBase32NoPad(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune(protocol.Base32Alphabet, r) {
			return false
		}
	}
	return true
}

// itoa 是 strconv.Itoa 的短别名，只为让 firstDiff 的拼接读起来不断行。
func itoa(n int) string { return strconv.Itoa(n) }
