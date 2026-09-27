package appstate

// 删除任务的集成测试（用户 2026-09-27 的需求：「删除之类的状态也要在传输中
// 显示，要不然我不知道到底啥情况，比如我删东西，不知道删除好了没有」）。
//
// 这里钉住四件事：
//   1. 一次多选 → 每个选中项一个任务，父目录吃掉自己的子项（不产生白跑的失败任务）；
//   2. 删除任务真的把远端删掉，并按**条目数**上报进度（不是字节）；
//   3. 删除任务**不写续传记录**（删除是一次性动作，不该出现在「恢复上传」横幅）；
//   4. 队列里只有删除任务时，快照的 TransferActive/TransferTasks 仍然正确 ——
//      这是本次一并修掉的一个真 bug：原先这两项被「字节总数 > 0」门禁住，
//      纯删除期间任务数为 0，侧栏角标与底部传输栏会一起消失。

import (
	"context"
	"testing"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/transfer"
)

// waitQueueIdle 等队列静默（上传/删除任务全部离开等待与执行态）。
func waitQueueIdle(t *testing.T, e *testEnv) {
	t.Helper()
	waitFor(t, 20*time.Second, "队列静默", func() bool { return e.queue.IsIdle() })
}

// uploadFiles 把若干内容确定的本地文件上传到 remoteDir 并等任务跑完。
func uploadFiles(t *testing.T, e *testEnv, srcDir, remoteDir string, names ...string) {
	t.Helper()
	paths := make([]string, 0, len(names))
	for _, n := range names {
		paths = append(paths, writeLocalFile(t, srcDir, n, "content-of-"+n))
	}
	if err := e.st.UploadPaths(context.Background(), paths, remoteDir); err != nil {
		t.Fatalf("UploadPaths(%v): %v", names, err)
	}
	waitQueueIdle(t, e)
}

// remoteDisplays 某个远端目录下的展示名集合（ListDir 已解密并过滤系统文件）。
func remoteDisplays(t *testing.T, e *testEnv, remote string) map[string]bool {
	t.Helper()
	entries, err := e.st.ListDir(context.Background(), remote)
	if err != nil {
		t.Fatalf("ListDir(%q): %v", remote, err)
	}
	out := make(map[string]bool, len(entries))
	for _, en := range entries {
		out[en.Display] = true
	}
	return out
}

// deleteTasks 取当前全部删除方向的任务视图。
func deleteTasks(e *testEnv) []TaskView {
	var out []TaskView
	for _, tv := range e.st.Tasks() {
		if tv.Direction == transfer.DirDelete {
			out = append(out, tv)
		}
	}
	return out
}

// TestDeleteRemotesOneTaskPerSelection 每个选中项一个任务，且父目录吃掉子项。
func TestDeleteRemotesOneTaskPerSelection(t *testing.T) {
	e := newTestEnv(t)
	e.mustConnect(true)

	src := t.TempDir()
	uploadFiles(t, e, src, "", "root-a.txt", "root-b.txt")
	uploadFiles(t, e, src, "sub", "inner.txt")

	subEntries, err := e.st.ListDir(context.Background(), "sub")
	if err != nil || len(subEntries) != 1 {
		t.Fatalf("远端 sub 下应有 1 个条目，实得 %d err=%v", len(subEntries), err)
	}
	inner := subEntries[0].Remote // 形如 "sub/<密文名>"

	// 拦下执行器：任务只入队不执行，于是同一环境可以连测多组，且远端不受影响
	e.queue.SetRunner(nil)

	cases := []struct {
		name    string
		remotes []string
		want    int
	}{
		{"三个顶层项 → 3 个任务", []string{"root-a.txt.cpenc", "root-b.txt.cpenc", "sub"}, 3},
		{"目录与其子项 → 只留目录", []string{"sub", inner}, 1},
		{"同上但顺序颠倒 → 仍只留一个", []string{inner, "sub"}, 1},
		{"重复项 → 去重成 1 个", []string{"sub", "sub"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := len(e.queue.UnfinishedTasks())
			n, err := e.st.DeleteRemotes(context.Background(), c.remotes)
			if err != nil {
				t.Fatalf("DeleteRemotes(%v): %v", c.remotes, err)
			}
			if n != c.want {
				t.Errorf("入队任务数应为 %d，实得 %d", c.want, n)
			}
			if got := len(e.queue.UnfinishedTasks()) - before; got != c.want {
				t.Errorf("队列增量应为 %d，实得 %d", c.want, got)
			}
		})
	}

	// 空选择不该入队
	if n, err := e.st.DeleteRemotes(context.Background(), []string{"", "///"}); err != nil || n != 0 {
		t.Errorf("空选择应返回 (0,nil)，实得 (%d,%v)", n, err)
	}
}

