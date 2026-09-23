<!--
  VaultsView.vue —— 密库页。
  对照 Python vault_info_page/quick_connect + main_window 引导页：
    · 未连接：欢迎引导 +「初始化向导」入口 + 最近连接密库列表
      （点卡片弹快速连接：按后端类型只需主密码 / 服务器密码+主密码，
      可切换恢复码开库）；卡片可移除（仅删本地记录）。
    · 已连接：信息卡（名称/位置/加密/连接时长/自动锁）+ 云端占用统计 +
      恢复码卡（重新生成→旧码失效）+ 文件夹同步卡 + 同位置其它密库切换。
  快照由 10Hz 帧驱动（ui.snap），无需本地定时器。
-->
<script setup lang="ts">
import {computed, onMounted, reactive, ref} from 'vue'
import {ui, openVault, navigate, lockVault, endOp, pickLocalDir} from '../lib/store'
import {Settings, Vault, unwrap} from '../lib/api'
import {fmtSize, fmtConnectSec} from '../lib/format'
import {showError, showInfo, showSuccess, showWarning} from '../lib/toast'
import Button from '../components/fluent/Button.vue'
import PrimaryButton from '../components/fluent/PrimaryButton.vue'
import Checkbox from '../components/fluent/Checkbox.vue'
import Icon from '../components/fluent/Icon.vue'
import Card from '../components/fluent/Card.vue'
import LineEdit from '../components/fluent/LineEdit.vue'
import MessageBox from '../components/fluent/MessageBox.vue'
import ProgressBar from '../components/fluent/ProgressBar.vue'
import PageHeader from '../components/layout/PageHeader.vue'
import ModalShell from '../components/layout/ModalShell.vue'
import InitWizard from './wizard/InitWizard.vue'
import RecoveryCodeDlg from './wizard/RecoveryCodeDlg.vue'

const connected = computed(() => !!ui.snap?.connected)
const snap = () => ui.snap!

/* ------------------------------------------------------------ 状态 */
const showWiz = ref(false)

/** 最近连接记录（本地副本：增删改后重拉）。 */
const recents = ref<Record<string, any>[]>([])

async function refreshRecents() {
  try {
    recents.value = (await Vault.RecentVaults()) ?? []
  } catch {
    recents.value = []
  }
}

onMounted(() => {
  // 未连接时拉取最近列表；已连接态由连接流程负责跳页，此页不展示 recents
  void refreshRecents()
})

function openWizard() {
  showWiz.value = true
}

function onWizClose() {
  showWiz.value = false
  void refreshRecents()
}

/* ------------------------------------------------- 最近密库卡片 */

/** 最近记录 → 后端类型图标（记录键 backend_type）。 */
function kindIcon(k: string): string {
  if (k === 'webdav') return 'globe'
  if (k === 'baidu') return 'cloud'
  return 'folder'
}

/** 卡片摘要行：位置 + 子目录 + 最近使用。 */
function recLocation(r: Record<string, any>): string {
  const path = String(r.path ?? '')
  const vp = String(r.vault_path ?? '')
  const parts = [path, vp].filter((s) => s && s !== '/')
  return parts.length ? parts.join(' · ') : '未知位置'
}

function recMeta(r: Record<string, any>): string {
  const t = String(r.backend_type ?? '')
  const user = String(r.webdav_user ?? '')
  const bname = t === 'local' ? '本地文件夹' : t === 'webdav' ? 'WebDAV' : '百度网盘'
  const last = String(r.last_used ?? '')
  const userTxt = user ? `（${user}）` : ''
  return `${bname}${userTxt} · 最近 ${last || '-'}`
}

async function forgetRecent(r: Record<string, any>) {
  try {
    await Vault.ForgetRecent(String(r.key ?? ''))
    showInfo('已移除最近记录（云端数据不受影响）')
    void refreshRecents()
  } catch (e) {
    showError('移除失败：' + unwrap(e).message)
  }
}

/* ---------------------------------------------- 快速连接（自绘对话框） */

const qc = reactive({
  open: false,
  record: null as Record<string, any> | null,
  webdavPass: '',
  master: '',
  useRecovery: false,
  recovery: '',
  busy: false,
  status: '',
})

function openQuickConnect(r: Record<string, any>) {
  qc.record = r
  qc.webdavPass = ''
  qc.master = ''
  qc.useRecovery = false
  qc.recovery = ''
  qc.busy = false
  qc.status = ''
  qc.open = true
}

