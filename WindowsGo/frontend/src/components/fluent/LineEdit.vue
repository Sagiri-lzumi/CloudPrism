<!--
  LineEdit.vue —— 单行输入框（qfw LineEdit / PasswordLineEdit 对应物）。
  视觉：32px 高、radius 4、1px 描边（黑 8%/白 8%），聚焦换 accent 描边；
  password 模式带「显示/隐藏」眼睛钮，clearable 模式带清除钮。
  Enter 上抛 enter 事件供表单提交。
-->
<script setup lang="ts">
import {computed, ref} from 'vue'
import Icon from './Icon.vue'

const props = withDefaults(
  defineProps<{
    modelValue?: string
    placeholder?: string
    /** 密码掩码（内置显示/隐藏切换钮） */
    password?: boolean
    /** 可一键清除（显示 × 钮） */
    clearable?: boolean
    disabled?: boolean
    /** 初始焦点自动选中（用于主密码输入等高频场景） */
    autofocus?: boolean
  }>(),
  {modelValue: '', placeholder: '', password: false, clearable: false, disabled: false, autofocus: false},
)

const emit = defineEmits<{
  'update:modelValue': [v: string]
  enter: []
}>()

const input = ref<HTMLInputElement>()
const reveal = ref(false) // 密码可见态
const type = computed(() => (props.password && !reveal.value ? 'password' : 'text'))

function onInput(e: Event) {
  emit('update:modelValue', (e.target as HTMLInputElement).value)
}

function clear() {
  emit('update:modelValue', '')
  input.value?.focus()
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter') emit('enter')
}

defineExpose({focus: () => input.value?.focus(), select: () => input.value?.select()})
</script>

<template>
  <div class="cp-line-edit" :class="{disabled}">
    <input
      ref="input"
      class="field"
      :type="type"
      :value="modelValue"
      :placeholder="placeholder"
      :disabled="disabled"
      :autofocus="autofocus"
      spellcheck="false"
      autocomplete="off"
      @input="onInput"
      @keydown="onKey"
    />
    <button
      v-if="password"
      type="button"
      class="suffix-btn"
      :title="reveal ? '隐藏密码' : '显示密码'"
      tabindex="-1"
      @click="reveal = !reveal"
    >
      <Icon :name="reveal ? 'hide' : 'view'" :size="14" />
    </button>
    <button
      v-else-if="clearable && modelValue"
      type="button"
      class="suffix-btn"
      title="清除"
      tabindex="-1"
      @click="clear"
    >
      <Icon name="cancel" :size="12" />
    </button>
  </div>
</template>

<style scoped>
.cp-line-edit {
  display: inline-flex;
  align-items: center;
  height: 32px;
  min-width: 0;
  background: var(--surface);
  border: 1px solid var(--stroke);
  border-radius: var(--radius-ctrl);
  transition: border-color var(--dur-fast) var(--ease);
}

/* 容器聚焦态（:focus-within 覆盖内层 input 拿不到焦点的结构） */
.cp-line-edit:focus-within {
  border-color: var(--accent);
}

.field {
  flex: 1;
  min-width: 0; /* 防内容撑破 flex */
  height: 100%;
  padding: 0 10px;
  font-family: inherit;
  font-size: 0.857rem;
  color: var(--text);
  background: transparent;
  border: none;
  outline: none;
}

.field::placeholder {
  color: var(--text2);
}

.cp-line-edit.disabled {
  opacity: 0.4;
}

.suffix-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 28px;
  height: 28px;
  margin-right: 2px;
  color: var(--muted);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  transition: background var(--dur-fast) var(--ease);
}

.suffix-btn:hover {
  background: color-mix(in srgb, var(--text) 8%, transparent);
}
</style>
