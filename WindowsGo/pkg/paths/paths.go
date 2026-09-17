// Package paths 定义便携数据路径（WindowsGo 端）。
//
// 便携化语义与 Python 端一致（对照 core/paths.py）：设置、凭证等持久化
// 数据一律放在程序目录旁的 data/ 子目录，跟随程序目录走——绿色版拷走即
// 整体迁移；不写注册表、不写 %APPDATA%，不污染系统。
//
// 与 Python 端的差异及理由：
//   - Python 打包态取 sys.executable 目录、源码运行上溯到 WindowsPy/；
//     Go 端统一以 os.Executable() 所在目录为准（wails 产出的 exe、开发态
//     go run 的临时二进制都各带一份 data/，语义一致且无需上溯魔法）。
//   - 开发态覆盖：环境变量 CLOUDPRISM_DATA_DIR 非空时数据目录整体重定向
//     （wails dev / go test 等场景不想让数据写进临时目录/源码树时使用）。
//   - qfluentwidgets 的 qfluent_config.json 是 Python 端库专属文件，
//     Go 端不存在该库，故不提供对应函数。
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// DataDirEnv 是数据目录覆盖的环境变量名（开发态专用，见包注释）。
const DataDirEnv = "CLOUDPRISM_DATA_DIR"

// AppDir 返回程序所在目录（os.Executable 的目录）。
// 解析失败（极罕见的运行时环境异常）时退回当前目录，保证应用能启动。
func AppDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// DataDir 返回数据目录：CLOUDPRISM_DATA_DIR 非空时整体重定向到该值，
// 否则为程序目录旁的 data/。不保证目录存在（写入时机调用 EnsureDataDir）。
func DataDir() string {
	if override := os.Getenv(DataDirEnv); override != "" {
		return override
	}
	return filepath.Join(AppDir(), "data")
}

// EnsureDataDir 尽力创建数据目录；失败静默（程序目录不可写如 Program
// Files 时由上层降级为「不持久化但照常运行」，对齐 Python 端 data_dir
// create=True 的吞异常语义）。
func EnsureDataDir() {
	_ = os.MkdirAll(DataDir(), 0o755)
}

// ConfigFile 返回旧版设置文件路径 data/cloudprism.ini。
//
// Python 端 SettingsStore 用 QSettings(IniFormat) 写的就是这个文件；
// Go 端仅把它作为「一次性只读导入」的遗留来源（见 pkg/settings 的
// ini_import），新设置写入 SettingsFile 指向的 JSON。
func ConfigFile() string {
	return filepath.Join(DataDir(), "cloudprism.ini")
}

// SettingsFile 返回 Go 端设置持久化文件路径 data/cloudprism_settings.json。
// 与旧 INI 分开命名，避免导入流程与写入流程互相踩踏同一文件。
func SettingsFile() string {
	return filepath.Join(DataDir(), "cloudprism_settings.json")
}

// BaiduCredentialFile 返回百度网盘凭证文件路径 data/baidu.json
// （内容为 DPAPI/PLAIN 前缀 + base64 密文，见 pkg/storage.BaiduCredStore）。
func BaiduCredentialFile() string {
	return filepath.Join(DataDir(), "baidu.json")
}

// TempDir 返回自管临时目录 data/tmp/。
//
// 不用系统 %TEMP%：部分沙箱/受管环境下 %TEMP% 的 ACL 不完整，创建临时
// 文件会遭拒绝访问；改落产品自己管理的数据目录，跟随程序目录整体迁移。
// create=true 时确保目录存在；创建失败返回 ok=false，调用方退回系统
// 默认临时目录（对齐 Python 端返回 None 的语义）。
func TempDir(create bool) (dir string, ok bool) {
	d := filepath.Join(DataDir(), "tmp")
	if create {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return d, false
		}
	}
	return d, true
}

// ---------------------------------------------------------------------------
// 缓存目录（大文件分块读缓存 / 缩略图缓存）
// ---------------------------------------------------------------------------

// DefaultCacheDir 返回缓存根目录的默认值：程序目录旁 data/cache。
//
// 便携化语义与 DataDir 一致 —— 绿色版把程序目录拷到哪，缓存就跟到哪；
// 拷到 D 盘就落 D 盘，拷走即整体迁移。
//
// **红线**：本函数是缓存目录的唯一默认来源，绝不回退系统 %TEMP% /
// %APPDATA% / %LOCALAPPDATA%。历史实现（Python 端与 Go 端早期版本）在
// 未配置时把缩略图缓存写进 %TEMP%\cloudprism_cache，会持续吃满系统盘，
// 这正是本次改造要根除的行为。
func DefaultCacheDir() string {
	return filepath.Join(DataDir(), "cache")
}

// EnsureCacheDir 尽力创建缓存根目录；成功返回路径，失败返回 ok=false。
//
// 失败时调用方应**降级为不缓存**，而不是改投系统临时目录 —— 见
// DefaultCacheDir 的红线说明。
func EnsureCacheDir(root string) (string, bool) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return root, false
	}
	return root, true
}

// ThumbCacheDir 返回缩略图加密缓存的落盘目录（<缓存根>/thumbs）。
func ThumbCacheDir(root string) string { return filepath.Join(root, "thumbs") }

// MediaCacheDir 返回媒体分块读缓存的落盘目录（<缓存根>/media）。
func MediaCacheDir(root string) string { return filepath.Join(root, "media") }

// systemCacheBases 列出「不允许作为缓存目录」的系统位置所对应的环境变量。
//
// 覆盖三类：系统临时目录、用户漫游/本地配置目录、系统与程序目录。
// 变量缺失（非 Windows 或未设置）时自动跳过。
var systemCacheBases = []string{
	"TEMP", "TMP",
	"APPDATA", "LOCALAPPDATA",
	"SystemRoot", "windir",
	"ProgramFiles", "ProgramFiles(x86)", "ProgramData",
	"USERPROFILE",
}

// ForbiddenCacheDir 报告 dir 是否落在系统目录内（缓存红线校验）。
//
// 命中返回非空原因说明（供设置页直接展示）。判定用 filepath.Rel 而非
// 字符串前缀，避免 "C:\TempX" 被误判为落在 "C:\Temp" 之下。
//
// 注意：这里**不**笼统地禁止整个 C 盘 —— 程序目录本身可能就在 C 盘，
// 用户选择的缓存根跟随程序目录即可；红线是「不得把缓存写进系统位置，
// 尤其是 %TEMP% / %APPDATA% 这类会被系统清理且位于系统盘的用户目录」。
func ForbiddenCacheDir(dir string) string {
	if dir == "" {
		return ""
	}
	target, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for _, env := range systemCacheBases {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		absBase, err := filepath.Abs(base)
		if err != nil {
			continue
		}
		if within(absBase, target) {
			return "该位置位于系统目录 " + absBase + " 内，缓存不允许写进系统盘位置"
		}
	}
	return ""
}

// within 报告 target 是否等于 base 或位于 base 之下（大小写不敏感，
// Windows 盘符与路径均不区分大小写）。
func within(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	rel = strings.ToLower(rel)
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