async function doQuickConnect() {
  const r = qc.record
  if (!r || qc.busy) return
  // 记录类型白名单收窄（防止脏数据把非法 kind 传进 OpenRequest）
  const k = String(r.backend_type ?? '')
  const kind = k === 'webdav' || k === 'baidu' ? k : 'local'
  const webdav = kind === 'webdav'
  if (!qc.useRecovery && !qc.master) {
    qc.status = '请输入主密码'
    return
  }
  if (qc.useRecovery && !qc.recovery.trim()) {
    qc.status = '请粘贴恢复码'
    return
  }
  if (webdav && !qc.webdavPass) {
    qc.status = '请输入 WebDAV 服务器密码（不落盘）'
    return
  }
  qc.busy = true
  qc.status = ''
  try {
    await openVault({
      kind,
      localDir: kind === 'local' ? String(r.path ?? '') : '',
      url: webdav ? String(r.path ?? '') : '',
      user: webdav ? String(r.webdav_user ?? '') : '',
      pass: webdav ? qc.webdavPass : '',
      vaultPath: String(r.vault_path ?? ''),
      masterPassword: qc.useRecovery ? '' : qc.master,
      recoveryCode: qc.useRecovery ? qc.recovery.trim() : '',
      create: false,
    })
    qc.open = false // openVault 已切文件页
    void refreshRecents()
  } catch (e) {
    qc.status = unwrap(e).message
  } finally {
    qc.busy = false
    endOp() // 后端只发 progress：忙碌复位由调用方 finally 保证
  }
}

/* ----------------------------------------------------- 已连接功能区 */

/** 快速连接面板的副标题：库名 · 位置。
 *  原先这段表达式内联在模板里（且 Esc 是本页手写全局 keydown 兜的），
 *  现在浮层结构与键盘处理都归 ModalShell，这里只留纯展示。 */
const qcSub = computed(() => {
  const r = qc.record
  const name = String(r?.vault_name ?? r?.label ?? '密库')
  const where = String(r?.vault_path ?? '') || '根目录'
  return `${name} · ${where}`
})

// 自动锁文案（索引语义：0=从不 1/2/3=5/15/30 分钟）
const AUTOLOCK_LABELS = ['从不（不自动锁定）', '5 分钟', '15 分钟', '30 分钟']
const autoLockText = computed(() => {
  const idx = Number(ui.settings.autoLockIndex ?? 0)
  return AUTOLOCK_LABELS[idx] ?? AUTOLOCK_LABELS[0]
})

// 重命名密库（MessageBox input）
const renameDlg = reactive({open: false})
function onRenameConfirm(payload: string | boolean) {
  renameDlg.open = false
  const name = String(payload).trim()
  if (!name) return
  void (async () => {
    try {
      await Vault.RenameVault(name)
      showSuccess(`密库已更名为「${name}」`)
    } catch (e) {
      showError('重命名失败：' + unwrap(e).message)
    }
  })()
}

// 云端占用统计
const statBusy = ref(false)
async function requestStats() {
  if (statBusy.value) return
  statBusy.value = true
  try {
    await Vault.RequestStats()
  } catch (e) {
    showError('统计失败：' + unwrap(e).message)
  } finally {
    // 统计通常秒级完成；按钮加 1.2s 冷却防连点（后端另有 seq 竞态防护）
    window.setTimeout(() => (statBusy.value = false), 1200)
  }
}

// 恢复码重新生成（需当前主密码）
const regenDlg = reactive({open: false, err: ''})
const codeDlg = reactive({open: false, code: ''})

async function onRegenConfirm(payload: string | boolean) {
  const pw = String(payload)
  regenDlg.open = false
  if (!pw) return
  try {
    const code = await Vault.RegenerateRecoveryCode(pw)
    codeDlg.code = code
    codeDlg.open = true
    showSuccess('已生成新恢复码（旧码作废）')
  } catch (e) {
    const err = unwrap(e)
    // 密码错误重开密码框，其余 toast
    if (err.code === 'bad-password') {
      regenDlg.err = '主密码不正确，请重试'
      regenDlg.open = true
    } else {
      showError('重新生成失败：' + err.message)
    }
  } finally {
    endOp()
  }
}

// 文件夹同步：无同步目录时可在此就地选择（设置页亦有同入口）
const syncBusy = ref(false)
const syncDirBusy = ref(false)

/** 选择/更改本地同步目录（网页版选择器，选完即落盘）。 */
async function chooseSyncDir() {
  if (syncDirBusy.value) return
  syncDirBusy.value = true
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
    showError('设置同步目录失败：' + unwrap(e).message)
  } finally {
    syncDirBusy.value = false
  }
}

/** 前往设置页（自动锁定等偏好项在设置页集中管理）。 */
function gotoSettings() {
  navigate('settings')
}

