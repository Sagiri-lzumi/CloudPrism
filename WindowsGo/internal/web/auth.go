package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 访问闸门（局域网档的唯一防线）。
//
// 设计前提：本程序没有用户体系 —— 密库主密钥始终留在主机，远端浏览器只是
// 界面。因此令牌的职责不是「身份认证」，而是「证明这台设备被授权访问本机
// 界面」。据此确定四条规则：
//
//  0. **Host 白名单 + Origin 校验（对回环与非回环一律生效）**：
//     - Host 只认 127.0.0.1/localhost/[::1]:<port>（局域网档另含本机全部
//     接口地址:port），防 DNS rebinding —— 攻击者域名重绑到 127.0.0.1 后
//     浏览器把它当同源，JS 可任意读写响应（含解密下载）；rebinding 请求
//     的 Host 头仍是攻击者域名，白名单直接 403。
//     - 状态变更方法（POST/PUT/DELETE/PATCH）校验 Origin：存在且非白名单
//     主机 → 403，堵住本机浏览器恶意网页对回环端点的盲打 CSRF；缺失则
//     放行（curl/托盘/单实例探测等非浏览器客户端不发 Origin）。
//  1. **回环来源永远免令牌**：127.0.0.1 / ::1 直接放行，本机浏览器、托盘
//     「打开界面」、单实例探测的行为与历史版本完全一致（零摩擦）。
//  2. **非回环来源必须带令牌**：覆盖包括静态资源与 SSE 在内的所有路径。
//     token 为空时非回环一律拒绝（**fail-closed**）—— 这样即使外部把监听
//     地址配成 0.0.0.0，也不会出现「无鉴权对外服务」的最坏情况。
//  3. **少数端点仅限本机**：会在主机上产生副作用的端点（读主机本地文件系统
//     的目录浏览、退出进程），远端即使令牌正确也拒绝，避免远端把主机目录
//     结构摸出来或误关程序。
//
// 已知局限（必须在 UI 中如实告知用户，不做粉饰）：
//   - 局域网是明文 HTTP，令牌与数据在链路上可被同一网段的嗅探者截获。
//     家用 WPA2 网络可接受；开放/公共 WiFi 下不应开启。
//   - 拿到令牌即等于拿到本程序全部界面能力（含解密下载），故默认关闭。
const tokenCookieName = "cp_lan_token"

// localOnlyPaths 是仅允许回环来源访问的端点集合。
//
// 这些端点在主机侧有副作用（读主机本地文件系统 / 关进程），远端触发没有
// 意义，只会造成困扰或被当作拒绝服务手段。
//
// /api/fs/* 是本机目录浏览（网页版目录选择器的数据源）：它能读出主机任意
// 目录的名字。登记在这里，与「只有本机能弹原生选择框」的原能力边界一致 ——
// 远端即使令牌正确也只能手输路径，不能让界面去枚举主机目录。
var localOnlyPaths = map[string]string{
	"/api/app/quit":  "退出程序",
	"/api/fs/drives": "本机目录浏览",
	"/api/fs/dirs":   "本机目录浏览",
	"/api/fs/mkdir":  "本机目录浏览",
}

// guard 实现访问闸门。
type guard struct {
	token string // 访问令牌；空串 = 纯本机模式（非回环一律拒绝）
	log   *slog.Logger
	port  int  // 实际监听端口（端口顺延后可能与配置不同；Host/Origin 端口匹配基准）
	lan   bool // 局域网档：Host 白名单额外纳入本机全部接口地址

	mu    sync.Mutex
	fails map[string]*failRecord // key = 客户端 IP

	// 本机接口地址缓存（仅局域网档使用）：DHCP 续约/VPN 拨号会换地址，
	// 启动时一次性快照会把用户锁在界面外，故短 TTL 动态刷新。
	ifMu     sync.Mutex
	ifAddrs  map[string]struct{}
	ifCached time.Time
}

// failRecord 记录单个来源的连续失败次数与封禁截止时间。
type failRecord struct {
	count     int
	blockTill time.Time
	seen      time.Time
}

const (
	// 连续失败达到该次数后进入封禁窗口（简单防暴力，不做复杂风控）。
	maxFails = 6
	// 封禁窗口时长。刻意做得短：误输令牌的人不应被长时间挡在门外。
	blockWindow = 30 * time.Second
	// 失败记录保留时长（超出即清理，避免 map 被伪造 IP 撑爆）。
	failTTL = 10 * time.Minute
	// 失败记录表的容量上限，超出时清理过期项，仍超则整体重置。
	maxFailEntries = 2048
)

