//go:build bindings

package main

// generatingBindings 报告当前是否处于 Wails 的「绑定生成」构建。
//
// `wails build` 的第一步是 `go build -tags bindings -o %TEMP%\wailsbindings.exe .`
// 然后**在项目目录里直接运行它** —— 也就是说 main() 会真实执行一遍，只是
// wails.Run 在该 tag 下被换成「生成前端绑定后立即返回」的实现
// （见 wails v2 internal/app/app_bindings.go）。
//
// 因此所有只为真实运行准备的副作用都必须用本常量挡住，否则：
//   - 缺 WebView2 运行时的构建机会让 `wails build` 直接失败，甚至弹出
//     系统模态错误框卡住整条构建流水线；
//   - os.Executable() 此时指向 %TEMP%\wailsbindings.exe，会在系统临时目录里
//     落下 data\webview2 垃圾目录（已实测复现）。
const generatingBindings = true