async function startSync() {
  if (syncBusy.value) return
  if (!String(ui.settings.syncDir ?? '')) {
    showWarning('请先选择要同步的本地目录')
    return
  }
  syncBusy.value = true
  try {
    const n = await Settings.SyncNow()
    showInfo(n > 0 ? `已开始同步 ${n} 个文件` : '本地目录已是最新，无需同步')
  } catch (e) {
    const err = unwrap(e)
    if (err.code === 'sync-dir-unset') {
      showWarning('请先选择要同步的本地目录')
    } else {
      showError('同步启动失败：' + err.message)
    }
  } finally {
    syncBusy.value = false
  }
}

// 同步进度/结果摘要
const sync = computed(() => ui.snap?.sync ?? null)
const syncText = computed(() => {
  const s = sync.value
  if (!s) return ''
  if (s.running) return `正在同步 ${s.current || '…'}（${s.done}/${s.total}）`
  if (s.total === 0 && s.failed === 0 && s.synced === 0) return '尚未开始过同步'
  return `上次同步：成功 ${s.synced} 项，失败 ${s.failed} 项`
})

// 同一位置其它密库（展开即拉取）
const othersOpen = ref(false)
const others = ref<string[]>([])
const othersLoading = ref(false)
const otherConnDlg = reactive({open: false, path: ''})

async function toggleOthers() {
  othersOpen.value = !othersOpen.value
  if (othersOpen.value && !others.value.length) {
    othersLoading.value = true
    try {
      others.value = (await Vault.ListOtherVaults()) ?? []
    } catch (e) {
      showError('扫描其它密库失败：' + unwrap(e).message)
    } finally {
      othersLoading.value = false
    }
  }
}

function connectOther(path: string) {
  otherConnDlg.path = path
  otherConnDlg.open = true
}

async function onOtherConfirm(payload: string | boolean) {
  const pw = String(payload)
  otherConnDlg.open = false
  if (!pw) return
  try {
    await Vault.ConnectOtherVault(otherConnDlg.path, pw)
    // 换连成功：与 openVault 相同的浏览态重置（store 无此导出，本地等价处理）
    showSuccess('已切换到另一密库')
    navigate('files')
  } catch (e) {
    const err = unwrap(e)
    if (err.code === 'bad-password') {
      showError('主密码不正确')
    } else {
      showError('连接失败：' + err.message)
    }
  } finally {
    endOp() // ConnectOtherVault 亦只发 progress；成功后新连接由 onFrame 兜底接管
  }
}
</script>

