//go:build windows

// Package win 提供 Windows 平台能力：目前只有 DPAPI 数据保护。
//
// 纯 syscall（x/sys/windows），无 cgo —— 与阶段 2 S3 spike 验证过的
// 往返路径一致，且与 Python 端 _dpapi_protect/_dpapi_unprotect
// （ctypes crypt32）在同一用户下双向互读（S3 已 PASS）。
package win

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Protect 用 DPAPI（当前用户级）加密数据，对应 Python _dpapi_protect。
// 加密结果与用户登录会话绑定：换用户或迁移机器后无法解密（预期行为）。
func Protect(data []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(data))}
	if len(data) > 0 {
		in.Data = &data[0]
	}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, fmt.Errorf("DPAPI 加密失败: %w", err)
	}
	// out 由系统分配，必须 LocalFree；先拷贝出明文副本再释放
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	result := make([]byte, int(out.Size))
	copy(result, unsafe.Slice(out.Data, int(out.Size)))
	return result, nil
}

// Unprotect 解密 DPAPI 数据，对应 Python _dpapi_unprotect。
func Unprotect(data []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(data))}
	if len(data) > 0 {
		in.Data = &data[0]
	}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, fmt.Errorf("DPAPI 解密失败: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	result := make([]byte, int(out.Size))
	copy(result, unsafe.Slice(out.Data, int(out.Size)))
	return result, nil
}
