// Package web 提供 CloudPrism 的 Web 服务模式：HTTP server + JSON API + SSE 事件。
//
// 架构（v32 起，替代 Wails 桌面 app）：
//   - HTTP server 监听本机（默认）/ 局域网（可选档，须带访问令牌）+ 端口顺延
//   - /api/* JSON API 封装 internal/bind 5 域方法
//   - /api/events SSE 推送状态帧 + 瞬时事件（替代 Wails EventsEmit）
//   - / 静态前端（内嵌 frontend/dist）
//   - /s/* /t/* /d/* 媒体、缩略图、下载——直接复用 appstate 的流式解密代理，
//     同源同端口，不再另起 127.0.0.1 代理端口
//
// 业务逻辑（internal/appstate + internal/bind + pkg/*）完全复用，不依赖 Wails。
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/bind"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/paths"
)

// Server 是 Web 服务模式的总装配：HTTP server + API + SSE + 静态前端 + 代理流。
type Server struct {
	log      *slog.Logger
	st       *appstate.State
	holder   *bind.ContextHolder
	vault    *bind.Vault
	files    *bind.Files
	transfer *bind.Transfer
	settings *bind.Settings
	preview  *bind.Preview
	lan      *bind.Lan // 局域网访问档（开关/令牌/可分享地址）
	distFS   fs.FS     // 内嵌前端 dist（main.go 注入）

	// guard 是访问闸门：回环来源放行，非回环来源要求访问令牌（局域网档）；
	// token 为空时为 fail-closed 的纯本机模式。Listen 时构造。
	guard *guard

	// SSE 事件收集器：appstate.Emit 注入此收集器，事件进 SSE 广播
	mu      sync.Mutex
	clients map[chan event]struct{}

	// 10Hz 状态帧合帧循环（替代 Wails 时代 bind.Events 的 Wails 专用循环）。
	// Listen 启动、Shutdown 停止；帧走 collect 进 SSE 广播。
	frameMu   sync.Mutex
	frameStop chan struct{}

	// quit 由 /api/app/quit 触发：前端 NavRail「退出」经此优雅关闭进程。
	quit chan struct{}

	addr string // 实际监听地址（端口冲突后可能与配置不同）
	ln   net.Listener
	srv  *http.Server

	// lanActive 表示当前进程是否真的对局域网监听（绑的不是回环地址）。
	// 与设置里的 listen/lan 分开上报：切换开关需要重启，两者可能不一致，
	// UI 必须能同时显示「已保存」与「当前生效」。
	lanActive bool
}

// frame 是每帧载荷：全局快照 + 传输任务明细（任务进度 10Hz 刷新）。
// 无活动传输时 Tasks 为 nil，减帧体积。
type frame struct {
	Snap  appstate.Snapshot   `json:"snap"`
	Tasks []appstate.TaskView `json:"tasks,omitempty"`
}

// frameInterval 合帧周期 100ms（10Hz）。
const frameInterval = 100 * time.Millisecond

// event 是 SSE 单条事件：name 作为 event: 名，data JSON 序列化。
type event struct {
	name string
	data any
}

// New 构造 Web server（未启动；Listen 才监听）。distFS 是内嵌的前端 dist（main.go 注入）。
func New(log *slog.Logger, st *appstate.State, holder *bind.ContextHolder,
	vault *bind.Vault, files *bind.Files, transfer *bind.Transfer,
	settings *bind.Settings, preview *bind.Preview, lan *bind.Lan, distFS fs.FS) *Server {
	s := &Server{
		log:      log,
		st:       st,
		holder:   holder,
		vault:    vault,
		files:    files,
		transfer: transfer,
		settings: settings,
		preview:  preview,
		lan:      lan,
		distFS:   distFS,
		clients:  make(map[chan event]struct{}),
		quit:     make(chan struct{}),
	}
	// 注入 Emit 收集器：appstate 事件 → SSE 广播
	st.SetEmit(s.collect)
	return s
}

