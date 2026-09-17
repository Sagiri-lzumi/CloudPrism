package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件钉死缓存位置的**红线**：缓存根永远落在程序目录旁，绝不主动
// 落到系统盘的用户目录（%TEMP% / %APPDATA% / Program Files 等）。
//
// 背景：历史实现（Python 端与 Go 端早期版本）在未配置缓存目录时把
// 缩略图缓存写进 %TEMP%\cloudprism_cache，会持续吃满系统盘。这些断言
// 就是为了防止该行为以任何形式回归。

// TestDefaultCacheDirUnderDataDir 默认缓存根必须位于数据目录之下（便携）。
func TestDefaultCacheDirUnderDataDir(t *testing.T) {
	override := t.TempDir()
	t.Setenv(DataDirEnv, override)

	got := DefaultCacheDir()
	want := filepath.Join(override, "cache")
	if got != want {
		t.Fatalf("DefaultCacheDir() = %q，期望 %q", got, want)
	}
	// 默认值本身不得引用系统临时目录的散落路径
	if strings.Contains(strings.ToLower(got), "cloudprism_cache") {
		t.Fatalf("默认缓存根不应使用 %%%%TEMP%%%%\\cloudprism_cache 形态：%q", got)
	}
}

// TestEnsureCacheDir 创建成功返回 ok=true，且目录真实存在。
func TestEnsureCacheDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	got, ok := EnsureCacheDir(dir)
	if !ok {
		t.Fatal("EnsureCacheDir 应成功")
	}
	if got != dir {
		t.Fatalf("EnsureCacheDir 应返回原路径，实得 %q", got)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("目录应已创建：%s（err=%v）", dir, err)
	}
}

// TestSubCacheDirs 缩略图与媒体缓存共用同一根目录下的固定子目录。
func TestSubCacheDirs(t *testing.T) {
	root := filepath.Join("D:", "cp", "cache")
	if got, want := ThumbCacheDir(root), filepath.Join(root, "thumbs"); got != want {
		t.Errorf("ThumbCacheDir = %q，期望 %q", got, want)
	}
	if got, want := MediaCacheDir(root), filepath.Join(root, "media"); got != want {
		t.Errorf("MediaCacheDir = %q，期望 %q", got, want)
	}
}

// TestForbiddenCacheDir 系统位置必须被拒绝，普通位置必须放行。
func TestForbiddenCacheDir(t *testing.T) {
	tmp := t.TempDir()
	appdata := filepath.Join(tmp, "AppData", "Roaming")
	t.Setenv("TEMP", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("APPDATA", appdata)
	t.Setenv("LOCALAPPDATA", filepath.Join(tmp, "AppData", "Local"))
	t.Setenv("ProgramFiles", filepath.Join(tmp, "Program Files"))
	t.Setenv("ProgramFiles(x86)", filepath.Join(tmp, "Program Files (x86)"))
	t.Setenv("SystemRoot", filepath.Join(tmp, "Windows"))
	t.Setenv("windir", filepath.Join(tmp, "Windows"))
	t.Setenv("ProgramData", filepath.Join(tmp, "ProgramData"))
	t.Setenv("USERPROFILE", filepath.Join(tmp, "Users", "me"))

	cases := []struct {
		name    string
		dir     string
		blocked bool
	}{
		{"系统临时目录", filepath.Join(tmp, "cloudprism_cache"), false},
		{"临时目录子路径", filepath.Join(tmp, "a", "b", "cache"), false},
		{"漫游配置目录", filepath.Join(appdata, "CloudPrism"), false},
		{"系统目录", filepath.Join(tmp, "Windows", "Temp"), false},
		{"程序目录", filepath.Join(tmp, "Program Files", "CloudPrism"), false},
		{"用户目录", filepath.Join(tmp, "Users", "me", "cache"), false},
		{"独立盘位（放行）", filepath.Join("D:", "CloudPrismCache"), true},
	}
	for _, c := range cases {
		got := ForbiddenCacheDir(c.dir)
		if c.blocked && got != "" {
			t.Fatalf("%s 应被拒绝，实得原因 %q", c.name, got)
		}
		if !c.blocked && got == "" {
			t.Fatalf("%s 应被放行（%s）", c.name, c.dir)
		}
	}
	if why := ForbiddenCacheDir(""); why != "" {
		t.Fatalf("空路径不应报错，实得 %q", why)
	}
}

// TestWithin 前缀相似不等于包含（C:\TempX 不应被当作 C:\Temp 之下）。
func TestWithin(t *testing.T) {
	base := filepath.Join("C:", "Temp")
	if within(base, filepath.Join("C:", "Temp")) != true {
		t.Fatal("同路径应判定为包含")
	}
	if within(base, filepath.Join("C:", "TempX")) {
		t.Fatal("相似前缀不应判定为包含")
	}
	if within(base, filepath.Join("D:", "Temp", "x")) {
		t.Fatal("不同盘符不应判定为包含")
	}
}
