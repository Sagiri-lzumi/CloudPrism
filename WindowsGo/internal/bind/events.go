package bind

import (
	"context"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
)

// 事件名常量：与 frontend/src/lib/events.ts 镜像，改动需两侧同步。
const (
	// EvtFrame 状态合帧（载荷 frame：快照 + 活动传输的任务明细）。
	EvtFrame = "st:frame"
	// EvtDropped 文件拖放（载荷 []string 本地路径；前端以当前目录发起上传）。
	EvtDropped = "st:files-dropped"
	// 瞬时事件沿用 appstate 的事件名（EventOpProgress / EventOpDone /
	// EventOpError / EventLocked），Forward 原样转发，此处不重复定义。
)

// frameInterval 合帧周期 100ms（10Hz）。状态变化触发频率远高于 10Hz，
// Wails 事件通道按此压流（计划约束 EventsEmit ≤10 次/s 即指帧）；瞬时
// 事件天然低频（进度文案/锁定广播），不受节流影响。
const frameInterval = 100 * time.Millisecond

// frame 是每帧载荷：全局快照 + 传输任务明细（任务进度 10Hz 刷新）。
// 无活动传输时 Tasks 为 nil，减帧体积。
type frame struct {
	Snap  appstate.Snapshot   `json:"snap"`
	Tasks []appstate.TaskView `json:"tasks,omitempty"`
}

// ContextHolder 保存 Wails 启动 context（对话框 API 依赖前台句柄）。
//
// 绑定 struct 不能导出 SetCtx 之类的方法 —— 导出方法全会进入 JS 绑定面，
// 而 context.Context 是 Go 内部对象，前端根本无法构造。故 5 个域 struct
// 各持 *ContextHolder 指针，由 main.go 在 OnStartup 时 Set。本类型不进
// Bind 数组（非绑定件），方法不受绑定面污染。
type ContextHolder struct {
	mu  sync.Mutex
	ctx context.Context
}

// NewContextHolder 构造空 holder（OnStartup 前 get 返回 Background）。
func NewContextHolder() *ContextHolder { return &ContextHolder{} }

// Set 在 OnStartup 中调用一次（此后只读）。
func (h *ContextHolder) Set(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ctx = ctx
}

// Context 返回前台 context；OnStartup 前为空（正常不会走到，防御性兜底）。
func (h *ContextHolder) Context() context.Context {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx == nil {
		return context.Background()
	}
	return h.ctx
}

// Events 是状态帧合帧器：每 100ms 取一次全量快照发 EvtFrame，并把
// appstate 的瞬时事件（st:locked / st:op-*）原样转发给前端。
//
// 非绑定 struct：不进 Wails Bind（无导出方法）。main.go 装配顺序：
// NewEvents 启动循环 → st.SetEmit(events.Forward) 接上事件源。
type Events struct {
	ctx  context.Context
	st   *appstate.State
	stop chan struct{}
	once sync.Once
}

// NewEvents 启动合帧循环（ctx 来自 OnStartup，必非 nil）。
func NewEvents(ctx context.Context, st *appstate.State) *Events {
	e := &Events{ctx: ctx, st: st, stop: make(chan struct{})}
	go e.loop()
	return e
}

// Forward 是 appstate.Emit 出口（main.go 经 State.SetEmit 注入）。
// 瞬时事件直发；帧由 loop 统一发送，此路径收到帧名直接忽略。
func (e *Events) Forward(name string, data any) {
	if e.ctx == nil || name == EvtFrame {
		return
	}
	wruntime.EventsEmit(e.ctx, name, data)
}

// Dropped 转发系统文件拖放（options.OnFileDrop 回调接线）。
func (e *Events) Dropped(paths []string) {
	e.Forward(EvtDropped, paths)
}

// Close 停止合帧循环（幂等；OnShutdown 调用）。
func (e *Events) Close() { e.once.Do(func() { close(e.stop) }) }

// loop 合帧主循环：ticker 驱动，退出即 Close。
func (e *Events) loop() {
	t := time.NewTicker(frameInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			f := frame{Snap: e.st.Snapshot()}
			if f.Snap.TransferActive {
				f.Tasks = e.st.Tasks()
			}
			if e.ctx != nil {
				wruntime.EventsEmit(e.ctx, EvtFrame, f)
			}
		case <-e.stop:
			return
		}
	}
}
