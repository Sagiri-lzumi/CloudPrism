package interop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// ---------------------------------------------------------------------------
// vectors.json 的 Go 侧模型
//
// 字段名与 gen_vectors.py 产出的键一一对应。任何一侧改名都必须同步另一侧，
// 否则 json.Unmarshal 会静默留零值 —— 因此 TestVectorsSchema 专门断言
// 关键字段非零，把「静默留零」变成响亮的失败。
// ---------------------------------------------------------------------------

// vectors 是 testdata/vectors.json 的根。
type vectors struct {
	Meta        meta          `json:"meta"`
	FilenameKey keyInfo       `json:"filename_key"`
	Files       []fileVector  `json:"files"`
	Vaults      []vaultVector `json:"vaults"`
	Filenames   []nameVector  `json:"filenames"`
	GoEmit      goEmit        `json:"go_emit"`
}

// meta 记录生成环境与协议版本，用于与 protocol 常量对账。
type meta struct {
	Generator       string  `json:"generator"`
	Schema          int     `json:"schema"`
	MasterPassword  string  `json:"master_password"`
	ProtocolVersion uint32  `json:"protocol_version"`
	VaultVersion    uint32  `json:"vault_version"`
	KDF             kdfInfo `json:"kdf"`
}

type kdfInfo struct {
	Algo       string `json:"algo"`
	Iterations int    `json:"iterations"`
	KeyLen     int    `json:"key_len"`
}

// keyInfo 是一对「盐 + 派生密钥」的 hex 表示。
type keyInfo struct {
	Salt string `json:"salt"`
	Key  string `json:"key"`
}

// recipe 描述确定性明文的生成配方。
type recipe struct {
	Algo string `json:"algo"`
	Seed string `json:"seed"`
}

// rangeVector 是一组随机访问解密用例（明文区间 [Start, End)）。
type rangeVector struct {
	Start     int64  `json:"start"`
	End       int64  `json:"end"`
	ExpectHex string `json:"expect_hex"`
}

// fileVector 是一个 .cpenc 夹具。
//
// Kind 决定比对方式：
//   - literal   ：明文已落盘（PlainPath），密文已落盘（CipherPath），直接读盘比对
//   - recipe    ：只落盘密文，明文由 Recipe 在两端各自重建
//   - hash_only ：都不落盘（大文件），只比 sha256；Go 侧本地重建后对哈希
type fileVector struct {
	Slug         string        `json:"slug"`
	Kind         string        `json:"kind"`
	Size         int64         `json:"size"`
	MaxWorkers   int           `json:"max_workers"`
	Recipe       recipe        `json:"recipe"`
	Salt         string        `json:"salt"`
	IV           string        `json:"iv"`
	Flags        byte          `json:"flags"`
	Version      uint32        `json:"version"`
	HeaderLength uint32        `json:"header_length"`
	DisplayName  string        `json:"display_name"`
	FilenameEnc  bool          `json:"filename_enc"`
	Leaf         string        `json:"leaf"`
	PlainSHA256  string        `json:"plain_sha256"`
	CipherSHA256 string        `json:"cipher_sha256"`
	PlainPath    *string       `json:"plain_path"`
	CipherPath   *string       `json:"cipher_path"`
	Ranges       []rangeVector `json:"ranges"`
}

// vaultExpect 是 VaultMarker.verify 应返回的字段。
type vaultExpect struct {
	Version         uint32 `json:"version"`
	Name            string `json:"name"`
	FilenameEnc     bool   `json:"filename_enc"`
	ProtocolVersion uint32 `json:"protocol_version"`
	HasRecovery     bool   `json:"has_recovery"`
	VaultID         string `json:"vault_id"`
	Salt            string `json:"salt"`
	IV              string `json:"iv"`
}

