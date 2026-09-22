<!--
  ModalShell.vue —— 模态浮层外壳（遮罩 + 面板 + 统一页头 + 键盘可达性）。

  为什么要有这个组件：向导 / 恢复码 / 消息框 / 快速连接四处浮层各自复制了一份
  「Teleport + 遮罩 + 图标块 + 标题」和各自的 Esc 处理，而且**都不把焦点移进面板**。

  原先的 Esc 写法（在遮罩元素上挂 @keydown）只有「焦点恰好落在面板内部」时才生效，
  而真实路径两条都会把焦点留在面板外 —— 实测（playwright 驱动 Edge）：
    · 点开向导后立即按 Esc：焦点仍在触发按钮上（Teleport 之后它与面板没有祖孙
      关系，事件根本不经过遮罩）→ 关不掉；
    · Enter 翻步后按 Esc：步进用 v-if 换分支会销毁被聚焦的节点，焦点落回 body
      → 同样关不掉。
  只有「手动点一下面板内部的卡片再按 Esc」这一条路能关，而用户不会那么干。

  本组件把三件事收到一处：
  · 结构：遮罩 / 面板 / 页头（图标块 + 标题 + 补充）/ 体 / 页脚；
  · 焦点：打开时把焦点拉进面板（面板内已有元素自行接管时不抢，避免压掉表单
    自己的 autofocus）、关闭时归还给打开它的元素、Tab 在面板内循环；
  · 键盘：Esc 挂 window 冒泡（keydown 无论落在哪个元素都会冒到 window，与焦点
    位置无关），并用模块级栈保证只有最上层那个模态响应 —— 避免一次 Esc 连关两层。

  分层：z-index = --z-modal + 栈深度×10。原先四处各写 +100/+150/+200 的魔法偏移、
  还得在注释里解释「谁在谁上面」，现在由打开顺序唯一决定。

  栈必须放在 lib/modalStack.ts（真·模块级）。**不能**写成 <script setup> 里的顶层
  常量 —— 那个块的顶层代码是「每个组件实例跑一次」，每个浮层会各拿一份栈，于是
  层级全部打平、Esc 的「只关最上层」也失效。v1.6 就是这么把向导内的目录选择器
  盖死的（详见该文件头注释）。

  刻意保持的既有语义：**点击遮罩不关闭**（防误触，四处浮层原本一致）。
-->
<script setup lang="ts">
import {computed, nextTick, onBeforeUnmount, ref, watch} from 'vue'
import {isTopModal, popModal, pushModal} from '../../lib/modalStack'
import Icon from '../fluent/Icon.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title?: string
    /** 标题下的补充（如当前步骤）；留空不渲染 */
    sub?: string
    /** 页头图标块（icons.ts 注册表 key）；留空则只有标题 */
    icon?: string
    /** 图标块的语义色 */
    tone?: 'accent' | 'ok' | 'danger'
    /** 面板宽度上限（px；窄屏自动收成可用宽度） */
    width?: number
    /** 是否渲染右上角关闭钮 */
    closable?: boolean
    /** 是否允许 Esc 关闭（执行中可临时关掉，避免半途被 Esc 打断） */
    esc?: boolean
    /**
     * 打开时优先聚焦的选择器（面板内）。缺省聚焦面板自身。
     * 面板内已有元素拿到焦点时不抢 —— 让表单自己的 autofocus 生效。
     */
    autofocus?: string
  }>(),
  {
    title: '',
    sub: '',
    icon: '',
    tone: 'accent',
    width: 480,
    closable: false,
    esc: true,
    autofocus: '',
  },
)

const emit = defineEmits<{close: []}>()

/* ------------------------------------------------ 模态栈（Esc 与分层） */

// 栈本体在 lib/modalStack.ts（模块级单例）：同屏可能叠着多个模态（向导 + 目录
// 选择器 + 恢复码），只有最上层那个该响应 Esc，也只有它配拿到最高 z-index。
// 本实例只持有一个身份 id 与自己的深度镜像。
const id = Symbol('modal')

const depth = ref(0)
const panel = ref<HTMLElement>()
const active = ref(false) // 本模态当前是否已入栈

/** 打开前的焦点落点：关闭时归还，避免焦点丢到 body。 */
let opener: HTMLElement | null = null

const maskStyle = computed(() => ({
  zIndex: `calc(var(--z-modal) + ${depth.value * 10})`,
}))