// Listen 建立监听并返回实际地址（供 main 在启动浏览器前拿到完整 URL）。
//
// host 为 "127.0.0.1"（默认，纯本机）或 "0.0.0.0"（局域网档）。
// token 为空串表示不启用访问闸门（纯本机模式）；非空时启用 guard：
// 回环来源免令牌，其余来源必须携带令牌。
func (s *Server) Listen(host string, port int, token string) (string, error) {
	mux := http.NewServeMux()
	s.registerAPI(mux)
	s.registerStatic(mux)
	s.registerStream(mux)

	// 访问闸门始终启用（token 为空即 fail-closed 的纯本机模式）：
	// 即使外部把监听地址误配成 0.0.0.0，也不会出现无鉴权对外服务。
	s.guard = newGuard(token, s.log)
	s.srv = &http.Server{Handler: s.guard.wrap(mux), ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return "", fmt.Errorf("监听 %s:%d 失败: %w", host, port, err)
	}
	s.ln = ln
	s.addr = ln.Addr().String()
	s.lanActive = !isLoopbackAddr(s.addr)
	s.log.Info("Web 服务启动", "addr", s.addr, "lan", s.lanActive)
	// 监听成功后启动 10Hz 状态帧合帧循环（Serve 阻塞前就绪，首帧即可达 SSE）。
	s.startFrameLoop()
	return s.addr, nil
}

// Port 返回实际监听端口；未监听时返回 0。
func (s *Server) Port() int {
	_, portStr, err := net.SplitHostPort(s.addr)
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return p
}

// LanActive 报告当前进程是否对局域网监听（与设置里的开关可能不一致，
// 因为切换开关需要重启进程）。
func (s *Server) LanActive() bool { return s.lanActive }

// Serve 开始服务（阻塞直到 Shutdown）。Listen 后调用。
func (s *Server) Serve() error {
	return s.srv.Serve(s.ln)
}

// Addr 返回实际监听地址。
func (s *Server) Addr() string { return s.addr }

// Shutdown 停止 server（先停帧循环，再停 HTTP）。
func (s *Server) Shutdown(ctx context.Context) error {
	s.stopFrameLoop()
	return s.srv.Shutdown(ctx)
}

// QuitCh 返回前端退出信号通道（/api/app/quit 触发）。
func (s *Server) QuitCh() <-chan struct{} { return s.quit }

// startFrameLoop 启动 10Hz 状态帧合帧循环（幂等：重复调用以首次为准）。
func (s *Server) startFrameLoop() {
	s.frameMu.Lock()
	defer s.frameMu.Unlock()
	if s.frameStop != nil {
		return // 已启动
	}
	stop := make(chan struct{})
	s.frameStop = stop
	go func() {
		t := time.NewTicker(frameInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				f := frame{Snap: s.st.Snapshot()}
				if f.Snap.TransferActive {
					f.Tasks = s.st.Tasks()
				}
				s.collect("st:frame", f)
			case <-stop:
				return
			}
		}
	}()
}

// stopFrameLoop 停止合帧循环（幂等；未启动/已停止时无操作）。
func (s *Server) stopFrameLoop() {
	s.frameMu.Lock()
	defer s.frameMu.Unlock()
	if s.frameStop == nil {
		return
	}
	close(s.frameStop)
	s.frameStop = nil
}

/* ----------------------------------------------------------- 路由注册 */

