//go:build windows

package win

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 校验：非法目录名必须在**建之前**被挡下（否则 Windows 会静默改名或建出
// 意料之外的目录，用户看到的名字与输入的不一致）。
func TestValidateLocalDirName(t *testing.T) {
	ok := []string{"CloudPrism", "云盘 数据", "a-b_c.d", "1234"}
	for _, n := range ok {
		if err := validateLocalDirName(n); err != nil {
			t.Errorf("合法名 %q 被拒: %v", n, err)
		}
	}
	bad := []string{"", ".", "..", "a/b", `a\b`, "a:b", "a*b", "a?b", `a"b`, "a<b", "a>b", "a|b", "name.", "name "}
	for _, n := range bad {
		if err := validateLocalDirName(n); err == nil {
			t.Errorf("非法名 %q 应被拒", n)
		}
	}
	// NUL 只能以字面量拼进来（字符串里不能直接写 \x00 之外的形式）
	if err := validateLocalDirName("a\x00b"); err == nil {
		t.Error("含 NUL 的名字应被拒")
	}
}

// 校验：上一级计算必须停在「最上层」，不能让界面把用户带到盘根之上。
func TestParentLocalDir(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\`, ""},
		{`C:\Users`, `C:\`},
		{`C:\Users\me\Documents`, `C:\Users\me`},
		{`\\NAS\share`, ""},                  // UNC 共享根没有合法上级（再往上是 \\NAS，不是路径）
		{`\\NAS\share\`, ""},                 // 尾反斜杠形态同属共享根
		{`\\NAS\share\docs`, `\\NAS\share\`}, // Dir 对 UNC 子目录返回带尾反斜杠的共享根
	}
	for _, c := range cases {
		if got := parentLocalDir(c.in); got != c.want {
			t.Errorf("parentLocalDir(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 根视图：空路径应返回盘符列表，且路径为空（前端据此切「此电脑」视图）。
func TestListLocalDirsRoot(t *testing.T) {
	res, err := ListLocalDirs("", false)
	if err != nil {
		t.Fatalf("列盘符失败: %v", err)
	}
	if res.Path != "" || res.Parent != "" {
		t.Errorf("根视图的 Path/Parent 应为空，got %q/%q", res.Path, res.Parent)
	}
	// Windows 上至少存在系统盘；盘符格式必须是 `X:\`。
	if len(res.Drives) == 0 {
		t.Fatal("盘符列表为空")
	}
	for _, d := range res.Drives {
		if len(d.Path) != 3 || d.Path[1] != ':' || d.Path[2] != '\\' {
			t.Errorf("盘符格式异常: %q", d.Path)
		}
		if d.Kind == "" {
			t.Errorf("盘符 %s 缺少 kind", d.Path)
		}
	}
}

// 列目录：只列目录、不列文件；结果按名称排序；路径与上一级正确。
func TestListLocalDirsEntries(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"beta", "Alpha", "gamma"} {
		if err := os.Mkdir(filepath.Join(root, n), 0o755); err != nil {
			t.Fatalf("准备目录失败: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	res, err := ListLocalDirs(root, false)
	if err != nil {
		t.Fatalf("列目录失败: %v", err)
	}
	if res.Path != filepath.Clean(root) {
		t.Errorf("Path = %q，期望 %q", res.Path, filepath.Clean(root))
	}
	if res.Parent == "" {
		t.Error("Parent 不应为空（临时目录必有上级）")
	}
	names := make([]string, 0, len(res.Dirs))
	for _, d := range res.Dirs {
		names = append(names, d.Name)
	}
	if len(names) != 3 {
		t.Fatalf("应只列出 3 个目录（文件不计），got %v", names)
	}
	// 排序按大小写不敏感：Alpha/beta/gamma
	if names[0] != "Alpha" || names[1] != "beta" || names[2] != "gamma" {
		t.Errorf("目录未按名称排序: %v", names)
	}
	for _, d := range res.Dirs {
		if !strings.HasPrefix(d.Path, res.Path) {
			t.Errorf("子目录路径 %q 不在 %q 之下", d.Path, res.Path)
		}
	}

	// 文件路径（非目录）必须被明确拒绝，而不是当成空目录列出来
	if _, err := ListLocalDirs(filepath.Join(root, "note.txt"), false); err == nil {
		t.Error("对文件调用 ListLocalDirs 应报错")
	}
	// 不存在的路径同理
	if _, err := ListLocalDirs(filepath.Join(root, "nope"), false); err == nil {
		t.Error("对不存在的路径调用 ListLocalDirs 应报错")
	}
}

// 列文件模式（「上传文件」选择器用）：文件带大小、与目录一起按名排序。
func TestListLocalDirsWithFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	// 大小刻意不同且不为 0，确保返回的是真实 stat 结果而不是占位值
	sizes := map[string]int{"a.txt": 3, "b.bin": 17}
	for n, sz := range sizes {
		if err := os.WriteFile(filepath.Join(root, n), make([]byte, sz), 0o644); err != nil {
			t.Fatalf("准备文件失败: %v", err)
		}
	}

	res, err := ListLocalDirs(root, true)
	if err != nil {
		t.Fatalf("列目录（含文件）失败: %v", err)
	}
	if len(res.Dirs) != 1 || res.Dirs[0].Name != "folder" {
		t.Errorf("目录项异常: %+v", res.Dirs)
	}
	if len(res.Files) != 2 {
		t.Fatalf("应列出 2 个文件，got %+v", res.Files)
	}
	// 排序同样是大小写不敏感：a.txt / b.bin
	if res.Files[0].Name != "a.txt" || res.Files[1].Name != "b.bin" {
		t.Errorf("文件未按名称排序: %+v", res.Files)
	}
	for _, f := range res.Files {
		if want := int64(sizes[f.Name]); f.Size != want {
			t.Errorf("文件 %s 大小 = %d，期望 %d", f.Name, f.Size, want)
		}
		if !strings.HasPrefix(f.Path, res.Path) {
			t.Errorf("文件路径 %q 不在 %q 之下", f.Path, res.Path)
		}
	}
}

// 截断预算只按**实际返回的条目**计：同层文件再多，也不能把目录挤掉。
//
// 这是本地选择器最容易被写错的一处 —— 若把「扫描到的条目数」当作已用预算，
// 一个放着 2000+ 文件的目录会让目录模式直接返回 Truncated 并丢掉部分目录，
// 用户就会「明明有子文件夹却看不到」。
func TestListLocalDirsFileCountDoesNotStarveDirs(t *testing.T) {
	// 把上限临时调到 12：真造 2000+ 个文件在 Windows 上要几十秒，
	// 而这里要验的是「预算怎么算」而不是「2000 这个数字」。
	saved := localDirMax
	localDirMax = 12
	defer func() { localDirMax = saved }()

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "keep"), 0o755); err != nil {
		t.Fatalf("准备目录失败: %v", err)
	}
	for i := 0; i < localDirMax+5; i++ {
		if err := os.WriteFile(filepath.Join(root, "f"+strconv.Itoa(i)+".tmp"), nil, 0o644); err != nil {
			t.Fatalf("准备文件失败: %v", err)
		}
	}

	dirsOnly, err := ListLocalDirs(root, false)
	if err != nil {
		t.Fatalf("目录模式失败: %v", err)
	}
	if dirsOnly.Truncated {
		t.Error("目录模式不该被同层文件触发截断")
	}
	if len(dirsOnly.Dirs) != 1 || dirsOnly.Dirs[0].Name != "keep" {
		t.Errorf("目录被同层文件挤掉了: %+v", dirsOnly.Dirs)
	}
	if len(dirsOnly.Files) != 0 {
		t.Errorf("目录模式不该返回文件: %+v", dirsOnly.Files)
	}

	withFiles, err := ListLocalDirs(root, true)
	if err != nil {
		t.Fatalf("文件模式失败: %v", err)
	}
	if !withFiles.Truncated {
		t.Error("文件模式超过 localDirMax 时该置 Truncated")
	}
	if n := len(withFiles.Dirs) + len(withFiles.Files); n != localDirMax {
		t.Errorf("截断后应恰好返回 localDirMax 项，got %d", n)
	}
}

// 新建目录：成功返回绝对路径；同名与非法名都要报错。
func TestCreateLocalDir(t *testing.T) {
	root := t.TempDir()
	created, err := CreateLocalDir(root, "新建 文件夹")
	if err != nil {
		t.Fatalf("新建目录失败: %v", err)
	}
	want := filepath.Join(root, "新建 文件夹")
	if created != want {
		t.Errorf("返回路径 = %q，期望 %q", created, want)
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Fatalf("目录未真正建出: %v", err)
	}
	if _, err := CreateLocalDir(root, "新建 文件夹"); err == nil {
		t.Error("同名目录应报错")
	}
	if _, err := CreateLocalDir(root, "a/b"); err == nil {
		t.Error("含路径分隔符的名字应报错")
	}
	if _, err := CreateLocalDir(root, ".."); err == nil {
		t.Error("「..」应报错")
	}
}
