package web_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/bind"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/web"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/secret"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/settings"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/transfer"
)

// 局域网访问档的**端到端**验证。
//
// 与 auth_test.go 的区别：那边用 httptest 直接跑中间件（不碰网络栈），
// 这里真的绑 0.0.0.0、真的走 socket，并用**本机真实局域网 IPv4 地址**发请求 ——
// 于是 RemoteAddr 就是 LAN 地址而非回环，闸门按「远端」处理，覆盖的正是最容易
// 翻车的那条链路：非回环来源 + Cookie 授权 + 同源媒体路由。
//
// 无可用局域网地址（离线 / 仅回环）时自动跳过，不让环境差异把测试搞红。

// lanIPv4 取本机第一个可用于局域网的 IPv4 地址（与 bind.lanIPv4Addrs 同口径：
// 跳过 down / 回环 / 链路本地 169.254.x.x）。
func lanIPv4(t *testing.T) string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("枚举网卡失败: %v", err)
	}
	for _, ifc := range ifaces {
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
			return ip4.String()
		}
	}
	t.Skip("本机没有可用的局域网 IPv4 地址，跳过端到端验证")
	return ""
}

// newE2EServer 装配一个与实际运行等价的 Web server（同一套 New 依赖图），
// 绑 0.0.0.0 的临时端口并启用访问令牌闸门。返回监听地址与令牌。
func newE2EServer(t *testing.T) (addr, token string) {
	t.Helper()

	dir := t.TempDir()
	store, err := settings.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("打开设置存储失败: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := appstate.New(appstate.Config{Store: store, Queue: transfer.New(), Log: logger})
	holder := bind.NewContextHolder()
	// 令牌用 PLAIN 保护器：本测试只关心闸门行为，不关心落盘加密
	// （DPAPI 往返由 pkg/secret 的单测覆盖）。
	lan := bind.NewLan(st, secret.NewFile(filepath.Join(dir, "lan_token"), nil))

	dist := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>cp</html>")}}
	srv := web.New(logger, st, holder,
		bind.NewVault(st, holder), bind.NewFiles(st, holder), bind.NewTransfer(st, holder),
		bind.NewSettings(st, holder), bind.NewPreview(st, holder), lan, dist)

	const tok = "e2e-test-token-0123456789"
	// 端口 0 = 由内核挑一个空闲端口，避开正在运行的正式实例（7840 起）。
	addr, err = srv.Listen("0.0.0.0", 0, tok)
	if err != nil {
		t.Fatalf("监听 0.0.0.0 失败: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	if !srv.LanActive() {
		t.Fatalf("绑 0.0.0.0 后 LanActive 应为 true，addr=%s", addr)
	}
	return addr, tok
}

// lanBase 由监听地址与局域网 IP 拼出「从远端看」的基础 URL。
func lanBase(t *testing.T, addr, lanIP string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("解析监听地址 %q 失败: %v", addr, err)
	}
	return "http://" + net.JoinHostPort(lanIP, port)
}

// noRedirectClient 不跟随重定向：需要观察 302 本身。
func noRedirectClient() *http.Client {
	return &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func TestLanE2EAccessGate(t *testing.T) {
	lanIP := lanIPv4(t)
	addr, token := newE2EServer(t)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("解析监听地址失败: %v", err)
	}
	base := lanBase(t, addr, lanIP)
	cl := noRedirectClient()

	// 1) 本机（127.0.0.1）免令牌可用 —— 默认体验零变化。
	resp, err := cl.Get("http://127.0.0.1:" + port + "/")
	if err != nil {
		t.Fatalf("回环请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("回环访问应 200，got %d", resp.StatusCode)
	}

	// 2) 局域网来源无令牌 → 401 中文提示页（真的走非回环地址）。
	resp2, err := cl.Get(base + "/")
	if err != nil {
		t.Fatalf("局域网请求失败（可能被 Windows 防火墙拦截本机自连）: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("局域网无令牌应 401，got %d", resp2.StatusCode)
	}
	if body, _ := io.ReadAll(resp2.Body); len(body) == 0 {
		t.Error("401 应带可读提示页正文（而不是裸状态码）")
	}

	// 3) 带令牌的分享链接 → 302 + 种 Cookie + 跳转到去掉令牌的干净 URL。
	resp3, err := cl.Get(base + "/?token=" + token)
	if err != nil {
		t.Fatalf("带令牌请求失败: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusFound {
		t.Fatalf("带 token 应 302，got %d", resp3.StatusCode)
	}
	if loc := resp3.Header.Get("Location"); loc != "/" {
		t.Errorf("跳转目标应去掉 token，got %q", loc)
	}
	var cookie *http.Cookie
	for _, c := range resp3.Cookies() {
		if c.Name == "cp_lan_token" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("应种下访问 Cookie")
	}
	if !cookie.HttpOnly {
		t.Error("Cookie 应 HttpOnly")
	}

	// 4) 带 Cookie 的局域网来源可用（静态资源同样要过闸门）。
	for _, path := range []string{"/", "/assets/index-x.js"} {
		req, err := http.NewRequest(http.MethodGet, base+path, nil)
		if err != nil {
			t.Fatalf("构造请求 %s 失败: %v", path, err)
		}
		req.AddCookie(cookie)
		r, err := cl.Do(req)
		if err != nil {
			t.Fatalf("带 Cookie 请求 %s 失败: %v", path, err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Errorf("带 Cookie 访问 %s 应 200，got %d", path, r.StatusCode)
		}
	}

	// 5) 媒体路由 /s/ /t/ /d/ 与其余 API 共用同一道闸门：无 Cookie 必须 401
	//    （这条正是「代理不再另起端口」后必须成立的性质）。
	reqMedia, err := http.NewRequest(http.MethodGet, base+"/s/abc/file.mp4", nil)
	if err != nil {
		t.Fatalf("构造媒体请求失败: %v", err)
	}
	rm, err := cl.Do(reqMedia)
	if err != nil {
		t.Fatalf("媒体路由请求失败: %v", err)
	}
	rm.Body.Close()
	if rm.StatusCode != http.StatusUnauthorized {
		t.Errorf("局域网无令牌访问媒体路由应 401，got %d", rm.StatusCode)
	}

	// 6) 本机专属端点：局域网来源即使令牌正确也 403（防远端把主机程序关掉）。
	reqQuit, err := http.NewRequest(http.MethodPost, base+"/api/app/quit", nil)
	if err != nil {
		t.Fatalf("构造退出请求失败: %v", err)
	}
	reqQuit.AddCookie(cookie)
	rq, err := cl.Do(reqQuit)
	if err != nil {
		t.Fatalf("本机专属端点请求失败: %v", err)
	}
	rq.Body.Close()
	if rq.StatusCode != http.StatusForbidden {
		t.Errorf("局域网来源调用 /api/app/quit 应 403，got %d", rq.StatusCode)
	}
}

// TestLanE2EStatusEndpoint 验证设置页要用的 /api/lan/status 在局域网来源 +
// Cookie 下可用；否则设置页在远端会整片空白（拿不到任何局域网信息）。
func TestLanE2EStatusEndpoint(t *testing.T) {
	lanIP := lanIPv4(t)
	addr, token := newE2EServer(t)
	base := lanBase(t, addr, lanIP)

	// 无令牌：被闸门挡下
	r0, err := http.Post(base+"/api/lan/status", "application/json", nil)
	if err != nil {
		t.Fatalf("请求 /api/lan/status 失败: %v", err)
	}
	r0.Body.Close()
	if r0.StatusCode != http.StatusUnauthorized {
		t.Errorf("无令牌访问 /api/lan/status 应 401，got %d", r0.StatusCode)
	}

	// 带 Cookie：可用，且返回体里应含局域网地址（说明枚举与拼链正常工作）
	req, err := http.NewRequest(http.MethodPost, base+"/api/lan/status", nil)
	if err != nil {
		t.Fatalf("构造状态请求失败: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "cp_lan_token", Value: token})
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("带 Cookie 请求 /api/lan/status 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("/api/lan/status 应 200，got %d body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), lanIP) {
		t.Errorf("/api/lan/status 返回体应包含本机局域网地址 %s，got %s", lanIP, body)
	}
}
