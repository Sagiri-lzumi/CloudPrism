// Package secret 提供「单个秘密值」的加密落盘读写。
//
// 为什么单独一个包而不是塞进 pkg/settings：设置存储顶部的约定明确写着
// 「密码/token 等秘密一律不入本存储（Python 端同约束）」。访问令牌属于秘密，
// 必须走独立的加密通道。
//
// 磁盘格式与 pkg/storage.BaiduCredStore 保持同一约定（Python 端已熟悉的写法）：
//
//	"DPAPI:" + base64(protect(utf8(value)))   —— DPAPI 加密（Windows）
//	"PLAIN:" + base64(utf8(value))            —— 明文兜底（非 Windows/测试）
//
// 注：`PLAIN:` 只在**构造时显式传入 nil 保护器**（非 Windows / 测试）时写出；
// 注入的加密器运行期失败不会降级成 PLAIN，而是直接报错（见 Save）。
//
// 加解密实现由装配层注入（DPAPI 在 internal/platform/win，而 pkg/* 不允许
// import internal/*），因此这里只声明 Protector 接口。
package secret

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Protector 抽象秘密加解密实现；internal/platform/win 的 DPAPI 适配器在
// 装配层注入。Scheme 返回落盘前缀（"DPAPI"/"PLAIN"）。
type Protector interface {
	Protect(data []byte) ([]byte, error)
	Unprotect(data []byte) ([]byte, error)
	Scheme() string
}

// plainProtector 是明文兜底：Protect/Unprotect 恒等，前缀写 PLAIN。
// 非 Windows 环境（或测试）用它，保证功能可用而不是直接失败。
type plainProtector struct{}

func (plainProtector) Protect(d []byte) ([]byte, error)   { return d, nil }
func (plainProtector) Unprotect(d []byte) ([]byte, error) { return d, nil }
func (plainProtector) Scheme() string                     { return "PLAIN" }

// File 是「一个文件装一个秘密值」的读写器。
type File struct {
	path string
	prot Protector
}

// NewFile 构造秘密文件读写器；prot 为 nil 时退化为 PLAIN 明文兜底。
func NewFile(path string, prot Protector) *File {
	if prot == nil {
		prot = plainProtector{}
	}
	return &File{path: path, prot: prot}
}

// Path 返回底层文件路径。
func (f *File) Path() string { return f.path }

// Exists 报告秘密文件是否存在（不判断内容能否解开）。
func (f *File) Exists() bool {
	_, err := os.Stat(f.path)
	return err == nil
}

// Save 加密写入秘密值。
//
// **加密器失败一律向上报错，不做明文降级。** 早期实现（对齐 BaiduCredStore
// 的兜底语义）在 DPAPI 失败时静默写 `PLAIN:<base64>`：结果是局域网访问
// 令牌、百度密钥被以明文落在磁盘上，而用户界面上一切正常 ——「失败的加密」
// 被伪装成成功，这比功能不可用严重得多。现在失败即返回错误、不落任何文件，
// 由装配层把失败如实展示出来（可用性让位于「不留明文秘密」）。
//
// prot 显式为 nil 时 NewFile 装入的 plainProtector 不受影响：那是**显式
// 选择**的明文存储（非 Windows / 测试），Protect 不会返回错误。
func (f *File) Save(value string) error {
	payload, err := f.prot.Protect([]byte(value))
	if err != nil {
		return fmt.Errorf("secret: 加密秘密值失败，已放弃写入（不降级为明文）: %w", err)
	}
	blob := f.prot.Scheme() + ":" + base64.StdEncoding.EncodeToString(payload)

	if dir := filepath.Dir(f.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(f.path, []byte(blob), 0o600)
}

// Load 读取并解密秘密值。文件不存在 / 前缀未知 / base64 损坏 / 解不开，
// 一律返回 ("", false)，由调用方按「无秘密」处理（不把坏文件当致命错误）。
func (f *File) Load() (string, bool) {
	raw, err := os.ReadFile(f.path)
	if err != nil {
		return "", false
	}
	blob := string(raw)
	switch {
	case len(blob) > 6 && blob[:6] == "DPAPI:":
		if f.prot.Scheme() != "DPAPI" {
			return "", false // 本进程无 DPAPI 能力，解不开
		}
		ct, err := base64.StdEncoding.DecodeString(blob[6:])
		if err != nil {
			return "", false
		}
		pt, err := f.prot.Unprotect(ct)
		if err != nil {
			return "", false
		}
		return string(pt), true
	case len(blob) > 6 && blob[:6] == "PLAIN:":
		pt, err := base64.StdEncoding.DecodeString(blob[6:])
		if err != nil {
			return "", false
		}
		return string(pt), true
	default:
		return "", false // 未知前缀：不是本程序写的文件
	}
}

// Clear 删除秘密文件；不存在时容忍。
func (f *File) Clear() {
	if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = err // 与 Python 端一致：清理失败不向上报错
	}
}

// tokenBytes 是访问令牌的随机字节数（16 字节 = 128 位，hex 后 32 字符）。
const tokenBytes = 16

// NewToken 生成一个新的随机访问令牌（32 位小写 hex）。
//
// 用 crypto/rand 而非 math/rand：令牌是局域网访问的唯一凭据，
// 可预测的随机数等于没有鉴权。
func NewToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
