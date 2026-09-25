// Package web 提供 CloudPrism 的 Web 服务模式：HTTP server + JSON API + SSE 事件。
//
// 架构（v32 起，替代 Wails 桌面 app）：
//   - HTTP server 监听本机（默认）/ 局域网（可选档，须带访问令牌）+ 端口顺延
//   - /api/* JSON API 封装 internal/bind 各域方法（含本机目录浏览 LocalFS）
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
	"path"
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
	localfs  *bind.LocalFS // 本机目录浏览（网页版目录选择器的数据源）
	lan      *bind.Lan     // 局域网访问档（开关/令牌/可分享地址）
	distFS   fs.FS         // 内嵌前端 dist（main.go 注入）

	// guard 是访问闸门：回环来源放行，非回环来源要求访问令牌（局域网档）；
	// token 为空时为 fail-closed 的纯本机模式。Listen 时构造。
	guard *guard

	// SSE 事件收集器：appstate.Emit 注入此收集器，事件进 SSE 广播
	mu      sync.Mutex
	clients map[chan event]struct{}

	// 10Hz 状态帧合帧循环（替代 Wails 时代的 bind.Events 专用循环）。
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
// 队列为空时 Tasks 为 nil，减帧体积。
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
	settings *bind.Settings, preview *bind.Preview, localfs *bind.LocalFS,
	lan *bind.Lan, distFS fs.FS) *Server {
	s := &Server{
		log:      log,
		st:       st,
		holder:   holder,
		vault:    vault,
		files:    files,
		transfer: transfer,
		settings: settings,
		preview:  preview,
		localfs:  localfs,
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

	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return "", fmt.Errorf("监听 %s:%d 失败: %w", host, port, err)
	}
	s.ln = ln
	s.addr = ln.Addr().String()
	s.lanActive = !isLoopbackAddr(s.addr)
	// 访问闸门始终启用（token 为空即 fail-closed 的纯本机模式）：
	// 即使外部把监听地址误配成 0.0.0.0，也不会出现无鉴权对外服务。
	// Host/Origin 白名单需要实际端口（端口顺延后可能与配置不同）与是否
	// 局域网档，故在监听成功后构造。
	s.guard = newGuard(token, s.Port(), s.lanActive, s.log)
	s.srv = &http.Server{Handler: s.guard.wrap(mux), ReadHeaderTimeout: 10 * time.Second}
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

// buildFrame 组装一帧状态载荷（合帧循环与 SSE 首帧共用，避免两处判定漂移）。
//
// 任务明细的携带条件不能只看 TransferActive：终态任务（done/failed/cancelled）
// 按设计保留在队列里供「传输」页重试与清空（见 appstate.ClearFinished），
// 只在有在飞任务时才带上，前端 ui.tasks 就会被逐帧置空 —— 于是传输页在没有
// 传输的每一刻都是空态，重试/清空两个动作永远点不到（实测：上传一完成，列表
// 立刻空掉、两个按钮双双置灰，而队列里那两个任务还在）。
// 故改为「在飞传输 或 队列非空」都带上；队列由用户主动清空，清空后自然停发，
// 空转时不会长期背着任务明细（每任务约 200B × 10Hz）。
func (s *Server) buildFrame() frame {
	f := frame{Snap: s.st.Snapshot()}
	if tasks := s.st.Tasks(); f.Snap.TransferActive || len(tasks) > 0 {
		f.Tasks = tasks
	}
	return f
}

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
				s.collect("st:frame", s.buildFrame())
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

	// LocalFS 域（网页版目录选择器）。
	//
	// 这三条取代了原先的 /api/{vault,settings}/choose*dir —— 旧实现由本进程弹
	// IFileOpenDialog，而 Show(owner=0) 没有属主窗口，对话框会跑到浏览器窗口
	// 后面（用户看到的是「后台莫名跳出个框」）。现在改为：后端只负责列目录，
	// 选择器整个跑在网页里，选完把**绝对路径**回传。
	//
	// 三个端点都登记在 auth.go 的 localOnlyPaths：它们能读出主机任意目录名，
	// 与「只有本机能弹原生框」的原能力边界必须一致。见 platform/win/localfs.go。
	mux.HandleFunc("/api/fs/drives", s.wrapErr(func(r *http.Request) (any, error) {
		return s.localfs.Drives()
	}))
	mux.HandleFunc("/api/fs/dirs", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Path string }
		json.Unmarshal(body, &req)
		return s.localfs.ListDir(req.Path)
	}))
	mux.HandleFunc("/api/fs/mkdir", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct{ Parent, Name string }
		json.Unmarshal(body, &req)
		return s.localfs.MakeDir(req.Parent, req.Name)
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
		// Dir 由前端选定（网页版目录选择器），后端不再弹原生对话框。
		var req struct{ Remote, Dir string }
		json.Unmarshal(body, &req)
		return s.files.Export(req.Remote, req.Dir)
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
	dataBytes, _ := json.Marshal(s.buildFrame())
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

// maxJSONBodyBytes 是 JSON 端点的请求体上限。
//
// 这些端点的入参都是几十字节的结构体；此前用无上限的 io.ReadAll 读 body，
// 意味着任何一个能访问本服务的客户端只要持续发送数据就能把进程内存吃光。
// 上传端点（multipart）不走这里，故不受此上限影响。
const maxJSONBodyBytes = 1 << 20

// apiMethodOK 校验 /api 端点的方法：只认 POST。
//
// 为什么钉死 POST：状态变更端点若接受 GET，就绕过了 originAllowed 的跨源
// 校验（该校验只覆盖非 GET 方法），而跨源 GET 恰恰是「简单请求」——不带
// Origin、不需要预检、响应读不回也不影响副作用。于是本机浏览器里打开的
// 任意网页都能盲打 /api/vault/lock、/api/app/quit、/api/settings/purgecache、
// /api/transfer/cancelall …（回环来源本来还免令牌，等于零门槛）。
// 前端 lib/api.ts 的 call() 本来就全部走 POST，故这里直接收紧到 POST。
func apiMethodOK(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodPost {
		return true
	}
	w.Header().Set("Allow", "POST")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusMethodNotAllowed)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code":    "internal",
		"message": "该接口只接受 POST 请求",
	})
	return false
}

