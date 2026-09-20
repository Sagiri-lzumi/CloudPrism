<!--
  Button.vue —— 通用按钮（次级操作）。
  视觉：32px 高、radius 6、hover 半透明文字色 7%、pressed 12% 并轻微内缩；
  图标+文字弹性布局，disabled 透明度 40%。颜色全部走 theme.css token。
  danger：红字 + 淡红描边（批量删除等破坏性次级操作；FilesView 早已在传
  danger 属性但此处未定义 prop，属性静默 fallthrough 无任何效果——缺陷修复）。
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
    /** 危险次级操作：红字 + 淡红描边 */
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
  transition: background var(--dur-fast) var(--ease), color var(--dur-fast) var(--ease),
    transform var(--dur-fast) var(--ease);
}

.cp-btn:hover:not(:disabled) {
  background: color-mix(in srgb, var(--text) 7%, transparent);
}

/* 按压：底色加深 + 轻微内缩，给出"按下去了"的触感 */
.cp-btn:active:not(:disabled) {
  background: color-mix(in srgb, var(--text) 12%, transparent);
  transform: scale(.96);
}

.cp-btn:disabled {
  opacity: 0.4;
}

/* 危险次级操作：红字 + 淡红描边（与主按钮红填充拉开重量差：
   破坏性主操作才用纯红，次级警示用描边即可）。描边用 box-shadow 实现
   而非 border，避免改变按钮盒尺寸破坏 32px 高对齐。 */
.cp-btn.danger {
  color: var(--err);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--err) 45%, transparent);
}

.cp-btn.danger:hover:not(:disabled) {
  background: color-mix(in srgb, var(--err) 8%, transparent);
}

.cp-btn.danger:active:not(:disabled) {
  background: color-mix(in srgb, var(--err) 14%, transparent);
}

.icon-only {
  width: 32px;
  padding: 0;
}
</style>
