<!--
  PreviewPanel.vue —— 预览面板（对照 WindowsPy preview_panel.py）。
  选中文件后按类别分流：video/audio → 令牌流内嵌播放器；image → 代理
  URL 直显；text → fetch 同源渲染（代理已带 CORS 头）；其余/目录 → 信息
  页 + 下载/解密导出动作。头部固定信息行；底部安全兜底保留「导出到本地
  用系统播放器」路径（URL 吊销与组件卸载时回收 token）。
-->
<script setup lang="ts">
import {computed, onBeforeUnmount, ref, watch} from 'vue'
import {ui, mediaUrl, revoke, downloadSel, exportSel} from '../lib/store'
import {fmtSize} from '../lib/format'
import {kindOf, KIND_ICON} from '../lib/media'
import Icon from '../components/fluent/Icon.vue'
import Button from '../components/fluent/Button.vue'
import ProgressBar from '../components/fluent/ProgressBar.vue'
import MediaPlayer from '../components/layout/MediaPlayer.vue'
import {ApiCode, unwrap} from '../lib/api'

const sel = computed(() => ui.sel)

// 类别派生：目录一律 other（信息页）；文件按扩展名
const isDir = computed(() => !!sel.value?.isDir)
const kind = computed(() => (sel.value && !isDir.value ? kindOf(sel.value.display) : 'other'))

/* ------------------------------------------------ 代理 URL 生命周期 */

/** 当前签发的预览 URL（图片/文本/媒体共用；签发即记账，更换时吊销） */
const url = ref('')
/** 文本预览内容与状态 */
const text = ref('')
const textErr = ref('')
/** 加载中（图片/媒体等待代理响应） */
const loading = ref(false)
const loadErr = ref('')

// 代际：快速切换条目时旧 URL 请求/吊销不串台
let urlGen = 0
let curUrl = ''

watch(sel, async (e) => {
  const gen = ++urlGen
  url.value = ''
  text.value = ''
  textErr.value = ''
  loadErr.value = ''
  loading.value = true
  // 回收上一枚令牌（幂等：吊销不存在的令牌无害）
  if (curUrl) void revoke(curUrl).catch(() => {})
  curUrl = ''
  if (!e || isDir.value) {
    loading.value = false
    return
  }
  try {
    const u = await mediaUrl(e)
    if (gen !== urlGen) return // 期间又切换了条目
    url.value = u
    curUrl = u
    if (kind.value === 'text') void loadText(u, gen)
    loading.value = false
  } catch (err) {
    const apiErr = unwrap(err)
    if (gen !== urlGen) return
    loading.value = false
    // 锁定类错误不打断浏览（store 已 toast；此处静默）
    loadErr.value = apiErr.code === ApiCode.Locked ? '' : apiErr.message
  }
})

/** 文本预览：走代理 URL fetch（Go 侧已带 Access-Control-Allow-Origin）。 */
async function loadText(u: string, gen: number) {
  try {
    const resp = await fetch(u)
    if (gen !== urlGen) return
    if (!resp.ok) {
      textErr.value = `读取失败（HTTP ${resp.status}）`
      return
    }
    const raw = await resp.text()
    if (gen !== urlGen) return
    text.value = raw
  } catch {
    if (gen !== urlGen) return
    textErr.value = '文本拉取失败（网络或解码错误）'
  }
}

function onImgErr() {
  if (url.value) loadErr.value = '图片解码失败'
}

// 超大文本只保留首尾各若干行，避免长文档拖垮渲染
const TEXT_LIMIT = 400_000
const textShown = computed(() => {
  if (text.value.length <= TEXT_LIMIT) return text.value
  return (
    text.value.slice(0, TEXT_LIMIT) +
    `\n\n……（内容过长，已截断显示前 ${Math.floor(TEXT_LIMIT / 1000)}K 字符）……`
  )
})

onBeforeUnmount(() => {
  urlGen++ // 阻止在途请求回写
  if (curUrl) void revoke(curUrl).catch(() => {})
  curUrl = ''
})

/* ------------------------------------------------------- 展示派生 */

// 面板头部标题：展示名（目录/文件一致）
const title = computed(() => sel.value?.display ?? '')
const metaKind = computed(() => (isDir.value ? '目录' : {video: '视频', audio: '音频', image: '图片', text: '文本', other: '文件'}[kind.value]))
const meta = computed(() => {
  const e = sel.value!
  const size = isDir.value ? '' : fmtSize(e.size)
  return [metaKind.value, size, e.remote].filter(Boolean).join(' · ')
})

// 图片可预览类别专用图标（信息页大头像）
const bigIcon = computed(() => (isDir.value ? 'folder' : KIND_ICON[kind.value]))
</script>

