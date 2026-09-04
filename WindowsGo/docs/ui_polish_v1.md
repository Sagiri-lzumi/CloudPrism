# WindowsGo UI 视觉对照检查表 v1（对照 WindowsPy）

> 活文档：随代码演进迭代。目的 = 把「UI 不如 Python 版好看」的观感问题拆成
> 可核对的条目；每项给 Python 源锚点 + Go 现状 + 结论（已一致 / 已改 / 待确认）。
> 结论为「待用户截图确认」的项不阻塞交付，等用户在 Go 版实测截图上复核后逐条收敛。
> 维护：每次视觉改动在本表补一行并更新结论，勿留悬空条目。

## A. 已一致（代码层已对齐 qfw/Python 默认，无需动作）

| # | 检查点 | Python 锚点 | Go 现状 / 结论 |
|---|---|---|---|
| A1 | 强调色与语义色逐字对译 | theme.py:37-67（THEME_COLOR、语义色双套） | theme.css `--accent #0067b8`、`--ok/--err/--warn/--muted` 双主题逐字对译，深色由 `html[data-theme=dark]` 覆盖。已一致 |
| A2 | 文件页分栏比例与可拖拽 | main_window.py:185-189（QSplitter stretch 1:2） | FilesView.vue Splitter：默认 400px、240px 下限、比例接近 1:2，位置持久化（localStorage）。已一致 |
| A3 | 状态栏位于窗口底部 | main_window.py:235-243（底部状态栏分段） | layout.css `.app-status` grid-row:3（App.vue 挂 TransferBar+StatusBar）。已一致 |
| A4 | 全局焦点环 / 选中底色 | qfw 默认 focus 强调色外圈、选中 accent-soft | base.css `:focus-visible` 2px accent + offset、`::selection` accent-soft（深色分支同 token）。已一致 |
| A5 | 滚动条细样式 | qfw 默认细滚动条 | base.css 10px、thumb hover 才亮（--muted 55%/75% 混合）、track 透明。已一致 |
| A6 | 字号体系 px 语义（防 pt 放大事故） | theme.py:115-119（记录过 pt@96DPI 放大教训） | base.css：html 根 `--font-size` 档位 + rem 等比，标题/正文同比例缩放。已一致 |
| A7 | 圆角 / 阴影层级 | qfw 默认：卡片 8px、控件 4px | theme.css `--radius-card 8px`、`--radius-ctrl 4px`、`--shadow-pop` 菜单/对话框 24px。已一致 |
| A8 | 深色主题表面分层 | theme.py:50-67（dark 分支 #202020/#292929） | theme.css dark：bg-page #202020、surface #292929（含 auto 跟随系统）。已一致 |

## B. 本批已改（本轮 UI 修复与打磨提交落地项）

| # | 检查点 | Python 锚点 | Go 现状 / 结论 |
|---|---|---|---|
| B1 | 顶部误置「状态横幅」删除 | Python 无此横幅（状态只在底部） | App.vue 移除顶部 absolute op-banner 及样式/依赖；op 忙碌态改由底部状态栏承接（提交 7a78cf4）。已改 |
| B2 | 底部状态栏承接连接进度（三态） | main_window.py:235-243 | StatusBar.vue：未连接（灰点+「未连接」）/ 连接中（橙点呼吸脉冲 + 后端 progress 文案直通）/ 已连接（绿点+密库名·后端·时长）。已改 |
| B3 | 状态栏视觉存在感 | main_window.py:235-243 | StatusBar.vue 高度 28→30px、连接中文案用主文字色、脉冲环动画。已改 |
| B4 | 新建密库成功的一次性恢复码必现 | RecoveryCodeDialog（Python 向导完成后模态） | 恢复码写入 store，由 App.vue 全局模态展示（页面切换/向导卸载不影响），三处调用方统一 finally endOp 复位（提交 86f0827/7a78cf4）。已改 |
| B5 | 死代码 token 清理 | —— | theme.css 删除零消费的 `--acrylic-bg/--acrylic-blur`（注释指向 ARCHITECTURE.md 差异 #13）。已改 |

## C. 待用户截图确认（不阻塞交付；按反馈逐条收敛）

| # | 检查点 | Python 锚点 | Go 现状 / 待核对点 |
|---|---|---|---|
| C1 | 页面边距/卡片间距/分组标题字重 | vault_info_page.py:66-120（RecentVaultCard margins 16,12,56,12、连接钮 76px）；side_panel.py:150-188（toolbar margins 8,4,8,0） | Go 用 --page-pad/--card-gap token；逐页观感（密库页卡片、欢迎区、文件页工具栏留白）需并排截图复核；分组标题字重 600/500 差异待确认 |
| C2 | 工具栏按钮尺寸与图标一致性 | side_panel.py:158/163（list/grid 钮 26×26） | FilesView 工具栏 iconOnly Button 统一尺寸；与 26px 的视觉差、图标字形（Segoe Fluent Icons 对 qfw icon font）需并排截图 |
| C3 | 对话框/菜单圆角阴影与 hover | init_wizard.py:65-318、quick_connect.py（qfw 对话框） | Go 向导/恢复码对话框用 --radius-card + --shadow-pop；关闭钮 hover、遮罩深浅需实测复核 |
| C4 | 窗口默认尺寸 | main_window.py:154（1200×700） | Go main.go:90-93 为 1280×800 + Min 960×600（更大不劣化，留待用户偏好确认） |
| C5 | 深色主题观感 | theme.py dark 分支 | token 对译但 QSS 与 CSS 渲染路径不同，深色下卡片层次感需截图确认 |
| C6 | 文字渲染观感 | Qt 字体平滑 | WebView2 灰度抗锯齿（base.css 已设 antialiased），粗细观感需实测确认 |

## 操作说明

- 用户在 Go 版逐项核对后，把 C 类行改为「已一致」或附差异描述（截图放 Release/_polish/ 亦可，不入库）。
- 涉及代码修改的条目：改完在 B 类补一行并保留 A 类不动；全部收敛后本表可折叠进 ARCHITECTURE.md。
