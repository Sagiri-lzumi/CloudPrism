<!--
  App.vue —— 应用外壳：48px 导航轨（内联 NavRail）+ 页面区 + 底栏。
  页面：files = 文件浏览（三栏）；transfers/settings = s6c 前的占位；
  vaults = 未连接时的引导占位（向导 s6c 交付）或密库信息占位。
  全局快捷键：F5 刷新、Ctrl+L 锁库、Ctrl+U 上传、Ctrl+D 下载选中；
  输入控件聚焦时全部忽略，避免打断输入。
-->
<script setup lang="ts">
import {onBeforeUnmount, onMounted} from 'vue'
import {
  ui,
  start,
  stop,
  navigate,
  reloadDir,
  lockVault,
  quitApp,
  uploadPaths,
  downloadSel,
} from './lib/store'
import FilesView from './views/FilesView.vue'
import StubPage from './views/StubPage.vue'
import TransferBar from './components/layout/TransferBar.vue'
import StatusBar from './components/layout/StatusBar.vue'
import InfoBar from './components/fluent/InfoBar.vue'
import Icon from './components/fluent/Icon.vue'
import ProgressBar from './components/fluent/ProgressBar.vue'
import {Transfer, unwrap} from './lib/api'
import {showError} from './lib/toast'

onMounted(() => {
  start()
  window.addEventListener('keydown', onGlobalKey)
})

onBeforeUnmount(() => {
  stop()
  window.removeEventListener('keydown', onGlobalKey)
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

/** 快捷键 Ctrl+U 共用：弹原生文件选择框入队上传。 */
async function pickUpload() {
  try {
    const paths = await Transfer.UploadDialog()
    if (paths && paths.length) void uploadPaths(paths)
  } catch (e) {
    showError('上传失败：' + unwrap(e).message)
  }
}
</script>

<template>
  <div class="app-shell">
    <!-- 长操作忙碌条（向导/恢复码期间全局可见） -->
    <Transition name="fade">
      <div v-if="ui.opBusy && ui.opText" class="op-banner">
        <span class="op-text">{{ ui.opText }}</span>
        <div class="op-track"><ProgressBar indeterminate /></div>
      </div>
    </Transition>

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
          title="锁定密库（Ctrl+L）"
          :disabled="!connected()"
          @click="lockVault"
        >
          <Icon name="fingerprint" :size="20" />
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

        <!-- 传输页：任务管理在 s6c 落地 -->
        <StubPage
          v-else-if="ui.page === 'transfers'"
          key="transfers"
          icon="sync"
          title="传输任务"
          :sub="connected()
            ? '上传/下载任务列表、续传与失败重试将在初始化向导完成后交付。'
            : '连接密库后即可上传与下载。'"
        />

        <!-- 密库页：未连接 = 引导占位；已连接 = 库信息占位（s6c 向导） -->
        <StubPage
          v-else-if="ui.page === 'vaults'"
          key="vaults"
          :icon="connected() ? 'certificate' : 'cloud'"
          :title="connected() ? (ui.snap?.vaultName ?? '密库') : '初始化或连接密库'"
          :sub="connected()
            ? '密库管理页将在下一步交付：同步状态、恢复码与安全设置。'
            : 'CloudPrism 将文件加密后存入本地目录或云盘；连接后文件在此浏览与播放。'"
          action="向导开发中（即将推出）"
        />

        <!-- 设置页 -->
        <StubPage
          v-else
          key="settings"
          icon="setting"
          title="设置"
          sub="主题、字体与网络设置在下一步交付。"
        />
      </Transition>
    </main>

    <!-- 底栏：传输条（仅活动时占位）+ 状态条 -->
    <TransferBar class="app-transfer" />
    <StatusBar class="app-status" />

    <!-- 通知条 host（队列在 lib/toast） -->
    <InfoBar />
  </div>
</template>

<style scoped>
/* 导航轨按钮组 */
.nav-group {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
}

.nav-space {
  flex: 1;
}

.nav-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 40px;
  height: 40px;
  margin: 2px 0;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: 6px;
  transition: background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease);
}

.nav-btn:hover:not(:disabled) {
  background: color-mix(in srgb, var(--text) 8%, transparent);
  color: var(--text);
}

.nav-btn:disabled {
  opacity: 0.35;
}

.nav-btn.on {
  color: var(--accent);
  background: var(--accent-soft);
}

.nav-btn.dim {
  color: var(--text2);
}

/* 连接状态点（导航轨底部） */
.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin: 6px 0 2px;
  background: var(--warn);
  opacity: 0.8;
}

.dot.ok {
  background: var(--ok);
}

/* 忙碌横幅：顶部通栏细条 */
.op-banner {
  position: absolute;
  top: 0;
  left: 48px;
  right: 0;
  z-index: 800;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 4px 16px 5px;
  font-size: 0.857rem;
  color: var(--text);
  background: color-mix(in srgb, var(--surface) 88%, transparent);
  backdrop-filter: blur(6px);
}

.op-text {
  white-space: nowrap;
}

.op-track {
  flex: 1;
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

.fade-enter-active,
.fade-leave-active {
  transition: opacity var(--dur) var(--ease);
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}

/* app-nav 底内边距收束（layout.css 已定义网格轨道） */
.app-nav {
  display: flex;
  flex-direction: column;
  padding: 8px 4px;
  background: color-mix(in srgb, var(--surface) 60%, var(--bg-page));
  border-right: 1px solid var(--divider);
  box-sizing: border-box;
}
</style>
