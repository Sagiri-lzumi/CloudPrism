<!--
  App.vue —— 应用外壳：可折叠侧栏（品牌头 + 导航 + 目录树 + 页脚）+ 页面区 + 单条底栏。

  页面：files = 文件浏览；transfers = 传输任务；vaults = 密库（未连接引导/已连接
  信息，双态自处理）；settings = 偏好设置。

  导航语义（v1.3 重构）：
  · 侧栏是「去哪」的唯一入口，导航项一律**图标 + 文字**，不再靠 tooltip 猜；
  · 「目录树」不是导航，是文件页的上下文——只在本页且已连接时出现在侧栏，
    与导航项用分区标题隔开（此前它是文件页里独立的一栏，白占 200px 宽）；
  · 「锁定密库」保留在页脚（高频且在任意页面都需要），「退出应用」降级为页脚
    次级图标钮——它不是导航目的地，不该与四个页面平级。

  全局快捷键：F5 刷新、Ctrl+L 锁库、Ctrl+U 上传、Ctrl+D 下载选中；
  输入控件聚焦时全部忽略，避免打断输入。
-->
<script setup lang="ts">
import {computed, onBeforeUnmount, onMounted, ref} from 'vue'
import {
  ui,
  start,
  stop,
  navigate,
  reloadDir,
  lockVault,
  quitApp,
  onDropFiles,
  pickUploadFiles,
  downloadSel,
  clearRecovery,
  localPick,
  settleLocalPick,
  type PageId,
} from './lib/store'
import FilesView from './views/FilesView.vue'
import TransfersView from './views/TransfersView.vue'
import VaultsView from './views/VaultsView.vue'
import SettingsView from './views/SettingsView.vue'
import RecoveryCodeDlg from './views/wizard/RecoveryCodeDlg.vue'
import TransferBar from './components/layout/TransferBar.vue'
import StatusBar from './components/layout/StatusBar.vue'
import FolderPicker from './components/layout/FolderPicker.vue'
import UploadPlan from './components/layout/UploadPlan.vue'
import DirTree from './components/DirTree.vue'
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

/* --------------------------------------------------- 侧栏导航定义 */

// 侧栏导航项：icon/name/page
const NAV_ITEMS: ReadonlyArray<{page: PageId; icon: string; title: string}> = [
  {page: 'files', icon: 'folder', title: '文件'},
  {page: 'transfers', icon: 'sync', title: '传输'},
  {page: 'vaults', icon: 'certificate', title: '密库'},
  {page: 'settings', icon: 'setting', title: '设置'},
]

/** 传输项的角标：仅在有活动任务时出现，让「传输」不只是个入口。 */
const transferBadge = computed(() => {
  const n = ui.snap?.transferTasks ?? 0
  return n > 0 ? String(n > 99 ? '99+' : n) : ''
})

function badgeOf(page: PageId): string {
  return page === 'transfers' ? transferBadge.value : ''
}

/* ------------------------------------------------------ 侧栏折叠 */

const NAV_KEY = 'cp-nav-collapsed'
const navCollapsed = ref(localStorage.getItem(NAV_KEY) === '1')
function toggleNav() {
  navCollapsed.value = !navCollapsed.value
  localStorage.setItem(NAV_KEY, navCollapsed.value ? '1' : '0')
}

const connected = computed(() => !!ui.snap?.connected)
const isFilePage = computed(() => ui.page === 'files')

/** 目录树只在文件页出现：它表达的是「当前浏览位置」，不是全局导航。 */
const showTree = computed(() => isFilePage.value && connected.value)

/* ------------------------------------------------------ 全局快捷键 */

