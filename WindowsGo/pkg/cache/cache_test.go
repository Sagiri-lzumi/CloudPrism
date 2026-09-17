package cache

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// 本文件钉死用户明确要求的磁盘布局规则：
//
//	(a) 小于阈值 → 单独文件，不进子文件夹；
//	(b) 大于等于阈值 → 块文件统一收进「以原名命名的子文件夹」；
//	(c) 块命名 = 原名-1 / 原名-2 / …（保持原名）。
//
// 这些规则是需求里最容易被后续改动破坏的部分，故逐条断言路径形态。

// openTest 建一个作用域隔离的临时缓存（chunkMB 即分块大小与阈值）。
func openTest(t *testing.T, chunkMB, limitMB int) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	s, err := Open(Options{Root: root, Scope: "s1", ChunkMB: chunkMB, LimitMB: limitMB})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, filepath.Join(root, "media", "s1")
}

// payload 生成可校验的确定性字节。gen 参与取值，便于发现错位。
func payload(n int, gen byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31) ^ gen
	}
	return b
}

// putWindows 按 256KiB 窗口模拟流式代理的写入节奏。
func putWindows(t *testing.T, s *Store, remote, display string, total int64, data []byte) {
	t.Helper()
	const win = 256 << 10
	for off := int64(0); off < total; off += win {
		end := off + win
		if end > total {
			end = total
		}
		s.Put(remote, display, total, off, data[off:end])
	}
}

// TestThresholdSingleFile 小于阈值：保持单独文件，不产生子文件夹。
func TestThresholdSingleFile(t *testing.T) {
	s, dir := openTest(t, 1, 64)
	const total = 512 << 10 // 0.5 MiB < 1 MiB 阈值
	data := payload(total, 7)

	putWindows(t, s, "A/small.bin", "小文件.mp4", total, data)

	p := filepath.Join(dir, "小文件.mp4")
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		t.Fatalf("期望单独文件 %s 存在，实得 err=%v", p, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "小文件.mp4.part")); !os.IsNotExist(err) {
		t.Fatalf("补齐后不应残留 .part 文件")
	}
	// 全命中且逐字节一致
	got, ok := s.Fetch("A/small.bin", total, 0, total-1)
	if !ok {
		t.Fatal("期望全命中")
	}
	if !bytes.Equal(got, data) {
		t.Fatal("缓存读回的字节与写入不一致")
	}
}

// TestThresholdChunkedLayout 大于等于阈值：原名子文件夹 + 原名-N 分块。
func TestThresholdChunkedLayout(t *testing.T) {
	s, dir := openTest(t, 1, 64)
	const total = 2<<20 + 512<<10 // 2.5 MiB → 3 块（1 + 1 + 0.5 MiB）
	data := payload(total, 11)

	putWindows(t, s, "B/big.mp4", "movie.mp4", total, data)

	sub := filepath.Join(dir, "movie.mp4")
	fi, err := os.Stat(sub)
	if err != nil || !fi.IsDir() {
		t.Fatalf("期望出现以原名命名的子文件夹 %s，实得 err=%v", sub, err)
	}
	for i, want := range []int64{1 << 20, 1 << 20, 512 << 10} {
		p := filepath.Join(sub, "movie.mp4-"+itoa(i+1))
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("期望块文件 %s 存在: %v", p, err)
		}
		if st.Size() != want {
			t.Fatalf("块 %d 长度应为 %d，实得 %d", i+1, want, st.Size())
		}
	}
	// 根目录下不应出现散落的块文件（防「根目录文件过多」）
	if _, err := os.Stat(filepath.Join(dir, "movie.mp4-1")); !os.IsNotExist(err) {
		t.Fatal("块文件不应落在缓存根目录，必须收进原名子文件夹")
	}
	// 全量校验：分块拼接后的字节必须与原始一致，且能按任意区间命中
	got, ok := s.Fetch("B/big.mp4", total, 0, total-1)
	if !ok || !bytes.Equal(got, data) {
		t.Fatal("分块缓存全量读回不一致")
	}
	if seg, ok := s.Fetch("B/big.mp4", total, total-100, total-1); !ok || !bytes.Equal(seg, data[total-100:]) {
		t.Fatal("尾部跨块区间读回不一致")
	}
}

