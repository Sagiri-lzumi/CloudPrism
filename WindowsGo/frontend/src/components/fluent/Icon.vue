<!--
  Icon.vue —— 图标组件（双源渲染）。
  · Fluent 源（fill 型，2048/16 坐标系 svg）：CSS :deep fill: currentColor
    覆盖图形内 fill="#000"，随主题/文字色着色；
  · Lucide 源（stroke 型，24 坐标系 svg，命中 STROKE_NAMES）：:deep
    fill:none + stroke: currentColor + 2px 圆角线帽。
  内联方式：保留 svg 原文嵌套进宿主 svg（内层自带 viewBox 负责坐标系），
  仅剥掉 width/height 属性让内层撑满宿主；图形体由 CSS 统一上色。
-->
<script setup lang="ts">
import {computed} from 'vue'
import {ICONS, STROKE_NAMES} from '../../lib/icons'

const props = withDefaults(
  defineProps<{
    /** 图标名（icons.ts 注册表 key） */
    name: string
    /** 边长（px），默认 16 */
    size?: number
  }>(),
  {size: 16},
)

// 命中 Lucide 线性源 → stroke 渲染；否则 Fluent fill
const isStroke = computed(() => STROKE_NAMES.has(props.name))

// 剥掉 license 注释与 svg 标签上的固定 width/height/style（保留 viewBox），
// 使内层 svg 自适应宿主尺寸并按其坐标系等比缩放
function normalize(svg: string): string {
  return svg
    .replace(/<!--[\s\S]*?-->/g, '')
    .replace(/(<svg[^>]*?)\s(width|height)="[^"]*"/g, '$1')
    .replace(/(<svg[^>]*?)\sstyle="[^"]*"/g, '$1')
    .trim()
}

// 未知图标名显示问号（question），保证布局不塌、错误可见
const inner = computed(() => normalize(ICONS[props.name] ?? ICONS.question ?? ''))
</script>

<template>
  <span
    class="fluent-icon"
    :style="{width: size + 'px', height: size + 'px'}"
    role="img"
    aria-hidden="true"
  >
    <!-- 宿主 svg：不设 viewBox，交由内层 svg 自身的 viewBox 缩放；
         宽高由 class 撑满外层 span -->
    <svg
      class="icon-svg"
      :class="isStroke ? 'stroke' : 'fill'"
      v-html="inner"
    ></svg>
  </span>
</template>

<style scoped>
.fluent-icon {
  display: inline-flex;
  flex: none; /* 不随 flex 容器伸缩，防图标被拉扁 */
  align-items: center;
  justify-content: center;
}

/* 宿主与内层 svg 均撑满，内层按自身 viewBox 等比缩放 */
.fluent-icon svg,
.icon-svg,
.icon-svg svg {
  width: 100%;
  height: 100%;
}

/* Fluent fill 源：图形内 fill 属性被 CSS 覆盖为 currentColor（author style
   优先级高于 presentation attribute），随文字/主题色着色 */
.icon-svg.fill :deep(path) {
  fill: currentColor;
}

/* Lucide stroke 源：镂空描边 + 继承文字色 */
.icon-svg.stroke :deep(path),
.icon-svg.stroke :deep(circle),
.icon-svg.stroke :deep(rect),
.icon-svg.stroke :deep(line),
.icon-svg.stroke :deep(polyline),
.icon-svg.stroke :deep(polygon) {
  fill: none;
  stroke: currentColor;
  stroke-width: 2;
  stroke-linecap: round;
  stroke-linejoin: round;
}
</style>
