<!--
  App.vue —— 应用外壳：48px 导航轨（内联 NavRail）+ 页面区 + 底栏。
  页面：files = 文件浏览（三栏）；transfers = 传输任务；vaults = 密库
  （未连接引导/已连接信息，双态自处理）；settings = 偏好设置。
  全局快捷键：F5 刷新、Ctrl+L 锁库、Ctrl+U 上传、Ctrl+D 下载选中；
  输入控件聚焦时全部忽略，避免打断输入。
-->
<script setup lang="ts">
import {onBeforeUnmount, onMounted, ref} from 'vue'
import {
  ui,
  start,
  stop,
  navigate,
  reloadDir,
  lockVault,
  quitApp,
  uploadFromFileList,
  onDropFiles,
  downloadSel,
  clearRecovery,
} from './lib/store'
import FilesView from './views/FilesView.vue'
import TransfersView from './views/TransfersView.vue'
import VaultsView from './views/VaultsView.vue'
import SettingsView from './views/SettingsView.vue'
import RecoveryCodeDlg from './views/wizard/RecoveryCodeDlg.vue'
import TransferBar from './components/layout/TransferBar.vue'
import StatusBar from './components/layout/StatusBar.vue'
import InfoBar from './components/fluent/InfoBar.vue'
import Icon from './components/fluent/Icon.vue'

onMounted(() => {
  start()
  window.addEventListener('keydown', onGlobalKey)
  // 全窗口拖放热区：任意页面都能拖入上传（见下方「全窗口拖放」段）
  window.addEventListener('dragenter', onDragEnter)
  window.addEventListener('dragover', onDragOver)
  window.addEventListener('dragleave', onDragLeave)
  window.addEventListener('drop', onDrop)
})

onBeforeUnmount(() => {
  stop()
  window.removeEventListener('keydown', onGlobalKey)
  window.removeEventListener('dragenter', onDragEnter)
  window.removeEventListener('dragover', onDragOver)
  window.removeEventListener('dragleave', onDragLeave)
  window.removeEventListener('drop', onDrop)
})

/* --------------------------------------------------- 页面导航定义 */

// NavRail 中段导航项：icon/name/page
const NAV_ITEMS = [
  {page: 'files', icon: 'folder', title: '文件', badge: false},
  {page: 'transfers', icon: 'sync', title: '传输', badge: true},
  {page: 'vaults', icon: 'certificate', title: '密库', badge: false},
  {page: 'settings', icon: 'setting', title: '设置', badge: false},
] as const

const connected = () => !!ui.snap?.connected
const isFilePage = () => ui.page === 'files'

/* ------------------------------------------------------ 全局快捷键 */

function onGlobalKey(e: KeyboardEvent) {
  const t = e.target as HTMLElement | null
  // 输入态（文本框/菜单项等）不抢快捷键
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return
  const mod = e.ctrlKey || e.metaKey

  if (e.key === 'F5') {
    e.preventDefault()
    if (connected() && isFilePage()) reloadDir()
    return
  }
  if (!mod) return
  const k = e.key.toLowerCase()
  if (k === 'l') {
    if (connected()) void lockVault()
  } else if (k === 'u') {
    if (connected() && isFilePage()) void pickUpload()
  } else if (k === 'd') {
    if (connected() && isFilePage() && ui.sel) void downloadSel()
  }
}

/** 快捷键 Ctrl+U 共用：浏览器文件选择框入队上传。 */
async function pickUpload() {
  const input = document.createElement('input')
  input.type = 'file'
  input.multiple = true
  input.onchange = () => {
    if (input.files?.length) void uploadFromFileList(input.files)
  }
  input.click()
}

/* -------------------------------------- 全窗口拖放（拖入即加密上传） */

// 热区挂在 window 上而非某一页：文件/传输/密库/设置任意页面都能接住，
// 用户不必先切回文件页。目标目录取 store 的当前浏览目录（ui.remote）。
//
// 加密发生在入队后的传输管线里（上传 = 加密 → 分块上传），所以「拖入即加密」
// 就是「拖入即入队」；这里只负责把浏览器 File 交给队列，并给出明确反馈。
const dropActive = ref(false)
const dropBusy = ref(false)

// dragenter/dragleave 会随光标跨过子元素反复触发，用深度计数抵消抖动，
// 否则遮罩会疯狂闪烁。
let dragDepth = 0

/** 只对「拖的是文件」做出反应：拖选文本/链接时不得弹上传遮罩。 */
function isFileDrag(e: DragEvent): boolean {
  const types = e.dataTransfer?.types
  // types 是 DOMStringList，各浏览器都支持 includes/length，这里用最保守的写法
  return !!types && Array.prototype.indexOf.call(types, 'Files') !== -1
}

function onDragEnter(e: DragEvent) {
  if (!isFileDrag(e)) return
  dragDepth++
  dropActive.value = true
}

function onDragOver(e: DragEvent) {
  if (!isFileDrag(e)) return
  // 必须 preventDefault：否则浏览器不会派发 drop，而是直接打开该文件
  e.preventDefault()
  if (e.dataTransfer) e.dataTransfer.dropEffect = 'copy'
}

