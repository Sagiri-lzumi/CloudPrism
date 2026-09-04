package appstate

// appstate 集成测试：真实 Local 后端 + 真实密库管线（PBKDF2 派生秒级），
// 覆盖连接/锁库/恢复码/续传横幅/云端统计/自动锁的状态机与竞态语义。
//
// 测试环境隔离：CLOUDPRISM_DATA_DIR 重定向到临时目录（缩略图缓存、日志、
// 设置文件均不落仓库）；每用例独立后端根目录，互不干扰。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/settings"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/transfer"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/vault"
)

const testMasterPassword = "test-master-pw"

// testEnv 一次用例的完整装配。
type testEnv struct {
	t      *testing.T
	root   string // 后端根目录（本地文件夹后端）
	store  *settings.Store
	queue  *transfer.Queue
	st     *State
	evMu   sync.Mutex
	events []string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	t.Setenv(paths.DataDirEnv, t.TempDir())

	store, err := settings.Open(filepath.Join(paths.DataDir(), "settings.json"))
	if err != nil {
		t.Fatalf("settings.Open: %v", err)
	}
	e := &testEnv{
		t:     t,
		root:  t.TempDir(),
		store: store,
		queue: transfer.New(),
	}
	e.st = New(Config{
		Store: store,
		Queue: e.queue,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Emit: func(name string, _ any) {
			e.evMu.Lock()
			e.events = append(e.events, name)
			e.evMu.Unlock()
		},
	})
	return e
}

// hasEvent 事件名是否已出现（跨 goroutine 安全）。
func (e *testEnv) hasEvent(name string) bool {
	e.evMu.Lock()
	defer e.evMu.Unlock()
	for _, n := range e.events {
		if n == name {
			return true
		}
	}
	return false
}

// openVault 便捷开库：create=false 时带可选恢复码。
func (e *testEnv) openVault(create bool, password, recovery string) (string, error) {
	e.t.Helper()
	res, err := e.st.OpenVault(context.Background(), OpenRequest{
		Kind:           "local",
		LocalDir:       e.root,
		MasterPassword: password,
		VaultName:      "测试密库",
		VaultPath:      "",
		Create:         create,
		RecoveryCode:   recovery,
	})
	return res.Code, err
}

func (e *testEnv) mustConnect(create bool) string {
	e.t.Helper()
	code, err := e.openVault(create, testMasterPassword, "")
	if err != nil {
		e.t.Fatalf("连接密库失败: %v", err)
	}
	if !e.st.Connected() {
		e.t.Fatal("连接后应处于已连接态")
	}
	return code
}

// waitFor 轮询条件直至超时（后台任务/goroutine 收尾）。
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时（%s）", what)
}

func writeLocalFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("写本地文件: %v", err)
	}
	return p
}

// TestOpenCreateOpenLock 新建/凭密码开库/锁库/错误密码的状态机闭环。
func TestOpenCreateOpenLock(t *testing.T) {
	e := newTestEnv(t)
	code := e.mustConnect(true)

	// 新建返回恢复码（原始 16 字符）；状态帧携带连接与密库信息
	if len(code) != 16 {
		t.Errorf("新建应返回 16 字符恢复码，实得 %d 字符", len(code))
	}
	snap := e.st.Snapshot()
	if !snap.Connected || snap.VaultName != "测试密库" || !snap.HasRecovery {
		t.Errorf("状态帧异常: %+v", snap)
	}
	if snap.BackendID == "" || snap.Backend == "" {
		t.Errorf("状态帧应携带后端显示名与地址: %+v", snap)
	}
	// 同一位置重复新建 → ErrVaultExists
	if _, err := e.openVault(true, testMasterPassword, ""); !errors.Is(err, vault.ErrVaultExists) {
		t.Errorf("重复新建应报 ErrVaultExists，实得 %v", err)
	}

	// 最近记录已记住（vault_name 同步）
	recents := e.st.RecentVaults()
	if len(recents) != 1 || recents[0]["vault_name"] != "测试密库" {
		t.Errorf("最近记录异常: %v", recents)
	}

	// 锁库：连接态清空，未连接操作报 ErrLocked
	e.st.Lock()
	if e.st.Connected() {
		t.Error("锁库后不应再处于连接态")
	}
	if _, err := e.st.ListDir(context.Background(), ""); !errors.Is(err, ErrLocked) {
		t.Errorf("未连接列目录应报 ErrLocked，实得 %v", err)
	}
	if !e.hasEvent(EventLocked) {
		t.Error("锁库应广播 st:locked 事件")
	}

	// 主密码错误：ErrBadPassword（区别于「位置无密库」）
	if _, err := e.openVault(false, "wrong-password", ""); !errors.Is(err, vault.ErrBadPassword) {
		t.Errorf("错误主密码应报 ErrBadPassword，实得 %v", err)
	}
	// 凭恢复码开库：还原主密码并完成连接
	if _, err := e.openVault(false, "", code); err != nil {
		t.Fatalf("凭恢复码开库失败: %v", err)
	}
	if !e.st.Connected() {
		t.Error("凭恢复码开库后应处于连接态")
	}
	// 恢复码错误 → ErrBadRecovery
	e.st.Lock()
	if _, err := e.openVault(false, "", "AAAA-BBBB-CCCC-DDDD"); !errors.Is(err, vault.ErrBadRecovery) {
		t.Errorf("错误恢复码应报 ErrBadRecovery，实得 %v", err)
	}
	e.st.Lock() // 收尾（幂等）
}

