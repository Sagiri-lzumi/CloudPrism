<!--
  RoundMenu.vue —— 弹出菜单（qfw RoundMenu 对应物：下拉与右键共用）。
  行为：open 时按 anchor 元素定位（下方优先、越界翻向），点击条目上抛
  index 并关闭；点外部 / Esc / 滚动关闭。Teleport 到 body 顶层。
-->
<script setup lang="ts">
import {computed, nextTick, onBeforeUnmount, ref, watch} from 'vue'
import Icon from './Icon.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    /** 锚点元素（getBoundingClientRect 定位）；与 position 二选一 */
    anchor: HTMLElement | null
    /** 光标坐标（右键/更多钮菜单用）：提供时优先于 anchor，菜单在鼠标处弹出 */
    position?: {x: number; y: number} | null
    items: {
      /** 条目文案（divider 项忽略） */
      label?: string
      /** 前置图标（可选） */
      icon?: string
      disabled?: boolean
      /** 危险操作条目（文字/图标转语义红） */
      danger?: boolean
      /** 条目分组（渲染分隔线，忽略其它字段） */
      divider?: boolean
    }[]
    /** 上方留白（px） */
    gap?: number
  }>(),
  {anchor: null, position: null, gap: 4},
)

const emit = defineEmits<{
  select: [index: number]
  close: []
}>()

const pos = ref({left: 0, top: 0})
const menu = ref<HTMLElement>()

// 打开瞬间测量定位；菜单先渲染再量（v-if + nextTick 由 watch 保证）
watch(
  () => props.open,
  async (open) => {
    if (!open) return
    await nextTick()
    const el = menu.value!
    // 用 offsetWidth/offsetHeight 测量：pop 入场动画带 scale(.96)，
    // getBoundingClientRect 会测到缩放后的偏小值，导致定位偏低/溢出
    const mw = el.offsetWidth
    const mh = el.offsetHeight
    // 锚点基准点：光标坐标（右键/更多）优先，其次锚点元素（下拉）
    let baseX: number
    let baseY: number
    let baseH = 0
    if (props.position) {
      baseX = props.position.x
      baseY = props.position.y
    } else if (props.anchor) {
      const a = props.anchor.getBoundingClientRect()
      baseX = a.left
      baseY = a.top
      baseH = a.height
    } else {
      return // 无锚点不定位
    }
    // 默认基准点下方弹出；空间不足翻到上方
    let top = baseY + baseH + props.gap
    if (top + mh > innerHeight) top = baseY - props.gap - mh
    // 双向钳位：菜单必须完整落在视口内（防底部被裁）
    top = Math.max(4, Math.min(top, innerHeight - mh - 4))
    const left = Math.max(4, Math.min(baseX, innerWidth - mw - 4))
    pos.value = {left, top}
  },
)

function pick(i: number) {
  if (props.items[i].disabled) return
  emit('select', i)
  emit('close')
}

function onDocDown(e: PointerEvent) {
  // 点击菜单内部不关（条目点击走 pick）；点外部关闭
  if (!menu.value?.contains(e.target as Node)) emit('close')
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}

watch(
  () => props.open,
  (open) => {
    if (open) {
      window.addEventListener('pointerdown', onDocDown, true)
      window.addEventListener('keydown', onKey, true)
      window.addEventListener('blur', onDocBlur)
      // 滚动/缩放时收起：菜单为 fixed 定位，容器滚动后继续悬浮会脱离锚点
      window.addEventListener('scroll', onDocScroll, true)
      window.addEventListener('resize', onDocScroll)
    } else {
      window.removeEventListener('pointerdown', onDocDown, true)
      window.removeEventListener('keydown', onKey, true)
      window.removeEventListener('blur', onDocBlur)
      window.removeEventListener('scroll', onDocScroll, true)
      window.removeEventListener('resize', onDocScroll)
    }
  },
)

function onDocBlur() {
  emit('close') // 窗口失焦（如切系统任务）即收起菜单
}

// 滚动/窗口尺寸变化：fixed 菜单不跟随滚动容器，直接收起避免错位悬浮
function onDocScroll() {
  emit('close')
}

onBeforeUnmount(() => {
  window.removeEventListener('pointerdown', onDocDown, true)
  window.removeEventListener('keydown', onKey, true)
  window.removeEventListener('blur', onDocBlur)
  window.removeEventListener('scroll', onDocScroll, true)
  window.removeEventListener('resize', onDocScroll)
})

const menuStyle = computed(() => ({left: pos.value.left + 'px', top: pos.value.top + 'px'}))
</script>

<template>
  <Teleport to="body">
    <Transition name="pop">
      <div
        v-if="open"
        ref="menu"
        class="cp-menu"
        :style="menuStyle"
        role="menu"
        @click.stop
        @contextmenu.prevent
      >
        <template v-for="(item, i) in items" :key="i">
          <div v-if="item.divider" class="sep"></div>
          <button
            v-else
            type="button"
            class="item"
            :class="{danger: item.danger, disabled: item.disabled}"
            role="menuitem"
            :disabled="item.disabled"
            @click="pick(i)"
          >
            <Icon v-if="item.icon" :name="item.icon" :size="15" class="item-icon" />
            <span class="item-label">{{ item.label }}</span>
          </button>
        </template>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.cp-menu {
  position: fixed;
  z-index: 1000;
  min-width: 140px;
  padding: 4px;
  background: var(--surface);
  border: 1px solid var(--stroke-card);
  border-radius: var(--radius-card); /* 与卡片/弹层圆角口径一致 */
  box-shadow: var(--shadow-pop);
}

.item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  height: 32px;
  padding: 0 10px;
  font-family: inherit;
  font-size: 0.857rem;
  color: var(--text);
  background: transparent;
  border: none;
  border-radius: 4px;
  text-align: left;
  transition: background var(--dur-fast) var(--ease);
}

.item:hover:not(:disabled) {
  background: color-mix(in srgb, var(--text) 8%, transparent);
}

.item:active:not(:disabled) {
  background: color-mix(in srgb, var(--text) 12%, transparent);
}

.item.danger {
  color: var(--err);
}

.item.disabled {
  opacity: 0.4;
}

.item-icon {
  color: var(--text2); /* danger 条目图标随文字红 */
}

.item.danger .item-icon {
  color: var(--err);
}

.sep {
  height: 1px;
  margin: 4px 8px;
  background: var(--divider);
}

.item-label {
  flex: 1;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