function onDragLeave(e: DragEvent) {
  if (!isFileDrag(e)) return
  dragDepth = Math.max(0, dragDepth - 1)
  // 光标移出窗口时 relatedTarget 为 null（子元素间移动时不为 null），直接收敛
  if (dragDepth === 0 || e.relatedTarget === null) {
    dragDepth = 0
    dropActive.value = false
  }
}

async function onDrop(e: DragEvent) {
  if (!isFileDrag(e)) return
  e.preventDefault()
  dragDepth = 0
  dropActive.value = false
  const dt = e.dataTransfer
  if (!dt) return
  // 读取文件夹可能耗时（大目录要逐层枚举）：先亮「正在读取」态，避免像没反应。
  // 注意 collectDropped 会在首个 await 之前同步取完所有 entry，因此 dt 在
  // 事件返回后失效也不影响后续读取。
  dropBusy.value = true
  try {
    await onDropFiles(dt)
  } finally {
    dropBusy.value = false
  }
}
</script>

<template>
  <div class="app-shell">
    <!-- NavRail：图标导轨，上下两组（对照 qfw ActivityBar） -->
    <nav class="app-nav">
      <div class="nav-group top">
        <button
          v-for="n in NAV_ITEMS"
          :key="n.page"
          type="button"
          class="nav-btn"
          :class="{on: ui.page === n.page}"
          :title="n.title"
          @click="navigate(n.page)"
        >
          <Icon :name="n.icon" :size="20" />
        </button>
      </div>
      <div class="nav-space" />
      <div class="nav-group bottom">
        <span class="dot" :class="connected() ? 'ok' : ''" title="连接状态" />
        <button
          type="button"
          class="nav-btn"
          :class="{dim: !connected()}"
          :title="connected() ? '锁定密库（Ctrl+L）' : '未连接'"
          :disabled="!connected()"
          @click="lockVault"
        >
          <Icon :name="connected() ? 'lock' : 'lock_open'" :size="20" />
        </button>
        <button
          type="button"
          class="nav-btn"
          title="退出"
          @click="quitApp"
        >
          <Icon name="power_button" :size="20" />
        </button>
      </div>
    </nav>

    <!-- 页面区 -->
    <main class="app-main">
      <Transition name="page" mode="out-in">
        <FilesView v-if="ui.page === 'files'" key="files" class="page-fill" />

        <!-- 传输页：任务列表 + 续传/重试/清空 -->
        <TransfersView v-else-if="ui.page === 'transfers'" key="transfers" class="page-fill" />

        <!-- 密库页：未连接=欢迎+最近记录；已连接=库信息/同步/恢复码 -->
        <VaultsView v-else-if="ui.page === 'vaults'" key="vaults" class="page-fill" />

        <!-- 设置页：外观/缓存/传输/安全/百度凭证/性能/关于 -->
        <SettingsView v-else key="settings" class="page-fill" />
      </Transition>
    </main>

    <!-- 底栏：传输条（仅活动时占位）+ 状态条 -->
    <TransferBar class="app-transfer" />
    <StatusBar class="app-status" />

    <!-- 通知条 host（队列在 lib/toast） -->
    <InfoBar />

    <!-- 全局恢复码模态：新建成功的一次性码展示，独立于页面生命周期 -->
    <RecoveryCodeDlg
      :open="ui.pendingRecovery !== ''"
      :code="ui.pendingRecovery"
      @close="clearRecovery"
    />

    <!-- 全窗口拖放遮罩：拖入文件时铺满视口，明确告知「松开即加密上传」。
         pointer-events: none 保证它不抢 drop 目标（否则遮罩自己成为落点）。 -->
    <div v-if="dropActive" class="drop-veil" :class="{busy: dropBusy}">
      <div class="drop-card">
        <Icon :name="dropBusy ? 'update' : 'folder-up'" :size="34" />
        <p class="drop-title">{{ dropBusy ? '正在读取…' : '松开即加密上传' }}</p>
        <p class="drop-sub">
          {{
            dropBusy
              ? '正在展开文件夹内容，大目录需要一点时间'
              : '文件与文件夹均可；文件夹会保留目录结构，上传前在本地加密'
          }}
        </p>
        <p v-if="!dropBusy" class="drop-dest">目标位置：{{ ui.remote || '密库根目录' }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* 导航轨按钮组 */
.nav-group {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
}

.nav-space {
  flex: 1;
}

.nav-btn {
  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 40px;
  height: 40px;
  margin: 2px 0;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  transition: background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease),
    transform var(--dur-fast) var(--ease);
}

/* 选中态左侧指示条（ActivityBar 语义）：圆头短条 + 淡辉光 */
.nav-btn.on::before {
  content: "";
  position: absolute;
  left: -4px;
  top: 50%;
  transform: translateY(-50%);
  width: 3px;
  height: 20px;
  border-radius: var(--radius-round);
  background: var(--accent);
  box-shadow: 0 0 8px var(--accent-ring);
}

