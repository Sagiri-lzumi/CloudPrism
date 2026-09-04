package streaming

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// 令牌化解密代理的端点设计（WindowsPy 的裸路径 GET 在 WebView2 下的升级）：
//
//	/s/{token}/{display-name}  视频/音频流端点（Range + 流式解密）
//	/t/{token}                 缩略图端点（绑定层用 pkg/thumb 生成 JPEG 后注入）
//
// token 是 32 位十六进制随机值（16 字节 crypto/rand），仅本机代理可见，
// 同时解决三个 Python 版遗留问题：
//  1. 密文路径不暴露（URL 里没有 vault 内路径）；
//  2. display-name 提供明文展示名，代理据此推断 Content-Type
//     （Chromium <video> 强依赖 MIME，Python 恒发 octet-stream 会黑屏）；
//  3. 注册时（而非每个 Range 请求时）完成文件头/大小/密钥的一次性获取与
//     缓存 —— handler 零后端往返，播放器高频续请不再打存储后端。
//
// 令牌生命周期由注册方管理：播放结束可 Revoke；锁库必须 RevokeAll
// （届时 key 与入口一并清除，密钥不留存）。
type Kind uint8

const (
	// KindStream 表示媒体流端点（/s/）。
	KindStream Kind = iota
	// KindThumb 表示缩略图端点（/t/）。
	KindThumb
)

func (k Kind) String() string {
	if k == KindThumb {
		return "thumb"
	}
	return "stream"
}

// MaxEntries 是令牌注册表容量上限。正常使用（播放页 + 缩略图懒加载）远
// 达不到该值；触及上限说明调用方忘记 Revoke/RevokeAll，宁可拒绝新注册
// 也不无限膨胀（每个 stream 入口持有派生密钥，是敏感面）。
const MaxEntries = 4096

// ErrTooManyTokens 注册表容量耗尽时返回。
var ErrTooManyTokens = errors.New("streaming: 令牌注册表已满（存在未回收的令牌）")

// Entry 是注册表里的一条令牌记录。
//
// 所有字段在注册时填好、此后只读 —— handler 与并发请求共享同一 entry，
// 无写路径即无锁（Python 用 ProxyState._lock 保护 header/size 缓存，
// Go 把缓存收敛到 entry，锁只存在于注册表结构本身）。
type Entry struct {
	Kind        Kind
	Token       string                // 32 位十六进制
	RemotePath  string                // 后端相对路径（密文）
	DisplayName string                // 明文展示名（MIME 推断依据）
	Header      *cryptox.Header       // 已解析文件头（流端点）
	CipherSize  int64                 // 密文文件总字节
	Key         [protocol.KeyLen]byte // 派生密钥（流端点；Revoke 时清零）
	Payload     []byte                // 缩略图 JPEG（缩略图端点）
	ContentType string                // 缩略图 Content-Type
	Created     time.Time
}

// URLPath 返回本端点在代理上的相对 URL（不含 host 前缀）。
//
// display-name 参与路径只为可读性（复制链接/书签时知道内容是什么），
// 服务端按 token 找 entry，尾巴内容不参与任何判定，因此 URL 编码后
// 即便个别字符被客户端改写也不影响播放。
func (e *Entry) URLPath() string {
	if e.Kind == KindThumb {
		return "/t/" + e.Token
	}
	return "/s/" + e.Token + "/" + url.PathEscape(e.DisplayName)
}

// dispose 释放敏感内容：派生密钥清零（明文载荷留给 GC，与 Python 一致）。
// 调用方须持有注册表写锁或已把 entry 摘出。
func (e *Entry) dispose() {
	Scrub(e.Key[:])
}

// newToken 生成 32 位十六进制随机令牌。
func newToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Registry 是令牌注册表（并发安全）。
//
// 同 (kind, 远端路径) 幂等：重复注册返回既有 entry（前端重复调用不会
// 堆积）。注意幂等建立在「注册后文件内容不变」的假设上 —— 若远端文件
// 被重新上传/覆盖，需先 Revoke 旧令牌再注册（注册会重新拉取文件头）。
type Registry struct {
	mu      sync.RWMutex
	byToken map[string]*Entry
	byKey   map[string]string // "kind\x00path" → token（幂等判重）
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{
		byToken: make(map[string]*Entry),
		byKey:   make(map[string]string),
	}
}

// Add 注册新 entry 或返回同路径既有 entry。
//
// 幂等命中的 entry 与传入的 e 内容无关，调用方应直接使用返回值；
// 注册表已满时返回 ErrTooManyTokens。
func (r *Registry) Add(e *Entry) (*Entry, error) {
	key := tokenKey(e.Kind, e.RemotePath)
	r.mu.Lock()
	defer r.mu.Unlock()
	if tok, ok := r.byKey[key]; ok {
		// 幂等命中：丢弃新 entry（其 Key/Payload 已由调用方持有，
		// 此处不再引用，不构成泄漏）
		return r.byToken[tok], nil
	}
	if len(r.byToken) >= MaxEntries {
		return nil, ErrTooManyTokens
	}
	r.byToken[e.Token] = e
	r.byKey[key] = e.Token
	return e, nil
}

// Get 按令牌查 entry；不存在返回 nil。
func (r *Registry) Get(token string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byToken[token]
}

// find 按 (kind, path) 幂等查找；不存在返回 nil。
func (r *Registry) find(kind Kind, path string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tok, ok := r.byKey[tokenKey(kind, path)]
	if !ok {
		return nil
	}
	return r.byToken[tok]
}

// Revoke 吊销一个令牌（清除密钥与入口）。
func (r *Registry) Revoke(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byToken[token]
	if !ok {
		return
	}
	delete(r.byToken, token)
	delete(r.byKey, tokenKey(e.Kind, e.RemotePath))
	e.dispose()
}

// RevokeAll 清空注册表（锁库/切换后端时调用，密钥一并清除）。
func (r *Registry) RevokeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.byToken {
		e.dispose()
	}
	r.byToken = make(map[string]*Entry)
	r.byKey = make(map[string]string)
}

// Len 返回当前令牌数（测试与诊断用）。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byToken)
}

func tokenKey(k Kind, path string) string {
	return fmt.Sprintf("%d\x00%s", k, path)
}
