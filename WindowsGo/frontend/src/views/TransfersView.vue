<!--
  TransfersView.vue —— 传输页（任务列表 + 续传 + 重试/取消/清空）。
  对照 WindowsPy 传输任务卡/传输页：任务明细由 store.ui.tasks 随 10Hz
  状态帧更新（TaskView：方向/状态/进度/字节），终态任务保留到「清空」。
  续传横幅：连接时后端探测到上次退出遗留记录（snap.resumeCount>0），
  点「恢复」调 Vault.ResumePending 重建入队。
-->
<script setup lang="ts">
import {computed, reactive, ref} from 'vue'
import type {appstate} from '../types/appstate'
import {Transfer, Vault, unwrap} from '../lib/api'
import {ui, navigate, uploadFromFileList} from '../lib/store'
import {fmtSize, fmtPct} from '../lib/format'
import {showError, showInfo, showSuccess, showWarning} from '../lib/toast'
import Button from '../components/fluent/Button.vue'
import PrimaryButton from '../components/fluent/PrimaryButton.vue'
import Icon from '../components/fluent/Icon.vue'
import ProgressBar from '../components/fluent/ProgressBar.vue'
import MessageBox from '../components/fluent/MessageBox.vue'
import PageHeader from '../components/layout/PageHeader.vue'

const connected = computed(() => !!ui.snap?.connected)
const tasks = computed(() => ui.tasks)

// 任务数摘要：终态之外的计数（供头部/清空按钮态）
const unfinished = computed(() => tasks.value.filter((t) => !doneStates.has(t.state)).length)

// 页头补充位：空列表不显示；否则优先报在飞数量，只剩终态任务（等重试/等清空）
// 时退化为「N 个已结束」——不能写成「0 个进行中」那种自相矛盾的文案。
const subTitle = computed(() => {
  if (!tasks.value.length) return ''
  return unfinished.value ? `${unfinished.value} 个进行中` : `${tasks.value.length} 个已结束`
})

const doneStates = new Set(['done', 'failed', 'cancelled'])

/** 状态徽章文案/语义色（对照 Python transfer 行状态文本）。 */
const STATE_META: Record<string, {text: string; cls: string}> = {
  waiting: {text: '等待中', cls: 'idle'},
  running: {text: '传输中', cls: 'run'},
  done: {text: '已完成', cls: 'ok'},
  failed: {text: '失败', cls: 'err'},
  cancelled: {text: '已取消', cls: 'idle'},
}

const chip = (s: string) => STATE_META[s] ?? {text: s, cls: 'idle'}

/* ------------------------------------------------------------ 动作 */

const busy = ref(false) // 本地忙碌态（防连点）

/** 恢复上次未完成任务（锁库/退出打断的上传）。 */
async function resumePending() {
  if (busy.value) return
  busy.value = true
  try {
    const n = await Vault.ResumePending()
    if (n > 0) {
      showSuccess(`已恢复 ${n} 个未完成任务`)
    } else {
      showWarning('没有可恢复的任务（可能已被清除）')
    }
  } catch (e) {
    const err = unwrap(e)
    if (err.code === 'no-resume') showInfo('没有可恢复的任务')
    else showError('恢复失败：' + err.message)
  } finally {
    busy.value = false
  }
}

/** 取消全部未完成任务（等待+传输中；终态不受影响）。 */
async function cancelAll() {
  try {
    await Transfer.CancelAll()
    showInfo('已请求取消全部任务')
  } catch (e) {
    showError('取消失败：' + unwrap(e).message)
  }
}

/** 清空已结束任务（done/failed/cancelled 从列表移除）。 */
async function clearFinished() {
  try {
    await Transfer.ClearFinished()
    showInfo('已清空已结束任务')
  } catch (e) {
    showError('清空失败：' + unwrap(e).message)
  }
}

/** 重试单个失败/已取消任务。 */
async function retryOne(id: number) {
  try {
    await Transfer.Retry(id)
    showInfo('已重新入队')
  } catch (e) {
    showError('重试失败：' + unwrap(e).message)
  }
}

/** 选择本地文件上传（空态快捷入口，浏览器 file input）。 */
async function pickUpload() {
  const input = document.createElement('input')
  input.type = 'file'
  input.multiple = true
  input.onchange = () => {
    if (input.files?.length) void uploadFromFileList(input.files)
  }
  input.click()
}

/* ---------------------------------------------------- 清空确认模态 */

