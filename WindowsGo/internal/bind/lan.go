package bind

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/secret"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/settings"
)

// Lan 是局域网访问档的绑定域：开关、访问令牌、可分享地址。
//
// 职责边界：本域只管「配置与令牌」，不碰监听 —— 绑哪个地址由 main.go 在
// 启动时读取设置决定，鉴权由 internal/web 的访问闸门执行。这样重启前
// 后的语义很清楚：设置是「已保存的意愿」，而当前进程实际是否对局域网
// 监听由 web.Server 单独上报（Status 的 active 字段），避免出现
// 「开关打开了但其实没生效」的展示缺口。
type Lan struct {
	st  *appstate.State
	tok *secret.File
}

// NewLan 构造局域网档绑定；tok 为令牌文件读写器（由装配层注入 DPAPI 保护器）。
func NewLan(st *appstate.State, tok *secret.File) *Lan {
	return &Lan{st: st, tok: tok}
}

// Enabled 报告设置里是否开启了局域网访问。
func (l *Lan) Enabled() bool {
	return l.st.Store().Int(settings.KeyLanEnabled, 0) == 1
}

// SetEnabled 写入局域网访问开关并落盘。
//
// 仅改设置、不热重载监听：绑定的地址在 Listen 时确定，切换需要重启进程。
// 前端必须把这一点显示出来（Status.active 表示当前进程的真实状态）。
func (l *Lan) SetEnabled(on bool) error {
	v := 0
	if on {
		v = 1
	}
	store := l.st.Store()
	store.SetInt(settings.KeyLanEnabled, v)
	return wrapSync(store.Sync())
}

// Token 返回访问令牌；不存在（或文件损坏）时生成并落盘，因此幂等。
func (l *Lan) Token() (string, error) {
	if l.tok == nil {
		return "", errors.New("访问令牌存储未装配")
	}
	if v, ok := l.tok.Load(); ok && v != "" {
		return v, nil
	}
	return l.Rotate()
}

// Rotate 重新生成访问令牌并落盘，返回新令牌。
//
// 副作用：旧令牌立即失效 —— 所有已授权的远端设备下次请求会被拒，
// 需要重新用新链接进入。这是「踢掉所有设备」的预期语义。
func (l *Lan) Rotate() (string, error) {
	if l.tok == nil {
		return "", errors.New("访问令牌存储未装配")
	}
	tok, err := secret.NewToken()
	if err != nil {
		return "", wrapSync(fmt.Errorf("生成访问令牌失败: %w", err))
	}
	if err := l.tok.Save(tok); err != nil {
		return "", wrapSync(fmt.Errorf("保存访问令牌失败: %w", err))
	}
	return tok, nil
}

// LanAddr 是一个可用于局域网访问的地址及其来源网卡。
//
// 为什么要带网卡名：带虚拟网卡（VMware/WSL/代理隧道等）的机器上会枚举出
// 多个「看着像局域网」的地址，但手机 / 另一台电脑**连不上**虚拟网卡上的
// 地址。只给一串裸 IP，用户很可能复制第一条却发现连不通 —— 所以必须把
// 来源与虚拟标记一并交给 UI 显示。
type LanAddr struct {
	IP      string `json:"ip"`      // IPv4 地址
	Iface   string `json:"iface"`   // 网卡名称（如「以太网」「WLAN」）
	URL     string `json:"url"`     // 带访问令牌的完整地址（复制即用）
	Virtual bool   `json:"virtual"` // 虚拟网卡/隧道上的地址：手机通常连不上
}

// Status 汇总设置页需要的局域网状态。
//
//	enabled  已保存的开关
//	active   当前进程是否真的对局域网监听（重启前后可能不一致）
//	port     实际监听端口
//	token    访问令牌（只有已授权的调用方拿得到，因此不算新增泄露面）
//	localUrl 本机地址
//	addrs    局域网可分享地址列表（含网卡名与虚拟标记，已内嵌令牌）
//
// port <= 0 表示尚未监听（调用方取不到地址），此时地址字段为空。
func (l *Lan) Status(port int, active bool) map[string]any {
	out := map[string]any{
		"enabled":  l.Enabled(),
		"active":   active,
		"port":     port,
		"token":    "",
		"localUrl": "",
		"addrs":    []LanAddr{},
	}
	if port <= 0 {
		return out
	}

	tok, err := l.Token()
	if err != nil {
		return out // 令牌不可用：不阻断状态查询，其余字段照常返回
	}
	out["token"] = tok
	out["localUrl"] = fmt.Sprintf("http://127.0.0.1:%d", port)
	out["addrs"] = shareAddrs(port, tok)
	return out
}

// ShareURLs 返回带访问令牌的局域网分享地址（启动日志用；port<=0 返回 nil）。
func (l *Lan) ShareURLs(port int) []string {
	if port <= 0 {
		return nil
	}
	tok, err := l.Token()
	if err != nil || tok == "" {
		return nil
	}
	addrs := shareAddrs(port, tok)
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.URL)
	}
	return out
}

// shareAddrs 枚举本机地址并拼成可直接打开的链接。
//
// 令牌放在查询参数而非片段里：服务端要能在首次访问时读到它并换成
// HttpOnly Cookie，随后 302 跳转到去掉令牌的干净 URL。
func shareAddrs(port int, token string) []LanAddr {
	out := lanIPv4Addrs()
	for i := range out {
		out[i].URL = fmt.Sprintf("http://%s:%d/?token=%s", out[i].IP, port, token)
	}
	return out
}

// virtualIfaceHints 是「虚拟网卡 / 隧道」的识别关键字（对网卡名与描述
// 做小写子串匹配）。命中即标记 Virtual，**只用于提示、不做过滤** ——
// 过滤掉会隐藏用户可能真正想用的地址（例如确实要走 Tailscale 组网时）。
var virtualIfaceHints = []string{
	"vmware", "vmnet", "virtualbox", "hyper-v", "vethernet", "wsl",
	"tailscale", "zerotier", "wintun", "tap-", "tun", "tunnel",
	"mihomo", "clash", "openvpn", "wireguard", "docker", "loopback",
}

// looksVirtual 判断网卡名是否像虚拟网卡或隧道。
//
// Go 标准库的 net.Interface 拿不到网卡「描述」（只有名字与标志位），因此这里
// 只对名字做匹配。名字已覆盖 Windows 上的常见形态：VMware 的网卡名含
// "VMware Network Adapter VMnet1"，代理隧道（Mihomo / Clash）的网卡名即
// "Mihomo"，Hyper-V 为 "vEthernet (...)"。
func looksVirtual(iface *net.Interface) bool {
	hay := strings.ToLower(iface.Name)
	for _, h := range virtualIfaceHints {
		if strings.Contains(hay, h) {
			return true
		}
	}
	return false
}

// lanIPv4Addrs 枚举本机可用于局域网访问的 IPv4 地址。
//
// 过滤规则：跳过已关闭的网卡与回环；跳过链路本地地址（169.254.0.0/16，
// APIPA 自动地址 —— 网线不通时会出现，给用户看只会造成困惑）；去重。
// 排序：真实网卡在前、虚拟网卡在后（stable，不打乱同组内的枚举顺序），
// 让「最可能连得通」的那条排在第一位。
func lanIPv4Addrs() []LanAddr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []LanAddr
	seen := map[string]bool{}
	for i := range ifaces {
		ifc := &ifaces[i]
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			s := ip4.String()
			if seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, LanAddr{IP: s, Iface: ifc.Name, Virtual: looksVirtual(ifc)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return !out[i].Virtual && out[j].Virtual })
	return out
}
