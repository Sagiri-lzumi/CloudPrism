package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// newTestGuard 构造一个带令牌的闸门（日志丢弃，避免污染测试输出）。
// 端口基准固定 7840；lan=false 对应纯本机档（Host 白名单只认回环/localhost）。
func newTestGuard(token string) *guard {
	return newGuard(token, 7840, false, slog.New(slog.NewTextHandler(discard{}, nil)))
}

// discard 是丢弃写入的 io.Writer（测试里不需要看闸门日志）。
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// okHandler 是被闸门保护的业务 handler：任何请求都回 200 + "ok"。
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

// serve 构造一个来源地址可控的请求并跑一遍闸门。
// remoteAddr 形如 "192.168.1.9:5000" / "127.0.0.1:5000"；
// Host 默认设为合法的 127.0.0.1:7840（httptest 默认 example.com 会被
// Host 白名单拦下），需要非法 Host/Origin 的用例用 mutate 覆盖。
func serve(g *guard, method, target, remoteAddr string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = remoteAddr
	r.Host = "127.0.0.1:7840"
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	g.wrap(okHandler()).ServeHTTP(w, r)
	return w
}

/* -------------------------------------------------- 规则 0：Host/Origin 白名单 */

// TestGuardHostWhitelistBlocksDNSRebinding 防线：Host 非本机地址一律 403，
// 即使来源是回环（DNS rebinding 的请求来源恰是本机浏览器）。
func TestGuardHostWhitelistBlocksDNSRebinding(t *testing.T) {
	g := newTestGuard("tok")
	// 回环来源 + 攻击者域名 Host：rebinding 后浏览器把它当同源，必须拦
	w := serve(g, http.MethodGet, "/api/app/state", "127.0.0.1:52341", func(r *http.Request) {
		r.Host = "evil.example.com:7840"
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("攻击者域名 Host 应 403，got %d", w.Code)
	}
	// 端口不匹配同样拒绝（可能是别的服务被 rebinding 或扫端口）
	for _, h := range []string{"127.0.0.1:9999", "localhost", "127.0.0.1"} {
		w := serve(g, http.MethodGet, "/api/app/state", "127.0.0.1:52341", func(r *http.Request) {
			r.Host = h
		})
		if w.Code != http.StatusForbidden {
			t.Errorf("Host %q 应 403，got %d", h, w.Code)
		}
	}
	// 合法 Host：回环三种形态 + 大小写不敏感
	for _, h := range []string{"127.0.0.1:7840", "localhost:7840", "[::1]:7840", "LOCALHOST:7840"} {
		w := serve(g, http.MethodGet, "/api/app/state", "127.0.0.1:52341", func(r *http.Request) {
			r.Host = h
		})
		if w.Code != http.StatusOK {
			t.Errorf("合法 Host %q 应放行，got %d", h, w.Code)
		}
	}
	// 纯本机档不认局域网 IP：即使来源就是本机网卡地址也拒绝（fail-closed，
	// 真正的局域网访问由 e2e 测试覆盖 lan=true 分支）
	if !g.hostAllowed("192.168.1.9:7840") {
		// lan=false 时预期拒绝；这里反向断言防误改白名单逻辑
		t.Log("纯本机档正确拒绝局域网 Host")
	} else {
		t.Error("纯本机档不应放行局域网 Host")
	}
}

// TestGuardOriginValidationBlocksCSRF：状态变更方法带非白名单 Origin → 403；
// Origin 缺失（非浏览器客户端）与同源 Origin 放行；GET 不校验。
func TestGuardOriginValidationBlocksCSRF(t *testing.T) {
	g := newTestGuard("tok")

	// 回环来源 + 恶意网页 Origin 的 POST：盲打 CSRF 必须拦
	w := serve(g, http.MethodPost, "/api/vault/open", "127.0.0.1:52341", func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example.com")
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("恶意 Origin 的 POST 应 403，got %d", w.Code)
	}
	// Origin: null（file:// 页面/沙盒 iframe）：解析不出 host，同样拒绝
	w = serve(g, http.MethodPost, "/api/vault/open", "127.0.0.1:52341", func(r *http.Request) {
		r.Header.Set("Origin", "null")
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("Origin: null 的 POST 应 403，got %d", w.Code)
	}
	// 同端口但白名单外主机的 Origin（重绑后同源假象）也拒绝
	w = serve(g, http.MethodPost, "/api/vault/open", "127.0.0.1:52341", func(r *http.Request) {
		r.Header.Set("Origin", "http://evil.example.com:7840")
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("rebinding 同源假象的 POST 应 403，got %d", w.Code)
	}

	// Origin 缺失：非浏览器客户端（curl/托盘/单实例探测）兼容，放行
	if w := serve(g, http.MethodPost, "/api/app/ping", "127.0.0.1:52341", nil); w.Code != http.StatusOK {
		t.Errorf("无 Origin 的 POST 应放行（非浏览器客户端），got %d", w.Code)
	}
	// 同源 Origin：本机前端 fetch 的常规形态，放行
	for _, o := range []string{"http://127.0.0.1:7840", "http://localhost:7840"} {
		w := serve(g, http.MethodPost, "/api/vault/open", "127.0.0.1:52341", func(r *http.Request) {
			r.Header.Set("Origin", o)
		})
		if w.Code != http.StatusOK {
			t.Errorf("同源 Origin %q 的 POST 应放行，got %d", o, w.Code)
		}
	}
	// GET 不校验 Origin（跨源标签加载媒体/静态资源不受影响）
	w = serve(g, http.MethodGet, "/s/abc/file.mp4", "127.0.0.1:52341", func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example.com")
	})
	if w.Code != http.StatusOK {
		t.Errorf("带任意 Origin 的 GET 应放行，got %d", w.Code)
	}
	// 令牌正确的远端（非回环）POST 同样受 Origin 防线保护
	w = serve(g, http.MethodPost, "/api/vault/open", "192.168.1.9:5000", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: tokenCookieName, Value: "tok"})
		r.Header.Set("Origin", "https://evil.example.com")
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("远端恶意 Origin 的 POST 应 403（优先于令牌检查），got %d", w.Code)
	}
}

