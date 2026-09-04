// Package appstate 是应用唯一有状态对象，取代 WindowsPy 的 AppController。
//
// 职责边界（见 docs/ARCHITECTURE.md 分层表）：
//   - 允许依赖 pkg/* 与 internal/*，禁止依赖 Wails 绑定层 —— UI 事件一律
//     经注入的 Emit 函数出口（bind/events 合帧器在装配时注入）；
//   - 连接 / 锁库 / 文件浏览 / 传输编排 / 同步 / 统计 / 自动锁全部收口于此，
//     internal/bind 各域只做薄适配，不写业务逻辑。
//
// 线程模型：所有公开方法并发安全（内部互斥锁）；PBKDF2 派生的秒级耗时
// 阶段在调用方 goroutine 内同步执行 —— Wails 绑定调用运行在独立 goroutine，
// 不会冻结 UI（Python 端 run_busy 的背景线程需求在 Go 侧天然消失）。
package appstate

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/settings"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/streaming"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/thumb"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/transfer"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/vault"
)

// 状态错误哨兵：绑定层据此映射错误码（见 internal/bind/apierr.go）。
var (
	// ErrLocked 需要连接才能执行的操作在未连接/已锁定状态被调用。
	ErrLocked = errors.New("密库已锁定，请先连接密库")
	// ErrBusy 需要独占的操作被并发重复触发（如同一轮统计/同步进行中）。
	ErrBusy = errors.New("上一轮操作尚未完成，请稍候")
	// ErrNoResume 没有可续传的未完成任务。
	ErrNoResume = errors.New("没有可续传的任务")
)

// Config 是 State 的装配依赖（main.go 提供；测试注入替身）。
type Config struct {
	Store *settings.Store
	Queue *transfer.Queue
	Log   *slog.Logger
	// Emit 事件出口（nil 时静默丢弃），New 后可用 SetEmit 替换。
	// 名称与载荷见各文件中的 emitXxx 注释；10Hz 合帧由 bind/events
	// 决定发不发、何时发，本层只保证「状态变化即调用」。
	Emit func(name string, data any)
}

// connState 一次成功连接的全部上下文（nil 表示未连接）。
type connState struct {
	sess      *session.Session
	backend   storage.Backend
	meta      *cryptox.VaultMetadata
	vaultPath string // 子目录密库根前缀（"" = 后端根目录）
	label     string // 后端显示名（本地文件夹 / WebDAV / 百度网盘）
	addr      string // 后端地址/路径
	kind      string // local / webdav / baidu
	connected time.Time

	engine *vault.SyncEngine // 文件夹同步引擎（随连接创建）

	cache *thumb.Cache // 缩略图加密缓存（会话绑定，锁库时丢弃）

	proxy *streaming.Server // 流式解密代理（127.0.0.1 动态端口）

	// transfer 状态回调装配由 connect 完成：任务完成/失败时驱动
	// 续传记录持久化（见 worker.go 的回调接线）。
}

// State 是应用唯一有状态对象。
type State struct {
	cfg Config

	mu   sync.RWMutex
	conn *connState // nil = 未连接

	// emitFn 事件出口（cfg.Emit 的初始值；SetEmit 在装配期替换为
	// bind/events 的 Forward）。只在启动连接前设置，无并发窗口不加锁。
	emitFn func(name string, data any)

	// 续传横幅：连接时由 pending 记录探测，>0 表示可提供续传入口。
	resumeCount int

	// 云端占用统计（stats.go）：seq 竞态防护，最新一轮结果才回填。
	statsSeq      int64
	statsRunning  bool
	statsDone     bool // 最新一轮是否已产出结果
	statsTotal    int64
	statsFiles    int64
	statsFailed   bool
	statsFinished time.Time

	// 同步批次状态（sync.go）。
	syncRun    syncState
	syncCancel context.CancelFunc // 在飞批次的取消句柄（锁库/重入时触发）

	// 自动锁（autolock.go）。
	autoLockMin  int
	lastActive   time.Time
	autoLockStop chan struct{}
}

// New 构造应用状态。装配方保证 cfg.Store / cfg.Queue 非 nil。
func New(cfg Config) *State {
	s := &State{
		cfg:        cfg,
		emitFn:     cfg.Emit,
		lastActive: time.Now(),
	}
	// 装配队列终态回调（任务完成/失败 → 续传记录与横幅计数同步）
	s.bindTaskCallbacks()
	return s
}

// Log 返回日志器（供 bind 层复用同一实例）。
func (s *State) Log() *slog.Logger { return s.cfg.Log }

// Store 返回设置存储（bind 层读键值用）。
func (s *State) Store() *settings.Store { return s.cfg.Store }

// Queue 返回传输队列（bind 层经 appstate 方法间接使用，直接引用仅装配用）。
func (s *State) Queue() *transfer.Queue { return s.cfg.Queue }

// ---------------------------------------------------------------------------
// 事件出口
// ---------------------------------------------------------------------------

// emit 转发事件；nil 出口静默丢弃（测试可不注入）。
func (s *State) emit(name string, data any) {
	if s.emitFn != nil {
		s.emitFn(name, data)
	}
}

// SetEmit 装配期替换事件出口。State 先于 OnStartup 构造（当时合帧器
// 尚未创建），startup 里 NewEvents 后经此注入 Forward；测试也能注入
// 收集器替代 cfg.Emit。调用需先于任何连接（无并发窗口，不加锁）。
func (s *State) SetEmit(fn func(name string, data any)) {
	s.emitFn = fn
}