function onGlobalKey(e: KeyboardEvent) {
  const t = e.target as HTMLElement | null
  // 输入态（文本框/菜单项等）不抢快捷键
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return
  const mod = e.ctrlKey || e.metaKey

  if (e.key === 'F5') {
    e.preventDefault()
    if (connected.value && isFilePage.value) reloadDir()
    return
  }
  if (!mod) return
  const k = e.key.toLowerCase()
  // 命中应用动作时才 preventDefault：Web 服务模式下浏览器原生快捷键
  // （Ctrl+D 收藏、Ctrl+U 查看源码）只在「应用此刻不会响应」时放行
  if (k === 'l') {
    if (connected.value) {
      e.preventDefault()
      void lockVault()
    }
  } else if (k === 'u') {
    if (connected.value && isFilePage.value) {
      e.preventDefault()
      void pickUploadFiles()
    }
  } else if (k === 'd') {
    if (connected.value && isFilePage.value && ui.sel) {
      e.preventDefault()
      void downloadSel()
    }
  }
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
  <div class="app-shell" :class="{collapsed: navCollapsed}">
    <!-- 侧栏：品牌头 + 导航（+ 文件页目录树）+ 页脚 -->
    <nav class="app-nav">
      <div class="brand">
        <!-- 品牌位：真实应用图标（public/brand.png，与 README 同源）+ 品牌色柔光。
     不再用 cloud 轮廓图标 —— 那是「云盘品类」的通用符号，棱镜分光才是本产品。 -->
<span class="brand-mark"><img class="brand-logo" src="/brand.png" alt="" /></span>
        <span class="brand-name lbl">CloudPrism</span>
      </div>

      <div class="nav-body">
        <p class="nav-title lbl">浏览</p>
        <button
          v-for="n in NAV_ITEMS"
          :key="n.page"
          type="button"
          class="nav-item"
          :class="{on: ui.page === n.page}"
          :title="navCollapsed ? n.title : undefined"
          @click="navigate(n.page)"
        >
          <Icon :name="n.icon" :size="19" class="nav-ic" />
          <span class="nav-label lbl">{{ n.title }}</span>
          <!-- :key 绑角标数值：数值一变就换一个新节点，badge-pop 动画随之重播
               （否则同一个节点上只改文字，CSS 动画不会重新触发）。
               帧循环每 100ms 重渲染一次，但 key 不变 ⇒ 不会无谓重播。 -->
          <span v-if="badgeOf(n.page)" :key="badgeOf(n.page)" class="nav-badge">
            {{ badgeOf(n.page) }}
          </span>
        </button>

        <!-- 目录树：文件页的上下文，与导航用分区标题隔开 -->
        <template v-if="showTree">
          <p class="nav-title lbl">目录</p>
          <div class="nav-tree"><DirTree /></div>
        </template>
      </div>

      <!-- 页脚：只放「随时可用」的动作。连接态不在这里重复——
           它是状态不是动作，且底栏（StatusBar）已按项目约定承担该职责
           （含「完整性核对中 / 可续传」这类事件驱动辅助），两处都显示会互相打架。 -->
      <div class="nav-foot">
        <button
          type="button"
          class="foot-btn lock-btn"
          :disabled="!connected"
          :title="connected ? '锁定密库（Ctrl+L）' : '未连接'"
          @click="lockVault"
        >
          <Icon :name="connected ? 'lock' : 'lock_open'" :size="19" />
          <span class="lbl">锁定密库</span>
        </button>
        <!-- 收起/展开侧栏：原在品牌头（与品牌名抢位，窄栏还会压住图标），
             移到页脚与「锁定/退出」并排 —— 三者同为全局常驻动作，语义一致；
             并且它贴着侧栏右缘，箭头方向与「栏往哪动」直接对应。 -->
        <button
          type="button"
          class="foot-btn icon-only collapse-btn"
          :title="navCollapsed ? '展开侧栏' : '收起侧栏'"
          :aria-label="navCollapsed ? '展开侧栏' : '收起侧栏'"
          :aria-expanded="!navCollapsed"
          @click="toggleNav"
        >
          <Icon name="chevron_right_med" :size="19" :class="{flip: !navCollapsed}" />
        </button>
        <button
          type="button"
          class="foot-btn icon-only power-btn"
          title="退出应用"
          aria-label="退出应用"
          @click="quitApp"
        >
          <Icon name="power_button" :size="19" />
        </button>
      </div>
    </nav>

    <!-- 页面区（四个子页：files=文件；transfers=任务列表+续传/重试/清空；
         vaults=未连接显示欢迎+最近记录、已连接显示库信息/同步/恢复码；
         settings=外观/缓存/传输/安全/百度凭证/性能/关于）
         注意：分支之间严禁夹 HTML 注释 —— dev 模式（vite dev）注释会保留为
         Comment vnode 混进 Transition 子节点，out-in 切换时新子节点被解析成
         空注释 → 页面空白（prod 构建剥离注释故不复发）。 -->
    <main class="app-main">
      <!-- 页面过渡用默认模式（新旧页同时淡入淡出），不用 mode="out-in"：
           out-in 的「等旧页退完再挂新页」状态机在 dev 下会被 10Hz 状态帧
           触发的过渡中途更新卡死（新视图永不挂载，.app-main 只剩注释占位）。
           默认模式无等待状态机，结构上免疫此类卡死。 -->
      <Transition name="page">
        <FilesView v-if="ui.page === 'files'" key="files" class="page-fill" />
        <TransfersView v-else-if="ui.page === 'transfers'" key="transfers" class="page-fill" />
        <VaultsView v-else-if="ui.page === 'vaults'" key="vaults" class="page-fill" />
        <SettingsView v-else key="settings" class="page-fill" />
      </Transition>
    </main>

    <!-- 底栏：单条区域，传输条仅活动时在此展开（外框与顶边只由 .app-foot 提供） -->
    <footer class="app-foot">
      <TransferBar />
      <StatusBar />
    </footer>

    <!-- 通知条 host（队列在 lib/toast） -->
    <InfoBar />

    <!-- 全局恢复码模态：新建成功的一次性码展示，独立于页面生命周期 -->
    <RecoveryCodeDlg
      :open="ui.pendingRecovery !== ''"
      :code="ui.pendingRecovery"
      @close="clearRecovery"
    />

    <!-- 全局路径选择器（目录 / 文件双模式）：向导、设置页、密库页、导出、
         上传文件、导入文件夹共用。挂在全局是为了同一时刻只有一个实例，
         天然复用 ModalShell 的模态栈（Esc 只关最上层）。 -->
    <FolderPicker
      :open="localPick.open"
      :mode="localPick.mode"
      :title="localPick.title"
      :start="localPick.start"
      @confirm="(p) => settleLocalPick({dir: p})"
      @confirm-files="(ps) => settleLocalPick({files: ps})"
      @cancel="settleLocalPick(null)"
    />

    <!-- 直读本机路径上传的确认清单（只对「按钮」通路生效；拖放保持选完即传）。 -->
    <UploadPlan />

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
        <p v-if="!dropBusy" class="drop-hint">
          拖放需先经浏览器读取内容；文件夹较大时，用工具栏的「上传文件夹…」
          更快，它由本程序直接读本地文件，不占用浏览器缓存。
        </p>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* ============================================================ 侧栏
   宽度走 --nav-w（折叠时由 .app-shell.collapsed 换成 56px），
   品牌头/导航项/页脚全部按同一变量排版，折叠只切一个值。 */
.app-nav {
  display: flex;
  flex-direction: column;
  width: var(--nav-w, 216px);
  min-width: var(--nav-w, 216px);
  padding: 0;
  /* v1.4 折叠/展开动画化：只过渡列宽（custom property 换值触发的是
     computed width 变化，transition 拿得到）。子元素几何全程不变 ——
     文字由栏宽收窄 + overflow 自然裁掉（旧方案在折叠态 display:none
     文字 + 子元素切居中，动画首尾各跳变一次，即「收起时闪一下」的
     来源）。网格 auto 列随宽逐帧重排，一次性 200ms 可接受。
     ≤640px 下 width:auto 不可插值，过渡自动失效为 no-op，
     底部 TabBar 不受影响。 */
  transition: width var(--dur) var(--ease), min-width var(--dur) var(--ease);
  background: var(--glass-chrome);
  backdrop-filter: blur(var(--glass-blur)) saturate(var(--glass-sat));
  border-right: 1px solid var(--divider);
  box-sizing: border-box;
  overflow: hidden;
}

/* ---- 品牌头 ---- */
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: none;
  height: 58px;
  padding: 0 10px 0 14px;
  /* padding 可过渡：折叠时 14→13 的居中是平滑滑入而非跳变 */
  transition: padding var(--dur) var(--ease);
}

