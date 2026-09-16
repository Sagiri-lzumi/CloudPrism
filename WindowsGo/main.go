// CloudPrism Go 版入口（v32 起 Web 服务模式）。
//
// 架构转变：从 Wails 桌面 app 变「HTTP server + 系统托盘」守护进程。
// 本文件装配依赖图（NewApp 复用）→ 起 internal/web HTTP server（API+SSE+静态前端）
// → 起系统托盘（打开浏览器/切换监听档/锁定/退出）→ 阻塞等退出信号。
//
// 业务逻辑（internal/appstate + internal/bind + pkg/*）完全复用，不依赖 Wails。
// 前端 Vue 组件逻辑复用，数据层改 fetch/SSE（不依赖 wailsjs runtime）。
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/platform/win"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/web"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
)

// assets 内嵌前端产物。Web 模式下 HTTP server 直接 serve 这份 dist。
//
//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// 构造依赖图（NewApp 复用：数据目录→日志→设置→传输队列→状态→绑定域）。
	// Wails 模式的 OnStartup 已不适用；holder 用 Background context 即可
	// （对话框 API 在 Web 模式下由前端 Web 替代，不再需要前台句柄）。
	app := NewApp()
	app.holder.Set(context.Background())

	// 内嵌前端 dist → fs.Sub 取 frontend/dist 子树，注入 web.Server。
	dist, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		fatal("CloudPrism 启动失败", "内嵌前端 dist 失败: "+err.Error())
	}

	// Web server：API + SSE + 静态前端 + 代理流。Emit 收集器在 web.New 内注入。
	srv := web.New(app.log, app.st, app.holder, app.vault, app.files,
		app.transfer, app.settings, app.preview, dist)

	// 先同步 Listen 拿实际地址（避免 goroutine 竞态读到空 addr），再开浏览器。
	// 绑 "::"（IPv6 通配，Windows dual-stack 同时接受 IPv4 + IPv6），让 Edge 访问
	// localhost/127.0.0.1/::1 都通。局域网暴露问题下轮通过网络档解决。
	if _, err := srv.Listen("::", 7840); err != nil {
		fatal("CloudPrism Web 服务启动失败", err.Error())
	}
	// url 用 127.0.0.1（强制 IPv4，避免 Edge 显示 localhost 导致解析问题）。
	url := "http://127.0.0.1:7840"
	app.log.Info("CloudPrism Web 就绪", "url", url, "frontend", frontendFingerprint())

	// 打开默认浏览器到 Web 界面。
	if err := win.OpenURL(url); err != nil {
		app.log.Warn("打开浏览器失败，请手动访问", "url", url, "err", err)
	}

	// goroutine 里开始服务（阻塞直到 Shutdown）。
	go func() {
		if err := srv.Serve(); err != nil {
			app.log.Error("Web 服务异常退出", "err", err)
		}
	}()

	// 系统托盘（后续阶段；当前先阻塞等 Ctrl+C / 信号退出）。
	app.log.Info("CloudPrism 启动完成", "mode", "web", "url", url)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	// 退出：锁库收尾 + 关日志。
	app.log.Info("CloudPrism 退出中")
	app.st.Lock()
	app.shutdown(context.Background())
	app.log.Info("CloudPrism 退出")
	app.closeLog()
}

// fatal 报告致命错误并终止进程。
func fatal(title, text string) {
	log.Printf("[fatal] %s: %s", title, text)
	win.FatalMessage(title, text)
	os.Exit(1)
}

// frontendFingerprint 定义在 app.go（启动日志核对前端版本用）。

// 防止未使用 import 警告（paths/slog 在 NewApp 内部用，但本文件编译期可能误报）
var _ = paths.EnsureDataDir
var _ = slog.Default
