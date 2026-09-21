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

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/bind"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/loggingx"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/platform/win"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/secret"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/settings"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/transfer"
)

// App 是依赖图装配宿主：承担数据目录→日志→设置→传输队列→应用状态→
// 各绑定域的构造顺序。业务编排都在 internal/appstate（唯一有状态
// 对象）与 internal/bind（Vault/Files/Transfer/Settings/Preview/
// LocalFS/Lan 七个域 struct），本层不写逻辑。
//
// v33 起 Web 模式为唯一形态：事件管线与生命周期由 internal/web Server
// 接管，不再有 Wails OnStartup/OnShutdown 钩子；退出路径由 main.go 的
// 托盘/信号统一处理（见 main.go）。
type App struct {
	holder   *bind.ContextHolder
	log      *slog.Logger
	closeLog func()

	st *appstate.State
	// 绑定域：域间互不依赖，共享同一 State 与 ContextHolder
	vault    *bind.Vault
	files    *bind.Files
	transfer *bind.Transfer
	settings *bind.Settings
	preview  *bind.Preview
	// localfs 无状态（只读主机本地文件系统），不持 State/ContextHolder
	localfs *bind.LocalFS
	lan     *bind.Lan

	// lanToken 是局域网访问令牌的加密存储（data/lan_token）。
	// 令牌属秘密，按 pkg/settings 顶部约定不入设置存储，故单独落盘。
	lanToken *secret.File
}

// dpapiProtector 把 internal/platform/win 的包级 DPAPI 函数适配成
// storage.BaiduProtector 接口（落盘前缀 DPAPI，与 Python 端一致，可互读）。
type dpapiProtector struct{}

func (dpapiProtector) Protect(d []byte) ([]byte, error)   { return win.Protect(d) }
func (dpapiProtector) Unprotect(d []byte) ([]byte, error) { return win.Unprotect(d) }
func (dpapiProtector) Scheme() string                     { return "DPAPI" }

// NewApp 构造依赖图：数据目录 → 日志 → 设置 → 传输队列 → 应用状态 →
// 绑定域。不能有窗口/对话框等前台操作（main.go 的 Web 启动路径先于
// 任何 HTTP 请求执行本函数）。
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

	// 局域网访问令牌：同一套 DPAPI 保护器，落盘 data/lan_token（秘密不入设置存储）
	lanToken := secret.NewFile(paths.LanTokenFile(), dpapiProtector{})

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
		localfs:  bind.NewLocalFS(),
		lan:      bind.NewLan(st, lanToken),
		lanToken: lanToken,
	}
}

// frontendFingerprint 从内嵌 dist 的 index.html 提取产物文件名
// （index-<hash>.js/css），启动日志据此可核对界面实际加载的前端版本。
// 提取失败或未命中时返回 unknown，不阻断启动。
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

// Version 汇总运行时诊断信息（「关于」入口展示）。
//
// 末尾附内嵌前端产物指纹（index-<hash>.css/js 文件名）：用户在「关于」
// 即可核对界面实际加载的前端版本，对照 dist/assets/ 目录里的文件名，
// 一眼判断「跑的 exe 是否带最新前端」。
func (a *App) Version() string {
	return fmt.Sprintf("Go %s · CGO_ENABLED=%s · %s/%s · 前端 %s",
		runtime.Version(), envOr("CGO_ENABLED", "unset"),
		runtime.GOOS, runtime.GOARCH, frontendFingerprint())
}

// envOr 读取环境变量，未设置时返回兜底值。
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Ping 原样回传 token，用于验证前后端绑定往返是否打通（单实例探测也用此端点）。
func (a *App) Ping(token string) string { return "pong:" + token }

// compile-time: 确保被 NewApp 间接引用的包在 web-only 构建下不被裁剪
var _ = context.Background