.brand-mark {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 30px;
  height: 30px;
}

/* 应用图标：圆角裁切 + 品牌色柔光（color-mix 随主题取当前 accent，
   深色下蓝更亮故光晕自动跟亮一档，无需手写两套色值） */
.brand-logo {
  width: 30px;
  height: 30px;
  border-radius: 8px;
  box-shadow: 0 2px 8px -1px color-mix(in srgb, var(--accent) 45%, transparent);
}

.brand-name {
  flex: 1;
  min-width: 0;
  font-size: 1.071rem;
  font-weight: 650;
  color: var(--heading);
  letter-spacing: 0.01em;
  white-space: nowrap;
  overflow: hidden;
}

/* 收起/展开钮（页脚 .foot-btn.icon-only 的变体）：尺寸/底色全部沿用 .foot-btn，
   这里只补箭头自身的动效 —— 展开态箭头朝左、收起态朝右，
   换向时箭头沿轴向弹一下，让「栏要往哪边动」有预告。 */
.collapse-btn {
  /* 不参与 .foot-btn 的 flex:1 拉伸：它是窄方钮，不是条状按钮 */
  flex: none;
}

/* 展开态朝左（flip）、收起态朝右：换向走 200ms 原地旋转，不换图标。
   必须 :deep + 选 .fluent-icon 外壳 span：Icon 组件的根是 span
   （svg 是它 v-html 的内层），class 透传落在外壳上 —— 此前选 svg.flip
   双层错位，是「展开时箭头不换向」的真正原因。 */