// wrapErr 包装无 body 的 API：取 handler 返回 (any, error)，error 转 JSON。
func (s *Server) wrapErr(h func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiMethodOK(w, r) {
			return
		}
		v, err := h(r)
		s.writeJSON(w, v, err)
	}
}

// wrapJSON 包装带 body 的 API：读 body（有上限）→ handler → JSON。
func (s *Server) wrapJSON(h func(*http.Request, []byte) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiMethodOK(w, r) {
			return
		}
		var body []byte
		if r.Body != nil {
			b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
			if err != nil {
				s.writeJSON(w, nil, fmt.Errorf("请求体读取失败（上限 %d 字节）: %w", maxJSONBodyBytes, err))
				return
			}
			body = b
		}
		v, err := h(r, body)
		s.writeJSON(w, v, err)
	}
}

// handleUpload 处理浏览器 multipart 上传：文件先落暂存目录（保留原始
// 文件名与**相对路径**），再经 appstate.UploadPaths 入传输队列。
//
// 浏览器 FormData 用 <input type=file> / webkitdirectory 或拖放产生 File 对象，
// 与 Wails 时代的本地路径语义不同：此处理器把 multipart 内容 staging 成
// 暂存文件作为任务 LocalPath。**文件夹上传靠 paths 字段还原目录结构**——
// paths 与 files 是同序平行数组，第 i 项是第 i 个文件的相对路径（如
// `photos/2026/a.jpg`）。暂存目录按该相对路径镜像成嵌套目录，于是
// appstate.UploadPaths 走目录展开路径时 walk 出的 rel 就是真实相对路径，
// 逐段加密与逐层 Mkdir 全部复用既有实现。
//
// 不传 paths（旧客户端/脚本调用）时退化为按文件名平铺，与历史行为一致。
//
// 暂存文件的回收**不在这里做**：任务入队是异步的，本函数返回时任务尚未读取
// 本地文件。回收由任务终态回调 appstate.reapStagedUpload 负责；只有入队失败
// 的分支才由本函数兜底清理。
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

	// 相对路径清单（与 files 同序）。解析失败直接拒绝，避免静默丢目录结构。
	var rels []string
	if raw := r.FormValue("paths"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &rels); err != nil {
			s.writeJSON(w, nil, fmt.Errorf("解析上传路径清单失败: %w", err))
			return
		}
	}

	tmpBase, _ := paths.TempDir(true)
	if tmpBase == "" {
		tmpBase = os.TempDir()
	}
	// 每个文件一个独立子目录，避免同名文件互相覆盖。
	stageDir, err := os.MkdirTemp(tmpBase, appstate.StagedUploadDirPrefix)
	if err != nil {
		s.writeJSON(w, nil, fmt.Errorf("创建暂存目录失败: %w", err))
		return
	}
	// 入队成功前由本函数兜底清理；成功后回收权移交任务终态回调。
	enqueued := false
	defer func() {
		if !enqueued {
			_ = os.RemoveAll(stageDir)
		}
	}()

	localPaths := make([]string, 0, len(fileHeaders))
	for i, fh := range fileHeaders {
		// 原始上报路径（可能不存在：客户端没带 paths 字段）。先取出来再判，
		// 否则报错分支里写 rels[i] 会在 i 越界时 panic —— 那是不可信输入。
		rawRel := ""
		if i < len(rels) {
			rawRel = rels[i]
		}
		rel, err := resolveStageRel(rels, i, fh.Filename)
		if err != nil {
			s.writeJSON(w, nil, fmt.Errorf("上传路径非法 %q: %w", rawRel, err))
			return
		}

		dstPath := filepath.Join(stageDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			s.writeJSON(w, nil, fmt.Errorf("创建暂存子目录失败: %w", err))
			return
		}

		src, err := fh.Open()
		if err != nil {
			s.writeJSON(w, nil, fmt.Errorf("打开上传文件失败: %w", err))
			return
		}
		dst, err := os.Create(dstPath)
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
		localPaths = append(localPaths, dstPath)
	}

	// 交整棵暂存根目录：UploadPaths 会 walk 出每个文件的真实相对路径，
	// 逐段加密后按层 Mkdir 到远端，目录结构因此完整保留。
	if err := s.st.UploadPaths(r.Context(), []string{stageDir}, remoteDir); err != nil {
		s.writeJSON(w, nil, bind.Wrap(err))
		return
	}
	enqueued = true
	s.writeJSON(w, map[string]any{"enqueued": len(localPaths)}, nil)
}

