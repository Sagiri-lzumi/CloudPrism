// Package transfer 提供并发传输任务队列：并发调度、聚合进度与自动重试。
//
// 职责对齐 WindowsPy/src/cloudprism/core/transfer_queue.py 的 TransferQueue，
// 但去掉 Qt 信号与线程模型：Python 靠 QThread + 信号事件循环，Go 端任务
// 执行体由调用方注入的 Runner 提供（绑定层组合 pipeline 加密/解密管线），
// 队列只负责状态机——补位调度、自动重试、取消、脏续传校验与字节级聚合
// 进度——并通过回调把事件推给上层（Wails 绑定层转发到前端）。
//
// 状态机与 Python 逐态对齐：waiting / running / done / failed / cancelled；
// 失败自动重试 1 次（AUTO_RETRIES），仍失败置 failed 供界面手动重试。
//
// 断点续传一致性（transfer_queue.py:10-12 三期「先删后传」教训延续）：
// 来自持久化记录的任务携带 expected_size/expected_mtime，启动上传前校验
// 本地源文件未变化，变化则先删除远端半成品再整传，避免脏偏移续传。
package transfer

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// 任务状态机取值（与 Python 字符串逐字一致，供持久化与前端透传）。
const (
	StateWaiting   = "waiting"
	StateRunning   = "running"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// 任务方向。
const (
	DirUpload   = "upload"
	DirDownload = "download"
)

// AutoRetries 失败后自动重试次数（网络容错；超出置 failed 供手动重试）。
// 对照 transfer_queue.py:38。
const AutoRetries = 1

// HeaderEstimate 是加密文件头估算长度（magic 8 + version 4 + hl 4 +
// saltlen 1 + salt 16 + ivlen 1 + iv 16 + flags 1 = 51）。
//
// 下载任务启动时用它从远端大小估算明文总字节（下载真正的头长要到解密
// 时才可知，进度条需要一个先验值）；对照 transfer_queue.py:28。
const HeaderEstimate = 51

// Task 是单个传输任务的状态载体。
//
// 与 Python dataclass(eq=False) 对齐：任务以指针身份比较，不做按值语义。
// ExpectedSize/ExpectedMtime 为入队时的本地源文件快照，仅由续传记录重建
// 的任务携带（指针非 nil）；携带时启动前做脏续传校验。
//
// mu 为指针字段是有意设计：队列需要跨 goroutine 锁任务，快照拷贝时
// 不想连带锁对象（快照不可变，无人加锁）；指针化后值拷贝对调用方透明。
type Task struct {
	LocalPath     string  // 本地文件路径
	RemotePath    string  // 后端上的目标路径（含 .cpenc）
	DisplayName   string  // 界面显示名；空时取本地文件名
	Direction     string  // "upload" / "download"
	State         string  // 状态机取值（见上）
	Progress      float64 // 0.0~1.0
	TotalBytes    int64   // 总字节（上传=本地大小；下载=远端大小-头估算）
	DoneBytes     int64   // 已完成字节（progress × total 的整数投影）
	Retries       int     // 已自动重试次数
	ErrorMsg      string  // 最近一次错误信息（failed 时展示）
	ExpectedSize  *int64  // 续传记录快照（可选）
	ExpectedMtime *float64

	mu *sync.Mutex // 保护上述可变字段；队列回调在锁外执行（NewTask 初始化）
}

// lock/unlock 暴露给队列内部使用（字段更新与快照读取的临界区）。
//
// 小写私有化是有意设计：Go vet 的 copylocks 把「定义 Lock/Unlock 方法对
// 的类型」一律视为不可拷贝，而本类型需要 Snapshot 值拷贝（快照/持久化）；
// 且 mu 为包私有字段，导出锁方法外部也无从使用，私有化零损失。
func (t *Task) lock()   { t.mu.Lock() }
func (t *Task) unlock() { t.mu.Unlock() }

// Snapshot 返回任务的不可变拷贝（前端/持久化读取，避免锁穿透接口）。
//
// 逐字段构造而非整体解引用：Task 持锁指针，直接 `return *t` 会连锁指针
// 一同拷贝（快照与任务将共享同一把锁）；构造字面量不带 mu 字段（快照的
// 锁指针为 nil），语义上不可变，杜绝误锁。
func (t *Task) Snapshot() Task {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Task{
		LocalPath:     t.LocalPath,
		RemotePath:    t.RemotePath,
		DisplayName:   t.DisplayName,
		Direction:     t.Direction,
		State:         t.State,
		Progress:      t.Progress,
		TotalBytes:    t.TotalBytes,
		DoneBytes:     t.DoneBytes,
		Retries:       t.Retries,
		ErrorMsg:      t.ErrorMsg,
		ExpectedSize:  t.ExpectedSize,
		ExpectedMtime: t.ExpectedMtime,
	}
}

// NewTask 构造任务；Direction 缺省按 upload。
func NewTask(localPath, remotePath, direction string) *Task {
	t := &Task{
		LocalPath:  localPath,
		RemotePath: remotePath,
		Direction:  direction,
		State:      StateWaiting,
	}
	if t.Direction == "" {
		t.Direction = DirUpload
	}
	t.DisplayName = filepath.Base(localPath)
	t.mu = &sync.Mutex{}
	return t
}

// Runner 执行单个任务的传输动作（由上层注入）。
//
// report 以 0.0~1.0 回调进度（可能从任意 goroutine 调用，实现需并发安全）。
// 返回 nil 表示成功；返回 context.Canceled 表示被取消；其余错误触发自动
// 重试逻辑。ctx 取消时 Runner 应在分块边界尽快退出。
type Runner func(ctx context.Context, task *Task, report func(float64)) error

// Queue 是并发传输队列（任务级并行 + 字节级聚合进度）。
type Queue struct {
	Session *session.Session
	Backend storage.Backend
	KDFSalt []byte // 上传加密复用的 KDF 盐（密库元信息盐，连接时注入）

	Chunk         int // 传输分块大小（默认 1MiB）
	MaxWorkers    int // 单任务并行加密核数
	MaxConcurrent int

	// 回调（均锁外调用，可 nil）：事件转发到 Wails 绑定层。
	OnTaskProgress func(*Task)
	OnTaskFinished func(*Task, bool) // success 表示成功与否
	OnAggregate    func(done, total int64)

	// Runner 实际执行传输；nil 时任务启动即失败。
	Runner Runner

	mu        sync.Mutex
	rootCtx   context.Context
	cancelAll context.CancelFunc
	tasks     []*Task
	running   map[*Task]context.CancelFunc
	draining  bool // Clear 后置位：运行中任务终态不再回调
	pumping   bool
	notify    chan struct{}
}

// New 构造队列并绑定根 ctx（进程退出/锁库时用 CancelAll/Clear 中断）。
func New() *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	return &Queue{
		Chunk:         1 << 20,
		MaxWorkers:    1,
		MaxConcurrent: 2, // 对照 transfer_queue.py:92
		rootCtx:       ctx,
		cancelAll:     cancel,
		running:       map[*Task]context.CancelFunc{},
		notify:        make(chan struct{}, 1),
	}
}