.collapse-btn :deep(.fluent-icon) {
  transition: transform var(--dur) var(--ease-spring);
}

.collapse-btn :deep(.fluent-icon.flip) {
  transform: rotate(180deg);
}

.collapse-btn:active svg {
  transform: scale(.85);
}

/* ---- 导航主体（可滚动：目录树长起来时导航项不被顶出视野） ---- */
.nav-body {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 0 10px;
  overflow-y: auto;
  overflow-x: hidden;
}

/* 分区标题：把「去哪」与「在哪」两种语义分开 */
.nav-title {
  margin: 12px 10px 5px;
  font-size: 0.786rem;
  font-weight: 600;
  color: var(--text2);
  letter-spacing: 0.06em;
  white-space: nowrap;
}

.nav-item {
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
  flex: none;
  height: 38px;
  padding: 0 10px;
  margin-bottom: 2px;
  font-family: inherit;
  font-size: 0.929rem;
  color: var(--text);
  background: transparent;
  border: none;
  /* 选中态是「整块圆角面」而不是细指示条：与苹果风侧栏一致 */
  border-radius: var(--radius-ctrl);
  text-align: left;
  /* padding 参与过渡：折叠时图标从「左对齐」平滑滑入栏中心（10→19），
     与栏宽收窄同步进行，杜绝任何跳变 */
  transition: background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease),
    transform var(--dur-fast) var(--ease-spring), padding var(--dur) var(--ease);
}

.nav-item:hover:not(.on) {
  background: color-mix(in srgb, var(--text) 7%, transparent);
}

/* 按压弹性：只做**收缩**不做位移 —— .nav-body 是 overflow-x:hidden 的容器，
   横向位移会把条目右端（角标/文字）直接裁掉。收缩缩的是自身盒内，永不越界。 */
.nav-item:active {
  transform: scale(.97);
}

.nav-item.on {
  background: var(--accent-soft);
  color: var(--accent);
  font-weight: 600;
}

