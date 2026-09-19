<!--
  PrimaryButton.vue —— 主操作按钮。
  视觉：品牌色渐变底 + 白字 + 品牌色投影（唯一"浮"起来的控件，用于每页主操作，
  与次级按钮的扁平观感拉开层级）；按压轻微内缩。尺寸与 Button 同体系（32px）。
-->
<script setup lang="ts">
import Icon from './Icon.vue'

withDefaults(
  defineProps<{
    icon?: string
    disabled?: boolean
    iconOnly?: boolean
    title?: string
  }>(),
  {disabled: false, iconOnly: false},
)

const emit = defineEmits<{click: [e: MouseEvent]}>()
</script>

<template>
  <button
    type="button"
    class="cp-btn-primary"
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
  background: var(--accent-grad);
  border: none;
  border-radius: var(--radius-ctrl);
  cursor: default;
  /* 品牌色投影：主操作的"重量"来源 */
  box-shadow: 0 1px 2px rgba(16, 24, 40, .12), 0 6px 16px -6px var(--accent-ring);
  transition: filter var(--dur-fast) var(--ease), box-shadow var(--dur-fast) var(--ease),
    transform var(--dur-fast) var(--ease);
}

/* hover/active 用 filter 提亮/压暗：渐变底无法靠改 background 颜色实现，
   覆盖 filter 才能让整套渐变一起变化 */
.cp-btn-primary:hover:not(:disabled) {
  filter: brightness(1.07);
}

.cp-btn-primary:active:not(:disabled) {
  filter: brightness(.94);
  transform: scale(.96);
  box-shadow: 0 1px 2px rgba(16, 24, 40, .16);
}

.cp-btn-primary:disabled {
  opacity: .4;
  box-shadow: none;
}

.icon-only {
  width: 32px;
  padding: 0;
}
</style>