// 事件名常量：bind/events 原样转发给前端（前缀 st 区分瞬时事件与帧）。
const (
	// EventOpProgress 向导/恢复码等长操作的阶段进度文案（低频，直发）。
	EventOpProgress = "st:op-progress"
	// EventOpDone 长操作结束（data: 文案）；EventOpError 长操作失败。
	EventOpDone  = "st:op-done"
	EventOpError = "st:op-error"
	// EventLocked 自动锁/手动锁触发后广播（前端立即回引导态）。
	EventLocked = "st:locked"
)

// ---------------------------------------------------------------------------
// 状态快照（事件帧与一次性查询共用）
// ---------------------------------------------------------------------------

// TaskView 是传输任务的前端视图（ID 为队列内稳定标识）。
type TaskView struct {
	ID          int64   `json:"id"`
	DisplayName string  `json:"name"`
	Direction   string  `json:"direction"`
	State       string  `json:"state"`
	Progress    float64 `json:"progress"`
	TotalBytes  int64   `json:"totalBytes"`
	DoneBytes   int64   `json:"doneBytes"`
	ErrorMsg    string  `json:"errorMsg"`
	RemotePath  string  `json:"remote"`
	LocalPath   string  `json:"local"`
}

// Snapshot 是前端订阅的全局状态帧（10Hz 合帧器每帧取一次）。
type Snapshot struct {
	Connected    bool   `json:"connected"`
	VaultName    string `json:"vaultName"`
	VaultPath    string `json:"vaultPath"`
	Backend      string `json:"backend"` // 显示名
	BackendID    string `json:"backendId"`
	FilenameEnc  bool   `json:"filenameEnc"`
	HasRecovery  bool   `json:"hasRecovery"`
	ConnectedSec int64  `json:"connectedSec"`
	ResumeCount  int    `json:"resumeCount"`
	ProxyBase    string `json:"proxyBase"`
	AutoLockMin  int    `json:"autoLockMin"`

	TransferActive bool  `json:"transferActive"`
	TransferDone   int64 `json:"transferDone"`
	TransferTotal  int64 `json:"transferTotal"`
	TransferTasks  int   `json:"transferTasks"`

	Sync SyncStatus `json:"sync"`

	StatsDone   bool  `json:"statsDone"`
	StatsTotal  int64 `json:"statsTotal"`
	StatsFiles  int64 `json:"statsFiles"`
	StatsFailed bool  `json:"statsFailed"`
}

// Snapshot 取当前全局状态（轻量拼接，10Hz 调用无压力）。
func (s *State) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap := Snapshot{
		ResumeCount: s.resumeCount,
		AutoLockMin: s.autoLockMin,
		Sync:        s.syncRun,
		StatsDone:   s.statsDone,
		StatsTotal:  s.statsTotal,
		StatsFiles:  s.statsFiles,
		StatsFailed: s.statsFailed,
	}
	if c := s.conn; c != nil {
		snap.Connected = true
		snap.VaultName = vaultDisplayName(c.meta)
		snap.VaultPath = c.vaultPath
		snap.Backend = c.label
		snap.BackendID = c.addr
		snap.FilenameEnc = c.meta.FilenameEnc
		snap.HasRecovery = c.meta.HasRecovery
		snap.ConnectedSec = int64(time.Since(c.connected).Seconds())
		if c.proxy != nil {
			snap.ProxyBase = c.proxy.BaseURL()
		}
	}
	if done, total := s.cfg.Queue.Aggregate(); total > 0 {
		snap.TransferActive = done < total || s.cfg.Queue.HasActive()
		snap.TransferDone = done
		snap.TransferTotal = total
		snap.TransferTasks = len(s.cfg.Queue.UnfinishedTasks())
	}
	return snap
}

// vaultDisplayName 密库展示名：自定义名称优先，空则回退 vault_id 截短
// （对照 app.py _vault_display_name）。
func vaultDisplayName(meta *cryptox.VaultMetadata) string {
	if meta == nil {
		return ""
	}
	if name := trimSpace(meta.Name); name != "" {
		return name
	}
	// vault_id 为 16 字节 UUID，展示前 8 个 hex + 省略号
	id := meta.VaultID
	if len(id) > 4 {
		return hexPrefix(id) + "…"
	}
	return ""
}

// ---------------------------------------------------------------------------
// 连接态访问器
// ---------------------------------------------------------------------------

// connectedLocked 返回当前连接（锁已持有）。
func (s *State) connectedLocked() *connState { return s.conn }

// Connected 是否已连接密库。
func (s *State) Connected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.conn != nil
}

// connSnapshot 供绑定层读取连接参数（设置页连接信息等）。
func (s *State) connSnapshot() (label, addr, kind string, vaultPath string, meta *cryptox.VaultMetadata) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c := s.conn; c != nil {
		return c.label, c.addr, c.kind, c.vaultPath, c.meta
	}
	return "", "", "", "", nil
}

// Meta 返回当前密库元信息（未连接返回 nil；只读共享，勿修改）。
func (s *State) Meta() *cryptox.VaultMetadata {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if c := s.conn; c != nil {
		return c.meta
	}
	return nil
}

// requireConn 取当前连接；未连接返回 ErrLocked。
func (s *State) requireConn() (*connState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.conn == nil {
		return nil, ErrLocked
	}
	return s.conn, nil
}

// contextWithTimeout 网络操作统一超时（后端无响应不悬挂 UI 操作）。
func contextWithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, d)
}

// 名字工具（避免 import 冲突的小函数）。
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func hexPrefix(b []byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, 8)
	for i := 0; i < 4 && i < len(b); i++ {
		out[2*i] = hexd[b[i]>>4]
		out[2*i+1] = hexd[b[i]&0x0f]
	}
	return string(out)
}
