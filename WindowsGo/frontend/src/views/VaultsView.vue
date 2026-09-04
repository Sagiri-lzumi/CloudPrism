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
import {ui, openVault, navigate, lockVault, endOp} from '../lib/store'
import {Settings, Vault, unwrap} from '../lib/api'
import {fmtSize, fmtConnectSec} from '../lib/format'
import {showError, showInfo, showSuccess, showWarning} from '../lib/toast'
import Button from '../components/fluent/Button.vue'
import PrimaryButton from '../components/fluent/PrimaryButton.vue'
import Icon from '../components/fluent/Icon.vue'
import Card from '../components/fluent/Card.vue'
import LineEdit from '../components/fluent/LineEdit.vue'
import MessageBox from '../components/fluent/MessageBox.vue'
import ProgressBar from '../components/fluent/ProgressBar.vue'
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
  void refreshRecents()
  // 锁库事件（st:locked）后回到本页时刷新最近列表
  if (!connected.value) void refreshRecents()
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

// 自动锁文案（索引语义：0=从不 1/2/3=5/15/30 分钟）
const AUTOLOCK_LABELS = ['从不（不自动锁定）', '5 分钟', '15 分钟', '30 分钟']
const autoLockText = computed(() => {
  const idx = Number(ui.settings.autoLockIndex ?? 0)
  return AUTOLOCK_LABELS[idx] ?? AUTOLOCK_LABELS[0]
})

