//go:build windows

// 本机目录浏览：为「网页版目录选择器」提供数据（列盘符 / 列子目录 / 新建目录）。
//
// 为什么要有它 —— 浏览器原生选择器拿不到绝对路径：
//   - `<input type="file" webkitdirectory>` 只给 `webkitRelativePath`
//     （所选根目录**之下**的相对路径），`File` 对象也没有任何路径属性；
//   - 而密库存放目录 / 同步目录 / 缓存目录 / 导出位置需要的正是**绝对路径**。
//
// 于是改由本机后端列目录、网页自己渲染选择器，用户点完把绝对路径回传。
// 顺带解决旧实现的观感问题：原先每次「浏览…」都由本进程弹 IFileOpenDialog，
// 且 Show(owner=0) 没有属主窗口 —— 对话框会跑到浏览器窗口后面，看起来像
// 「后台莫名蹦出来一个框」。现在全程留在网页里。
//
// 安全边界：这些函数能读出主机任意目录的**名字**。web 层把对应端点登记为
// 「仅限回环」（internal/web/auth.go 的 localOnlyPaths），远端即使持有访问
// 令牌也调不到 —— 与原先「只有本机能弹原生框」的能力边界保持一致。
package win

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// localDirMax 单层目录项返回上限：超大目录（如临时目录、node_modules 的父级）
// 不该把 JSON 响应撑到几 MB。超出时置 Truncated 由前端如实提示。
const localDirMax = 2000

// LocalDrive 是一个可选盘符。
type LocalDrive struct {
	Path  string `json:"path"`  // 形如 `C:\`
	Label string `json:"label"` // 卷标；空光驱/未格式化盘取不到，为空串
	Kind  string `json:"kind"`  // fixed/removable/remote/cdrom/ramdisk/other
}

// LocalDirEntry 是一个子目录（只列目录，不列文件）。
type LocalDirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Hidden 隐藏或系统属性（$RECYCLE.BIN、System Volume Information 这类）。
	// 交前端默认折叠而不是后端丢弃：否则用户找不到自己的隐藏文件夹时，
	// 界面上没有任何线索说明「它们被过滤了」。
	Hidden bool `json:"hidden"`
}

// LocalListing 是一次目录浏览的结果。
type LocalListing struct {
	// Path 是当前目录的绝对路径；空串表示「此电脑」视图（只列盘符）。
	Path string `json:"path"`
	// Parent 是上一级目录；空串表示当前已在最上层（盘符根 / UNC 共享根 /
	// 此电脑视图）—— 前端据此禁用「上一级」。
	Parent string `json:"parent"`
	// Drives 仅在 Path 为空串时有值。
	Drives []LocalDrive `json:"drives,omitempty"`
	// Dirs 是全部子目录（含隐藏项，由 Hidden 标记），已按名称排序。
	Dirs []LocalDirEntry `json:"dirs"`
	// Truncated 表示该层目录项超过 localDirMax，已截断。
	Truncated bool `json:"truncated,omitempty"`
}

// ListLocalDirs 列出 path 下的子目录；path 为空串时返回盘符列表。
func ListLocalDirs(path string) (LocalListing, error) {
	if strings.TrimSpace(path) == "" {
		return LocalListing{Path: "", Drives: listDrives(), Dirs: []LocalDirEntry{}}, nil
	}
	dir, err := normalizeLocalDir(path)
	if err != nil {
		return LocalListing{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return LocalListing{}, describeLocalDirErr(dir, err)
	}
	out := LocalListing{
		Path:   dir,
		Parent: parentLocalDir(dir),
		Dirs:   make([]LocalDirEntry, 0, len(entries)),
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue // 竞态/权限：跳过单项，不让整轮目录浏览失败
		}
		if !info.IsDir() {
			// 软链接与 junction 在 Windows 上很常见（如用户目录里的兼容链接），
			// lstat 看不出它们是目录；只在必要时补一次 stat 跟随，避免
			// 「目录明明在那儿却列不出来」。
			if info.Mode()&os.ModeSymlink == 0 {
				continue
			}
			fi, err := os.Stat(full)
			if err != nil || !fi.IsDir() {
				continue
			}
			info = fi
		}
		out.Dirs = append(out.Dirs, LocalDirEntry{
			Name:   e.Name(),
			Path:   full,
			Hidden: isHiddenAttr(info),
		})
		if len(out.Dirs) >= localDirMax {
			out.Truncated = true
			break
		}
	}
	sort.Slice(out.Dirs, func(i, j int) bool {
		a, b := strings.ToLower(out.Dirs[i].Name), strings.ToLower(out.Dirs[j].Name)
		if a != b {
			return a < b // 大小写不敏感（Windows 语义），同序时再按原名稳定排序
		}
		return out.Dirs[i].Name < out.Dirs[j].Name
	})
	return out, nil
}

