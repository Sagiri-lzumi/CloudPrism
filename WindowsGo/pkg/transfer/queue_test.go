package transfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// newSession 建测试会话（cleanup 时销毁密钥缓存）。
func newSession(t *testing.T) *session.Session {
	t.Helper()
	s := session.New("transfer-queue-test-pw")
	t.Cleanup(s.Close)
	return s
}

// waitIdle 轮询等待队列空闲（无 waiting/running 任务），超时即失败。
func waitIdle(t *testing.T, q *Queue, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for q.HasActive() {
		if time.Now().After(deadline) {
			t.Fatalf("队列未在 %v 内空闲：状态=%v", timeout, q.snapshotStates())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// snapshotStates 返回全部任务状态（错误定位用）。
func (q *Queue) snapshotStates() []string {
	out := make([]string, 0, len(q.tasks))
	for _, t := range q.tasks {
		out = append(out, t.Snapshot().State)
	}
	return out
}

// bindQueue 建队列并绑定真会话与本地后端（Runner 由用例注入）。
func bindQueue(t *testing.T, b storage.Backend) *Queue {
	t.Helper()
	q := New()
	q.Bind(newSession(t), b, nil)
	t.Cleanup(q.Clear)
	return q
}

// countingRunner 统计每个任务的执行次数与并发峰值。
type countingRunner struct {
	runs      map[*Task]int
	mu        sync.Mutex
	active    int
	peak      int
	failFirst int           // 前 failFirst 次调用返回错误（0=从不失败）
	gate      chan struct{} // 非 nil 时 runner 在此等待放行
	started   chan *Task    // 非 nil 时每个任务启动发一次信号
}

func (c *countingRunner) run(ctx context.Context, task *Task, report func(float64)) error {
	c.mu.Lock()
	c.runs[task]++
	c.active++
	if c.active > c.peak {
		c.peak = c.active
	}
	c.mu.Unlock()
	if c.started != nil {
		c.started <- task
	}
	if c.gate != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.gate:
		}
	}
	report(1.0)
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
	return nil
}

func (c *countingRunner) count(task *Task) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs[task]
}

func newCountingRunner() *countingRunner {
	return &countingRunner{runs: map[*Task]int{}}
}

