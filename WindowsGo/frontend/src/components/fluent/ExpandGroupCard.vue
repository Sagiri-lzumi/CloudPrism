<!--
  ExpandGroupCard.vue —— 可展开设置组（qfw ExpandGroupSettingCard 对应物）。
  头部卡点击折叠/展开，箭头旋转 180°；展开区为独立圆角卡列表
  （components.css .expand-items），子项用设置卡类铺排（如传输/安全分组）。
-->
<script setup lang="ts">
import {ref} from 'vue'
import Icon from './Icon.vue'

const props = withDefaults(
  defineProps<{
    icon?: string
    title?: string
    content?: string
    /** 默认展开 */
    defaultOpen?: boolean
  }>(),
  {icon: '', title: '', content: '', defaultOpen: false},
)

const open = ref(props.defaultOpen)
</script>

<template>
  <div class="expand-group" :class="{open}">
    <div class="set-card clickable" @click="open = !open">
      <span v-if="icon" class="set-icon">
        <Icon :name="icon" :size="17" />
      </span>
      <div class="set-body">
        <div class="set-title" v-if="title">{{ title }}</div>
        <div class="set-content" v-if="content">{{ content }}</div>
      </div>
      <div class="set-right">
        <span class="expand-arrow">
          <Icon name="chevron_down_med" :size="14" />
        </span>
      </div>
    </div>
    <Transition name="pop">
      <div v-if="open" class="expand-items">
        <slot />
      </div>
    </Transition>
  </div>
</template>
