package bind

import (
	"context"
	"sync"
)

// ContextHolder 保存前台 context（对话框 API 曾依赖前台句柄；Web 模式下
// 由 win.PickFolder/PickFiles 纯 syscall 对话框替代，holder 仅作装配兼容保留）。
//
// 绑定 struct 不能导出 SetCtx 之类的方法 —— 导出方法全会进入 JS 绑定面，
// 而 context.Context 是 Go 内部对象，前端根本无法构造。故 5 个域 struct
// 各持 *ContextHolder 指针。本类型不进 Bind 数组（非绑定件）。
type ContextHolder struct {
	mu  sync.Mutex
	ctx context.Context
}

// NewContextHolder 构造空 holder（Set 前 Context 返回 Background）。
func NewContextHolder() *ContextHolder { return &ContextHolder{} }

// Set 注入前台 context（仅装配期一次）。
func (h *ContextHolder) Set(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ctx = ctx
}

// Context 返回前台 context；未 Set 前为 Background（防御性兜底）。
func (h *ContextHolder) Context() context.Context {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx == nil {
		return context.Background()
	}
	return h.ctx
}
