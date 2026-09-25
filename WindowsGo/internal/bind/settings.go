package bind

import (
	"errors"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cache"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/settings"
)

// Settings 是设置域的 Wails 绑定：读写外观/缓存/传输/同步/安全设置。
//
// 每次写入即落盘（Store.Sync），与 Python 端 QSettings 的「每次 set 即
// 持久化」对齐；需要即时生效的项（传输并发/自动锁）同步应用。
type Settings struct {
	st  *appstate.State
	ctx *ContextHolder
}

// NewSettings 构建设置域绑定。
func NewSettings(st *appstate.State, ctx *ContextHolder) *Settings {
	return &Settings{st: st, ctx: ctx}
}

// Get 聚合返回全部设置（页面初始化一次取齐；键名与 Python 版语义对齐）。
func (s *Settings) Get() map[string]any {
	store := s.st.Store()
	return map[string]any{
		"themeIndex":    store.Int(settings.KeyThemeIndex, 0),
		"fontSize":      store.Int(settings.KeyFontSize, 14),
		"cacheLimitMb":  store.Int(settings.KeyCacheLimitMB, 512),
		"cachePath":     store.Get(settings.KeyCachePath, ""),
		"chunkSizeMb":   store.Int(settings.KeyCacheChunkMB, cache.DefaultChunkMB),
		"chunkIndex":    store.Int(settings.KeyChunkIndex, appstate.DefaultChunkIndex),
		"concurrent":    store.Int(settings.KeyConcurrent, 2),
		"syncDir":       store.Get(settings.KeySyncLocalDir, ""),
		"maxCores":      store.Int(settings.KeyMaxCores, 0),
		"autoLockIndex": store.Int(settings.KeyAutoLockIndex, 0),
		"backendType":   store.Get(settings.KeyBackendType, "local"),
		"localDir":      store.Get(settings.KeyLocalDir, ""),
		"webdavUrl":     store.Get(settings.KeyWebDAVURL, ""),
		"webdavUser":    store.Get(settings.KeyWebDAVUser, ""),
	}
}

// SetTheme 主题档位：0=跟随系统 1=深色 2=浅色。
func (s *Settings) SetTheme(index int) error {
	return s.putInt(settings.KeyThemeIndex, index)
}

// SetFontSize 界面字号（px）。
func (s *Settings) SetFontSize(px int) error {
	return s.putInt(settings.KeyFontSize, px)
}

// SetCache 设置缓存上限（MB）与缓存根目录；path 空串 = 程序目录旁默认位置。
//
// 路径校验是本项目「缓存绝不写系统盘位置」红线的一道闸门：用户若把缓存
// 目录指到 %TEMP% / %APPDATA% / Program Files 等系统位置，这里直接拒绝
// 并返回可读原因，而不是等运行时悄悄写满系统盘。
func (s *Settings) SetCache(limitMB int, path string) error {
	if path != "" {
		if why := paths.ForbiddenCacheDir(path); why != "" {
			return Wrap(errors.New(why))
		}
	}
	store := s.st.Store()
	store.SetInt(settings.KeyCacheLimitMB, limitMB)
	store.Set(settings.KeyCachePath, path)
	return wrapSync(store.Sync())
}

// SetChunkSize 设置大文件分块读缓存的分块大小（MB）。
//
// 该值同时是「是否分块」的阈值（用户明确要求只保留一个旋钮）：小于它
// 整存为单独文件，大于等于它切成 原名-1 / 原名-2 … 收进原名子文件夹。
// 对**后续新建的缓存条目**生效；已有条目在下次访问时按布局不兼容重建。
func (s *Settings) SetChunkSize(mb int) error {
	if mb < cache.MinChunkMB {
		mb = cache.MinChunkMB
	}
	if mb > cache.MaxChunkMB {
		mb = cache.MaxChunkMB
	}
	return s.putInt(settings.KeyCacheChunkMB, mb)
}

// CacheInfo 返回缓存目录、占用与生效分块大小（设置页展示）。
func (s *Settings) CacheInfo() appstate.CacheInfo { return s.st.CacheInfo() }

// PurgeCache 清空本地缓存（缩略图 + 媒体分块），返回清空后的状态。
func (s *Settings) PurgeCache() (appstate.CacheInfo, error) {
	if err := s.st.PurgeCache(); err != nil {
		return appstate.CacheInfo{}, Wrap(err)
	}
	return s.st.CacheInfo(), nil
}

// SetTransfer 分卷尺寸档位与并发任务数，立即应用到后续任务。
//
// 档位同时决定「云端分卷尺寸」：超过它的文件在远端拆成多个对象，
// 因此改档位只影响后续写入（已有分卷照旧可读）。
func (s *Settings) SetTransfer(chunkIndex, concurrent int) error {
	store := s.st.Store()
	store.SetInt(settings.KeyChunkIndex, chunkIndex)
	store.SetInt(settings.KeyConcurrent, concurrent)
	if err := wrapSync(store.Sync()); err != nil {
		return err
	}
	s.st.ApplyTransferPrefs()
	return nil
}

// SetAutoLock 自动锁档位：0=从不，1/2/3 = 5/15/30 分钟，即时重启心跳。
func (s *Settings) SetAutoLock(index int) error {
	store := s.st.Store()
	store.SetInt(settings.KeyAutoLockIndex, index)
	if err := wrapSync(store.Sync()); err != nil {
		return err
	}
	s.st.ApplyAutoLockIndex(index)
	return nil
}

// SetMaxCores 单任务加密核心数；0 = 自动（按 CPU 数，上限 8），即时应用。
// 与 Python 设置页「性能」组语义对齐（Go 侧 0=auto 由队列回退 CPU 数）。
func (s *Settings) SetMaxCores(n int) error {
	store := s.st.Store()
	store.SetInt(settings.KeyMaxCores, n)
	if err := wrapSync(store.Sync()); err != nil {
		return err
	}
	s.st.ApplyTransferPrefs()
	return nil
}

// SetSyncDir 文件夹同步的本地目录（空串 = 清除设置）。
func (s *Settings) SetSyncDir(dir string) error {
	return s.putString(settings.KeySyncLocalDir, dir)
}

// SyncNow 触发一轮本地目录 → 云端增量同步，返回本批任务数。
// 批次在后台执行，进度经状态帧 Sync 字段推送（Snap 快照见 state.go）。
func (s *Settings) SyncNow() (int, error) {
	s.st.Activity()
	n, err := s.st.StartSync(s.ctx.Context())
	if err != nil {
		return 0, Wrap(err)
	}
	return n, nil
}

// putInt 写整数值并落盘。
func (s *Settings) putInt(key string, v int) error {
	store := s.st.Store()
	store.SetInt(key, v)
	return wrapSync(store.Sync())
}

// putString 写字符串值并落盘。
func (s *Settings) putString(key, v string) error {
	store := s.st.Store()
	store.Set(key, v)
	return wrapSync(store.Sync())
}

// wrapSync 设置落盘失败的统一错误包装（写盘失败属内部错误）。
func wrapSync(err error) error {
	if err != nil {
		return Wrap(err)
	}
	return nil
}
