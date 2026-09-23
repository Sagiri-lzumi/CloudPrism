<!--
  SettingsView.vue —— 设置页。
  对照 Python side_panel.SettingsPage 分组：外观（主题/字号）→ 缓存
  （分块大小 / 目录 / 上限 / 清空）→ 传输（分块/并发）→ 安全（自动锁定）
  → 局域网访问（Web 模式特有：开关 / 分享链接 / 访问令牌）
  → 百度网盘（凭证表单 + 授权流程，与向导内嵌表单同链路）→ 性能
  （加密核心数）→ 关于（运行时版本）。
  注意两个「分块」不是同一件事：缓存组的「分块大小」是大文件本地分块
  读缓存的分块粒度（同时是是否分块的阈值，Go 键 cache/chunk_mb）；传输组
  的「分块大小」是上传/下载单次传输块（transfer/chunk_index）。
  差异说明：Python 设置页的「连接信息/文件夹同步/恢复码」在 Go 端由
  密库页（VaultsView）承担（同步、恢复码均依赖连接态快照）；此处仅保留
  纯偏好类设置。全部写入即落盘（Go 设置 Store 每次 Set 即 Sync）。
-->
<script setup lang="ts">
import {computed, onMounted, reactive, ref} from 'vue'
import {ui} from '../lib/store'
import {applyFontSize, pickLocalDir} from '../lib/store'
import {App, Lan, Settings, Vault, unwrap} from '../lib/api'
import type {CacheInfo, LanStatus} from '../lib/api'
import {MODE_LABELS, applyThemeIndex} from '../lib/theme'
import {showError, showInfo, showSuccess, showWarning} from '../lib/toast'
import Button from '../components/fluent/Button.vue'
import PrimaryButton from '../components/fluent/PrimaryButton.vue'
import ComboBoxCard from '../components/fluent/ComboBoxCard.vue'
import Icon from '../components/fluent/Icon.vue'
import LineEdit from '../components/fluent/LineEdit.vue'
import SpinBox from '../components/fluent/SpinBox.vue'
import SwitchCard from '../components/fluent/SwitchCard.vue'
import PageHeader from '../components/layout/PageHeader.vue'

/* -------------------------------------------------------- 选项常量 */

// 与 Go 端 settings 键注释对齐：字号 12/14/16/18 px；主题索引 0=系统 1=深 2=浅
const FONT_PX = [12, 14, 16, 18]
const FONT_LABELS = ['小 (12px)', '中 (14px)', '大 (16px)', '特大 (18px)']
// 分块档位：索引与 Go 端 transfer/chunk_index 一致（0=256KB … 3=4MB）
const CHUNK_LABELS = ['256 KB', '512 KB', '1 MB', '4 MB']
// 大文件分块读缓存的分块大小（MB）：与 Go 端 cache/chunk_mb 同值域，
// 同时充当「是否分块」的阈值（小于它整存为单独文件）
const CHUNK_SIZE_MB = [8, 16, 32, 50, 64, 128, 256, 512]
const CHUNK_SIZE_LABELS = CHUNK_SIZE_MB.map((mb) => `${mb} MB`)
// 自动锁定：0=从不 1/2/3 = 5/15/30 分钟
const AUTOLOCK_LABELS = ['从不', '5 分钟', '15 分钟', '30 分钟']

/** 把 ui.settings 中的未知值安全转数字（缺失/非法回退默认）。 */
function num(v: unknown, d: number): number {
  const n = Number(v)
  return Number.isFinite(n) ? n : d
}

/** 统一的设置写入错误提示。 */
function onErr(e: unknown) {
  showError('保存失败：' + unwrap(e).message)
}

/* ------------------------------------------------------- 响应式取值 */

const themeIdx = computed(() => num(ui.settings.themeIndex, 0))
const fontIdx = computed(() => Math.max(0, FONT_PX.indexOf(num(ui.settings.fontSize, 14))))
const chunkIdx = computed(() => {
  const i = num(ui.settings.chunkIndex, 1)
  return Math.min(CHUNK_LABELS.length - 1, Math.max(0, i))
})
const concurrent = computed(() => num(ui.settings.concurrent, 2))
const cacheLimit = computed(() => num(ui.settings.cacheLimitMb, 512))
const cachePath = computed(() => String(ui.settings.cachePath ?? ''))
const cachePathSet = computed(() => cachePath.value !== '')
// 分块大小：命中预设档位取下标，自定义值（如配置文件手改）回退到最接近档位
const chunkSizeMb = computed(() => num(ui.settings.chunkSizeMb, 50))
const chunkSizeIdx = computed(() => {
  const i = CHUNK_SIZE_MB.indexOf(chunkSizeMb.value)
  if (i >= 0) return i
  let best = 0
  CHUNK_SIZE_MB.forEach((mb, idx) => {
    if (mb <= chunkSizeMb.value) best = idx
  })
  return best
})
// 缓存运行时信息（占用/生效目录），挂载时拉一次、清理后再拉
const cacheInfo = ref<CacheInfo | null>(null)
const maxCores = computed(() => num(ui.settings.maxCores, 0))
const autoLockIdx = computed(() => num(ui.settings.autoLockIndex, 0))
const syncDir = computed(() => String(ui.settings.syncDir ?? ''))
const syncDirSet = computed(() => syncDir.value !== '')
// 同步需先连接密库（引擎挂在连接态上）
const connected = computed(() => !!ui.snap?.connected)

