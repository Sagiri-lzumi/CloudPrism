<!--
  Button.vue —— 通用按钮（qfw PushButton 对应物）。
  视觉：32px 高、radius 4、hover 半透明黑/白 8%、pressed 12%；
  图标+文字弹性布局，disabled 透明度 40%。颜色全部走 theme.css token。
-->
<script setup lang="ts">
import Icon from './Icon.vue'

withDefaults(
  defineProps<{
    /** 前置图标（可选；icons.ts 注册表 key） */
    icon?: string
    disabled?: boolean
    /** 仅图标按钮（去掉内边距，宽高等同高的正方形） */
    iconOnly?: boolean
    /** 悬停提示（原生 tooltip 足够，避免自绘弹层开销） */
    title?: string
  }>(),
  {disabled: false, iconOnly: false},
)

const emit = defineEmits<{click: [e: MouseEvent]}>()
</script>

<template>
  <button
    type="button"
    class="cp-btn"
    :class="{'icon-only': iconOnly}"
    :disabled="disabled"
    :title="title"
    @click="(e: MouseEvent) => emit('click', e)"
  >
    <Icon v-if="icon" :name="icon" :size="16" />
    <span v-if="$slots.default" class="label"><slot /></span>
  </button>
</template>

<style scoped>
.cp-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 32px;
  min-width: 32px;
  padding: 0 16px;
  font-family: inherit;
  font-size: 0.857rem; /* 12px：qfw PushButton 字号偏小一档 */
  font-weight: 400;
  color: var(--text);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  cursor: default; /* 桌面应用语义：不用手型 */
  transition: background var(--dur-fast) var(--ease);
}

.cp-btn:hover:not(:disabled) {
  background: color-mix(in srgb, var(--text) 8%, transparent);
}

.cp-btn:active:not(:disabled) {
  background: color-mix(in srgb, var(--text) 12%, transparent);
  transform: scale(0.98);
  transition: transform var(--dur-fast) var(--ease);
}

.cp-btn:disabled {
  opacity: 0.4;
}

.icon-only {
  width: 32px;
  padding: 0;
}
</style>
