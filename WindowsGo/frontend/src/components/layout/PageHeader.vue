<!--
  PageHeader.vue —— 统一页面顶栏（页标题 / 面包屑 + 页面级动作）。

  骨架约定（v1.3 起各页共用，替代此前「只有设置页有标题、动作散落在
  工具行/右上角/多选条三处」的混乱）：
  · v1.01 起页头是**浮层**：绝对定位横跨内容顶缘，z = --z-head，
    内容滚动区从 y=0 起、以 `padding-top: var(--page-head-h)` 让位 ——
    滚动内容从玻璃页头底下穿过（scroll-under，macOS 窗口语义）。
    调用方视图根必须 position:relative；高度真源 --page-head-h；
  · 左区回答「我在哪」——文字标题（`title`，可跟一枚轻量补充 `sub`，
    如「3 个进行中」）或更丰富的内容（默认插槽，如文件页的可点面包屑），
    二者取其一；
  · 右区回答「能做什么」——页面级动作一律走 #actions 插槽，
    不再允许动作出现在页面其它角落；
  · 页面级动作超过 3 个时，收敛为「主操作 + 一个「⋯」溢出菜单」，
    不要一排无文字图标钮。

  状态切换（例如文件页选中条目后把左区换成批量摘要）由调用方自行用
  v-if 表达——顶栏本身不预设任何页面的状态机。
-->
<script setup lang="ts">
import Icon from '../fluent/Icon.vue'

withDefaults(
  defineProps<{
    /** 页标题（默认插槽存在时忽略） */
    title?: string
    /** 标题前置图标（可选；icons.ts 注册表 key） */
    icon?: string
    /** 标题后的轻量补充（计数/状态等；留空则不渲染） */
    sub?: string
  }>(),
  {title: '', icon: '', sub: ''},
)
</script>

<template>
  <header class="page-head">
    <div class="ph-lead">
      <!-- 默认插槽优先：文件页塞的是面包屑，其余页用 title 即可 -->
      <slot>
        <span v-if="icon" class="ph-icon"><Icon :name="icon" :size="17" /></span>
        <h1 class="ph-title">{{ title }}</h1>
        <span v-if="sub" class="ph-sub">{{ sub }}</span>
      </slot>
    </div>
    <div class="ph-actions">
      <slot name="actions" />
    </div>
  </header>
</template>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  gap: 12px;
  flex: none;
  min-height: var(--page-head-h);
  padding: 0 14px;
  /* v1.01 浮层页头：绝对定位横在内容顶缘（调用方视图根须 position:relative），
     内容滚动区从 y=0 起、自带 padding-top 让位 —— 滚动内容从页头底下穿过，
     chrome 档玻璃压的是「滚过来的真实内容 + 环境光」，毛玻璃在这里真正显形。
     inset 高光把窗口顶边衬出来，否则玻璃面看起来就是块贴在顶上的灰板。
     层级 --z-head：高于 ≤640px 预览浮层（页头必须可点），低于遮罩/菜单/模态。 */
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  z-index: var(--z-head);
  background: var(--glass-chrome);
  backdrop-filter: blur(var(--glass-blur)) saturate(var(--glass-sat));
  box-shadow: inset 0 1px 0 var(--glass-edge);
  border-bottom: 1px solid var(--divider);
}

.ph-lead {
  display: flex;
  align-items: center;
  gap: 9px;
  flex: 1;
  min-width: 0;
}

/* 标题图标块：与 Settings 页的卡片图标同一套「淡品牌底 + 品牌色图形」 */
.ph-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 28px;
  height: 28px;
  color: var(--accent);
  background: var(--accent-soft);
  border-radius: var(--radius-ctrl);
}

.ph-title {
  margin: 0;
  font-size: 1.007rem; /* 14.1px：比正文大一档，不喧宾夺主 */
  font-weight: 650;
  color: var(--heading);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* 标题后的补充信息（计数等）：灰底胶囊、小一号，不参与标题的省略号收缩。
   原先是 components.css 里的全局 .count，随大标题模式一并收进这里。 */
.ph-sub {
  flex: none;
  padding: 1px 8px;
  font-size: 0.786rem;
  color: var(--text2);
  background: color-mix(in srgb, var(--text) 7%, transparent);
  border-radius: var(--radius-round);
  vertical-align: 1px;
  white-space: nowrap;
}

.ph-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: none;
}

/* 窄屏：动作区可横向滑动，标题先让位（宁可滑，不要挤成两行） */
@media (max-width: 640px) {
  .page-head {
    gap: 8px;
    padding: 0 10px;
  }

  .ph-actions {
    overflow-x: auto;
    scrollbar-width: none;
  }

  .ph-actions::-webkit-scrollbar {
    display: none;
  }
}
</style>