<template>
  <div class="v-view">
    <!-- ======================= 未连接：欢迎 + 最近密库 ======================= -->
    <template v-if="!connected">
      <div class="welcome">
        <div class="hero">
          <span class="hero-ic">
            <svg class="hero-cloud" viewBox="0 0 2048 2048" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
              <path transform="translate(0, 300)" fill="currentColor" d="M412 1408 q-84 0 -159 -35 q-75 -35 -131.5 -93.5 q-56.5 -58.5 -89 -135 q-32.5 -76.5 -32.5 -159.5 q0 -85 29 -163 q29 -78 81.5 -138.5 q52.5 -60.5 125.5 -98 q73 -37.5 161 -41.5 l32 -1 q11 -123 59 -223 q48 -100 126 -171.5 q78 -71.5 182.5 -110 q104.5 -38.5 227.5 -38.5 q123 0 227.5 39 q104.5 39 182.5 110 q78 71 126 171 q48 100 59 223 l16 0 q87 0 162.5 35.5 q75.5 35.5 131 95.5 q55.5 60 87.5 138.5 q32 78.5 32 163.5 q0 85 -32 163 q-32 78 -87.5 138 q-55.5 60 -130.5 95.5 q-75 35.5 -162 35.5 l-1224 0 ZM1634 1280 q61 0 113.5 -25.5 q52.5 -25.5 90.5 -67.5 q38 -42 60 -97 q22 -55 22 -114 q0 -64 -23 -119.5 q-23 -55.5 -63.5 -97 q-40.5 -41.5 -95.5 -65 q-55 -23.5 -119 -23.5 q-52 0 -86 -32.5 q-34 -32.5 -42 -82.5 q-5 -36 -11.5 -70 q-6.5 -34 -19.5 -69 q-26 -72 -69.5 -126 q-43.5 -54 -100 -90.5 q-56.5 -36.5 -124 -54.5 q-67.5 -18 -142.5 -18 q-75 0 -142.5 18 q-67.5 18 -124 54 q-56.5 36 -100 90 q-43.5 54 -69.5 126 q-13 35 -19.5 69 q-6.5 34 -11.5 71 q-8 59 -46.5 87 q-38.5 28 -95.5 28 q-61 0 -113.5 25.5 q-52.5 25.5 -91 68 q-38.5 42.5 -60.5 97.5 q-22 55 -22 114 q0 59 22 114 q22 55 60 97 q38 42 90.5 67.5 q52.5 25.5 113.5 25.5 l1220 0 Z"/>
            </svg>
          </span>
          <h2>欢迎使用 CloudPrism</h2>
          <p>文件加密后存入本地文件夹或云盘；连接密库后即可浏览与播放，全程端到端解密。</p>
          <div class="hero-actions">
            <PrimaryButton icon="add" @click="openWizard">初始化向导</PrimaryButton>
            <Button icon="help" @click="showInfo('从「初始化向导」开始：新建密库或连接已有密库')">
              使用帮助
            </Button>
          </div>
        </div>

        <div v-if="recents.length" class="recent-sec">
          <div class="sec-head">
            <span class="sec-title">最近连接</span>
            <span class="sec-sub">点击卡片快速连接（仅需密码）</span>
          </div>
          <div class="recent-list">
            <Card v-for="(r, i) in recents" :key="String(r.key ?? i)" clickable padding="none">
              <div class="recent-row" @click="openQuickConnect(r)">
                <span class="rec-ic">
                  <Icon :name="kindIcon(String(r.backend_type ?? ''))" :size="18" />
                </span>
                <div class="rec-txt">
                  <div class="rec-name">
                    {{ r.vault_name || r.label || '未命名密库' }}
                    <span v-if="r.vault_path" class="rec-path">/{{ r.vault_path }}</span>
                  </div>
                  <div class="rec-meta">{{ recLocation(r) }}</div>
                  <div class="rec-meta">{{ recMeta(r) }}</div>
                </div>
                <button
                  type="button"
                  class="rec-x"
                  title="移除记录"
                  @click.stop="forgetRecent(r)"
                >
                  <Icon name="cancel" :size="12" />
                </button>
              </div>
            </Card>
          </div>
        </div>
      </div>
    </template>

    <!-- ======================= 已连接：密库信息（设置页同款单列卡） ======================= -->
    <template v-else>
      <!-- 统一页头（56px）：页面级动作（重命名 / 锁定）只出现在这里。
           未连接态刻意不带页头 —— 那是一个整屏引导页，身份由 hero 自己承担，
           与文件页未连接时只给一个居中空态是同一条处理。 -->
      <PageHeader title="密库" icon="cloud">
        <template #actions>
          <Button icon="edit" title="重命名当前密库" @click="renameDlg.open = true">重命名</Button>
          <PrimaryButton icon="lock" title="锁定密库（Ctrl+L）" @click="lockVault">锁定</PrimaryButton>
        </template>
      </PageHeader>

      <div class="v-conn">
        <div class="v-inner">
          <!-- 概览卡：图标 + 库名/后端。库名全页只此一处，故卡片保留；
               动作已按骨架约定上收页头，这里回归纯身份展示。 -->
          <section class="ov-card">
            <span class="ov-icon"><Icon name="cloud" :size="22" /></span>
            <div class="ov-body">
              <div class="ov-name">{{ snap().vaultName }}</div>
              <div class="ov-meta">{{ snap().backend }} · {{ snap().backendId }}</div>
            </div>
          </section>

          <!-- ============ 连接信息 ============ -->
          <div class="group-title">连接信息</div>
          <div class="set-card accent-icon">
            <span class="set-icon"><Icon name="globe" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">存储位置</div>
              <div class="set-content" :title="snap().backendId">{{ snap().backendId }}</div>
            </div>
            <div class="set-right"><span class="ch-badge">{{ snap().backend }}</span></div>
          </div>
          <div class="set-card accent-icon">
            <span class="set-icon"><Icon name="folder" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">密库路径</div>
              <div class="set-content">{{ snap().vaultPath || '根目录' }}</div>
            </div>
          </div>
          <div class="set-card accent-icon">
            <span class="set-icon"><Icon name="hide" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">文件名加密</div>
              <div class="set-content">
                创建密库时决定，不可中途修改。
                {{ snap().filenameEnc ? '已加密：云端仅见密文名。' : '未加密：云端可见明文文件名。' }}
              </div>
            </div>
            <div class="set-right">
              <span class="ch-badge" :class="snap().filenameEnc ? 'ok' : 'warn'">
                {{ snap().filenameEnc ? '已开启' : '未开启' }}
              </span>
            </div>
          </div>
          <div class="set-card accent-icon">
            <span class="set-icon"><Icon name="date_time" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">本次连接</div>
              <div class="set-content">自连接起已持续 {{ fmtConnectSec(snap().connectedSec) }}，锁定后重连需重新验证</div>
            </div>
          </div>
          <div class="set-card accent-icon">
            <span class="set-icon"><Icon name="stop_watch" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">自动锁定</div>
              <div class="set-content">无操作超过设定时间后自动锁定密库；修改在设置页进行</div>
            </div>
            <div class="set-right">
              <span class="set-value">{{ autoLockText }}</span>
              <Button icon="setting" title="前往设置页修改" @click="gotoSettings">去设置</Button>
            </div>
          </div>

          <!-- ============ 云端占用 ============ -->
          <div class="group-title">云端占用</div>
          <div class="set-card accent-icon">
            <span class="set-icon"><Icon name="pie_single" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">密库空间统计</div>
              <div class="set-content">
                <template v-if="snap().statsDone && !snap().statsFailed">
                  已用 <b class="em">{{ fmtSize(snap().statsTotal) }}</b>，共
                  <b class="em">{{ snap().statsFiles }}</b> 个文件
                </template>
                <template v-else-if="snap().statsFailed">
                  <span class="err">上一轮统计有部分文件核对失败（网络中断等），可重试。</span>
                </template>
                <template v-else>尚未统计：递归遍历全部文件，计数 + 云端占用字节，结果仅存本机展示</template>
              </div>
            </div>
            <div class="set-right">
              <Button icon="update" :disabled="statBusy" title="重新统计（遍历全部文件）" @click="requestStats">
                立即统计
              </Button>
            </div>
          </div>

          <!-- ============ 同步与安全 ============ -->
          <div class="group-title">同步与安全</div>
          <div class="set-card accent-icon">
            <span class="set-icon"><Icon name="sync" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">文件夹同步</div>
              <div class="set-content">
                <template v-if="String(ui.settings.syncDir ?? '')">
                  {{ String(ui.settings.syncDir) }}（本地 → 云端，单向增量）
                </template>
                <template v-else>尚未选择本地目录：先选择要同步的文件夹，再点「开始同步」</template>
              </div>
            </div>
            <div class="set-right">
              <Button
                icon="folder_add"
                :disabled="syncDirBusy"
                title="选择要同步到密库的本地目录"
                @click="chooseSyncDir"
              >
                {{ String(ui.settings.syncDir ?? '') ? '更改目录' : '选择目录…' }}
              </Button>
              <Button
                icon="sync"
                :disabled="syncBusy || !!sync?.running || !String(ui.settings.syncDir ?? '')"
                @click="startSync"
              >
                {{ sync?.running ? '同步中…' : '开始同步' }}
              </Button>
            </div>
          </div>
          <template v-if="sync?.running || syncText">
            <div v-if="sync?.running" class="sync-bar">
              <ProgressBar
                :value="sync.total > 0 ? Math.round((sync.done / sync.total) * 100) : 0"
                :indeterminate="sync.total <= 0"
              />
            </div>
            <div v-if="syncText && !sync?.running" class="card-note">{{ syncText }}</div>
            <ul v-if="sync?.errors?.length" class="sync-errs">
              <li v-for="(e, i) in sync.errors" :key="i">{{ e }}</li>
            </ul>
          </template>

          <div class="set-card accent-icon">
            <span class="set-icon"><Icon name="qrcode" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">恢复码</div>
              <div class="set-content">
                忘记主密码时可凭恢复码开库；恢复码仅加密保存在本机库内，重新生成后旧码立即失效。
              </div>
            </div>
            <div class="set-right">
              <span class="ch-badge" :class="snap().hasRecovery ? 'ok' : 'warn'">
                {{ snap().hasRecovery ? '已生成' : '未生成' }}
              </span>
              <Button icon="update" @click="regenDlg.err = ''; regenDlg.open = true">重新生成</Button>
            </div>
          </div>

          <!-- ============ 其它密库 ============ -->
          <div class="group-title">其它密库</div>
          <div class="set-card accent-icon">
            <span class="set-icon"><Icon name="library" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">同一位置的其它密库</div>
              <div class="set-content" v-if="!othersOpen">展开查看存储在同一位置的其它密库，可一键切换</div>
              <div class="set-content" v-else-if="othersLoading">扫描中…</div>
              <div class="set-content" v-else-if="!others.length">未发现其它密库（可在别的目录位置新建后再来切换）</div>
            </div>
            <div class="set-right">
              <button type="button" class="expand-btn" @click="toggleOthers">
                {{ othersOpen ? '收起' : '展开' }}
                <Icon :name="othersOpen ? 'chevron_down_med' : 'chevron_right_med'" :size="12" />
              </button>
            </div>
          </div>
          <div v-if="othersOpen && !othersLoading && others.length" class="other-list">
            <div v-for="p in others" :key="p" class="other-row">
              <Icon name="library" :size="15" class="oth-ic" />
              <span class="oth-path">{{ p || '（根目录）' }}</span>
              <Button iconOnly icon="connect" title="连接该密库" @click="connectOther(p)" />
            </div>
          </div>
        </div>
      </div>
    </template>

    <!-- ======================= 模态区 ======================= -->
    <InitWizard v-if="showWiz" @close="onWizClose" />

    <!-- 快速连接对话框（未连接态）：浮层结构走 ModalShell，
         Esc 由它按栈处理 —— 原先本页手写的全局 keydown 已删（含「忙碌中不响应」
         与「上层压着向导/重命名时不响应」两条守卫：前者由 :esc 承担，
         后者由模态栈的「只有最上层响应」天然满足）。 -->
    <ModalShell
      :open="qc.open"
      title="快速连接"
      :sub="qcSub"
      icon="connect"
      :width="440"
      closable
      :esc="!qc.busy"
      autofocus=".cp-line-edit input"
      @close="qc.open = false"
    >
      <div v-if="String(qc.record?.backend_type ?? '') === 'webdav'" class="qc-fld">
        <label>WebDAV 服务器密码（{{ qc.record?.webdav_user || '账号' }}，不落盘）</label>
        <LineEdit v-model="qc.webdavPass" password placeholder="服务器密码（应用专用密码）" />
      </div>

      <Checkbox v-model="qc.useRecovery" class="chk">
        忘记主密码？改用恢复码开库
      </Checkbox>

      <div v-if="!qc.useRecovery" class="qc-fld">
        <label>主密码</label>
        <LineEdit
          v-model="qc.master"
          password
          placeholder="主密码（仅本次驻内存使用）"
          @enter="doQuickConnect"
        />
      </div>
      <div v-else class="qc-fld">
        <label>恢复码</label>
        <LineEdit
          v-model="qc.recovery"
          clearable
          placeholder="XXXX-XXXX-XXXX-XXXX"
          @enter="doQuickConnect"
        />
      </div>

      <div v-if="qc.status" class="qc-status">{{ qc.status }}</div>

      <template #actions>
        <Button :disabled="qc.busy" @click="qc.open = false">取消</Button>
        <PrimaryButton icon="connect" :disabled="qc.busy" @click="doQuickConnect">
          {{ qc.busy ? '连接中…' : '连接' }}
        </PrimaryButton>
      </template>
    </ModalShell>

    <!-- 重命名 / 重新生成恢复码 / 其它密库密码 -->
    <MessageBox
      :open="renameDlg.open"
      title="重命名密库"
      content="仅修改展示名称，不影响加密密钥与云端数据。"
      input-label="新名称"
      :initial="ui.snap?.vaultName ?? ''"
      @confirm="onRenameConfirm"
      @cancel="renameDlg.open = false"
    />
    <MessageBox
      :open="regenDlg.open"
      title="重新生成恢复码"
      :content="regenDlg.err || '需验证当前主密码；生成后旧恢复码立即失效。'"
      input-label="主密码"
      password
      confirm-text="生成"
      @confirm="onRegenConfirm"
      @cancel="regenDlg.open = false"
    />
    <MessageBox
      :open="otherConnDlg.open"
      title="连接其它密库"
      :content="'将切换到「' + (otherConnDlg.path || '根目录') + '」位置的密库。'"
      input-label="该密库主密码"
      password
      confirm-text="连接"
      @confirm="onOtherConfirm"
      @cancel="otherConnDlg.open = false"
    />

    <!-- 新恢复码展示（一次） -->
    <RecoveryCodeDlg :open="codeDlg.open" :code="codeDlg.code" @close="codeDlg.open = false" />
  </div>
