package bind

import (
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
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
		"chunkIndex":    store.Int(settings.KeyChunkIndex, 1),
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

// SetCache 设置缩略图缓存上限（MB）与目录；path 空串 = 默认临时目录。
func (s *Settings) SetCache(limitMB int, path string) error {
	store := s.st.Store()
	store.SetInt(settings.KeyCacheLimitMB, limitMB)
	store.Set(settings.KeyCachePath, path)
	return wrapSync(store.Sync())
}

// SetTransfer 分块档位与并发任务数，立即应用到后续任务。
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

// ChooseSyncDir 弹目录选择框返回同步目录；取消返回空串（非错误）。
func (s *Settings) ChooseSyncDir() (string, error) {
	dir, err := wruntime.OpenDirectoryDialog(s.ctx.Context(), wruntime.OpenDialogOptions{
		Title:                "选择要同步的本地目录",
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", Wrap(err)
	}
	return dir, nil
}

// ChooseCacheDir 弹目录选择框返回缩略图缓存目录；取消返回空串（非错误）。
// 与 ChooseSyncDir 的「同步目录」语义区分，避免设置页误用。
func (s *Settings) ChooseCacheDir() (string, error) {
	dir, err := wruntime.OpenDirectoryDialog(s.ctx.Context(), wruntime.OpenDialogOptions{
		Title:                "选择缩略图缓存目录",
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", Wrap(err)
	}
	return dir, nil
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