func (s *Server) registerAPI(mux *http.ServeMux) {
	// SSE 事件流
	mux.HandleFunc("/api/events", s.handleSSE)

	// App 域
	mux.HandleFunc("/api/app/ping", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Token string }
		json.Unmarshal(body, &req)
		return "pong:" + req.Token, nil
	}))
	mux.HandleFunc("/api/app/version", s.wrapErr(func(r *http.Request) (any, error) {
		return s.version(), nil
	}))
	mux.HandleFunc("/api/app/quit", s.wrapErr(func(r *http.Request) (any, error) {
		// 延迟 200ms 让本响应先刷新，再通知 main 收尾退出。
		go func() {
			time.Sleep(200 * time.Millisecond)
			select {
			case s.quit <- struct{}{}:
			default:
			}
		}()
		return nil, nil
	}))

	// Vault 域
	mux.HandleFunc("/api/vault/state", s.wrapErr(func(r *http.Request) (any, error) { return s.vault.State(), nil }))
	mux.HandleFunc("/api/vault/open", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req appstate.OpenRequest
		json.Unmarshal(body, &req)
		return s.vault.Open(req)
	}))
	mux.HandleFunc("/api/vault/lock", s.wrapErr(func(r *http.Request) (any, error) {
		s.vault.Lock()
		return nil, nil
	}))
	mux.HandleFunc("/api/vault/recents", s.wrapErr(func(r *http.Request) (any, error) {
		return s.vault.RecentVaults(), nil
	}))
	mux.HandleFunc("/api/vault/forget", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Key string }
		json.Unmarshal(body, &req)
		return nil, s.vault.ForgetRecent(req.Key)
	}))
	mux.HandleFunc("/api/vault/listother", s.wrapErr(func(r *http.Request) (any, error) {
		return s.vault.ListOtherVaults()
	}))
	mux.HandleFunc("/api/vault/connectother", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ VaultPath, MasterPassword string }
		json.Unmarshal(body, &req)
		return nil, s.vault.ConnectOtherVault(req.VaultPath, req.MasterPassword)
	}))
	mux.HandleFunc("/api/vault/renamevault", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ NewName string }
		json.Unmarshal(body, &req)
		return nil, s.vault.RenameVault(req.NewName)
	}))
	mux.HandleFunc("/api/vault/regenrecovery", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ MasterPassword string }
		json.Unmarshal(body, &req)
		return s.vault.RegenerateRecoveryCode(req.MasterPassword)
	}))
	mux.HandleFunc("/api/vault/resume", s.wrapErr(func(r *http.Request) (any, error) {
		return s.vault.ResumePending()
	}))
	mux.HandleFunc("/api/vault/requeststats", s.wrapErr(func(r *http.Request) (any, error) {
		return nil, s.vault.RequestStats()
	}))
	mux.HandleFunc("/api/vault/baidustatus", s.wrapErr(func(r *http.Request) (any, error) {
		return s.vault.BaiduStatus(), nil
	}))
	mux.HandleFunc("/api/vault/baiduauthurl", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ AppID, AppKey string }
		json.Unmarshal(body, &req)
		return s.vault.BaiduAuthURL(req.AppID, req.AppKey)
	}))
	mux.HandleFunc("/api/vault/baidusaveauth", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ AppID, AppKey, SecretKey, SignKey, Code string }
		json.Unmarshal(body, &req)
		return nil, s.vault.BaiduSaveAuth(req.AppID, req.AppKey, req.SecretKey, req.SignKey, req.Code)
	}))
	mux.HandleFunc("/api/vault/baiduclearauth", s.wrapErr(func(r *http.Request) (any, error) {
		return nil, s.vault.BaiduClearAuth()
	}))
	mux.HandleFunc("/api/vault/chooselocaldir", s.wrapErr(func(r *http.Request) (any, error) {
		return s.vault.ChooseLocalDir()
	}))

	// Files 域
	mux.HandleFunc("/api/files/list", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Remote string }
		json.Unmarshal(body, &req)
		return s.files.List(req.Remote)
	}))
	mux.HandleFunc("/api/files/newfolder", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Parent, Name string }
		json.Unmarshal(body, &req)
		return nil, s.files.NewFolder(req.Parent, req.Name)
	}))
	mux.HandleFunc("/api/files/rename", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Remote, Name string }
		json.Unmarshal(body, &req)
		return nil, s.files.Rename(req.Remote, req.Name)
	}))
	mux.HandleFunc("/api/files/delete", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Remotes []string }
		json.Unmarshal(body, &req)
		return nil, s.files.Delete(req.Remotes)
	}))
	mux.HandleFunc("/api/files/export", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Remote string }
		json.Unmarshal(body, &req)
		return s.files.Export(req.Remote)
	}))

	// Settings 域
	mux.HandleFunc("/api/settings/get", s.wrapErr(func(r *http.Request) (any, error) { return s.settings.Get(), nil }))
	mux.HandleFunc("/api/settings/settheme", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Index int }
		json.Unmarshal(body, &req)
		return nil, s.settings.SetTheme(req.Index)
	}))
	mux.HandleFunc("/api/settings/setfontsize", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Px int }
		json.Unmarshal(body, &req)
		return nil, s.settings.SetFontSize(req.Px)
	}))
	mux.HandleFunc("/api/settings/setcache", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct {
			LimitMB int
			Path    string
		}
		json.Unmarshal(body, &req)
		return nil, s.settings.SetCache(req.LimitMB, req.Path)
	}))
	mux.HandleFunc("/api/settings/setchunksize", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ MB int }
		json.Unmarshal(body, &req)
		return nil, s.settings.SetChunkSize(req.MB)
	}))
	mux.HandleFunc("/api/settings/cacheinfo", s.wrapErr(func(r *http.Request) (any, error) { return s.settings.CacheInfo(), nil }))
	mux.HandleFunc("/api/settings/purgecache", s.wrapErr(func(r *http.Request) (any, error) { return s.settings.PurgeCache() }))
	mux.HandleFunc("/api/settings/settransfer", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ ChunkIndex, Concurrent int }
		json.Unmarshal(body, &req)
		return nil, s.settings.SetTransfer(req.ChunkIndex, req.Concurrent)
	}))
	mux.HandleFunc("/api/settings/setautolock", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Index int }
		json.Unmarshal(body, &req)
		return nil, s.settings.SetAutoLock(req.Index)
	}))
	mux.HandleFunc("/api/settings/setmaxcores", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ N int }
		json.Unmarshal(body, &req)
		return nil, s.settings.SetMaxCores(req.N)
	}))
	mux.HandleFunc("/api/settings/setsyncdir", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Dir string }
		json.Unmarshal(body, &req)
		return nil, s.settings.SetSyncDir(req.Dir)
	}))
	mux.HandleFunc("/api/settings/syncnow", s.wrapErr(func(r *http.Request) (any, error) { return s.settings.SyncNow() }))
	mux.HandleFunc("/api/settings/choosesyncdir", s.wrapErr(func(r *http.Request) (any, error) { return s.settings.ChooseSyncDir() }))
	mux.HandleFunc("/api/settings/choosecachedir", s.wrapErr(func(r *http.Request) (any, error) { return s.settings.ChooseCacheDir() }))

	// Lan 域（局域网访问档）
	//
	// 令牌由本域返回：能调到这里说明调用方已通过访问闸门（本机或已带令牌），
	// 因此不构成新的泄露面。lanUrls 里的链接已内嵌令牌，复制即用。
	mux.HandleFunc("/api/lan/status", s.wrapErr(func(r *http.Request) (any, error) {
		return s.lan.Status(s.Port(), s.LanActive()), nil
	}))
	mux.HandleFunc("/api/lan/setenabled", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		// 用 *bool 而非 bool：body 缺 on 字段与「显式传 false」必须区分开。
		// 若用 bool 且忽略解析错误，一个坏 body 会被当成 false 静默写入 ——
		// 等于「用户点开、开关自己弹回去」，正是要杜绝的展示缺口。
		var req struct{ On *bool }
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, fmt.Errorf("请求体解析失败: %w", err)
		}
		if req.On == nil {
			return nil, errors.New("缺少 on 字段")
		}
		return nil, s.lan.SetEnabled(*req.On)
	}))
	mux.HandleFunc("/api/lan/rotatetoken", s.wrapErr(func(r *http.Request) (any, error) {
		tok, err := s.lan.Rotate()
		if err != nil {
			return nil, err
		}
		return map[string]string{"token": tok}, nil
	}))

	// Transfer 域
	mux.HandleFunc("/api/transfer/upload", s.handleUpload)
	mux.HandleFunc("/api/transfer/download", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct {
			Entries  []appstate.FileEntry
			LocalDir string
		}
		json.Unmarshal(body, &req)
		return nil, s.transfer.Download(req.Entries, req.LocalDir)
	}))
	mux.HandleFunc("/api/transfer/tasks", s.wrapErr(func(r *http.Request) (any, error) { return s.transfer.Tasks(), nil }))
	mux.HandleFunc("/api/transfer/downloadurl", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct {
			Remote  string
			Display string
		}
		json.Unmarshal(body, &req)
		if req.Remote == "" {
			return nil, fmt.Errorf("缺少 remote")
		}
		url, err := s.preview.DownloadURL(req.Remote, req.Display)
		if err != nil {
			return nil, bind.Wrap(err)
		}
		return map[string]string{"url": url}, nil
	}))
	mux.HandleFunc("/api/transfer/retry", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Id int64 }
		json.Unmarshal(body, &req)
		return nil, s.transfer.Retry(req.Id)
	}))
	mux.HandleFunc("/api/transfer/cancelall", s.wrapErr(func(r *http.Request) (any, error) { return nil, s.transfer.CancelAll() }))
	mux.HandleFunc("/api/transfer/clearfinished", s.wrapErr(func(r *http.Request) (any, error) { return nil, s.transfer.ClearFinished() }))

	// Preview 域
	mux.HandleFunc("/api/preview/mediaurl", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Remote, Display string }
		json.Unmarshal(body, &req)
		return s.preview.MediaURL(req.Remote, req.Display)
	}))
	mux.HandleFunc("/api/preview/thumburl", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Remote string }
		json.Unmarshal(body, &req)
		return s.preview.ThumbURL(req.Remote)
	}))
	mux.HandleFunc("/api/preview/revoke", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Token string }
		json.Unmarshal(body, &req)
		s.preview.Revoke(req.Token)
		return nil, nil
	}))
}