// CreateLocalDir 在 parent 下新建名为 name 的目录，返回新目录的绝对路径。
func CreateLocalDir(parent, name string) (string, error) {
	dir, err := normalizeLocalDir(parent)
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if err := validateLocalDirName(name); err != nil {
		return "", err
	}
	full := filepath.Join(dir, name)
	if err := os.Mkdir(full, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("「%s」已存在", name)
		}
		return "", fmt.Errorf("新建文件夹失败：%w", err)
	}
	return full, nil
}

/* ------------------------------------------------------------ 内部实现 */

// normalizeLocalDir 规范化并校验目标路径（必须是已存在的目录）。
func normalizeLocalDir(path string) (string, error) {
	p := filepath.Clean(strings.TrimSpace(path))
	// `D:`（没有分隔符）在 Windows 上指「D 盘的当前工作目录」而非盘根，
	// 补成 `D:\` 才与用户输入「浏览到 D 盘」的直觉一致。
	if len(p) == 2 && p[1] == ':' {
		p += `\`
	}
	info, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("目录不存在：%s", p)
		}
		return "", fmt.Errorf("无法访问目录：%s（%v）", p, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("不是文件夹：%s", p)
	}
	return p, nil
}

// validateLocalDirName 校验新建目录名。只做 Windows 层面的硬约束，
// 不额外发明规则 —— 名字不好看应该由用户决定，不该由程序拒绝。
func validateLocalDirName(name string) error {
	if name == "" {
		return errors.New("文件夹名不能为空")
	}
	if name == "." || name == ".." {
		return errors.New(`文件夹名不能是「.」或「..」`)
	}
	if strings.ContainsRune(name, 0) || strings.ContainsAny(name, `\/:*?"<>|`) {
		return errors.New(`文件夹名不能包含 \ / : * ? " < > | 这些字符`)
	}
	// Windows 会静默丢掉结尾的句点与空格，导致「输的名字」与「建出的目录」
	// 不一致 —— 属于必须挡掉的坑，而不是风格问题。
	if strings.TrimRight(name, ". ") != name {
		return errors.New("文件夹名不能以句点或空格结尾")
	}
	return nil
}

// parentLocalDir 返回上一级目录；已在最上层时返回空串。
//
// 盘符根（`C:\`）与 UNC 共享根（`\\NAS\share`）的 Dir 都等于自身，
// 用这一个判定即可同时覆盖；UNC 共享根再往上没有任何合法表示（会退化成
// `\\NAS`，那不是路径），故必须停在共享根。
//
// 注意：Go 的 Dir 对 UNC 子目录返回**带尾反斜杠**的共享根
// （`Dir(\\NAS\share\docs)` = `\\NAS\share\`）—— 这是 Clean 对「卷根」的
// 规范形式（与 `C:\` 同类），不是笔误。别顺手 TrimRight 掉：那一刀会让
// `Dir(p) == p` 这个根判定在两台机器上表现不一致。
func parentLocalDir(p string) string {
	parent := filepath.Dir(p)
	if parent == p || parent == "." || parent == string(filepath.Separator) {
		return ""
	}
	return parent
}

// describeLocalDirErr 把 os 错误翻成可直接展示的中文（列不出目录时用户
// 需要知道是「没权限」还是「盘没插」）。
func describeLocalDirErr(dir string, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("没有权限读取：%s", dir)
	}
	return fmt.Errorf("读取目录失败：%s（%v）", dir, err)
}

// isHiddenAttr 报告文件是否带隐藏/系统属性。
func isHiddenAttr(info os.FileInfo) bool {
	d, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return false
	}
	const mask = windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM
	return d.FileAttributes&mask != 0
}

// listDrives 枚举本机盘符（按字母序，GetLogicalDrives 的位序天然有序）。
func listDrives() []LocalDrive {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	out := make([]LocalDrive, 0, 8)
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		out = append(out, LocalDrive{Path: root, Label: volumeLabel(root), Kind: driveKind(root)})
	}
	return out
}

// driveKind 把 GetDriveType 的结果映射为稳定的英文枚举值（前端据此选文案）。
func driveKind(root string) string {
	p, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return "other"
	}
	switch windows.GetDriveType(p) {
	case windows.DRIVE_FIXED:
		return "fixed"
	case windows.DRIVE_REMOVABLE:
		return "removable"
	case windows.DRIVE_REMOTE:
		return "remote"
	case windows.DRIVE_CDROM:
		return "cdrom"
	case windows.DRIVE_RAMDISK:
		return "ramdisk"
	default:
		return "other"
	}
}

// volumeLabel 取卷标。空光驱、未格式化盘、无卷标盘都会失败 —— 这不是错误，
// 返回空串由前端退化成「本地磁盘 (D:)」这类通用文案。
func volumeLabel(root string) string {
	rootPtr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return ""
	}
	buf := make([]uint16, 256)
	if err := windows.GetVolumeInformation(rootPtr, &buf[0], uint32(len(buf)),
		nil, nil, nil, nil, 0); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf)
}
