package streaming

import (
	"path"
	"strings"
)

// 展示名扩展名 → Content-Type 推断表。
//
// Go 标准库 mime.TypeByExtension 覆盖不了 .mkv/.flv/.ts 等媒体容器
// （Windows 注册表才有），而这些恰恰是播放最常用的格式 —— 因此自备
// 表为主。值与 Chromium 媒体栈的期望一致（ARCHITECTURE.md 差异 6：
// Python 恒发 application/octet-stream，Qt Multimedia 会嗅探内容而
// Chromium <video> 不会，照抄会黑屏）。
//
// **这张表同时是 /s/ 的内联白名单，安全边界见 StreamContentType**：
// 表里不允许出现 text/html、image/svg+xml、application/xml 这类
// 「浏览器会当代码执行」的类型。
var mimeByExt = map[string]string{
	// 视频
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".mkv":  "video/x-matroska",
	".webm": "video/webm",
	".avi":  "video/x-msvideo",
	".mov":  "video/quicktime",
	".flv":  "video/x-flv",
	".ts":   "video/mp2t",
	".m2ts": "video/mp2t",
	".wmv":  "video/x-ms-wmv",
	".mpg":  "video/mpeg",
	".mpeg": "video/mpeg",
	".3gp":  "video/3gpp",
	// 音频
	".mp3":  "audio/mpeg",
	".aac":  "audio/aac",
	".wav":  "audio/wav",
	".flac": "audio/flac",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".opus": "audio/ogg",
	".m4a":  "audio/mp4",
	".wma":  "audio/x-ms-wma",
	// 图片（供 /s/ 兜底场景；正常走 /t/）
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".avif": "image/avif",
	".heic": "image/heic",
	".heif": "image/heif",
	".tif":  "image/tiff",
	".tiff": "image/tiff",
	".ico":  "image/x-icon",
	// 文档
	//
	// .pdf 必须显式给出 application/pdf：Chromium 只认这个类型才会把响应交给
	// 内置 PDF 阅读器接管。落到 application/octet-stream（本表兜底值）时，
	// nosniff 之下浏览器不会嗅探，表现是「点开变下载」而不是内嵌翻阅。
	// /s/ 端点本就不设 Content-Disposition（inline 语义），故这里只需补类型。
	".pdf": "application/pdf",
	// 文本 / 字幕（纯文本类不会被当页面渲染，且响应带 nosniff）
	".txt":  "text/plain; charset=utf-8",
	".log":  "text/plain; charset=utf-8",
	".srt":  "text/plain; charset=utf-8",
	".csv":  "text/plain; charset=utf-8",
	".vtt":  "text/vtt; charset=utf-8",
	".md":   "text/markdown; charset=utf-8",
	".json": "application/json",
}

// MIMEForDisplayName 按展示名（明文文件名）推断 Content-Type。
//
// 未知扩展名一律回退 application/octet-stream，**不再回退
// mime.TypeByExtension**：那会去问 Windows 注册表，返回值既依赖用户机器
// 上装了什么软件、也没有上界（.html→text/html、.svg→image/svg+xml、
// 各种 ActiveX/脚本类型…）。/s/ 与主界面同源，把这些类型内联渲染出去
// 就是一条存储型 XSS 通道。
func MIMEForDisplayName(displayName string) string {
	ext := strings.ToLower(path.Ext(displayName))
	if ct, ok := mimeByExt[ext]; ok {
		return ct
	}
	return "application/octet-stream"
}

// StreamContentType 返回 /s/ 流式响应应使用的 Content-Type，以及是否允许
// 内联渲染。
//
// 安全模型：/s/ 不带 Content-Disposition、与主界面**同源**，而密库里的
// 文件名是用户（或从别处下载的压缩包）完全可控的。一旦让浏览器按上传者
// 指定的类型就地渲染 .html / .svg，脚本就运行在应用源上 —— 回环来源本
// 就不校验令牌，脚本可直接调 /api/*（含解密下载、删文件、改设置），
// 这就是一条完整的存储型 XSS。
//
// 因此只放行「纯媒体与文档」：视频/音频/位图/PDF/纯文本/JSON 这些类型
// 浏览器不会当代码执行（PDF 由 Chromium 沙箱化的阅读器接管）。其余一律
// 降级为 application/octet-stream + attachment（调用方负责加），内容是
// 什么就只能是文件下载，绝不给浏览器嗅探成可执行文档的机会。
func StreamContentType(displayName string) (contentType string, inline bool) {
	ext := strings.ToLower(path.Ext(displayName))
	if ct, ok := mimeByExt[ext]; ok {
		return ct, true
	}
	return "application/octet-stream", false
}
