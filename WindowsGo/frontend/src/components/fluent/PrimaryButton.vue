<!--
  PrimaryButton.vue —— 主操作按钮（qfw PrimaryPushButton 对应物）。
  视觉：accent 品牌蓝底白字，hover 微提亮、pressed 压暗并缩放 0.98；
  与 Button 共用 32px/radius 4 尺寸体系，语义上只用于每个页面的主操作。
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
  gap: 7px;
  height: 34px;
  min-width: 34px;
  padding: 0 16px;
  font-family: inherit;
  font-size: 0.85rem;
  font-weight: 600;
  color: var(--text-on-accent);
  background: var(--accent-grad);
  border: none;
  border-radius: var(--radius-round);
  cursor: default;
  box-shadow: 0 2px 8px color-mix(in srgb, var(--accent) 28%, transparent);
  transition: filter var(--dur-fast) var(--ease), transform var(--dur-fast) var(--ease);
}

.cp-btn-primary:hover:not(:disabled) {
  filter: brightness(1.06);
}

.cp-btn-primary:active:not(:disabled) {
  transform: scale(0.97);
}

.cp-btn-primary:disabled {
  opacity: 0.4;
}

.icon-only {
  width: 34px;
  padding: 0;
}

.label {
  white-space: nowrap;
}
</style>