.nav-btn:hover:not(:disabled) {
  background: color-mix(in srgb, var(--text) 7%, transparent);
  color: var(--text);
}

.nav-btn:active:not(:disabled) {
  transform: scale(.94);
}

.nav-btn:disabled {
  opacity: 0.3;
}

.nav-btn.on {
  color: var(--accent);
  background: var(--accent-soft);
}

.nav-btn.on:hover:not(:disabled) {
  background: color-mix(in srgb, var(--accent) 14%, transparent);
}

.nav-btn.dim {
  color: var(--text2);
}

/* 底部操作簇：小圆点 + 锁定/退出，整体圆角聚组，hover 分明 */
.nav-group.bottom {
  gap: 2px;
  padding-top: 6px;
  margin-top: 6px;
  border-top: 1px solid var(--divider);
}

/* 连接状态点（导航轨底部） */
.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin: 4px 0 4px;
  background: var(--warn);
  opacity: 0.85;
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--warn) 16%, transparent);
}

.dot.ok {
  background: var(--ok);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--ok) 16%, transparent);
}

/* 页面切换淡入（qfw StackedWidget 过渡语义） */
.page-enter-active,
.page-leave-active {
  transition: opacity var(--dur) var(--ease);
}

.page-enter-from,
.page-leave-to {
  opacity: 0;
}

.page-fill {
  height: 100%;
}

/* app-nav 底内边距收束（layout.css 已定义网格轨道）
   半透底 + 毛玻璃：环境光渐层从下方透出，导航轨不再是"一块死板的灰条" */
.app-nav {
  display: flex;
  flex-direction: column;
  padding: 8px 4px;
  background: var(--nav-bg);
  backdrop-filter: blur(16px) saturate(1.5);
  border-right: 1px solid var(--divider);
  box-sizing: border-box;
}

/* ---- 全窗口拖放遮罩 ----
   铺满视口、压住内容但低于模态框。pointer-events: none 是关键：
   遮罩若可接收指针事件，它自己就成了 drop 落点，虽然 window 上的 drop
   监听仍会触发，但 dragover 的 dropEffect 会被遮罩覆盖而丢失「复制」光标。 */
.drop-veil {
  position: fixed;
  inset: 0;
  z-index: 800;
  display: flex;
  align-items: center;
  justify-content: center;
  pointer-events: none;
  /* 兼容性优先：不用 color-mix，深浅两主题下都是标准遮罩观感 */
  background: rgba(0, 0, 0, .28);
  backdrop-filter: blur(2px);
  animation: veil-in var(--dur-fast) var(--ease);
}

.drop-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  max-width: 380px;
  padding: 24px 32px;
  border: 2px dashed var(--accent);
  border-radius: var(--radius-card);
  background: var(--surface);
  box-shadow: var(--shadow-pop);
  color: var(--accent);
  text-align: center;
}

.drop-title {
  margin: 6px 0 0;
  color: var(--text);
  font-size: 1rem;
  font-weight: 600;
}

.drop-sub {
  margin: 0;
  color: var(--text2);
  font-size: .85rem;
  line-height: 1.5;
}

.drop-dest {
  margin: 2px 0 0;
  color: var(--accent);
  font-size: .8rem;
}

/* 读取文件夹期间让图标持续旋转，表明后台在枚举目录而不是卡住 */
.drop-veil.busy .drop-card :deep(svg) {
  animation: veil-spin 1.1s linear infinite;
}

@keyframes veil-in {
  from { opacity: 0; }
  to { opacity: 1; }
}

@keyframes veil-spin {
  to { transform: rotate(360deg); }
}

/* ---- 手机（≤640px）：导航轨竖轨 → 底部横排 TabBar ----
   本组件 scoped 规则的 specificity 高于 layout.css 的裸类选择器，
   故导航轨的形态覆盖必须写在这里，写进 layout.css 会被这里压掉。 */
@media (max-width: 640px) {
  .app-nav {
    grid-row: 4;
    grid-column: 1;
    flex-direction: row;
    align-items: center;
    padding: 4px 6px;
    border-right: none;
    border-top: 1px solid var(--divider);
  }

  /* 横排后原左侧选中指示条改为底部 2px 短条（TabBar 选中语义） */
  .nav-btn.on::before {
    left: 50%;
    top: auto;
    bottom: 0;
    width: 22px;
    height: 2px;
    transform: translateX(-50%);
  }

  /* 上下两组由纵向堆叠改横向：导航项 | 弹性空隙 | 锁定/退出 */
  .nav-group {
    flex-direction: row;
    gap: 4px;
  }

  .nav-space {
    flex: 1;
  }

  /* 操作簇的分隔线由顶部改为左侧（横排后"上"变"左"） */
  .nav-group.bottom {
    padding: 0 0 0 8px;
    margin: 0 0 0 8px;
    border-top: none;
    border-left: 1px solid var(--divider);
  }

  /* 触摸目标放大到 44px（iOS HIG 最小点击区），避免手机误触 */
  .nav-btn {
    width: 44px;
    height: 44px;
    margin: 0;
  }
}
</style>
