<!--
  Switch.vue —— 开关（qfw SwitchButton 对应物）。
  视觉：轨道 40×20、圆钮 12px 白点，开启态轨道 accent；
  关闭态轨道黑/白 20% 底。滑动动画 120ms。
-->
<script setup lang="ts">
const props = withDefaults(
  defineProps<{
    modelValue?: boolean
    disabled?: boolean
  }>(),
  {modelValue: false, disabled: false},
)

const emit = defineEmits<{
  'update:modelValue': [v: boolean]
}>()

function toggle() {
  if (props.disabled) return
  emit('update:modelValue', !props.modelValue)
}
</script>

<template>
  <button
    type="button"
    role="switch"
    class="cp-switch"
    :class="{on: modelValue, disabled}"
    :aria-checked="modelValue"
    @click="toggle"
  >
    <span class="knob"></span>
  </button>
</template>

<style scoped>
.cp-switch {
  position: relative;
  display: inline-block;
  flex: none;
  width: 40px;
  height: 20px;
  padding: 0;
  background: color-mix(in srgb, var(--text) 20%, transparent);
  border: none;
  border-radius: var(--radius-round);
  cursor: default;
  transition: background var(--dur-fast) var(--ease);
}

.cp-switch.on {
  background: var(--accent);
}

.cp-switch:active .knob {
  width: 16px; /* 按压时圆钮略拉长（qfw 按压动效的近似） */
}

.knob {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 16px;
  height: 16px;
  background: #fff;
  border-radius: var(--radius-round);
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.25);
  transition: left var(--dur-fast) var(--ease), width var(--dur-fast) var(--ease);
}

.cp-switch.on .knob {
  left: 22px;
}

.cp-switch.disabled {
  opacity: 0.4;
}
</style>
