// Package session 提供主密码会话与密钥派生缓存。
//
// 对照 WindowsPy/src/cloudprism/core/session.py。Session 持有用户主密码
// （仅存内存，不落盘、不上传）与按 salt 缓存的派生密钥：同一会话内对
// 相同 salt 的派生结果只做一次 PBKDF2（200000 次迭代较慢，缓存可显著
// 缩短同一密库上的重复开库/加解密路径）。
//
// 与 Python 端的差异及理由：
//   - Python 用 bytearray 保存密码以便原地清零；Go 的 string 不可变、
//     无法清除，故密码一律拷贝进私有 []byte，关闭会话时用内建 clear()
//     置零后再置 nil（防止误用残留）。
//   - Python 端会话关闭后 derive_key 仍会照常执行（对空密码做 PBKDF2，
//     产物必然打不开任何密库）；Go 端关闭后 DeriveKey 返回 ErrClosed
//     ——「直接报错」比「静默产出废钥」安全，属安全向的必要偏离。
package session

import (
	"errors"
	"sync"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// ErrClosed 表示会话已关闭，不能继续派生密钥（见包注释）。
var ErrClosed = errors.New("会话已关闭，主密码已清零")

// Session 是主密码会话：持有主密码并按 salt 派生密钥，结果缓存在内存中。
// 所有方法并发安全（流式代理与同步引擎可能同时派生）。
type Session struct {
	mu       sync.Mutex
	pw       []byte // 主密码 UTF-8 字节；Close 后置 nil
	keyCache map[string][protocol.KeyLen]byte
}

// New 建立主密码会话。密码立即拷入私有缓冲，调用方传入的字符串
// 由调用方自行处理（Go 无法保证 string 的清除）。
func New(masterPassword string) *Session {
	return &Session{
		pw:       []byte(masterPassword),
		keyCache: make(map[string][protocol.KeyLen]byte),
	}
}

// MasterPassword 返回主密码的 UTF-8 字符串形式（Python 端同名属性语义）。
func (s *Session) MasterPassword() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.pw)
}

// DeriveKey 按 salt 派生 32 字节密钥并缓存；同一 salt 只派生一次。
// 会话已关闭时返回 ErrClosed。
func (s *Session) DeriveKey(salt []byte) ([protocol.KeyLen]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pw == nil {
		return [protocol.KeyLen]byte{}, ErrClosed
	}
	key := string(salt)
	if k, ok := s.keyCache[key]; ok {
		return k, nil
	}
	k := cryptox.DeriveKey(string(s.pw), salt)
	s.keyCache[key] = k
	return k, nil
}

// ClearCache 清零并清空密钥缓存（自动锁库等场景清除派生密钥残留）。
func (s *Session) ClearCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keyCache {
		zeroize(k[:])
	}
	clear(s.keyCache)
}

// Close 清零密码与缓存密钥并关闭会话（退出会话时必须调用）。
// 关闭后不可再派生密钥，需要重新 New。
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keyCache {
		zeroize(k[:])
	}
	clear(s.keyCache)
	zeroize(s.pw)
	s.pw = nil
}

// zeroize 原地清零字节切片。使用内建 clear：pw/key 均为堆上分配的
// []byte（或逃逸的数组切片），置零写有可观察副作用，不会被编译器
// 当作死存储消除。
func zeroize(b []byte) {
	clear(b)
}
