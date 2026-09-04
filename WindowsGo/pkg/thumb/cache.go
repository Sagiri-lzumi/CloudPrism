package thumb

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
)

// 磁盘缓存文件扩展名与 GCM 载荷布局（对照 thumbnail.py:105-115）：
// 文件 = nonce(12) + tag(16) + 密文；文件名 = <sha256hex>.cthumb。
const (
	cacheExt      = ".cthumb"
	nonceLen      = 12
	gcmTagLen     = 16
	defaultMaxMem = 200 // 内存 LRU 上限（对照 thumbnail.py:53）
)

// cacheSalt 磁盘缓存密钥派生用固定盐（密钥本体来自会话主密码，盐不敏感；
// 与 Python _CACHE_SALT = sha256("cloudprism-thumb-cache")[:16] 逐字节一致）。
var cacheSalt = func() []byte {
	sum := sha256.Sum256([]byte("cloudprism-thumb-cache"))
	return sum[:16]
}()

// Cache 是缩略图两级缓存（内存 LRU + 加密磁盘），与一次会话绑定。
//
// 锁库后应丢弃实例（密钥随会话销毁）；并发安全，可被多个列表请求共享。
// 磁盘读写失败一律静默（缓存是性能优化而非正确性依赖），对照
// thumbnail.py:108-131 的吞异常语义。
type Cache struct {
	sess *session.Session
	dir  string

	mu    sync.Mutex
	key   []byte // 惰性派生（PBKDF2 较重）
	mem   map[string][]byte
	order []string // LRU 顺序（队尾最新）
}

// NewCache 构造缓存；dir 为磁盘缓存目录（上层传 data/thumb-cache）。
func NewCache(sess *session.Session, dir string) *Cache {
	return &Cache{
		sess:  sess,
		dir:   dir,
		mem:   make(map[string][]byte, defaultMaxMem),
		order: make([]string, 0, defaultMaxMem),
	}
}

// KeyFor 缓存键：远端路径的 sha256 十六进制（对照 thumbnail.py:67-70）。
func KeyFor(remotePath string) string {
	sum := sha256.Sum256([]byte(remotePath))
	return hex.EncodeToString(sum[:])
}

// Get 命中返回缩略图明文（内存优先，其次磁盘解密）；未命中返回 nil。
func (c *Cache) Get(remotePath string) []byte {
	key := KeyFor(remotePath)

	c.mu.Lock()
	if data, ok := c.mem[key]; ok {
		c.touchLocked(key)
		c.mu.Unlock()
		return data
	}
	c.mu.Unlock()

	data := c.readDisk(key)
	if data != nil {
		c.putMem(key, data)
	}
	return data
}

// Put 写入两级缓存（磁盘为加密形态）；磁盘失败静默。
func (c *Cache) Put(remotePath string, data []byte) {
	key := KeyFor(remotePath)
	c.putMem(key, data)
	c.writeDisk(key, data)
}

// ---------------------------------------------------------------------------
// 内部实现
// ---------------------------------------------------------------------------

// touchLocked 把 key 移到 LRU 队尾（调用方须持有 mu）。
func (c *Cache) touchLocked(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
}

// putMem 写入内存 LRU 并裁剪到上限（对照 thumbnail.py:94-98）。
func (c *Cache) putMem(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.mem[key]; !exists {
		c.order = append(c.order, key)
	} else {
		c.touchLocked(key)
	}
	c.mem[key] = data
	for len(c.order) > defaultMaxMem {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.mem, oldest)
	}
}

// deriveKey 惰性派生 GCM 缓存密钥（对照 thumbnail.py:100-103）。
func (c *Cache) deriveKey() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != nil {
		return c.key, nil
	}
	raw, err := c.sess.DeriveKey(cacheSalt)
	if err != nil {
		return nil, err
	}
	c.key = make([]byte, protocol.KeyLen)
	copy(c.key, raw[:])
	return c.key, nil
}

// writeDisk 加密落盘；失败静默（内存缓存仍生效）。
func (c *Cache) writeDisk(key string, data []byte) {
	keyMaterial, err := c.deriveKey()
	if err != nil {
		return
	}
	aead, err := cryptox.NewGCM12(keyMaterial)
	if err != nil {
		return
	}
	// 每次加密取全新随机 nonce（12 字节标准长度）
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return
	}
	sealed := aead.Seal(nil, nonce, data, nil)
	blob := make([]byte, 0, nonceLen+gcmTagLen+len(data))
	blob = append(blob, nonce...)
	blob = append(blob, sealed...)

	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(c.dir, key+cacheExt), blob, 0o600)
}

// readDisk 读盘解密；损坏/篡改/缺失返回 nil（对照 thumbnail.py:119-131）。
func (c *Cache) readDisk(key string) []byte {
	blob, err := os.ReadFile(filepath.Join(c.dir, key+cacheExt))
	if err != nil {
		return nil
	}
	if len(blob) <= nonceLen+gcmTagLen {
		return nil
	}
	keyMaterial, err := c.deriveKey()
	if err != nil {
		return nil
	}
	aead, err := cryptox.NewGCM12(keyMaterial)
	if err != nil {
		return nil
	}
	plain, err := aead.Open(nil, blob[:nonceLen], blob[nonceLen:], nil)
	if err != nil {
		return nil // tag 校验失败：缓存损坏或密钥不匹配
	}
	return plain
}
