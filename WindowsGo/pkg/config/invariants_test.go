package config

import "testing"

// TestPortsInvariants 穷举校验 Ports() 的不变量：非空、递增、全部落在
// 1..65535、首元素等于 Port、长度不超过 PortRange（越界时被收窄）。
//
// 这是单实例探测与实际监听共用的列表，任何一项被破坏都会直接表现为
// 「多开」或「绑不上端口」，且很难从现象反推，故用穷举钉死。
func TestPortsInvariants(t *testing.T) {
	ports := []int{1, 80, 7840, 65530, 65534, 65535}
	ranges := []int{1, 2, 10, 256}
	for _, p := range ports {
		for _, r := range ranges {
			cfg := Config{Port: p, PortRange: r}
			got := cfg.Ports()
			if len(got) == 0 {
				t.Fatalf("Port=%d Range=%d: Ports() 为空", p, r)
			}
			if got[0] != p {
				t.Errorf("Port=%d Range=%d: 首元素应为 %d，实得 %d", p, r, p, got[0])
			}
			for i, v := range got {
				if v < 1 || v > 65535 {
					t.Errorf("Port=%d Range=%d: 端口 %d 越界", p, r, v)
				}
				if i > 0 && v != got[i-1]+1 {
					t.Errorf("Port=%d Range=%d: 非连续 %v", p, r, got)
				}
			}
			if len(got) > r {
				t.Errorf("Port=%d Range=%d: 长度 %d 超过 Range", p, r, len(got))
			}
		}
	}
}
