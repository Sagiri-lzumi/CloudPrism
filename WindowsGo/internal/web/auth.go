package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// 访问闸门（局域网档的唯一防线）。
//
// 设计前提：本程序没有用户体系 —— 密库主密钥始终留在主机，远端浏览器只是
// 界面。因此令牌的职责不是「身份认证」，而是「证明这台设备被授权访问本机
// 界面」。据此确定三条规则：
//
//  1. **回环来源永远免令牌**：127.0.0.1 / ::1 直接放行，本机浏览器、托盘
//     「打开界面」、单实例探测的行为与历史版本完全一致（零摩擦）。
//  2. **非回环来源必须带令牌**：覆盖包括静态资源与 SSE 在内的所有路径。
//     token 为空时非回环一律拒绝（**fail-closed**）—— 这样即使外部误把监听
//     地址配成 0.0.0.0，也不会出现「无鉴权对外服务」的最坏情况。
//  3. **少数端点仅限本机**：会弹主机原生对话框或直接关进程的端点（退出、
//     选目录），远端即使令牌正确也拒绝，避免远端把主机 UI 顶出来或误关程序。
//
// 已知局限（必须在 UI 中如实告知用户，不做粉饰）：
//   - 局域网是明文 HTTP，令牌与数据在链路上可被同一网段的嗅探者截获。
//     家用 WPA2 网络可接受；开放/公共 WiFi 下不应开启。
//   - 拿到令牌即等于拿到本程序全部界面能力（含解密下载），故默认关闭。
const tokenCookieName = "cp_lan_token"

// localOnlyPaths 是仅允许回环来源访问的端点集合。
//
// 这些端点在主机侧有副作用（弹原生选择框）或是自杀式操作（关进程），
// 远端触发没有意义，只会造成困扰或被当作拒绝服务手段。
var localOnlyPaths = map[string]string{
	"/api/app/quit":                "退出程序",
	"/api/vault/chooselocaldir":    "本机目录选择",
	"/api/settings/choosesyncdir":  "本机目录选择",
	"/api/settings/choosecachedir": "本机目录选择",
}

// guard 实现访问闸门。
type guard struct {
	token string // 访问令牌；空串 = 纯本机模式（非回环一律拒绝）
	log   *slog.Logger

	mu    sync.Mutex
	fails map[string]*failRecord // key = 客户端 IP
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
func newGuard(token string, log *slog.Logger) *guard {
	if log == nil {
		log = slog.Default()
	}
	return &guard{token: token, log: log, fails: map[string]*failRecord{}}
}

// wrap 把闸门套在业务 handler 外面。
func (g *guard) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			http.Error(w, "该操作仅允许在本机执行（"+why+"）", http.StatusForbidden)
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
