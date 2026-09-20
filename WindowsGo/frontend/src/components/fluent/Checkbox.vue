<!--
  Checkbox.vue —— 自绘复选框（苹果风：勾选态 accent 底 + 白勾）。
  为什么自绘：原生 <input type="checkbox"> 无法随明暗主题 token 换肤，
  浏览器默认样式在深色档观感突兀；此前 VaultsView/InitWizard 里的裸
  checkbox 正是这个来源。此处保留真实 input（键盘可达、表单语义完整），
  仅视觉隐藏后以 token 化的 .box 呈现，label 点击天然转发到 input。
-->
<script setup lang="ts">
import Icon from './Icon.vue'

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

// 受控模式：input 的 checked 只由 modelValue 驱动，change 仅上抛
function toggle() {
  if (props.disabled) return
  emit('update:modelValue', !props.modelValue)
}
</script>

<template>
  <label class="cp-chk" :class="{disabled}">
    <input
      type="checkbox"
      :checked="modelValue"
      :disabled="disabled"
      @change="toggle"
    />
    <span class="box" aria-hidden="true">
      <Icon v-if="modelValue" name="check" :size="12" />
    </span>
    <span v-if="$slots.default" class="txt"><slot /></span>
  </label>
</template>

<style scoped>
.cp-chk {
  position: relative; /* input 视觉隐藏后仍锚定在行内 */
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: default; /* 桌面应用语义：与按钮一致不用手型 */
}

/* 真实 input 仅承担交互/可达性：隐藏视觉但保留焦点能力（配 :focus-visible） */
.cp-chk input {
  position: absolute;
  width: 18px;
  height: 18px;
  margin: 0;
  opacity: 0;
}

.cp-chk .box {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 18px;
  height: 18px;
  color: var(--text-on-accent); /* 勾选后白勾 */
  background: var(--surface);
  border: 1px solid var(--stroke);
  border-radius: var(--radius-ctrl);
  transition: background var(--dur-fast) var(--ease),
    border-color var(--dur-fast) var(--ease);
}

/* 勾选态：accent 纯色底（描边让位，与主按钮同一强调语言） */
.cp-chk input:checked + .box {
  background: var(--accent);
  border-color: transparent;
}

.cp-chk input:focus-visible + .box {
  box-shadow: 0 0 0 3px var(--accent-ring);
}

.txt {
  font-size: 0.857rem;
  line-height: 1.4;
  color: var(--text);
}

.cp-chk.disabled {
  opacity: 0.5;
}
</style>
