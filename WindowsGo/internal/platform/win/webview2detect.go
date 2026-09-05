//go:build windows

// Package win 汇集 Windows 平台专属的 syscall 封装。
//
// 本包是 CloudPrism Go 端唯一允许直接触碰 Windows API 的位置：上层 pkg/*
// 只依赖这里导出的语义化函数，从而保证 pkg/ 保持 GUI 与平台无关，
// 未来 Android 端可整体抽出复用（对照 WindowsPy 把 DPAPI 直接写在
// storage/baidu_backend.py 里、无法跨平台复用的做法）。
//
// 全部实现走纯 syscall，不依赖 cgo —— 本机无 gcc，且 CGO_ENABLED=0
// 是阶段 2 spike 已验证通过的构建前提。
package win

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// edgeUpdateClientGUID 是 WebView2 Evergreen 运行时在 EdgeUpdate 客户端
// 注册表项下的固定 GUID（微软公布值，所有版本一致）。
const edgeUpdateClientGUID = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`

// runtimeKeyPaths 按优先级列出可能记录运行时版本的注册表键。
//
// 机器级键覆盖所有用户，用户级键对应「仅为当前用户安装」的场景，
// 32 位视图（WOW6432Node）对应 32 位安装器写入的位置；三者都可能
// 不存在，故必须逐个尝试而非只读一个。
var runtimeKeyPaths = []struct {
	root registry.Key
	path string
}{
	{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + edgeUpdateClientGUID},
	{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + edgeUpdateClientGUID},
	{registry.CURRENT_USER, `Software\Microsoft\EdgeUpdate\Clients\` + edgeUpdateClientGUID},
}

// RuntimeVersion 返回已安装的 WebView2 Evergreen 运行时版本号，
// 未安装时返回空串。版本号形如 152.0.4191.53。
//
// 读注册表而非探测 msedgewebview2.exe 文件：后者需要遍历多个 Program
// Files 候选路径，且 Fixed Version 分发形态下会漏判。
func RuntimeVersion() string {
	for _, k := range runtimeKeyPaths {
		key, err := registry.OpenKey(k.root, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		ver, _, err := key.GetStringValue("pv")
		_ = key.Close()
		// pv 存在但为空串或全零表示「曾安装后被卸载」，等同未安装
		if err == nil && ver != "" && ver != "0.0.0.0" {
			return ver
		}
	}
	return ""
}

// RuntimeInstalled 报告 WebView2 运行时是否可用。
func RuntimeInstalled() bool { return RuntimeVersion() != "" }

// udfLeaf 由前端产物指纹派生 UDF 目录名后缀：webview2 或 webview2-<slug>。
//
// fingerprint 为内嵌 dist 的产物文件名（如 "index-Abc.css + index-Def.js"），
// 取首个 js 文件名里 index- 与 .js 之间的 hash 段作 slug；为空、unknown 或
// 解析失败时退化为空串（目录名回到 webview2，兼容绑定生成阶段与异常路径）。
//
// 把 slug 编入 UDF 路径是关键：dist hash 一变，UDF 路径即变，WebView2
// 视为全新应用从零建缓存，杜绝「前端已更新但界面仍显示旧版」的缓存污染
// （v15/v17 反复出现用户删了 data/webview2 重启仍看到旧 UI 的事故）。
func udfLeaf(fingerprint string) string {
	const jsPrefix = "index-"
	// 找 "index-XXX.js" 里的 XXX：从首个 jsPrefix 到紧随其后的 ".js"
	idx := strings.Index(fingerprint, jsPrefix)
	for idx >= 0 {
		start := idx + len(jsPrefix)
		end := strings.Index(fingerprint[start:], ".js")
		if end > 0 {
			slug := fingerprint[start : start+end]
			if slug != "" {
				return "webview2-" + slug
			}
		}
		// 继续找下一个 index- 出现处（理论上只有一个 js，防御性遍历）
		next := strings.Index(fingerprint[start:], jsPrefix)
		if next < 0 {
			break
		}
		idx = start + next
	}
	return "webview2"
}

// UserDataDir 返回 WebView2 用户数据目录（UDF）。
//
// 必须显式指定：go-webview2 的默认值是 %AppData%\<exe 文件名>
// （见 go-webview2 pkg/edge/chromium.go 的 Embed 实现），既破坏
// CloudPrism「绿色便携、数据随 exe 走」的约定，也会让改名后的 exe
// 丢失既有缓存。
//
// fingerprint 编入目录名（见 udfLeaf）：每个前端版本用独立 UDF，
// 新版自动从零建缓存，用户无需手动清理。
//
// 目录不可写时（装在 Program Files 等受保护位置）静默降级到
// LOCALAPPDATA，与 pkg/paths 的降级策略保持一致 —— 宁可换位置也
// 不能让应用启动失败。
func UserDataDir(fingerprint string) string {
	leaf := udfLeaf(fingerprint)
	if p := os.Getenv("CLOUDPRISM_DATA_DIR"); p != "" {
		return filepath.Join(p, leaf)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exe), "data", leaf)
		if os.MkdirAll(dir, 0o755) == nil {
			return dir
		}
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		return filepath.Join(la, "CloudPrism", leaf)
	}
	return filepath.Join(os.TempDir(), "CloudPrism-"+leaf)
}

// CleanupStaleUDF 删除 exe 旁边 data/ 下除 current 外的旧 webview2* 目录。
//
// 每个前端版本一个 UDF（见 udfLeaf），升级后会遗留旧版本目录；启动时
// 顺手清理避免 data/ 下 webview2-xxx 堆积。current 传当前版本 UDF 全路径，
// 枚举同目录下 webview2* 前缀项，命中 current 的跳过，其余递归删除。
//
// 失败静默：清不掉旧缓存不阻断启动（最坏只是多占点磁盘）。
func CleanupStaleUDF(current string) {
	parent := filepath.Dir(current)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	base := filepath.Base(current)
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() {
			continue
		}
		if !strings.HasPrefix(name, "webview2") {
			continue
		}
		if name == base {
			continue // 当前版本，保留
		}
		_ = os.RemoveAll(filepath.Join(parent, name))
	}
}

// FatalMessage 弹出阻塞式错误框。
//
// GUI 子系统程序（-H windowsgui）没有控制台，启动期致命错误若不弹窗
// 就表现为「双击无反应」，用户完全无法自助排查。
func FatalMessage(title, text string) {
	caption, err1 := windows.UTF16PtrFromString(title)
	body, err2 := windows.UTF16PtrFromString(text)
	if err1 != nil || err2 != nil {
		// 文本含 NUL 之类极端情况：退化为 stderr，至少留痕
		os.Stderr.WriteString("CloudPrism 致命错误: " + text + "\n")
		return
	}
	// MB_ICONERROR(0x10) | MB_SYSTEMMODAL(0x1000)：系统模态确保窗口
	// 一定浮在最前，不会被主窗口遮住而看起来像「程序没反应」
	_, _ = windows.MessageBox(0, body, caption, 0x10|0x1000)
}
