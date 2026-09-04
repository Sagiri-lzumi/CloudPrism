package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/interop"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// interopTestdata 是黄金夹具目录（interop/testdata，跨包共享防双份漂移）：
// 由 WindowsPy 端 gen_vectors.py 一次性生成提交，Go 侧只读。
// go test 的工作目录恒为包目录，故可用相对路径引用。
const interopTestdata = "../../interop/testdata"

// vectorRecipe 对应 vectors.json 的 recipe 字段。
type vectorRecipe struct {
	Algo string `json:"algo"`
	Seed string `json:"seed"`
}

// vectorFile 对应 vectors.json 中 files 数组的条目（只取测试需要的字段）。
type vectorFile struct {
	Slug         string       `json:"slug"`
	Kind         string       `json:"kind"` // literal / recipe / hash_only
	Size         int64        `json:"size"`
	MaxWorkers   int          `json:"max_workers"`
	Recipe       vectorRecipe `json:"recipe"`
	Salt         string       `json:"salt"`
	IV           string       `json:"iv"`
	PlainPath    *string      `json:"plain_path"`  // recipe/hash_only 为 null
	CipherPath   *string      `json:"cipher_path"` // hash_only 为 null
	PlainSHA256  string       `json:"plain_sha256"`
	CipherSHA256 string       `json:"cipher_sha256"`
	MasterPw     string       // 由 meta 注入
}

type vectorsDoc struct {
	Meta struct {
		MasterPassword string `json:"master_password"`
	} `json:"meta"`
	Files []*vectorFile `json:"files"`
}

func loadVectors(t *testing.T) *vectorsDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(interopTestdata, "vectors.json"))
	if err != nil {
		t.Fatalf("读取黄金向量失败（夹具缺失？先运行 WindowsPy/interop/gen_vectors.py）: %v", err)
	}
	var doc vectorsDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("vectors.json 解析失败: %v", err)
	}
	if len(doc.Files) == 0 {
		t.Fatal("vectors.json 无 files 条目")
	}
	for _, f := range doc.Files {
		f.MasterPw = doc.Meta.MasterPassword
	}
	return &doc
}

// plainBytes 取得条目的明文实体：literal 读盘（原件优先），否则按配方重建
// （recipe / hash_only 不落盘明文，配方即契约）。
func (v *vectorFile) plainBytes(t *testing.T) []byte {
	t.Helper()
	if v.PlainPath != nil {
		return readFixture(t, *v.PlainPath)
	}
	return interop.RecipePlain(mustHex(t, v.Recipe.Seed, v.Slug+" 配方种子"), v.Size)
}

// sessionFor 用夹具主密码建会话（每条独立创建，成本可控）。
func sessionFor(t *testing.T, pw string) *session.Session {
	t.Helper()
	s := session.New(pw)
	t.Cleanup(s.Close)
	return s
}

// encryptVector 用给定 workers 定 salt/iv 加密条目明文，返回容器字节。
func encryptVector(t *testing.T, s *session.Session, v *vectorFile, workers int) []byte {
	t.Helper()
	src := filepath.Join(t.TempDir(), "plain.bin")
	if err := os.WriteFile(src, v.plainBytes(t), 0o644); err != nil {
		t.Fatal(err)
	}
	dst, err := os.CreateTemp(t.TempDir(), "out_*.cpenc")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	enc := &Encryptor{
		Sess:    s,
		Workers: workers,
		Salt:    mustHex(t, v.Salt, v.Slug+" 盐"),
		IV:      mustHex(t, v.IV, v.Slug+" IV"),
	}
	header, err := enc.EncryptFile(context.Background(), src, dst, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, int64(len(header))+v.Size)
	if _, err := dst.ReadAt(got, 0); err != nil {
		t.Fatal(err)
	}
	return got
}

// ---------------------------------------------------------------------------
// 分片数学
// ---------------------------------------------------------------------------

