package cryptox

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// 文件头黄金向量：salt = 0x11*16、iv = 0x22*16，
// 由 WindowsPy 端 FileHeader.build 真实运行后打印（header.py:55-84）。
const (
	goldenHeaderSalt = "11111111111111111111111111111111"
	goldenHeaderIV   = "22222222222222222222222222222222"

	// flags=0x00、version=1 —— 即真实加密文件的头部，共 51 字节
	goldenHeader = "43505249534d0001" + // Magic  "CPRISM\x00\x01"
		"00000001" + // Version      = 1
		"00000033" + // HeaderLength = 51
		"10" + goldenHeaderSalt + // SaltLen=16 + Salt
		"10" + goldenHeaderIV + // IVLen=16 + IV
		"00" // Flags

	// flags=0x07、version=9 —— 验证非默认取值也按大端写入
	goldenHeaderCustom = "43505249534d0001" +
		"00000009" +
		"00000033" +
		"10" + goldenHeaderSalt +
		"10" + goldenHeaderIV +
		"07"
)

func TestBuildGolden(t *testing.T) {
	salt := mustHex(t, goldenHeaderSalt)
	iv := mustHex(t, goldenHeaderIV)

	tests := []struct {
		name    string
		flags   byte
		version uint32
		want    string
	}{
		{"默认取值", 0x00, protocol.Version, goldenHeader},
		{"自定义 flags 与 version", 0x07, 9, goldenHeaderCustom},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Build(salt, iv, tc.flags, tc.version)
			if err != nil {
				t.Fatalf("Build 失败: %v", err)
			}
			if len(got) != 51 {
				t.Errorf("头部长度应为 51（19 + 16 + 16），实为 %d", len(got))
			}
			if !bytes.Equal(got, mustHex(t, tc.want)) {
				t.Errorf("头部字节与 Python 端不一致\n got = %x\nwant = %s", got, tc.want)
			}
		})
	}
}

func TestBuildRejectsOverlongFields(t *testing.T) {
	iv := mustHex(t, goldenHeaderIV)

	// Python 侧此处由 struct.pack(">B", n) 抛 struct.error；
	// Go 若直接用 byte(n) 会静默截断成 0，拼出 HeaderLength 与实际不符的头，
	// 产物是「写得出、解不开」的坏文件。
	if _, err := Build(make([]byte, 256), iv, 0, protocol.Version); !errors.Is(err, ErrHeaderFieldTooLong) {
		t.Errorf("salt 长度 256 应返回 ErrHeaderFieldTooLong，实为 %v", err)
	}
	if _, err := Build(make([]byte, 16), make([]byte, 300), 0, protocol.Version); !errors.Is(err, ErrHeaderFieldTooLong) {
		t.Errorf("iv 长度 300 应返回 ErrHeaderFieldTooLong，实为 %v", err)
	}
}

func TestBuildAcceptsEmptyFields(t *testing.T) {
	// 边界：空 salt / 空 iv 在协议上不合法但格式上可表达，
	// HeaderLength 必须随之收缩，而不是固定写 51
	got, err := Build(nil, nil, 0, protocol.Version)
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	if len(got) != 19 {
		t.Fatalf("空 salt/iv 的头部长度应为 19，实为 %d", len(got))
	}
	if got[16] != 0 || got[17] != 0 {
		t.Errorf("SaltLen/IVLen 应均为 0，实为 %d/%d", got[16], got[17])
	}
}

func TestParseBytesRoundTrip(t *testing.T) {
	salt := mustHex(t, goldenHeaderSalt)
	iv := mustHex(t, goldenHeaderIV)

	built, err := Build(salt, iv, 0x00, protocol.Version)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ParseBytes(built)
	if err != nil {
		t.Fatalf("ParseBytes 失败: %v", err)
	}

	if h.Version != protocol.Version {
		t.Errorf("Version = %d，应为 %d", h.Version, protocol.Version)
	}
	if h.HeaderLength != uint32(len(built)) {
		t.Errorf("HeaderLength = %d，应为 %d", h.HeaderLength, len(built))
	}
	if !bytes.Equal(h.Salt, salt) {
		t.Errorf("Salt = %x，应为 %x", h.Salt, salt)
	}
	if !bytes.Equal(h.IV, iv) {
		t.Errorf("IV = %x，应为 %x", h.IV, iv)
	}
	if h.Flags != 0 {
		t.Errorf("Flags = %d，应为 0", h.Flags)
	}
	if h.CipherOffset() != int64(len(built)) {
		t.Errorf("CipherOffset = %d，应为 %d", h.CipherOffset(), len(built))
	}
}

