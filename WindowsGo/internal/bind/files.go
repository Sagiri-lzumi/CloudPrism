package bind

import (
	"strings"

	"github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/appstate"
)

// Files 是文件浏览域的 Wails 绑定：目录列表/新建/重命名/删除/导出。
//
// 薄适配原则同 Vault；条目一律以 appstate.FileEntry.Remote（完整后端
// 相对路径）回传操作，前端不拼路径。
type Files struct {
	st  *appstate.State
	ctx *ContextHolder
}

// NewFiles 构造文件浏览域绑定。
func NewFiles(st *appstate.State, ctx *ContextHolder) *Files {
	return &Files{st: st, ctx: ctx}
}

// List 列目录；remote 为空串表示密库根。
func (f *Files) List(remote string) ([]appstate.FileEntry, error) {
	f.st.Activity()
	entries, err := f.st.ListDir(f.ctx.Context(), remote)
	if err != nil {
		return nil, Wrap(err)
	}
	return entries, nil
}

// NewFolder 在 parentRemote 下新建目录（空串 = 密库根）。
func (f *Files) NewFolder(parentRemote, displayName string) error {
	f.st.Activity()
	if err := f.st.NewFolder(f.ctx.Context(), parentRemote, displayName); err != nil {
		return Wrap(err)
	}
	return nil
}

// Rename 重命名远端条目（文件或目录；改名后令牌/缩略图缓存自动失效）。
func (f *Files) Rename(remote, newDisplay string) error {
	f.st.Activity()
	if err := f.st.RenameRemote(f.ctx.Context(), remote, newDisplay); err != nil {
		return Wrap(err)
	}
	return nil
}

// Delete 批量删除远端条目（含目录时递归）；任一失败立即中止并返回。
func (f *Files) Delete(remotes []string) error {
	f.st.Activity()
	for _, r := range remotes {
		if err := f.st.DeleteRemote(f.ctx.Context(), r); err != nil {
			return Wrap(err)
		}
	}
	return nil
}

// Export 解密导出单个远端条目到 dir，返回落盘路径。
//
// dir 由前端选定（网页版目录选择器 → /api/fs/dirs 列目录），本层不再弹原生
// 对话框：IFileOpenDialog 以 owner=0 弹出、没有属主窗口，会跑到浏览器窗口
// 后面，用户看到的是「后台莫名跳出个框」。
func (f *Files) Export(remote, dir string) (string, error) {
	f.st.Activity()
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", &ApiError{Code: CodeInternal, Message: "未指定导出位置"}
	}
	path, err := f.st.ExportRemote(f.ctx.Context(), remote, dir)
	if err != nil {
		return "", Wrap(err)
	}
	return path, nil
}