// resolveStageRel 得出第 i 个上传文件的暂存相对路径。
//
// 优先取 paths[i]（清洗后）；该下标不存在、或清洗后为空串（客户端没上报路径信息）
// 时退回 multipart 里的原始文件名，等价于平铺上传。文件名同样过 filepath.Base，
// 防止构造出带目录分隔符的文件名越出暂存根。
//
// 非法路径返回错误：调用方整体拒绝本次上传，不做「丢掉这段路径继续传」的降级，
// 否则用户会得到一棵缺目录的密库树却毫无提示。
func resolveStageRel(rels []string, i int, filename string) (string, error) {
	rel := ""
	if i < len(rels) {
		cleaned, err := sanitizeUploadRel(rels[i])
		if err != nil {
			return "", err
		}
		rel = cleaned
	}
	if rel != "" {
		return rel, nil
	}
	base := filepath.Base(filepath.Clean(filename))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "", errors.New("无法从文件名推出可用名称")
	}
	return base, nil
}

// sanitizeUploadRel 清洗浏览器上报的相对路径。
//
// 这是**不可信输入**：可能来自被篡改的客户端，可能含 `..`、绝对路径、盘符、
// NUL。任何一段非法都返回错误，由调用方整体拒绝本次上传——宁可报错也不能
// 静默丢掉目录结构（那正是 v38 前的老毛病）。
//
// 返回 POSIX 风格（/ 分隔）的相对路径；空串表示"无可用的路径信息"，调用方
// 按文件名平铺处理。
func sanitizeUploadRel(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if strings.ContainsRune(raw, 0) {
		return "", errors.New("路径含 NUL")
	}
	// 统一分隔符后再判定，防止 Windows 风格反斜杠绕过检查
	p := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if p == "" {
		return "", nil
	}
	if strings.HasPrefix(p, "/") {
		return "", errors.New("路径为绝对路径")
	}
	// 盘符（C:/…）与 UNC（//server/share）都以「根」起始，一律拒绝
	if len(p) >= 2 && p[1] == ':' {
		return "", errors.New("路径含盘符")
	}

	segs := make([]string, 0, 8)
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
			continue // 冗余段直接丢弃
		case "..":
			return "", errors.New("路径含上跳段 ..")
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		return "", nil
	}
	clean := path.Clean(strings.Join(segs, "/"))
	// path.Clean 之后仍以 .. 开头说明构造异常，兜底再拦一次
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("路径清洗后仍为上跳路径")
	}
	return clean, nil
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
