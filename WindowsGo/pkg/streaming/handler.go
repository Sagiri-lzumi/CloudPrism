package streaming

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/cryptox"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/pipeline"
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"
)

// errContainerShort 表示解密窗口取到的密文不足（远端容器被截断/覆盖），
// 与后端故障区分开：前者确定无法继续，后者可能瞬时。
var errContainerShort = errors.New("streaming: 远端密文不足，容器不完整或已损坏")

// setCommonHeaders 输出代理共用响应头。
//
// CORS：v33 前代理是独立端口、与页面跨源，故需放行 fetch 拉取；v35 起
// 媒体链路已改为同源相对路径（/s/ /t/ /d/ 直接挂在主 mux 上），该头不再
// 有调用方依赖，保留只为兼容旧缓存与外部直连。端点 URL 带一次性令牌、
// 无 Cookie/凭据，放开 * 无实际安全面（记录于 docs/ARCHITECTURE 差异清单）。
func setCommonHeaders(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("X-Content-Type-Options", "nosniff")
}

// route 是代理入口：只认 GET/HEAD 与 /s/、/t/ 两种端点形态。
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	// 全部响应（含错误）统一带公共头，先于任何分支设置
	setCommonHeaders(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 用 EscapedPath 切分：display-name 可能含中文/空格（Path 已解码，
	// 用它切会把解码后的 / 与真实分隔符混在一起），token 纯 hex，
	// 两种编码态下一致。
	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	kindStr, tail, ok := strings.Cut(rest, "/")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	token := tail
	if i := strings.IndexByte(token, '/'); i >= 0 {
		token = token[:i]
	}
	if len(token) != tokenHexLen {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	e := s.reg.Get(token)
	if e == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	switch {
	case kindStr == routeStream && e.Kind == KindStream:
		s.serveStream(w, r, e)
	case kindStr == routeThumb && e.Kind == KindThumb:
		s.serveThumb(w, r, e)
	case kindStr == routeDownload && e.Kind == KindDownload:
		s.serveDownload(w, r, e)
	default:
		// 端点与令牌类型不匹配（拿流令牌打 /t/ 等）
		writeError(w, http.StatusNotFound, "not found")
	}
}

// serveThumb 输出缩略图端点：注册时注入的 JPEG 一次写出（5-15KB，
// 无流式必要）。
func (s *Server) serveThumb(w http.ResponseWriter, r *http.Request, e *Entry) {
	h := w.Header()
	h.Set("Content-Type", e.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(e.Payload)))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	// 客户端中断忽略（缩略图短，正常不会发生）
	_, _ = w.Write(e.Payload)
}

// serveStream 处理媒体流的 GET/HEAD。
//
// 状态码语义对齐 proxy_server.py:180-245：
//
//	HEAD            → 200 + Content-Length=明文总量（播放器探时长用）
//	空文件          → 206 + Content-Range: bytes 0-0/0
//	Range 越界      → 416 + Content-Range: bytes */total（不回退整文件）
//	正常            → 206 + 明文段（受 MaxResponseBytes 截断）
//	后端故障        → 首窗口前 502；头已发后断流（见下）
//
// 与 Python 的差异（有意）：Python 先全量下载解密再一次性响应，任何失败
// 都可回 502；Go 逐 256KiB 窗口边解边发（WriteChunk），只有首窗口失败时
// 响应头尚未发出、可以改写状态码，中途窗口失败只能断流 —— 播放器感知
// Content-Length 不足会自动重试整个 Range，语义与 502 等价。
func (s *Server) serveStream(w http.ResponseWriter, r *http.Request, e *Entry) {
	hl := e.Header.CipherOffset()
	total := pipeline.PlaintextTotal(hl, e.CipherSize)

	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Type", MIMEForDisplayName(e.DisplayName))
	h.Set("Cache-Control", "no-store")

	if r.Method == http.MethodHead {
		h.Set("Content-Length", strconv.FormatInt(total, 10))
		w.WriteHeader(http.StatusOK)
		return
	}
	// 空文件：206 且 Content-Range 0-0/0（Python 原样语义）
	if total <= 0 {
		h.Set("Content-Range", "bytes 0-0/0")
		h.Set("Content-Length", "0")
		w.WriteHeader(http.StatusPartialContent)
		return
	}

	rng, ok := ParseRangeHeader(r.Header.Get("Range"), total)
	if !ok {
		writeError(w, http.StatusRequestedRangeNotSatisfiable, "",
			"Content-Range", "bytes */"+strconv.FormatInt(total, 10))
		return
	}
	// 截断到单次响应上限；Content-Range 反映实际发送段，总长不变，
	// 播放器按续请拼接（proxy_server.py:216 同因）
	end := min(rng.End, rng.Start+MaxResponseBytes-1)

	ctr, err := cryptox.NewCTR(e.Key[:], e.Header.IV)
	if err != nil {
		// 密钥/IV 非法在注册时不可能发生；防御性兜底
		writeError(w, http.StatusBadGateway, "backend error")
		return
	}

	// 首窗口：取到且解密成功才发响应头（否则可回 502）
	firstEnd := min(rng.Start+WriteChunk-1, end)
	ct, part, err := s.fetchWindow(r.Context(), e, rng.Start, firstEnd, ctr)
	if err != nil {
		writeError(w, http.StatusBadGateway, "backend error")
		return
	}
	h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rng.Start, end, total))
	h.Set("Content-Length", strconv.FormatInt(end-rng.Start+1, 10))
	w.WriteHeader(http.StatusPartialContent)
	if !writeScrubbed(w, ct, part) {
		return // 客户端中断：静默
	}

	// 后续窗口：失败只能断流（头已发），客户端自动重试
	pos := firstEnd + 1
	for pos <= end {
		winEnd := min(pos+WriteChunk-1, end)
		ct, part, err := s.fetchWindow(r.Context(), e, pos, winEnd, ctr)
		if err != nil {
			return
		}
		if !writeScrubbed(w, ct, part) {
			return
		}
		pos = winEnd + 1
	}
}

