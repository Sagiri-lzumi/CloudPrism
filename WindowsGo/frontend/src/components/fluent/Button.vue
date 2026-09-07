<!--
  Button.vue —— v21 通用按钮。
  视觉对照 docs/ui-redesign/v21-mock.html：胶囊形（radius pill）、34px 高、
  描边 + hover 提亮、:active 微缩。iconOnly 模式为正方形圆钮。
  主操作走 PrimaryButton（accent 渐变）。
-->
<script setup lang="ts">
import Icon from './Icon.vue'

withDefaults(
  defineProps<{
    /** 前置图标（可选；icons.ts 注册表 key） */
    icon?: string
    disabled?: boolean
    /** 仅图标按钮（正方形圆钮） */
    iconOnly?: boolean
    /** 悬停提示 */
    title?: string
    /** 危险操作（红文字） */
    danger?: boolean
  }>(),
  {disabled: false, iconOnly: false, danger: false},
)

const emit = defineEmits<{click: [e: MouseEvent]}>()
</script>

<template>
  <button
    type="button"
    class="cp-btn"
    :class="{'icon-only': iconOnly, danger: danger}"
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
  gap: 7px;
  height: 34px;
  min-width: 34px;
  padding: 0 14px;
  font-family: inherit;
  font-size: 0.85rem;
  font-weight: 500;
  color: var(--text);
  background: var(--surface);
  border: 1px solid var(--stroke);
  border-radius: var(--radius-round);
  cursor: default;
  transition: background var(--dur-fast) var(--ease),
    border-color var(--dur-fast) var(--ease), transform var(--dur-fast) var(--ease);
}

.cp-btn:hover:not(:disabled) {
  background: var(--surface-hover);
  border-color: var(--accent-soft-2);
}

.cp-btn:active:not(:disabled) {
  transform: scale(0.97);
}

.cp-btn:disabled {
  opacity: 0.4;
}

.cp-btn.danger {
  color: var(--err);
  border-color: color-mix(in srgb, var(--err) 30%, transparent);
}

.cp-btn.danger:hover:not(:disabled) {
  background: color-mix(in srgb, var(--err) 8%, transparent);
}

.icon-only {
  width: 34px;
  padding: 0;
}

.label {
  white-space: nowrap;
}
</style>