// newGuard 构造访问闸门。token 为空串时进入 fail-closed 的纯本机模式。
// port 是实际监听端口（Host/Origin 白名单的端口基准）；lan 表示是否对
// 局域网监听（决定 Host 白名单是否纳入本机接口地址）。须在监听成功后
// 调用（端口此时才确定）。
func newGuard(token string, port int, lan bool, log *slog.Logger) *guard {
	if log == nil {
		log = slog.Default()
	}
	return &guard{token: token, port: port, lan: lan, log: log, fails: map[string]*failRecord{}}
}

// wrap 把闸门套在业务 handler 外面。
func (g *guard) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 规则 0a：Host 白名单。必须在来源判断之前 —— DNS rebinding 的
		// 请求来源恰是回环（浏览器在本机），靠 RemoteAddr 区分不出来。
		if !g.hostAllowed(r.Host) {
			g.log.Warn("拒绝 Host 非白名单的请求（疑似 DNS rebinding）",
				"host", r.Host, "path", r.URL.Path)
			http.Error(w, "请求目标不在本服务允许列表", http.StatusForbidden)
			return
		}
		// 规则 0b：状态变更方法的 Origin 校验（CSRF 防线）。
		if !g.originAllowed(r) {
			g.log.Warn("拒绝跨源状态变更请求（疑似 CSRF）",
				"origin", r.Header.Get("Origin"), "path", r.URL.Path)
			http.Error(w, "跨源请求被拒绝", http.StatusForbidden)
			return
		}
		if isLoopback(r) {
			next.ServeHTTP(w, r) // 规则 1：本机无摩擦
			return
		}

		ip := clientIP(r)

		// 规则 2：纯本机模式下非回环直接拒绝（fail-closed，不泄露任何内容）
		if g.token == "" {
			g.log.Warn("拒绝非本机访问（未启用局域网档）", "ip", ip, "path", r.URL.Path)
			writeNeedTokenPage(w, "本程序当前只允许本机访问。")
			return
		}

		if g.blocked(ip) {
			w.Header().Set("Retry-After", "30")
			http.Error(w, "尝试过于频繁，请稍后再试", http.StatusTooManyRequests)
			return
		}

		// 首次访问携带 ?token=：验证通过后种 Cookie 并跳转到去掉令牌的干净
		// URL —— 令牌不应留在浏览器书签/历史记录里。
		if q := r.URL.Query().Get("token"); q != "" {
			if !tokenEqual(q, g.token) {
				g.recordFail(ip)
				g.log.Warn("局域网访问令牌错误", "ip", ip, "path", r.URL.Path)
				writeNeedTokenPage(w, "访问令牌不正确。")
				return
			}
			g.clearFail(ip)
			g.setCookie(w, r)
			http.Redirect(w, r, stripToken(r.URL), http.StatusFound)
			return
		}

		// 已授权设备：Cookie 或 Authorization: Bearer 二者之一即可。
		ok := false
		if c, err := r.Cookie(tokenCookieName); err == nil && tokenEqual(c.Value, g.token) {
			ok = true
		} else if b, found := bearerToken(r); found && tokenEqual(b, g.token) {
			ok = true
		}
		if !ok {
			g.recordFail(ip)
			g.log.Warn("局域网访问未授权", "ip", ip, "path", r.URL.Path)
			writeNeedTokenPage(w, "需要访问令牌。")
			return
		}
		g.clearFail(ip)

		// 规则 3：本机专属端点，远端即使令牌正确也不放行。
		if why, only := localOnlyPaths[r.URL.Path]; only {
			g.log.Warn("拒绝远端调用本机专属端点", "ip", ip, "path", r.URL.Path)
			writeLocalOnlyErr(w, r, why)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// setCookie 种下访问 Cookie。
//
// 不带 Secure 属性：局域网走明文 HTTP，带 Secure 浏览器会直接丢弃 Cookie。
// 这是明文链路的固有局限，已在包的注释与 UI 中说明。
func (g *guard) setCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     tokenCookieName,
		Value:    g.token,
		Path:     "/",
		HttpOnly: true, // 前端 JS 无需读取，挡住 XSS 顺手窃取
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 3600,
	})
}