// TestShardOffsetsAreBlockAligned 任意尺寸分片偏移必须 16 字节对齐：
// 这是「并行产物 == 单核产物」的数学前提（16 对齐纪律，见包注释）。
func TestShardOffsetsAreBlockAligned(t *testing.T) {
	sizes := []int64{0, 1, 15, 16, 17, 51, 52, 4095, 4096, 65539,
		ShardAlign - 1, ShardAlign, ShardAlign + 1, 3*ShardAlign + 12345, 20 * ShardAlign}
	for _, size := range sizes {
		shards := Shards(size)
		if size == 0 {
			if len(shards) != 0 {
				t.Fatalf("size=0 应无分片，实得 %v", shards)
			}
			continue
		}
		var total int64
		for _, s := range shards {
			if s.Offset%int64(protocol.BlockSize) != 0 {
				t.Errorf("size=%d 分片偏移 %d 未 16 对齐", size, s.Offset)
			}
			if s.Offset%ShardAlign != 0 {
				t.Errorf("size=%d 分片偏移 %d 未按 ShardAlign 对齐", size, s.Offset)
			}
			if s.Length <= 0 || s.Length > ShardAlign {
				t.Errorf("size=%d 分片长度非法: %d", size, s.Length)
			}
			total += s.Length
		}
		if total != size {
			t.Errorf("size=%d 分片覆盖不完整: %d", size, total)
		}
	}
}

// TestPlaintextToCipher 换算数学对照 range_mapper.py（两端必须同式）。
func TestPlaintextToCipher(t *testing.T) {
	const hl = 51
	cases := []struct{ start, end int64 }{
		{0, 1}, {0, 16}, {1, 17}, {15, 16}, {16, 32}, {17, 52},
		{0, 4095}, {500, 501}, {4094, 4096},
	}
	for _, c := range cases {
		r := PlaintextToCipher(hl, c.start, c.end)
		if r.FirstBlock != c.start/16 {
			t.Errorf("[%d,%d) first_block=%d 期望 %d", c.start, c.end, r.FirstBlock, c.start/16)
		}
		if r.CtStart != hl+(c.start/16)*16 {
			t.Errorf("[%d,%d) ct_start=%d 期望 %d", c.start, c.end, r.CtStart, hl+(c.start/16)*16)
		}
		// 密文区间必须完整覆盖明文区间
		if r.CtStart > hl+c.start || r.CtEnd < hl+c.end {
			t.Errorf("[%d,%d) 密文区间 [%d,%d) 未覆盖明文", c.start, c.end, r.CtStart, r.CtEnd)
		}
	}
	if got := PlaintextTotal(hl, 262149+hl); got != 262149 {
		t.Errorf("PlaintextTotal 异常: %d", got)
	}
}

// ---------------------------------------------------------------------------
// 加密方向：Go 产物 == Python 产物（三重断言）
// ---------------------------------------------------------------------------

// TestEncryptEqualsPythonVectors 对全部黄金向量注入相同 salt/iv 后加密，
// 产物必须与 Python 端记录逐字节一致（literal 直比文件；recipe/hash_only
// 比对 cipher_sha256，等价于逐字节比较）。
//
// par_*（hash_only）是 Python 用 workers=2/4 并行加密的规格：本测试按
// 记录的 max_workers 并行加密再比对哈希，即「并行 == Python」的锚点。
func TestEncryptEqualsPythonVectors(t *testing.T) {
	doc := loadVectors(t)
	sess := sessionFor(t, doc.Meta.MasterPassword)

	for _, v := range doc.Files {
		name := v.Slug
		t.Run(name, func(t *testing.T) {
			got := encryptVector(t, sess, v, v.MaxWorkers)
			if v.CipherSHA256 != "" {
				if sum := sha256Hex(got); sum != v.CipherSHA256 {
					t.Errorf("密文 sha256 不符：\n  Go 侧 = %s\n  夹具  = %s", sum, v.CipherSHA256)
				}
			}
			// literal 条目额外直比已落盘的 Python 密文原件
			if v.CipherPath != nil {
				want := readFixture(t, *v.CipherPath)
				if !bytes.Equal(got, want) {
					t.Error("Go 加密产物与 Python 密文原件逐字节不一致")
				}
			}
		})
	}
}