// writeLocal 造本地源文件并返回其路径。
func writeLocal(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ---------------------------------------------------------------------------
// 调度与状态机
// ---------------------------------------------------------------------------

// TestConcurrencyCap 并发峰值不得超过 MaxConcurrent，且任务全部完成。
func TestConcurrencyCap(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backend")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	q := bindQueue(t, b)
	c := newCountingRunner()
	c.gate = make(chan struct{})
	c.started = make(chan *Task, 8)
	q.Runner = c.run

	dir := t.TempDir()
	const n = 6
	var tasks []*Task
	for i := 0; i < n; i++ {
		local := writeLocal(t, dir, fmt.Sprintf("src%d.bin", i), make([]byte, 1024))
		tasks = append(tasks, NewTask(local, fmt.Sprintf("f%d.cpenc", i), DirUpload))
	}
	q.Enqueue(tasks)

	// 并发 2 个先启动，其余等待补位
	for i := 0; i < 2; i++ {
		select {
		case <-c.started:
		case <-time.After(2 * time.Second):
			t.Fatal("前 2 个任务未启动")
		}
	}
	time.Sleep(150 * time.Millisecond) // 观察期：不应有第 3 个启动
	select {
	case tk := <-c.started:
		t.Fatalf("并发超出上限：观察期出现第 3 个启动: %s", tk.RemotePath)
	default:
	}
	close(c.gate) // 放行；补位任务将依次启动执行
	for i := 0; i < n-2; i++ {
		select {
		case <-c.started:
		case <-time.After(3 * time.Second):
			t.Fatal("等待任务未被补位启动")
		}
	}
	waitIdle(t, q, 3*time.Second)

	c.mu.Lock()
	peak, done := c.peak, len(c.runs)
	c.mu.Unlock()
	if peak > q.MaxConcurrent {
		t.Errorf("并发峰值 %d 超过上限 %d", peak, q.MaxConcurrent)
	}
	if done != n {
		t.Errorf("执行次数 %d != 任务数 %d", done, n)
	}
	for _, tk := range tasks {
		if s := tk.Snapshot(); s.State != StateDone {
			t.Errorf("任务 %s 状态 %s != done", tk.RemotePath, s.State)
		}
	}
}

// TestAutoRetryThenDone 首次失败自动重试 1 次后成功。
func TestAutoRetryThenDone(t *testing.T) {
	b, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	q := bindQueue(t, b)

	var tries int32
	q.Runner = func(ctx context.Context, task *Task, report func(float64)) error {
		if atomic.AddInt32(&tries, 1) == 1 {
			return fmt.Errorf("网络抖动")
		}
		report(1.0)
		return nil
	}
	local := writeLocal(t, t.TempDir(), "src.bin", []byte("data"))
	task := NewTask(local, "f.cpenc", DirUpload)
	q.Enqueue([]*Task{task})
	waitIdle(t, q, 3*time.Second)

	snap := task.Snapshot()
	if snap.State != StateDone {
		t.Fatalf("重试后应 done，实得 %s（err=%s）", snap.State, snap.ErrorMsg)
	}
	if got := atomic.LoadInt32(&tries); got != 2 {
		t.Errorf("应执行 2 次（首次失败+重试成功），实得 %d", got)
	}
	if snap.Retries != 1 {
		t.Errorf("Retries 应为 1，实得 %d", snap.Retries)
	}
}

// TestAutoRetryExhausted 恒失败的任务重试 1 次后置 failed 并带错误信息。
func TestAutoRetryExhausted(t *testing.T) {
	b, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	q := bindQueue(t, b)
	var tries int32
	q.Runner = func(ctx context.Context, task *Task, report func(float64)) error {
		atomic.AddInt32(&tries, 1)
		return fmt.Errorf("持续故障")
	}
	local := writeLocal(t, t.TempDir(), "src.bin", []byte("data"))
	task := NewTask(local, "f.cpenc", DirUpload)
	q.Enqueue([]*Task{task})
	waitIdle(t, q, 3*time.Second)

	snap := task.Snapshot()
	if snap.State != StateFailed {
		t.Fatalf("应 failed，实得 %s", snap.State)
	}
	if got := atomic.LoadInt32(&tries); got != AutoRetries+1 {
		t.Errorf("应执行 %d 次，实得 %d", AutoRetries+1, got)
	}
	if snap.ErrorMsg == "" {
		t.Error("failed 任务应携带错误信息")
	}
}

// TestManualRetry 手动重试 failed 任务后成功。
func TestManualRetry(t *testing.T) {
	b, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	q := bindQueue(t, b)
	c := newCountingRunner()
	c.failFirst = AutoRetries + 1 // 连败（含自动重试）后置 failed
	q.Runner = func(ctx context.Context, task *Task, report func(float64)) error {
		if c.count(task) < c.failFirst {
			c.runs[task]++
			return fmt.Errorf("持续故障")
		}
		report(1.0)
		return nil
	}
	local := writeLocal(t, t.TempDir(), "src.bin", []byte("data"))
	task := NewTask(local, "f.cpenc", DirUpload)
	q.Enqueue([]*Task{task})
	waitIdle(t, q, 3*time.Second)
	if task.Snapshot().State != StateFailed {
		t.Fatalf("前置：任务应 failed")
	}
	if !q.Retry(task) {
		t.Fatal("Retry 应返回 true")
	}
	waitIdle(t, q, 3*time.Second)
	if s := task.Snapshot(); s.State != StateDone || s.Retries != 0 {
		t.Errorf("手动重试后应 done 且 Retries 清零：state=%s retries=%d", s.State, s.Retries)
	}
}

// TestCancelAllWaitingAndRunning 取消同时作用于等待与运行中的任务。
func TestCancelAllWaitingAndRunning(t *testing.T) {
	b, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	q := bindQueue(t, b)
	started := make(chan *Task, 4)
	q.Runner = func(ctx context.Context, task *Task, report func(float64)) error {
		started <- task
		<-ctx.Done() // 运行中的任务：观察取消
		return ctx.Err()
	}
	dir := t.TempDir()
	var tasks []*Task
	for i := 0; i < 3; i++ {
		local := writeLocal(t, dir, fmt.Sprintf("s%d.bin", i), []byte("x"))
		tasks = append(tasks, NewTask(local, fmt.Sprintf("f%d.cpenc", i), DirUpload))
	}
	q.Enqueue(tasks)
	<-started // 第 1 个进入运行
	time.Sleep(100 * time.Millisecond)

	var finishedMu sync.Mutex
	var finished []bool
	q.OnTaskFinished = func(task *Task, success bool) {
		finishedMu.Lock()
		finished = append(finished, success)
		finishedMu.Unlock()
	}
	q.CancelAll()
	waitIdle(t, q, 3*time.Second)

	for _, tk := range tasks {
		if s := tk.Snapshot(); s.State != StateCancelled {
			t.Errorf("任务 %s 状态 %s != cancelled", tk.RemotePath, s.State)
		}
	}
	finishedMu.Lock()
	defer finishedMu.Unlock()
	if len(finished) != 3 {
		t.Errorf("应收到 3 次终态回调，实得 %d", len(finished))
	}
}

// TestClearSuppressesCallbacks Clear 后运行中的任务收尾不再触发任何回调。
func TestClearSuppressesCallbacks(t *testing.T) {
	b, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	q := bindQueue(t, b)
	block := make(chan struct{})
	var cbCount int32
	q.OnTaskFinished = func(task *Task, success bool) { atomic.AddInt32(&cbCount, 1) }
	q.Runner = func(ctx context.Context, task *Task, report func(float64)) error {
		<-block
		return nil
	}
	local := writeLocal(t, t.TempDir(), "s.bin", []byte("x"))
	task := NewTask(local, "f.cpenc", DirUpload)
	q.Enqueue([]*Task{task})

	// 等运行后 Clear（模拟锁库）；再放行让 runner 收尾
	deadline := time.Now().Add(2 * time.Second)
	for !q.isTaskRunning(task) {
		if time.Now().After(deadline) {
			t.Fatal("任务未进入运行态")
		}
		time.Sleep(5 * time.Millisecond)
	}
	q.Clear()
	close(block)
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&cbCount); got != 0 {
		t.Errorf("Clear 后不应有终态回调，实得 %d", got)
	}
	if len(q.tasks) != 0 {
		t.Errorf("Clear 后任务列表应清空，实得 %d 项", len(q.tasks))
	}
}