function onWindowKey(e: KeyboardEvent) {
  if (!active.value || !props.esc) return
  if (e.key !== 'Escape' || !isTopModal(id)) return
  e.preventDefault()
  emit('close')
}

/** Tab 循环：把焦点锁在面板内（遮罩之下仍有可聚焦的页面元素）。 */
function onPanelKey(e: KeyboardEvent) {
  if (e.key !== 'Tab' || !panel.value) return
  const sel =
    'button:not([disabled]),[href],input:not([disabled]),select,textarea,[tabindex]:not([tabindex="-1"])'
  const list = Array.from(panel.value.querySelectorAll<HTMLElement>(sel)).filter(
    (el) => el.offsetParent !== null || el === document.activeElement,
  )
  if (list.length === 0) return
  const first = list[0]
  const last = list[list.length - 1]
  const cur = document.activeElement as HTMLElement | null
  const inside = !!cur && panel.value.contains(cur)
  if (e.shiftKey && (!inside || cur === first)) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && (!inside || cur === last)) {
    e.preventDefault()
    first.focus()
  }
}

function enter() {
  if (active.value) return
  active.value = true
  opener = (document.activeElement as HTMLElement | null) ?? null
  depth.value = pushModal(id)
  window.addEventListener('keydown', onWindowKey)
  void nextTick(() => {
    const cur = document.activeElement as HTMLElement | null
    // 面板内已有元素接管了焦点（如 LineEdit autofocus）就不要再抢
    if (cur && panel.value?.contains(cur)) return
    const target = props.autofocus
      ? panel.value?.querySelector<HTMLElement>(props.autofocus)
      : null
    ;(target ?? panel.value)?.focus()
  })
}

function leave() {
  if (!active.value) return
  active.value = false
  window.removeEventListener('keydown', onWindowKey)
  popModal(id)
  // depth 故意不复位：离场过渡期间仍维持原层级，免得淡出时突然掉到最底层
  // 归还焦点：打开者可能已随页面切换卸载，必须确认它仍在文档里
  if (opener && document.contains(opener)) opener.focus()
  opener = null
}

watch(() => props.open, (v) => (v ? enter() : leave()), {immediate: true})
onBeforeUnmount(leave)
</script>

<template>
  <Teleport to="body">
    <!-- name="ms"（而不是复用全局的 fade）：遮罩与面板要用两套动效 ——
         遮罩只淡入，面板还要过冲弹入。自定义名让这两条规则都收在本组件内，
         不再跨文件耦合 base.css 的 .fade-*。 -->
    <Transition name="ms">
      <!-- 遮罩点击不关闭：四处浮层原本的一致语义，防误触 -->
      <div v-if="open" class="ms-mask" :style="maskStyle">
        <div
          ref="panel"
          class="ms-panel"
          :class="`tone-${tone}`"
          role="dialog"
          aria-modal="true"
          :aria-label="title"
          tabindex="-1"
          :style="{width: `min(${width}px, 100%)`}"
          @keydown="onPanelKey"
        >
          <div v-if="title" class="ms-head">
            <span v-if="icon" class="ms-icon"><Icon :name="icon" :size="18" /></span>
            <div class="ms-titles">
              <div class="ms-title">{{ title }}</div>
              <div v-if="sub" class="ms-sub">{{ sub }}</div>
            </div>
            <slot name="head-extra" />
            <button
              v-if="closable"
              type="button"
              class="ms-x"
              title="关闭"
              aria-label="关闭"
              @click="emit('close')"
            >
              <Icon name="cancel" :size="14" />
            </button>
          </div>

          <!-- 条带区（页头之下、滚动体之上）：步骤指示这类「常驻不滚」的内容放这里 -->
          <div v-if="$slots.bar" class="ms-bar"><slot name="bar" /></div>

          <div class="ms-body"><slot /></div>
          <div class="ms-foot">
            <span class="ms-lead"><slot name="foot-lead" /></span>
            <div class="ms-actions"><slot name="actions" /></div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.ms-mask {
  position: fixed;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  /* 24px 呼吸位：内容接近满高时面板不贴边 */
  padding: 24px;
  background: var(--veil);
}

.ms-panel {
  display: flex;
  flex-direction: column;
  max-height: 100%;
  /* 焦点落在面板自身（如无输入的可聚焦内容）时不要画描边 */
  outline: none;
  /* view 档玻璃：模态压在整个界面之上，是毛玻璃效果最该看得见的地方。
     面板宽度有上限、背后是静态的遮罩底，模糊代价可忽略。
     inset 高光发丝线补在阴影之前 —— 面板无描边，这条线是「玻璃有厚度」的唯一暗示。 */
  background: var(--glass-view);
  backdrop-filter: blur(var(--glass-blur)) saturate(var(--glass-sat));
  border-radius: var(--radius-card);
  box-shadow: var(--shadow-pop), inset 0 1px 0 var(--glass-edge);
}

