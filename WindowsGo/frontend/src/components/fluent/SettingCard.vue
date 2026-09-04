<!--
  SettingCard.vue —— 设置行卡（qfw SettingCard 对应物）。
  骨架（components.css）：图标块 32 + 标题/说明两行 + 右侧插槽。
  默认插槽内容渲染到右操作区（放按钮/下拉/文本等）。
-->
<script setup lang="ts">
import Icon from './Icon.vue'

withDefaults(
  defineProps<{
    /** 前置图标（icons.ts 注册表 key） */
    icon?: string
    title?: string
    /** 说明文字（次级色，自动折行） */
    content?: string
  }>(),
  {icon: '', title: '', content: ''},
)

const emit = defineEmits<{click: []}>()
</script>

<template>
  <div class="set-card" @click="emit('click')">
    <span v-if="icon" class="set-icon">
      <Icon :name="icon" :size="17" />
    </span>
    <div class="set-body">
      <div class="set-title" v-if="title">{{ title }}</div>
      <div class="set-content" v-if="content">{{ content }}</div>
    </div>
    <div class="set-right">
      <slot />
    </div>
  </div>
</template>
