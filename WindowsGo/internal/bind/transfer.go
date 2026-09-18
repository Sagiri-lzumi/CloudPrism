package bind

import (
	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
)

// Transfer 是传输域的 Wails 绑定：上传/下载/任务管理/系统对话框。
//
// 任务进度不在此轮询：活动传输的任务明细随 10Hz 状态帧（st:frame）
// 推送，Tasks 只服务页面初始化与下拉刷新。
type Transfer struct {
	st  *appstate.State
	ctx *ContextHolder
}

// NewTransfer 构造传输域绑定。
func NewTransfer(st *appstate.State, ctx *ContextHolder) *Transfer {
	return &Transfer{st: st, ctx: ctx}
}

// Upload 上传本地文件/目录到远端目录（入队后异步执行）。
func (t *Transfer) Upload(localPaths []string, remoteDir string) error {
	t.st.Activity()
	if err := t.st.UploadPaths(t.ctx.Context(), localPaths, remoteDir); err != nil {
		return Wrap(err)
	}
	return nil
}

// Download 把选中的远端条目下载到本地目录（条目为目录时整棵下载）。
func (t *Transfer) Download(entries []appstate.FileEntry, localDir string) error {
	t.st.Activity()
	if err := t.st.DownloadFiles(t.ctx.Context(), entries, localDir); err != nil {
		return Wrap(err)
	}
	return nil
}

// Tasks 当前全部传输任务明细。
func (t *Transfer) Tasks() []appstate.TaskView {
	return t.st.Tasks()
}

// Retry 重试单个失败任务（相同目标重新入队）。
func (t *Transfer) Retry(id int64) error {
	t.st.Activity()
	if err := t.st.RetryTask(id); err != nil {
		return Wrap(err)
	}
	return nil
}

// CancelAll 取消全部在飞/等待任务（已完成的保留供清理）。
func (t *Transfer) CancelAll() error {
	t.st.Activity()
	t.st.CancelAll()
	return nil
}

// ClearFinished 清空已完成/已取消任务（失败任务需先 Retry 或经此移除）。
func (t *Transfer) ClearFinished() error {
	t.st.Activity()
	t.st.ClearFinished()
	return nil
}