// 重命名密库（MessageBox input）
const renameDlg = reactive({open: false})
function onRenameConfirm(payload: string | boolean) {
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

/** 选择/更改本地同步目录（弹目录框，选完即落盘）。 */
async function chooseSyncDir() {
  if (syncDirBusy.value) return
  syncDirBusy.value = true
  try {
    const dir = await Settings.ChooseSyncDir()
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
const syncText = computed(() => {
  const s = ui.snap?.sync
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
          <span class="hero-ic"><Icon name="cloud" :size="44" /></span>
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
      <div class="v-conn">
        <div class="v-inner">
          <!-- 概览卡：图标 + 库名/后端 + 快捷操作（锁定/重命名） -->
          <section class="ov-card">
            <span class="ov-icon"><Icon name="cloud" :size="22" /></span>
            <div class="ov-body">
              <div class="ov-name">{{ snap().vaultName }}</div>
              <div class="ov-meta">{{ snap().backend }} · {{ snap().backendId }}</div>
            </div>
            <div class="ov-actions">
              <Button icon="edit" @click="renameDlg.open = true">重命名</Button>
              <PrimaryButton icon="lock" title="锁定密库（Ctrl+L）" @click="lockVault">锁定</PrimaryButton>
            </div>
          </section>

          <!-- ============ 连接信息 ============ -->
          <div class="group-title">连接信息</div>
          <div class="set-card">
            <span class="set-icon"><Icon name="globe" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">存储位置</div>
              <div class="set-content" :title="snap().backendId">{{ snap().backendId }}</div>
            </div>
            <div class="set-right"><span class="ch-badge">{{ snap().backend }}</span></div>
          </div>
          <div class="set-card">
            <span class="set-icon"><Icon name="folder" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">密库路径</div>
              <div class="set-content">{{ snap().vaultPath || '根目录' }}</div>
            </div>
          </div>
          <div class="set-card">
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
          <div class="set-card">
            <span class="set-icon"><Icon name="date_time" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">本次连接</div>
              <div class="set-content">自连接起已持续 {{ fmtConnectSec(snap().connectedSec) }}，锁定后重连需重新验证</div>
            </div>
          </div>
          <div class="set-card">
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
          <div class="set-card">
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
          <div class="set-card">
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
                :disabled="syncBusy || !!ui.snap?.sync.running || !String(ui.settings.syncDir ?? '')"
                @click="startSync"
              >
                {{ ui.snap?.sync.running ? '同步中…' : '开始同步' }}
              </Button>
            </div>
          </div>
          <template v-if="ui.snap?.sync.running || syncText">
            <div v-if="ui.snap?.sync.running" class="sync-bar">
              <ProgressBar
                :value="ui.snap.sync.total > 0 ? Math.round((ui.snap.sync.done / ui.snap.sync.total) * 100) : 0"
                :indeterminate="ui.snap.sync.total <= 0"
              />
            </div>
            <div v-if="syncText && !ui.snap?.sync.running" class="card-note">{{ syncText }}</div>
            <ul v-if="ui.snap?.sync.errors?.length" class="sync-errs">
              <li v-for="(e, i) in ui.snap.sync.errors" :key="i">{{ e }}</li>
            </ul>
          </template>

          <div class="set-card">
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
          <div class="set-card">
            <span class="set-icon"><Icon name="library" :size="17" /></span>
            <div class="set-body">
              <div class="set-title">同一位置的其它密库</div>
              <div class="set-content" v-if="othersOpen">
                <template v-if="othersLoading">扫描中…</template>
                <template v-else-if="!others.length">未发现其它密库（可在别的目录位置新建后再来切换）</template>
              </div>
            </div>
            <div class="set-right">
              <button type="button" class="expand-btn" @click="toggleOthers">
                {{ othersOpen ? '收起' : '展开' }}
                <Icon name="chevron_down_med" :size="12" :class="{rot: othersOpen}" />
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

    <!-- 快速连接对话框（未连接态） -->
    <Teleport to="body">
      <Transition name="fade">
        <div v-if="qc.open" class="qc-mask">
          <div class="qc-panel" role="dialog" aria-label="快速连接">
            <div class="qc-head">
              <Icon name="connect" :size="18" class="qc-ic" />
              <div>
                <div class="qc-title">快速连接</div>
                <div class="qc-sub">
                  {{ qc.record?.vault_name || qc.record?.label || '密库' }} ·{{
                    String(qc.record?.vault_path ?? '') || '根目录'
                  }}
                </div>
              </div>
              <button type="button" class="qc-x" title="取消" @click="qc.open = false">
                <Icon name="cancel" :size="14" />
              </button>
            </div>

            <div v-if="String(qc.record?.backend_type ?? '') === 'webdav'" class="qc-fld">
              <label>WebDAV 服务器密码（{{ qc.record?.webdav_user || '账号' }}，不落盘）</label>
              <LineEdit v-model="qc.webdavPass" password placeholder="服务器密码（应用专用密码）" />
            </div>

            <label class="chk">
              <input v-model="qc.useRecovery" type="checkbox" />
              忘记主密码？改用恢复码开库
            </label>

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

            <div class="qc-actions">
              <Button :disabled="qc.busy" @click="qc.open = false">取消</Button>
              <PrimaryButton icon="connect" :disabled="qc.busy" @click="doQuickConnect">
                {{ qc.busy ? '连接中…' : '连接' }}
              </PrimaryButton>
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>

    <!-- 重命名 / 重新生成恢复码 / 其它密库密码 -->
    <MessageBox
      :open="renameDlg.open"
      title="重命名密库"
      content="仅修改展示名称，不影响加密密钥与云端数据。"
      input-label="新名称"
      :initial="snap().vaultName"
      @confirm="renameDlg.open = false; onRenameConfirm"
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
  height: 100%;
  overflow-y: auto;
}

/* ---------- 未连接：欢迎区 ---------- */
.welcome {
  display: flex;
  flex-direction: column;
  align-items: center;
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
  height: 100%;
  overflow-y: auto;
  padding: 18px 24px 28px;
}

.v-inner {
  max-width: 760px;
  margin: 0 auto;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

/* 概览卡：图标 + 名称/后端 + 快捷操作 */
.ov-card {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 6px;
  padding: 18px 16px;
  background: linear-gradient(120deg, color-mix(in srgb, var(--accent) 7%, var(--surface)), var(--surface));
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

.ov-actions {
  display: flex;
  gap: 8px;
  flex: none;
}

/* 分组标题（对照 SettingsView 同款） */
.group-title {
  margin: 14px 4px 2px;
  font-size: 0.857rem;
  font-weight: 600;
  color: var(--muted);
}

.group-title:first-child {
  margin-top: 0;
}

/* 设置卡（对照 SettingsView .set-card：图标+标题+说明+右侧控件） */
.set-card {
  display: flex;
  align-items: center;
  gap: 14px;
  min-height: 56px;
  padding: 10px 16px;
  background: var(--surface);
  border: 1px solid var(--stroke-card);
  border-radius: var(--radius-card);
  box-shadow: var(--shadow-card);
  transition: border-color var(--dur-fast) var(--ease);
}

.set-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 34px;
  height: 34px;
  color: var(--accent);
  background: var(--accent-soft);
  border-radius: 8px;
}

.set-body {
  flex: 1;
  min-width: 0;
}

.set-title {
  font-size: 0.857rem;
  font-weight: 600;
  color: var(--heading);
}

.set-content {
  margin-top: 2px;
  overflow: hidden;
  font-size: 0.786rem;
  line-height: 1.45;
  color: var(--text2);
  text-overflow: ellipsis;
  overflow-wrap: anywhere;
}

.set-content .em {
  color: var(--heading);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.set-content .err {
  color: var(--err);
}

.set-right {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
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

/* 同步进度/备注/错误（嵌在卡片下方） */
.sync-bar {
  margin: 4px 0 8px;
}

.card-note {
  margin: 0;
  padding: 0 4px 4px;
  font-size: 0.786rem;
  line-height: 1.5;
  color: var(--muted);
}

.card-note.err {
  color: var(--err);
}

.sync-errs {
  margin: 0;
  padding: 0 4px 8px 24px;
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

.expand-btn .rot {
  transform: rotate(180deg);
}

.expand-btn svg {
  transition: transform var(--dur-fast) var(--ease);
}

/* 其它密库列表 */
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
.qc-mask {
  position: fixed;
  inset: 0;
  z-index: 1650;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.4);
}

.qc-panel {
  width: min(440px, calc(100vw - 96px));
  padding: 20px;
  background: var(--surface);
  border-radius: var(--radius-card);
  box-shadow: var(--shadow-pop);
}

.qc-head {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin-bottom: 14px;
}

.qc-ic {
  flex: none;
  margin-top: 2px;
  color: var(--accent);
}

.qc-head > div {
  flex: 1;
  min-width: 0;
}

.qc-title {
  font-size: 1rem;
  font-weight: 600;
  color: var(--heading);
}

.qc-sub {
  overflow: hidden;
  font-size: 0.786rem;
  color: var(--muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.qc-x {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 26px;
  height: 26px;
  color: var(--muted);
  background: transparent;
  border: none;
  border-radius: var(--radius-ctrl);
}

.qc-x:hover {
  background: color-mix(in srgb, var(--text) 8%, transparent);
}

.qc-fld {
  margin: 10px 0;
}

.qc-fld label {
  display: block;
  margin-bottom: 6px;
  font-size: 0.786rem;
  color: var(--text);
}

.chk {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 4px;
  font-size: 0.857rem;
  color: var(--text);
  cursor: pointer;
  user-select: none;
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

.qc-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
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
