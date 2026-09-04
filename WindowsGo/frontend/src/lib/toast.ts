// toast.ts —— 通知条队列与静态 API（InfoBar.vue 渲染 host 用）。
//
// 独立于组件文件存放：视图/状态层 import showXxx 不需要经过 .vue 模块
// 解析。队列是模块级响应式数组，InfoBar host 组件常驻渲染右上角堆叠。
// 行为：滑入 180ms、自动消失（info/success 3s，warning/error 5s）、
// 可手动关闭；同文案 1s 内去重（连续失败通知不刷屏）。

import {reactive} from 'vue'

export type ToastLevel = 'info' | 'success' | 'warning' | 'error'

export interface ToastItem {
  id: number
  level: ToastLevel
  message: string
}

let seq = 0
let lastKey = ''
let lastAt = 0

/** 全局通知列表（响应式，InfoBar host 渲染）。 */
export const toasts = reactive<ToastItem[]>([])

function push(level: ToastLevel, message: string, duration: number) {
  const now = Date.now()
  const key = level + '|' + message
  if (key === lastKey && now - lastAt < 1000) return
  lastKey = key
  lastAt = now
  const t: ToastItem = {id: ++seq, level, message}
  toasts.push(t)
  window.setTimeout(() => dismiss(t.id), duration)
}

/** 手动关闭（关闭钮 / 超时）。 */
export function dismiss(id: number) {
  const i = toasts.findIndex((t) => t.id === id)
  if (i >= 0) toasts.splice(i, 1)
}

export function showInfo(message: string) { push('info', message, 3000) }
export function showSuccess(message: string) { push('success', message, 3000) }
export function showWarning(message: string) { push('warning', message, 5000) }
export function showError(message: string) { push('error', message, 5000) }