/* ---- 页头：36px 图标块 + 标题（+ 补充）---- */
.ms-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: none;
  padding: 18px 22px 12px;
}

.ms-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 36px;
  height: 36px;
  color: var(--accent);
  background: var(--accent-soft);
  border-radius: var(--radius-ctrl);
}

.tone-ok .ms-icon {
  color: var(--ok);
  background: color-mix(in srgb, var(--ok) 14%, transparent);
}

.tone-danger .ms-icon {
  color: var(--err);
  background: color-mix(in srgb, var(--err) 12%, transparent);
}

.ms-titles {
  flex: 1;
  min-width: 0;
}

.ms-title {
  overflow: hidden;
  font-size: 1rem;
  font-weight: 600;
  color: var(--heading);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ms-sub {
  margin-top: 1px;
  font-size: 0.857rem;
  color: var(--text2);
}

.ms-x {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 30px;
  height: 30px;
  color: var(--muted);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  transition: background var(--dur-fast) var(--ease);
}

.ms-x:hover {
  background: color-mix(in srgb, var(--text) 8%, transparent);
}

/* ---- 条带区：常驻（不随体滚动）---- */
.ms-bar {
  flex: none;
  padding: 0 22px 12px;
}

/* ---- 体：唯一滚动区（面板 max-height 封顶后由它承担溢出）---- */
.ms-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 4px 22px 16px;
}

/* ---- 页脚：左侧说明 + 右侧动作 ---- */
.ms-foot {
  display: flex;
  align-items: center;
  gap: 12px;
  flex: none;
  padding: 14px 22px 18px;
  border-top: 1px solid var(--divider);
}

.ms-lead {
  flex: 1;
  min-width: 0;
  font-size: 0.786rem;
  color: var(--muted);
}

.ms-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
}

/* ---- 进出场：遮罩淡入 + 面板过冲弹入 ----
   Transition 类只落在遮罩元素上（面板是它的子元素），两条动效各归各的。

   ⚠️ 面板的过冲动画**不能**写成 `.ms-enter-active .ms-panel { animation: ... }`。
   Vue 判断「进出场何时结束」只看 `<Transition>` 根元素（这里是遮罩）身上的
   `transition` 时长 —— 遮罩淡入用的是 --dur-fast（120ms），于是 120ms 后
   `ms-enter-active` 就被摘掉，面板 460ms 的过冲动画**随之被截断**（动画属性不再
   生效 ⇒ 元素立刻跳回静止态）。实测在 460ms 窗口内轮询 `.ms-panel` 的
   animationName 恒为 `none`，就是这个原因；肉眼看是「弹到四分之一就没了」。
   故改为**直接挂在 .ms-panel 上**：面板随 `v-if` 新建，动画在元素被创建时自然触发，
   与过渡类的寿命无关（同一条路子在 DropCard / nav-ic 上已验证可靠）。
   代价是面板没有独立的退场动画 —— 退场只由遮罩淡出承担，与改前一致。 */
.ms-enter-active {
  transition: opacity var(--dur-fast) var(--ease);
}

.ms-leave-active {
  transition: opacity calc(var(--dur) / 2) var(--ease);
}

.ms-enter-from,
.ms-leave-to {
  opacity: 0;
}

.ms-panel {
  animation: ms-pop var(--dur-spring) var(--ease-spring);
}

@keyframes ms-pop {
  0% {
    opacity: 0;
    transform: scale(.94) translateY(8px);
  }
  100% {
    opacity: 1;
    transform: scale(1) translateY(0);
  }
}

/* 手机：页头/条带/体/页脚内边距收窄一档，否则 375px 下正文只剩半行宽 */
@media (max-width: 640px) {
  .ms-head {
    padding: 14px 14px 10px;
  }

  .ms-bar {
    padding: 0 14px 10px;
  }

  .ms-body {
    padding: 4px 14px 14px;
  }

  .ms-foot {
    padding: 12px 14px 14px;
  }
}

/* 进出场动效见上方 .ms-* 段（原先是复用 base.css 的 .fade-*，本组件另需面板过冲，
   故独立成 ms 前缀，不再跨文件共用那一份）。 */
</style>
