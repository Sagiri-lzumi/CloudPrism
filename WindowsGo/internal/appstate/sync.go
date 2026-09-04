package appstate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/settings"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/vault"
)

// SyncStatus 同步批次的状态（快照帧携带，前端据此渲染进度与结果）。
type SyncStatus struct {
	Running bool     `json:"running"`
	Total   int      `json:"total"`   // 本批应同步文件数
	Done    int      `json:"done"`    // 已处理（成功+失败）
	Current string   `json:"current"` // 正在同步的相对路径
	Synced  int      `json:"synced"`  // 成功上传
	Failed  int      `json:"failed"`  // 失败条目
	Errors  []string `json:"errors"`  // 失败明细（限前 50 条，防帧体积膨胀）
}

// syncState 兼容别名（state.go 字段类型名）。
type syncState = SyncStatus

// maxSyncErrors 错误明细条数上限。
const maxSyncErrors = 50

// ErrSyncDirUnset 未设置本地同步目录时返回。
var ErrSyncDirUnset = errors.New("请先在设置中指定本地同步目录")

// StartSync 启动本地目录 → 云端单向增量同步（对照 app.py _on_sync_folder）。
//
// 计划（目录扫描 + 三态对比）在调用方 goroutine 同步完成并返回本批任务数；
// 上传批次转后台 goroutine 执行，进度经状态帧携带。锁库时批次被取消。
// 已在同步中返回 ErrBusy；无待同步项时直接返回 0（不置 Running）。
func (s *State) StartSync(parent context.Context) (int, error) {
	conn, err := s.requireConn()
	if err != nil {
		return 0, err
	}
	localDir := trimSpace(s.cfg.Store.Get(settings.KeySyncLocalDir, ""))
	if localDir == "" {
		return 0, ErrSyncDirUnset
	}
	// 预检：目录被删除/不可读时给友好错误，而不是让 Plan 报裸系统路径
	if st, statErr := os.Stat(localDir); statErr != nil {
		return 0, fmt.Errorf("同步目录不可用：%s", localDir)
	} else if !st.IsDir() {
		return 0, fmt.Errorf("同步目录不是文件夹：%s", localDir)
	}

	s.mu.Lock()
	if s.syncRun.Running {
		s.mu.Unlock()
		return 0, ErrBusy
	}
	// 取消旧批次上下文（理论上 Running=false 时不存在，防御性清理）
	if s.syncCancel != nil {
		s.syncCancel()
		s.syncCancel = nil
	}
	s.mu.Unlock()

	// 计划：目录扫描与三态对比（本地 IO，可接受在绑定 goroutine 同步执行）
	plan, err := conn.engine.Plan(parent, localDir)
	if err != nil {
		return 0, err
	}
	total := plan.PendingCount()
	if total == 0 {
		return 0, nil // 无待同步项，不产生批次
	}

	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.syncCancel = cancel
	s.syncRun = SyncStatus{Running: true, Total: total}
	s.mu.Unlock()

	go s.runSyncBatch(ctx, conn, localDir, plan)
	return total, nil
}

// runSyncBatch 执行上传批次：逐项加密上传（目录自动创建），成功后更新
// 同步索引；被取消/锁库时中断并保留失败计数。状态变化写入 State 供帧读取。
func (s *State) runSyncBatch(ctx context.Context, conn *connState, localDir string, plan *vault.SyncPlan) {
	start := time.Now()
	var (
		success = map[string]vault.FileState{}
		status  SyncStatus
	)
	status.Running = true
	status.Total = plan.PendingCount()

	update := func() {
		s.mu.Lock()
		status.Done = status.Synced + status.Failed
		s.syncRun = status
		s.mu.Unlock()
	}

	for _, item := range append(append([]vault.SyncItem{}, plan.New...), plan.Changed...) {
		if ctx.Err() != nil {
			break // 锁库/取消：剩余条目保持未同步，下次批处理自然接管
		}
		status.Current = item.RelPath
		update()

		remote, rErr := conn.engine.RemotePath(item.RelPath)
		if rErr == nil {
			rErr = s.syncUploadOne(ctx, conn, item.LocalPath, remote)
		}
		if rErr != nil {
			status.Failed++
			if len(status.Errors) < maxSyncErrors {
				status.Errors = append(status.Errors, item.RelPath+": "+rErr.Error())
			}
			s.cfg.Log.Warn("同步失败", "rel", item.RelPath, "err", rErr)
			update()
			continue
		}
		success[item.RelPath] = vault.FileState{Size: item.Size, Mtime: item.Mtime}
		status.Synced++
		update()
	}

	// 合并上传成功条目并整体重写索引（索引损坏自愈语义在 engine 内）。
	// 取消/锁库（ctx.Err()!=nil）时不写回：会话已失效必然失败，且本批被
	// 打断，成功集不完整——留给下轮批处理以「已同步」状态续接
	if len(success) > 0 && ctx.Err() == nil {
		if err := conn.engine.UpdateIndex(ctx, success); err != nil {
			s.cfg.Log.Error("同步索引更新失败", "err", err)
		}
	}

	s.mu.Lock()
	if s.syncCancel != nil {
		s.syncCancel()
		s.syncCancel = nil
	}
	status.Running = false
	status.Current = ""
	s.syncRun = status
	s.mu.Unlock()

	s.cfg.Log.Info("同步批次结束",
		"synced", status.Synced, "failed", status.Failed,
		"elapsed", time.Since(start).Round(time.Millisecond).String())
}

// syncUploadOne 单个文件同步上传：加密到临时文件后走分块上传。
func (s *State) syncUploadOne(ctx context.Context, conn *connState, local, remote string) error {
	dir, ok := paths.TempDir(true)
	if !ok {
		return errors.New("临时目录不可用")
	}
	return s.uploadFileVia(ctx, conn, local, remote, dir, nil)
}