// Bind 绑定一次成功连接（连接密库后调用），并唤醒可能因未绑定而停摆的调度。
func (q *Queue) Bind(session *session.Session, backend storage.Backend, kdfSalt []byte) {
	q.mu.Lock()
	q.Session = session
	q.Backend = backend
	q.KDFSalt = kdfSalt
	q.mu.Unlock()
	q.kick()
}

// SetTransferOptions 同步设置页的分块大小与并行加密核数。
func (q *Queue) SetTransferOptions(chunk, maxWorkers int) {
	q.mu.Lock()
	if chunk > 0 {
		q.Chunk = chunk
	}
	if maxWorkers > 0 {
		q.MaxWorkers = maxWorkers
	}
	q.mu.Unlock()
}

// SetMaxConcurrent 调整并发上限；调大时立即补位启动等待中的任务。
func (q *Queue) SetMaxConcurrent(n int) {
	q.mu.Lock()
	if n < 1 {
		n = 1
	}
	q.MaxConcurrent = n
	q.mu.Unlock()
	q.kick()
}

// Enqueue 入队一批任务并立即调度（等待中任务也计入聚合进度，避免进度条
// 在任务启动时跳变——上传任务在此预统计本地大小）。
func (q *Queue) Enqueue(tasks []*Task) {
	if len(tasks) == 0 {
		return
	}
	for _, t := range tasks {
		t.lock()
		if t.Direction == DirUpload && t.TotalBytes == 0 {
			if st, err := os.Stat(t.LocalPath); err == nil {
				t.TotalBytes = st.Size() // 预统计；失败留待启动时再试
			}
		}
		t.State = StateWaiting
		t.unlock()
	}
	q.mu.Lock()
	q.tasks = append(q.tasks, tasks...)
	q.mu.Unlock()
	q.emitAggregate()
	q.kick()
}