/* ------------------------------------------------------------ 动作 */

async function onTheme(i: number) {
  ui.settings.themeIndex = i
  applyThemeIndex(i) // 立即生效（html[data-theme] + localStorage）
  try {
    await Settings.SetTheme(i)
  } catch (e) {
    onErr(e)
  }
}

async function onFont(i: number) {
  const px = FONT_PX[i] ?? 14
  ui.settings.fontSize = px
  applyFontSize(px)
  try {
    await Settings.SetFontSize(px)
  } catch (e) {
    onErr(e)
  }
}

async function onChunk(i: number) {
  ui.settings.chunkIndex = i
  try {
    await Settings.SetTransfer(i, concurrent.value)
  } catch (e) {
    onErr(e)
  }
}

async function onConcurrent(n: number) {
  ui.settings.concurrent = n
  try {
    await Settings.SetTransfer(chunkIdx.value, n)
  } catch (e) {
    onErr(e)
  }
}

async function onCacheLimit(mb: number) {
  ui.settings.cacheLimitMb = mb
  try {
    await Settings.SetCache(mb, cachePath.value)
  } catch (e) {
    onErr(e)
  }
}

/** 浏览选择新缓存目录（保持上限不变）。选在网页里完成，不再弹主机原生框。 */
async function browseCacheDir() {
  const dir = await pickLocalDir({
    title: '选择缓存目录（建议避开系统盘的用户目录）',
    start: cachePath.value,
  })
  if (!dir) return // 用户取消
  try {
    ui.settings.cachePath = dir
    await Settings.SetCache(cacheLimit.value, dir)
    showSuccess('缓存目录已更新')
    await refreshCacheInfo()
  } catch (e) {
    onErr(e)
  }
}

/** 清除自定义缓存目录（回退程序目录旁的默认位置）。 */
async function resetCacheDir() {
  ui.settings.cachePath = ''
  try {
    await Settings.SetCache(cacheLimit.value, '')
    showInfo('已恢复默认缓存目录（程序目录旁 data/cache）')
    await refreshCacheInfo()
  } catch (e) {
    onErr(e)
  }
}

/** 修改分块大小（= 分块阈值），对后续新建的缓存条目生效。 */
async function onChunkSize(i: number) {
  const mb = CHUNK_SIZE_MB[i] ?? 50
  ui.settings.chunkSizeMb = mb
  try {
    await Settings.SetChunkSize(mb)
    showInfo(`分块大小已设为 ${mb} MB（${mb} MB 以上的文件将按块缓存）`)
    await refreshCacheInfo()
  } catch (e) {
    onErr(e)
  }
}

/** 拉取缓存占用/生效目录；失败静默（展示型信息，不打断设置操作）。 */
async function refreshCacheInfo() {
  try {
    cacheInfo.value = await Settings.CacheInfo()
  } catch {
    cacheInfo.value = null
  }
}

/** 清空本地缓存（缩略图 + 媒体分块），不影响云端数据。 */
const cacheBusy = ref(false)
async function purgeCache() {
  if (cacheBusy.value) return
  cacheBusy.value = true
  try {
    cacheInfo.value = await Settings.PurgeCache()
    showSuccess('本地缓存已清空')
  } catch (e) {
    onErr(e)
  } finally {
    cacheBusy.value = false
  }
}

/** 缓存占用的人类可读文本。 */
const cacheUsageText = computed(() => {
  const info = cacheInfo.value
  if (!info) return ''
  const mb = info.bytes / (1024 * 1024)
  const size = mb >= 1024 ? `${(mb / 1024).toFixed(2)} GB` : `${mb.toFixed(1)} MB`
  return `已占用 ${size} · ${info.entries} 个条目`
})

async function onMaxCores(n: number) {
  ui.settings.maxCores = n
  try {
    await Settings.SetMaxCores(n)
  } catch (e) {
    onErr(e)
  }
}

async function onAutoLock(i: number) {
  ui.settings.autoLockIndex = i
  try {
    await Settings.SetAutoLock(i)
    if (i > 0) showInfo(`将在无操作 ${AUTOLOCK_LABELS[i]} 后自动锁定`)
  } catch (e) {
    onErr(e)
  }
}

/* ------------------------------------------------------- 文件夹同步 */

// 同步目录操作的防重入（快速双击防重复弹目录框/重复请求）
const syncBusy = ref(false)

