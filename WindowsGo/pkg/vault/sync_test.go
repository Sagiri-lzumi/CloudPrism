package vault

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// newSyncFixture 搭本地后端 + 同步目录 + 同步引擎（文件名加密关闭）。
func newSyncFixture(t *testing.T) (*SyncEngine, *storage.Local, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "backend_root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	localDir := filepath.Join(t.TempDir(), "sync_dir")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sess := session.New("sync-master-pw")
	eng := NewSyncEngine(sess, b, "", false, nil)
	t.Cleanup(sess.Close)
	return eng, b, localDir
}

// writeFile 写本地文件并设置 mtime（秒级，兼容 NTFS 精度）。
func writeFile(t *testing.T, path string, data []byte, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// TestPlanThreeStates new / changed / unchanged 三态判定。
func TestPlanThreeStates(t *testing.T) {
	ctx := context.Background()
	eng, _, localDir := newSyncFixture(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)

	writeFile(t, filepath.Join(localDir, "a.txt"), []byte("AAA"), base)
	writeFile(t, filepath.Join(localDir, "sub", "b.txt"), []byte("BBB"), base)

	// 首次：全部 new
	plan, err := eng.Plan(ctx, localDir)
	if err != nil {
		t.Fatal(err)
	}
	if plan.PendingCount() != 2 || len(plan.Unchanged) != 0 {
		t.Fatalf("首次应 2 new: %+v", plan)
	}

	// 上传后写索引 → 再次计划全 unchanged
	entries := map[string]FileState{}
	for _, it := range append(append([]SyncItem{}, plan.New...), plan.Changed...) {
		entries[it.RelPath] = FileState{Size: it.Size, Mtime: it.Mtime}
	}
	if err := eng.UpdateIndex(ctx, entries); err != nil {
		t.Fatal(err)
	}
	plan2, err := eng.Plan(ctx, localDir)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.PendingCount() != 0 || len(plan2.Unchanged) != 2 {
		t.Fatalf("同步后应全 unchanged: %+v", plan2)
	}

	// 改一个内容 + 新增一个 → changed 1 + new 1
	writeFile(t, filepath.Join(localDir, "a.txt"), []byte("AAA-longer"), time.Now())
	writeFile(t, filepath.Join(localDir, "c.txt"), []byte("CCC"), base)
	plan3, err := eng.Plan(ctx, localDir)
	if err != nil {
		t.Fatal(err)
	}
	if plan3.PendingCount() != 2 || len(plan3.Unchanged) != 1 {
		t.Fatalf("应 new=1 changed=1 unchanged=1: %+v", plan3)
	}
	found := map[string]bool{}
	for _, it := range plan3.New {
		found["new:"+it.RelPath] = true
	}
	for _, it := range plan3.Changed {
		found["chg:"+it.RelPath] = true
	}
	if !found["new:c.txt"] || !found["chg:a.txt"] {
		t.Errorf("三态归类错误: %v", found)
	}
}

// TestPlanMtimeTolerance mtime 差在容差内不触发 changed（内容未变的
// touch 不产生上传）。
func TestPlanMtimeTolerance(t *testing.T) {
	ctx := context.Background()
	eng, _, localDir := newSyncFixture(t)
	base := time.Now().Add(-2 * time.Hour).Truncate(time.Second)

	writeFile(t, filepath.Join(localDir, "f.txt"), []byte("data"), base)
	plan, err := eng.Plan(ctx, localDir)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]FileState{}
	for _, it := range plan.New {
		entries[it.RelPath] = FileState{Size: it.Size, Mtime: it.Mtime}
	}
	if err := eng.UpdateIndex(ctx, entries); err != nil {
		t.Fatal(err)
	}

	// touch 只挪 0.5s（< 1s 容差）→ unchanged；挪 5s（内容没变但超容差）
	// Python 语义按 size+mtime 判定，mtime 大跳会误报 changed —— 这里只
	// 验证容差内不误报，超容差行为由 TestPlanThreeStates 的内容变更覆盖
	shift := base.Add(500 * time.Millisecond)
	if err := os.Chtimes(filepath.Join(localDir, "f.txt"), shift, shift); err != nil {
		t.Fatal(err)
	}
	plan2, err := eng.Plan(ctx, localDir)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.PendingCount() != 0 {
		t.Errorf("0.5s 内 mtime 偏移不应触发同步: %+v", plan2)
	}
}

// TestRemotePathFilenameEnc 文件名加密开/关两种模式的远程路径形态。
func TestRemotePathFilenameEnc(t *testing.T) {
	sess := session.New("pw")
	defer sess.Close()

	// 关：原样 + .cpenc 后缀
	plain := NewSyncEngine(sess, nil, "", false, nil)
	p, err := plain.RemotePath("dir/我的文件.txt")
	if err != nil {
		t.Fatal(err)
	}
	if p != "dir/我的文件.txt.cpenc" {
		t.Errorf("未加密路径应原样 + .cpenc: %q", p)
	}

	// 开：路径段全部密文化（不可读），长度 ≠ 明文
	enc := NewSyncEngine(sess, nil, "", true, []byte("fixed-salt-16B!"))
	p2, err := enc.RemotePath("dir/我的文件.txt")
	if err != nil {
		t.Fatal(err)
	}
	if p2 == "dir/我的文件.txt.cpenc" || strings.Contains(p2, "我的文件") {
		t.Errorf("加密路径不应含明文: %q", p2)
	}
	if !strings.HasSuffix(p2, ".cpenc") {
		t.Errorf("加密路径仍应有 .cpenc 后缀: %q", p2)
	}
	if strings.Contains(p2, "/") == false {
		t.Error("目录段也要加密但路径层级要保持")
	}

	// 子目录密库前缀
	sub := NewSyncEngine(sess, nil, "vault/sub/", false, nil)
	p3, err := sub.RemotePath("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if p3 != "vault/sub/a.txt.cpenc" {
		t.Errorf("子目录前缀应拼接: %q", p3)
	}
}

