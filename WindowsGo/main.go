// CloudPrism Go 版入口。
//
// 本文件只负责「装配」：前置环境探测 → 构造应用状态 → 注册绑定 →
// 交给 Wails 运行，不写任何业务逻辑。业务编排在 internal/appstate，
// 对外接口在 internal/bind，与 GUI 无关的核心在 pkg/。
package main

import (
	"embed"
	"log"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/platform/win"
)

// assets 内嵌前端产物。
//
// frontend/dist 必须入库（仓库根 .gitignore 已为它开白名单例外），
// 否则 fresh clone 下这条 embed 指令会因找不到匹配文件而编译失败。
//
//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// 绑定生成阶段这段代码同样会真实执行，故必须先分流（见 bindings_mode.go）
	if !generatingBindings {
		ensureWebView2Runtime()
	}

	// 构造依赖图（internal/appstate.State + internal/bind 的 5 个域 struct），
	// 全部注册进 Bind —— 域间互不依赖，避免单个巨型绑定对象撑爆
	// wailsjs 生成物；宿主 App 只留生命周期钩子与全局操作。
	app := NewApp()

	if err := wails.Run(runtimeOptions(app)); err != nil {
		fatal("CloudPrism 启动失败", err.Error())
	}
}

// ensureWebView2Runtime 探测 WebView2 运行时，缺失时给出安装指引后退出。
//
// 不做前置探测的话，wails.Run 只会抛出一句 0x800700aa 之类的 HRESULT，
// 用户完全无从下手。
func ensureWebView2Runtime() {
	ver := win.RuntimeVersion()
	if ver == "" {
		fatal("CloudPrism 无法启动",
			"未检测到 Microsoft Edge WebView2 运行时。\n\n"+
				"请安装后重试：\n"+
				"https://developer.microsoft.com/microsoft-edge/webview2/")
		return // fatal 内部 os.Exit，编译器不识别其不返回，故需显式收尾
	}
	log.Printf("[startup] WebView2 Runtime %s", ver)
}

// fatal 报告致命错误并终止进程。
//
// GUI 子系统程序（-H windowsgui）没有控制台，不弹窗就表现为「双击无反应」；
// 但绑定生成阶段是在命令行里跑的，弹窗反而会卡住构建，故按模式分流。
func fatal(title, text string) {
	log.Printf("[fatal] %s: %s", title, text)
	if !generatingBindings {
		win.FatalMessage(title, text)
	}
	os.Exit(1)
}

// runtimeOptions 集中描述窗口与运行时行为，便于与 WindowsPy 的
// gui/main_window.py 窗口参数逐项对照。
func runtimeOptions(app *App) *options.App {
	winOpts := &windows.Options{}
	if !generatingBindings {
		// 显式指定 UDF：默认值落在 %AppData%\<exe 名>，与 CloudPrism
		// 「绿色便携、数据随 exe 走」的约定冲突（详见 win.UserDataDir）
		winOpts.WebviewUserDataPath = win.UserDataDir()
	}

	return &options.App{
		Title:     "CloudPrism",
		Width:     1280,
		Height:    800,
		MinWidth:  960,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// 前端挂载前的底色，取 Fluent 浅色主题的应用背景，避免启动瞬间
		// 闪白/闪黑（阶段 6 落地主题后与 CSS 变量对齐）
		BackgroundColour: &options.RGBA{R: 0xFA, G: 0xFA, B: 0xFA, A: 0xFF},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		// 系统级拖放在 startup 里经 wruntime.OnFileDrop 注册（见 app.go）
		Windows: winOpts,
		Bind: []interface{}{
			app,
			// 5 域：Vault/Files/Transfer/Settings/Preview
			app.vault, app.files, app.transfer, app.settings, app.preview,
		},
	}
}
