package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseRealQSettingsSample 解析「PySide6 QSettings 实测产出」的完整样本。
//
// 样本由 .venv 里的 PySide6 真实写盘一次后原样摘录（键/组/转义/中文值/
// CRLF/组间空行都是 Qt 的真实形态），保证解析器是按实证而非猜测实现。
const qsettingsRealSample = "[appearance]\r\n" +
	"theme_index=2\r\n" +
	"font_size=16\r\n" +
	"\r\n" +
	"[cache]\r\n" +
	"limit_mb=512\r\n" +
	"\r\n" +
	"[transfer]\r\n" +
	"pending=\"[{\\\"local\\\": \\\"a\\\\\\\\b/c.bin\\\", \\\"name\\\": \\\"c.bin\\\"}]\"\r\n" +
	"\r\n" +
	"[conn]\r\n" +
	"webdav_url=https://dav.example.com/远程 路径/\r\n" +
	"\r\n" +
	"[security]\r\n" +
	"auto_lock_index=1\r\n"

func TestParseRealQSettingsSample(t *testing.T) {
	kv, err := parseQSettingsINI(strings.NewReader(qsettingsRealSample))
	if err != nil {
		t.Fatal(err)
	}

	checks := map[string]string{
		"appearance/theme_index":   "2",
		"appearance/font_size":     "16",
		"cache/limit_mb":           "512",
		"transfer/pending":         `[{"local": "a\\b/c.bin", "name": "c.bin"}]`,
		"conn/webdav_url":          "https://dav.example.com/远程 路径/",
		"security/auto_lock_index": "1",
	}
	for key, want := range checks {
		got, ok := kv[key]
		if !ok {
			t.Errorf("键 %q 未解析出", key)
			continue
		}
		if got != want {
			t.Errorf("键 %q 解析不符:\n  want %q\n  got  %q", key, want, got)
		}
	}
	if len(kv) != len(checks) {
		t.Errorf("应恰好 %d 个键，实得 %d: %v", len(checks), len(kv), kv)
	}
}

// TestParseEncodedGroupKey %UXXXX 组/键名解码（防御路径）。
func TestParseEncodedGroupKey(t *testing.T) {
	kv, err := parseQSettingsINI(strings.NewReader(
		"[%U4E2D%U6587%U952E]\r\n" + // 中文键
			"%U542B%U7A7A%U683C=\u542b\u7279\u6b8a\u5b57\u7b26#%\u7684\u503c\r\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := kv["中文键/含空格"]; got != "含特殊字符#%的值" {
		t.Errorf("编码键/原样值解析不符: %q", got)
	}
}

// TestParseNoiseLines 注释/空行/坏行跳过，LF 行尾也能解析。
func TestParseNoiseLines(t *testing.T) {
	kv, err := parseQSettingsINI(strings.NewReader(
		"; 注释行\n# 另一条\n\n[appearance]\ntheme_index=1\n=no-key\n[broken\nfoo\n[a]\nb=2\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	if kv["appearance/theme_index"] != "1" || kv["a/b"] != "2" {
		t.Errorf("正常键应解析出，实得 %v", kv)
	}
	if len(kv) != 2 {
		t.Errorf("噪声行应被跳过，实得 %v", kv)
	}
}

// TestDecodeEscapedValues 引号值与各类转义解码。
func TestDecodeEscapedValues(t *testing.T) {
	cases := []struct{ in, want string }{
		{`"plain"`, "plain"},
		{`"a\"b"`, `a"b`},
		{`"a\\b"`, `a\b`},
		{`"line\nbreak"`, "line\nbreak"},
		{`"tab\there"`, "tab\there"},
		{`512`, "512"},             // int 无引号原样
		{`C:\no\esc`, `C:\no\esc`}, // 无引号值不反转义
	}
	for _, c := range cases {
		if got := decodeQSettingsValue(c.in); got != c.want {
			t.Errorf("decode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDecodeKeys %UXXXX 解码与普通键直通。
func TestDecodeKeys(t *testing.T) {
	if got := decodeQSettingsKey("%U4E2D%U6587%U952E"); got != "中文键" {
		t.Errorf("中文键解码失败: %q", got)
	}
	if got := decodeQSettingsKey("plain_key"); got != "plain_key" {
		t.Errorf("普通键应直通: %q", got)
	}
	// 非 hex 的 %U 尾巴按字面保留
	if got := decodeQSettingsKey("a%UZZZZb"); got != "a%UZZZZb" {
		t.Errorf("非法编码应字面保留: %q", got)
	}
}

// TestImportLegacyLifecycle 完整导入生命周期：
// 无 json 有 ini → 导入；ini 被改名；json 键值正确；再跑一次跳过。
func TestImportLegacyLifecycle(t *testing.T) {
	dir := t.TempDir()
	iniPath := filepath.Join(dir, "cloudprism.ini")
	jsonPath := filepath.Join(dir, "cloudprism_settings.json")
	if err := os.WriteFile(iniPath, []byte(qsettingsRealSample), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := ImportLegacy(iniPath, jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 {
		t.Fatalf("应导入 6 个键，实得 %d", n)
	}

	// INI 已改名，JSON 已生成且内容为扁平键值
	if _, err := os.Stat(iniPath); !os.IsNotExist(err) {
		t.Error("INI 应被改名（已处理标记）")
	}
	if _, err := os.Stat(iniPath + ".imported"); err != nil {
		t.Error("应存在 .imported 改名文件")
	}
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("JSON 文件非法: %v", err)
	}
	if m["conn/webdav_url"] != "https://dav.example.com/远程 路径/" {
		t.Errorf("导入键值不符: %v", m)
	}

	// 幂等：再跑一次直接跳过（INI 已不存在也回 0）
	n2, err := ImportLegacy(iniPath, jsonPath)
	if err != nil || n2 != 0 {
		t.Errorf("json 已存在应跳过: n=%d err=%v", n2, err)
	}
}

// TestImportLegacyNoIni / NoJson 各种缺省组合都不报错。
func TestImportLegacyNoIni(t *testing.T) {
	dir := t.TempDir()
	n, err := ImportLegacy(filepath.Join(dir, "nope.ini"), filepath.Join(dir, "s.json"))
	if err != nil || n != 0 {
		t.Errorf("无 INI 应返回 0 且无错: n=%d err=%v", n, err)
	}

	// 只有 INI 没有 JSON 是正常导入路径（上面已测）；这里测 json 先存在
	iniPath := filepath.Join(dir, "old.ini")
	if err := os.WriteFile(iniPath, []byte("[a]\nb=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(dir, "s.json")
	if err := os.WriteFile(jsonPath, []byte(`{"x":"y"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err = ImportLegacy(iniPath, jsonPath)
	if err != nil || n != 0 {
		t.Errorf("json 先存在应跳过: n=%d err=%v", n, err)
	}
	if _, err := os.Stat(iniPath); err != nil {
		t.Error("跳过导入时 INI 不应被动过")
	}
}