/** 浏览选择本地同步目录并立即落盘（与密库页同步卡共用同一选择器）。 */
async function chooseSyncDir() {
  if (syncBusy.value) return
  syncBusy.value = true
  try {
    const dir = await pickLocalDir({
      title: '选择要同步的本地目录',
      start: String(ui.settings.syncDir ?? ''),
    })
    if (!dir) return // 用户取消
    ui.settings.syncDir = dir
    await Settings.SetSyncDir(dir)
    showSuccess('本地同步目录已更新')
  } catch (e) {
    onErr(e)
  } finally {
    syncBusy.value = false
  }
}

/** 清除同步目录设置（回退未设置态）。 */
async function clearSyncDir() {
  if (syncBusy.value) return
  syncBusy.value = true
  try {
    ui.settings.syncDir = ''
    await Settings.SetSyncDir('')
    showInfo('已清除本地同步目录设置')
  } catch (e) {
    onErr(e)
  } finally {
    syncBusy.value = false
  }
}

/** 立即执行一轮本地 → 云端单向同步（需已连接密库）。 */
async function syncNow() {
  if (syncBusy.value) return
  if (!connected.value) {
    showWarning('请先连接密库后再同步')
    return
  }
  syncBusy.value = true
  try {
    const n = await Settings.SyncNow()
    showInfo(n > 0 ? `已开始同步 ${n} 个文件` : '本地目录已是最新，无需同步')
  } catch (e) {
    const err = unwrap(e)
    if (err.code === 'sync-dir-unset') showWarning('请先选择本地同步目录')
    else onErr(e)
  } finally {
    syncBusy.value = false
  }
}

/* ------------------------------------------------------- 百度授权卡 */

// 与向导内嵌表单同链路（bind Vault 域），此处供已连接态也可重授权
const bAuth = reactive({authorized: false, appKey: '', appID: ''})
const bf = reactive({appID: '', appKey: '', secret: '', signKey: '', code: ''})
const baiduBusy = ref(false)
const baiduUrl = ref('')
const bNote = ref('') // 就地状态/错误行（✓ 前缀=成功）
const showGuide = ref(false)

async function refreshBaidu() {
  try {
    const info = await Vault.BaiduStatus()
    bAuth.authorized = !!info.authorized
    bAuth.appKey = info.appKey ?? ''
    bAuth.appID = info.appId ?? ''
  } catch {
    bAuth.authorized = false
  }
}

onMounted(() => {
  void refreshBaidu()
  void refreshCacheInfo()
  void App.Version().then((v) => (version.value = v)).catch(() => {})
  readBuild()
})

/** 打开百度授权页（oob：页面直接展示 code；下方 URL 可复制兜底）。 */
async function openBaiduPage() {
  const appKey = bf.appKey.trim()
  if (!appKey) {
    bNote.value = '请先填写 AppKey'
    return
  }
  baiduBusy.value = true
  bNote.value = ''
  try {
    baiduUrl.value = await Vault.BaiduAuthURL(bf.appID.trim(), appKey)
    bNote.value = '✓ 已打开浏览器授权页：登录并同意后，把页面展示的 code 粘贴到下方输入框'
  } catch (e) {
    bNote.value = unwrap(e).message
  } finally {
    baiduBusy.value = false
  }
}

/** 粘贴 code 完成授权：换 token 加密落盘（60s 内有效）。 */
async function finishBaidu() {
  const code = bf.code.trim()
  if (!code) {
    bNote.value = '请先粘贴授权页展示的 code'
    return
  }
  baiduBusy.value = true
  bNote.value = ''
  try {
    await Vault.BaiduSaveAuth(bf.appID.trim(), bf.appKey.trim(), bf.secret.trim(), bf.signKey.trim(), code)
    await refreshBaidu()
    bf.code = ''
    bNote.value = '✓ 授权成功：凭证已加密保存在本机（%APPDATA%\\CloudPrism\\baidu.json）'
  } catch (e) {
    bNote.value = '授权失败：' + unwrap(e).message
  } finally {
    baiduBusy.value = false
  }
}

/** 清除本地授权（仅影响本机连接能力，云端数据不受影响）。 */
async function clearBaidu() {
  if (baiduBusy.value) return
  baiduBusy.value = true
  try {
    await Vault.BaiduClearAuth()
    await refreshBaidu()
    baiduUrl.value = ''
    bNote.value = ''
    showWarning('已清除本地百度授权记录')
  } catch (e) {
    bNote.value = unwrap(e).message
  } finally {
    baiduBusy.value = false
  }
}

/** 复制文本到剪贴板（失败时提示手动选中，不静默吞掉）。 */
function copyText(text: string, okMsg: string) {
  if (!text) return
  void navigator.clipboard.writeText(text).then(
    () => showInfo(okMsg),
    () => showWarning('复制失败，请手动选中文本'),
  )
}

function copyBaiduUrl() {
  copyText(baiduUrl.value, '授权网址已复制')
}

