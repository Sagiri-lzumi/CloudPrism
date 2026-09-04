package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDataDirOverride CLOUDPRISM_DATA_DIR 非空时整体重定向。
func TestDataDirOverride(t *testing.T) {
	t.Setenv(DataDirEnv, `C:\custom\portable-data`)
	if got := DataDir(); got != `C:\custom\portable-data` {
		t.Errorf("override 后应整体重定向，实得 %q", got)
	}
	if got := ConfigFile(); got != filepath.Join(`C:\custom\portable-data`, "cloudprism.ini") {
		t.Errorf("ConfigFile 应跟随 override，实得 %q", got)
	}
	if got := SettingsFile(); got != filepath.Join(`C:\custom\portable-data`, "cloudprism_settings.json") {
		t.Errorf("SettingsFile 应跟随 override，实得 %q", got)
	}
	if got := BaiduCredentialFile(); got != filepath.Join(`C:\custom\portable-data`, "baidu.json") {
		t.Errorf("BaiduCredentialFile 应跟随 override，实得 %q", got)
	}
}

// TestDataDirDefault 未设置环境变量时回落到 exe 目录旁的 data/。
func TestDataDirDefault(t *testing.T) {
	t.Setenv(DataDirEnv, "")
	if !strings.HasSuffix(DataDir(), filepath.Join(string(filepath.Separator), "data")) {
		t.Errorf("默认数据目录应在 exe 旁的 data/，实得 %q", DataDir())
	}
	// exe 目录本身存在且是绝对路径
	if !filepath.IsAbs(AppDir()) {
		t.Errorf("AppDir 应为绝对路径，实得 %q", AppDir())
	}
}

// TestTempDirCreate TempDir create=true 建目录、false 不建。
func TestTempDirCreate(t *testing.T) {
	root := t.TempDir()
	t.Setenv(DataDirEnv, root)

	d, ok := TempDir(false)
	if !ok || d != filepath.Join(root, "tmp") {
		t.Fatalf("TempDir(false) 应返回路径且 ok，实得 %q %v", d, ok)
	}
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Errorf("create=false 不应创建目录，实得 err=%v", err)
	}

	d2, ok := TempDir(true)
	if !ok {
		t.Fatal("TempDir(true) 应 ok")
	}
	st, err := os.Stat(d2)
	if err != nil || !st.IsDir() {
		t.Errorf("create=true 应建出目录，实得 %v", err)
	}
}

// TestEnsureDataDirIdempotent 幂等且路径正确。
func TestEnsureDataDirIdempotent(t *testing.T) {
	root := t.TempDir()
	t.Setenv(DataDirEnv, root)
	EnsureDataDir()
	EnsureDataDir() // 二次调用不报错
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		t.Errorf("数据目录应存在，实得 %v", err)
	}
}

// TestAllFilesUnderDataDir 各文件路径都落在数据目录内（防路径漂移回归）。
func TestAllFilesUnderDataDir(t *testing.T) {
	t.Setenv(DataDirEnv, t.TempDir())
	dir := DataDir()
	for _, p := range []string{ConfigFile(), SettingsFile(), BaiduCredentialFile()} {
		if filepath.Dir(p) != dir {
			t.Errorf("路径 %q 应位于数据目录 %q 下", p, dir)
		}
	}
}