<template>
  <aside class="cp-preview">
    <!-- 欢迎态（无选中） -->
    <div v-if="!sel" class="welcome">
      <Icon name="photo" :size="44" />
      <p>选择文件以预览</p>
      <span>单击左侧文件即可查看；媒体文件在此内嵌播放</span>
    </div>

    <template v-else>
      <!-- 固定信息头：图标 + 名称 + 元信息 -->
      <header class="head">
        <span class="head-icon"><Icon :name="bigIcon" :size="20" /></span>
        <div class="head-text">
          <h2 class="name" :title="title">{{ title }}</h2>
          <p class="meta" :title="sel.remote">{{ meta }}</p>
        </div>
      </header>

      <!-- 正文分流 -->
      <div class="body">
        <!-- 加载骨架 -->
        <div v-if="loading && !url" class="center">
          <div class="ring"><ProgressBar indeterminate /></div>
        </div>

        <!-- 视频 / 音频：令牌流内嵌播放器（remote 用于记忆续播 key） -->
        <MediaPlayer
          v-else-if="url && (kind === 'video' || kind === 'audio')"
          :url="url"
          :display="sel.display"
          :remote="sel.remote"
        />

        <!-- 图片：代理 URL 直显 -->
        <div v-else-if="url && kind === 'image'" class="center">
          <img v-if="!loadErr" class="img" :src="url" alt="" @error="onImgErr" />
          <div v-else class="center err-line">
            <p>{{ loadErr }}</p>
            <Button icon="download" @click="downloadSel">下载</Button>
          </div>
        </div>

        <!-- 文本：fetch 后纯文本渲染 -->
        <div v-else-if="kind === 'text'" class="txt-wrap">
          <pre v-if="text && !textErr" class="txt">{{ textShown }}</pre>
          <div v-else-if="textErr" class="center err-line">
            <p>{{ textErr }}</p>
          </div>
          <div v-else-if="!url && !loading" class="center"><p>无法读取文本</p></div>
        </div>

        <!-- 目录 / 不支持内嵌预览的文件 -->
        <div v-else class="center">
          <Icon :name="bigIcon" :size="56" class="dim" />
          <p class="lead2">{{ isDir ? '这是一个文件夹' : '此类型不支持内嵌预览' }}</p>
          <p class="sub2" v-if="!isDir">
            可直接下载，或解密导出后用本地应用打开。
          </p>
          <div class="actions">
            <Button icon="download" @click="downloadSel">下载</Button>
            <Button icon="share" @click="exportSel">解密导出</Button>
          </div>
        </div>

        <!-- 全局错误行（签发失败等） -->
        <div v-if="loadErr && kind !== 'image'" class="center err-line">
          <p>{{ loadErr }}</p>
        </div>
      </div>
    </template>
  </aside>
</template>

<style scoped>
.cp-preview {
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  background: var(--bg-page);
  border-left: none;
}

/* ---- 欢迎态 ---- */
.welcome {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  height: 100%;
  padding: 24px;
  text-align: center;
  color: var(--text2);
}

.welcome p {
  margin: 0;
  font-size: 1.2rem;
  color: var(--muted);
}

.welcome span {
  font-size: 0.857rem;
}

/* ---- 信息头 ---- */
.head {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 56px;
  padding: 8px 16px;
  border-bottom: 1px solid var(--divider);
}

.head-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 36px;
  height: 36px;
  color: var(--accent);
  background: var(--accent-soft);
  border-radius: 6px;
}

.head-text {
  min-width: 0;
}

.name {
  margin: 0;
  font-size: 0.929rem; /* 13px */
  font-weight: 600;
  color: var(--heading);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.meta {
  margin: 2px 0 0;
  font-size: 0.786rem;
  color: var(--text2);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* ---- 正文区 ---- */
.body {
  position: relative;
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

.center {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 16px;
  overflow: auto;
}

.center .ring {
  width: 160px;
}

.img {
  max-width: 100%;
  max-height: 100%;
  object-fit: contain;
  user-select: none;
}

.dim {
  color: var(--text2);
}

.lead2 {
  margin: 4px 0 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--heading);
}

.sub2 {
  margin: 0;
  font-size: 0.857rem;
  color: var(--text2);
}

.actions {
  display: flex;
  gap: 8px;
  margin-top: 8px;
}

/* 错误行（红字 + 操作） */
.err-line {
  color: var(--err);
  font-size: 0.857rem;
}

.err-line p {
  margin: 0 0 6px;
  overflow-wrap: anywhere;
  text-align: center;
}

/* 文本预览区：等宽滚动 */
.txt-wrap {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: auto;
  background: var(--surface);
}

.txt {
  flex: 1;
  margin: 0;
  padding: 14px 16px;
  font-family: Consolas, "Cascadia Mono", "Courier New", monospace;
  font-size: 0.857rem;
  line-height: 1.6;
  color: var(--text);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
