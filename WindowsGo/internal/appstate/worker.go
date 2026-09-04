package appstate

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/pipeline"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/transfer"
)

// 本文件是传输任务的执行器与编排层（对照 transfer_worker.py）：
//
//   - bindTaskCallbacks：装配队列终态回调（New 时调用一次）——任务终态
//     即同步续传记录与横幅计数；进度与聚合不进回调，由状态帧按 10Hz 汇总
//     （差异清单 #7，避免 Wails IPC 风暴）；
//   - makeRunner：按连接构造执行器（上传=加密→分块上传；下载=解密落盘），
//     Runner 在队列调度 goroutine 中执行；
//   - UploadPaths / DownloadFiles：本地/远端路径展开成任务集合。
//
// 进度口径：Runner 只 report 0.0~1.0，队列负责映射到 Task 的
// DoneBytes（Task.TotalBytes 在 prepare 阶段已预统计）。

// bindTaskCallbacks 装配队列终态回调（进程生命周期一次）。
func (s *State) bindTaskCallbacks() {
	q := s.cfg.Queue
	q.OnTaskFinished = func(_ *transfer.Task, _ bool) {
		s.persistPending() // 终态即落续传记录（锁库打断由 Lock 补拍）
		s.refreshResumeCount()
	}
}

// makeRunner 构造连接专属的任务执行器。conn 在任务入队前已锁定；
// 锁库 Clear 使在飞任务取消后不会再启动新任务，故闭包捕获安全。
func (s *State) makeRunner(conn *connState) transfer.Runner {
	return func(ctx context.Context, task *transfer.Task, report func(float64)) error {
		if task.Direction == transfer.DirDownload {
			return s.runDownloadTask(ctx, conn, task, report)
		}
		return s.runUploadTask(ctx, conn, task, report)
	}
}

// runDownloadTask 下载执行：整文件解密落盘（头解析/分片并行/进度在
// pipeline.Decryptor.DownloadAndDecrypt 内实现）。
func (s *State) runDownloadTask(ctx context.Context, conn *connState, task *transfer.Task, report func(float64)) error {
	opts := s.cfg.Queue.Options()
	dec := &pipeline.Decryptor{
		Sess:    conn.sess,
		Backend: conn.backend,
		Workers: opts.MaxWorkers,
	}
	return dec.DownloadAndDecrypt(ctx, task.RemotePath, task.LocalPath, report)
}

// runUploadTask 上传执行：加密到 data/tmp 临时文件，再分块上传远端。
func (s *State) runUploadTask(ctx context.Context, conn *connState, task *transfer.Task, report func(float64)) error {
	dir, ok := paths.TempDir(true)
	if !ok {
		dir = os.TempDir() // data/ 不可写时退回系统临时目录
	}
	return s.uploadFileVia(ctx, conn, task.LocalPath, task.RemotePath, dir, report)
}

// uploadFileVia 单文件上传管线（传输与文件夹同步共用）：
// 加密阶段无进度回调（本地加密显著快于上传，Python 端同样只在分块上传
// 阶段报进度），上传阶段转发分块进度。
func (s *State) uploadFileVia(ctx context.Context, conn *connState, local, remote, tmpDir string, report func(float64)) error {
	tmp, err := os.CreateTemp(tmpDir, "cloudprism-upload-*.cpenc")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 密文临时文件用完即删

	opts := s.cfg.Queue.Options()
	enc := &pipeline.Encryptor{
		Sess:    conn.sess,
		Workers: opts.MaxWorkers,
		Salt:    conn.meta.Salt, // 复用密库元信息盐，命中密钥缓存
	}
	if _, err := enc.EncryptFile(ctx, local, tmp, nil); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return conn.backend.UploadChunked(ctx, tmpName, remote, opts.Chunk, report)
}

// enqueueTasks 任务入队并落续传记录。入队前二次校验连接未变
// （UploadPaths 展开期间用户可能已锁库/切库，防止旧库任务挂到新连接上）。
func (s *State) enqueueTasks(conn *connState, tasks []*transfer.Task) error {
	if len(tasks) == 0 {
		return nil
	}
	s.mu.RLock()
	same := s.conn == conn
	s.mu.RUnlock()
	if !same {
		return ErrLocked
	}
	s.cfg.Queue.Enqueue(tasks)
	s.persistPending()
	s.refreshResumeCount()
	return nil
}

