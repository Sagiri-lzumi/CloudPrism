// compress.go —— 静态资源 gzip 传输中间件。
//
// 动机：局域网档下手机 / 别的电脑经 HTTP 拉前端，dist 里 index-*.js 明文
// 637KB、index-*.css 明文 53KB，首屏要传 690KB；弱网下打开明显偏慢。
// Go 侧原先直接 http.FileServer，没有任何压缩中间件。实测 gzip 后
// JS 189KB（29%）/ CSS 9KB（16%），首屏降到 198KB。
//
// 设计要点：
//   - 只挂在静态资源路由上（registerStatic），SSE / API / 媒体流一律不碰；
//   - Content-Type 由 http.FileServer 自己写，压缩决策只能推迟到 WriteHeader
//     那一刻，按实际 Content-Type 判断可压缩性；
//   - 每个请求压缩完必须 Close() 冲刷 gzip 尾部，否则响应会被截断；
//   - gzip.Writer 走 sync.Pool，避免每请求分配约 32KB 的压缩窗口。
package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// gzipPool 复用 gzip.Writer（每个带约 32KB 压缩窗口，不值得每请求 new 一个）。
var gzipPool = sync.Pool{
	New: func() any { return gzip.NewWriter(io.Discard) },
}

// compressibleTypes 需要压缩的 Content-Type 前缀。
// 已自带压缩的格式（woff2 / png / jpeg / 音视频）不在表内——再压一遍几乎不减小，
// 纯属浪费 CPU。
var compressibleTypes = []string{
	"text/",
	"application/javascript",
	"application/json",
	"application/manifest+json",
	"application/xml",
	"image/svg+xml",
}

// compressible 判断该 Content-Type 是否值得 gzip。
func compressible(contentType string) bool {
	// 去掉 charset 等参数，只按 MIME 主体判断
	mt, _, _ := strings.Cut(contentType, ";")
	mt = strings.ToLower(strings.TrimSpace(mt))
	// 空类型不敢妄断；SSE 必须逐帧透传，绝不能整体压缩
	if mt == "" || mt == "text/event-stream" {
		return false
	}
	for _, p := range compressibleTypes {
		if strings.HasPrefix(mt, p) {
			return true
		}
	}
	return false
}

// acceptsGzip 解析 Accept-Encoding，判断客户端是否接受 gzip（尊重 q=0 的显式拒绝）。
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		token, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		switch strings.ToLower(strings.TrimSpace(token)) {
		case "gzip", "*":
			if !qZero(params) {
				return true
			}
		}
	}
	return false
}

// qZero 判断参数串里有没有 q=0（表示显式拒绝该编码）。
func qZero(params string) bool {
	for _, p := range strings.Split(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "q") && strings.TrimSpace(v) == "0" {
			return true
		}
	}
	return false
}

// gzipResponseWriter 惰性压缩：WriteHeader 时才知道 Content-Type，
// 因此「是否压缩」与 gzip.Writer 的获取都推迟到那一刻。
type gzipResponseWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	compress bool // 本次响应是否走压缩
	wrote    bool // 是否已写过响应头
}

// Unwrap 让 http.ResponseController 及各路 ResponseWriter 能力探测能穿透本包装。
func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.wrote {
		return
	}
	g.wrote = true
	// 仅对 2xx、可压缩类型、且尚未被上游压过的响应启用压缩
	// （http.Header 没有 Has 方法，用 Get 判空）
	if code >= 200 && code < 300 && g.Header().Get("Content-Encoding") == "" &&
		compressible(g.Header().Get("Content-Type")) {
		g.compress = true
		// 长度已变，必须删掉；留着会让客户端按原长度截断或挂起
		g.Header().Del("Content-Length")
		// 压缩后的字节范围不再对应原始文件，Range 续传对本响应无意义，
		// 必须撤掉这个广告，否则客户端可能按压缩前的偏移来续传而拿到错位数据
		g.Header().Del("Accept-Ranges")
		g.Header().Set("Content-Encoding", "gzip")
		g.gz = gzipPool.Get().(*gzip.Writer)
		g.gz.Reset(g.ResponseWriter)
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.wrote {
		// 下游没显式 WriteHeader 就写正文，等价于 200
		g.WriteHeader(http.StatusOK)
	}
	if g.compress {
		return g.gz.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

// Flush 透传，并先把 gzip 缓冲刷出去，避免流式响应被压在压缩窗口里。
func (g *gzipResponseWriter) Flush() {
	if g.compress && g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Close 结束压缩流并把 Writer 归还池；必须在 handler 返回后调用，
// 否则 gzip 尾部（CRC + 长度）不会写出，响应会被判定为截断。
func (g *gzipResponseWriter) Close() error {
	if !g.compress || g.gz == nil {
		return nil
	}
	err := g.gz.Close()
	g.gz.Reset(io.Discard)
	gzipPool.Put(g.gz)
	g.gz = nil
	return err
}

// withGzip 给静态资源响应加 gzip。
//
// 入口处跳过两种情况：
//   - 客户端没声明接受 gzip；
//   - 带 Range 头 —— http.FileServer 的断点逻辑依赖 Content-Range/Content-Length
//     的字节语义，压缩后长度不再对应原始文件，会直接破坏 Range 续传。
//
// 其余情况（已有 Content-Encoding / 非 2xx / 类型不可压缩）由 WriteHeader 判定。
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r) || r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}
		// 告知缓存与代理：同一 URL 依 Accept-Encoding 有不同表示
		w.Header().Add("Vary", "Accept-Encoding")
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer func() { _ = gw.Close() }()
		next.ServeHTTP(gw, r)
	})
}