/* -------------------------------------------------- 规则 1：回环免令牌 */

func TestGuardLoopbackBypass(t *testing.T) {
	// 令牌非空（局域网档）与令牌为空（纯本机档）两种情况下，回环都必须零摩擦。
	for _, token := range []string{"lan-token-abc", ""} {
		g := newTestGuard(token)
		for _, addr := range []string{"127.0.0.1:52341", "[::1]:52341", "127.0.0.5:80"} {
			w := serve(g, http.MethodGet, "/api/app/state", addr, nil)
			if w.Code != http.StatusOK {
				t.Errorf("token=%q addr=%s: 回环应放行，got %d", token, addr, w.Code)
			}
		}
	}
}

func TestGuardLoopbackSSEAndStatic(t *testing.T) {
	g := newTestGuard("tok")
	// 静态资源与 SSE 在回环下同样不该被拦（否则本机界面直接白屏）。
	for _, path := range []string{"/", "/assets/index-x.js", "/api/events", "/s/abc/file.mp4"} {
		if w := serve(g, http.MethodGet, path, "127.0.0.1:1", nil); w.Code != http.StatusOK {
			t.Errorf("回环访问 %s 应放行，got %d", path, w.Code)
		}
	}
}

/* -------------------------------------------------- 规则 2：非回环必须带令牌 */

func TestGuardRemoteWithoutTokenRejected(t *testing.T) {
	g := newTestGuard("correct-token")
	// 覆盖静态资源、API、媒体三类路径：闸门必须一视同仁。
	for _, path := range []string{"/", "/assets/index-x.js", "/api/app/state", "/s/abc/file.mp4", "/api/events"} {
		w := serve(g, http.MethodGet, path, "192.168.1.9:5000", nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("无令牌远端访问 %s 应 401，got %d", path, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("无令牌应返回可读中文提示页，got Content-Type=%q", ct)
		}
	}
}

func TestGuardRemoteWrongTokenRejected(t *testing.T) {
	g := newTestGuard("correct-token")
	for _, bad := range []string{"wrong", "correct-toke", "correct-token!", "CORRECT-TOKEN", ""} {
		w := serve(g, http.MethodGet, "/api/app/state", "10.0.0.4:5000", func(r *http.Request) {
			if bad != "" {
				r.AddCookie(&http.Cookie{Name: tokenCookieName, Value: bad})
			}
		})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("错令牌 %q 应 401，got %d", bad, w.Code)
		}
	}
}

func TestGuardRemoteCorrectTokenAccepted(t *testing.T) {
	const tok = "correct-token"
	g := newTestGuard(tok)

	// 路径 A：Cookie（首次 ?token= 种下后的常规方式）
	if w := serve(g, http.MethodGet, "/api/app/state", "192.168.1.9:5000", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: tokenCookieName, Value: tok})
	}); w.Code != http.StatusOK {
		t.Errorf("Cookie 令牌应放行，got %d", w.Code)
	}

	// 路径 B：Authorization: Bearer（脚本/小工具用）
	for _, h := range []string{"Bearer " + tok, "bearer " + tok, "BEARER " + tok} {
		if w := serve(g, http.MethodGet, "/api/app/state", "192.168.1.9:5000", func(r *http.Request) {
			r.Header.Set("Authorization", h)
		}); w.Code != http.StatusOK {
			t.Errorf("Bearer %q 应放行，got %d", h, w.Code)
		}
	}
}