// TestProgressiveFill 渐进填充：未补齐的块按覆盖区间命中，且带 .part 后缀。
func TestProgressiveFill(t *testing.T) {
	s, dir := openTest(t, 1, 64)
	const total = 2 << 20
	data := payload(total, 3)

	// 只写第 1 块的前 256KiB
	s.Put("C/prog.bin", "prog.bin", total, 0, data[:256<<10])

	part := filepath.Join(dir, "prog.bin", "prog.bin-1.part")
	if _, err := os.Stat(part); err != nil {
		t.Fatalf("未补齐的块应名为 .part：%v", err)
	}
	if got, ok := s.Fetch("C/prog.bin", total, 0, 100); !ok || !bytes.Equal(got, data[:101]) {
		t.Fatal("已覆盖区间应命中")
	}
	if _, ok := s.Fetch("C/prog.bin", total, 256<<10, 256<<10+99); ok {
		t.Fatal("未覆盖区间不应命中")
	}
	if _, ok := s.Fetch("C/prog.bin", total, 1<<20, 1<<20+99); ok {
		t.Fatal("未开始下载的第二块不应命中")
	}

	// 补齐第 1 块 → 转正
	s.Put("C/prog.bin", "prog.bin", total, 256<<10, data[256<<10:1<<20])
	if _, err := os.Stat(filepath.Join(dir, "prog.bin", "prog.bin-1")); err != nil {
		t.Fatalf("补齐后应去掉 .part：%v", err)
	}
	if _, ok := s.Fetch("C/prog.bin", total, 0, 1<<20-1); !ok {
		t.Fatal("整块补齐后应全命中")
	}
	if _, ok := s.Fetch("C/prog.bin", total, 1<<20, 1<<20+9); ok {
		t.Fatal("第二块未下载，跨块请求不应命中")
	}
}

// TestMetaPersistAcrossReopen 元信息落盘：重开缓存后覆盖区间仍有效。
func TestMetaPersistAcrossReopen(t *testing.T) {
	root := t.TempDir()
	data := payload(2<<20, 5)

	s, err := Open(Options{Root: root, Scope: "s1", ChunkMB: 1, LimitMB: 64})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	s.Put("D/p.bin", "p.bin", 2<<20, 0, data[:256<<10])
	if err := s.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	s2, err := Open(Options{Root: root, Scope: "s1", ChunkMB: 1, LimitMB: 64})
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	defer s2.Close()
	// 注意区间长度：end=255KiB 时读回 255KiB+1 字节，比较也要用同样的切片
	want := data[:255<<10+1]
	if got, ok := s2.Fetch("D/p.bin", 2<<20, 0, 255<<10); !ok || !bytes.Equal(got, want) {
		t.Fatal("重开后已覆盖区间应仍然命中且字节一致")
	}
}

// TestChunkSizeChangeInvalidates 分块大小变更后旧布局不兼容，必须按未命中处理。
func TestChunkSizeChangeInvalidates(t *testing.T) {
	root := t.TempDir()
	data := payload(2<<20, 9)
	s, _ := Open(Options{Root: root, Scope: "s1", ChunkMB: 1, LimitMB: 64})
	putWindows(t, s, "E/x.bin", "x.bin", 2<<20, data)
	if _, ok := s.Fetch("E/x.bin", 2<<20, 0, 2<<20-1); !ok {
		t.Fatal("前置条件：应已缓存")
	}
	_ = s.Close()

	// 用 2MiB 分块重开：同一文件应按未命中处理（旧块布局无法复用）
	s2, err := Open(Options{Root: root, Scope: "s1", ChunkMB: 2, LimitMB: 64})
	if err != nil {
		t.Fatalf("重开失败: %v", err)
	}
	defer s2.Close()
	if _, ok := s2.Fetch("E/x.bin", 2<<20, 0, 100); ok {
		t.Fatal("分块大小变更后旧缓存必须失效")
	}
	// 重新写入后应形成新的单文件布局（2MiB 恰好等于阈值 → 分块，2 块）
	s2.Put("E/x.bin", "x.bin", 2<<20, 0, data)
	if _, err := os.Stat(filepath.Join(root, "media", "s1", "x.bin", "x.bin-1")); err != nil {
		t.Fatalf("新布局应产生 x.bin/x.bin-1：%v", err)
	}
}