.nav-ic {
  flex: none;
  color: var(--text2);
  transition: transform var(--dur-fast) var(--ease-spring);
}

/* hover 时图标向内侧轻推 1px：给「这一项要被点了」一点预告。
   推图标而不是推整行 —— 17px 的图标离条目边框很远，怎么挪都不会碰到裁剪边界。 */
.nav-item:hover:not(.on) .nav-ic {
  transform: translateX(1px);
}

.nav-item.on .nav-ic {
  color: var(--accent);
  /* 切页时图标弹一下：.on 是切页时新加上的类，动画随之重播。
     这是「Q 弹」在导航上最省的一次投放 —— 不改布局尺寸，只动图标。 */
  animation: nav-ic-pop var(--dur-spring) var(--ease-spring);
}

@keyframes nav-ic-pop {
  0% {
    transform: scale(.72);
  }
  100% {
    transform: scale(1);
  }
}

.nav-label {
  flex: 1;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 活动任务角标：让「传输」这个入口自己带上状态 */
.nav-badge {
  flex: none;
  min-width: 20px;
  height: 20px;
  padding: 0 6px;
  font-size: 0.786rem;
  font-weight: 600;
  line-height: 20px;
  color: var(--text-on-accent);
  background: var(--accent);
  border-radius: var(--radius-round);
  text-align: center;
  font-variant-numeric: tabular-nums;
  /* 计数变化时弹一下（触发条件见模板里 :key 的注释） */
  animation: badge-pop var(--dur-spring) var(--ease-spring);
}

@keyframes badge-pop {
  0% {
    transform: scale(.4);
  }
  100% {
    transform: scale(1);
  }
}

/* ---- 目录树宿主 ----
   树自己要撑满剩余高度并可滚动；侧栏已提供左右内边距，树不再自带。 */
.nav-tree {
  flex: 1;
  min-height: 120px;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

/* ---- 页脚：随时可用的动作（锁定 / 退出） ---- */
.nav-foot {
  flex: none;
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 10px;
  border-top: 1px solid var(--divider);
}

.foot-btn {
  display: flex;
  align-items: center;
  gap: 9px;
  flex: 1;
  min-width: 0;
  height: 34px;
  padding: 0 10px;
  font-family: inherit;
  font-size: 0.929rem;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  /* overflow 裁切：栏宽收窄时按钮随之变窄，标签在按钮内部被平滑裁掉
     （修复：此前按钮无裁切，「锁定密库」在窄栏里被挤成逐字竖排泄漏到栏外） */
  overflow: hidden;
  /* max-width 上限是收没动画的起点（锁定钮的实际宽度远小于它，
     折叠时 200→0 平滑收没；取 200 而非更大值，避免大差值压缩前半程速率） */
  max-width: 200px;
  transition: background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease),
    transform var(--dur-fast) var(--ease-spring), max-width var(--dur) var(--ease),
    padding var(--dur) var(--ease), opacity var(--dur) var(--ease);
}

.foot-btn .lbl {
  white-space: nowrap;
  min-width: 0;
}

.foot-btn:hover:not(:disabled) {
  background: color-mix(in srgb, var(--text) 7%, transparent);
  color: var(--text);
}

.foot-btn:active:not(:disabled) {
  transform: scale(.95);
}

.foot-btn:disabled {
  opacity: 0.4;
}

/* 次级图标钮（收起 / 退出）：不与「锁定密库」争夺视觉权重，等宽方形 */
.foot-btn.icon-only {
  flex: none;
  width: 34px;
  justify-content: center;
  padding: 0;
}

/* ============================================================ 折叠态
   v1.4 重构：折叠只收窄栏宽（--nav-w 200ms 过渡），**不改任何子元素的
   几何** —— 展开态的几何本来就兼容 56px（品牌头左 14 + 图标 30 的中心
   ≈ 栏中心 28；nav-body 10 + nav-item 10 + 半枚图标 ≈ 28 同理），文字
   由栏宽收窄 + overflow 平滑裁掉（flex:1 + min-width:0 + ellipsis 会让
   标签逐帧缩短成「文…」，而不是瞬消）。
   唯一的例外是「目录树 + 分区标题」：窄栏里它们只剩图标碎片，留着像
   显示错误 —— 用 opacity 渐隐（150ms，可过渡）替代 display:none 瞬消，
   与宽度动画同向叠加，整栏看起来是一个连续动作。 */

/* 折叠态保留品牌图标：56px 栏里它是唯一的身份标识，
   藏掉之后顶部只剩一片空白（且用户会以为「收起把 logo 弄丢了」）。 */
.app-shell.collapsed .brand-mark {
  flex: none;
}

/* 树与分区标题：渐隐而非瞬消（visibility 跟随 opacity 结束时离散步进，
   不占可点击区域）；展开时反向淡入。 */
.nav-tree,
.nav-title {
  transition: opacity 150ms var(--ease), visibility 150ms var(--ease);
}

.app-shell.collapsed .nav-tree,
.app-shell.collapsed .nav-title {
  opacity: 0;
  visibility: hidden;
}

/* 角标在折叠态改为吸附在图标右上角，避免把 34px 的行撑破 */
.app-shell.collapsed .nav-badge {
  position: absolute;
  top: 1px;
  right: 6px;
  min-width: 15px;
  height: 15px;
  padding: 0 3px;
  font-size: 0.643rem;
  line-height: 15px;
}

/* ---- 折叠态居中与页脚收没（仅桌面；移动端底栏有自己的排版） ----
   原则：只动**可过渡**属性（padding / max-width / opacity），
   不碰 display / flex-direction / justify-content 这类瞬切属性。
   栏中心 = 28px；logo 30px ⇒ padding 13+15=28；导航图标 19px ⇒
   padding 19+9.5=28.5 ≈ 28，全程随栏宽动画滑入中心。 */
@media (min-width: 641px) {
  .app-shell.collapsed .brand {
    padding: 0 13px;
  }

  .app-shell.collapsed .nav-item {
    /* 图标中心 = nav-body 左内距 10 + 此值 + 图标半径 9.5 = 28 ≈ 栏中心。
       （v1.4 初版误算成 19，漏加了 nav-body 的 10px，图标偏右 10.5px） */
    padding: 0 9px;
  }

  /* 页脚：锁定/退出两钮横向收没（max-width 200→0 + 淡出），
     只留收起钮 —— 页脚高度全程不变，上方不会产生空白带，
     也没有 flex-direction 瞬切的重排跳变。 */
  .app-shell.collapsed .nav-foot {
    gap: 0;
  }

  .app-shell.collapsed .foot-btn.lock-btn,
  .app-shell.collapsed .foot-btn.power-btn {
    max-width: 0;
    padding: 0;
    opacity: 0;
    pointer-events: none;
  }
}

/* ============================================================ 页面过渡
   .page-* 过渡基元定义在 styles/base.css（全局单一真源），此处**不要**再写一份。
   这里曾有一份 scoped 版本，而 scoped 选择器带 [data-v-*]、特异性高于全局同名类
   ⇒ 它一直压着 base.css 那份生效，导致「页面切换位移」这段设计其实从未被渲染过
   （只淡入、不位移）。删掉重复实现后 base.css 才真正接管。 */

.page-fill {
  height: 100%;
}

/* ---- 全窗口拖放遮罩 ----
   铺满视口、压住内容但低于模态框。pointer-events: none 是关键：
   遮罩若可接收指针事件，它自己就成了 drop 落点，虽然 window 上的 drop
   监听仍会触发，但 dragover 的 dropEffect 会被遮罩覆盖而丢失「复制」光标。 */
.drop-veil {
  position: fixed;
  inset: 0;
  z-index: var(--z-veil);
  display: flex;
  align-items: center;
  justify-content: center;
  pointer-events: none;
  /* 提示级暗罩：比模态遮罩轻，且不随主题变化（见 theme.css 的 --scrim-hint） */
  background: var(--scrim-hint);
  backdrop-filter: blur(6px);
  animation: veil-in var(--dur-fast) var(--ease);
}

/* 拖放卡：压在整屏内容之上的浮层 → 用 view 档玻璃（此处是毛玻璃最该看得见的地方之一）。
   过冲入场：卡片从 .9 冲到略大再落定，配合「松开即加密上传」这句提示，
   手感上比单纯淡入更像「有个东西接住了文件」。 */
.drop-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
  max-width: 380px;
  padding: 24px 32px;
  border: 2px dashed var(--accent);
  border-radius: var(--radius-card);
  background: var(--glass-view);
  backdrop-filter: blur(var(--glass-blur)) saturate(var(--glass-sat));
  box-shadow: var(--shadow-pop), inset 0 1px 0 var(--glass-edge);
  color: var(--accent);
  text-align: center;
  animation: drop-pop var(--dur-spring) var(--ease-spring);
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

/* 如实告知两条通路的差别：拖放受限于浏览器（只给 File 对象），必须先经浏览器
   读一遍；主动点按钮则是后端直读本机文件。不写这句，用户会以为「拖进来也一样」。 */
.drop-hint {
  margin: 6px 0 0;
  max-width: 30em;
  color: var(--text2);
  font-size: .78rem;
  line-height: 1.5;
  opacity: .85;
}

/* 读取文件夹期间让图标持续旋转，表明后台在枚举目录而不是卡住 */
.drop-veil.busy .drop-card :deep(svg) {
  animation: veil-spin 1.1s linear infinite;
}

@keyframes veil-in {
  from { opacity: 0; }
  to { opacity: 1; }
}

@keyframes drop-pop {
  0% {
    opacity: 0;
    transform: scale(.9);
  }
  100% {
    opacity: 1;
    transform: scale(1);
  }
}

@keyframes veil-spin {
  to { transform: rotate(360deg); }
}

/* ---- 手机（≤640px）：侧栏 → 底部横排 TabBar ----
   本组件 scoped 规则的 specificity 高于 layout.css 的裸类选择器，
   故侧栏的形态覆盖必须写在这里，写进 layout.css 会被这里压掉。
   品牌头与目录树是桌面语义（640px 下没有可用宽度），隐去；
   四个导航项平分宽度，右侧保留「锁定 / 退出」两个图标钮——
   它们是任意页面都用得到的动作，不该因为窄屏而消失。 */
@media (max-width: 640px) {
  .app-nav {
    flex-direction: row;
    align-items: center;
    width: auto;
    min-width: 0;
    padding: 4px 6px;
    border-right: none;
    border-top: 1px solid var(--divider);
  }

  .brand {
    display: none;
  }

  /* 横排时导航主体不再滚动，四项平分剩余宽度 */
  .nav-body {
    flex: 1;
    flex-direction: row;
    align-items: center;
    gap: 2px;
    padding: 0;
    min-width: 0;
    overflow: visible;
  }

  .nav-title,
  .nav-tree {
    display: none;
  }

  .nav-item {
    flex: 1;
    height: 46px;
    flex-direction: column;
    gap: 1px;
    justify-content: center;
    padding: 0;
    font-size: 0.714rem;
  }

  /* 页脚收成右侧动作簇：竖排改为横排，去掉顶边改左边的分隔线 */
  .nav-foot {
    flex: none;
    flex-direction: row;
    gap: 2px;
    padding: 0 0 0 6px;
    margin-left: 6px;
    border-top: none;
    border-left: 1px solid var(--divider);
  }

  .foot-btn {
    flex: none;
    width: 42px;
    height: 42px;
    justify-content: center;
    padding: 0;
  }

  /* 横排下只留图标：文字会把这 42px 的格子挤爆 */
  .foot-btn .lbl {
    display: none;
  }

  /* 触摸目标：文字标签在横排下必须常显（图标 + 文字才够明确） */
  .app-shell.collapsed .nav-item .lbl {
    display: block;
  }
}
</style>