// TestParallelEqualsSequentialOnPythonSpec 并行==单核的第二个锚点：
// par_* 条目用 workers=1 再加密一次，必须与 Python 并行产物的哈希相同
// （即 Go 并行 == Go 单核 == Python 并行）。
func TestParallelEqualsSequentialOnPythonSpec(t *testing.T) {
	doc := loadVectors(t)
	sess := sessionFor(t, doc.Meta.MasterPassword)

	for _, v := range doc.Files {
		if v.Kind != "hash_only" {
			continue // 只有 par_* 超过并行阈值
		}
		t.Run(v.Slug, func(t *testing.T) {
			seq := encryptVector(t, sess, v, 1)
			par := encryptVector(t, sess, v, v.MaxWorkers)
			if !bytes.Equal(seq, par) {
				t.Error("Go 单核与并行加密产物不一致（16 字节对齐缺陷回归）")
			}
			if sum := sha256Hex(par); sum != v.CipherSHA256 {
				t.Errorf("Go 并行产物 sha256 与 Python 规格不符：\n  Go = %s\n  Py = %s", sum, v.CipherSHA256)
			}
		})
	}
}

// TestParallelRoundTrip 一般性往返：随机明文（非配方）>4MiB 并行加密
// 后下载解密回原文，且进度回调单调非降、终值 1.0。
func TestParallelRoundTrip(t *testing.T) {
	sess := sessionFor(t, "pipeline-parallel-pw")

	const size = 5*ShardAlign + 12345 // 非 16 对齐，横跨 6 个分片
	payload := make([]byte, size)
	rng := rand.New(rand.NewSource(20260904))
	if _, err := rng.Read(payload); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	salt := make([]byte, protocol.SaltLen)
	iv := make([]byte, protocol.IVLen)
	for i := range salt {
		salt[i], iv[i] = byte(i*3+7), byte(i*5+11)
	}

	dst, err := os.CreateTemp(dir, "out_*.cpenc")
	if err != nil {
		t.Fatal(err)
	}
	container := dst.Name() // CreateTemp 实际产出的文件名
	enc := &Encryptor{Sess: sess, Workers: 4, Salt: salt, IV: iv}
	var last float64
	if _, err := enc.EncryptFile(context.Background(), src, dst, func(p float64) {
		if p < last || p > 1.0 {
			t.Errorf("进度非单调或越界: %f -> %f", last, p)
		}
		last = p
	}); err != nil {
		t.Fatal(err)
	}
	dst.Close()
	if last != 1.0 {
		t.Errorf("进度终值应为 1.0，实得 %f", last)
	}

	// 容器搬进本地后端，并行下载解密回原文
	root := filepath.Join(t.TempDir(), "backend")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(container, filepath.Join(root, "big.cpenc")); err != nil {
		t.Fatal(err)
	}
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(dir, "dec.bin")
	dec := &Decryptor{Sess: sess, Backend: b, Workers: 4}
	if err := dec.DownloadAndDecrypt(context.Background(), "big.cpenc", local, nil); err != nil {
		t.Fatal(err)
	}
	round, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(round, payload) {
		t.Error("并行加密产物解密回读与明文不一致")
	}
}

// ---------------------------------------------------------------------------
// 解密方向
// ---------------------------------------------------------------------------

// TestDecryptPythonVectors Go 下载解密全部密文夹具（literal 直比明文文件；
// recipe 比对 plain_sha256），非对齐/空文件/跨块全尺寸覆盖。
func TestDecryptPythonVectors(t *testing.T) {
	doc := loadVectors(t)
	sess := sessionFor(t, doc.Meta.MasterPassword)

	root := filepath.Join(interopTestdata, "cpenc")
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range doc.Files {
		if v.CipherPath == nil {
			continue // hash_only 无密文实体
		}
		name := v.Slug
		t.Run(name, func(t *testing.T) {
			local := filepath.Join(t.TempDir(), "out.bin")
			dec := &Decryptor{Sess: sess, Backend: b, Workers: 4}
			var last float64
			if err := dec.DownloadAndDecrypt(context.Background(), v.Slug+".cpenc", local, func(p float64) {
				if p < last || p > 1.0 {
					t.Errorf("进度非单调或越界: %f -> %f", last, p)
				}
				last = p
			}); err != nil {
				t.Fatal(err)
			}
			if sum := sha256HexOrFile(t, local); sum != v.PlainSHA256 {
				t.Errorf("解密产物 sha256 与 Python 明文不符：\n  Go = %s\n  Py = %s", sum, v.PlainSHA256)
			}
			// literal 条目有明文原件，直比最严格
			if v.PlainPath != nil {
				got, err := os.ReadFile(local)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, readFixture(t, *v.PlainPath)) {
					t.Error("解密产物与 Python 明文原件逐字节不一致")
				}
			}
		})
	}
}

