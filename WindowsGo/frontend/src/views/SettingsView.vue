<!--
  SettingsView.vue —— 设置页。
  对照 Python side_panel.SettingsPage 分组：外观（主题/字号）→ 缓存
  （上限/目录）→ 传输（分块/并发）→ 安全（自动锁定）→ 百度网盘
  （凭证表单 + 授权流程，与向导内嵌表单同链路）→ 性能（加密核心数）
  → 关于（运行时版本）。
  差异说明：Python 设置页的「连接信息/文件夹同步/恢复码」在 Go 端由
  密库页（VaultsView）承担（同步、恢复码均依赖连接态快照）；此处仅保留
  纯偏好类设置。全部写入即落盘（Go 设置 Store 每次 Set 即 Sync）。
-->
<script setup lang="ts">
import {computed, onMounted, reactive, ref} from 'vue'
import {ui} from '../lib/store'
import {applyFontSize} from '../lib/store'
import {App, Settings, Vault, unwrap} from '../lib/api'
import {MODE_LABELS, applyThemeIndex} from '../lib/theme'
import {showError, showInfo, showSuccess, showWarning} from '../lib/toast'
import Button from '../components/fluent/Button.vue'
import PrimaryButton from '../components/fluent/PrimaryButton.vue'
import ComboBoxCard from '../components/fluent/ComboBoxCard.vue'
import Icon from '../components/fluent/Icon.vue'
import LineEdit from '../components/fluent/LineEdit.vue'
import SpinBox from '../components/fluent/SpinBox.vue'

/* -------------------------------------------------------- 选项常量 */

// 与 Go 端 settings 键注释对齐：字号 12/14/16/18 px；主题索引 0=系统 1=深 2=浅
const FONT_PX = [12, 14, 16, 18]
const FONT_LABELS = ['小 (12px)', '中 (14px)', '大 (16px)', '特大 (18px)']
// 分块档位：索引与 Go 端 transfer/chunk_index 一致（0=256KB … 3=4MB）
const CHUNK_LABELS = ['256 KB', '512 KB', '1 MB', '4 MB']
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
const maxCores = computed(() => num(ui.settings.maxCores, 0))
const autoLockIdx = computed(() => num(ui.settings.autoLockIndex, 0))

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

/** 浏览选择新缓存目录（保持上限不变）。 */
async function browseCacheDir() {
  try {
    const dir = await Settings.ChooseCacheDir()
    if (!dir) return // 用户取消
    ui.settings.cachePath = dir
    await Settings.SetCache(cacheLimit.value, dir)
    showSuccess('缩略图缓存目录已更新')
  } catch (e) {
    onErr(e)
  }
}

/** 清除自定义缓存目录（回退系统临时目录）。 */
async function resetCacheDir() {
  ui.settings.cachePath = ''
  try {
    await Settings.SetCache(cacheLimit.value, '')
    showInfo('已恢复默认缓存目录（系统临时目录）')
  } catch (e) {
    onErr(e)
  }
}

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
  void App.Version().then((v) => (version.value = v)).catch(() => {})
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

function copyBaiduUrl() {
  void navigator.clipboard.writeText(baiduUrl.value).then(
    () => showInfo('授权网址已复制'),
    () => showWarning('复制失败，请手动选中网址'),
  )
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

/* ------------------------------------------------------------ 关于 */

const version = ref('读取运行时信息…')
</script>

<template>
  <div class="s-view">
    <!-- 工具行：页题（轨道样式复用 layout.css .cp-toolbar） -->
    <div class="cp-toolbar">
      <span class="page-title">
        <Icon name="setting" :size="16" />
        设置
        <span class="count">外观与偏好</span>
      </span>
    </div>

    <div class="s-scroll">
      <div class="s-inner">
        <!-- ===================== 外观 ===================== -->
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
        <div class="set-card">
          <span class="set-icon"><Icon name="history" :size="17" /></span>
          <div class="set-body">
            <div class="set-title">缓存大小上限</div>
            <div class="set-content">缩略图缓存占用磁盘的上限（64–4096 MB）</div>
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
          </div>
        </div>
        <div class="set-card">
          <span class="set-icon"><Icon name="folder" :size="17" /></span>
          <div class="set-body">
            <div class="set-title">缓存目录</div>
            <div class="set-content" :title="cachePath">
              {{ cachePathSet ? cachePath : '默认：系统临时目录（%TEMP% 下 cloudprism_cache）' }}
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
        <div class="hint-row">恢复码管理与文件夹同步位于「密库」页（连接密库后可见）。</div>

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
            <div class="set-content">文件加密云端保险库（WindowsGo 版：Wails v2 + WebView2）</div>
          </div>
        </div>
        <div class="about-note">{{ version }}</div>
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
}

.cp-toolbar {
  gap: 10px;
}

.page-title {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 0.929rem;
  font-weight: 600;
  color: var(--heading);
}

.count {
  padding: 1px 8px;
  font-size: 0.786rem;
  font-weight: 400;
  color: var(--text2);
  background: color-mix(in srgb, var(--text) 7%, transparent);
  border-radius: var(--radius-round);
}

/* 滚动内容区：单列居中，分组标题分隔 */
.s-scroll {
  flex: 1;
  overflow-y: auto;
  padding: 4px 24px 28px;
}

.s-inner {
  max-width: 860px;
  margin: 0 auto;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.group-title {
  margin: 16px 4px 4px;
  font-size: 0.857rem;
  font-weight: 600;
  color: var(--muted);
}

.group-title:first-child {
  margin-top: 12px;
}

/* 状态卡 ok 时图标底色偏绿（对照语义色） */
.set-card.ok .set-icon {
  color: var(--ok);
  background: color-mix(in srgb, var(--ok) 14%, transparent);
}

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

/* ---- 百度凭证表单卡 ---- */
.b-form {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 14px;
  background: var(--surface);
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

/* 授权 URL 兜底展示行 */
.bf-url {
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

.fade-enter-active,
.fade-leave-active {
  transition: opacity var(--dur) var(--ease);
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}
</style>