// TestDeleteTaskRemovesRemoteAndCountsItems 真删一遍：远端消失 + 条目数正确。
func TestDeleteTaskRemovesRemoteAndCountsItems(t *testing.T) {
	e := newTestEnv(t)
	e.mustConnect(true)

	src := t.TempDir()
	uploadFiles(t, e, src, "", "a.txt", "b.txt")
	uploadFiles(t, e, src, "sub", "c.txt")

	if got := remoteDisplays(t, e, ""); !got["a.txt"] || !got["b.txt"] || !got["sub"] {
		t.Fatalf("前置远端内容不符: %v", got)
	}

	// 同时删一个目录与一个文件：这是两个任务
	n, err := e.st.DeleteRemotes(context.Background(), []string{"sub", "a.txt.cpenc"})
	if err != nil {
		t.Fatalf("DeleteRemotes: %v", err)
	}
	if n != 2 {
		t.Fatalf("应入队 2 个任务，实得 %d", n)
	}
	waitQueueIdle(t, e)

	// 远端只剩 b.txt
	got := remoteDisplays(t, e, "")
	if got["a.txt"] || got["sub"] {
		t.Errorf("删除后不该再有 a.txt / sub，实得 %v", got)
	}
	if !got["b.txt"] {
		t.Errorf("b.txt 未被删除影响，应当仍在，实得 %v", got)
	}
	// 被删目录已不存在：列它应当直接报错（「路径不存在」本身就是期望结果，
	// 不能用 remoteDisplays 那种「列出即通过」的语义）
	if entries, err := e.st.ListDir(context.Background(), "sub"); err == nil {
		t.Errorf("被删目录 sub 不该还能列出内容，实得 %d 项", len(entries))
	}

	tasks := deleteTasks(e)
	if len(tasks) != 2 {
		t.Fatalf("应有 2 个删除任务，实得 %d", len(tasks))
	}
	// 逐任务核对条目数与终态：sub 里 1 个文件 + 目录自身 = 2 个节点；文件 1 个节点
	byName := map[string]TaskView{}
	for _, tv := range tasks {
		byName[tv.DisplayName] = tv
	}
	subTask, ok := byName["sub"]
	if !ok {
		t.Fatalf("缺少名为 sub 的删除任务，实得 %v", byName)
	}
	if subTask.State != transfer.StateDone {
		t.Errorf("sub 删除任务终态应为 done，实得 %q（err=%q）", subTask.State, subTask.ErrorMsg)
	}
	if subTask.TotalItems != 2 || subTask.DoneItems != 2 {
		t.Errorf("sub 条目进度应为 2/2，实得 %d/%d", subTask.DoneItems, subTask.TotalItems)
	}
	fileTask, ok := byName["a.txt"]
	if !ok {
		t.Fatalf("缺少名为 a.txt 的删除任务，实得 %v", byName)
	}
	if fileTask.State != transfer.StateDone || fileTask.TotalItems != 1 {
		t.Errorf("a.txt 删除任务应 done 且 1 个条目，实得 state=%q total=%d",
			fileTask.State, fileTask.TotalItems)
	}
	// 删除任务没有字节口径
	for _, tv := range tasks {
		if tv.TotalBytes != 0 {
			t.Errorf("删除任务不该有字节总数（%s total=%d）", tv.DisplayName, tv.TotalBytes)
		}
	}
}

// TestDeleteTasksAreNotResumable 删除任务不进「恢复上传」横幅，但仍计入任务数。
func TestDeleteTasksAreNotResumable(t *testing.T) {
	e := newTestEnv(t)
	e.mustConnect(true)

	src := t.TempDir()
	uploadFiles(t, e, src, "", "keep.txt")

	// 拦下执行器，让删除任务滞留在 waiting（模拟删除过程中锁库/退出）
	e.queue.SetRunner(nil)
	if _, err := e.st.DeleteRemotes(context.Background(), []string{"keep.txt.cpenc"}); err != nil {
		t.Fatalf("DeleteRemotes: %v", err)
	}

	if got := len(e.st.pendingRecords()); got != 0 {
		t.Errorf("删除任务不该写续传记录，实得 %d 条", got)
	}
	if got := e.st.Snapshot().ResumeCount; got != 0 {
		t.Errorf("续传横幅计数应为 0，实得 %d", got)
	}

	// 锁库再重连：仍不该冒出可恢复任务
	e.st.LockVault()
	e.mustConnect(false)
	if got := e.st.Snapshot().ResumeCount; got != 0 {
		t.Errorf("重连后续传横幅仍应为 0，实得 %d", got)
	}
	if _, err := e.st.ResumePending(); err == nil {
		t.Error("没有上传类记录时 ResumePending 应报错")
	}

	// 远端文件原封不动（删除任务从未执行）
	if got := remoteDisplays(t, e, ""); !got["keep.txt"] {
		t.Errorf("未执行的删除不该动到远端，实得 %v", got)
	}
}

// TestSnapshotCountsDeleteOnlyQueue 队列里只有删除任务时，任务计数与活动态必须正确。
//
// 这是本次一并修掉的真 bug：Snapshot 原先以「聚合字节总数 > 0」为唯一入口，
// 而删除任务字节恒为 0 ⇒ 纯删除期间 TransferTasks 恒 0、TransferActive 恒 false，
// 侧栏角标与底部传输栏会在用户最需要看的时候一起消失。
func TestSnapshotCountsDeleteOnlyQueue(t *testing.T) {
	e := newTestEnv(t)
	e.mustConnect(true)

	src := t.TempDir()
	uploadFiles(t, e, src, "", "x.txt", "y.txt")
	waitQueueIdle(t, e)

	// 上传任务终结后先清空，保证队列里只剩删除任务
	e.st.ClearFinished()
	e.queue.SetRunner(nil)
	if _, err := e.st.DeleteRemotes(context.Background(),
		[]string{"x.txt.cpenc", "y.txt.cpenc"}); err != nil {
		t.Fatalf("DeleteRemotes: %v", err)
	}

	snap := e.st.Snapshot()
	if snap.TransferTasks != 2 {
		t.Errorf("纯删除队列的任务数应为 2，实得 %d", snap.TransferTasks)
	}
	if !snap.TransferActive {
		t.Error("有删除任务在排队时 TransferActive 应为 true（否则底栏与角标都不出现）")
	}
	if snap.TransferTotal != 0 {
		t.Errorf("删除任务的字节总数应为 0，实得 %d", snap.TransferTotal)
	}
}
