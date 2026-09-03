//go:build !bindings

package main

// generatingBindings 在正常构建下恒为 false。
//
// 语义与用途见 bindings_mode.go —— 两个文件靠 `bindings` 构建标签互斥，
// 同一常量在两种构建下取不同值。
const generatingBindings = false
