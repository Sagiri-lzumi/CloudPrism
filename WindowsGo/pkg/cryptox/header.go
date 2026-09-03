package cryptox

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// headerFixedLen 是文件头中除 Salt / IV 之外的固定字节数：
//
//	8(Magic) + 4(Version) + 4(HeaderLength) + 1(SaltLen) + 1(IVLen) + 1(Flags) = 19
//
// HeaderLength = headerFixedLen + len(Salt) + len(IV)，两端均为 16 时即 51。
const headerFixedLen = 19

var (
	// ErrHeaderMagic 表示魔数不匹配，目标不是 CloudPrism 加密文件。
	// 流式代理据此回 404（而非 502），因为这是「文件不对」而不是「后端坏了」。
	ErrHeaderMagic = errors.New("魔数不匹配，非 CloudPrism 加密文件")

	// ErrHeaderShort 表示文件头数据不足：文件被截断，或后端返回的头部不完整。
	ErrHeaderShort = errors.New("文件头数据不足")

	// ErrHeaderFieldTooLong 表示 salt / iv 长度超出 uint8 上限。
	// Python 侧此处由 struct.pack(">B", n) 抛 struct.error；
	// Go 若直接用 byte(n) 转换会静默截断，拼出一个 HeaderLength 与实际不符的头。
	ErrHeaderFieldTooLong = errors.New("文件头字段长度超出 uint8 上限")
)

// Header 是 .cpenc 文件头（全部大端序）：
//
//	偏移     长度  字段
//	0        8     Magic          "CPRISM\x00\x01"
//	8        4     Version        uint32 BE
//	12       4     HeaderLength   uint32 BE = 19 + SaltLen + IVLen
//	16       1     SaltLen        uint8
//	17       N     Salt           KDF 盐
//	17+N     1     IVLen          uint8
//	18+N     M     IV             AES-CTR 初始向量
//	18+N+M   1     Flags          保留，0x00
//
// 密文自 HeaderLength 起，明文长度 == 密文长度（CTR 无填充）。
//
// 对照 WindowsPy/src/cloudprism/crypto/header.py:3-13
type Header struct {
	Version      uint32
	HeaderLength uint32
	Salt         []byte
	IV           []byte
	Flags        byte
}

// CipherOffset 返回密文起始偏移（即 HeaderLength）。
//
// 单独给个方法是因为下游（Range 映射、代理、缩略图）全都要 int64，
// 到处写 int64(h.HeaderLength) 既啰嗦又容易在某一处漏掉转换。
func (h *Header) CipherOffset() int64 { return int64(h.HeaderLength) }

// Build 构建 .cpenc 文件头字节序列（全部大端序）。
//
// salt 与 iv 的长度必须 ≤ 255（协议规定恒为 16），超限返回 ErrHeaderFieldTooLong。
// flags 传 0x00、version 传 protocol.Version 即得到与 WindowsPy 端逐字节相同的头。
//
// 对照 WindowsPy/src/cloudprism/crypto/header.py:55-84
func Build(salt, iv []byte, flags byte, version uint32) ([]byte, error) {
	if len(salt) > 0xFF || len(iv) > 0xFF {
		return nil, fmt.Errorf("%w: salt=%d iv=%d", ErrHeaderFieldTooLong, len(salt), len(iv))
	}

	// 一次分配到位，不做 append 增长：头部长度在进函数时就已完全确定
	out := make([]byte, headerFixedLen+len(salt)+len(iv))
	copy(out[0:8], protocol.Magic)
	binary.BigEndian.PutUint32(out[8:12], version)
	binary.BigEndian.PutUint32(out[12:16], uint32(len(out)))
	out[16] = byte(len(salt))
	copy(out[17:], salt)

	ivPos := 17 + len(salt)
	out[ivPos] = byte(len(iv))
	copy(out[ivPos+1:], iv)
	out[len(out)-1] = flags

	return out, nil
}

// Parse 从可读流解析文件头。
//
// **读完后流正好停在密文首字节**，这是刻意保持的行为：解密管线可以接着同一个
// reader 顺序读密文，不需要 seek 也不需要额外缓冲。
//
// 与 Python 端一致，Parse **不校验** Version 与 HeaderLength 的取值
// （header.py:111-112 解包后直接返回）。保持宽容是为了兼容历史上可能存在的
// 非常规取值；真正决定密文偏移的是 HeaderLength 字段本身，由调用方使用。
//
// 对照 WindowsPy/src/cloudprism/crypto/header.py:86-122
func Parse(r io.Reader) (*Header, error) {
	h := &Header{}

	magic := make([]byte, protocol.MagicLen)
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, headerReadErr(err)
	}
	if string(magic) != protocol.Magic {
		return nil, ErrHeaderMagic
	}

	// 4(Version) + 4(HeaderLength) + 1(SaltLen) 一次读完，减少 reader 往返
	var fixed [9]byte
	if _, err := io.ReadFull(r, fixed[:]); err != nil {
		return nil, headerReadErr(err)
	}
	h.Version = binary.BigEndian.Uint32(fixed[0:4])
	h.HeaderLength = binary.BigEndian.Uint32(fixed[4:8])

	h.Salt = make([]byte, int(fixed[8]))
	if _, err := io.ReadFull(r, h.Salt); err != nil {
		return nil, headerReadErr(err)
	}

	var ivLen [1]byte
	if _, err := io.ReadFull(r, ivLen[:]); err != nil {
		return nil, headerReadErr(err)
	}
	h.IV = make([]byte, int(ivLen[0]))
	if _, err := io.ReadFull(r, h.IV); err != nil {
		return nil, headerReadErr(err)
	}

	var flags [1]byte
	if _, err := io.ReadFull(r, flags[:]); err != nil {
		return nil, headerReadErr(err)
	}
	h.Flags = flags[0]

	return h, nil
}

// ParseBytes 从字节序列解析文件头（便捷方法）。
//
// 对照 WindowsPy/src/cloudprism/crypto/header.py:124-127
func ParseBytes(data []byte) (*Header, error) {
	return Parse(bytes.NewReader(data))
}

// headerReadErr 把 io.ReadFull 的两种「读不满」错误折叠成 ErrHeaderShort。
//
// Python 侧的 _read 只比较实际长度与期望长度，不区分 EOF 与读到一半，
// 因此这里同样不做区分；非读取类错误（如网络后端故障）原样透出，
// 好让上层能把它归到 502 而不是 404。
func headerReadErr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrHeaderShort
	}
	return err
}