// Retry 手动重试失败/取消的任务（重置状态重新入队）。
func (q *Queue) Retry(task *Task) bool {
	task.lock()
	if task.State != StateFailed && task.State != StateCancelled {
		task.unlock()
		return false
	}
	task.State = StateWaiting
	task.Progress = 0
	task.DoneBytes = 0
	task.Retries = 0
	task.ErrorMsg = ""
	task.unlock()
	q.emitAggregate()
	q.kick()
	return true
}

// CancelAll 取消全部任务：运行中的在分块边界停止，等待中的直接标记终态。
func (q *Queue) CancelAll() {
	q.mu.Lock()
	finished := make([]*Task, 0)
	for _, t := range q.tasks {
		t.lock()
		if t.State == StateWaiting {
			t.State = StateCancelled
			finished = append(finished, t)
		}
		t.unlock()
	}
	cancels := make([]context.CancelFunc, 0, len(q.running))
	for _, c := range q.running {
		cancels = append(cancels, c)
	}
	q.mu.Unlock()

	for _, c := range cancels {
		c() // 运行中的任务：ctx 取消，在分块边界停止
	}
	for _, t := range finished {
		q.notifyFinished(t, false)
	}
	q.emitAggregate()
	q.kick()
}

// Clear 锁库时调用：静默停止全部任务并清空状态（不发终态回调）。
//
// 运行中的 goroutine 可能在分块边界后仍在收尾：置 draining 后其终态不再
// 触发任何回调，任务列表清空；goroutine 结束即被回收，无 Python 端的
// QObject 析构时序问题。
func (q *Queue) Clear() {
	q.mu.Lock()
	q.draining = true
	cancels := make([]context.CancelFunc, 0, len(q.running))
	for _, c := range q.running {
		cancels = append(cancels, c)
	}
	q.tasks = nil
	q.running = map[*Task]context.CancelFunc{}
	q.mu.Unlock()

	for _, c := range cancels {
		c()
	}
}

// HasActive 是否有运行中或等待中的任务（自动锁定豁免用）。
func (q *Queue) HasActive() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.running) > 0 {
		return true
	}
	for _, t := range q.tasks {
		t.lock()
		active := t.State == StateWaiting
		t.unlock()
		if active {
			return true
		}
	}
	return false
}

// IsIdle 与 HasActive 相反。
func (q *Queue) IsIdle() bool { return !q.HasActive() }

// UnfinishedTasks 未完成（waiting/running）的任务快照（续传记录持久化用）。
func (q *Queue) UnfinishedTasks() []Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Task, 0, len(q.tasks))
	for _, t := range q.tasks {
		snap := t.Snapshot()
		if snap.State == StateWaiting || snap.State == StateRunning {
			out = append(out, snap)
		}
	}
	return out
}

// Aggregate 当前聚合进度（已完成字节, 总字节）。
func (q *Queue) Aggregate() (done, total int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.computeAggregateLocked()
}

// ---------------------------------------------------------------------------
// 内部调度
// ---------------------------------------------------------------------------

// kick 唤醒调度（幂等）：发通知 + 启动 pump goroutine。pump 内部单飞，
// 重复 kick 只会多创建几个立刻退出的 goroutine，无副作用。
func (q *Queue) kick() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
	go q.pump()
}

