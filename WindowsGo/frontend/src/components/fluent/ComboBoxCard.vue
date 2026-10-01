<!--
  ComboBoxCard.vue —— 下拉选择设置行（qfw ComboBoxSettingCard 对应物）。
  选项是字符串数组，选中索引双向绑定（Go 端设置项一律存索引，
  与 Python settings_store 的 index 语义一致）。点击当前值弹 RoundMenu。
-->
<script setup lang="ts">
import {computed, ref} from 'vue'
import Icon from './Icon.vue'
import RoundMenu from './RoundMenu.vue'

const props = withDefaults(
  defineProps<{
    icon?: string
    title?: string
    content?: string
    options: string[]
    /** 当前选中索引 */
    modelValue?: number
    disabled?: boolean
  }>(),
  {icon: '', title: '', content: '', modelValue: 0, disabled: false},
)

const emit = defineEmits<{
  'update:modelValue': [v: number]
  /** 选择新项（索引已变），用于立即落盘 */
  change: [v: number]
}>()

const btn = ref<HTMLElement>()
const open = ref(false)

const current = computed(() =>
  props.modelValue >= 0 && props.modelValue < props.options.length
    ? props.options[props.modelValue]
    : '',
)

// RoundMenu 需要 {label,icon,...} 对象数组
const items = computed(() => props.options.map((label) => ({label})))

function pick(i: number) {
  if (i === props.modelValue || props.disabled) return
  emit('update:modelValue', i)
  emit('change', i)
}

// 触发钮与 RoundMenu 的「点外部关闭」存在事件顺序冲突：菜单打开时点按钮，
// window capture 阶段的 pointerdown 先把菜单关了，随后的 click 又把它打开
// —— 按钮永远收不起菜单。记录收起时刻，同一手势（pointerdown→click 通常
// <100ms）内的 click 不再重开。
let closedAt = 0
function onMenuClose() {
  closedAt = Date.now()
  open.value = false
}
function onBtnClick() {
  if (open.value) {
    open.value = false
    return
  }
  if (Date.now() - closedAt < 150) return // 刚被 pointerdown 收起的同一次点击
  open.value = true
}
</script>

<template>
  <div class="set-card">
    <span v-if="icon" class="set-icon">
      <Icon :name="icon" :size="17" />
    </span>
    <div class="set-body">
      <div class="set-title" v-if="title">{{ title }}</div>
      <div class="set-content" v-if="content">{{ content }}</div>
    </div>
    <div class="set-right">
      <button
        ref="btn"
        type="button"
        class="set-combo"
        :disabled="disabled"
        @click="onBtnClick"
      >
        <span class="combo-text">{{ current }}</span>
        <span class="combo-chev" aria-hidden="true">
          <svg class="combo-chev-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <path d="m6 9 6 6 6-6" />
          </svg>
        </span>
      </button>
    </div>
    <RoundMenu :open="open" :anchor="btn ?? null" :items="items" @select="pick" @close="onMenuClose" />
  </div>
</template>

<style scoped>
/* 下拉钮：line-height:1 防行高影响子元素基线 */
.set-combo {
  line-height: 1;
}

/* 倒三角容器：固定 16px flex 容器，强制 svg 几何居中。
   v27 起 chevron 用内联 svg（绕开 Icon 组件 normalize/双源机制，
   浏览器真实渲染下 100% 可控）。*/
.combo-chev {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 16px;
  height: 16px;
  overflow: visible;
}

.combo-chev-svg {
  width: 100%;
  height: 100%;
  overflow: visible;
}

.combo-text {
  max-width: 180px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