// version 返回运行时版本信息（Web 模式简化：前端指纹由 main.go 注入日志）。
func (s *Server) version() string {
	return "web-mode"
}

/* ----------------------------------------------------------- SSE 事件 */

// collect 是 appstate.Emit 注入的收集器：事件 → SSE 广播给所有客户端。
func (s *Server) collect(name string, data any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- event{name: name, data: data}:
		default: // 客户端慢，丢弃（帧 10Hz 丢一帧无碍）
		}
	}
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE 不支持", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan event, 64)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
	}()

	// 立即发一帧真实快照（前端连上即有数据；不能发空帧——
	// 前端对每帧 JSON.parse，空 data 会抛异常触发 boot-err）。
	f := frame{Snap: s.st.Snapshot()}
	if f.Snap.TransferActive {
		f.Tasks = s.st.Tasks()
	}
	dataBytes, _ := json.Marshal(f)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", "st:frame", string(dataBytes))
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			var dataBytes []byte
			if ev.data != nil {
				dataBytes, _ = json.Marshal(ev.data)
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, string(dataBytes))
			flusher.Flush()
		}
	}
}

/* ----------------------------------------------------------- 静态前端 */

func (s *Server) registerStatic(mux *http.ServeMux) {
	// distFS 已是 frontend/dist 子树（main.go 注入 fs.Sub 后的结果）
	fileServer := http.FileServer(http.FS(s.distFS))
	// 静态资源统一走 gzip（SSE / API / 媒体流不经过此路由，见 compress.go）
	serve := withGzip(fileServer)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// /api/* /s/ /t/ /d/ 等由各自路由模式接管；此处只处理静态资源。
		rel := strings.TrimPrefix(r.URL.Path, "/")
		if rel != "" {
			// 文件存在则直接服务；不存在（SPA 历史路由）回退 index.html。
			if _, err := fs.Stat(s.distFS, rel); err == nil {
				// 带内容 hash 的构建产物可强缓存一年；但 .html 必须例外 ——
				// .html 文件名里没有内容 hash。注意 /index.html 会被
				// http.FileServer 301 重定向到 ./，这条 301 若带上 immutable，
				// 浏览器会把它当永久重定向缓存；而 dist 里若出现非 index 的
				// .html，FileServer 会直接服务、不重定向，那才是真被钉死一年。
				if strings.EqualFold(filepath.Ext(rel), ".html") {
					w.Header().Set("Cache-Control", "no-store")
				} else {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				serve.ServeHTTP(w, r)
				return
			}
		}
		// index.html / 未知路径：no-store 强制每次取最新（防旧前端缓存）
		w.Header().Set("Cache-Control", "no-store")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		serve.ServeHTTP(w, r2)
	})
}