func TestGuardQueryTokenSetsCookieAndRedirects(t *testing.T) {
	const tok = "correct-token"
	g := newTestGuard(tok)

	// 带其他查询参数验证「只去掉 token、其余保留」。
	w := serve(g, http.MethodGet, "/files?token="+tok+"&sort=name", "192.168.1.9:5000", nil)

	if w.Code != http.StatusFound {
		t.Fatalf("首次带 token 应 302，got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/files?sort=name" {
		t.Errorf("跳转目标应去掉 token 且保留其余参数，got %q", loc)
	}
	cookies := w.Result().Cookies()
	var got *http.Cookie
	for _, c := range cookies {
		if c.Name == tokenCookieName {
			got = c
		}
	}
	if got == nil {
		t.Fatal("应种下访问 Cookie")
	}
	if got.Value != tok {
		t.Errorf("Cookie 值应为令牌，got %q", got.Value)
	}
	if !got.HttpOnly {
		t.Error("Cookie 必须 HttpOnly（前端 JS 无需读取）")
	}
	if got.SameSite != http.SameSiteLaxMode {
		t.Errorf("Cookie 应 SameSite=Lax，got %v", got.SameSite)
	}
	// 明文 HTTP 下不能带 Secure，否则浏览器直接丢弃 Cookie（局域网档会失效）。
	if got.Secure {
		t.Error("Cookie 不应带 Secure：局域网走明文 HTTP")
	}
}

func TestGuardQueryTokenWrongNotRedirected(t *testing.T) {
	g := newTestGuard("correct-token")
	w := serve(g, http.MethodGet, "/?token=nope", "192.168.1.9:5000", nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("错 token 查询参数不应跳转，应 401，got %d", w.Code)
	}
	// 错令牌绝不能种下 Cookie（否则一次错试就能留下一个无效凭据）。
	for _, c := range w.Result().Cookies() {
		if c.Name == tokenCookieName {
			t.Error("错令牌不应种下 Cookie")
		}
	}
}

/* -------------------------------------------------- 规则 2 兜底：fail-closed */

func TestGuardEmptyTokenRejectsRemote(t *testing.T) {
	// token 为空 = 纯本机模式。即使有人把监听地址误改成 0.0.0.0，
	// 非回环来源也必须被拒（fail-closed），不能出现无鉴权对外服务。
	g := newTestGuard("")
	for _, path := range []string{"/", "/api/app/state", "/d/abc/secret.bin"} {
		if w := serve(g, http.MethodGet, path, "192.168.1.9:5000", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("纯本机模式下远端访问 %s 应 401，got %d", path, w.Code)
		}
	}
	// 而回环照常可用。
	if w := serve(g, http.MethodGet, "/api/app/state", "127.0.0.1:5000", nil); w.Code != http.StatusOK {
		t.Errorf("纯本机模式下回环应放行，got %d", w.Code)
	}
}

/* -------------------------------------------------- 规则 3：本机专属端点 */

func TestGuardLocalOnlyPathsRemoteForbidden(t *testing.T) {
	const tok = "correct-token"
	g := newTestGuard(tok)
	// 令牌正确也不放行：这些端点在主机侧有副作用（弹原生框 / 关进程）。
	for path := range localOnlyPaths {
		w := serve(g, http.MethodPost, path, "192.168.1.9:5000", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: tokenCookieName, Value: tok})
		})
		if w.Code != http.StatusForbidden {
			t.Errorf("远端调用本机专属端点 %s 应 403，got %d", path, w.Code)
		}
	}
	// 回环调用同一批端点必须照常放行（本机行为不变）。
	for path := range localOnlyPaths {
		if w := serve(g, http.MethodPost, path, "127.0.0.1:5000", nil); w.Code != http.StatusOK {
			t.Errorf("回环调用 %s 应放行，got %d", path, w.Code)
		}
	}
}

/* -------------------------------------------------- 失败节流 */