// blocked 报告该来源是否处于封禁窗口内；顺带做记录表清理。
func (g *guard) blocked(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked()
	rec := g.fails[ip]
	if rec == nil {
		return false
	}
	return time.Now().Before(rec.blockTill)
}

// recordFail 记一次失败；达到阈值则开启封禁窗口。
func (g *guard) recordFail(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked()
	rec := g.fails[ip]
	if rec == nil {
		rec = &failRecord{}
		g.fails[ip] = rec
	}
	rec.count++
	rec.seen = time.Now()
	if rec.count >= maxFails {
		rec.blockTill = time.Now().Add(blockWindow)
		rec.count = 0 // 封禁后重新计数，避免永久累加
	}
}

// clearFail 成功授权后清除该来源的失败记录。
func (g *guard) clearFail(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, ip)
}

// pruneLocked 清理过期记录；表过大时整体重置（防伪造 IP 撑爆内存）。
func (g *guard) pruneLocked() {
	if len(g.fails) > maxFailEntries {
		g.fails = map[string]*failRecord{}
		return
	}
	cut := time.Now().Add(-failTTL)
	for k, rec := range g.fails {
		if rec.seen.Before(cut) && time.Now().After(rec.blockTill) {
			delete(g.fails, k)
		}
	}
}

/* ----------------------------------------------------------- Host/Origin 白名单 */

// ifaceCacheTTL 是本机接口地址缓存的刷新周期。
const ifaceCacheTTL = 30 * time.Second

// hostAllowed 判断 Host 头（形如 host:port）是否指向本服务自身。
//
// 威胁模型（DNS rebinding）：攻击者域名 evil.com 先解析到自己的服务器，
// 页面加载后再把域名重新解析到 127.0.0.1 —— 浏览器认为仍在访问 evil.com
// （同源策略按域名判定），页面 JS 于是能任意读写本服务响应（含解密下载）。
// rebinding 后的请求 Host 头仍是 evil.com:<port>，白名单只认本机地址，
// 不匹配直接拒绝。
//
// 端口必须与实际监听端口一致：浏览器对非 80/443 端口必显式携带端口，
// 无端口的 Host（HTTP/1.0 客户端）按 fail-closed 拒绝。域名不区分大小写。
func (g *guard) hostAllowed(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || port != strconv.Itoa(g.port) {
		return false
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	if !g.lan {
		return false // 纯本机档只认回环与 localhost
	}
	// 局域网档：远端浏览器用本机任一接口地址访问，全部纳入白名单
	// （宁多勿漏 —— 漏了会把用户锁在界面外；回环已在静态名单里排除）。
	_, ok := g.interfaceAddrs()[host]
	return ok
}

// originAllowed 判断状态变更请求（非 GET/HEAD/OPTIONS）的 Origin 是否可信。
//
// 威胁模型（CSRF）：本机浏览器里打开的恶意网页可向 http://127.0.0.1:<port>
// 发起跨源 POST —— 响应虽读不回，但开库/删文件/改设置等状态变更端点在
// 「盲打」下同样危险。跨源请求按规范必带 Origin 且值是攻击者自己的源；
// Origin 缺失则放行（curl/托盘/单实例探测等非浏览器客户端不发 Origin）。
//
// Origin 为 "null"（file:// 页面或沙盒 iframe）解析不出 host，同样拒绝 —
// file:// 里的脚本也是威胁。GET/HEAD/OPTIONS 不改状态，一律不校验（媒体
// 预览的跨源标签加载不受影响）。
func (g *guard) originAllowed(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // 非浏览器客户端兼容：无 Origin 视为非跨站发起
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false // "null" 或畸形 Origin 一律拒绝（fail-closed）
	}
	// Origin 与 Host 同口径校验：主机在白名单且端口等于监听端口
	return g.hostAllowed(u.Host)
}

// interfaceAddrs 返回本机全部非回环接口地址（仅局域网档使用）。
//
// 30 秒缓存：net.InterfaceAddrs 是 syscall，逐请求调用开销不可接受；
// DHCP 续约/VPN 拨号换地址后最多迟一个周期放行，可接受。
func (g *guard) interfaceAddrs() map[string]struct{} {
	g.ifMu.Lock()
	defer g.ifMu.Unlock()
	now := time.Now()
	if g.ifAddrs != nil && now.Sub(g.ifCached) < ifaceCacheTTL {
		return g.ifAddrs
	}
	set := make(map[string]struct{})
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				set[ipnet.IP.String()] = struct{}{}
			}
		}
	}
	g.ifAddrs = set
	g.ifCached = now
	return set
}