// cdAttrChars 是 RFC 5987 的 attr-char 集合（filename* 值里可直出的 ASCII），
// 除此之外的字节（含空格、%、引号与所有非 ASCII）都必须百分号编码。
const cdAttrChars = "!#$&+-.^_`|~"

// hexUpper 用于百分号编码的大写十六进制（RFC 5987 示例风格）。
const hexUpper = "0123456789ABCDEF"

// contentDisposition 构造下载响应的 Content-Disposition 值。
//
// 威胁模型与编码依据：传统 filename="..." 是 ASCII/latin-1 字段，直接拼
// 非 ASCII 展示名会乱码，且名中的引号/换行可注入或拆分响应头。因此：
//   - filename 提供纯 ASCII 回退名：非 ASCII 与危险字节（引号、反斜杠、
//     控制字符、DEL）一律替换为 '_'，老客户端拿到安全可显示的名字；
//   - filename* 按 RFC 5987 以 UTF-8 百分号编码携带原名，现代浏览器
//     优先解析它，中文原名得以完整保留。
func contentDisposition(name string) string {
	fallback := make([]byte, 0, len(name))
	enc := make([]byte, 0, len(name)*3) // 上限：全量三字节百分号编码
	for i := 0; i < len(name); i++ {
		c := name[i]
		// filename* 分支：attr-char 直出，其余 %XX
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte(cdAttrChars, c) >= 0:
			enc = append(enc, c)
		default:
			enc = append(enc, '%', hexUpper[c>>4], hexUpper[c&0x0f])
		}
		// filename 回退分支：可打印 ASCII（去引号/反斜杠）直出，其余 '_'
		if c >= 0x20 && c < 0x7f && c != '"' && c != '\\' {
			fallback = append(fallback, c)
		} else {
			fallback = append(fallback, '_')
		}
	}
	return `attachment; filename="` + string(fallback) + `"; filename*=UTF-8''` + string(enc)
}

// serveDownload 输出下载端点：全文件流式解密 + Content-Disposition:
// attachment（浏览器触发保存而非播放）。GET 触发下载；HEAD 返回长度。
//
// 与 serveStream 的区别：忽略 Range、不截断（浏览器 <a download> 需要完整
// 文件），Content-Length = 明文总长，响应状态 200。
func (s *Server) serveDownload(w http.ResponseWriter, r *http.Request, e *Entry) {
	hl := e.Header.CipherOffset()
	total := pipeline.PlaintextTotal(hl, e.CipherSize)

	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", contentDisposition(e.DisplayName))
	h.Set("Cache-Control", "no-store")

	if r.Method == http.MethodHead {
		h.Set("Content-Length", strconv.FormatInt(total, 10))
		w.WriteHeader(http.StatusOK)
		return
	}
	if total <= 0 {
		// 空文件：下载得到 0 字节文件（浏览器端正常）
		h.Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}

	ctr, err := cryptox.NewCTR(e.Key[:], e.Header.IV)
	if err != nil {
		writeError(w, http.StatusBadGateway, "backend error")
		return
	}

	// 首窗口：失败可回 502（响应头未发）。
	firstEnd := min(WriteChunk-1, total-1)
	ct, part, err := s.fetchWindow(r.Context(), e, 0, firstEnd, ctr)
	if err != nil {
		writeError(w, http.StatusBadGateway, "backend error")
		return
	}
	h.Set("Content-Length", strconv.FormatInt(total, 10))
	w.WriteHeader(http.StatusOK)
	if !writeScrubbed(w, ct, part) {
		return // 客户端中断：静默
	}
	// 后续窗口：失败只能断流（头已发），浏览器按 Content-Length 感知中断。
	pos := firstEnd + 1
	for pos < total {
		winEnd := min(pos+WriteChunk-1, total-1)
		ct, part, err := s.fetchWindow(r.Context(), e, pos, winEnd, ctr)
		if err != nil {
			return
		}
		if !writeScrubbed(w, ct, part) {
			return
		}
		pos = winEnd + 1
	}
}