// pump 是调度主循环：单飞（同一时刻至多一个），无活动任务即退出。
func (q *Queue) pump() {
	q.mu.Lock()
	if q.pumping {
		q.mu.Unlock()
		return
	}
	q.pumping = true
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.pumping = false
		// 退出窗口竞态：pumping 置位期间若任务被加回 waiting（如失败重试
		// 的 kick 落在这轮 pump 退出后），这里兜底再唤醒一轮，避免任务
		// 滞留无人调度。
		active := len(q.running) > 0 || q.hasWaitingLocked()
		q.mu.Unlock()
		if active {
			q.kick()
		}
	}()

	for q.pumpOnce() {
		<-q.notify
	}
}

// pumpOnce 补位一轮：并发未满时启动等待中的任务。返回是否有活任务。
func (q *Queue) pumpOnce() bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	bound := q.Session != nil && q.Backend != nil && q.Runner != nil
	for _, t := range q.tasks {
		if len(q.running) >= q.MaxConcurrent {
			break
		}
		t.lock()
		waiting := t.State == StateWaiting
		t.unlock()
		if !waiting {
			continue
		}
		if !bound {
			// 未绑定：不启动也不移除，等待 Bind 后 kick 再补位
			break
		}
		ctx, cancel := context.WithCancel(q.rootCtx)
		q.running[t] = cancel
		t.lock()
		t.State = StateRunning
		t.unlock()
		go q.runOne(ctx, t)
	}
	return len(q.running) > 0 || q.hasWaitingLocked()
}

func (q *Queue) hasWaitingLocked() bool {
	for _, t := range q.tasks {
		t.lock()
		w := t.State == StateWaiting
		t.unlock()
		if w {
			return true
		}
	}
	return false
}

// runOne 执行单个任务并落地终态（成功/失败/重试/取消）。
func (q *Queue) runOne(ctx context.Context, task *Task) {
	// 启动前准备：统计总字节 + 脏续传校验；失败直接置 failed（不重试，
	// 与 Python _start_task 的 prepare 异常路径一致）
	if err := q.prepare(ctx, task); err != nil {
		q.finish(task, false, err.Error())
		return
	}

	report := func(p float64) { q.report(task, p) }
	err := q.Runner(ctx, task, report)

	switch {
	case err == nil:
		q.finish(task, true, "")
	case ctx.Err() != nil:
		// 取消（CancelAll/根 ctx 中断）：任务置 cancelled
		q.mu.Lock()
		delete(q.running, task)
		q.mu.Unlock()
		task.lock()
		task.State = StateCancelled
		task.ErrorMsg = ""
		task.unlock()
		q.notifyFinished(task, false)
		q.emitAggregate()
		q.kick()
	default:
		q.finish(task, false, err.Error())
	}
}

// finish 统一收尾：成功 → done；失败 → 自动重试 1 次或 failed。
func (q *Queue) finish(task *Task, success bool, errMsg string) {
	q.mu.Lock()
	delete(q.running, task)
	q.mu.Unlock()

	task.lock()
	if success {
		task.State = StateDone
		task.Progress = 1.0
		task.DoneBytes = task.TotalBytes
		task.ErrorMsg = ""
	} else {
		task.Progress = 0.0
		task.DoneBytes = 0
		task.ErrorMsg = errMsg
		if task.Retries < AutoRetries {
			// 自动重试一次（网络抖动容错）：回 waiting 等待补位
			task.Retries++
			task.State = StateWaiting
			task.unlock()
			q.emitAggregate()
			q.kick()
			return
		}
		task.State = StateFailed
	}
	task.unlock()

	if success {
		q.notifyFinished(task, true)
	} else {
		q.notifyFinished(task, false)
	}
	q.emitAggregate()
	q.kick()
}