// TestVaultOpsRenameAndRegenerate 重命名密库 + 重新生成恢复码（新旧码互斥）。
func TestVaultOpsRenameAndRegenerate(t *testing.T) {
	e := newTestEnv(t)
	oldCode := e.mustConnect(true)

	// 重命名：帧与最近记录同步更新
	if err := e.st.RenameCurrentVault(context.Background(), "新名字"); err != nil {
		t.Fatalf("重命名密库失败: %v", err)
	}
	if got := e.st.Snapshot().VaultName; got != "新名字" {
		t.Errorf("重命名后状态帧名称应为「新名字」，实得 %q", got)
	}
	if got := e.st.RecentVaults()[0]["vault_name"]; got != "新名字" {
		t.Errorf("最近记录应同步新名称，实得 %v", got)
	}
	// 空名称拒绝
	if err := e.st.RenameCurrentVault(context.Background(), "   "); err == nil {
		t.Error("空名称应被拒绝")
	}

	// 重新生成恢复码需主密码确认
	if _, err := e.st.RegenerateRecoveryCode(context.Background(), "wrong"); !errors.Is(err, vault.ErrBadPassword) {
		t.Errorf("错误主密码生成恢复码应报 ErrBadPassword，实得 %v", err)
	}
	newCode, err := e.st.RegenerateRecoveryCode(context.Background(), testMasterPassword)
	if err != nil {
		t.Fatalf("重新生成恢复码失败: %v", err)
	}
	// 展示码为 4 字符分组格式（XXXX-XXXX-XXXX-XXXX）
	if len(newCode) != 19 || strings.Count(newCode, "-") != 3 {
		t.Errorf("展示码应为 4 分组格式，实得 %q", newCode)
	}

	// 新码生效即旧码失效：凭旧码开库失败，凭新码成功
	e.st.Lock()
	if _, err := e.openVault(false, "", oldCode); !errors.Is(err, vault.ErrBadRecovery) {
		t.Errorf("旧恢复码应已失效，实得 %v", err)
	}
	if _, err := e.openVault(false, "", newCode); err != nil {
		t.Fatalf("凭新恢复码开库失败: %v", err)
	}
	e.st.Lock()
}