const dlg = reactive({open: false})
</script>

<template>
  <div class="t-view">
    <!-- 统一页头（56px）：左区「我在哪」+ 进行中计数，右区「能做什么」。
         此前这里是「46px 工具行（2 个无文字图标钮，用途只能靠悬停提示猜）
         + 内容区大标题」两层，动作与页题上下分居；现按 PageHeader 骨架收成
         一行，页面级动作一律只出现在这里。 -->
    <PageHeader title="传输任务" icon="sync" :sub="subTitle">
      <template #actions>
        <Button
          icon="cancel"
          :disabled="!tasks.length || !unfinished"
          title="取消所有等待中与传输中的任务"
          @click="cancelAll"
        >
          取消全部
        </Button>
        <Button
          icon="delete"
          danger
          :disabled="!tasks.length"
          title="从列表移除已结束的任务（不影响云端与本地文件）"
          @click="dlg.open = true"
        >
          清空已结束
        </Button>
      </template>
    </PageHeader>

    <!-- v1.01：单一滚动区从 y=0 起，内容从浮层页头底下穿过（macOS 语义：
         页头之下皆内容，续传横幅随内容滚走）。 -->
    <div class="t-scroll">
      <!-- 续传横幅：锁库/退出遗留任务提示 -->
      <Transition name="fade">
        <div v-if="connected && (ui.snap?.resumeCount ?? 0) > 0" class="resume-banner">
          <Icon name="history" :size="16" />
          <span class="rb-text">
            上次退出时有 {{ ui.snap!.resumeCount }} 个任务未完成，可立即恢复
          </span>
          <PrimaryButton icon="update" :disabled="busy" @click="resumePending">
            恢复上传
          </PrimaryButton>
        </div>
      </Transition>

      <!-- 任务列表：行键 = t.id —— 10Hz 帧只整组替换同键数组（patch 不重建），
           只有真实的任务增删才进出动画，帧循环不会让它反复重播。 -->
      <TransitionGroup v-if="tasks.length" tag="div" class="list" name="task">
        <div
          v-for="t in tasks"
          :key="t.id"
          class="t-row"
          :class="'st-' + t.state"
        >
          <Icon
            :name="t.direction === 'download' ? 'download' : 'send'"
            :size="18"
            class="dir-ic"
            :class="t.direction"
            :title="t.direction === 'download' ? '下载' : '上传'"
          />
          <div class="t-body">
            <div class="t-line1">
              <span class="t-name" :title="t.name">{{ t.name }}</span>
              <span class="t-meta">
                {{ fmtSize(t.doneBytes) }} / {{ fmtSize(t.totalBytes) }}
              </span>
            </div>
            <div class="t-line2">
              <ProgressBar
                :value="t.state === 'done' ? 100 : fmtPct(t.progress)"
                :indeterminate="t.state === 'running' && t.totalBytes <= 0"
                :color="t.state === 'done' ? 'ok' : t.state === 'failed' ? 'err' : 'accent'"
              />
              <span v-if="t.state === 'running'" class="t-pct">{{ fmtPct(t.progress) }}%</span>
            </div>
            <div v-if="t.state === 'failed' && t.errorMsg" class="t-err" :title="t.errorMsg">
              {{ t.errorMsg }}
            </div>
          </div>
          <div class="t-right">
            <span class="chip" :class="chip(t.state).cls">{{ chip(t.state).text }}</span>
            <Button
              v-if="t.state === 'failed' || t.state === 'cancelled'"
              iconOnly
              icon="update"
              title="重试"
              @click="retryOne(t.id)"
            />
          </div>
        </div>
      </TransitionGroup>

      <!-- 空态 -->
      <div v-else class="empty-state">
        <span class="plate"><Icon name="sync" :size="32" /></span>
        <p class="lead">暂无任务</p>
        <p class="sub">上传/下载任务会显示在这里，可重试失败项或在结束后清空列表。</p>
        <Button v-if="connected" icon="send" @click="pickUpload">上传文件…</Button>
        <Button v-else icon="certificate" @click="navigate('vaults')">前往连接</Button>
      </div>
    </div>

    <!-- 清空确认 -->
    <MessageBox
      :open="dlg.open"
      title="清空已结束任务"
      content="将从列表移除全部已完成/失败/已取消的任务（不影响云端与本地文件）。"
      danger
      :show-cancel="true"
      confirm-text="清空"
      @confirm="dlg.open = false; clearFinished()"
      @cancel="dlg.open = false"
    />
  </div>
