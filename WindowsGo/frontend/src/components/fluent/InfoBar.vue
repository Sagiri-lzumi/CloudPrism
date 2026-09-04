<!--
  InfoBar.vue —— 通知条渲染 host（qfw InfoBar 对应物：右上角堆叠滑入）。
  队列与静态 API 在 lib/toast.ts（showInfo/showSuccess/showWarning/showError）；
  本组件常驻挂在 App.vue，把队列渲染为右上角浮层。滑入 180ms；
  图标与主题色语义：info→accent、success→ok、warning→warn、error→err。
-->
<script setup lang="ts">
import {dismiss, toasts} from '../../lib/toast'
import Icon from './Icon.vue'

const iconOf = {info: 'info', success: 'completed', warning: 'feedback', error: 'cancel'} as const
</script>

<template>
  <Teleport to="body">
    <div class="info-host">
      <TransitionGroup name="toast">
        <div
          v-for="t in toasts"
          :key="t.id"
          class="cp-info"
          :class="'lv-' + t.level"
          role="status"
        >
          <Icon :name="iconOf[t.level]" :size="16" class="lv-icon" />
          <span class="msg">{{ t.message }}</span>
          <button type="button" class="x" title="关闭" @click="dismiss(t.id)">
            <Icon name="cancel" :size="12" />
          </button>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>

<style scoped>
.info-host {
  position: fixed;
  top: 12px;
  right: 12px;
  z-index: 2000;
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 8px;
  pointer-events: none; /* 空区不挡下层点击 */
}

.cp-info {
  display: flex;
  align-items: center;
  gap: 8px;
  max-width: 420px;
  min-height: 40px;
  padding: 8px 8px 8px 12px;
  font-size: 0.857rem;
  color: var(--text);
  background: var(--surface);
  border: 1px solid var(--stroke-card);
  border-radius: 6px;
  box-shadow: var(--shadow-pop);
  pointer-events: auto;
}

.lv-icon {
  flex: none;
  color: var(--accent);
}

.lv-success .lv-icon {
  color: var(--ok);
}

.lv-warning .lv-icon {
  color: var(--warn);
}

.lv-error .lv-icon {
  color: var(--err);
}

.msg {
  line-height: 1.35;
  overflow-wrap: anywhere;
}

.x {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 24px;
  height: 24px;
  color: var(--text2);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
}

.x:hover {
  background: color-mix(in srgb, var(--text) 8%, transparent);
}

/* 滑入：从右侧 120% 平移进入（qfw InfoBar push 动效） */
.toast-enter-active {
  transition: opacity var(--dur) var(--ease), transform var(--dur) var(--ease);
}

.toast-enter-from {
  opacity: 0;
  transform: translateX(120%);
}

.toast-leave-active {
  transition: opacity calc(var(--dur) / 2) var(--ease);
}

.toast-leave-to {
  opacity: 0;
}
</style>