/** 授权状态卡文案（掩码 AppKey 展示，与后端 BaiduStatus 口径一致）。 */
const bStatusText = computed(() =>
  bAuth.authorized
    ? `已授权（AppKey ${bAuth.appKey}）——凭证经 DPAPI 加密存储在本机，仅影响本机连接能力`
    : '未授权：填写下方应用凭证并完成授权后，方可使用百度网盘后端',
)

/* 凭证获取教程（浓缩自 WindowsPy assets/baidu_guide.md 关键步骤） */
const GUIDE_LINES = [
  '1. 百度账号需通过实名认证（个人开发者即可，开放平台入口：pan.baidu.com/union/console/home）。',
  '2. 登录后创建「个人开发者」应用；应用若处于审核中，接口调用会失败。',
  '3. 应用详情页可找到四项凭证：Appid（应用标识）、AppKey（即 client_id）、SecretKey（即 client_secret，严格保密）、SignKey（签名校验，可留空）。',
  '4. 无需配置回调地址：CloudPrism 使用 oob 模式，授权页会直接展示 code。',
  '5. 把四项凭证填入上方表单，点「打开授权页」用百度账号授权；将页面展示的 code 粘贴到「授权码」后点「完成授权」。',
  '6. 凭证仅加密保存在本机 %APPDATA%\\CloudPrism\\baidu.json，不会上传；access_token 约 30 天过期，届时重新授权即可。',
]

/* -------------------------------------------------------- 局域网访问档 */

// 局域网访问状态（镜像 bind.Lan.Status）。
//
// 关键：enabled 是「设置里已保存的意愿」，active 是「当前进程真的在对局域网
// 监听」。切换开关不热重载监听（绑定的地址在 Listen 时确定），因此两者可能
// 不一致 —— 卡片必须把两者都显示出来并明确提示「需重启」，否则就成了
// 「看似可配但不会生效」的展示缺口。
const lan = reactive<LanStatus>({
  enabled: false,
  active: false,
  port: 0,
  token: '',
  localUrl: '',
  addrs: [],
})
const lanBusy = ref(false)
const lanShowToken = ref(false) // 令牌默认打码（截图/投屏时不至于直接泄露）

async function refreshLan() {
  try {
    Object.assign(lan, await Lan.Status())
  } catch {
    // 状态拉取失败时保留上一次的值：设置页不应因为后端未连接而整页报错
  }
}

/** 开关内容文案：把「已保存」与「当前生效」的差异说清楚。 */
const lanSwitchHint = computed(() => {
  if (!lan.enabled) {
    return '关闭时只监听本机（127.0.0.1），其他设备无法访问；开启后需重启程序才会真正对外监听'
  }
  if (lan.active) {
    return `已开启并生效：正在监听 ${lan.port} 端口，同一网络下的设备可用下方链接访问`
  }
  return '已保存为「开启」，但当前进程仍在仅本机监听：重启程序后生效'
})

/** 需要在卡片上单独提示「重启才生效」的条件（避免隐藏的无效开关）。 */
const lanNeedRestart = computed(() => lan.enabled !== lan.active)
const lanRestartHint = computed(() =>
  lan.enabled
    ? '开关已改为「开启」，当前进程仍在监听本机。请从托盘菜单退出后重新打开本程序。'
    : '开关已改为「关闭」，当前进程仍在监听局域网。请从托盘菜单退出后重新打开本程序。',
)

/** 令牌展示：默认打码，保留首尾各 4 位便于用户比对。 */
const lanTokenText = computed(() => {
  const t = lan.token
  if (!t) return '—'
  if (lanShowToken.value || t.length <= 8) return t
  return `${t.slice(0, 4)}${'•'.repeat(t.length - 8)}${t.slice(-4)}`
})

async function onLanToggle(on: boolean) {
  if (lanBusy.value) return
  lanBusy.value = true
  const prev = lan.enabled
  lan.enabled = on // 乐观更新，失败回滚
  try {
    await Lan.SetEnabled(on)
    // 不热重载监听：如实告知生效时机，而不是让用户以为已经生效
    showInfo(on ? '已开启局域网访问：重启程序后生效' : '已关闭局域网访问：重启程序后恢复仅本机访问')
  } catch (e) {
    lan.enabled = prev
    showError('保存失败：' + unwrap(e).message)
  } finally {
    lanBusy.value = false
    void refreshLan()
  }
}

async function rotateLanToken() {
  if (lanBusy.value) return
  lanBusy.value = true
  try {
    await Lan.RotateToken()
    await refreshLan()
    showWarning('已重新生成访问令牌：之前分享的链接全部失效，需用新链接重新进入')
  } catch (e) {
    showError('重新生成失败：' + unwrap(e).message)
  } finally {
    lanBusy.value = false
  }
}

/* ------------------------------------------------------------ 关于 */

const version = ref('读取运行时信息…')

