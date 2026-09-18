//go:build windows

// 纯 syscall 原生对话框：文件夹选择 / 多文件选择。
//
// 替代 Wails v2 的 wruntime.OpenDirectoryDialog / OpenMultipleFilesDialog
// （v33 Web 模式下 Wails runtime 在非 Wails context 上 log.Fatalf 杀进程）。
// 用 IFileOpenDialog COM（Vista+），CGO_ENABLED=0 可用：ole32 经
// windows.NewLazySystemDLL，vtable 经 unsafe.Pointer 解引用 +
// syscall.SyscallN 调用，UTF16 字符串用 syscall.UTF16PtrFromString，
// 返回的 PWSTR 用 windows.UTF16PtrToString 读出后 CoTaskMemFree。
//
// vtable 索引以 Windows SDK 10.0.26100 shobjidl_core.h 的 C++ 继承顺序
// 为准（IFileOpenDialog : IFileDialog : IModalWindow : IUnknown；
// IShellItem / IShellItemArray : IUnknown）。改动须对照 SDK 重新核对。
package win

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// COM GUID（IID/CLSID），字节序与 windows.GUID 一致：Data1/2/3 + 8 字节 Data4。
var (
	// CLSID_FileOpenDialog = {DC1C5A9C-E88A-4DDE-A5A1-60F82A20AEF7}
	clsidFileOpenDialog = windows.GUID{
		Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE,
		Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7},
	}
	// IID_IFileOpenDialog = {d57c7288-d4ad-4768-be02-9d969532d960}
	iidIFileOpenDialog = windows.GUID{
		Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768,
		Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60},
	}
)

// FOS_* 选项标志（SDK shobjidl_core.h FILEOPENDIALOGOPTIONS）。
const (
	fosNoChangeDir      = 0x8
	fosPickFolders      = 0x20
	fosAllowMultiSelect = 0x200
	fosPathMustExist    = 0x800
	fosFileMustExist    = 0x1000
)

// SIGDN 值：GetDisplayName 的签名风格。
const sigdnFileSystemPath = 0x80058000

// IFileOpenDialog vtable 索引（C++ 继承扁平顺序）。
const (
	vtRelease    = 2
	vtShow       = 3
	vtSetOptions = 9
	vtGetOptions = 10
	vtSetTitle   = 17
	vtGetResult  = 20 // 单选结果（IShellItem*）
	vtGetResults = 27 // 多选结果（IShellItemArray*）
)

// IShellItem vtable 索引。
const (
	vtShellRelease        = 2
	vtShellGetDisplayName = 5
)

// IShellItemArray vtable 索引。
const (
	vtArrRelease   = 2
	vtArrGetCount  = 7
	vtArrGetItemAt = 8
)

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
)

// comSession 跟踪 CoInitializeEx 的配对 CoUninitialize（仅当本 goroutine
// 真正初始化 COM 成功时才 uninit；已初始化时 CoInitializeEx 返回 S_FALSE(1)）。
type comSession struct {
	needUninit bool
}

func newCOMSession() (*comSession, error) {
	// COINIT_APARTMENTTHREADED=2：对话框需 STA（同线程串行，避免 UI 死锁）。
	err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
	hr, ok := err.(syscall.Errno)
	switch {
	case err == nil, ok && hr == 1: // S_OK / S_FALSE(已初始化)
		return &comSession{needUninit: err == nil}, nil
	default:
		return nil, fmt.Errorf("CoInitializeEx 失败: %w", err)
	}
}

func (c *comSession) close() {
	if c != nil && c.needUninit {
		windows.CoUninitialize()
	}
}

// vtable 返回对象的虚函数表首地址（解一层指针）。
func vtable(obj unsafe.Pointer) []uintptr {
	return *(*[]uintptr)(obj) // unsafe；obj 为 COM 对象指针（指向 vtable 指针）
}

// callVtable 调用 obj 的 vtable[idx](obj, args...)，返回 HRESULT。
func callVtable(obj unsafe.Pointer, idx int, args ...uintptr) uintptr {
	vt := vtable(obj)
	argv := append([]uintptr{uintptr(obj)}, args...)
	r1, _, _ := syscall.SyscallN(vt[idx], argv...)
	return r1
}

// callVtable 已够用：COM 方法一律返回 HRESULT，不置 errno，故无需读 callErr。

// PickFolder 弹 Win32 文件夹选择框；取消返回空串与 nil。
func PickFolder(title string) (string, error) {
	return pickOne(title, true)
}

