<!--
  App.vue —— 应用外壳：48px 导航轨（内联 NavRail）+ 页面区 + 底栏。
  页面：files = 文件浏览（三栏）；transfers = 传输任务；vaults = 密库
  （未连接引导/已连接信息，双态自处理）；settings = 偏好设置。
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
  uploadFiles,
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

/** 快捷键 Ctrl+U 共用：浏览器文件选择框入队上传。 */
async function pickUpload() {
  const input = document.createElement('input')
  input.type = 'file'
  input.multiple = true
  input.onchange = () => {
    if (input.files?.length) void uploadFiles(Array.from(input.files))
  }
  input.click()
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
  border-radius: 6px;
  transition: background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease);
}

/* 选中态左侧 2px 指示条（VS Code/ActivityBar 语义） */
.nav-btn.on::before {
  content: "";
  position: absolute;
  left: -4px;
  top: 50%;
  transform: translateY(-50%);
  width: 2px;
  height: 22px;
  border-radius: 2px;
  background: var(--accent);
}

.nav-btn:hover:not(:disabled) {
  background: color-mix(in srgb, var(--text) 8%, transparent);
  color: var(--text);
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

/* app-nav 底内边距收束（layout.css 已定义网格轨道） */
.app-nav {
  display: flex;
  flex-direction: column;
  padding: 8px 4px;
  background: color-mix(in srgb, var(--surface) 60%, var(--bg-page));
  border-right: 1px solid var(--divider);
  box-sizing: border-box;
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
