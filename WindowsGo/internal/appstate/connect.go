package appstate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/settings"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/streaming"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/thumb"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/transfer"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/vault"
)

// 网络操作超时：绑定调用运行在后台 goroutine，后端僵死不能悬挂无限久。
const opTimeout = 60 * time.Second

// OpenRequest 是密库向导 / 快速连接 / 连接其他密库的统一入参。
//
// Kind=local 用 LocalDir；kind=webdav 用 URL/User/Pass；kind=baidu 只允许
// Create=false（百度密库由已授权记录定位，见 storage.Build 的错误分类）。
type OpenRequest struct {
	Kind           string `json:"kind"`
	LocalDir       string `json:"localDir"`
	URL            string `json:"url"`
	User           string `json:"user"`
	Pass           string `json:"pass"`
	MasterPassword string `json:"masterPassword"`
	// RecoveryCode 连接模式可空：忘记主密码时凭恢复码开库（向导页可见）
	RecoveryCode string `json:"recoveryCode"`
	FilenameEnc  bool   `json:"filenameEnc"`
	VaultName    string `json:"vaultName"`
	VaultPath    string `json:"vaultPath"` // 子目录密库位置（"" = 根）
	Create       bool   `json:"create"`
}

// OpenVaultResult 打开/新建结果。
type OpenVaultResult struct {
	// Code 新建成功时的恢复码（仅展示一次，不落盘）；连接成功为空。
	Code string `json:"code"`
}

// OpenVault 新建或连接密库并装载为当前连接（同步阻塞，绑定 goroutine 中执行）。
//
// 编排（对照 app.py 向导回调链）：构造后端 → 建/开库（PBKDF2 秒级派生，
// 阶段文案经 progress 回调发事件）→ 换入新连接 → 记住最近记录 → 续传探测。
// 已处于连接态时先锁旧连接，保证任意时刻至多一个连接上下文。
func (s *State) OpenVault(ctx context.Context, req OpenRequest) (OpenVaultResult, error) {
	prog := func(msg string) { s.emit(EventOpProgress, msg) }
	prog("正在连接存储位置…")

	backend, err := storage.Build(storage.Params{
		Kind:       req.Kind,
		LocalDir:   req.LocalDir,
		WebDAVURL:  req.URL,
		WebDAVUser: req.User,
		WebDAVPass: req.Pass,
	})
	if err != nil {
		return OpenVaultResult{}, err
	}

	// 真实网络往返都要兜超时：backend 实现接受 ctx，统一截断。
	opCtx, cancel := contextWithTimeout(ctx, opTimeout)
	defer cancel()

	mgr := vault.NewManager(backend)
	var (
		meta           *cryptox.VaultMetadata
		code           string
		masterPassword = req.MasterPassword
	)
	if req.Create {
		prog("正在生成密库（恢复码，密钥派生约需数秒）…")
		var err error
		meta, code, err = mgr.CreateWithRecovery(
			opCtx, req.MasterPassword, req.FilenameEnc,
			trimSpace(req.VaultName), req.VaultPath, prog,
		)
		if err != nil {
			return OpenVaultResult{}, err
		}
	} else if recovery := trimSpace(req.RecoveryCode); recovery != "" {
		// 忘记主密码：凭恢复码开库（还原的主密码仅驻内存，不落盘/不回传）
		prog("正在凭恢复码开库（约需数秒）…")
		var err error
		meta, masterPassword, err = mgr.OpenWithRecovery(opCtx, recovery, req.VaultPath, prog)
		if err != nil {
			return OpenVaultResult{}, err
		}
	} else {
		prog("正在校验主密码（密钥派生约需数秒）…")
		var err error
		meta, err = mgr.Open(opCtx, req.MasterPassword, req.VaultPath, prog)
		if err != nil {
			return OpenVaultResult{}, err
		}
	}

	sess := session.New(masterPassword)
	label, addr, kind := storage.Describe(backend)
	conn := &connState{
		sess:      sess,
		backend:   backend,
		meta:      meta,
		vaultPath: trimSlash(req.VaultPath),
		label:     label,
		addr:      addr,
		kind:      kind,
		connected: time.Now(),
	}
	if err := s.applyConnection(conn); err != nil {
		sess.Close()
		return OpenVaultResult{}, err
	}
	s.rememberCurrentVault(conn, req.User)
	prog("连接成功")
	return OpenVaultResult{Code: code}, nil
}