/* ----------------------------------------------------------- 辅助函数 */

// isLoopback 判断请求来源是否为回环地址。
func isLoopback(r *http.Request) bool {
	return isLoopbackAddr(r.RemoteAddr)
}

// isLoopbackAddr 判断 "host:port" 形式的地址是否回环。
func isLoopbackAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr // 无端口（测试里直接用 IP 字符串）时原样解析
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// clientIP 取客户端 IP（去掉端口；取不到时返回原串，仅用于日志与限流）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// tokenEqual 常量时间比较令牌。
//
// 先各取 SHA-256 再比较：subtle.ConstantTimeCompare 在长度不等时立即返回，
// 会泄露令牌长度；先哈希可让「长度不同」与「内容不同」耗时一致。
func tokenEqual(got, want string) bool {
	ga := sha256.Sum256([]byte(got))
	gb := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(ga[:], gb[:]) == 1
}

// bearerToken 取出 Authorization: Bearer <token> 中的令牌。
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

// stripToken 返回去掉 token 查询参数的 URL（路径 + 其余参数原样保留）。
func stripToken(u *url.URL) string {
	q := u.Query()
	q.Del("token")
	out := u.Path
	if out == "" {
		out = "/"
	}
	if enc := q.Encode(); enc != "" {
		out += "?" + enc
	}
	return out
}

// needTokenPage 是无令牌访问时展示的提示页模板。
var needTokenPage = template.Must(template.New("need-token").Parse(`<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>需要访问令牌 · CloudPrism</title>
<style>
 body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
      background:#f5f6f8;color:#1f2328;font:14px/1.6 system-ui,"Segoe UI","Microsoft YaHei",sans-serif}
 .card{max-width:520px;margin:24px;padding:28px 30px;background:#fff;border-radius:14px;
       box-shadow:0 1px 3px rgba(0,0,0,.08),0 8px 28px rgba(0,0,0,.06)}
 h1{margin:0 0 6px;font-size:18px}
 p{margin:8px 0;color:#57606a}
 ol{margin:10px 0 0;padding-left:20px;color:#57606a}
 li{margin:4px 0}
 form{margin-top:16px;display:flex;gap:8px}
 input{flex:1;padding:8px 10px;border:1px solid #d0d7de;border-radius:8px;font:inherit}
 button{padding:8px 16px;border:0;border-radius:8px;background:#1f6feb;color:#fff;font:inherit;cursor:pointer}
</style></head><body>
<div class="card">
  <h1>需要访问令牌</h1>
  <p>{{.Reason}}</p>
  <ol>
    <li>在<strong>运行本程序的那台电脑</strong>上打开 CloudPrism 界面</li>
    <li>进入「设置 → 局域网访问」</li>
    <li>复制那条带令牌的完整链接，在本设备打开（或把令牌粘到下面）</li>
  </ol>
  <form method="get" action="/">
    <input name="token" placeholder="粘贴访问令牌" autocomplete="off" autofocus>
    <button type="submit">进入</button>
  </form>
</div></body></html>
`))

// writeNeedTokenPage 输出提示页（状态 401）。
//
// 刻意返回可读中文页面而非裸 401：局域网档下最常见的困惑是「打开了却一片
// 空白/被拦下」，直接告诉用户去哪里取令牌，比让用户自己猜要好。
func writeNeedTokenPage(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_ = needTokenPage.Execute(w, struct{ Reason string }{Reason: reason})
}

// writeLocalOnlyErr 输出「仅本机可执行」的拒绝响应。
//
// /api/ 路径必须回 JSON：前端的统一解包层只认 {code,message}，若扔一段纯文本，
// 界面只能显示「HTTP 403」——正是要杜绝的「展示缺口」（用户看到报错却不知道
// 发生了什么）。非 API 路径保持纯文本，与历史行为一致。
func writeLocalOnlyErr(w http.ResponseWriter, r *http.Request, why string) {
	msg := "该操作仅允许在本机执行（" + why + "）"
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		http.Error(w, msg, http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": "internal", "message": msg})
}
