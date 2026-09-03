package main

import (
	"context"
	"fmt"
	"os"
	"runtime"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/platform/win"
)

// App 是骨架阶段的绑定宿主占位。
//
// 阶段 5 会把它替换为 internal/bind 下的 5 个域 struct
// （VaultAPI / FilesAPI / TransferAPI / SettingsAPI / PreviewAPI），
// 每个域各持 *appstate.State 且方法数 ≤ 12 —— 直接 Bind 一个 60+ 方法的
// 巨型 struct 会让 Wails 生成单个无法维护的 wailsjs/go/main/App.js。
// 届时本文件只保留生命周期钩子。
type App struct {
	ctx context.Context
}

// NewApp 构造占位宿主。
func NewApp() *App { return &App{} }

// startup 由 Wails 在窗口创建后调用。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// shutdown 由 Wails 在退出前调用，负责收尾（停代理、落盘传输队列等）。
func (a *App) shutdown(context.Context) {}

// Ping 原样回传 token，用于验证前后端绑定往返是否打通。
//
// 这是阶段 2 spike 中 S1b 未能验证的一项（IDE 沙箱禁止 Chromium
// 建立 Mojo IPC 通道）；骨架页启动即调用它，因此在沙箱外运行本程序
// 就等于顺手完成了 S1b 验证。
func (a *App) Ping(token string) string { return "pong:" + token }

// Version 汇总运行时诊断信息，骨架页启动即展示。
func (a *App) Version() string {
	wv := win.RuntimeVersion()
	if wv == "" {
		wv = "未检测到"
	}
	return fmt.Sprintf("Go %s · WebView2 %s · CGO_ENABLED=%s · %s/%s",
		runtime.Version(), wv, envOr("CGO_ENABLED", "unset"),
		runtime.GOOS, runtime.GOARCH)
}

// Quit 由前端主动退出应用。
func (a *App) Quit() { wruntime.Quit(a.ctx) }

// envOr 读取环境变量，未设置时返回兜底值。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
