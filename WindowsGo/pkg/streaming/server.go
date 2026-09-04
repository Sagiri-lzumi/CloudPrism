// Package streaming 提供令牌化流式解密代理：本地 HTTP 服务把加密容器
// 按 Range 请求解密为明文流，喂给 WebView2 的 <video>/<audio>，明文不落盘。
//
// 对照 WindowsPy/src/cloudprism/streaming/proxy_server.py，差异与动机：
//
//   - 端点令牌化：/s/{token}/{display-name} 与 /t/{token}，替代 Python 的
//     裸路径 GET —— 密文路径不暴露、展示名供 MIME 推断、注册时一次性
//     拉取文件头/大小/密钥（handler 零后端往返），详见 token.go 头部。
//   - 流式窗口：响应被 256KiB 步长边解边发（WriteChunk），而非 Python 的
//     整段解密后一次性写出；首字节延迟降到单个窗口的下载时间，大视频 +
//     网盘后端下尤其明显（Python 受 MAX_RESPONSE_BYTES 截断单次响应，
//     但响应内部仍是全量缓冲）。
//   - 明文内存写后清零：见 memguard.go。
//
// 错误语义逐条对齐 Python：416 不回退整文件（尾部探测防全量下载）、
// 空文件 206 bytes 0-0/0、未知路径 404、后端故障 502、客户端中断静默。
package streaming

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/session"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/storage"
)

// 代理常量。
const (
	// MaxResponseBytes 单次 GET 响应上限（明文段）：开口区间
	// （bytes=0-）按此截断为多个有界 206，避免一次性拉取并全量解密整个
	// 密文文件 —— 与 Python MAX_RESPONSE_BYTES 同值（proxy_server.py:41）。
	MaxResponseBytes = 2 << 20

	// WriteChunk 流式写出步长：每个窗口独立下载、解密、写出 + Flush，
	// 首字节延迟 ≈ 一个窗口的下载时间（Python 是一次性全量缓冲）。
	WriteChunk = 256 << 10

	// headFetchLen 注册时下载的文件头前缀长度（默认头 51 字节，
	// 64 足够覆盖 salt/iv 均 16 字节的情形，对照 decryptor.py:48）。
	headFetchLen = 64

	// tokenHexLen 令牌十六进制长度（16 字节随机数）。
	tokenHexLen = 32

	routeStream = "s" // /s/{token}/{display-name}
	routeThumb  = "t" // /t/{token}
)

// ErrNotVaultFile 目标存在但不是合法加密容器时返回（绑定层据此提示
// 「文件损坏」而不是「网络故障」；Python 端同类归 HeaderError → 404）。
var ErrNotVaultFile = errors.New("不是合法的加密容器")

// Server 是令牌化流式解密代理。一个 Server 绑定一个 Session + Backend
// （连接/锁库时重建）；Handler 与 Start 二选一（测试用 httptest 包
// Handler，绑定层用 Start 起真实监听）。
type Server struct {
	sess    *session.Session
	backend storage.Backend
	reg     *Registry

	mu   sync.RWMutex // 保护 httpSrv/addr
	http *http.Server
	addr string // 实际监听地址 "host:port"
}

// NewServer 构造代理（sess/backend 为 nil 的校验延迟到注册时）。
func NewServer(sess *session.Session, backend storage.Backend) *Server {
	return &Server{
		sess:    sess,
		backend: backend,
		reg:     NewRegistry(),
	}
}

// Registry 暴露注册表（锁库清理/诊断用；通常经 Server 方法操作）。
func (s *Server) Registry() *Registry { return s.reg }