// applyConnection 装载连接上下文：先锁旧连接，再装配队列/代理/缓存/
// 同步引擎，最后探测续传记录。返回错误时连接不生效。
func (s *State) applyConnection(conn *connState) error {
	s.Lock() // 切换连接前先彻底清场（幂等：未连接时为空操作）

	// 传输队列绑定：注入密库元信息盐，上传加密复用命中密钥缓存
	s.cfg.Queue.Bind(conn.sess, conn.backend, conn.meta.Salt)
	s.cfg.Queue.Runner = s.makeRunner(conn)
	s.applyTransferPrefsLocked()

	// 流式解密代理：127.0.0.1 动态端口，仅本机可访问。
	// 启动失败只记日志不阻断连接（视频播放降级为导出后用系统播放器）。
	if err := s.startProxy(conn); err != nil {
		s.cfg.Log.Warn("流式代理启动失败", "err", err)
	}

	// 缩略图缓存：加密磁盘缓存与会话绑定，锁库时一并丢弃
	dir := filepath.Join(paths.DataDir(), "thumb-cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.cfg.Log.Warn("缩略图缓存目录不可用", "err", err)
	}
	conn.cache = thumb.NewCache(conn.sess, dir)
	conn.engine = vault.NewSyncEngine(
		conn.sess, conn.backend, conn.vaultPath,
		conn.meta.FilenameEnc, conn.meta.Salt,
	)

	s.mu.Lock()
	s.conn = conn
	s.resumeCount = len(s.pendingRecords())
	s.mu.Unlock()

	// 自动锁按设置重启（新连接重新计时）
	s.cfg.Log.Info("密库已连接",
		"backend", conn.kind, "vaultPath", conn.vaultPath,
		"filenameEnc", conn.meta.FilenameEnc)
	return nil
}

// startProxy 在 127.0.0.1 动态端口启动流式解密代理。
func (s *State) startProxy(conn *connState) error {
	proxy := streaming.NewServer(conn.sess, conn.backend)
	addr, err := proxy.Start("127.0.0.1", 0)
	if err != nil {
		return fmt.Errorf("streaming: %w", err)
	}
	_ = addr // 动态端口经 BaseURL() 取用
	conn.proxy = proxy
	return nil
}

// rememberCurrentVault 把成功连接写入最近密库记录（仅连接参数，密码不落盘）。
// 对照 app.py _remember_current_vault。
func (s *State) rememberCurrentVault(conn *connState, webdavUser string) {
	rec := map[string]any{
		"backend_type": conn.kind,
		"label":        conn.label,
		"path":         conn.addr,
		"vault_name":   vaultDisplayName(conn.meta),
		"vault_path":   conn.vaultPath,
	}
	if conn.kind == "webdav" {
		rec["webdav_user"] = webdavUser // 仅账号；密码绝不落盘
	}
	s.cfg.Store.RememberVault(rec)
	s.cfg.Store.Sync()
}

// Lock 锁定密库：清会话与后端引用，中断传输，重置界面所需状态。
// 对照 app.py _lock_vault。幂等（未连接时为空操作）。
func (s *State) Lock() {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	// 统计全量复位（作废在飞结果 + 清掉旧数字，切库不带上一库的占用）
	s.statsSeq++
	s.statsRunning = false
	s.statsDone = false
	s.statsTotal = 0
	s.statsFiles = 0
	s.statsFailed = false
	s.syncRun = syncState{} // 同步状态复位
	if s.syncCancel != nil {
		s.syncCancel() // 中断在飞同步批次（goroutine 内部检查 ctx）
		s.syncCancel = nil
	}
	s.autoLockMin = 0
	s.resumeCount = 0
	s.mu.Unlock()

	// 停止自动锁心跳
	s.stopAutoLock()

	if conn == nil {
		return
	}

	// 先持久化未完成任务（续传记录），再清空队列：
	// 任务完成/失败回调只在运行期写记录，锁库打断的任务必须在此补拍。
	s.persistPending()
	s.cfg.Queue.Clear()

	if conn.proxy != nil {
		_ = conn.proxy.Close()
	}
	conn.sess.Close() // 清零主密码与派生密钥

	s.cfg.Log.Info("密库已锁定")
	s.emit(EventLocked, nil)
}

