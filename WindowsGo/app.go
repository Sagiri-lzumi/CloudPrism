package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"regexp"
	"runtime"
	"strings"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/bind"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/loggingx"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/platform/win"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/settings"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/transfer"
)

// App 是 Wails 绑定宿主：承担生命周期钩子与 App 域（Quit/Version 等
// 全局操作），并持有装配好的依赖图。业务编排都在 internal/appstate
// （唯一有状态对象）与 internal/bind（5 个域 struct），本层不写逻辑。
type App struct {
	ctx      context.Context
	holder   *bind.ContextHolder
	log      *slog.Logger
	closeLog func()

	st     *appstate.State
	events *bind.Events

	// 5 个绑定域：域间互不依赖，共享同一 State 与 ContextHolder
	vault    *bind.Vault
	files    *bind.Files
	transfer *bind.Transfer
	settings *bind.Settings
	preview  *bind.Preview
}

// dpapiProtector 把 internal/platform/win 的包级 DPAPI 函数适配成
// storage.BaiduProtector 接口（落盘前缀 DPAPI，与 Python 端一致，可互读）。
type dpapiProtector struct{}

func (dpapiProtector) Protect(d []byte) ([]byte, error)   { return win.Protect(d) }
func (dpapiProtector) Unprotect(d []byte) ([]byte, error) { return win.Unprotect(d) }
func (dpapiProtector) Scheme() string                     { return "DPAPI" }

// NewApp 构造依赖图：数据目录 → 日志 → 设置 → 传输队列 → 应用状态 →
// 绑定域。wails build 的绑定生成阶段同样会执行本函数（bindings_mode.go
// 只分流需要 GUI 的步骤），故这里不能有窗口/对话框等前台操作。
func NewApp() *App {
	paths.EnsureDataDir()

	logger, closeLog := loggingx.New(paths.DataDir())

	store, err := settings.Open(paths.SettingsFile())
	if err != nil {
		closeLog()
		fatal("CloudPrism 启动失败", "设置存储初始化失败: "+err.Error())
	}

	// 百度凭证存储：DPAPI 加密落盘 data/baidu.json（与 Python 版同路径同格式）
	baiduCred := storage.NewBaiduCredStore(paths.BaiduCredentialFile(), dpapiProtector{})

	st := appstate.New(appstate.Config{
		Store:     store,
		Queue:     transfer.New(),
		Log:       logger,
		BaiduCred: baiduCred,
	})

	holder := bind.NewContextHolder()
	return &App{
		holder:   holder,
		log:      logger,
		closeLog: closeLog,
		st:       st,
		vault:    bind.NewVault(st, holder),
		files:    bind.NewFiles(st, holder),
		transfer: bind.NewTransfer(st, holder),
		settings: bind.NewSettings(st, holder),
		preview:  bind.NewPreview(st, holder),
	}
}

// startup 由 Wails 在窗口创建后调用：注入前台 context 并接上事件管线。
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.holder.Set(ctx)
	// 前端产物指纹先落日志：用户反馈「界面不对」时凭 cloudprism.log 即可
	// 自证所跑前端版本（曾发生便携目录错放旧 exe 导致改版看不到的教训）
	a.log.Info("前端产物指纹", "assets", frontendFingerprint())
	// 系统级文件拖放（列表/预览页内部的拖放由前端自行处理）
	wruntime.OnFileDrop(ctx, a.fileDropped)
	// 状态帧合帧器（10Hz）在 State 构造之后才有前台 context 可用，
	// 就绪后把事件出口接上（见 State.SetEmit 注释的装配顺序说明）
	a.events = bind.NewEvents(ctx, a.st)
	a.st.SetEmit(a.events.Forward)
	a.log.Info("CloudPrism 启动完成", "mode", runModeLabel())
}

// shutdown 收尾：停合帧 → 锁库收尾（落盘未完成任务/停代理/清会话）→ 关日志。
func (a *App) shutdown(ctx context.Context) {
	wruntime.OnFileDropOff(ctx)
	if a.events != nil {
		a.st.SetEmit(nil) // 退出阶段不再向已销毁的窗口广播
		a.events.Close()
	}
	a.st.Lock()
	a.log.Info("CloudPrism 退出")
	a.closeLog()
}

// frontendFingerprint 从内嵌 dist 的 index.html 提取产物文件名
// （index-<hash>.js/css），启动日志据此可核对界面实际加载的前端版本。
// 提取失败或未命中（未来 Wails 压缩内嵌资源时）返回 unknown，不阻断启动。
func frontendFingerprint() string {
	raw, err := fs.ReadFile(assets, "frontend/dist/index.html")
	if err != nil {
		return "unknown"
	}
	re := regexp.MustCompile(`assets/(index-[\w-]+\.(?:css|js))`)
	seen := map[string]bool{}
	names := []string{}
	for _, m := range re.FindAllSubmatch(raw, -1) {
		name := string(m[1])
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "unknown"
	}
	return strings.Join(names, " + ")
}

// fileDropped 处理系统级文件拖放：转成事件给前端（载荷为本地路径列表，
// 前端按当前目录发起上传）。
func (a *App) fileDropped(_ int, _ int, paths []string) {
	if a.events != nil {
		a.events.Dropped(paths)
	}
}

// runModeLabel 运行时/绑定生成模式的日志标识（排障时一眼区分）。
func runModeLabel() string {
	if generatingBindings {
		return "bindings"
	}
	return "runtime"
}

// Ping 原样回传 token，用于验证前后端绑定往返是否打通。
//
// 骨架阶段 S1b spike 未能在沙箱内验证的 Mojo IPC 通道，沙箱外运行本程序
// 时由骨架页启动即调用完成验证；阶段 6 重写前端后可移除。
func (a *App) Ping(token string) string { return "pong:" + token }

// Version 汇总运行时诊断信息（骨架页与「关于」入口展示）。
//
// 末尾附内嵌前端产物指纹（index-<hash>.css/js 文件名）：用户在「关于」
// 即可核对界面实际加载的前端版本，对照 dist/assets/ 目录里的文件名，
// 一眼判断「跑的 exe 是否带最新前端」（v15/v17 反复出现用户删了 UDF
// 仍看到旧 UI，根因之一就是无法自助核对前端版本）。
func (a *App) Version() string {
	wv := win.RuntimeVersion()
	if wv == "" {
		wv = "未检测到"
	}
	return fmt.Sprintf("Go %s · WebView2 %s · CGO_ENABLED=%s · %s/%s · 前端 %s",
		runtime.Version(), wv, envOr("CGO_ENABLED", "unset"),
		runtime.GOOS, runtime.GOARCH, frontendFingerprint())
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
