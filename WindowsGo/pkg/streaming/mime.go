package streaming

import (
	"mime"
	"path"
	"strings"
)

// 展示名扩展名 → Content-Type 推断表。
//
// Go 标准库 mime.TypeByExtension 覆盖不了 .mkv/.flv/.ts 等媒体容器
// （Windows 注册表才有），而这些恰恰是播放最常用的格式 —— 因此自备
// 表为主、mime.TypeByExtension 兜底。值与 Chromium 媒体栈的期望一致
// （ARCHITECTURE.md 差异 6：Python 恒发 application/octet-stream，
// Qt Multimedia 会嗅探内容而 Chromium <video> 不会，照抄会黑屏）。
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
	// 文本 / 字幕
	".txt":  "text/plain; charset=utf-8",
	".log":  "text/plain; charset=utf-8",
	".srt":  "text/plain; charset=utf-8",
	".vtt":  "text/vtt; charset=utf-8",
	".md":   "text/markdown; charset=utf-8",
	".json": "application/json",
}

// MIMEForDisplayName 按展示名（明文文件名）推断 Content-Type。
//
// 未知扩展名回退 application/octet-stream（Chromium 对个别无扩展名
// 的视频会失败，但那是数据本身的问题，不应臆测类型）。
func MIMEForDisplayName(displayName string) string {
	ext := strings.ToLower(path.Ext(displayName))
	if ct, ok := mimeByExt[ext]; ok {
		return ct
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