// PickFiles 弹 Win32 多文件选择框；取消返回 nil 与 nil。
func PickFiles(title string) ([]string, error) {
	s, err := pickMulti(title)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// pickOne 实现单选：FOS_PICKFOLDERS 选目录，否则选单文件 → GetResult。
func pickOne(title string, folder bool) (string, error) {
	sess, err := newCOMSession()
	if err != nil {
		return "", err
	}
	defer sess.close()

	dialog, err := createFileOpenDialog()
	if err != nil {
		return "", err
	}
	defer callVtable(dialog, vtRelease)

	if err := applyOptions(dialog, title, folder, false); err != nil {
		return "", err
	}
	// Show 返回 S_OK(0) 或 ERROR_CANCELLED(0x800704C7 的低 16 位 = 1223)。
	if hr := callVtable(dialog, vtShow, 0); hr != 0 {
		if isCancelled(hr) {
			return "", nil
		}
		return "", fmt.Errorf("Show 失败: hr=0x%x", hr)
	}
	item, err := getResult(dialog)
	if err != nil {
		return "", err
	}
	defer callVtable(item, vtShellRelease)
	return shellItemPath(item)
}

// pickMulti 实现多选：GetResults → IShellItemArray → 逐项 GetDisplayName。
func pickMulti(title string) ([]string, error) {
	sess, err := newCOMSession()
	if err != nil {
		return nil, err
	}
	defer sess.close()

	dialog, err := createFileOpenDialog()
	if err != nil {
		return nil, err
	}
	defer callVtable(dialog, vtRelease)

	if err := applyOptions(dialog, title, false, true); err != nil {
		return nil, err
	}
	if hr := callVtable(dialog, vtShow, 0); hr != 0 {
		if isCancelled(hr) {
			return nil, nil
		}
		return nil, fmt.Errorf("Show 失败: hr=0x%x", hr)
	}
	arr, err := getResults(dialog)
	if err != nil {
		return nil, err
	}
	defer callVtable(arr, vtArrRelease)

	var n uint32
	callVtable(arr, vtArrGetCount, uintptr(unsafe.Pointer(&n)))
	paths := make([]string, 0, n)
	for i := uint32(0); i < n; i++ {
		var item unsafe.Pointer
		// GetItemAt(idx, **IShellItem)
		callVtable(arr, vtArrGetItemAt, uintptr(i), uintptr(unsafe.Pointer(&item)))
		if item == nil {
			continue
		}
		p, err := shellItemPath(item)
		callVtable(item, vtShellRelease)
		if err != nil {
			continue
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// createFileOpenDialog 调 CoCreateInstance 拿 IFileOpenDialog 对象指针。
//
// 输出用 unsafe.Pointer 变量承接（而非 uintptr 再转）：uintptr→unsafe.Pointer
// 的逆向转换会被 go vet 判为 possible misuse（GC 可能在转换间隙移动对象）。
// COM 返回的指针指向 COM 自己的堆区，GC 不扫描，用 unsafe.Pointer 直接持有无风险。
func createFileOpenDialog() (unsafe.Pointer, error) {
	var ppv unsafe.Pointer
	ret, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0, // pUnkOuter
		1, // CLSCTX_INPROC_SERVER
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)),
		uintptr(unsafe.Pointer(&ppv)), // **IFileOpenDialog 输出槽
	)
	if ret != 0 { // S_OK=0
		return nil, fmt.Errorf("CoCreateInstance 失败: hr=0x%x", ret)
	}
	return ppv, nil
}

// applyOptions 设 FOS 标志与标题。
func applyOptions(dialog unsafe.Pointer, title string, folder, multi bool) error {
	var opts uintptr
	callVtable(dialog, vtGetOptions, uintptr(unsafe.Pointer(&opts)))
	flags := uint32(opts) | fosPathMustExist
	if folder {
		flags |= fosPickFolders | fosNoChangeDir
	}
	if multi {
		flags |= fosAllowMultiSelect | fosFileMustExist
	}
	if !folder && !multi {
		flags |= fosFileMustExist
	}
	if hr := callVtable(dialog, vtSetOptions, uintptr(flags)); hr != 0 {
		return fmt.Errorf("SetOptions 失败: hr=0x%x", hr)
	}
	if title != "" {
		t, err := syscall.UTF16PtrFromString(title)
		if err != nil {
			return err
		}
		callVtable(dialog, vtSetTitle, uintptr(unsafe.Pointer(t)))
	}
	return nil
}

// getResult 调 GetResult 拿单选 IShellItem。
func getResult(dialog unsafe.Pointer) (unsafe.Pointer, error) {
	var item unsafe.Pointer
	callVtable(dialog, vtGetResult, uintptr(unsafe.Pointer(&item)))
	if item == nil {
		return nil, fmt.Errorf("GetResult 返回空")
	}
	return item, nil
}

// getResults 调 GetResults 拿多选 IShellItemArray。
func getResults(dialog unsafe.Pointer) (unsafe.Pointer, error) {
	var arr unsafe.Pointer
	callVtable(dialog, vtGetResults, uintptr(unsafe.Pointer(&arr)))
	if arr == nil {
		return nil, fmt.Errorf("GetResults 返回空")
	}
	return arr, nil
}

// shellItemPath 调 IShellItem::GetDisplayName(SIGDN_FILESYSPATH) 取路径。
//
// 同上：COM 分配的 PWSTR 用 unsafe.Pointer 承接，避免 uintptr→unsafe.Pointer 转换。
// 该串由 COM 任务分配器分配，读出后必须 CoTaskMemFree 归还。
func shellItemPath(item unsafe.Pointer) (string, error) {
	var pwstr unsafe.Pointer
	callVtable(item, vtShellGetDisplayName, uintptr(sigdnFileSystemPath), uintptr(unsafe.Pointer(&pwstr)))
	if pwstr == nil {
		return "", fmt.Errorf("GetDisplayName 返回空")
	}
	defer windows.CoTaskMemFree(pwstr)
	return windows.UTF16PtrToString((*uint16)(pwstr)), nil
}

// isCancelled 判 HRESULT 是否 ERROR_CANCELLED（用户在 Show 里按取消）。
// COM 把 Win32 errno 包进 HRESULT：MAKE_HRESULT(SEVERITY_ERROR, FACILITY_WIN32, 1223) = 0x800704C7。
func isCancelled(hr uintptr) bool {
	return uint32(hr) == 0x800704C7
}