func TestGuardBlocksAfterRepeatedFailures(t *testing.T) {
	g := newTestGuard("correct-token")

	// 前 maxFails-1 次：仍是 401（提示页）。
	for i := 0; i < maxFails-1; i++ {
		if w := serve(g, http.MethodGet, "/", "192.168.1.9:5000", nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败应 401，got %d", i+1, w.Code)
		}
	}
	// 第 maxFails 次：记录触发封禁窗口，但本次响应仍是 401。
	if w := serve(g, http.MethodGet, "/", "192.168.1.9:5000", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("达到阈值的那次仍应 401，got %d", w.Code)
	}
	// 之后进入 429。
	w := serve(g, http.MethodGet, "/", "192.168.1.9:5000", nil)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("封禁窗口内应 429，got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("429 应带 Retry-After 头")
	}

	// 封禁只针对该 IP：另一个来源不受影响（仍是正常的 401 而非 429）。
	if w := serve(g, http.MethodGet, "/", "192.168.1.10:5000", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("封禁不应波及其他来源，got %d", w.Code)
	}
	// 本机始终不受封禁影响（否则误试会把用户锁在自己界面外）。
	if w := serve(g, http.MethodGet, "/", "127.0.0.1:5000", nil); w.Code != http.StatusOK {
		t.Errorf("封禁不应影响回环，got %d", w.Code)
	}
}

func TestGuardSuccessClearsFailures(t *testing.T) {
	const tok = "correct-token"
	g := newTestGuard(tok)
	// 先失败几次（未达阈值）
	for i := 0; i < maxFails-2; i++ {
		serve(g, http.MethodGet, "/", "192.168.1.9:5000", nil)
	}
	// 成功一次应清零
	if w := serve(g, http.MethodGet, "/", "192.168.1.9:5000", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: tokenCookieName, Value: tok})
	}); w.Code != http.StatusOK {
		t.Fatalf("正确令牌应放行，got %d", w.Code)
	}
	// 再失败 maxFails-1 次仍不该被封（计数已清零）
	for i := 0; i < maxFails-1; i++ {
		if w := serve(g, http.MethodGet, "/", "192.168.1.9:5000", nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("清零后第 %d 次失败不该被封，got %d", i+1, w.Code)
		}
	}
}

/* -------------------------------------------------- 辅助函数 */

func TestIsLoopbackAddr(t *testing.T) {
	yes := []string{"127.0.0.1:80", "[::1]:80", "127.0.0.5:1", "127.0.0.1", "::1", "[::1]"}
	for _, a := range yes {
		if !isLoopbackAddr(a) {
			t.Errorf("%q 应判为回环", a)
		}
	}
	no := []string{"192.168.1.9:80", "10.0.0.1:1", "0.0.0.0:80", "8.8.8.8:443", "", "localhost:80", "::ffff:192.168.1.9"}
	for _, a := range no {
		if isLoopbackAddr(a) {
			t.Errorf("%q 不应判为回环", a)
		}
	}
}

func TestClientIP(t *testing.T) {
	cases := map[string]string{
		"192.168.1.9:5000": "192.168.1.9",
		"[::1]:5000":       "::1",
		"127.0.0.1:1":      "127.0.0.1",
		"weird":            "weird", // 解析失败时原样返回（只用于日志/限流）
	}
	for in, want := range cases {
		if got := clientIP(&http.Request{RemoteAddr: in}); got != want {
			t.Errorf("clientIP(%q) = %q，want %q", in, got, want)
		}
	}
}

func TestTokenEqual(t *testing.T) {
	if !tokenEqual("abc", "abc") {
		t.Error("相同令牌应判等")
	}
	if tokenEqual("abc", "abd") {
		t.Error("不同令牌不应判等")
	}
	if tokenEqual("abc", "") {
		t.Error("空令牌不应与任何非空值判等")
	}
	if !tokenEqual("", "") {
		t.Error("两个空串应判等（调用方保证 token 非空才走此分支）")
	}
	if tokenEqual("abc", "abcd") {
		t.Error("长度不同不应判等")
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
		found  bool
	}{
		{"Bearer abc123", "abc123", true},
		{"bearer abc123", "abc123", true},
		{"BEARER  abc123 ", "abc123", true},
		{"Bearer ", "", false},
		{"Bearer", "", false},
		{"Basic abc123", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		r := &http.Request{Header: http.Header{}}
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		got, found := bearerToken(r)
		if got != c.want || found != c.found {
			t.Errorf("bearerToken(%q) = (%q,%v)，want (%q,%v)", c.header, got, found, c.want, c.found)
		}
	}
}

func TestStripToken(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/?token=abc", "/"},
		{"/files?token=abc&sort=name", "/files?sort=name"},
		{"/files?sort=name&token=abc", "/files?sort=name"},
		{"/files", "/files"},
		{"/?a=1&b=2", "/?a=1&b=2"},
	}
	for _, c := range cases {
		u, err := url.Parse(c.in)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", c.in, err)
		}
		if got := stripToken(u); got != c.want {
			t.Errorf("stripToken(%q) = %q，want %q", c.in, got, c.want)
		}
	}
}