// prepare 启动前准备：上传统计本地大小（续传记录则做脏校验），下载按
// 远端大小减头估算。错误表示任务无法启动（如源文件丢失）。
func (q *Queue) prepare(ctx context.Context, task *Task) error {
	if task.Direction == DirUpload {
		st, err := os.Stat(task.LocalPath)
		if err != nil {
			return err
		}
		task.lock()
		task.TotalBytes = st.Size()
		expected := task.ExpectedSize != nil
		task.unlock()
		if expected {
			q.verifyResumeSource(ctx, task)
		}
		return nil
	}

	// 下载：远端大小 - 头估算；取不到时置 0（运行中 runner 会报真实错误）
	q.mu.Lock()
	backend := q.Backend
	q.mu.Unlock()
	total := int64(0)
	if backend != nil {
		if size, err := backend.GetSize(ctx, task.RemotePath); err == nil {
			total = size - HeaderEstimate
			if total < 0 {
				total = 0
			}
		}
	}
	task.lock()
	task.TotalBytes = total
	task.unlock()
	return nil
}

// verifyResumeSource 校验续传任务的本地源文件未变化；变化则删除远端
// 半成品（防脏续传：半成品基于旧内容，删后整传）。
// 对照 transfer_queue.py:271-291。
func (q *Queue) verifyResumeSource(ctx context.Context, task *Task) {
	task.lock()
	expectedSize := *task.ExpectedSize
	expectedMtime := float64(0)
	if task.ExpectedMtime != nil {
		expectedMtime = *task.ExpectedMtime
	}
	total := task.TotalBytes
	task.unlock()

	sourceOK := false
	if st, err := os.Stat(task.LocalPath); err == nil {
		sourceOK = st.Size() == expectedSize &&
			absFloat64(st.ModTime().Sub(time.Unix(0, 0)).Seconds()-expectedMtime) < 1.0
	}

	if sourceOK {
		return
	}
	// 源文件已变：远端半成品基于旧内容，删除后整传。删除失败不阻断：
	// 上传按已有字节续传最多浪费带宽（Python 同语义）
	q.mu.Lock()
	backend := q.Backend
	q.mu.Unlock()
	if backend == nil {
		return
	}
	exists, err := backend.Exists(ctx, task.RemotePath)
	if err != nil || !exists {
		return
	}
	size, err := backend.GetSize(ctx, task.RemotePath)
	if err != nil {
		return
	}
	// 仅当远端是「未完成的半成品」（比完整文件还小）才删；
	// 完整文件说明上次传输其实成功了，删掉反而丢数据
	if size > 0 && size <= total+HeaderEstimate+16 {
		_ = backend.Delete(ctx, task.RemotePath)
	}
}

// report 单任务进度更新（锁外回调）。
func (q *Queue) report(task *Task, p float64) {
	task.lock()
	if task.State != StateRunning {
		task.unlock()
		return // 终态后的迟到进度忽略
	}
	task.Progress = p
	task.DoneBytes = int64(p * float64(task.TotalBytes))
	task.unlock()
	q.notifyProgress(task)
	q.emitAggregate()
}

// notifyProgress 回调包装（draining 时不发）。
func (q *Queue) notifyProgress(task *Task) {
	q.mu.Lock()
	draining := q.draining
	cb := q.OnTaskProgress
	q.mu.Unlock()
	if !draining && cb != nil {
		cb(task)
	}
}

// notifyFinished 终态回调包装（draining 时不发）。
func (q *Queue) notifyFinished(task *Task, success bool) {
	q.mu.Lock()
	draining := q.draining
	cb := q.OnTaskFinished
	q.mu.Unlock()
	if !draining && cb != nil {
		cb(task, success)
	}
}

// emitAggregate 按字节聚合全部任务进度并回调（完成的任务计满额）。
// 对照 transfer_queue.py:371-381。
func (q *Queue) emitAggregate() {
	q.mu.Lock()
	done, total := q.computeAggregateLocked()
	draining := q.draining
	cb := q.OnAggregate
	q.mu.Unlock()
	if !draining && cb != nil {
		cb(done, total)
	}
}

func (q *Queue) computeAggregateLocked() (done, total int64) {
	for _, t := range q.tasks {
		t.lock()
		total += t.TotalBytes
		switch t.State {
		case StateDone:
			done += t.TotalBytes
		case StateRunning, StateWaiting, StateFailed:
			done += t.DoneBytes
		}
		t.unlock()
	}
	return done, total
}

func absFloat64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