// ---------------------------------------------------------------------------
// 最近密库记录（引导页/密库信息页）
// ---------------------------------------------------------------------------

// RecentVaults 返回最近密库记录列表（最新在前）。
func (s *State) RecentVaults() []map[string]any {
	return s.cfg.Store.JSONList(settings.KeyRecentVaults)
}

// ForgetRecent 移除一条最近密库记录（仅删记录，不影响云端数据）。
func (s *State) ForgetRecent(key string) error {
	s.cfg.Store.ForgetVault(key)
	return s.cfg.Store.Sync()
}

// ListOtherVaults 扫描当前后端上除已连接密库外的其它密库位置。
// 对照 app.py _update_vault_info_page 的 other vaults 段。
func (s *State) ListOtherVaults(ctx context.Context) ([]string, error) {
	conn, err := s.requireConn()
	if err != nil {
		return nil, err
	}
	opCtx, cancel := contextWithTimeout(ctx, opTimeout)
	defer cancel()
	vaults, err := vault.NewManager(conn.backend).ListVaults(opCtx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(vaults))
	for _, v := range vaults {
		if v.Path != conn.vaultPath {
			out = append(out, v.Path) // "" = 根目录密库
		}
	}
	return out, nil
}

// ConnectOtherVault 连接当前后端上的另一个密库（复用后端，仅需目标库主密码）。
// 对照 app.py _on_connect_other_vault。
func (s *State) ConnectOtherVault(ctx context.Context, vaultPath, masterPassword string) error {
	conn, err := s.requireConn()
	if err != nil {
		return err
	}
	backend := conn.backend
	opCtx, cancel := contextWithTimeout(ctx, opTimeout)
	defer cancel()

	mgr := vault.NewManager(backend)
	meta, err := mgr.Open(opCtx, masterPassword, vaultPath, func(msg string) {
		s.emit(EventOpProgress, msg)
	})
	if err != nil {
		return err
	}
	sess := session.New(masterPassword)
	label, addr, kind := storage.Describe(backend)
	newConn := &connState{
		sess: sess, backend: backend, meta: meta,
		vaultPath: trimSlash(vaultPath), label: label, addr: addr,
		kind: kind, connected: time.Now(),
	}
	if err := s.applyConnection(newConn); err != nil {
		sess.Close()
		return err
	}
	// 复用既有记录的账号字段补记
	user := s.webdavUserOf(backend)
	s.rememberCurrentVault(newConn, user)
	return nil
}

