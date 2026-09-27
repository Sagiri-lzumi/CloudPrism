package appstate

import (
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cache"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/settings"
)

// 本文件收敛「缓存根目录解析 + 分块缓存装配 + 缓存查询/清理」。
//
// 独立成文件的原因：缓存位置是本项目的**红线**（绝不写系统盘位置），
// 把所有与位置相关的判断集中在一处，便于审计与回归。

// CacheInfo 是缓存运行时信息（设置页展示用）。
type CacheInfo struct {
	Dir     string `json:"dir"`     // 实际生效的缓存根目录
	Scope   string `json:"scope"`   // 当前密库的作用域目录（未连接为空）
	ChunkMB int    `json:"chunkMb"` // 生效的分块大小（MB）
	LimitMB int    `json:"limitMb"` // 容量上限（MB）
	Bytes   int64  `json:"bytes"`   // 已占用字节
	Entries int    `json:"entries"` // 条目数
	Enabled bool   `json:"enabled"` // 分块缓存是否可用
}

// resolveCacheRoot 解析生效的缓存根目录。
//
// 取值顺序：用户配置（cache/path）→ 程序目录旁 data/cache。
// 用户配置若落在系统目录（%TEMP% / %APPDATA% / Program Files 等）则拒绝
// 并回退默认值——这是「缓存绝不写 C 盘系统位置」红线的技术落地。
//
// 返回的 root 恒为非空（最坏情况退回默认值），ok 表示该目录确实可用
// （已确保存在）。ok=false 时调用方应关闭缓存而不是另找位置。
func (s *State) resolveCacheRoot() (root string, ok bool) {
	root = s.cfg.Store.Get(settings.KeyCachePath, "")
	if root != "" {
		if why := paths.ForbiddenCacheDir(root); why != "" {
			s.logWarn("缓存目录被拒绝，回退程序目录旁默认位置", "path", root, "reason", why)
			root = ""
		}
	}
	if root == "" {
		root = paths.DefaultCacheDir()
	}
	dir, ok := paths.EnsureCacheDir(root)
	if !ok {
		return dir, false
	}
	return dir, true
}

// openMediaCache 按当前设置构造分块缓存；失败返回 nil。
//
// 返回 nil 是**合法且安全**的降级：读取链路会退回「直连远端」模式，
// 只是不缓存，功能与正确性完全不受影响。
func (s *State) openMediaCache(conn *connState) *cache.Store {
	root, ok := s.resolveCacheRoot()
	if !ok {
		s.logWarn("缓存目录不可用，本次连接已关闭大文件分块缓存", "path", root)
		return nil
	}
	// 作用域把后端、密库位置与密库 ID 一起纳入哈希：不同密库（哪怕同名、
	// 同路径重建）的缓存物理隔离，杜绝互相命中导致解密错乱。
	scope := cache.BuildScope(vaultDisplayName(conn.meta),
		conn.kind, conn.addr, conn.vaultPath, hex.EncodeToString(conn.meta.VaultID))

	cc, err := cache.Open(cache.Options{
		Root:    root,
		Scope:   scope,
		ChunkMB: s.cfg.Store.Int(settings.KeyCacheChunkMB, cache.DefaultChunkMB),
		LimitMB: s.cfg.Store.Int(settings.KeyCacheLimitMB, 512),
		Log:     s.cfg.Log,
	})
	if err != nil {
		s.logWarn("打开分块缓存失败，本次连接已关闭该功能", "err", err)
		return nil
	}
	return cc
}

// thumbCacheDir 返回缩略图加密缓存目录（<缓存根>/thumbs）。
//
// 与分块缓存共用同一个可配置根目录：设置页只需要一个「缓存目录」，
// 避免用户面对两个位置概念。
func (s *State) thumbCacheDir() string {
	if root, ok := s.resolveCacheRoot(); ok {
		return paths.ThumbCacheDir(root)
	}
	// 缓存在配置位置不可用时仍要能出缩略图：退回程序目录旁的 data/thumb-cache，
	// 依旧不碰系统临时目录（红线）。
	return filepath.Join(paths.DataDir(), "thumb-cache")
}

// CacheInfo 汇总缓存状态（设置页「缓存」分组展示）。
func (s *State) CacheInfo() CacheInfo {
	root, usable := s.resolveCacheRoot()
	info := CacheInfo{
		Dir:     root,
		ChunkMB: s.cfg.Store.Int(settings.KeyCacheChunkMB, cache.DefaultChunkMB),
		LimitMB: s.cfg.Store.Int(settings.KeyCacheLimitMB, 512),
		Enabled: usable,
	}
	if conn, err := s.requireConn(); err == nil && conn.mediaCache != nil {
		info.Enabled = true
		info.Scope = conn.mediaCache.ScopeDir()
		info.Bytes, info.Entries = conn.mediaCache.Stats()
		info.ChunkMB = int(conn.mediaCache.ChunkSize() >> 20)
	}
	return info
}

// invalidateCaches 丢弃若干远端路径上的派生状态：流注册（/s/ /t/ /d/ 的
// Entry）、媒体分块读缓存、缩略图缓存。
//
// 为什么必须由写路径显式失效 —— 三者都以远端路径为键、且都**没有内容校验**：
//   - 流 Entry 冻结了注册时刻的文件头（salt/iv）、派生密钥与密文大小，
//     覆盖后继续用旧 Entry 解密得到的是乱码（实测 4 字节覆盖后 /s/ 吐
//     `J1\xb6^`），Range 数学也失真；
//   - 分块读缓存以 (路径, 密文大小) 判命中，同尺寸覆盖两者都不变 ⇒ 继续吐
//     上一版内容；
//   - 缩略图缓存只以路径为键 ⇒ 显示上一版画面。
//
// 调用时机：上传成功、重命名（旧名 + 新名）、删除成功之后。
func invalidateCaches(conn *connState, remotes ...string) {
	if conn == nil {
		return
	}
	for _, r := range remotes {
		if r == "" {
			continue
		}
		if conn.proxy != nil {
			conn.proxy.RevokeRemote(r)
		}
		if conn.mediaCache != nil {
			conn.mediaCache.Invalidate(r)
		}
		if conn.cache != nil {
			conn.cache.Remove(r)
		}
	}
}

// PurgeCache 清空本地缓存（缩略图 + 媒体分块）。
//
// 已连接时清当前密库作用域；未连接时清理 media/ 下所有「带占用标记的」
// 作用域目录——标记校验确保只删本程序创建的缓存目录，绝不碰用户数据。
func (s *State) PurgeCache() error {
	conn, err := s.requireConn()
	if err == nil && conn.mediaCache != nil {
		if err := conn.mediaCache.Purge(); err != nil {
			return err
		}
		_ = os.RemoveAll(s.thumbCacheDir())
		return nil
	}
	root, ok := s.resolveCacheRoot()
	if !ok {
		return nil
	}
	_ = os.RemoveAll(paths.ThumbCacheDir(root))
	media := paths.MediaCacheDir(root)
	ents, err := os.ReadDir(media)
	if err != nil {
		return nil // 目录不存在即无缓存可清
	}
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		dir := filepath.Join(media, ent.Name())
		if _, err := os.Stat(filepath.Join(dir, ".meta", "scope.json")); err != nil {
			continue // 无占用标记：不是本程序创建的目录，跳过
		}
		_ = os.RemoveAll(dir)
	}
	return nil
}

// logWarn 带 nil 校验的告警日志（Config.Log 在测试中可能缺省）。
func (s *State) logWarn(msg string, args ...any) {
	if s.cfg.Log != nil {
		s.cfg.Log.Warn(msg, args...)
	}
}