// vaultVector 是一个 Vault Marker 夹具。
//
// RecoveryBlob 是 Python 侧 build_recovery_blob 的产物（内含随机盐）。
// Go 侧把它**原样**传给 CreateMarker，因此带恢复块的 Marker 同样能做
// 逐字节重建比对 —— 随机性被固化在夹具里，而不是留在生成过程中。
type vaultVector struct {
	Slug            string      `json:"slug"`
	Path            string      `json:"path"`
	Password        string      `json:"password"`
	VaultID         string      `json:"vault_id"`
	Salt            string      `json:"salt"`
	IV              string      `json:"iv"`
	Version         uint32      `json:"version"`
	ProtocolVersion uint32      `json:"protocol_version"`
	NameIn          string      `json:"name_in"`
	RecoverySecret  string      `json:"recovery_secret"`
	RecoveryBlob    string      `json:"recovery_blob"`
	RecoveryCode    string      `json:"recovery_code"`
	BytesSHA256     string      `json:"bytes_sha256"`
	Length          int         `json:"length"`
	Expect          vaultExpect `json:"expect"`
	Split           splitInfo   `json:"split"`
}

type splitInfo struct {
	HeadLen int    `json:"head_len"`
	TailHex string `json:"tail_hex"`
}

// nameVector 是一对文件名加密产物。
type nameVector struct {
	Plain string `json:"plain"`
	Enc   string `json:"enc"`
}

// goEmit 描述 Go 侧应产出的反向夹具（testdata/go/），Python 侧读它做验证。
type goEmit struct {
	Cpenc    []goEmitCpenc   `json:"cpenc"`
	Vault    []goEmitVault   `json:"vault"`
	Filename goEmitFilenames `json:"filename"`
}