/**
 * 当前**界面构建指纹**（本 bundle 自己的文件名 + 同批 css）。
 *
 * 为什么需要它：应用内原本没有任何可信的版本显示 —— Go 端 `version()` 恒返回
 * `"web-mode"`，指纹只打进 `data/logs/cloudprism.log`。而「双击新包却还是旧界面」
 * 在单实例探测下是**必然**现象（新 exe 探到旧实例就静默退出、把浏览器指回旧端口），
 * 于是用户完全无法自证跑的是哪一版，只能反复怀疑「修了到底生效没有」。
 * （2026-09-21 实测踩过：用户双击 v1.7 两次都被顶掉，界面始终是带 bug 的 v1.6。）
 *
 * 取的是自身 bundle 名，与 `Release\<日期>-<tag>-Go-MD5.txt` 的「前端产物:」一行
 * 逐字对照即可判定；css 不是本模块的 URL，只能从已加载资源里捞同批产物。
 */
const build = ref('（读取失败）')

function readBuild() {
  const tail = (u: string) => u.split('/').pop() ?? ''
  try {
    const js = tail(import.meta.url)
    const css = performance
      .getEntriesByType('resource')
      .map((e) => e.name)
      .filter((n) => /\/assets\/index-[\w-]+\.css$/.test(n))
      .map(tail)[0]
    build.value = [js, css].filter(Boolean).join(' + ') || '（读取失败）'
  } catch {
    build.value = '（读取失败）'
  }
}
</script>

