//go:build !windows

// Package win 的非 Windows 占位：无 DPAPI 能力。
// 凭证存储的调用方应捕获错误后走 PLAIN 明文兜底（对齐 Python 行为）。
package win

import "errors"

var errNoDPAPI = errors.New("DPAPI 仅支持 Windows")

// Protect 在非 Windows 平台恒失败（调用方兜底 PLAIN）。
func Protect([]byte) ([]byte, error) { return nil, errNoDPAPI }

// Unprotect 在非 Windows 平台恒失败。
func Unprotect([]byte) ([]byte, error) { return nil, errNoDPAPI }
