package config

import "testing"

// TestIsLoopbackHost 钉死「哪些 host 不算放宽监听范围」。
//
// 调用方用它判定 config.json 的 host 是否试图把监听放大到全网卡：
// 判错的后果是配置文件能单方面扩大暴露面，而令牌闸门因 listen/lan 关闭
// 而未启用。因此回环判定必须从严 —— 不认识的写法一律视为非回环。
func TestIsLoopbackHost(t *testing.T) {
	loopback := []string{
		"", "127.0.0.1", "localhost", "::1", "[::1]",
		"127.0.0.5", "127.1.2.3", "  127.0.0.1  ", "LOCALHOST", "LocalHost",
	}
	for _, h := range loopback {
		if !IsLoopbackHost(h) {
			t.Errorf("IsLoopbackHost(%q) 应为 true", h)
		}
	}

	notLoopback := []string{
		"0.0.0.0", "::", "10.0.0.1", "192.168.1.10", "180.201.1.203",
		"example.com", "bogus", "0.0.0.0 ", "8.8.8.8",
	}
	for _, h := range notLoopback {
		if IsLoopbackHost(h) {
			t.Errorf("IsLoopbackHost(%q) 应为 false（不得放宽监听）", h)
		}
	}
}
