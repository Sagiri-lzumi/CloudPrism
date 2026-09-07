<!--
  App.vue —— v21 应用外壳：56px 顶部水平导航栏 + 页面区 + 底栏。
  导航从原左侧 48px 图标轨改为顶部页签（Logo + 文件/传输/密库/设置
  居中 + 右侧 主题/锁定/退出 + 连接状态点），对照 docs/ui-redesign/v21-mock.html。
  页面：files = 文件浏览（页头+网格/列表+预览）；transfers = 传输任务；
  vaults = 密库；settings = 偏好设置。
  全局快捷键：F5 刷新、Ctrl+L 锁库、Ctrl+U 上传、Ctrl+D 下载选中、
  Esc 退出多选（文件页且多选>1 且无菜单/对话框打开时）；
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
  clearMulti,
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

// 顶部导航页签：icon/title/page/badge（传输页角标显示未完成任务数）
const NAV_ITEMS = [
  {page: 'files', icon: 'folder', title: '文件', badge: false},
  {page: 'transfers', icon: 'sync', title: '传输', badge: true},
  {page: 'vaults', icon: 'certificate', title: '密库', badge: false},
  {page: 'settings', icon: 'setting', title: '设置', badge: false},
] as const

const connected = () => !!ui.snap?.connected
const isFilePage = () => ui.page === 'files'
// 传输页角标数：未完成任务（运行/等待）
const transferBadge = () =>
  ui.tasks.filter((t) => !['done', 'failed', 'cancelled'].includes(t.state)).length

/* ------------------------------------------------------ 全局快捷键 */

function onGlobalKey(e: KeyboardEvent) {
  const t = e.target as HTMLElement | null
  // 输入态（文本框/菜单项等）不抢快捷键
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return

  // Esc：文件页且多选>1 且无打开的对话框时退出多选。
  // 不拦截 Esc —— 菜单/对话框各自用自己 capture 阶段的 Esc 处理器先收，
  // 这里只在「没有它们打开」时兜底清多选。
  if (e.key === 'Escape') {
    if (isFilePage() && ui.multi.length > 1) {
      // 菜单/对话框打开时让它们优先处理（它们的 Esc 处理器在 capture 阶段已注册）
      const dialogOpen = document.querySelector('.cp-menu, .qc-mask, .msg-mask, .dlg-open')
      if (!dialogOpen) {
        e.preventDefault()
        clearMulti()
      }
    }
    return
  }

  if (e.key === 'F5') {
    e.preventDefault()
    if (connected() && isFilePage()) reloadDir()
    return
  }
  const mod = e.ctrlKey || e.metaKey
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
    <!-- 顶部水平导航栏（替代原左侧 48px 图标轨） -->
    <header class="app-topbar">
      <div class="brand">
        <span class="brand-logo" aria-hidden="true">
          <svg viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
            <path d="M12 3l7 4v5c0 4.5-3 7.5-7 9-4-1.5-7-4.5-7-9V7l7-4z" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"/>
            <path d="M9 12l2 2 4-4" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
          </svg>
        </span>
        <span class="brand-name">CloudPrism</span>
      </div>

      <nav class="nav-tabs" aria-label="主导航">
        <button
          v-for="n in NAV_ITEMS"
          :key="n.page"
          type="button"
          class="nav-tab"
          :class="{on: ui.page === n.page}"
          :title="n.title"
          @click="navigate(n.page)"
        >
          <Icon :name="n.icon" :size="17" />
          <span class="nav-label">{{ n.title }}</span>
          <span
            v-if="n.badge && transferBadge() > 0"
            class="nav-badge"
          >{{ transferBadge() }}</span>
        </button>
      </nav>

      <div class="topbar-right">
        <span class="conn-dot" :class="{ok: connected()}" :title="connected() ? '已连接' : '未连接'" />
        <button
          type="button"
          class="topbar-btn"
          title="锁定密库（Ctrl+L）"
          :disabled="!connected()"
          @click="lockVault"
        >
          <Icon :name="connected() ? 'lock' : 'lock_open'" :size="18" />
        </button>
        <button type="button" class="topbar-btn" title="退出" @click="quitApp">
          <Icon name="power_button" :size="18" />
        </button>
      </div>
    </header>

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
  </div>
</template>

<style scoped>
/* ---- 品牌 ---- */
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 1rem;
  font-weight: 700;
  color: var(--heading);
  letter-spacing: -0.01em;
  flex: none;
}

.brand-logo {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  background: var(--accent-grad);
  border-radius: 9px;
  color: var(--text-on-accent);
  box-shadow: 0 2px 8px color-mix(in srgb, var(--accent) 35%, transparent);
}

.brand-logo svg {
  width: 18px;
  height: 18px;
}

.brand-name {
  white-space: nowrap;
}

/* ---- 顶部页签组 ---- */
.nav-tabs {
  display: flex;
  align-items: center;
  gap: 2px;
  height: 100%;
  flex: 1;
  justify-content: center;
}

.nav-tab {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  height: 36px;
  padding: 0 16px;
  border-radius: var(--radius-round);
  color: var(--text2);
  font-size: 0.9rem;
  font-weight: 500;
  transition: background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease);
}

.nav-tab:hover:not(.on) {
  background: var(--surface-hover);
  color: var(--text);
}

.nav-tab.on {
  color: var(--accent);
  background: var(--accent-soft);
  font-weight: 600;
}

.nav-label {
  white-space: nowrap;
}

/* 传输页角标：未完成任务数 */
.nav-badge {
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: var(--radius-round);
  background: var(--accent);
  color: var(--text-on-accent);
  font-size: 0.68rem;
  font-weight: 700;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  line-height: 1;
}

.nav-tab:not(.on) .nav-badge {
  background: var(--muted);
}

/* ---- 右侧操作簇 ---- */
.topbar-right {
  display: flex;
  align-items: center;
  gap: 4px;
  flex: none;
}

.topbar-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  border-radius: var(--radius-round);
  color: var(--text2);
  transition: background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease);
}

.topbar-btn:hover:not(:disabled) {
  background: var(--surface-hover);
  color: var(--accent);
}

.topbar-btn:disabled {
  opacity: 0.3;
}

/* 连接状态点 */
.conn-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 4px;
  background: var(--warn);
  opacity: 0.85;
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--warn) 16%, transparent);
}

.conn-dot.ok {
  background: var(--ok);
  opacity: 1;
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--ok) 16%, transparent);
}

/* ---- 页面切换淡入（保留原过渡语义） ---- */
.page-enter-active,
.page-leave-active {
  transition: opacity var(--dur) var(--ease), transform var(--dur) var(--ease);
}

.page-enter-from,
.page-leave-to {
  opacity: 0;
}

.page-enter-from {
  transform: translateY(6px);
}

.page-fill {
  height: 100%;
}
</style>
