// Package web 提供 CloudPrism 的 Web 服务模式：HTTP server + JSON API + SSE 事件。
//
// 架构（v32 起，替代 Wails 桌面 app）：
//   - HTTP server 监听本机/局域网/公网（三档可切换）+ 端口可配
//   - /api/* JSON API 封装 internal/bind 5 域方法
//   - /api/events SSE 推送状态帧 + 瞬时事件（替代 Wails EventsEmit）
//   - / 静态前端（内嵌 frontend/dist）
//   - /s/* /t/* 复用 pkg/streaming 代理流（预览/缩略图令牌）
//
// 业务逻辑（internal/appstate + internal/bind + pkg/*）完全复用，不依赖 Wails。
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/bind"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/streaming"
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
	stream   *streaming.Server // 媒体/缩略图代理（连接时重建）
	distFS   fs.FS             // 内嵌前端 dist（main.go 注入）

	// SSE 事件收集器：appstate.Emit 注入此收集器，事件进 SSE 广播
	mu      sync.Mutex
	clients map[chan event]struct{}

	addr string // 实际监听地址（端口冲突后可能与配置不同）
	ln   net.Listener
	srv  *http.Server
}

// event 是 SSE 单条事件：name 作为 event: 名，data JSON 序列化。
type event struct {
	name string
	data any
}

// New 构造 Web server（未启动；Start 才监听）。distFS 是内嵌的前端 dist（main.go 注入）。
func New(log *slog.Logger, st *appstate.State, holder *bind.ContextHolder,
	vault *bind.Vault, files *bind.Files, transfer *bind.Transfer,
	settings *bind.Settings, preview *bind.Preview, distFS fs.FS) *Server {
	s := &Server{
		log:      log,
		st:        st,
		holder:    holder,
		vault:     vault,
		files:     files,
		transfer:  transfer,
		settings:  settings,
		preview:   preview,
		distFS:    distFS,
		clients:   make(map[chan event]struct{}),
	}
	// 注入 Emit 收集器：appstate 事件 → SSE 广播
	st.SetEmit(s.collect)
	return s
}

// SetStreaming 连接建立后注入代理流 server（锁库/换连时重建）。
func (s *Server) SetStreaming(sess *session.Session, backend storage.Backend) {
	s.stream = streaming.NewServer(sess, backend)
}

// Start 启动 HTTP server（阻塞直到 Shutdown）。
// Listen 同步建立监听并返回地址（供 main 在启动浏览器前拿到完整 URL）。
func (s *Server) Listen(host string, port int) (string, error) {
	mux := http.NewServeMux()
	s.registerAPI(mux)
	s.registerStatic(mux)
	s.registerStream(mux)

	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return "", fmt.Errorf("监听 %s:%d 失败: %w", host, port, err)
	}
	s.ln = ln
	s.addr = ln.Addr().String()
	s.log.Info("Web 服务启动", "addr", s.addr)
	return s.addr, nil
}

// Serve 开始服务（阻塞直到 Shutdown）。Listen 后调用。
func (s *Server) Serve() error {
	return s.srv.Serve(s.ln)
}

// Start 兼容旧入口：Listen + Serve（goroutine 内调用时主线程应立即读 Addr 则竞态，请用 Listen + Serve 分开）。
func (s *Server) Start(host string, port int) error {
	if _, err := s.Listen(host, port); err != nil {
		return err
	}
	return s.Serve()
}

// Addr 返回实际监听地址。
func (s *Server) Addr() string { return s.addr }

// Shutdown 停止 server。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
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
		var req struct{ LimitMB int; Path string }
		json.Unmarshal(body, &req)
		return nil, s.settings.SetCache(req.LimitMB, req.Path)
	}))
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

	// Transfer 域
	mux.HandleFunc("/api/transfer/upload", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		// TODO 阶段4：multipart。当前占位。
		return nil, nil
	}))
	mux.HandleFunc("/api/transfer/download", s.wrapJSON(func(r *http.Request, body []byte) (any, error) {
		var req struct {
			Entries []appstate.FileEntry
			LocalDir string
		}
		json.Unmarshal(body, &req)
		return nil, s.transfer.Download(req.Entries, req.LocalDir)
	}))
	mux.HandleFunc("/api/transfer/tasks", s.wrapErr(func(r *http.Request) (any, error) { return s.transfer.Tasks(), nil }))
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

	// 立即发一帧当前状态（前端连上即有数据）
	s.collect("st:frame", nil) // 触发一帧（实际数据由合帧器提供，这里仅唤醒）

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
	mux.Handle("/", http.FileServer(http.FS(s.distFS)))
}

/* ----------------------------------------------------------- 代理流 */

func (s *Server) registerStream(mux *http.ServeMux) {
	// /s/* 媒体预览 + /t/* 缩略图（连接后 s.stream 非 nil 才可用）
	mux.HandleFunc("/s/", func(w http.ResponseWriter, r *http.Request) {
		if s.stream == nil {
			http.NotFound(w, r)
			return
		}
		s.stream.Handler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/t/", func(w http.ResponseWriter, r *http.Request) {
		if s.stream == nil {
			http.NotFound(w, r)
			return
		}
		s.stream.Handler().ServeHTTP(w, r)
	})
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