// TestResumeBannerLockAndResume 传输中锁库 → 补拍续传记录 → 重连横幅 →
// 续传执行并清空横幅的完整闭环。
func TestResumeBannerLockAndResume(t *testing.T) {
	e := newTestEnv(t)
	e.mustConnect(true)

	// 两个待上传文件
	dir := t.TempDir()
	fA := writeLocalFile(t, dir, "note-a.txt", "hello-a")
	fB := writeLocalFile(t, dir, "note-b.txt", "hello-b")

	// 拦截任务执行（模拟「排队等待中即锁库」：任务滞留 waiting 不启动）
	e.queue.Runner = nil
	if err := e.st.UploadPaths(context.Background(), []string{fA, fB}, ""); err != nil {
		t.Fatalf("UploadPaths: %v", err)
	}
	// 入队即落续传记录 → 横幅计数 2
	if got := e.st.Snapshot().ResumeCount; got != 2 {
		t.Fatalf("入队后横幅应为 2，实得 %d", got)
	}

	// 锁库：先补拍未完成任务再清空队列（记录保留供重连恢复）
	e.st.Lock()
	if got := len(e.st.pendingRecords()); got != 2 {
		t.Errorf("锁库应保留 2 条续传记录，实得 %d", got)
	}
	if got := e.st.Snapshot().ResumeCount; got != 0 {
		t.Errorf("锁库后横幅计数应复位为 0，实得 %d", got)
	}

	// 重连：探测到续传记录 → 横幅 2
	e.mustConnect(false)
	if got := e.st.Snapshot().ResumeCount; got != 2 {
		t.Fatalf("重连后横幅应为 2，实得 %d", got)
	}
	// 无记录时 ResumePending 报 ErrNoResume（先消耗掉当前两条再验证）
	if n, err := e.st.ResumePending(); err != nil || n != 2 {
		t.Fatalf("ResumePending 应恢复 2 条，实得 n=%d err=%v", n, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := e.st.Snapshot()
		if s.ResumeCount == 0 && !s.TransferActive {
			time.Sleep(50 * time.Millisecond)
			s = e.st.Snapshot() // 终态回调与横幅清零可能差一拍，再确认一次
			if s.ResumeCount == 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := e.st.Snapshot().ResumeCount; got != 0 {
		snap := e.st.Snapshot()
		t.Logf("现场 resumeCount=%d transferActive=%v done=%d total=%d pending=%d",
			snap.ResumeCount, snap.TransferActive, snap.TransferDone, snap.TransferTotal, len(e.st.pendingRecords()))
		for _, tv := range e.st.Tasks() {
			t.Logf("任务滞留: %+v", tv)
		}
		t.Fatalf("续传任务未在 5s 内完成，resumeCount=%d", got)
	}
	if _, err := e.st.ResumePending(); !errors.Is(err, ErrNoResume) {
		t.Errorf("无记录时续传应报 ErrNoResume，实得 %v", err)
	}

	// 远端可见两个文件（系统内部文件被过滤；密文大小为正）
	entries, err := e.st.ListDir(context.Background(), "")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("远端应恰有 2 个条目，实得 %d: %+v", len(entries), entries)
	}
	byName := map[string]FileEntry{}
	for _, en := range entries {
		byName[en.Display] = en
	}
	for _, name := range []string{"note-a.txt", "note-b.txt"} {
		en, ok := byName[name]
		if !ok {
			t.Errorf("远端缺文件 %s", name)
			continue
		}
		if en.IsDir || en.Size <= 0 {
			t.Errorf("文件 %s 元信息异常: %+v", name, en)
		}
	}
	e.st.Lock()
}

// TestStatsLockInvalidates 云端统计完成回填 + 锁库作废（seq 竞态丢弃）。
func TestStatsLockInvalidates(t *testing.T) {
	e := newTestEnv(t)
	e.mustConnect(true)

	// 造一个云端文件，让统计有非零结果
	f := writeLocalFile(t, t.TempDir(), "payload.bin", strings.Repeat("x", 4096))
	if err := e.st.UploadPaths(context.Background(), []string{f}, ""); err != nil {
		t.Fatalf("UploadPaths: %v", err)
	}
	waitFor(t, 5*time.Second, "上传任务完成", func() bool {
		return e.st.Snapshot().ResumeCount == 0
	})

	// 统计完成回填（含密库 marker 共 2 个文件）
	e.st.RequestStats(context.Background())
	waitFor(t, 5*time.Second, "统计结果回填", func() bool {
		s := e.st.Snapshot()
		return s.StatsDone && s.StatsFiles >= 2 && s.StatsTotal > 0
	})

	// 竞态作废：在飞统计期间锁库 → 结果序号过期被丢弃 / 状态复位，
	// 两种交错最终都不得残留旧统计数字
	e.st.RequestStats(context.Background())
	e.st.Lock()
	time.Sleep(300 * time.Millisecond) // 留出在飞 goroutine 的收尾窗口
	s := e.st.Snapshot()
	if s.StatsDone || s.StatsTotal != 0 || s.StatsFiles != 0 {
		t.Errorf("锁库后统计应复位为空，实得 %+v", s)
	}

	// 重新连接后统计能再次工作
	e.mustConnect(false)
	e.st.RequestStats(context.Background())
	waitFor(t, 5*time.Second, "重连后统计再次回填", func() bool {
		s := e.st.Snapshot()
		return s.StatsDone && s.StatsTotal > 0
	})
	e.st.Lock()
}

// TestAutoLockBehavior 自动锁判定：到期锁定 + 传输中豁免 + 活动刷新。
func TestAutoLockBehavior(t *testing.T) {
	e := newTestEnv(t)
	e.mustConnect(true)

	// 档位 1 = 5 分钟；刚连接不应到期
	e.st.ApplyAutoLockIndex(1)
	e.st.stopAutoLock() // 隔离 1s 心跳，手动驱动判定
	if min := e.st.autoLockMin; min != 5 {
		t.Fatalf("档位 1 应为 5 分钟，实得 %d", min)
	}
	e.st.checkAutoLockDue()
	if !e.st.Connected() {
		t.Fatal("空闲未到期不应锁定")
	}

	// 空闲超时 → 自动锁定并广播事件
	e.st.mu.Lock()
	e.st.lastActive = time.Now().Add(-10 * time.Minute)
	e.st.mu.Unlock()
	e.st.checkAutoLockDue()
	if e.st.Connected() {
		t.Error("空闲超时应自动锁定")
	}
	if !e.hasEvent(EventLocked) {
		t.Error("自动锁定应广播 st:locked")
	}

	// 传输进行中豁免（即使已超时也不锁，避免会话失效打挂任务）
	e.mustConnect(false)
	e.st.ApplyAutoLockIndex(1)
	e.st.stopAutoLock()
	f := writeLocalFile(t, t.TempDir(), "busy.bin", "busy")
	e.queue.Runner = nil // 任务滞留 waiting（队列仍有活任务）
	e.queue.Enqueue([]*transfer.Task{transfer.NewTask(f, "busy.bin.cpenc", transfer.DirUpload)})
	if !e.queue.HasActive() {
		t.Fatal("入队任务应处于活动状态")
	}
	e.st.mu.Lock()
	e.st.lastActive = time.Now().Add(-10 * time.Minute)
	e.st.mu.Unlock()
	e.st.checkAutoLockDue()
	if !e.st.Connected() {
		t.Error("传输进行中不应自动锁定")
	}

	// 活动刷新续期：同一空闲起点 Activity() 后不再到期
	e.st.mu.Lock()
	e.st.lastActive = time.Now().Add(-10 * time.Minute)
	e.st.mu.Unlock()
	e.st.Activity()
	e.st.checkAutoLockDue()
	if !e.st.Connected() {
		t.Error("Activity 刷新后不应锁定")
	}
	e.st.Lock()
}

// TestConcurrentTaskFinishClearsPending 并发任务同时收尾的续传记录竞态回归。
//
// 历史上 persistPendingLocked 无锁，多个终态回调（各自 goroutine）并发
// 读队列-写存储被交错 → 最后落盘残留非空记录，重连后横幅永远清不掉。
// 多任务并发完成反复触发该窗口，记录必须收敛为空。
func TestConcurrentTaskFinishClearsPending(t *testing.T) {
	e := newTestEnv(t)
	e.mustConnect(true)

	// 一批小文件并发上传：任务几乎同时收尾，最大化终态回调重叠概率
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 8; i++ {
		paths = append(paths, writeLocalFile(t, dir,
			fmt.Sprintf("f%d.txt", i), strings.Repeat("y", 64)))
	}
	if err := e.st.UploadPaths(context.Background(), paths, ""); err != nil {
		t.Fatalf("UploadPaths: %v", err)
	}

	// 全部任务收敛到终态后，续传记录必须已清空（横幅归零）
	waitFor(t, 10*time.Second, "并发任务全部完成", func() bool {
		if e.st.Snapshot().ResumeCount != 0 {
			return false
		}
		for _, tv := range e.st.Tasks() {
			if tv.State != "done" {
				return false
			}
		}
		return true
	})
	if rec := e.st.pendingRecords(); len(rec) != 0 {
		t.Fatalf("并发完成后续传记录应清空，实得 %d 条: %+v", len(rec), rec)
	}
	e.st.Lock()
}