func (q *Queue) isTaskRunning(task *Task) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.running[task]
	return ok
}

// TestEnqueueUploadPreStat 上传任务入队时预统计本地大小（进度条不跳变）。
func TestEnqueueUploadPreStat(t *testing.T) {
	q := New() // 未绑定也可以入队（预统计与后端无关）
	defer q.Clear()
	local := writeLocal(t, t.TempDir(), "s.bin", make([]byte, 4096))
	task := NewTask(local, "f.cpenc", DirUpload)
	q.Enqueue([]*Task{task})
	if s := task.Snapshot(); s.TotalBytes != 4096 {
		t.Errorf("入队后 TotalBytes 应为 4096，实得 %d", s.TotalBytes)
	}
}

// ---------------------------------------------------------------------------
// 脏续传校验（真 Local 后端 + 远端半成品）
// ---------------------------------------------------------------------------

// TestDirtyResumeDeletesPartial 源文件变化时删除远端半成品后整传。
func TestDirtyResumeDeletesPartial(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backend")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	q := bindQueue(t, b)
	var deleted atomic.Bool
	q.Runner = func(ctx context.Context, task *Task, report func(float64)) error {
		// 真实 runner 上传前应确认远端半成品已清理
		ok, err := b.Exists(ctx, task.RemotePath)
		if err != nil {
			return err
		}
		if ok {
			return fmt.Errorf("脏续传校验未删除远端半成品")
		}
		deleted.Store(true)
		report(1.0)
		return nil
	}

	dir := t.TempDir()
	local := writeLocal(t, dir, "src.bin", []byte("version-1"))
	st, err := os.Stat(local)
	if err != nil {
		t.Fatal(err)
	}
	size := st.Size()
	mtime := st.ModTime().Sub(time.Unix(0, 0)).Seconds()

	// 远端遗留基于旧内容的半成品（大小 < 完整容器估算）
	if err := os.WriteFile(filepath.Join(root, "f.cpenc"), []byte("partial-"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 源文件已变化（更长的内容）
	local = writeLocal(t, dir, "src.bin", make([]byte, 5000))
	task := NewTask(local, "f.cpenc", DirUpload)
	task.ExpectedSize = &size
	task.ExpectedMtime = &mtime
	q.Enqueue([]*Task{task})
	waitIdle(t, q, 3*time.Second)

	if !deleted.Load() {
		t.Error("runner 未观察到远端半成品被删除")
	}
	if s := task.Snapshot(); s.State != StateDone {
		t.Errorf("任务应 done，实得 %s（%s）", s.State, s.ErrorMsg)
	}
}

// TestCleanResumeKeepsRemote 源文件与快照一致时保留远端（续传而非重传）。
func TestCleanResumeKeepsRemote(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backend")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	q := bindQueue(t, b)
	q.Runner = func(ctx context.Context, task *Task, report func(float64)) error {
		report(1.0)
		return nil
	}
	dir := t.TempDir()
	local := writeLocal(t, dir, "src.bin", make([]byte, 2048))
	st, err := os.Stat(local)
	if err != nil {
		t.Fatal(err)
	}
	size := st.Size()
	mtime := st.ModTime().Sub(time.Unix(0, 0)).Seconds()
	if err := os.WriteFile(filepath.Join(root, "f.cpenc"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	task := NewTask(local, "f.cpenc", DirUpload)
	task.ExpectedSize = &size
	task.ExpectedMtime = &mtime
	q.Enqueue([]*Task{task})
	waitIdle(t, q, 3*time.Second)

	ctx := context.Background()
	exists, err := b.Exists(ctx, "f.cpenc")
	if err != nil || !exists {
		t.Error("源未变化时不应删除远端半成品（应断点续传）")
	}
}

// TestDownloadPrepareTotal 下载任务按远端大小减头估算总字节。
func TestDownloadPrepareTotal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backend")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// 远端容器 = 51 头 + 1000 明文
	if err := os.WriteFile(filepath.Join(root, "f.cpenc"), make([]byte, 51+1000), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	q := bindQueue(t, b)
	q.Runner = func(ctx context.Context, task *Task, report func(float64)) error {
		report(1.0)
		return nil
	}
	task := NewTask("unused.bin", "f.cpenc", DirDownload)
	q.Enqueue([]*Task{task})
	waitIdle(t, q, 3*time.Second)

	if s := task.Snapshot(); s.TotalBytes != 1000 {
		t.Errorf("下载 TotalBytes 应为 1000，实得 %d", s.TotalBytes)
	}
}

// ---------------------------------------------------------------------------
// 聚合进度与快照
// ---------------------------------------------------------------------------

// TestAggregateDoneFull 全部完成后聚合 done==total（完成计满额）。
func TestAggregateDoneFull(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backend")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	q := bindQueue(t, b)
	q.Runner = func(ctx context.Context, task *Task, report func(float64)) error {
		report(1.0)
		return nil
	}
	dir := t.TempDir()
	sizes := []int64{100, 2000, 30000}
	var total int64
	var tasks []*Task
	for i, sz := range sizes {
		local := writeLocal(t, dir, fmt.Sprintf("s%d.bin", i), make([]byte, sz))
		total += sz
		tasks = append(tasks, NewTask(local, fmt.Sprintf("f%d.cpenc", i), DirUpload))
	}
	q.Enqueue(tasks)
	waitIdle(t, q, 3*time.Second)

	done, tot := q.Aggregate()
	if tot != total {
		t.Errorf("聚合 total=%d 应为 %d", tot, total)
	}
	if done != total {
		t.Errorf("聚合 done=%d 应为满额 %d", done, total)
	}
	// UnfinishedTasks：全部完成后应为空
	if u := q.UnfinishedTasks(); len(u) != 0 {
		t.Errorf("完成后 UnfinishedTasks 应为空，实得 %d 项", len(u))
	}
}