/* ----------------------------------------------------------- 代理流 */

func (s *Server) registerStream(mux *http.ServeMux) {
	// /s/* 媒体预览 + /t/* 缩略图 + /d/* 下载。
	// 三者都直接复用 appstate 当前连接的流式解密代理 —— 不再另起
	// 127.0.0.1 代理端口，因此媒体流量与本机/局域网访问共用同一个端口
	// 与同一道鉴权闸门（远端浏览器拿到的也是同源相对路径）。
	//
	// streamer 每请求取一次：连接建立/锁库/换连时 appstate 会整体替换
	// 代理实例，缓存指针会拿到过期对象。
	streamer := func(w http.ResponseWriter, r *http.Request) {
		proxy := s.st.StreamProxy()
		if proxy == nil {
			http.NotFound(w, r)
			return
		}
		proxy.Handler().ServeHTTP(w, r)
	}
	mux.HandleFunc("/s/", streamer)
	mux.HandleFunc("/t/", streamer)
	mux.HandleFunc("/d/", streamer)
}

/* ----------------------------------------------------------- API 包装 */

// wrapErr 包装无 body 的 API：取 handler 返回 (any, error)，error 转 JSON。
func (s *Server) wrapErr(h func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := h(r)
		s.writeJSON(w, v, err)
	}
}