// fetchWindow 下载并解密一个明文窗口 [pos, winEnd]（含两端）。
//
// 返回 ct 为解密后的整段明文缓冲（含窗口前块内对齐的块首字节），
// part 是精确窗口视图（写响应用）；调用方写完后必须 Scrub(ct)
// （writeScrubbed 负责），缓冲不进入任何池 —— 见 memguard.go。
func (s *Server) fetchWindow(ctx context.Context, e *Entry, pos, winEnd int64, ctr *cryptox.CTR) (ct, part []byte, err error) {
	cr := pipeline.PlaintextToCipher(e.Header.CipherOffset(), pos, winEnd+1)
	// 密文终点 clamp 到文件尾（尾块可能不满 16 字节）
	if cr.CtEnd > e.CipherSize {
		cr.CtEnd = e.CipherSize
	}
	if cr.CtEnd <= cr.CtStart {
		return nil, nil, errContainerShort
	}
	raw, err := s.readCipher(ctx, e, cr.CtStart, cr.CtEnd-1)
	if err != nil {
		return nil, nil, err
	}
	if int64(len(raw)) != cr.CtEnd-cr.CtStart {
		// 下载不足：远端被截断/覆盖（注册时的大小已失效）
		return nil, nil, errContainerShort
	}
	// 就地解密（CTR 随机访问：块索引 = 明文主体偏移 / 16）
	ctr.XORAt(raw, raw, uint64(cr.FirstBlock))
	from := pos % int64(protocol.BlockSize)
	need := winEnd - pos + 1
	if int64(len(raw))-from < need {
		Scrub(raw)
		return nil, nil, errContainerShort
	}
	return raw, raw[from : from+need], nil
}

// readCipher 取密文区间 [start, end]（含两端），优先走本地分块缓存。
//
// 读穿语义（见 pkg/cache 包注释）：
//
//   - 命中：直接返回本地字节，零网络往返（重看 / 回拖的加速来源）；
//   - 未命中：按原路径从远端拉取**同样的区间**（首字节延迟与无缓存时
//     完全一致，绝不为了填块而扩大单次下载），随后把这一段喂给缓存。
//
// 缓存写入是「尽力而为」：失败只记日志，绝不影响本次响应 —— 缓存是
// 性能优化而非正确性依赖。
func (s *Server) readCipher(ctx context.Context, e *Entry, start, end int64) ([]byte, error) {
	if s.cc != nil {
		if data, ok := s.cc.Fetch(e.RemotePath, e.CipherSize, start, end); ok {
			return data, nil
		}
	}
	raw, err := s.backend.DownloadRange(ctx, e.RemotePath, start, end)
	if err != nil {
		return nil, err
	}
	if s.cc != nil && len(raw) > 0 {
		s.cc.Put(e.RemotePath, e.DisplayName, e.CipherSize, start, raw)
	}
	return raw, nil
}

// writeScrubbed 写出明文段并清零缓冲，返回 false 表示客户端已断开
// （播放器 seek/切文件属常态，静默退出，Python 同语义）。
//
// net/http 的 Write 返回后不再引用该切片，因此清零是安全的；
// 每窗口显式 Flush 压低首字节延迟（Go 的 ResponseWriter 默认会攒缓冲）。
func writeScrubbed(w http.ResponseWriter, ct, part []byte) bool {
	if _, err := w.Write(part); err != nil {
		Scrub(ct)
		return false
	}
	Scrub(ct)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return true
}

// writeError 发送错误响应：Content-Type/长度与 Python _send_bytes 一致
// （application/octet-stream + Accept-Ranges，proxy_server.py:136-152）。
// extra 形如 ["Content-Range", "bytes */123"]，按对加入响应头。
func writeError(w http.ResponseWriter, status int, body string, extra ...string) {
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	for i := 0; i+1 < len(extra); i += 2 {
		h.Set(extra[i], extra[i+1])
	}
	w.WriteHeader(status)
	if body != "" {
		_, _ = w.Write([]byte(body))
	}
}
