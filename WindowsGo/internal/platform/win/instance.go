//go:build windows

package win

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// instanceMutex 持有进程级单实例互斥体句柄。
//
// 故意不提供释放函数：进程退出时内核自动释放（见 AcquireInstanceLock
// 的说明），持有句柄直到进程结束正是我们要的语义。
var instanceMutex windows.Handle

// AcquireInstanceLock 尝试独占 key（通常是数据目录）对应的单实例锁。
//
// 返回 ok=false 表示**已有另一个进程**持有同一 key 的锁，调用方应放弃启动。
// 返回 err 表示锁本身无法建立（环境异常）；此时按 fail-open 处理更安全：
// 单实例约束失败不该让用户完全打不开程序，故调用方应照常启动。
//
// 为什么用命名互斥体而不是锁文件：
//   - 内核对象随进程消亡自动释放 —— 进程崩溃/被强杀后不会留下「僵尸锁」
//     把用户永久挡在门外（锁文件必须靠 PID+存活探测或超时来兜底，复杂且易错）。
//   - 无文件系统残留，不需要清理逻辑，也不会被误删。
//
// 为什么 key 先做 SHA-256 再取前 16 字节：互斥体名不能含反斜杠（它被用作
// 命名空间分隔），而 Windows 路径含反斜杠；同时固定长度避免名称过长。
// 用 Local\ 前缀（而非 Global\）使锁限定在当前登录会话内 —— 与本程序的
// 数据目录模型一致，也避免了多用户/终端服务场景下跨会话互相阻塞。
func AcquireInstanceLock(key string) (bool, error) {
	// key 归一化：同一数据目录的不同写法必须映射到同一把锁。
	//   - 转绝对路径：CLOUDPRISM_DATA_DIR 允许相对路径，而调用方 cwd 不同
	//     会得到不同 key；
	//   - Clean + 小写：消除 "./"、多余分隔符、正反斜杠与盘符大小写差异。
	// 漏掉任何一步都意味着「同一 data/ 两个进程各持一把锁」，即单实例失效。
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	key = strings.ToLower(filepath.Clean(key))
	sum := sha256.Sum256([]byte(key))
	name := `Local\CloudPrism-` + hex.EncodeToString(sum[:16])
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	h, err := windows.CreateMutex(nil, false, namePtr)
	if err != nil {
		// ERROR_ALREADY_EXISTS 也可能是普通错误码；CreateMutex 成功时
		// 用 GetLastError 判定是否「已存在」，见下方处理。
		if err == windows.ERROR_ALREADY_EXISTS {
			return false, nil
		}
		return false, err
	}
	// CreateMutex 在「名字已存在」时仍返回有效句柄，须看 LastError。
	if lastErr := windows.GetLastError(); lastErr == windows.ERROR_ALREADY_EXISTS {
		_ = windows.CloseHandle(h)
		return false, nil
	}
	instanceMutex = h
	return true, nil
}