<template>
  <div class="s-view">
    <!-- 统一页头（56px）：本页无页面级动作，故只用标题区。
         此前这里是「只放页题的 46px 工具行 + 内容区大标题」两层，现已合并。 -->
    <PageHeader title="设置" icon="palette" />

    <div class="s-scroll">
      <div class="s-inner">        <!-- ===================== 外观 ===================== -->
        <div class="group-title">外观</div>
        <ComboBoxCard
          icon="palette"
          title="主题模式"
          content="跟随系统或手动切换明暗配色"
          :options="[...MODE_LABELS]"
          :model-value="themeIdx"
          @change="onTheme"
        />
        <ComboBoxCard
          icon="font_size"
          title="界面字号"
          content="调整全局文字大小（12–18 px）"
          :options="[...FONT_LABELS]"
          :model-value="fontIdx"
          @change="onFont"
        />

        <!-- ===================== 缓存 ===================== -->
        <div class="group-title">缓存</div>
        <ComboBoxCard
          icon="library"
          title="分块大小"
          content="大于等于该值的文件按块缓存：切成「原名-1 / 原名-2 …」存进同名子文件夹；小于该值的文件整存为单独文件。按块读取，视频无需等整文件下载完即可播放"
          :options="[...CHUNK_SIZE_LABELS]"
          :model-value="chunkSizeIdx"
          @change="onChunkSize"
        />
        <div class="set-card">
          <span class="set-icon"><Icon name="folder" :size="17" /></span>
          <div class="set-body">
            <div class="set-title">缓存目录</div>
            <div class="set-content" :title="cachePath">
              {{ cachePathSet ? cachePath : '默认：程序目录旁 data/cache（便携，不写系统目录）' }}
            </div>
          </div>
          <div class="set-right">
            <Button
              v-if="cachePathSet"
              icon="cancel"
              iconOnly
              title="恢复默认目录"
              :disabled="baiduBusy"
              @click="resetCacheDir"
            />
            <Button icon="folder_add" :disabled="baiduBusy" @click="browseCacheDir">浏览…</Button>
          </div>
        </div>
        <div class="set-card">
          <span class="set-icon"><Icon name="history" :size="17" /></span>
          <div class="set-body">
            <div class="set-title">缓存大小上限</div>
            <div class="set-content">
              媒体分块缓存占用磁盘的上限（64–4096 MB）<template v-if="cacheUsageText">
                · {{ cacheUsageText }}</template
              >
            </div>
          </div>
          <div class="set-right">
            <SpinBox
              :model-value="cacheLimit"
              :min="64"
              :max="4096"
              :step="16"
              suffix=" MB"
              :width="96"
              @change="onCacheLimit"
            />
            <Button icon="delete" :disabled="cacheBusy" @click="purgeCache">清空缓存</Button>
          </div>
        </div>

        <!-- ===================== 传输 ===================== -->
        <div class="group-title">传输</div>
        <ComboBoxCard
          icon="library"
          title="分块大小"
          content="上传 / 下载单次分块尺寸；网络越好可越大，块头开销越小"
          :options="[...CHUNK_LABELS]"
          :model-value="chunkIdx"
          @change="onChunk"
        />
        <div class="set-card">
          <span class="set-icon"><Icon name="speed_high" :size="17" /></span>
          <div class="set-body">
            <div class="set-title">并发任务数</div>
            <div class="set-content">同时运行的上传 / 下载任务数上限（1–4），即时应用到后续任务</div>
          </div>
          <div class="set-right">
            <SpinBox
              :model-value="concurrent"
              :min="1"
              :max="4"
              suffix=" 个"
              :width="76"
              @change="onConcurrent"
            />
          </div>
        </div>

        <!-- ===================== 文件夹同步 ===================== -->
        <div class="group-title">文件夹同步</div>
        <div class="set-card">
          <span class="set-icon"><Icon name="sync" :size="17" /></span>
          <div class="set-body">
            <div class="set-title">本地同步目录</div>
            <div class="set-content" :title="syncDir">
              {{ syncDirSet ? syncDir : '未设置：选择后将本目录内容单向同步到密库当前目录' }}
            </div>
          </div>
          <div class="set-right">
            <Button
              v-if="syncDirSet"
              icon="cancel"
              iconOnly
              title="清除同步目录设置"
              :disabled="syncBusy"
              @click="clearSyncDir"
            />
            <Button icon="folder_add" :disabled="syncBusy" @click="chooseSyncDir">
              {{ syncDirSet ? '更改…' : '选择目录…' }}
            </Button>
          </div>
        </div>
        <div class="set-card">
          <span class="set-icon"><Icon name="send" :size="17" /></span>
          <div class="set-body">
            <div class="set-title">立即同步</div>
            <div class="set-content">执行一轮本地 → 云端单向增量同步（目录需先在密库页连接后可用）</div>
          </div>
          <div class="set-right">
            <Button
              icon="sync"
              :disabled="syncBusy || !syncDirSet || !connected"
              title="需先连接密库"
              @click="syncNow"
            >
              {{ syncBusy ? '执行中…' : '开始同步' }}
            </Button>
          </div>
        </div>

        <!-- ===================== 安全 ===================== -->
        <div class="group-title">安全</div>
        <ComboBoxCard
          icon="fingerprint"
          title="自动锁定"
          content="无操作超过设定时间后自动锁定密库（再访问需重新验证）"
          :options="[...AUTOLOCK_LABELS]"
          :model-value="autoLockIdx"
          @change="onAutoLock"
        />
        <div class="hint-row">连接详情与恢复码管理位于「密库」页（连接密库后可见）。</div>

        <!-- ===================== 局域网访问 ===================== -->
        <div class="group-title">局域网访问</div>
        <SwitchCard
          icon="wifi"
          title="允许其他设备访问"
          :content="lanSwitchHint"
          :checked="lan.enabled"
          :disabled="lanBusy"
          @change="onLanToggle"
        />
        <div v-if="lanNeedRestart" class="set-card">
          <span class="set-icon"><Icon name="update" :size="17" /></span>
          <div class="set-body">
            <div class="set-title">需重启程序才会生效</div>
            <div class="set-content">{{ lanRestartHint }}</div>
          </div>
        </div>
        <template v-if="lan.active">
          <div v-for="a in lan.addrs" :key="a.ip" class="lan-row">
            <span class="lan-iface" :title="a.iface">{{ a.iface }}</span>
            <span class="url-text" dir="ltr">{{ a.url }}</span>
            <span v-if="a.virtual" class="lan-tag">虚拟网卡</span>
            <Button icon="copy" :disabled="lanBusy" @click="copyText(a.url, '分享链接已复制')">
              复制
            </Button>
          </div>
          <div v-if="!lan.addrs.length" class="hint-row">
            未检测到局域网 IPv4 地址：请确认本机已连接 Wi-Fi 或网线后重启程序。
          </div>
          <div v-if="lan.addrs.some((a) => a.virtual)" class="hint-row">
            标「虚拟网卡」的地址（VMware / 代理隧道等）其他设备通常连不上，
            请优先使用不带该标记的地址；若手机与电脑连的是同一个 Wi-Fi，选「WLAN」或「以太网」那条。
          </div>
          <div class="set-card">
            <span class="set-icon"><Icon name="lock" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">访问令牌</div>
              <div class="set-content">
                远端设备凭它证明自己已被授权；重新生成可立即踢掉所有已授权设备
              </div>
            </div>
            <div class="set-right">
              <span class="lan-token" dir="ltr">{{ lanTokenText }}</span>
              <Button
                :icon="lanShowToken ? 'hide' : 'eye'"
                iconOnly
                :title="lanShowToken ? '隐藏令牌' : '显示令牌'"
                :disabled="lanBusy"
                @click="lanShowToken = !lanShowToken"
              />
              <Button icon="update" :disabled="lanBusy" @click="rotateLanToken">重新生成</Button>
            </div>
          </div>
        </template>
        <div v-else class="hint-row">
          当前未对外监听：开启并重启程序后，这里会显示带令牌的分享链接与访问令牌。
        </div>
        <div class="hint-row">
          首次开启时 Windows 会弹出防火墙授权框，需选择「允许」，否则其他设备连不上；
          局域网走明文 HTTP，令牌与文件名在同一网段内可被嗅探，请勿在公共 Wi-Fi 下开启。
          密库内容始终是端到端加密的，令牌只保护界面访问。
        </div>

        <!-- ===================== 百度网盘 ===================== -->
        <div class="group-title">百度网盘</div>
        <div class="set-card" :class="{ok: bAuth.authorized}">
          <span class="set-icon"><Icon name="cloud" :size="17" /></span>
          <div class="set-body">
            <div class="set-title">授权状态</div>
            <div class="set-content" :class="{err: !bAuth.authorized}">{{ bStatusText }}</div>
          </div>
          <div class="set-right">
            <Button
              v-if="bAuth.authorized"
              icon="cancel"
              title="清除本机授权记录"
              :disabled="baiduBusy"
              @click="clearBaidu"
            >
              清除授权
            </Button>
          </div>
        </div>

        <div class="b-form">
          <div class="bf-row">
            <label class="bf-label">Appid</label>
            <div class="bf-host">
              <LineEdit
                v-model="bf.appID"
                placeholder="应用唯一标识（应用详情页可见）"
                :disabled="baiduBusy"
              />
            </div>
          </div>
          <div class="bf-row">
            <label class="bf-label">AppKey</label>
            <div class="bf-host">
              <LineEdit
                v-model="bf.appKey"
                placeholder="即 client_id（OAuth 客户端标识）"
                :disabled="baiduBusy"
              />
            </div>
          </div>
          <div class="bf-row">
            <label class="bf-label">SecretKey</label>
            <div class="bf-host">
              <LineEdit
                v-model="bf.secret"
                password
                placeholder="即 client_secret（严格保密）"
                :disabled="baiduBusy"
              />
            </div>
          </div>
          <div class="bf-row">
            <label class="bf-label">SignKey</label>
            <div class="bf-host">
              <LineEdit
                v-model="bf.signKey"
                password
                clearable
                placeholder="签名校验密钥（可选，可留空）"
                :disabled="baiduBusy"
              />
            </div>
          </div>

          <button type="button" class="bf-guide-toggle" @click="showGuide = !showGuide">
            如何获取这些凭证？查看教程
            <Icon :name="showGuide ? 'chevron_down_med' : 'chevron_right'" :size="12" />
          </button>
          <Transition name="fade">
            <div v-if="showGuide" class="bf-guide">
              <p v-for="(l, i) in GUIDE_LINES" :key="i">{{ l }}</p>
            </div>
          </Transition>

          <div class="bf-actions">
            <PrimaryButton icon="connect" :disabled="baiduBusy || !bf.appKey.trim()" @click="openBaiduPage">
              {{ baiduBusy ? '执行中…' : '打开授权页' }}
            </PrimaryButton>
            <span class="bf-note">授权页展示的 code 粘贴到下行后完成授权</span>
          </div>

          <div v-if="baiduUrl" class="bf-url">
            <span class="url-text" dir="ltr">{{ baiduUrl }}</span>
            <Button icon="copy" :disabled="baiduBusy" @click="copyBaiduUrl">复制网址</Button>
          </div>

          <div class="bf-row">
            <label class="bf-label">授权码</label>
            <div class="bf-host">
              <LineEdit
                v-model="bf.code"
                placeholder="粘贴授权页展示的 code（一次性）"
                :disabled="baiduBusy"
                @enter="finishBaidu"
              />
            </div>
            <Button :disabled="baiduBusy || !bf.code.trim()" @click="finishBaidu">完成授权</Button>
          </div>

          <div v-if="bNote" class="bf-status" :class="{err: !bNote.startsWith('✓')}">{{ bNote }}</div>
        </div>

        <!-- ===================== 性能 ===================== -->
        <div class="group-title">性能</div>
        <div class="set-card">
          <span class="set-icon"><Icon name="speed_high" :size="17" /></span>
          <div class="set-body">
            <div class="set-title">加密核心数</div>
            <div class="set-content">单文件加解密使用的 CPU 核数上限；0 = 自动（按 CPU 数，上限 8）</div>
          </div>
          <div class="set-right">
            <SpinBox
              :model-value="maxCores"
              :min="0"
              :max="8"
              suffix=" 核"
              :width="76"
              @change="onMaxCores"
            />
          </div>
        </div>

        <!-- ===================== 关于 ===================== -->
        <div class="group-title">关于</div>
        <div class="set-card">
          <span class="set-icon"><Icon name="cloud" :size="17" /></span>
          <div class="set-body">
            <div class="set-title">CloudPrism</div>
            <div class="set-content">文件加密云端保险库（Go 内嵌 Web 服务版）</div>
          </div>
        </div>
        <!-- 两行只读信息：服务端自报串 + 界面构建指纹。
             「界面构建」与 Release 包里 -Go-MD5.txt 的「前端产物:」一行对照；
             不一致就说明当前浏览器连的不是那个发布包 —— 单实例探测把新 exe 顶掉时
             正是这种情况，不给这行字用户无从察觉。 -->
        <div
          class="about-note"
          title="「界面构建」用于分辨当前跑的是哪个发布包：与 Release 包里 -Go-MD5.txt 的「前端产物:」一行对照即可"
        >
          <div>服务端：{{ version }}</div>
          <div>界面构建：{{ build }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.s-view {
  display: flex;
  flex-direction: column;
  height: 100%;
  box-sizing: border-box;
  /* 浮层页头的定位上下文（PageHeader 绝对定位在本视图顶缘） */
  position: relative;
}

/* 页头已由 components/layout/PageHeader.vue 承担（v1.3 起三页共用同一根
   56px 顶栏），原 components.css 的 .page-head/.page-h1/.page-sub/.count
   大标题模式随之废弃；分组标题 .group-title、设置卡的 ok 语义变体仍在
   styles/components.css。历史问题：.page-title/.count 曾与 TransfersView
   各写一份且逐字节相同；.group-title 两页间距还不一致（16/4/4 vs 14/4/2）。 */

/* 滚动内容区：单列居中，分组标题分隔。
   滚动区从 y=0 起（页头是浮层），padding-top 让位 —— 内容从玻璃页头底下
   穿过；让位之外再留 18px —— 页头自带一条分隔线，内容贴边会显局促。 */
.s-scroll {
  flex: 1;
  overflow-y: auto;
  padding: calc(var(--page-head-h) + 18px) 24px 28px;
}

/* 单列内容列宽：与密库页统一为 860px（设置页右侧有「数值框 + 单位 + 按钮」
   这类组合控件，是两页里更宽的那个需求，取它做基准） */
.s-inner {
  max-width: 860px;
  margin: 0 auto;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

/* .set-card.ok .set-icon 的语义变体已上收 components.css */
.set-content.err {
  color: var(--err);
}

/* 组间提示行 */
.hint-row {
  padding: 0 4px;
  font-size: 0.786rem;
  line-height: 1.5;
  color: var(--muted);
}

/* ---- 百度凭证表单卡（v1.01：半透玻璃卡面，无模糊） ---- */
.b-form {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px;
  background: var(--glass-card);
  border: 1px solid var(--stroke-card);
  border-radius: var(--radius-card);
}

.bf-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.bf-label {
  flex: none;
  width: 88px;
  font-size: 0.857rem;
  color: var(--text2);
  text-align: right;
  user-select: none;
}

/* 输入框宿主：占满剩余宽度并让内部输入拉伸（子组件根节点） */
.bf-host {
  flex: 1;
  min-width: 0;
  display: flex;
}

.bf-host :deep(.cp-line-edit) {
  width: 100%;
}

/* 教程开关（链接风按钮） */
.bf-guide-toggle {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 0;
  font-size: 0.857rem;
  color: var(--accent);
  background: none;
  border: none;
  cursor: pointer;
}

.bf-guide-toggle:hover {
  text-decoration: underline;
}

.bf-guide {
  padding: 10px 12px;
  font-size: 0.786rem;
  line-height: 1.6;
  color: var(--text2);
  background: var(--bg-page);
  border-radius: var(--radius-ctrl);
}

.bf-guide p {
  margin: 0 0 6px;
}

.bf-guide p:last-child {
  margin-bottom: 0;
}

.bf-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}

.bf-note {
  font-size: 0.786rem;
  color: var(--muted);
}

/* 授权 URL / 局域网分享链接兜底展示行（同一版式：等宽文本框 + 复制按钮） */
.bf-url,
.lan-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.url-text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  padding: 4px 8px;
  font-family: Consolas, 'Cascadia Mono', monospace;
  font-size: 0.786rem;
  color: var(--text2);
  background: var(--bg-page);
  border: 1px solid var(--stroke);
  border-radius: var(--radius-ctrl);
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 令牌值：等宽打码展示，宽度受限避免把「重新生成」按钮挤出卡片 */
.lan-token {
  max-width: 220px;
  overflow: hidden;
  font-family: Consolas, 'Cascadia Mono', monospace;
  font-size: 0.786rem;
  color: var(--text2);
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 网卡名：窄列，避免长名（VMware Network Adapter VMnet1）挤压地址栏 */
.lan-iface {
  flex: none;
  max-width: 104px;
  overflow: hidden;
  font-size: 0.786rem;
  color: var(--muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 「虚拟网卡」标记：提示该地址其他设备通常连不上 */
.lan-tag {
  flex: none;
  padding: 1px 6px;
  font-size: 0.714rem;
  color: var(--warn);
  background: color-mix(in srgb, var(--warn) 12%, transparent);
  border-radius: var(--radius-round);
  white-space: nowrap;
}

.bf-status {
  font-size: 0.786rem;
  line-height: 1.5;
  color: var(--ok);
}

.bf-status.err {
  color: var(--err);
}

/* 关于区尾部版本行 */
.about-note {
  padding: 0 4px;
  font-size: 0.786rem;
  color: var(--muted);
}

/* .fade-* 过渡基元已在 styles/base.css 全局定义（此前 6 个文件各抄一份，
   内容逐字节相同），这里删除重复副本。 */
</style>
