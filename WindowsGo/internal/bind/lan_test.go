package bind

import (
	"net"
	"testing"
)

// TestLooksVirtual 覆盖真实机器上出现过的网卡名形态，以及必须**不**误判的普通网卡。
func TestLooksVirtual(t *testing.T) {
	virtual := []string{
		"Mihomo",                             // 代理隧道（Meta Tunnel / Clash 内核）
		"Clash",                              // 代理隧道
		"VMware Network Adapter VMnet1",      // VMware 仅主机网络
		"VMware Network Adapter VMnet8",      // VMware NAT
		"vEthernet (Default Switch)",         // Hyper-V 虚拟交换机
		"VirtualBox Host-Only Network",       // VirtualBox
		"Tailscale",                          // 组网隧道
		"WireGuard Tunnel",                   // VPN
		"Loopback Pseudo-Interface 1",        // 回环伪接口
		"vEthernet (WSL (Hyper-V firewall))", // WSL
	}
	for _, name := range virtual {
		if !looksVirtual(&net.Interface{Name: name}) {
			t.Errorf("%q 应判为虚拟网卡/隧道", name)
		}
	}

	// 这些是真实物理网卡名（含中文名）：误判会让用户看到「虚拟网卡」标记
	// 而怀疑自己的网卡有问题，因此必须为 false。
	real := []string{
		"以太网",
		"WLAN",
		"Wi-Fi",
		"Ethernet",
		"Realtek PCIe GbE Family Controller",
		"Intel(R) Wi-Fi 6 AX201 160MHz",
		"en0",
	}
	for _, name := range real {
		if looksVirtual(&net.Interface{Name: name}) {
			t.Errorf("%q 是真实网卡，不应判为虚拟网卡", name)
		}
	}
}

// TestLanIPv4Addrs 验证枚举结果可用且排序正确。
//
// 排序不是装饰：UI 把第一行当作「最可能连得通」的地址推荐给用户，
// 排错顺序就会让用户复制到一条连不上的链接（虚拟网卡地址）。
func TestLanIPv4Addrs(t *testing.T) {
	addrs := lanIPv4Addrs()
	if len(addrs) == 0 {
		t.Skip("本机没有可用的局域网 IPv4 地址，跳过")
	}

	dup := map[string]bool{}
	lastReal, firstVirtual := -1, -1
	for i, a := range addrs {
		ip := net.ParseIP(a.IP)
		if ip == nil {
			t.Errorf("枚举出非法 IP: %q", a.IP)
			continue
		}
		if ip.IsLoopback() {
			t.Errorf("不应枚举回环地址: %s", a.IP)
		}
		if ip.IsLinkLocalUnicast() {
			t.Errorf("不应枚举链路本地地址（169.254/16）: %s", a.IP)
		}
		if a.Iface == "" {
			t.Errorf("%s 缺少网卡名（UI 要用它帮用户挑地址）", a.IP)
		}
		if dup[a.IP] {
			t.Errorf("地址重复: %s", a.IP)
		}
		dup[a.IP] = true

		if a.Virtual {
			if firstVirtual < 0 {
				firstVirtual = i
			}
		} else {
			lastReal = i
		}
	}
	// 真实网卡必须整体排在虚拟网卡之前。
	if firstVirtual >= 0 && lastReal > firstVirtual {
		t.Errorf("真实网卡地址应排在虚拟网卡之前：最后一个真实地址在下标 %d，第一个虚拟地址在下标 %d",
			lastReal, firstVirtual)
	}
}
