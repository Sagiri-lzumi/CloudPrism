<!--
  RecoveryCodeDlg.vue —— 恢复码一次性展示框（向导新建成功 / 密库页重新生成共用）。
  对照 Python RecoveryCodeDialog：Consolas 大字等宽分片展示 + 复制按钮 +
  「我已备份，进入」主钮。Esc/遮罩关闭 = 取消等待（不标记已备份）。
-->
<script setup lang="ts">
import {watch} from 'vue'
import Button from '../../components/fluent/Button.vue'
import PrimaryButton from '../../components/fluent/PrimaryButton.vue'
import Icon from '../../components/fluent/Icon.vue'

const props = defineProps<{
  open: boolean
  code: string
}>()

const emit = defineEmits<{close: []}>()

// 恢复码展示：后端返回 16 字符原生码（无分隔符），按 4 段分片空格展示；
// 若已带连字符（防御性兼容）则原样展示。复制恒为原生内容。
const segments = () => {
  const c = props.code.trim().toUpperCase()
  return c.includes('-') ? c.split('-') : (c.match(/.{1,4}/g) ?? [])
}

let copied = false
watch(
  () => props.open,
  (v) => {
    if (v) copied = false
  },
)

function onCopy() {
  try {
    void navigator.clipboard.writeText(props.code.trim())
  } catch {
    /* 剪贴板不可用时退化为选中文本（用户手动 Ctrl+C） */
    const el = document.querySelector<HTMLElement>('.rc-code')
    const range = document.createRange()
    if (el) {
      range.selectNodeContents(el)
      const sel = window.getSelection()
      sel?.removeAllRanges()
      sel?.addRange(range)
    }
  }
  copied = true
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}
</script>

<template>
  <Teleport to="body">
    <Transition name="fade">
      <div v-if="open" class="rc-mask" @keydown="onKey">
        <div class="rc-panel" role="dialog" aria-label="恢复码">
          <div class="rc-head">
            <span class="rc-head-icon">
              <Icon name="save" :size="18" />
            </span>
            <div class="rc-title">保存恢复码</div>
          </div>

          <p class="rc-tip">
            请复制并妥善备份此恢复码（建议离线保存）。忘记主密码时可凭它开库；
            恢复码一旦丢失将无法找回。此码只显示这一次，不会写入云端。
          </p>

          <div class="rc-code" dir="ltr" aria-label="恢复码内容">
            <span v-for="(s, i) in segments()" :key="i" class="rc-seg">{{ s }}</span>
          </div>

          <p class="rc-note">重新生成后旧恢复码立即失效；恢复码不会上传到云端。</p>

          <div class="rc-actions">
            <Button icon="copy" @click="onCopy">{{ copied ? '已复制' : '复制恢复码' }}</Button>
            <PrimaryButton icon="completed" @click="emit('close')">我已备份，进入密库</PrimaryButton>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.rc-mask {
  position: fixed;
  inset: 0;
  z-index: 1700;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.45);
}

.rc-panel {
  width: min(480px, calc(100vw - 96px));
  padding: 20px;
  background: var(--surface);
  border-radius: var(--radius-card);
  box-shadow: var(--shadow-pop);
  outline: none;
}

.rc-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}

.rc-head-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 36px;
  height: 36px;
  color: var(--ok);
  background: color-mix(in srgb, var(--ok) 14%, transparent);
  border-radius: var(--radius-ctrl);
}

.rc-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--heading);
}

.rc-tip {
  margin: 0 0 12px;
  font-size: 0.857rem;
  line-height: 1.5;
  color: var(--text2);
}

.rc-code {
  display: flex;
  justify-content: center;
  gap: 14px;
  padding: 14px;
  margin-bottom: 10px;
  background: var(--bg-page);
  border: 1px dashed var(--stroke);
  border-radius: var(--radius-ctrl);
  user-select: all;
}

.rc-seg {
  font-family: Consolas, 'Cascadia Mono', monospace;
  font-size: 1.429rem; /* 20px：对照 Python Consolas 18pt 视觉权重 */
  font-weight: 600;
  letter-spacing: 2px;
  color: var(--heading);
}

.rc-note {
  margin: 0 0 16px;
  font-size: 0.786rem;
  line-height: 1.4;
  color: var(--muted);
}

.rc-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.fade-enter-active,
.fade-leave-active {
  transition: opacity var(--dur) var(--ease);
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