// UploadPaths 上传本地路径（文件或目录；目录递归展开）到远端目录。
//
// 展开在调用方 goroutine 同步完成（本地 walk，快）：目录先幂等 Mkdir，
// 随后构造任务整体入队。远端文件名按加密配置变换，目录结构保留明文
// 逻辑名（对照 app.py _upload_paths / _expand_dir_tasks）。
func (s *State) UploadPaths(ctx context.Context, localPaths []string, remoteDir string) error {
	conn, err := s.requireConn()
	if err != nil {
		return err
	}

	type pending struct{ local, rel string } // rel = 相对 remoteDir 的逻辑路径
	var files []pending
	var dirs []string
	for _, p := range localPaths {
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		if !st.IsDir() {
			files = append(files, pending{p, filepath.Base(p)})
			continue
		}
		root := filepath.Clean(p)
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, rErr := filepath.Rel(root, path)
			if rErr != nil {
				return rErr
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if path != root {
					dirs = append(dirs, rel)
				}
				return nil
			}
			files = append(files, pending{path, rel})
			return nil
		})
		if walkErr != nil {
			return walkErr
		}
	}

	// 远端目录先建（幂等；空目录不产生任务，与 Python 行为一致）
	if len(dirs) > 0 {
		opCtx, cancel := contextWithTimeout(ctx, opTimeout)
		defer cancel()
		for _, d := range dirs {
			enc, eErr := s.encryptRelPath(conn, d, false)
			if eErr != nil {
				return eErr
			}
			remote := joinRemote(trimSlash(remoteDir), enc)
			if mErr := conn.backend.Mkdir(opCtx, joinRemote(conn.vaultPath, remote)); mErr != nil {
				return mErr
			}
		}
	}

	tasks := make([]*transfer.Task, 0, len(files))
	for _, f := range files {
		enc, eErr := s.encryptRelPath(conn, f.rel, true) // 末段为文件名
		if eErr != nil {
			return eErr
		}
		// 与 Mkdir/下载一致：先拼 vaultPath 前缀（子库密库时上传目标在库内）
		remote := joinRemote(conn.vaultPath, joinRemote(trimSlash(remoteDir), enc))
		t := transfer.NewTask(f.local, remote, transfer.DirUpload)
		t.RemoteDir = trimSlash(remoteDir) // UI 目录 remote（根=空串）：前端刷新判定用
		t.DisplayName = filepath.Base(f.local)
		// 携带本地源快照：续传/锁库补拍后按 size+mtime 校验源未变（对照
		// Python _make_upload_task 的 expected_size/expected_mtime）
		if st, sErr := os.Stat(f.local); sErr == nil {
			size := st.Size()
			mtime := float64(st.ModTime().UnixNano()) / 1e9
			t.ExpectedSize = &size
			t.ExpectedMtime = &mtime
		}
		tasks = append(tasks, t)
	}
	return s.enqueueTasks(conn, tasks)
}

// encryptRelPath 加密相对路径的每一段（目录/文件名加密配置逐段应用）；
// lastIsFile 时末段追加 .cpenc 扩展名。
func (s *State) encryptRelPath(conn *connState, rel string, lastIsFile bool) (string, error) {
	segs := strings.Split(strings.ReplaceAll(rel, "\\", "/"), "/")
	out := make([]string, len(segs))
	for i, seg := range segs {
		enc, err := s.encryptName(conn, seg, lastIsFile && i == len(segs)-1)
		if err != nil {
			return "", err
		}
		out[i] = enc
	}
	return strings.Join(out, "/"), nil
}

// DownloadFiles 下载远端条目（文件或目录；目录递归展开，本地保留明文
// 目录结构）到本地目录。重名文件自动序号化。目录在本端逐层预建，
// 任务入队后由队列执行解密落盘。
func (s *State) DownloadFiles(ctx context.Context, entries []FileEntry, localDir string) error {
	conn, err := s.requireConn()
	if err != nil {
		return err
	}

	var (
		tasks []*transfer.Task
		walk  func(fe FileEntry, destDir string) error
	)
	walk = func(fe FileEntry, destDir string) error {
		if fe.IsDir {
			// 递归展开：目录名解密后作为本地目录名（可读的目录结构）
			sub, lErr := s.ListDir(ctx, fe.Remote)
			if lErr != nil {
				return lErr
			}
			for _, child := range sub {
				if wErr := walk(child, filepath.Join(destDir, fe.Display)); wErr != nil {
					return wErr
				}
			}
			return nil
		}
		dest := filepath.Join(destDir, fe.Display)
		if _, sErr := os.Stat(dest); sErr == nil {
			dest = uniqueLocalPath(dest) // 本端已存在同名 → 序号化
		}
		t := transfer.NewTask(dest, joinRemote(conn.vaultPath, trimSlash(fe.Remote)), transfer.DirDownload)
		t.DisplayName = fe.Display
		tasks = append(tasks, t)
		return nil
	}
	for _, fe := range entries {
		// 目录逐层预建，保证任务启动前父目录存在
		if wErr := walk(fe, localDir); wErr != nil {
			return wErr
		}
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		return err
	}
	return s.enqueueTasks(conn, tasks)
}

// Tasks 返回队列任务全量快照（供前端传输页渲染；绑定层无状态缓存）。
func (s *State) Tasks() []TaskView {
	raw := s.cfg.Queue.AllTasks()
	out := make([]TaskView, 0, len(raw))
	for _, t := range raw {
		snap := t.Snapshot() // 锁内拷贝，避免与进度更新竞争
		out = append(out, TaskView{
			ID:          snap.ID,
			DisplayName: snap.DisplayName,
			Direction:   snap.Direction,
			State:       snap.State,
			Progress:    snap.Progress,
			TotalBytes:  snap.TotalBytes,
			DoneBytes:   snap.DoneBytes,
			ErrorMsg:    snap.ErrorMsg,
			RemotePath:  snap.RemotePath,
			RemoteDir:   snap.RemoteDir,
			LocalPath:   snap.LocalPath,
		})
	}
	return out
}

// RetryTask 手动重试失败/取消的任务（按任务 ID 定位；已被回收的任务幂等忽略）。
func (s *State) RetryTask(id int64) error {
	q := s.cfg.Queue
	t := q.FindByID(id)
	if t == nil {
		return nil // 任务已被清除/回收：幂等忽略
	}
	q.Retry(t)
	return nil
}

// CancelAll 取消全部进行中/等待中的任务（运行中任务在分块边界停止）。
func (s *State) CancelAll() {
	s.cfg.Queue.CancelAll()
	s.persistPending()
	s.refreshResumeCount()
}

// ClearFinished 清空终态任务（done/failed/cancelled）的记录。
func (s *State) ClearFinished() {
	s.cfg.Queue.RemoveFinished()
	s.persistPending()
	s.refreshResumeCount()
}
