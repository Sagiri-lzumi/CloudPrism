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
    /** 锚点元素（getBoundingClientRect 定位） */
    anchor: HTMLElement | null
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
  {anchor: null, gap: 4},
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
    if (!open || !props.anchor) return
    await nextTick()
    const a = props.anchor.getBoundingClientRect()
    const el = menu.value!
    // 用 offsetWidth/offsetHeight 测量：pop 入场动画带 scale(.96)，
    // getBoundingClientRect 会测到缩放后的偏小值，导致定位偏低/溢出
    const mw = el.offsetWidth
    const mh = el.offsetHeight
    // 默认锚点下方左对齐；空间不足翻到上方
    let top = a.bottom + props.gap
    if (top + mh > innerHeight) top = a.top - props.gap - mh
    // 双向钳位：无论锚点在何位置，菜单必须完整落在视口内（防底部被裁）
    top = Math.max(4, Math.min(top, innerHeight - mh - 4))
    const left = Math.max(4, Math.min(a.left, innerWidth - mw - 4))
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
    } else {
      window.removeEventListener('pointerdown', onDocDown, true)
      window.removeEventListener('keydown', onKey, true)
      window.removeEventListener('blur', onDocBlur)
    }
  },
)

function onDocBlur() {
  emit('close') // 窗口失焦（如切系统任务）即收起菜单
}

onBeforeUnmount(() => {
  window.removeEventListener('pointerdown', onDocDown, true)
  window.removeEventListener('keydown', onKey, true)
  window.removeEventListener('blur', onDocBlur)
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
  border-radius: 6px;
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