// TestDecryptRangeToBytes 明文随机访问：非对齐窗口与 Python 明文切片
// 逐字节一致；越界/空区间/空文件边界行为。
func TestDecryptRangeToBytes(t *testing.T) {
	doc := loadVectors(t)
	sess := sessionFor(t, doc.Meta.MasterPassword)

	root := filepath.Join(interopTestdata, "cpenc")
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	dec := &Decryptor{Sess: sess, Backend: b}
	ctx := context.Background()

	// rec_262149 非对齐大文件 + lit_17 小文件（明文从盘上/配方取得）
	for _, slug := range []string{"rec_262149", "lit_17"} {
		var v *vectorFile
		for _, f := range doc.Files {
			if f.Slug == slug {
				v = f
				break
			}
		}
		if v == nil {
			t.Fatalf("向量缺失: %s", slug)
		}
		plain := v.plainBytes(t)
		if int64(len(plain)) != v.Size {
			t.Fatalf("%s 明文尺寸不符", slug)
		}

		windows := []struct{ start, end int64 }{
			{0, 0}, // end=0 → 取到文件尾
			{0, 1},
			{1, 17},     // 跨块非对齐
			{500, 4097}, // 中段
			{int64(len(plain)) - 5, int64(len(plain))},       // 尾部不满块
			{int64(len(plain)) - 3, int64(len(plain)) + 100}, // end 越界 clamp
			{int64(len(plain)), int64(len(plain)) + 10},      // 空区间
		}
		for _, w := range windows {
			data, err := dec.DecryptRangeToBytes(ctx, v.Slug+".cpenc", w.start, w.end)
			if err != nil {
				t.Fatalf("%s [%d,%d): %v", slug, w.start, w.end, err)
			}
			wantEnd := w.end
			if w.end <= 0 || w.end > int64(len(plain)) {
				wantEnd = int64(len(plain))
			}
			want := []byte{}
			if w.start < wantEnd {
				want = plain[w.start:wantEnd]
			}
			if !bytes.Equal(data, want) {
				t.Errorf("%s [%d,%d): 窗口解密与明文切片不一致 (%d vs %d)",
					slug, w.start, w.end, len(data), len(want))
			}
		}
	}

	// 空文件（lit_0）：任意窗口返回空
	data, err := dec.DecryptRangeToBytes(ctx, "lit_0.cpenc", 0, 0)
	if err != nil || len(data) != 0 {
		t.Errorf("空文件应返回空字节: len=%d err=%v", len(data), err)
	}
}

// TestCancelRespect 已取消的 ctx 应立即返回错误（不启动新分片）。
func TestCancelRespect(t *testing.T) {
	doc := loadVectors(t)
	sess := sessionFor(t, doc.Meta.MasterPassword)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预先取消
	root := filepath.Join(interopTestdata, "cpenc")
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	dec := &Decryptor{Sess: sess, Backend: b}
	if err := dec.DownloadAndDecrypt(ctx, "rec_262149.cpenc", filepath.Join(t.TempDir(), "o.bin"), nil); err == nil {
		t.Error("已取消 ctx 应报错")
	}
}

// ---------------------------------------------------------------------------
// 测试工具
// ---------------------------------------------------------------------------

func readFixture(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(interopTestdata, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustHex(t *testing.T, s, what string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("%s 不是合法 hex（%q）: %v", what, s, err)
	}
	return b
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sha256HexOrFile 读文件并返回其 sha256 摘要。
func sha256HexOrFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256Hex(data)
}

// copyFile 简单整文件拷贝（测试用）。
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}
