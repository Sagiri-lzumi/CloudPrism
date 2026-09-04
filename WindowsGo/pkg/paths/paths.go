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
