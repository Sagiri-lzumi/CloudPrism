package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeCfg 把内容写进临时 config.json 并返回路径。
func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("写测试配置失败：%v", err)
	}
	return p
}

// TestLoadMissingFileUsesDefaults 文件不存在是正常情况：全默认且无告警。
func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, problems := Load(filepath.Join(t.TempDir(), "nope.json"))
	if cfg != Default() {
		t.Errorf("缺文件应得默认配置，实得 %+v", cfg)
	}
	if len(problems) != 0 {
		t.Errorf("缺文件不算异常，不应有告警，实得 %v", problems)
	}
}

// TestLoadEmptyPathUsesDefaults 空路径即无配置。
func TestLoadEmptyPathUsesDefaults(t *testing.T) {
	cfg, problems := Load("   ")
	if cfg != Default() || len(problems) != 0 {
		t.Errorf("空路径应得默认配置且无告警，实得 %+v %v", cfg, problems)
	}
}

// TestLoadOverrides 正常覆盖各项。
func TestLoadOverrides(t *testing.T) {
	p := writeCfg(t, `{"port": 8080, "port_range": 3}`)
	cfg, problems := Load(p)
	want := Config{Port: 8080, PortRange: 3}
	if cfg != want {
		t.Errorf("实得 %+v，期望 %+v", cfg, want)
	}
	if len(problems) != 0 {
		t.Errorf("合法配置不应告警，实得 %v", problems)
	}
}

// TestLoadPartialKeepsDefaults 只写一项时其余保持默认。
func TestLoadPartialKeepsDefaults(t *testing.T) {
	cfg, problems := Load(writeCfg(t, `{"port": 9000}`))
	if cfg.Port != 9000 {
		t.Errorf("port 应为 9000，实得 %d", cfg.Port)
	}
	if cfg.PortRange != DefaultPortRange {
		t.Errorf("未写的项应保持默认，实得 %+v", cfg)
	}
	if len(problems) != 0 {
		t.Errorf("不应告警，实得 %v", problems)
	}
}

// TestLoadToleratesUTF8BOM 带 BOM 的 config.json 必须正常解析。
//
// 回归测试：Windows 记事本另存 UTF-8 默认带 BOM，而 encoding/json 遇到
// BOM 会报 "invalid character '\ufeff'"。若不剥掉 BOM，用户用记事本把
// 端口从 7840 改成 8080 会静默失效（回退默认值），几乎无从自己排查。
func TestLoadToleratesUTF8BOM(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	body := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"port": 8080}`)...)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatalf("写测试配置失败：%v", err)
	}
	cfg, problems := Load(p)
	if cfg.Port != 8080 {
		t.Errorf("带 BOM 也应读出 port=8080，实得 %d（problems=%v）", cfg.Port, problems)
	}
	if len(problems) != 0 {
		t.Errorf("带 BOM 不算异常，不应告警，实得 %v", problems)
	}
}

// TestLoadToleratesCRLF 记事本保存的 CRLF 行尾同样必须正常解析。
func TestLoadToleratesCRLF(t *testing.T) {
	cfg, problems := Load(writeCfg(t, "{\r\n  \"port\": 8081,\r\n  \"port_range\": 2\r\n}\r\n"))
	if cfg.Port != 8081 || cfg.PortRange != 2 {
		t.Errorf("CRLF 配置应正常解析，实得 %+v", cfg)
	}
	if len(problems) != 0 {
		t.Errorf("不应告警，实得 %v", problems)
	}
}

// TestLoadIgnoresRemovedHostKey 历史版本的 config.json 含 host 键；
// 该字段从未生效、已删除，残留键必须静默忽略而不是报错。
func TestLoadIgnoresRemovedHostKey(t *testing.T) {
	cfg, problems := Load(writeCfg(t, `{"port": 8080, "host": "0.0.0.0"}`))
	if cfg.Port != 8080 {
		t.Errorf("port 应为 8080，实得 %d", cfg.Port)
	}
	if len(problems) != 0 {
		t.Errorf("残留的 host 键不应产生告警，实得 %v", problems)
	}
}

// TestLoadMalformedFallsBack 坏 JSON 回退默认值且报告原因（绝不阻塞启动）。
func TestLoadMalformedFallsBack(t *testing.T) {
	cfg, problems := Load(writeCfg(t, `{"port": `))
	if cfg != Default() {
		t.Errorf("坏 JSON 应回退默认配置，实得 %+v", cfg)
	}
	if len(problems) == 0 {
		t.Error("坏 JSON 必须报出原因，否则用户无从排查")
	}
}

// TestLoadInvalidValuesFallBack 非法值逐项回退并各自报告。
func TestLoadInvalidValuesFallBack(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"port 为 0", `{"port": 0}`},
		{"port 超上限", `{"port": 70000}`},
		{"port 为负", `{"port": -1}`},
		{"port_range 为 0", `{"port_range": 0}`},
		{"port_range 为负", `{"port_range": -5}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, problems := Load(writeCfg(t, c.body))
			if cfg != Default() {
				t.Errorf("非法值应回退默认，实得 %+v", cfg)
			}
			if len(problems) == 0 {
				t.Error("非法值必须报出原因")
			}
		})
	}
}

// TestLoadPortRangeClamped 超大 port_range 截断到上限，不无限扫端口。
func TestLoadPortRangeClamped(t *testing.T) {
	cfg, problems := Load(writeCfg(t, `{"port": 100, "port_range": 99999}`))
	if cfg.PortRange != maxPortRange {
		t.Errorf("port_range 应截断为 %d，实得 %d", maxPortRange, cfg.PortRange)
	}
	if len(problems) == 0 {
		t.Error("截断应报告")
	}
}

// TestLoadPortRangeClippedAt65535 起始端口靠后时顺延范围收窄，不越界。
func TestLoadPortRangeClippedAt65535(t *testing.T) {
	cfg, problems := Load(writeCfg(t, `{"port": 65530, "port_range": 100}`))
	if cfg.PortRange != 6 {
		t.Errorf("应收窄为 6（65530..65535），实得 %d", cfg.PortRange)
	}
	if len(problems) == 0 {
		t.Error("收窄应报告")
	}
	for _, p := range cfg.Ports() {
		if p > 65535 {
			t.Errorf("候选端口 %d 越界", p)
		}
	}
}

// TestPorts 顺序与范围正确（探测与监听共用，顺序错会漏实例）。
func TestPorts(t *testing.T) {
	cfg := Config{Port: 8000, PortRange: 3}
	want := []int{8000, 8001, 8002}
	if got := cfg.Ports(); !reflect.DeepEqual(got, want) {
		t.Errorf("Ports 实得 %v，期望 %v", got, want)
	}
}

// TestPortsGuardsZeroRange PortRange 未初始化时至少给出一个端口。
func TestPortsGuardsZeroRange(t *testing.T) {
	cfg := Config{Port: 8000}
	if got := cfg.Ports(); !reflect.DeepEqual(got, []int{8000}) {
		t.Errorf("零 PortRange 应退化为单端口，实得 %v", got)
	}
}

// TestDefaultPortsUnchanged 默认值必须仍是历史的 7840..7849：
// 改这个范围会让历史用户的默认监听位置漂移，非主实例也更难找到已有实例。
func TestDefaultPortsUnchanged(t *testing.T) {
	ports := Default().Ports()
	if len(ports) != 10 || ports[0] != 7840 || ports[9] != 7849 {
		t.Errorf("默认候选端口应仍为 7840..7849，实得 %v", ports)
	}
}