</template>

<style scoped>
.v-view {
  /* 列布局：统一页头（56px）浮在顶，未连接/已连接两种状态各自滚动——
     此前本容器自己 overflow-y:auto，页头会跟着内容一起滚走。 */
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  /* 浮层页头的定位上下文（未连接态无页头，仅已连接态用到） */
  position: relative;
}

/* ---------- 未连接：欢迎区 ---------- */
.welcome {
  display: flex;
  flex-direction: column;
  align-items: center;
  /* 未连接态没有页头，本容器即滚动区 */
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  gap: 28px;
  padding: 48px 24px 32px;
}

.hero {
  display: flex;
  flex-direction: column;
  align-items: center;
  max-width: 520px;
  text-align: center;
}

.hero-ic {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 84px;
  height: 84px;
  margin-bottom: 14px;
  color: var(--accent);
  background: var(--accent-soft);
  border-radius: 22px;
}

/* hero 云朵：内联 fluent 实心 svg（绕开 Icon 组件，浏览器下 100% 可控） */
.hero-cloud {
  width: 56px;
  height: 56px;
  overflow: visible;
}

.hero h2 {
  margin: 0 0 8px;
  font-size: 1.429rem;
  font-weight: 600;
  color: var(--heading);
}

.hero p {
  margin: 0 0 18px;
  font-size: 0.857rem;
  line-height: 1.6;
  color: var(--text2);
}