// RegisterStream 注册流端点令牌并预取文件头/大小/派生密钥。
//
// 同 (stream, remotePath) 重复注册幂等返回既有 entry；远端文件被覆盖后
// 需先 Revoke 再注册（幂等语义建立在文件不变假设上，见 Registry 注释）。
//
// 错误分类（绑定层据此提示用户）：
//
//	errors.Is(err, storage.ErrNotFound) → 文件不存在（前端 404 文案）
//	errors.Is(err, ErrNotVaultFile)     → 不是合法加密容器
//	其它（含 ErrBackend）               → 后端故障（前端 502 文案）
func (s *Server) RegisterStream(ctx context.Context, remotePath, displayName string) (*Entry, error) {
	if s.sess == nil || s.backend == nil {
		return nil, errors.New("streaming: 代理未绑定 Session/Backend")
	}
	// 幂等快路径
	if e := s.reg.find(KindStream, remotePath); e != nil {
		return e, nil
	}

	// 取文件头并解析 —— 这一步同时裁决「文件不存在 / 非密文 / 后端故障」，
	// 与 Python get_header 的 404/502 语义逐条一致（proxy_server.py:82-99）
	head, err := s.backend.DownloadRange(ctx, remotePath, 0, headFetchLen-1)
	if err != nil {
		return nil, classifyBackendErr(err, "读取文件头失败")
	}
	header, err := cryptox.ParseBytes(head)
	if err != nil {
		// 下载成功但解析失败：非密文/已损坏（Python HeaderError → 404）
		return nil, fmt.Errorf("%w: %v", ErrNotVaultFile, err)
	}
	size, err := s.backend.GetSize(ctx, remotePath)
	if err != nil {
		return nil, classifyBackendErr(err, "读取文件大小失败")
	}
	if size < header.CipherOffset() {
		// 密文比文件头还短：容器损坏（Python 会算出负的明文总量，
		// 这里注册阶段就拦截，语义更严但无行为差异）
		return nil, fmt.Errorf("%w: 文件大小 %d 小于文件头", ErrNotVaultFile, size)
	}
	key, err := s.sess.DeriveKey(header.Salt)
	if err != nil {
		return nil, fmt.Errorf("streaming: 派生密钥失败: %w", err)
	}
	token, err := newToken()
	if err != nil {
		return nil, fmt.Errorf("streaming: 生成令牌失败: %w", err)
	}
	return s.reg.Add(&Entry{
		Kind:        KindStream,
		Token:       token,
		RemotePath:  remotePath,
		DisplayName: displayName,
		Header:      header,
		CipherSize:  size,
		Key:         key,
		Created:     time.Now(),
	})
}

// RegisterThumb 注册缩略图端点令牌，payload 为已生成的 JPEG 字节
// （由绑定层用 pkg/thumb.Fetch 生成后注入，本包不感知图像格式）。
//
// 幂等语义同 RegisterStream。payload 非明文（缩略图），Revoke 时不清零。
func (s *Server) RegisterThumb(remotePath, displayName string, payload []byte, contentType string) (*Entry, error) {
	if e := s.reg.find(KindThumb, remotePath); e != nil {
		return e, nil
	}
	token, err := newToken()
	if err != nil {
		return nil, fmt.Errorf("streaming: 生成令牌失败: %w", err)
	}
	return s.reg.Add(&Entry{
		Kind:        KindThumb,
		Token:       token,
		RemotePath:  remotePath,
		DisplayName: displayName,
		Payload:     payload,
		ContentType: contentType,
		Created:     time.Now(),
	})
}

// Handler 返回 HTTP 处理器（供 httptest 或自定义 http.Server 挂载）。
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.route)
}

// Start 在 host:port 上启动真实监听（port=0 自动分配），返回实际地址。
// 代理只应监听 127.0.0.1 —— 令牌与解密能力不能暴露给局域网。
func (s *Server) Start(host string, port int) (string, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return "", err
	}
	httpSrv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.mu.Lock()
	s.http = httpSrv
	s.addr = ln.Addr().String()
	s.mu.Unlock()
	go httpSrv.Serve(ln) //nolint:errcheck // 关闭走 Close，Serve 错误无需上报
	return s.addr, nil
}

// Addr 返回实际监听地址（Start 前为空）。
func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.addr
}

// BaseURL 返回代理根 URL（如 http://127.0.0.1:54321），供绑定层拼接
// entry.URLPath()。Start 前调用返回空串。
func (s *Server) BaseURL() string {
	if addr := s.Addr(); addr != "" {
		return "http://" + addr
	}
	return ""
}

// Revoke 吊销一个令牌。
func (s *Server) Revoke(token string) { s.reg.Revoke(token) }

// RevokeAll 清空全部令牌（锁库/切换后端时调用，派生密钥一并清零）。
func (s *Server) RevokeAll() { s.reg.RevokeAll() }

// Close 停止监听并清空令牌。幂等。
func (s *Server) Close() error {
	s.reg.RevokeAll()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.http == nil {
		return nil
	}
	err := s.http.Close()
	s.http = nil
	s.addr = ""
	return err
}

// classifyBackendErr 把注册阶段的存储错误折叠成语义哨兵：404 原样透出
// （代理与绑定层都要区分「文件不存在」与「后端故障」），其余包成
// ErrBackend —— 与 storage/errors.go 的注释约定一致。
func classifyBackendErr(err error, what string) error {
	if errors.Is(err, storage.ErrNotFound) {
		return err
	}
	return fmt.Errorf("streaming: %s: %w: %v", what, storage.ErrBackend, err)
}
