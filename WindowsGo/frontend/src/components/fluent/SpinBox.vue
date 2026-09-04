<!--
  SpinBox.vue —— 数值步进器（qfw SpinBox 对应物，含单位后缀）。
  视觉：LineEdit 同款 32px 容器，右侧上下步进钮叠排；
  clamp 到 [min,max]；滚轮在容器上悬停时可步进。
-->
<script setup lang="ts">
import {ref} from 'vue'
import Icon from './Icon.vue'

const props = withDefaults(
  defineProps<{
    modelValue?: number
    min?: number
    max?: number
    step?: number
    /** 单位后缀（如 " MB"），只读展示 */
    suffix?: string
    disabled?: boolean
    /** 空输入框宽度（px），默认 64 容纳 3 位整数 */
    width?: number
  }>(),
  {modelValue: 0, min: 0, max: 100, step: 1, suffix: '', disabled: false, width: 64},
)

const emit = defineEmits<{
  'update:modelValue': [v: number]
  /** 值稳定后（步进/失焦/回车）通知，用于立即落盘 */
  change: [v: number]
}>()

const dirty = ref(String(props.modelValue))

// clamp 后上抛；返回值供 change 用
function commit(raw: number): number {
  const v = Math.min(props.max, Math.max(props.min, raw))
  dirty.value = String(v)
  emit('update:modelValue', v)
  emit('change', v)
  return v
}

function stepBy(delta: number) {
  if (props.disabled) return
  commit((props.modelValue ?? 0) + delta * props.step)
}

function onInput(e: Event) {
  dirty.value = (e.target as HTMLInputElement).value
}

// 失焦/回车时把文本按数值解析（空串回退到 modelValue）
function normalize() {
  const n = Number(dirty.value)
  commit(Number.isFinite(n) ? n : props.modelValue)
}

function onWheel(e: WheelEvent) {
  if (props.disabled) return
  e.preventDefault() // 防页面滚动（设置页滚动容器）
  stepBy(e.deltaY < 0 ? 1 : -1)
}

defineExpose({commit})
</script>

<template>
  <div class="cp-spinbox" :class="{disabled}" :style="{width: width + 'px'}" @wheel="onWheel">
    <input
      class="field"
      :value="dirty"
      :disabled="disabled"
      inputmode="numeric"
      @input="onInput"
      @change="normalize"
      @keydown.enter.prevent="normalize"
      @blur="normalize"
    />
    <span v-if="suffix" class="suffix">{{ suffix }}</span>
    <span class="steppers">
      <button type="button" tabindex="-1" :disabled="disabled" @click="stepBy(1)">
        <Icon name="care_up_solid" :size="8" />
      </button>
      <button type="button" tabindex="-1" :disabled="disabled" @click="stepBy(-1)">
        <Icon name="care_down_solid" :size="8" />
      </button>
    </span>
  </div>
</template>

<style scoped>
.cp-spinbox {
  position: relative;
  display: inline-flex;
  align-items: center;
  height: 32px;
  background: var(--surface);
  border: 1px solid var(--stroke);
  border-radius: var(--radius-ctrl);
  transition: border-color var(--dur-fast) var(--ease);
}

.cp-spinbox:focus-within {
  border-color: var(--accent);
}

.field {
  flex: 1;
  min-width: 0;
  height: 100%;
  padding: 0 8px 0 10px;
  font-family: inherit;
  font-size: 0.857rem;
  color: var(--text);
  background: transparent;
  border: none;
  outline: none;
}

.cp-spinbox.disabled {
  opacity: 0.4;
}

.suffix {
  flex: none;
  padding-right: 2px;
  font-size: 0.857rem;
  color: var(--text2);
}

.steppers {
  display: flex;
  flex-direction: column;
  flex: none;
  width: 18px;
  height: 100%;
  border-left: 1px solid var(--stroke);
}

.steppers button {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: center;
  padding: 0;
  color: var(--text2);
  background: transparent;
  border: none;
}

.steppers button:hover:not(:disabled) {
  color: var(--text);
  background: color-mix(in srgb, var(--text) 8%, transparent);
}

.steppers button:disabled {
  opacity: 0.4;
}
</style>
