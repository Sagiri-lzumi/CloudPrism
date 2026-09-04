package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore 在临时目录建一个空 Store。
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

// TestGetSetIntRoundTrip 字符串与整数读写往返 + 默认值回退。
func TestGetSetIntRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)

	if got := s.Get(KeyBackendType, "local"); got != "local" {
		t.Errorf("缺失键应回退默认 local，实得 %q", got)
	}
	if got := s.Int(KeyThemeIndex, 0); got != 0 {
		t.Errorf("缺失键应回退 0，实得 %d", got)
	}

	s.SetInt(KeyThemeIndex, 2)
	s.Set(KeyBackendType, "baidu")
	if got := s.Int(KeyThemeIndex, 0); got != 2 {
		t.Errorf("theme_index 应 2，实得 %d", got)
	}
	if got := s.Get(KeyBackendType, "local"); got != "baidu" {
		t.Errorf("backend_type 应 baidu，实得 %q", got)
	}

	// 类型损坏回退默认：直接塞非数字字符串
	s.Set(KeyThemeIndex, "abc")
	if got := s.Int(KeyThemeIndex, 3); got != 3 {
		t.Errorf("非数字应回退默认 3，实得 %d", got)
	}
}

// TestSyncPersistsAndReload Sync 后重开文件值不丢，JSON 内容为排序键对。
func TestSyncPersistsAndReload(t *testing.T) {
	s, path := newTestStore(t)
	s.SetInt(KeyFontSize, 16)
	s.Set(KeyCachePath, `C:\用户\缓存 目录`)
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}

	// 文件内容可解析为扁平键值 JSON
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("落盘内容应为合法 JSON: %v\n%s", err, raw)
	}
	if m[KeyFontSize] != "16" || m[KeyCachePath] != `C:\用户\缓存 目录` {
		t.Errorf("落盘键值不符: %v", m)
	}

	// 重开（模拟下次启动）
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Int(KeyFontSize, 14); got != 16 {
		t.Errorf("重开后 font_size 应 16，实得 %d", got)
	}
	if got := s2.Get(KeyCachePath, ""); got != `C:\用户\缓存 目录` {
		t.Errorf("重开后 cache_path 丢失: %q", got)
	}
}

// TestOpenCorruptedJSON 损坏 JSON 容错为空存储（不阻塞启动）。
func TestOpenCorruptedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{broken json!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("损坏文件不应报错: %v", err)
	}
	if got := s.Int(KeyThemeIndex, 1); got != 1 {
		t.Errorf("损坏后应全默认，实得 %d", got)
	}
	s.SetInt(KeyThemeIndex, 1) // 能继续写
	if err := s.Sync(); err != nil {
		t.Fatal(err)
	}
}

// TestOpenMissingFile 文件不存在 → 空存储而非错误。
func TestOpenMissingFile(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("文件不存在不应报错: %v", err)
	}
	if len(s.values) != 0 {
		t.Errorf("应为空存储，实得 %v", s.values)
	}
}

// TestJSONListRoundTrip pending 传输与最近密库的 JSON 列表往返、损坏容错。
func TestJSONListRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)

	if got := s.JSONList(KeyPendingTransfers); got != nil {
		t.Errorf("缺失键应返回空，实得 %v", got)
	}

	items := []map[string]any{
		{"local": `C:\a\b.bin`, "remote": "dir/f.bin", "direction": "upload", "size": float64(123)},
		{"local": `D:\x\y.mp4`, "remote": "v.mp4", "direction": "download", "size": float64(456)},
	}
	s.SetJSONList(KeyPendingTransfers, items)
	if got := s.JSONList(KeyPendingTransfers); len(got) != 2 {
		t.Fatalf("往返后应 2 条，实得 %v", got)
	} else if got[0]["local"] != `C:\a\b.bin` || got[1]["remote"] != "v.mp4" {
		t.Errorf("往返内容不符: %v", got)
	}

	// 空列表即清空
	s.SetJSONList(KeyPendingTransfers, nil)
	if got := s.JSONList(KeyPendingTransfers); got != nil {
		t.Errorf("空列表后应返回空，实得 %v", got)
	}

	// 损坏容错
	s.Set(KeyPendingTransfers, "not-json")
	if got := s.JSONList(KeyPendingTransfers); got != nil {
		t.Errorf("损坏应返回空，实得 %v", got)
	}
}

// TestRememberVaultDedupAndOrder 按三元组去重置顶 + 淘汰最旧 + key 生成。
func TestRememberVaultDedupAndOrder(t *testing.T) {
	s, _ := newTestStore(t)
	old := clockNow
	clockNow = func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.Local) }
	defer func() { clockNow = old }()

	base := map[string]any{"backend_type": "webdav", "path": "https://dav.example.com"}
	s.RememberVault(base)
	s.RememberVault(map[string]any{
		"backend_type": "baidu", "path": "百度",
		"vault_path": "/sub/", // 首尾斜杠应剥掉参与归一
	})
	s.RememberVault(base) // 同根密库重复连接：置顶且不新增
	s.RememberVault(map[string]any{
		"backend_type": "webdav", "path": "https://dav.example.com", "vault_path": "sub",
	})

	got := s.JSONList(KeyRecentVaults)
	if len(got) != 3 {
		t.Fatalf("应剩 3 条（根/子目录/baidu），实得 %d: %v", len(got), got)
	}
	if got[0]["vault_path"] != "sub" {
		t.Errorf("最新连接的子目录密库应置顶，实得 %#v", got[0])
	}
	if got[0]["key"] != "webdav|https://dav.example.com|sub" {
		t.Errorf("key 应为三字段归一，实得 %q", got[0]["key"])
	}
	if got[1]["key"] != "webdav|https://dav.example.com|" {
		t.Errorf("根密库 key 的 vault_path 段应为空，实得 %q", got[1]["key"])
	}
	if got[0]["last_used"] != "2026-09-04 12:00" {
		t.Errorf("last_used 应为注入时钟，实得 %v", got[0]["last_used"])
	}

	// 超上限淘汰最旧
	for i := 0; i < MaxRecentVaults+2; i++ {
		s.RememberVault(map[string]any{
			"backend_type": "local", "path": filepath.Join(t.TempDir(), "r", "v"),
			"vault_path": itoa(i),
		})
	}
	got = s.JSONList(KeyRecentVaults)
	if len(got) > MaxRecentVaults {
		t.Errorf("应截断到 %d 条，实得 %d", MaxRecentVaults, len(got))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

// TestForgetVault 删除指定 key 记录。
func TestForgetVault(t *testing.T) {
	s, _ := newTestStore(t)
	s.RememberVault(map[string]any{"backend_type": "local", "path": "C:\\v1"})
	s.RememberVault(map[string]any{"backend_type": "local", "path": "C:\\v2"})

	all := s.JSONList(KeyRecentVaults)
	var target string
	for _, r := range all {
		if recordString(r, "path") == "C:\\v2" {
			target = recordString(r, "key")
		}
	}
	if target == "" {
		t.Fatal("没找到 v2 记录")
	}
	s.ForgetVault(target)
	got := s.JSONList(KeyRecentVaults)
	if len(got) != 1 || recordString(got[0], "path") == "C:\\v2" {
		t.Errorf("v2 应被删除，实得 %v", got)
	}
}