// wrapJSON 包装带 body 的 API：读 body → handler → JSON。
func (s *Server) wrapJSON(h func(*http.Request, []byte) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		v, err := h(r, body)
		s.writeJSON(w, v, err)
	}
}

// handleUpload 处理浏览器 multipart 上传：文件先落临时目录（保留原始
// 文件名），再经 appstate.UploadPaths 入传输队列（复用续传/重试/进度）。
//
// 浏览器 FormData 用 <input type=file> 或拖放产生 File 对象，与 Wails 时代
// 的本地路径语义不同：此处理器把 multipart 内容 staging 成临时文件作为
// 任务 LocalPath，上传完成后由任务清理逻辑删除。
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	// 32MB 内存在内存，更大自动溢出到系统临时目录。
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.writeJSON(w, nil, fmt.Errorf("解析上传表单失败: %w", err))
		return
	}
	remoteDir := r.FormValue("remoteDir")
	fileHeaders := r.MultipartForm.File["files"]
	if len(fileHeaders) == 0 {
		s.writeJSON(w, nil, fmt.Errorf("未选择文件"))
		return
	}

	tmpBase, _ := paths.TempDir(true)
	if tmpBase == "" {
		tmpBase = os.TempDir()
	}
	// 每个文件一个独立子目录，避免同名文件互相覆盖。
	stageDir, err := os.MkdirTemp(tmpBase, "cp-upload-*")
	if err != nil {
		s.writeJSON(w, nil, fmt.Errorf("创建暂存目录失败: %w", err))
		return
	}
	defer os.RemoveAll(stageDir)

	localPaths := make([]string, 0, len(fileHeaders))
	for _, fh := range fileHeaders {
		src, err := fh.Open()
		if err != nil {
			s.writeJSON(w, nil, fmt.Errorf("打开上传文件失败: %w", err))
			return
		}
		dst, err := os.Create(filepath.Join(stageDir, filepath.Base(filepath.Clean(fh.Filename))))
		if err != nil {
			src.Close()
			s.writeJSON(w, nil, fmt.Errorf("创建暂存文件失败: %w", err))
			return
		}
		_, cpErr := io.Copy(dst, src)
		src.Close()
		if cpErr != nil {
			dst.Close()
			s.writeJSON(w, nil, fmt.Errorf("写入暂存文件失败: %w", cpErr))
			return
		}
		if err := dst.Close(); err != nil {
			s.writeJSON(w, nil, fmt.Errorf("关闭暂存文件失败: %w", err))
			return
		}
		localPaths = append(localPaths, dst.Name())
	}

	if err := s.st.UploadPaths(r.Context(), localPaths, remoteDir); err != nil {
		s.writeJSON(w, nil, bind.Wrap(err))
		return
	}
	s.writeJSON(w, map[string]any{"enqueued": len(localPaths)}, nil)
}

func (s *Server) writeJSON(w http.ResponseWriter, v any, err error) {
	if err != nil {
		wrapped := bind.Wrap(err)
		apiErr, ok := wrapped.(*bind.ApiError)
		if !ok {
			apiErr = &bind.ApiError{Code: "internal", Message: wrapped.Error()}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(apiErr)
		return
	}
	if v == nil {
		w.Write([]byte(`{"ok":true}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