// TestParseLeavesReaderAtCipherOffset 锁定一条容易被无声破坏的契约：
// Parse 读完后，流必须正好停在密文首字节。
//
// 解密管线依赖它接着同一个 reader 顺序读密文；若某天有人为了「健壮」
// 改成一次性读满 51 字节再切片，这条契约就会断，而表现是解密结果整体错位。
func TestParseLeavesReaderAtCipherOffset(t *testing.T) {
	built, err := Build(mustHex(t, goldenHeaderSalt), mustHex(t, goldenHeaderIV), 0, protocol.Version)
	if err != nil {
		t.Fatal(err)
	}

	ciphertext := []byte("这是密文占位，长度刻意不是 16 的倍数")
	r := bytes.NewReader(append(append([]byte(nil), built...), ciphertext...))

	h, err := Parse(r)
	if err != nil {
		t.Fatalf("Parse 失败: %v", err)
	}
	if int64(r.Len()) != int64(len(ciphertext)) {
		t.Fatalf("Parse 后剩余 %d 字节，应为密文长度 %d", r.Len(), len(ciphertext))
	}

	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rest, ciphertext) {
		t.Fatalf("Parse 后读到的不是密文\n got = %q\nwant = %q", rest, ciphertext)
	}
	if h.CipherOffset() != int64(len(built)) {
		t.Errorf("CipherOffset = %d，应为 %d", h.CipherOffset(), len(built))
	}
}

func TestParseMagicMismatch(t *testing.T) {
	bad := append([]byte("CPRISN\x00\x01"), mustHex(t, goldenHeader)[8:]...)
	if _, err := ParseBytes(bad); !errors.Is(err, ErrHeaderMagic) {
		t.Errorf("魔数不符应返回 ErrHeaderMagic，实为 %v", err)
	}
}

func TestParseShort(t *testing.T) {
	full := mustHex(t, goldenHeader)

	// 在每一个可能的截断点上都不许 panic，且必须报 ErrHeaderShort
	for n := range len(full) {
		if _, err := ParseBytes(full[:n]); !errors.Is(err, ErrHeaderShort) {
			t.Errorf("截断到 %d 字节应返回 ErrHeaderShort，实为 %v", n, err)
		}
	}
	if _, err := ParseBytes(nil); !errors.Is(err, ErrHeaderShort) {
		t.Errorf("空输入应返回 ErrHeaderShort，实为 %v", err)
	}
}

// TestParseDoesNotValidateVersionOrHeaderLength 记录一条**刻意保持的宽容**：
// 与 header.py:111-112 一致，Parse 不校验 Version 与 HeaderLength 的取值。
//
// 这不是疏漏：真正决定密文偏移的是 HeaderLength 字段本身，历史上若存在
// 非常规取值的文件，严格校验会让它们变成「打不开」而不是「照常读」。
func TestParseDoesNotValidateVersionOrHeaderLength(t *testing.T) {
	built, err := Build(mustHex(t, goldenHeaderSalt), mustHex(t, goldenHeaderIV), 0, 999)
	if err != nil {
		t.Fatal(err)
	}
	// 手工把 HeaderLength 改成一个与实际长度不符的值（127）
	built[14], built[15] = 0x00, 0x7F

	h, err := ParseBytes(built)
	if err != nil {
		t.Fatalf("Parse 应当宽容通过，实为 %v", err)
	}
	if h.Version != 999 {
		t.Errorf("Version = %d，应原样返回 999", h.Version)
	}
	if h.HeaderLength != 127 {
		t.Errorf("HeaderLength = %d，应原样返回文件里的 127", h.HeaderLength)
	}
}

func TestParsePassesThroughReaderErrors(t *testing.T) {
	// 非「读不满」类的错误必须原样透出，好让上层把它归到 502（后端故障）
	// 而不是 404（文件不对）
	sentinel := errors.New("后端连接被重置")
	if _, err := Parse(errReader{sentinel}); !errors.Is(err, sentinel) {
		t.Errorf("reader 错误应原样透出，实为 %v", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
