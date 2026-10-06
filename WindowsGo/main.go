// CloudPrism Go 版入口（v33 起 Web 服务模式为唯一形态）。
//
// 架构：HTTP server（API+SSE+静态前端+代理流）+ 系统托盘守护进程。
// 业务逻辑（internal/appstate + internal/bind + pkg/*）完全复用，不依赖 Wails。
// 前端 Vue 组件逻辑复用，数据层改 fetch/SSE（不依赖 wailsjs runtime）。
//
// 启动顺序：依赖图 → 读 data/config.json（起始端口/地址/顺延范围）→
// 内嵌前端 dist → 按「局域网访问档」设置决定监听地址
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
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/platform/win"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/tray"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/web"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/config"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
)

// assets 内嵌前端产物。Web 模式下 HTTP server 直接 serve 这份 dist。
//
//go:embed all:frontend/dist
var assets embed.FS

// 监听端口不再硬编码：起始端口与顺延范围来自程序目录旁的
// data/config.json（见 pkg/config），缺省值等价于历史的 7840 + 顺延 10。
//
// 单实例探测与实际监听共用同一候选列表（cfg.Ports()）：探测范围若小于
// 监听范围，会漏掉落在尾部端口上的已在运行实例，导致多开。

func main() {
	// 启动配置（data/config.json）：起始端口 / 监听地址 / 顺延范围。
	// 先于一切副作用读取（Load 是纯函数，不碰日志/设置），因为单实例闸门
	// 需要它与默认端口段一起构成探测范围。
	// 缺失或损坏一律回退默认值；每条回退都写进日志，不让用户写错的配置
	// 静默失效（否则「我明明配了 8080 怎么还是 7840」无从排查）。
	launchCfg, cfgProblems := config.Load(paths.LaunchConfigFile())
	ports := launchCfg.Ports()

	// 单实例闸门必须在 NewApp() **之前**：NewApp 会打开 data/logs 下的日志
	// 文件并初始化设置存储，两个进程同时持有这些文件会互相踩踏
	//（loggingx 轮转要 os.Remove + os.Rename 整条链，另一方正在写的文件
	// 可能被删掉；设置存储是「最后写入者胜」，会静默丢改动）。
	//
	// 这里锁的是**数据目录**而不是端口：真正的约束是「一个数据目录只能有
	// 一个进程」。端口扫描只是它的一个代理指标，且自端口可配之后不再可靠
	// —— 改了 config.json 的 port 重启，新进程扫的是新端口段，探测不到
	// 仍在旧端口上运行的实例，于是两个进程共享同一个 data/。
	//
	// 锁建立失败（环境异常）时 fail-open 照常启动：单实例约束不该让用户
	// 完全打不开程序。
	if primary, lockErr := win.AcquireInstanceLock(paths.DataDir()); lockErr != nil {
		log.Printf("[warn] 单实例锁不可用，跳过单实例检查: %v", lockErr)
	} else if !primary {
		// 已有实例在跑。端口可配后无法确定它用的是哪一段配置，故默认段与
		// 当前配置段都扫一遍；找到就把它的界面打开，找不到则明确告知。
		if url := detectRunningInstance(mergePorts(config.Default().Ports(), ports)); url != "" {
			_ = win.OpenURL(url)
		} else {
			win.FatalMessage("CloudPrism 已在运行",
				"检测到本程序已在运行（同一数据目录只允许一个实例）。\n\n"+
					"请使用已打开的程序界面；若确实需要启动新实例，请先从托盘退出旧实例。")
		}
		return
	}

	app := NewApp()
	app.holder.Set(context.Background())
	for _, p := range cfgProblems {
		app.log.Warn("启动配置回退", "problem", p, "file", paths.LaunchConfigFile())
	}

	// 内嵌前端 dist → fs.Sub 取 frontend/dist 子树，注入 web.Server。
	dist, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		fatal("CloudPrism 启动失败", "内嵌前端 dist 失败: "+err.Error())
	}

	// Web server：API + SSE + 静态前端 + 代理流。Emit 收集器在 web.New 内注入。
	srv := web.New(app.log, app.st, app.holder, app.vault, app.files,
		app.transfer, app.settings, app.preview, app.localfs, app.lan, dist)

	// 单实例检查已在 main 开头完成（命名互斥体锁数据目录），此处不再按
	// 端口扫描：端口只是数据目录的代理指标，配置改了端口就失效。走到这里
	// 说明本进程已持有数据目录锁，是按定义的主实例。

	// 监听地址与访问令牌由「局域网访问档」设置决定：
	//   档位关闭（默认）→ 只绑 127.0.0.1，无令牌，行为与历史版本完全一致；
	//   档位开启       → 绑 0.0.0.0 并启用访问令牌闸门（回环来源仍免令牌）。
	// 令牌取不到时**回退为仅本机监听**（fail-closed）：宁可局域网访问不了，
	// 也不能把没有鉴权的界面暴露出去。
	//
	// 「哪些接口可达」与「远端是否要鉴权」是同一条安全决策的两面，由
	// listen/lan 这一个开关统一裁决 —— config.json 因此刻意不含监听地址
	//（见 pkg/config 的说明），配置文件无法单方面把监听放大到全网卡。
	host, token := config.DefaultHost, ""
	if app.lan.Enabled() {
		tok, err := app.lan.Token()
		if err != nil {
			app.log.Error("已开启局域网访问，但访问令牌不可用，本次回退为仅本机监听", "err", err)
		} else {
			host, token = "0.0.0.0", tok
		}
	}

	// 端口从配置的起始端口起顺延，直到找到一个可绑端口。
	port, err := listenOn(srv, host, token, ports)
	if err != nil && host != config.DefaultHost {
		// 局域网档绑失败（例如被安全软件拦截）：退一步保证程序仍可用。
		app.log.Error("局域网监听失败，回退为仅本机监听", "err", err)
		host, token = config.DefaultHost, ""
		port, err = listenOn(srv, host, token, ports)
	}
	if err != nil {
		fatal("CloudPrism Web 服务启动失败", err.Error())
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	app.log.Info("CloudPrism Web 就绪", "url", url, "lan", srv.LanActive(),
		"frontend", frontendFingerprint())
	if srv.LanActive() {
		for _, u := range app.lan.ShareURLs(port) {
			app.log.Info("局域网访问地址（带令牌的完整链接见设置页）", "url", sanitizeShareURL(u))
		}
	}

	// 打开默认浏览器到 Web 界面。开发脚本（CP_NO_BROWSER=1）会禁止这一步，
	// 避免后端把浏览器带到内嵌旧 dist；开发时应访问 Vite 前端地址。
	if os.Getenv("CP_NO_BROWSER") == "1" {
		app.log.Info("开发模式：不自动打开后端界面", "url", url)
	} else if err := win.OpenURL(url); err != nil {
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
	// onQuit 只负责通知主流程：**结束托盘消息循环是 tray 包的事**（点「退出」即
	// systray.Quit）。少了那一步 tray.Run 永不返回，下面那句 <-quitCh 就永远执行
	// 不到 —— 用户看到的就是「点托盘『退出』没反应、进程退不掉」（2026-09-21 修）。
	tray.Run(
		func() { _ = win.OpenURL(url) }, // 打开界面
		func() { app.st.LockVault() },   // 锁定密库
		// 退出：托盘消息循环已由 tray 包结束，这里只通知主流程收尾。
		func() {
			select {
			case quitCh <- struct{}{}:
			default:
			}
		},
	)

	// 主流程阻塞在 quitCh：托盘「退出」/ /api/app/quit / SIGINT 三路任一触发。
	<-quitCh

	// 优雅收尾：停 HTTP（带超时）→ 锁库 → 关日志 → 移除托盘。
	app.log.Info("CloudPrism 退出中")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	app.st.LockVault()
	tray.Quit()
	app.log.Info("CloudPrism 退出")
	app.closeLog()
}

// mergePorts 合并两组端口并去重，保持各组内部顺序（先 a 后 b）。
//
// 用途：单实例探测无法预知「已在运行的那个实例」用的是哪份配置，故把
// 默认端口段与当前配置段一起探测，避免改了 port 之后互相看不见。
func mergePorts(a, b []int) []int {
	seen := make(map[int]bool, len(a)+len(b))
	out := make([]int, 0, len(a)+len(b))
	for _, group := range [][]int{a, b} {
		for _, p := range group {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// detectRunningInstance 在候选端口上探测是否已有 CloudPrism 实例：
// POST /api/app/ping（500ms 超时，body 带 token=probe），应答 pong:probe
// 即认作本程序。返回其 URL；无则空串。
//
// ports 必须与 listenOn 用的是**同一个列表**（cfg.Ports()）：探测范围若
// 窄于监听范围，会漏掉落在尾部端口上的运行实例，导致多开。
//
// 用 POST 而非 GET：/api/* 现在一律只接受 POST（见 web.apiMethodOK），
// GET 会被 405 挡下 —— 探测失败会让第二个实例以为端口空着而另起一个服务。
func detectRunningInstance(ports []int) string {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for _, p := range ports {
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

// listenOn 按 ports 顺序尝试绑 host（"127.0.0.1" 或 "0.0.0.0"），
// 范围与单实例探测一致；token 透传给访问闸门（空串 = 纯本机 fail-closed
// 模式）。返回实际监听端口。所有端口均不可绑时返回 error。
func listenOn(srv *web.Server, host string, token string, ports []int) (int, error) {
	for _, p := range ports {
		addr, err := srv.Listen(host, p, token)
		if err != nil {
			continue // 端口被占（非本程序，detectRunningInstance 已排除本程序），顺延
		}
		// addr 形如 127.0.0.1:7840；解析出实际端口。
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
	return 0, fmt.Errorf("无可绑端口（尝试 %v 均失败），可在 %s 里改 port / port_range",
		ports, paths.LaunchConfigFile())
}

// sanitizeShareURL 去掉分享链接里的查询串（即访问令牌）后再写日志。
//
// 令牌是局域网访问的唯一凭据，拿到即等于拿到全部界面能力；而日志文件是
// 明文落盘、还会轮转保留三份 —— 把带 token 的链接写进去等于把凭据持久化
// 到磁盘。界面里本来就有可一键复制的完整链接，日志留地址即可。
func sanitizeShareURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

// fatal 报告致命错误并终止进程。
func fatal(title, text string) {
	log.Printf("[fatal] %s: %s", title, text)
	win.FatalMessage(title, text)
	os.Exit(1)
}

// frontendFingerprint 定义在 app.go（启动日志核对前端版本用）。
