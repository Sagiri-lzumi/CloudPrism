package bind

import "github.com/Sagiri-lzumi/cloudprism/windowsgo/internal/platform/win"

// LocalFS 是本机目录浏览域的绑定：给网页版「目录选择器」列盘符 / 列子目录 /
// 新建目录。
//
// 与其余 6 个域不同，它**无状态**：不碰 appstate、不碰密库、不需要前台 context ——
// 读写的只是运行本程序这台机器的本地文件系统。归在 bind 层的原因是它同样属于
// 「平台能力 → JSON 友好结构 + ApiError 包装」的适配工作（web 层不直接引平台包）。
//
// 为什么需要它见 internal/platform/win/localfs.go 的文件头：浏览器原生
// `<input webkitdirectory>` 只能给相对路径，拿不到绝对路径，而这里要的正是绝对路径。
type LocalFS struct{}

// NewLocalFS 构造本机目录浏览绑定（无依赖，纯适配）。
func NewLocalFS() *LocalFS { return &LocalFS{} }

// Drives 列出本机盘符（选择器的「此电脑」视图）。
func (l *LocalFS) Drives() (win.LocalListing, error) {
	res, err := win.ListLocalDirs("")
	return res, Wrap(err)
}

// ListDir 列出 path 下的子目录；path 为空串等价于 Drives。
func (l *LocalFS) ListDir(path string) (win.LocalListing, error) {
	res, err := win.ListLocalDirs(path)
	return res, Wrap(err)
}

// MakeDir 在 parent 下新建 name 目录，返回新目录的绝对路径。
// 前端拿到路径后自行进入该目录（不再回一趟列表接口，少一次往返）。
func (l *LocalFS) MakeDir(parent, name string) (string, error) {
	dir, err := win.CreateLocalDir(parent, name)
	return dir, Wrap(err)
}
