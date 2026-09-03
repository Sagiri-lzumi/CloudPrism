# CloudPrism Go 端手工冒烟清单

自动化测试覆盖不到的部分（GUI 渲染、真实网盘、系统交互）在这里逐项手工验证。
**每完成一个阶段就把对应小节的勾选项跑一遍**，发现回归立刻停下修，不要攒到最后。

运行前置：

```powershell
cd C:\Codes\CloudPrism\WindowsGo
$env:CGO_ENABLED = "0"
wails build -platform windows/amd64
# 产物：build\bin\CloudPrismGo.exe
```

> ⚠️ 以下所有需要窗口的项，**必须在 IDE 之外的普通 PowerShell 里启动 exe**。
> IDE 的沙箱会禁止 Chromium 建立 Mojo IPC 通道，窗口起不来（详见
> `docs/ARCHITECTURE.md` 第 4 节）。

---

## 阶段 3：工程骨架

- [ ] `wails doctor` 全绿（WebView2 ✅，不提示需要 gcc）
- [ ] `go vet ./...` 无输出
- [ ] `wails build` 成功，产出 `build/bin/CloudPrismGo.exe`
- [ ] 双击 exe：窗口标题为 `CloudPrism`，图标是项目图标（非 Wails 默认 logo）
- [ ] 任务栏 / 资源管理器右键属性 → 详细信息：产品版本 `1.0.0`、
      文件说明 `CloudPrism 端到端加密云盘`、版权 `AGPL-3.0`
- [ ] **启动瞬间不闪黑窗**（`-H windowsgui` 生效）
- [ ] 页面「绑定往返」一栏显示绿色 `pong:skeleton`（= 阶段 2 的 S1b 验证通过）
- [ ] 页面「运行时」一栏显示 `Go go1.27.x · WebView2 <版本> · CGO_ENABLED=0 · windows/amd64`
- [ ] exe 同级生成 `data\webview2\`，且 `%AppData%\CloudPrismGo.exe` **不存在**
      （证明 UDF 显式生效，便携约定未被破坏）
- [ ] 点「退出」按钮，进程正常结束，任务管理器无残留 `CloudPrismGo.exe`
- [ ] DPI 缩放 125% / 150% 下窗口文字不模糊、不错位（`wails.exe.manifest` 的
      permonitorv2 生效）

## 阶段 4–5：核心与绑定层

_待阶段推进后补充。_

## 阶段 6：界面

_待阶段推进后补充。_

## 阶段 7：发布产物

_待阶段推进后补充。_
