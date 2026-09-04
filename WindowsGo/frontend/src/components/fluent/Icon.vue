<!--
  Icon.vue —— Fluent 字体图标等价物：内联 SVG + currentColor 着色。
  与 Python 端用法对应：那里是 qfw 的 FluentIcon 枚举（SVG 资源），
  这里把同一批图形内联，颜色交给 CSS（fill: currentColor）——
  明暗主题、hover/disabled 状态无需切换图形文件。
-->
<script setup lang="ts">
import {computed} from 'vue'
import {ICONS} from '../../lib/icons'

const props = withDefaults(
  defineProps<{
    /** 图标名（icons.ts 注册表 key，等价 qfw FluentIcon 枚举名小写） */
    name: string
    /** 边长（px），默认 16（qfw 默认图标尺寸） */
    size?: number
  }>(),
  {size: 16},
)

// 未知图标名显示问号（question），保证布局不塌、错误可见
const svg = computed(() => ICONS[props.name] ?? ICONS.question ?? '')
</script>

<template>
  <span
    class="fluent-icon"
    :style="{width: size + 'px', height: size + 'px'}"
    role="img"
    aria-hidden="true"
  >
    <!-- v-html 注入 SVG 原文；fill 由 .icon-svg 的 CSS 覆盖（presentation
         attribute 优先级低于样式表规则），随 currentColor 着色 -->
    <svg class="icon-svg" viewBox="0 0 16 16" v-html="svg"></svg>
  </span>
</template>

<style scoped>
.fluent-icon {
  display: inline-flex;
  flex: none; /* 不随 flex 容器伸缩，防 16px 图标被拉扁 */
  align-items: center;
  justify-content: center;
}

.icon-svg {
  width: 100%;
  height: 100%;
  fill: currentColor; /* 覆盖 SVG 内 fill="#000000"，继承文字色 */
}
</style>