// TestSameNameNoCrosstalk 同名不同路径不得互相串扰（消歧后缀）。
func TestSameNameNoCrosstalk(t *testing.T) {
	s, dir := openTest(t, 1, 64)
	a, b := payload(300<<10, 1), payload(300<<10, 2)

	s.Put("A/pic.jpg", "pic.jpg", int64(len(a)), 0, a)
	s.Put("B/pic.jpg", "pic.jpg", int64(len(b)), 0, b)

	sub, err := os.Stat(filepath.Join(dir, "pic.jpg"))
	if err != nil || sub.IsDir() {
		t.Fatalf("第一个条目应为单独文件：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pic.jpg (2)")); err != nil {
		t.Fatalf("第二个同名条目应拿到消歧名：%v", err)
	}
	gotA, okA := s.Fetch("A/pic.jpg", int64(len(a)), 0, int64(len(a))-1)
	gotB, okB := s.Fetch("B/pic.jpg", int64(len(b)), 0, int64(len(b))-1)
	if !okA || !okB || !bytes.Equal(gotA, a) || !bytes.Equal(gotB, b) {
		t.Fatal("同名文件内容发生串扰")
	}
}

// TestEvictByLimit 超出容量上限时按最近访问淘汰最旧条目。
func TestEvictByLimit(t *testing.T) {
	// 上限 2MB：写两个 1.5MB 条目必然触发淘汰
	s, _ := openTest(t, 1, 2)
	old, fresh := payload(1500<<10, 4), payload(1500<<10, 6)

	s.Put("O/old.bin", "old.bin", int64(len(old)), 0, old)
	s.Put("O/new.bin", "new.bin", int64(len(fresh)), 0, fresh)

	if _, ok := s.Fetch("O/old.bin", int64(len(old)), 0, 10); ok {
		t.Fatal("最旧条目应被淘汰")
	}
	if _, ok := s.Fetch("O/new.bin", int64(len(fresh)), 0, 10); !ok {
		t.Fatal("最新条目应保留")
	}
	if n, _ := s.Stats(); n > 2<<20 {
		t.Fatalf("占用应回落到上限内，实得 %d", n)
	}
}

// TestSanitizeName 名称清洗：非法字符、结尾点空格、保留设备名、超长。
func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		`a<b>c:d"e/f\g|h?i*j`: "a_b_c_d_e_f_g_h_i_j",
		"trailing.  ":         "trailing",
		"CON":                 "_CON",
		"nul.txt":             "_nul.txt",
		"":                    "unnamed",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q，期望 %q", in, got, want)
		}
	}
	long := sanitizeName(string(bytes.Repeat([]byte("字"), 400)))
	if len([]rune(long)) > maxNameRunes {
		t.Fatalf("超长名未截断：%d", len([]rune(long)))
	}
}

// TestScopeIsolation 不同作用域物理隔离（不同密库不共用缓存）。
func TestScopeIsolation(t *testing.T) {
	root := t.TempDir()
	a, err := Open(Options{Root: root, Scope: BuildScope("v1", "local", "C:/x", "", "id1"), ChunkMB: 1})
	if err != nil {
		t.Fatalf("Open a 失败: %v", err)
	}
	defer a.Close()
	b, err := Open(Options{Root: root, Scope: BuildScope("v1", "local", "C:/x", "", "id2"), ChunkMB: 1})
	if err != nil {
		t.Fatalf("Open b 失败: %v", err)
	}
	defer b.Close()

	if a.ScopeDir() == b.ScopeDir() {
		t.Fatal("不同密库 ID 应映射到不同作用域目录")
	}
	data := payload(100<<10, 1)
	a.Put("F/f.bin", "f.bin", int64(len(data)), 0, data)
	if _, ok := b.Fetch("F/f.bin", int64(len(data)), 0, int64(len(data))-1); ok {
		t.Fatal("作用域之间不应互相命中")
	}
}

// TestPurge 清空缓存后作用域目录仍在（标记保持），但内容为空。
func TestPurge(t *testing.T) {
	s, dir := openTest(t, 1, 64)
	data := payload(200<<10, 8)
	s.Put("G/g.bin", "g.bin", int64(len(data)), 0, data)

	if err := s.Purge(); err != nil {
		t.Fatalf("Purge 失败: %v", err)
	}
	if n, c := s.Stats(); n != 0 || c != 0 {
		t.Fatalf("清空后统计应为 0/0，实得 %d/%d", n, c)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("作用域目录应保留：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".meta", markerName)); err != nil {
		t.Fatalf("占用标记应保留：%v", err)
	}
}

// TestRefuseForeignDirectory 作用域目录已有非本程序数据时必须拒绝，绝不误删。
func TestRefuseForeignDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "media", "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "重要文件.txt")
	if err := os.WriteFile(keep, []byte("用户数据"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(Options{Root: root, Scope: "s1", ChunkMB: 1}); err == nil {
		t.Fatal("目录被占用时应返回错误")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("用户数据不得被删除：%v", err)
	}
}

// itoa 是 strconv.Itoa 的小名，避免测试文件引入额外依赖时的命名冲突。
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