type goEmitCpenc struct {
	Slug    string `json:"slug"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Recipe  recipe `json:"recipe"`
	Salt    string `json:"salt"`
	IV      string `json:"iv"`
	Flags   byte   `json:"flags"`
	Version uint32 `json:"version"`
}

type goEmitVault struct {
	Slug              string  `json:"slug"`
	Path              string  `json:"path"`
	Password          string  `json:"password"`
	VaultID           string  `json:"vault_id"`
	Salt              string  `json:"salt"`
	IV                string  `json:"iv"`
	Version           uint32  `json:"version"`
	ProtocolVersion   uint32  `json:"protocol_version"`
	Name              string  `json:"name"`
	FilenameEnc       bool    `json:"filename_enc"`
	RecoverySecret    *string `json:"recovery_secret"`
	RecoveryCode      *string `json:"recovery_code"`
	ExpectName        string  `json:"expect_name"`
	ExpectHasRecovery bool    `json:"expect_has_recovery"`
}

type goEmitFilenames struct {
	Salt  string           `json:"salt"`
	Key   string           `json:"key"`
	Items []goEmitNameItem `json:"items"`
}

type goEmitNameItem struct {
	Index int    `json:"index"`
	Path  string `json:"path"`
	Plain string `json:"plain"`
}

// ---------------------------------------------------------------------------
// 夹具加载与通用助手
// ---------------------------------------------------------------------------

// testdataDir 是夹具根目录（相对本包，go test 的 cwd 就是包目录）。
const testdataDir = "testdata"

// emitEnvVar 打开后 TestEmitGoArtifacts 才会写 testdata/go/。
//
// 默认关闭是刻意的：常规 go test 必须只读不写，否则每次跑测试都可能
// 污染工作树（文件名 nonce 随机，产物每轮都变），git status 恒脏。
const emitEnvVar = "CLOUDPRISM_INTEROP_EMIT"

// streamChunk 是分块步长，与 Python 侧 Encryptor.DEFAULT_CHUNK 同值。
//
// 必须是 16 的倍数：XORAt 按 blockIdx 定位密钥流，步长非对齐会让每块
// 起点错位 —— 这正是阶段 0 修掉的 P0 缺陷的成因。
const streamChunk = 1 << 20

// recipeAlgo 是唯一支持的明文配方标识；出现别的值说明夹具由更新版的
// 生成器产出，本端必须同步升级而不是默默跳过。
// 实现与标识的导出版见 recipe.go（pipeline 等跨包用方共用同一份）。
const recipeAlgo = RecipeAlgo

// loadVectors 读取并反序列化 vectors.json。
func loadVectors(t *testing.T) *vectors {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(testdataDir, "vectors.json"))
	if err != nil {
		t.Fatalf("读取 vectors.json 失败（请先运行 gen_vectors.py 生成夹具）: %v", err)
	}

	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("解析 vectors.json 失败: %v", err)
	}
	return &v
}

// readFixture 按 vectors.json 里的相对路径读取夹具文件。
func readFixture(t *testing.T, rel string) []byte {
	t.Helper()

	data, err := readFixtureBytes(rel)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return data
}

// readFixtureBytes 是 readFixture 的不终止版本，供需要把「文件缺失」
// 当成普通断言失败（而非直接 Fatal）的场合使用。
func readFixtureBytes(rel string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(testdataDir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, fmt.Errorf("读取夹具 %s 失败: %w", rel, err)
	}
	return data, nil
}

// readDirNames 列出 testdata 下某个子目录的文件名（不递归）。
func readDirNames(sub string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(testdataDir, filepath.FromSlash(sub)))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// mustHex 把 hex 字符串解成字节，失败即终止。
//
// what 用于报错定位：夹具里几十个 hex 字段，只给「解码失败」四个字
// 等于没给信息。
func mustHex(t *testing.T, s, what string) []byte {
	t.Helper()

	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("%s 不是合法 hex（%q）: %v", what, s, err)
	}
	return b
}

// sha256Hex 返回字节的 sha256 十六进制小写摘要。
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// assertSHA256 比对摘要，失败时打印双方取值。
func assertSHA256(t *testing.T, what, got, want string) {
	t.Helper()

	if got != want {
		t.Errorf("%s 的 sha256 不符：\n  Go 侧 = %s\n  夹具  = %s", what, got, want)
	}
}

// recipePlain 按配方重建确定性明文（委托 recipe.go 的导出实现）。
//
// 契约源头是 gen_vectors.py 的 recipe_plain —— 它是两端明文约定的唯一
// 载体。TestRecipePlainMatchesLiteralFixtures 用已落盘的明文原件反过来
// 验证本实现，避免「配方写错 → 所有下游用例一起红且无从定位」。
func recipePlain(t *testing.T, r recipe, size int64) []byte {
	t.Helper()

	if r.Algo != RecipeAlgo {
		t.Fatalf("未知的明文配方 %q（本端只支持 %q），生成器与测试代码版本不匹配", r.Algo, RecipeAlgo)
	}
	return RecipePlain(mustHex(t, r.Seed, "配方种子"), size)
}

// expectedPlain 取得某个夹具应有的明文：优先读落盘原件，否则按配方重建。
//
// 两条路径互为交叉验证：literal 条目两者都有，TestLiteralPlainEqualsRecipe
// 断言它们相等，于是「配方实现」与「Python 真实产出」被绑在一起。
func expectedPlain(t *testing.T, fv fileVector) []byte {
	t.Helper()

	if fv.PlainPath != nil {
		return readFixture(t, *fv.PlainPath)
	}
	return recipePlain(t, fv.Recipe, fv.Size)
}

// deriveKey 用主密码 + 盐派生 32 字节密钥，并顺带校验 KDF 参数与夹具一致。
func deriveKey(t *testing.T, password string, salt []byte) []byte {
	t.Helper()

	key := cryptox.DeriveKey(password, salt)
	return key[:]
}

// buildCpenc 用 Go 原语组装一个完整的 .cpenc 容器（头 + 密文）。
//
// 这里刻意不复用未来的 pkg/pipeline：本包要在 pipeline 存在之前就能跑，
// 且它验证的正是「原语按生产方式组合后与 Python 逐字节相同」这一命题本身。
// pipeline 落地后需另写测试证明 pipeline 与本函数的产物一致。
func buildCpenc(t *testing.T, password string, plain, salt, iv []byte, flags byte, version uint32) []byte {
	t.Helper()

	header, err := cryptox.Build(salt, iv, flags, version)
	if err != nil {
		t.Fatalf("构建文件头失败: %v", err)
	}

	key := deriveKey(t, password, salt)
	ctr, err := cryptox.NewCTR(key, iv)
	if err != nil {
		t.Fatalf("初始化 CTR 失败: %v", err)
	}

	// 一次分配到位：容器长度 = 头长 + 明文长（CTR 无填充，两者等长）
	out := make([]byte, len(header)+len(plain))
	copy(out, header)
	copy(out[len(header):], plain)

	body := out[len(header):]
	for off := 0; off < len(body); off += streamChunk {
		// 步长恒为 16 的倍数，故 off/16 无余数丢弃。断言写在这里而不是
		// 只写在注释里：一旦有人把 streamChunk 改成非对齐值，立即失败。
		if off%protocol.BlockSize != 0 {
			t.Fatalf("分块起点 %d 未按 %d 字节对齐，密文会整段错位", off, protocol.BlockSize)
		}
		end := min(off+streamChunk, len(body))
		ctr.XORAt(body[off:end], plain[off:end], uint64(off/protocol.BlockSize))
	}
	return out
}

// decryptCpenc 解析文件头并把密文主体解密为明文。
func decryptCpenc(t *testing.T, password string, data []byte) (*cryptox.Header, []byte) {
	t.Helper()

	hdr, err := cryptox.ParseBytes(data)
	if err != nil {
		t.Fatalf("解析文件头失败: %v", err)
	}
	if int64(len(data)) < hdr.CipherOffset() {
		t.Fatalf("文件长度 %d 不足以容纳声明的头部长度 %d", len(data), hdr.HeaderLength)
	}

	key := deriveKey(t, password, hdr.Salt)
	ctr, err := cryptox.NewCTR(key, hdr.IV)
	if err != nil {
		t.Fatalf("初始化 CTR 失败: %v", err)
	}

	body := data[hdr.CipherOffset():]
	out := make([]byte, len(body))
	for off := 0; off < len(body); off += streamChunk {
		end := min(off+streamChunk, len(body))
		ctr.XORAt(out[off:end], body[off:end], uint64(off/protocol.BlockSize))
	}
	return hdr, out
}

// decryptRange 复刻 Python 侧 Decryptor.decrypt_range_to_bytes 的随机访问语义：
// 按 16 字节块对齐拉取密文超集，从 firstBlock 起解密，再切片到 [start, end)。
//
// 对照 WindowsPy/src/cloudprism/core/decryptor.py:99-134
// 与 WindowsPy/src/cloudprism/crypto/stream_cipher.py:50-81
func decryptRange(t *testing.T, password string, data []byte, hdr *cryptox.Header, start, end int64) []byte {
	t.Helper()

	if end <= start {
		return nil
	}
	cipherSize := int64(len(data))

	firstBlock := start / protocol.BlockSize
	lastBlock := (end - 1) / protocol.BlockSize
	ctStart := hdr.CipherOffset() + firstBlock*protocol.BlockSize
	ctEnd := hdr.CipherOffset() + (lastBlock+1)*protocol.BlockSize - 1
	if ctEnd > cipherSize-1 {
		ctEnd = cipherSize - 1 // 末块可能不满 16 字节，收敛到文件末尾
	}
	if ctStart > ctEnd {
		t.Fatalf("区间 [%d,%d) 超出密文范围（文件 %d 字节）", start, end, cipherSize)
	}

	key := deriveKey(t, password, hdr.Salt)
	ctr, err := cryptox.NewCTR(key, hdr.IV)
	if err != nil {
		t.Fatalf("初始化 CTR 失败: %v", err)
	}

	ct := data[ctStart : ctEnd+1]
	decrypted := make([]byte, len(ct))
	ctr.XORAt(decrypted, ct, uint64(firstBlock))

	localStart := start - firstBlock*protocol.BlockSize
	localEnd := end - firstBlock*protocol.BlockSize
	if localEnd > int64(len(decrypted)) {
		t.Fatalf("区间 [%d,%d) 越过解密结果长度 %d", start, end, len(decrypted))
	}
	return decrypted[localStart:localEnd]
}

// assertHeaderMatches 断言解析出的文件头与夹具记录逐字段一致。
func assertHeaderMatches(t *testing.T, slug string, hdr *cryptox.Header, fv fileVector) {
	t.Helper()

	if got, want := hex.EncodeToString(hdr.Salt), fv.Salt; got != want {
		t.Errorf("%s: 头内 salt = %s，夹具 = %s", slug, got, want)
	}
	if got, want := hex.EncodeToString(hdr.IV), fv.IV; got != want {
		t.Errorf("%s: 头内 iv = %s，夹具 = %s", slug, got, want)
	}
	if hdr.Flags != fv.Flags {
		t.Errorf("%s: 头内 flags = %#02x，夹具 = %#02x", slug, hdr.Flags, fv.Flags)
	}
	if hdr.Version != fv.Version {
		t.Errorf("%s: 头内 version = %d，夹具 = %d", slug, hdr.Version, fv.Version)
	}
	if hdr.HeaderLength != fv.HeaderLength {
		t.Errorf("%s: 头内 header_length = %d，夹具 = %d", slug, hdr.HeaderLength, fv.HeaderLength)
	}
}

// TestVectorsSchema 断言夹具本身结构完整。
//
// 这个测试看着像在测 JSON，实际是在防一类很难查的失败：字段名两端不同步时
// json.Unmarshal 不报错、只留零值，于是下游断言拿 "" 和 0 去比，
// 报出来的错误信息完全指错方向。这里先把「关键字段必须有值」钉住。
func TestVectorsSchema(t *testing.T) {
	v := loadVectors(t)

	if v.Meta.Schema != 1 {
		t.Fatalf("夹具 schema = %d，本端只认 1", v.Meta.Schema)
	}
	if v.Meta.MasterPassword == "" {
		t.Fatal("夹具缺少 master_password")
	}
	if len(v.Files) == 0 {
		t.Fatal("夹具里没有任何 .cpenc 条目")
	}
	if len(v.Vaults) == 0 {
		t.Fatal("夹具里没有任何 Vault Marker 条目")
	}
	if len(v.Filenames) == 0 {
		t.Fatal("夹具里没有任何文件名加密条目")
	}

	// 协议版本必须与 protocol 常量对账：单侧升版本而忘了重新生成夹具，
	// 会表现为「一堆用例莫名其妙地红」，这条断言把它提前成一个明确的信号。
	if v.Meta.ProtocolVersion != protocol.Version {
		t.Errorf("夹具 protocol_version = %d，protocol.Version = %d（有一侧改了版本却没重生成夹具）",
			v.Meta.ProtocolVersion, protocol.Version)
	}
	if v.Meta.VaultVersion != protocol.VaultVersion {
		t.Errorf("夹具 vault_version = %d，protocol.VaultVersion = %d", v.Meta.VaultVersion, protocol.VaultVersion)
	}
	if v.Meta.KDF.Iterations != protocol.KDFIterations {
		t.Errorf("夹具 KDF 迭代数 = %d，protocol.KDFIterations = %d", v.Meta.KDF.Iterations, protocol.KDFIterations)
	}
	if v.Meta.KDF.KeyLen != protocol.KeyLen {
		t.Errorf("夹具 KDF 密钥长度 = %d，protocol.KeyLen = %d", v.Meta.KDF.KeyLen, protocol.KeyLen)
	}
	if v.Meta.KDF.Algo != protocol.KDFAlgo {
		t.Errorf("夹具 KDF 算法 = %q，protocol.KDFAlgo = %q", v.Meta.KDF.Algo, protocol.KDFAlgo)
	}

	// 文件名密钥必须由主密码 + 夹具里的盐派生得出 —— 这条同时验证了
	// Go 与 Python 的 PBKDF2 在真实参数下一致，且夹具没有自相矛盾。
	fnSalt := mustHex(t, v.FilenameKey.Salt, "文件名密钥盐")
	fnKey := mustHex(t, v.FilenameKey.Key, "文件名密钥")
	if got := deriveKey(t, v.Meta.MasterPassword, fnSalt); hex.EncodeToString(got) != v.FilenameKey.Key {
		t.Errorf("由主密码 + 盐派生的文件名密钥与夹具不符：\n  Go 侧 = %x\n  夹具  = %s", got, v.FilenameKey.Key)
	}
	if len(fnKey) != protocol.KeyLen {
		t.Errorf("夹具里的文件名密钥长度 = %d，应为 %d", len(fnKey), protocol.KeyLen)
	}
}

// TestRecipePlainMatchesLiteralFixtures 用已落盘的明文原件验证配方实现。
//
// 这是整套大文件哈希比对的地基：hash_only 条目两端都不落盘明文，全靠
// 配方各自重建。若 Go 的配方与 Python 不同，那些用例会以「cipher sha256
// 不符」的形式失败，看起来像加密错了，其实是明文错了。先在这里把配方
// 单独钉死，下游失败才有明确指向。
func TestRecipePlainMatchesLiteralFixtures(t *testing.T) {
	v := loadVectors(t)

	checked := 0
	for _, fv := range v.Files {
		if fv.PlainPath == nil {
			continue
		}
		fromDisk := readFixture(t, *fv.PlainPath)
		fromRecipe := recipePlain(t, fv.Recipe, fv.Size)

		if int64(len(fromDisk)) != fv.Size {
			t.Fatalf("%s: 明文原件长度 = %d，夹具声明 %d", fv.Slug, len(fromDisk), fv.Size)
		}
		if sha256Hex(fromDisk) != fv.PlainSHA256 {
			t.Errorf("%s: 明文原件的 sha256 与夹具不符（夹具文件被改过？）", fv.Slug)
		}
		if got, want := sha256Hex(fromRecipe), fv.PlainSHA256; got != want {
			t.Errorf("%s: 配方重建的明文 sha256 = %s，应为 %s", fv.Slug, got, want)
		}
		checked++
	}

	if checked != len(literalSlugs(v)) {
		t.Errorf("只校验了 %d 个明文原件，夹具里有 %d 个", checked, len(literalSlugs(v)))
	}
}

// literalSlugs 返回所有落盘了明文原件的条目 slug。
func literalSlugs(v *vectors) []string {
	var out []string
	for _, fv := range v.Files {
		if fv.PlainPath != nil {
			out = append(out, fv.Slug)
		}
	}
	return out
}

// TestLiteralPlainEqualsRecipe 交叉验证「落盘原件」与「配方重建」两条取明文的路径。
//
// 与上一个测试的区别：上一个比 sha256，这个比字节并覆盖**全部**尺寸
// （含只落密文的 recipe 条目）。两者都过，才能说配方在 0/1/15/16/17/51/52
// 这些块边界上也是对的 —— 而边界恰恰是最容易差一字节的地方。
func TestLiteralPlainEqualsRecipe(t *testing.T) {
	v := loadVectors(t)

	for _, fv := range v.Files {
		if fv.PlainPath == nil {
			continue
		}
		fromDisk := readFixture(t, *fv.PlainPath)
		fromRecipe := recipePlain(t, fv.Recipe, fv.Size)
		if len(fromDisk) != len(fromRecipe) {
			t.Fatalf("%s: 原件 %d 字节，配方 %d 字节", fv.Slug, len(fromDisk), len(fromRecipe))
		}
		for i := range fromDisk {
			if fromDisk[i] != fromRecipe[i] {
				t.Fatalf("%s: 明文在第 %d 字节处分叉（原件 %#02x，配方 %#02x）",
					fv.Slug, i, fromDisk[i], fromRecipe[i])
			}
		}
	}
}