</template>

<style scoped>
.t-view {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  /* 浮层页头的定位上下文（PageHeader 绝对定位在本视图顶缘） */
  position: relative;
}

/* 工具行与内容区大标题已在 v1.3 收敛为顶部的统一页头（PageHeader 组件），
   本页不再需要工具栏 / 页头的样式覆盖。 */

/* v1.01：页头之下皆内容 —— 横幅/列表/空态收进同一滚动区（从 y=0 起、
   padding-top 让位浮层页头）。此前 .t-view > .empty-state 覆写是为抵消
   flex 列里 height:100% 连 56px 页头一起算出的多余滚动条；空态挪进
   确定高度的滚动器后全局 .empty-state 自然成立，覆写随之删除。 */
.t-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: calc(var(--page-head-h) + 12px) 16px 16px;
}

/* 续传横幅（外距的「顶/左/右」移交滚动区 padding，只留下与列表的间距） */
.resume-banner {
  display: flex;
  align-items: center;
  gap: 10px;
  margin: 0 0 12px;
  padding: 10px 14px;
  color: var(--text);
  background: color-mix(in srgb, var(--warn) 12%, var(--glass-card));
  border: 1px solid color-mix(in srgb, var(--warn) 40%, transparent);
  border-radius: var(--radius-card);
}

.rb-text {
  flex: 1;
  font-size: 0.857rem;
}

/* 任务列表：纯布局（滚动职责已移交 .t-scroll）。position:relative 是
   离场行绝对定位（.task-leave-active）的包含块。 */
.list {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.t-row {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 12px 14px;
  background: var(--glass-card);
  border: 1px solid var(--stroke-card);
  border-radius: var(--radius-card);
  box-shadow: var(--shadow-card);
  transition: border-color var(--dur-fast) var(--ease);
}

/* 任务行组过渡：进入滑入 / 离场淡出（绝对定位脱离流，让 move 的 FLIP
   接管余行上移）、余行补位平滑。规则必须排在 .t-row 之后 —— 同特异度
   靠源序在进场期间压掉基础 border-color transition，进场结束类摘除后
   基础过渡恢复。 */
.task-enter-active {
  transition: opacity var(--dur) var(--ease),
    transform var(--dur-spring) var(--ease-spring-soft);
}

.task-enter-from {
  opacity: 0;
  transform: translateY(-10px);
}

.task-leave-active {
  position: absolute;
  width: 100%;
  transition: opacity calc(var(--dur) / 2) var(--ease);
}

.task-leave-to {
  opacity: 0;
}

.task-move {
  transition: transform var(--dur) var(--ease-spring-soft);
}

.t-row.st-failed {
  border-color: color-mix(in srgb, var(--err) 35%, var(--stroke-card));
}

.dir-ic {
  flex: none;
  margin-top: 2px;
  color: var(--muted);
}

.dir-ic.download {
  color: var(--accent);
}

.dir-ic.upload {
  color: var(--ok);
}

.t-body {
  flex: 1;
  min-width: 0;
}

.t-line1 {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 6px;
}

.t-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  font-size: 0.857rem;
  font-weight: 500;
  color: var(--heading);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.t-meta {
  flex: none;
  font-size: 0.786rem;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}

.t-line2 {
  display: flex;
  align-items: center;
  gap: 10px;
}

.t-pct {
  flex: none;
  width: 40px;
  text-align: right;
  font-size: 0.786rem;
  color: var(--muted);
  font-variant-numeric: tabular-nums;
}

.t-err {
  margin-top: 6px;
  overflow: hidden;
  font-size: 0.786rem;
  line-height: 1.4;
  color: var(--err);
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 右侧：状态徽章 + 行内重试钮 */
.t-right {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: none;
}

.chip {
  padding: 2px 10px;
  font-size: 0.786rem;
  border-radius: var(--radius-round);
  white-space: nowrap;
}

.chip.idle {
  color: var(--text2);
  background: color-mix(in srgb, var(--text) 8%, transparent);
}

.chip.run {
  color: var(--accent);
  background: var(--accent-soft);
}

.chip.ok {
  color: var(--ok);
  background: color-mix(in srgb, var(--ok) 12%, transparent);
}

.chip.err {
  color: var(--err);
  background: color-mix(in srgb, var(--err) 12%, transparent);
}

/* .fade-* 过渡基元已在 styles/base.css 全局定义，此处删除重复副本。 */
</style>