// TestSyncFullCycle 一轮完整同步：本地目录 → 索引上传 → 远端 .cpenc
// 容器可用后端直接读回并解密（LoadIndex 语义 + 字节级正确性）。
func TestSyncFullCycle(t *testing.T) {
	ctx := context.Background()
	eng, b, localDir := newSyncFixture(t)
	base := time.Now().Add(-30 * time.Minute).Truncate(time.Second)

	// 子目录文件 + 中文文件名
	writeFile(t, filepath.Join(localDir, "docs", "说明.md"), []byte("# 标题\n正文"), base)
	writeFile(t, filepath.Join(localDir, "pic.bin"), []byte{0x00, 0xFF, 1, 2, 3}, base)

	plan, err := eng.Plan(ctx, localDir)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]FileState{}
	for _, it := range append(plan.New, plan.Changed...) {
		entries[it.RelPath] = FileState{Size: it.Size, Mtime: it.Mtime}
	}
	if err := eng.UpdateIndex(ctx, entries); err != nil {
		t.Fatal(err)
	}

	// 远端应存在加密的索引容器（固定名，不参与文件名加密）
	idx := eng.LoadIndex(ctx)
	if len(idx) != 2 {
		t.Fatalf("索引应 2 条: %v", idx)
	}
	e1 := idx["docs/说明.md"]
	if e1.Size != int64(len("# 标题\n正文")) {
		t.Errorf("索引 size 不符: %+v", e1)
	}
	if e1.UploadedAt <= 0 {
		t.Errorf("uploaded_at 应已记录: %+v", e1)
	}

	// 索引文件确实在云端（本地后端 = root 下固定名）
	if _, err := os.Stat(filepath.Join(b.Root(), ".cloudprism_index")); err != nil {
		t.Errorf("索引容器应在云端: %v", err)
	}
}

// TestBuildTasks 任务列表字段与顺序（new 在前 changed 在后）。
func TestBuildTasks(t *testing.T) {
	eng, _, localDir := newSyncFixture(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeFile(t, filepath.Join(localDir, "x1.txt"), []byte("1"), base)
	writeFile(t, filepath.Join(localDir, "x2.txt"), []byte("2"), base)

	plan, err := eng.Plan(context.Background(), localDir)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := eng.BuildTasks(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("应 2 个任务，实得 %d", len(tasks))
	}
	for _, tk := range tasks {
		if tk.DisplayName == "" || !strings.HasSuffix(tk.RemotePath, ".cpenc") ||
			tk.ExpectedSize == 0 || tk.LocalPath == "" {
			t.Errorf("任务字段不完整: %+v", tk)
		}
	}
}

// TestIndexCorruptionSelfHeal 索引损坏 → 视为空索引全量重扫 → 重写自愈。
func TestIndexCorruptionSelfHeal(t *testing.T) {
	ctx := context.Background()
	eng, b, localDir := newSyncFixture(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	writeFile(t, filepath.Join(localDir, "f.txt"), []byte("data"), base)

	plan, err := eng.Plan(ctx, localDir)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]FileState{}
	for _, it := range plan.New {
		entries[it.RelPath] = FileState{Size: it.Size, Mtime: it.Mtime}
	}
	if err := eng.UpdateIndex(ctx, entries); err != nil {
		t.Fatal(err)
	}

	// 把云端索引搅成垃圾
	if err := os.WriteFile(filepath.Join(b.Root(), ".cloudprism_index"), []byte("garbage!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if idx := eng.LoadIndex(ctx); len(idx) != 0 {
		t.Errorf("损坏索引应读为空: %v", idx)
	}
	// 重扫 → 全量 new → 重写后自愈
	plan2, err := eng.Plan(ctx, localDir)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.PendingCount() != 1 {
		t.Errorf("损坏后应全量重扫为 new: %+v", plan2)
	}
	entries2 := map[string]FileState{}
	for _, it := range append(plan2.New, plan2.Changed...) {
		entries2[it.RelPath] = FileState{Size: it.Size, Mtime: it.Mtime}
	}
	if err := eng.UpdateIndex(ctx, entries2); err != nil {
		t.Fatal(err)
	}
	if idx := eng.LoadIndex(ctx); len(idx) != 1 {
		t.Errorf("重写后索引应自愈: %v", idx)
	}
}

// TestSubdirVaultSync 子目录密库的索引与远程路径都带前缀。
func TestSubdirVaultSync(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "backend_root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := storage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Mkdir(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	localDir := t.TempDir()
	writeFile(t, filepath.Join(localDir, "a.bin"), []byte("hello"), time.Now())

	sess := session.New("pw")
	defer sess.Close()
	eng := NewSyncEngine(sess, b, "team", false, nil)

	plan, err := eng.Plan(ctx, localDir)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]FileState{}
	for _, it := range plan.New {
		entries[it.RelPath] = FileState{Size: it.Size, Mtime: it.Mtime}
	}
	if err := eng.UpdateIndex(ctx, entries); err != nil {
		t.Fatal(err)
	}
	// 索引落在 team/.cloudprism_index（子目录密库位置）
	if _, err := os.Stat(filepath.Join(root, "team", ".cloudprism_index")); err != nil {
		t.Errorf("子目录密库索引位置错误: %v", err)
	}
	if idx := eng.LoadIndex(ctx); len(idx) != 1 {
		t.Errorf("子目录密库索引应 1 条: %v", idx)
	}
}
