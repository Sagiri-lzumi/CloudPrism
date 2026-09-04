<!--
  InfoBar.vue —— 通知条（qfw InfoBar 对应物：右上角堆叠滑入）。
  模块级静态 API：任意视图 import {showInfo/showSuccess/showWarning/showError}
  即可弹条，无需在模板里挂组件（组件实例由下方 host 常驻渲染）。
  行为：滑入 180ms、自动消失（info/success 3s，warning/error 5s）、可手动关闭。
  同文案去重（1s 内相同消息不重复弹）。
-->
<script lang="ts">
// —— 模块级通知队列（普通 script 块导出，供组件外直接调用）——
import {reactive} from 'vue'

export type InfoLevel = 'info' | 'success' | 'warning' | 'error'

export interface ToastItem {
  id: number
  level: InfoLevel
  message: string
}

let seq = 0
let lastKey = ''
let lastAt = 0

/** 全局通知列表（响应式，host 组件渲染） */
export const toasts = reactive<ToastItem[]>([])

function push(level: InfoLevel, message: string, duration: number) {
  const now = Date.now()
  const key = level + '|' + message
  // 同文案 1s 内去重：连续失败通知（如逐文件重试）不刷屏
  if (key === lastKey && now - lastAt < 1000) return
  lastKey = key
  lastAt = now
  const t: ToastItem = {id: ++seq, level, message}
  toasts.push(t)
  window.setTimeout(() => dismiss(t.id), duration)
}

/** 手动关闭（由关闭钮 / 超时调用） */
export function dismiss(id: number) {
  const i = toasts.findIndex((t) => t.id === id)
  if (i >= 0) toasts.splice(i, 1)
}

export function showInfo(message: string) { push('info', message, 3000) }
export function showSuccess(message: string) { push('success', message, 3000) }
export function showWarning(message: string) { push('warning', message, 5000) }
export function showError(message: string) { push('error', message, 5000) }
</script>

<script setup lang="ts">
// —— host：常驻渲染右上角堆叠（普通 script 与 setup 同模块作用域，
// toasts/dismiss 直接可用，无需自 import）——
import Icon from './Icon.vue'
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
          <Icon
            :name="{info: 'info', success: 'completed', warning: 'feedback', error: 'cancel'}[t.level]"
            :size="16"
            class="lv-icon"
          />
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
