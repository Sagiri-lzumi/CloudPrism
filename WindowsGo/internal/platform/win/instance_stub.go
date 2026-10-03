//go:build !windows

package win

// AcquireInstanceLock 在非 Windows 平台的桩实现：恒返回 true（不限制单实例）。
//
// 存在意义仅在于让 pkg/ 与 internal/ 能跨平台编译（本项目实际只服务
// Windows，见 ARCHITECTURE §1「Windows 专属」）。语义与真实现不同是有意的：
// 「无法建立单实例锁」时应当放行而不是阻断启动。
func AcquireInstanceLock(string) (bool, error) { return true, nil }
