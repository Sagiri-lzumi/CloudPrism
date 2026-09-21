//go:build windows

package win

import (
	"os"
	"path/filepath"
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
	res, err := ListLocalDirs("")
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

	res, err := ListLocalDirs(root)
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
	if _, err := ListLocalDirs(filepath.Join(root, "note.txt")); err == nil {
		t.Error("对文件调用 ListLocalDirs 应报错")
	}
	// 不存在的路径同理
	if _, err := ListLocalDirs(filepath.Join(root, "nope")); err == nil {
		t.Error("对不存在的路径调用 ListLocalDirs 应报错")
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