// webdavUserOf 从最近记录取 WebDAV 账号（改连其他密库时保留账号栏位）。
func (s *State) webdavUserOf(backend storage.Backend) string {
	if _, _, kind := storage.Describe(backend); kind != "webdav" {
		return ""
	}
	for _, r := range s.RecentVaults() {
		if str, _ := r["webdav_user"].(string); str != "" {
			return str
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// 续传记录
// ---------------------------------------------------------------------------

// pendingRecords 读未完成传输记录（锁内调用方保证不并发）。
func (s *State) pendingRecords() []map[string]any {
	return s.cfg.Store.JSONList(settings.KeyPendingTransfers)
}

// persistPending 把队列未完成任务快照写为续传记录（重启/锁库后续传用）。
// 对照 app.py _sync_pending_records；仅路径与快照，无任何秘密。
//
// 持 s.mu 保证读-写原子：终态回调可能从多个 goroutine 并发进入（并发
// 任务各自收尾，见 queue.finish），无锁时读队列-写存储会被交错，丢记录
// 意味着用户下次连接看不到可续传任务。锁序 s.mu→queue.mu，与快照/锁库
// 路径一致，无反向加锁。
func (s *State) persistPending() {
	s.mu.Lock()
	defer s.mu.Unlock()

	var records []map[string]any
	for _, t := range s.cfg.Queue.UnfinishedTasks() {
		rec := map[string]any{
			"local":     t.LocalPath,
			"remote":    t.RemotePath,
			"name":      t.DisplayName,
			"direction": t.Direction,
		}
		if t.ExpectedSize != nil {
			rec["size"] = *t.ExpectedSize
		}
		if t.ExpectedMtime != nil {
			rec["mtime"] = *t.ExpectedMtime
		}
		records = append(records, rec)
	}
	s.cfg.Store.SetJSONList(settings.KeyPendingTransfers, records)
}

// ResumePending 把续传记录重建为任务重新入队（横幅按钮）。
// 对照 app.py _resume_pending_transfers。
func (s *State) ResumePending() (int, error) {
	conn, err := s.requireConn()
	if err != nil {
		return 0, err
	}
	records := s.pendingRecords()
	if len(records) == 0 {
		return 0, ErrNoResume
	}

	var tasks []*transfer.Task
	for _, rec := range records {
		local := strOf(rec["local"])
		remote := strOf(rec["remote"])
		if local == "" || remote == "" {
			continue
		}
		direction := strOf(rec["direction"])
		if direction == "" {
			direction = transfer.DirUpload
		}
		if direction == transfer.DirUpload {
			if st, err := os.Stat(local); err != nil || !st.Mode().IsRegular() {
				continue // 本地源文件已不存在，无法续传（静默跳过）
			}
		}
		name := strOf(rec["name"])
		if name == "" {
			name = filepath.Base(local)
		}
		task := transfer.NewTask(local, remote, direction)
		task.DisplayName = name
		if direction == transfer.DirUpload {
			if v, ok := rec["size"].(float64); ok {
				size := int64(v)
				mtime := 0.0
				if f, ok := rec["mtime"].(float64); ok {
					mtime = f
				}
				task.ExpectedSize = &size
				task.ExpectedMtime = &mtime
			}
		}
		tasks = append(tasks, task)
	}
	// 无论是否全部有效都清空横幅：无效记录随本次重写清理
	s.cfg.Store.SetJSONList(settings.KeyPendingTransfers, nil)
	s.cfg.Store.Sync()

	if len(tasks) > 0 {
		s.enqueueTasks(conn, tasks)
	}
	s.refreshResumeCount()
	return len(tasks), nil
}

// refreshResumeCount 重算续传横幅计数（入队/清除后调用）。
func (s *State) refreshResumeCount() {
	s.mu.Lock()
	s.resumeCount = len(s.pendingRecords())
	s.mu.Unlock()
}

// strOf map 值安全取字符串（数字/缺失一律空串，对齐 Python rec.get 语义）。
func strOf(v any) string {
	if str, ok := v.(string); ok {
		return str
	}
	return ""
}

// trimSlash 去除首尾斜杠（路径归一，Python strip("/") 对等）。
func trimSlash(p string) string {
	for len(p) > 0 && (p[0] == '/' || p[0] == '\\') {
		p = p[1:]
	}
	for len(p) > 0 && (p[len(p)-1] == '/' || p[len(p)-1] == '\\') {
		p = p[:len(p)-1]
	}
	return p
}

// ---------------------------------------------------------------------------
// 传输偏好下发（设置页保存后调用）
// ---------------------------------------------------------------------------

// chunkBytesByIndex 分块大小索引 → 字节（对照 settings 键注释）。
var chunkBytesByIndex = [...]int{256 << 10, 512 << 10, 1 << 20, 4 << 20}

// ApplyTransferPrefs 把设置里的传输参数下发到队列：分块大小、单任务
// 加密核数、并发任务数。设置页保存后由绑定层调用（连接态外调用无害，
// 队列常驻）。
func (s *State) ApplyTransferPrefs() {
	s.applyTransferPrefsLocked()
}

// applyTransferPrefsLocked 实现见上；连接装配内部也调用一次。
func (s *State) applyTransferPrefsLocked() {
	store := s.cfg.Store
	q := s.cfg.Queue

	idx := store.Int(settings.KeyChunkIndex, 0)
	if idx < 0 || idx >= len(chunkBytesByIndex) {
		idx = 0
	}
	// 加密核数：0/未设置时按 CPU 数，上限 8（避免小核数机器过载）
	workers := store.Int(settings.KeyMaxCores, 0)
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > 8 {
		workers = 8
	}
	q.SetTransferOptions(chunkBytesByIndex[idx], workers)
	q.SetMaxConcurrent(store.Int(settings.KeyConcurrent, 2))
}
