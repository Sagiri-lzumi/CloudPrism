// CloudPrism Go 版入口（v33 起 Web 服务模式为唯一形态）。
//
// 架构：HTTP server（API+SSE+静态前端+代理流）+ 系统托盘守护进程。
// 业务逻辑（internal/appstate + internal/bind + pkg/*）完全复用，不依赖 Wails。
// 前端 Vue 组件逻辑复用，数据层改 fetch/SSE（不依赖 wailsjs runtime）。
//
// 启动顺序：依赖图 → 内嵌前端 dist → 按「局域网访问档」设置决定监听地址
// （关闭=127.0.0.1；开启=0.0.0.0 + 访问令牌闸门，令牌不可用则回退本机）→
// 端口顺延 + 单实例探测 → 开浏览器 → Serve（goroutine）→ 托盘消息循环（主线程）。
// 退出：托盘「退出」/ NavRail「退出」/ SIGINT → 统一收尾（停帧循环→
// Shutdown→ 锁库 → 关日志 → 移除托盘）。
package main

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/platform/win"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/tray"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/web"
)

// assets 内嵌前端产物。Web 模式下 HTTP server 直接 serve 这份 dist。
//
//go:embed all:frontend/dist
var assets embed.FS

// basePort 默认监听端口；被占用时顺延至 basePort+tryPorts。
const basePort = 7840
const tryPorts = 10

func main() {
	app := NewApp()
	app.holder.Set(context.Background())

	// 内嵌前端 dist → fs.Sub 取 frontend/dist 子树，注入 web.Server。
	dist, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		fatal("CloudPrism 启动失败", "内嵌前端 dist 失败: "+err.Error())
	}

	// Web server：API + SSE + 静态前端 + 代理流。Emit 收集器在 web.New 内注入。
	srv := web.New(app.log, app.st, app.holder, app.vault, app.files,
		app.transfer, app.settings, app.preview, app.lan, dist)

	// 单实例：若 basePort..basePort+3 已有 CloudPrism 实例（ping 应答），
	// 直接打开其界面并退出，避免多开。
	if existing := detectRunningInstance(); existing != "" {
		app.log.Info("检测到已在运行的实例，打开其界面", "url", existing)
		_ = win.OpenURL(existing)
		return
	}

	// 监听地址与访问令牌由「局域网访问档」设置决定：
	//   档位关闭（默认）→ 只绑 127.0.0.1，无令牌，行为与历史版本完全一致；
	//   档位开启       → 绑 0.0.0.0 并启用访问令牌闸门（回环来源仍免令牌）。
	// 令牌取不到时**回退为仅本机监听**（fail-closed）：宁可局域网访问不了，
	// 也不能把没有鉴权的界面暴露出去。
	host, token := "127.0.0.1", ""
	if app.lan.Enabled() {
		tok, err := app.lan.Token()
		if err != nil {
			app.log.Error("已开启局域网访问，但访问令牌不可用，本次回退为仅本机监听", "err", err)
		} else {
			host, token = "0.0.0.0", tok
		}
	}

	// 端口从 basePort 起顺延，直到找到一个可绑端口。
	port, err := listenOn(srv, host, token)
	if err != nil && host != "127.0.0.1" {
		// 局域网档绑失败（例如被安全软件拦截）：退一步保证程序仍可用。
		app.log.Error("局域网监听失败，回退为仅本机监听", "err", err)
		host, token = "127.0.0.1", ""
		port, err = listenOn(srv, host, token)
	}
	if err != nil {
		fatal("CloudPrism Web 服务启动失败", err.Error())
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	app.log.Info("CloudPrism Web 就绪", "url", url, "lan", srv.LanActive(),
		"frontend", frontendFingerprint())
	if srv.LanActive() {
		for _, u := range app.lan.ShareURLs(port) {
			app.log.Info("局域网访问地址（含访问令牌，勿外传）", "url", u)
		}
	}

	// 打开默认浏览器到 Web 界面。
	if err := win.OpenURL(url); err != nil {
		app.log.Warn("打开浏览器失败，请手动访问", "url", url, "err", err)
	}

	// goroutine 里开始服务（阻塞直到 Shutdown）。
	go func() {
		if err := srv.Serve(); err != nil && err != http.ErrServerClosed {
			app.log.Error("Web 服务异常退出", "err", err)
		}
	}()

	// 统一退出信号：托盘「退出」/ /api/app/quit / SIGINT。
	quitCh := make(chan struct{}, 1)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// 后端触发退出（/api/app/quit）与 SIGINT：tray.Run 占主 goroutine，
	// 无法直接 select srv.QuitCh()/sigCh，故用 goroutine 监听并：
	//   1) 调 tray.Quit() 让托盘消息循环结束（tray.Run 返回，主 goroutine 释放）；
	//   2) 经 quitCh 通知主流程继续优雅收尾。
	go func() {
		select {
		case <-srv.QuitCh():
		case <-sigCh:
		}
		tray.Quit()
		select {
		case quitCh <- struct{}{}:
		default:
		}
	}()

	// 托盘占主 goroutine（Windows 消息循环要求）；HTTP server 已在 goroutine。
	// onQuit 直接 signal quitCh → 主流程继续走优雅收尾（tray.Run 返回后）。
	tray.Run(
		func() { _ = win.OpenURL(url) }, // 打开界面
		func() { app.st.Lock() },        // 锁定密库
		func() {
			select {
			case quitCh <- struct{}{}:
			default:
			}
		}, // 退出
	)

	// 主流程阻塞在 quitCh：托盘「退出」/ /api/app/quit / SIGINT 三路任一触发。
	<-quitCh

	// 优雅收尾：停 HTTP（带超时）→ 锁库 → 关日志 → 移除托盘。
	app.log.Info("CloudPrism 退出中")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	app.st.Lock()
	tray.Quit()
	app.log.Info("CloudPrism 退出")
	app.closeLog()
}

