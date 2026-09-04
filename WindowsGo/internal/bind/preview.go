package bind

import (
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
)

// Preview 是预览域的 Wails 绑定：流媒体/缩略图代理 URL 的签发与吊销。
//
// 令牌 URL 只在密库会话内有效：代理随锁库停止，派生密钥随 Revoke/进程
// 退出清空。播放/预览页拿 URL 交给 <video>/<img>，无需暴露密文路径。
type Preview struct {
	st  *appstate.State
	ctx *ContextHolder
}

// NewPreview 构造预览域绑定。
func NewPreview(st *appstate.State, ctx *ContextHolder) *Preview {
	return &Preview{st: st, ctx: ctx}
}

// ThumbURL 取远端图片的缩略图代理 URL（解码 → 192px JPEG → 加密磁盘缓存，
// 后端同步取图，前端失败回退占位图标不阻塞列表）。
func (p *Preview) ThumbURL(remote string) (string, error) {
	p.st.Activity()
	url, err := p.st.ThumbURL(p.ctx.Context(), remote)
	if err != nil {
		return "", Wrap(err)
	}
	return url, nil
}

// MediaURL 签发流式播放代理 URL；displayName 用于 MIME 推断（文件列表里
// 的展示名直传即可）。重复签发同路径返回既有令牌（幂等）。
func (p *Preview) MediaURL(remote, displayName string) (string, error) {
	p.st.Activity()
	url, err := p.st.MediaURL(p.ctx.Context(), remote, displayName)
	if err != nil {
		return "", Wrap(err)
	}
	return url, nil
}

// Revoke 吊销一个代理令牌（停止播放/离开预览页时调用，释放派生密钥）。
func (p *Preview) Revoke(token string) {
	p.st.Activity()
	p.st.RevokeMedia(token)
}
