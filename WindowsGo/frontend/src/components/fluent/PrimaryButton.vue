<!--
  PrimaryButton.vue —— 主操作按钮。
  视觉：品牌色纯色底 + 白字（macOS HIG 主按钮：纯填充、无渐变无辉光，
  靠色块本身的重量与次级按钮拉开层级）；hover 提亮、按压轻微内缩。
  danger：纯语义红填充（删除确认等破坏性主操作，与 --err 同源）。
  尺寸与 Button 同体系（32px）。
-->
<script setup lang="ts">
import Icon from './Icon.vue'

withDefaults(
  defineProps<{
    icon?: string
    disabled?: boolean
    iconOnly?: boolean
    title?: string
    /** 危险主操作：确认键转纯红填充（缺陷修复：此前删除确认键是普通蓝主钮） */
    danger?: boolean
  }>(),
  {disabled: false, iconOnly: false, danger: false},
)

const emit = defineEmits<{click: [e: MouseEvent]}>()
</script>

<template>
  <button
    type="button"
    class="cp-btn-primary"
    :class="{'icon-only': iconOnly, danger}"
    :disabled="disabled"
    :title="title"
    @click="(e: MouseEvent) => emit('click', e)"
  >
    <Icon v-if="icon" :name="icon" :size="16" />
    <span v-if="$slots.default" class="label"><slot /></span>
  </button>
</template>

<style scoped>
.cp-btn-primary {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  height: 32px;
  min-width: 32px;
  padding: 0 16px;
  font-family: inherit;
  font-size: 0.857rem;
  font-weight: 600;
  color: var(--text-on-accent);
  background: var(--accent);
  border: none;
  border-radius: var(--radius-ctrl);
  cursor: default;
  transition: filter var(--dur-fast) var(--ease), transform var(--dur-fast) var(--ease-spring);
}

/* hover/active 用 filter 提亮/压暗：纯色底沿用这套方案（渐变时代的遗产），
   好处是与 background 色值解耦，主题换色无需同步改这里。
   位移/缩放只走 transform，不参与排版。 */
.cp-btn-primary:hover:not(:disabled) {
  filter: brightness(1.07);
  transform: translateY(-1px);
}

.cp-btn-primary:active:not(:disabled) {
  filter: brightness(.94);
  transform: translateY(0) scale(.94);
}

.cp-btn-primary:disabled {
  opacity: .4;
}

/* 危险主操作：纯语义红底（与普通主钮同视觉重量，仅色相区分）。
   深浅两主题 --err 均可白字可读，无需变体。 */
.cp-btn-primary.danger {
  background: var(--err);
}

.icon-only {
  width: 32px;
  padding: 0;
}
</style>