// detectRunningInstance 探测 basePort..basePort+3 是否已有 CloudPrism 实例：
// GET /api/app/ping?token=probe（500ms 超时），应答 pong:probe 即认作本程序。
// 返回其 URL；无则空串。
func detectRunningInstance() string {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for p := basePort; p <= basePort+3; p++ {
		resp, err := client.Post(
			fmt.Sprintf("http://127.0.0.1:%d/api/app/ping", p),
			"application/json",
			strings.NewReader(`{"Token":"probe"}`),
		)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), "pong:probe") {
			return fmt.Sprintf("http://127.0.0.1:%d", p)
		}
	}
	return ""
}

// listenOn 从 basePort 起尝试绑 host（"127.0.0.1" 或 "0.0.0.0"），
// 最多顺延 tryPorts 次；token 透传给访问闸门（空串 = 纯本机 fail-closed 模式）。
// 返回实际监听端口。所有端口均不可绑时返回 error。
func listenOn(srv *web.Server, host string, token string) (int, error) {
	for p := basePort; p < basePort+tryPorts; p++ {
		addr, err := srv.Listen(host, p, token)
		if err != nil {
			continue // 端口被占（非本程序，detectRunningInstance 已排除本程序），顺延
		}
		// addr 形如 127.0.0.1:7840；解析端口。
		_, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return p, nil
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return p, nil
		}
		return port, nil
	}
	return 0, fmt.Errorf("无可绑端口（尝试 %d-%d 均失败）", basePort, basePort+tryPorts-1)
}

// fatal 报告致命错误并终止进程。
func fatal(title, text string) {
	log.Printf("[fatal] %s: %s", title, text)
	win.FatalMessage(title, text)
	os.Exit(1)
}

// frontendFingerprint 定义在 app.go（启动日志核对前端版本用）。