.hero-actions {
  display: flex;
  gap: 10px;
}

.recent-sec {
  width: min(680px, 100%);
}

.sec-head {
  margin-bottom: 10px;
}

.sec-title {
  font-size: 0.929rem;
  font-weight: 600;
  color: var(--heading);
}

.sec-sub {
  margin-left: 10px;
  font-size: 0.786rem;
  color: var(--muted);
}

.recent-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.recent-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  cursor: pointer;
}

.recent-row:hover {
  background: color-mix(in srgb, var(--text) 3%, transparent);
}

.rec-ic {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 38px;
  height: 38px;
  color: var(--accent);
  background: var(--accent-soft);
  border-radius: var(--radius-ctrl);
}

.rec-txt {
  flex: 1;
  min-width: 0;
}

.rec-name {
  overflow: hidden;
  font-size: 0.929rem;
  font-weight: 500;
  color: var(--heading);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rec-path {
  font-weight: 400;
  color: var(--muted);
}

.rec-meta {
  overflow: hidden;
  font-size: 0.786rem;
  color: var(--muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rec-x {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 28px;
  height: 28px;
  color: var(--muted);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  opacity: 0;
  transition: opacity var(--dur-fast) var(--ease), background var(--dur-fast) var(--ease);
}

.recent-row:hover .rec-x {
  opacity: 1;
}

.rec-x:hover {
  background: color-mix(in srgb, var(--text) 8%, transparent);
}

/* ---------- 已连接：设置页同款单列卡片 ---------- */
.v-conn {
  /* 滚动区从 y=0 起（页头是浮层，不再占流），padding-top 让位 ——
    内容从玻璃页头底下穿过（scroll-under）；占满剩余高度。 */
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: calc(var(--page-head-h) + 18px) 24px 28px;
}

/* 单列内容列宽：与设置页统一为 860px（此前本页 760 / 设置页 860，切换页面时
   中间列会左右跳动） */
.v-inner {
  max-width: 860px;
  margin: 0 auto;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

/* 概览卡：图标 + 名称/后端（动作已按骨架约定上收页头，卡内不再放按钮）。
   v1.01：半透玻璃卡面（无模糊），环境光透过微微上色。 */
.ov-card {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 6px;
  padding: 18px 16px;
  background: linear-gradient(120deg, color-mix(in srgb, var(--accent) 7%, var(--glass-card)), var(--glass-card));
  border: 1px solid var(--stroke-card);
  border-radius: var(--radius-card);
  box-shadow: var(--shadow-card);
}

.ov-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 48px;
  height: 48px;
  color: var(--accent);
  background: var(--accent-soft);
  border-radius: 12px;
}

.ov-body {
  flex: 1;
  min-width: 0;
}

.ov-name {
  overflow: hidden;
  font-size: 1.143rem;
  font-weight: 600;
  color: var(--heading);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ov-meta {
  overflow: hidden;
  margin-top: 2px;
  font-size: 0.786rem;
  color: var(--muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* .ov-actions 已随「动作上收页头」移除（重命名/锁定现居 PageHeader #actions） */

/* 分组标题、设置卡骨架（.group-title / .set-card / .set-icon / .set-body /
   .set-title / .set-content / .set-right）**已全部收敛到 styles/components.css**，
   此处不再重复定义。历史问题：本页曾整份复制一份「accent 图标 + 阴影」变体，
   因 scoped 选择器带 [data-v-*]、特异性高于全局同名类，导致全局窄屏规则对本页
   失效（同一套骨架两处维护、改一处漏一处）。现在本页只用全局骨架 + 两个语义
   变体类（模板上的 .accent-icon 与全局的 .set-card.ok），字号也随之与设置页统一
   （标题 14px / 说明 12px）。
   下面只保留仅本页使用、无需上收的补充规则。 */

/* 说明行里的强调片段（当前值 / 错误态） */
.set-content .em {
  color: var(--heading);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.set-content .err {
  color: var(--err);
}

.set-value {
  font-size: 0.857rem;
  color: var(--text);
  white-space: nowrap;
}

/* 徽章（ok/warn 语义；无修饰类时用中性底展示后端类型等） */
.ch-badge {
  padding: 2px 10px;
  font-size: 0.786rem;
  color: var(--text2);
  background: color-mix(in srgb, var(--text) 7%, transparent);
  border-radius: var(--radius-round);
  white-space: nowrap;
}

.ch-badge.ok {
  color: var(--ok);
  background: color-mix(in srgb, var(--ok) 12%, transparent);
}

.ch-badge.warn {
  color: var(--warn);
  background: color-mix(in srgb, var(--warn) 12%, transparent);
}

/* 同步进度/备注/错误（嵌在卡片下方；与 set-card 内容 16px 左缘对齐） */
.sync-bar {
  margin: 4px 16px 8px;
}

.card-note {
  margin: 0 16px;
  padding: 0 0 4px;
  font-size: 0.786rem;
  line-height: 1.5;
  color: var(--muted);
}

.card-note.err {
  color: var(--err);
}

.sync-errs {
  margin: 0 16px;
  padding: 0 0 8px 24px;
  font-size: 0.786rem;
  color: var(--err);
}

/* 展开/收起钮（文字链接风） */
.expand-btn {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 4px 8px;
  font-family: inherit;
  font-size: 0.786rem;
  color: var(--accent);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
  cursor: pointer;
}

.expand-btn:hover {
  background: color-mix(in srgb, var(--accent) 10%, transparent);
}

/* 其它密库列表（v1.01 注：凹面井 —— 刻意不透、保持 --bg-page，
   与设置页 .expand-items / .mini-radio 同族「嵌入面」，不上玻璃） */
.other-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin: 0 4px 8px;
  padding: 4px;
  background: var(--bg-page);
  border-radius: var(--radius-ctrl);
}

.other-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 6px;
  border-radius: var(--radius-ctrl);
}

.other-row:hover {
  background: color-mix(in srgb, var(--text) 5%, transparent);
}

.oth-ic {
  color: var(--muted);
}

.oth-path {
  flex: 1;
  overflow: hidden;
  font-size: 0.857rem;
  color: var(--text);
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ---------- 快速连接对话框 ---------- */
/* 快速连接的遮罩 / 面板 / 页头 / 动作行已收进 ModalShell（原先 mask/panel/head/
   title/sub/x/actions 七条规则只服务这一个浮层）。本页只剩表单自身的节奏。 */
.qc-fld {
  margin: 10px 0;
}

.qc-fld label {
  display: block;
  margin-bottom: 6px;
  font-size: 0.786rem;
  color: var(--text);
}

/* 只负责复选框在表单里的垂直节奏；外观（尺寸/勾选态/字号）全部由 Checkbox 组件负责，
   此处不要再写 display/gap/font-size —— 会与组件内 .cp-chk 的规则同权重打架 */
.chk {
  margin-top: 4px;
}

.qc-status {
  margin-top: 10px;
  padding: 8px 10px;
  font-size: 0.786rem;
  line-height: 1.4;
  color: var(--err);
  background: color-mix(in srgb, var(--err) 8%, transparent);
  border-radius: var(--radius-ctrl);
}

/* .fade-* 过渡基元已在 styles/base.css 全局定义，此处删除重复副本。 */

/* ============================================================ 响应式
 * 手机（≤640px）适配。
 *
 * 注意：本页的 .set-card 是「accent 图标 + 阴影」变体，在本组件 scoped 块
 * 内重新定义过（见上方 911 行起）。scoped 选择器带 [data-v-*] 属性，特异性
 * 高于 styles/components.css 里的同名全局类 —— **全局那份窄屏规则对本页
 * 无效**，所以这里必须再写一份；将来改设置卡骨架，两处都要动。
 *
 * 挤压根因：.set-body / .ov-body 是 flex:1 + min-width:0，可以一路收缩到 0，
 * 而 .set-right 是 flex:none 不参与收缩。右侧一旦是「数值 + 按钮」这类组合
 * （自动锁定卡的"从不（不自动锁定）+ 去设置"），文本列就被压成一行一个字。
 * 解法：右侧整体换到第二行并右对齐。
 *
 * 概览卡的窄屏动作换行已随「动作上收页头」消失：重命名/锁定现在由
 * PageHeader 的 .ph-actions 统一处理（≤640px 时横向滑动），本页不必再管。
 * ============================================================ */

@media (max-width: 640px) {
  /* 设置卡的窄屏换行（.set-card/.set-body/.set-right）已在 styles/components.css
     统一处理，此处不再重复 —— 重复的 scoped 副本特异性更高，会让全局规则形同虚设。 */

  /* 概览卡（本页独有结构）：图标列固定在左，文本列给一个下限，避免缩到零宽 */
  .ov-card {
    flex-wrap: wrap;
    align-items: flex-start;
    gap: 10px 12px;
    padding: 14px 12px;
  }

  .ov-body {
    min-width: 120px;
  }
}

</style>
