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

// 文件夹同步：无同步目录时引导去设置页
const syncBusy = ref(false)
async function startSync() {
  if (syncBusy.value) return
  if (!String(ui.settings.syncDir ?? '')) {
    showWarning('请先在设置页指定要同步的本地目录')
    navigate('settings')
    return
  }
  syncBusy.value = true
  try {
    const n = await Settings.SyncNow()
    showInfo(n > 0 ? `已开始同步 ${n} 个文件` : '本地目录已是最新，无需同步')
  } catch (e) {
    const err = unwrap(e)
    if (err.code === 'sync-dir-unset') {
      showWarning('请先在设置页指定要同步的本地目录')
      navigate('settings')
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

    <!-- ======================= 已连接：密库信息 ======================= -->
    <template v-else>
      <div class="v-cards">
        <!-- 左列 -->
        <div class="col">
          <!-- 连接信息 -->
          <Card padding="md" class="info-card">
            <div class="ic-head">
              <span class="ic-badge"><Icon name="certificate" :size="20" /></span>
              <div class="ic-titles">
                <div class="ic-name">{{ snap().vaultName }}</div>
                <div class="ic-sub">{{ snap().backend }} · {{ snap().backendId }}</div>
              </div>
              <PrimaryButton icon="lock" title="锁定密库（Ctrl+L）" @click="lockVault">
                锁定
              </PrimaryButton>
            </div>
            <div class="kv-grid">
              <span class="kv-k">密库名称</span>
              <span class="kv-v">{{ snap().vaultName }}</span>
              <span class="kv-k">存储位置</span>
              <span class="kv-v" :title="snap().backendId">{{ snap().backendId }}</span>
              <span class="kv-k">密库路径</span>
              <span class="kv-v">{{ snap().vaultPath || '根目录' }}</span>
              <span class="kv-k">文件名加密</span>
              <span class="kv-v">{{ snap().filenameEnc ? '开启（文件名不可见）' : '关闭（云端可见）' }}</span>
              <span class="kv-k">已连接</span>
              <span class="kv-v">{{ fmtConnectSec(snap().connectedSec) }}</span>
              <span class="kv-k">自动锁定</span>
              <span class="kv-v">{{ autoLockText }}</span>
            </div>
            <div class="ic-actions">
              <Button icon="edit" @click="renameDlg.open = true">重命名密库</Button>
            </div>
          </Card>

          <!-- 云端占用 -->
          <Card padding="md">
            <div class="card-head">
              <span class="ch-title">云端占用</span>
              <Button
                icon="update"
                :disabled="statBusy"
                title="重新统计（遍历全部文件）"
                @click="requestStats"
              >
                立即统计
              </Button>
            </div>
            <div v-if="snap().statsDone && !snap().statsFailed" class="stats-row">
              <div class="stat-cell">
                <b>{{ fmtSize(snap().statsTotal) }}</b>
                <i>占用空间</i>
              </div>
              <div class="stat-cell">
                <b>{{ snap().statsFiles }}</b>
                <i>文件数</i>
              </div>
            </div>
            <div v-else-if="snap().statsFailed" class="card-note err">
              上一轮完整性统计有部分文件核对失败（网络中断等），可重试。
            </div>
            <div v-else class="card-note">
              尚未统计。统计会递归遍历密库全部文件（计数 + 云端占用字节），结果仅存本机展示。
            </div>
          </Card>
        </div>

        <!-- 右列 -->
        <div class="col">
          <!-- 文件夹同步 -->
          <Card padding="md">
            <div class="card-head">
              <span class="ch-title">文件夹同步</span>
              <Button icon="sync" :disabled="syncBusy || !!ui.snap?.sync.running" @click="startSync">
                {{ ui.snap?.sync.running ? '同步中…' : '开始同步' }}
              </Button>
            </div>
            <div class="kv-grid">
              <span class="kv-k">本地目录</span>
              <span class="kv-v" :title="String(ui.settings.syncDir ?? '')">
                {{ String(ui.settings.syncDir ?? '') || '未设置（点击右上角前往设置）' }}
              </span>
              <span class="kv-k">方向</span>
              <span class="kv-v">本地 → 云端（单向增量）</span>
            </div>
            <template v-if="ui.snap?.sync.running">
              <div class="sync-bar">
                <ProgressBar
                  :value="ui.snap.sync.total > 0 ? Math.round((ui.snap.sync.done / ui.snap.sync.total) * 100) : 0"
                  :indeterminate="ui.snap.sync.total <= 0"
                />
              </div>
              <div class="card-note">{{ syncText }}</div>
            </template>
            <template v-else>
              <div class="card-note">{{ syncText }}</div>
              <ul v-if="ui.snap?.sync.errors?.length" class="sync-errs">
                <li v-for="(e, i) in ui.snap.sync.errors" :key="i">{{ e }}</li>
              </ul>
            </template>
          </Card>

          <!-- 恢复码与安全 -->
          <Card padding="md">
            <div class="card-head">
              <span class="ch-title">恢复码与安全</span>
              <span class="ch-badge" :class="snap().hasRecovery ? 'ok' : 'warn'">
                {{ snap().hasRecovery ? '已生成' : '未生成' }}
              </span>
            </div>
            <p class="card-note">
              忘记主密码时可凭恢复码开库。恢复码只在本机加密保存的库内，不会上传云端；
              重新生成后旧码立即失效。
            </p>
            <div class="ic-actions">
              <Button icon="update" @click="regenDlg.err = ''; regenDlg.open = true">
                重新生成恢复码
              </Button>
            </div>
          </Card>

          <!-- 同一位置的其它密库 -->
          <Card padding="md">
            <button type="button" class="card-head expandable" @click="toggleOthers">
              <span class="ch-title">同一位置的其它密库</span>
              <Icon
                name="chevron_down_med"
                :size="14"
                class="chev"
                :class="{open: othersOpen}"
              />
            </button>
            <template v-if="othersOpen">
              <div v-if="othersLoading" class="card-note">扫描中…</div>
              <div v-else-if="!others.length" class="card-note">
                未发现其它密库（可在别的目录位置新建后再来切换）
              </div>
              <div v-else class="other-list">
                <div v-for="p in others" :key="p" class="other-row">
                  <Icon name="library" :size="15" class="oth-ic" />
                  <span class="oth-path">{{ p || '（根目录）' }}</span>
                  <Button iconOnly icon="connect" title="连接该密库" @click="connectOther(p)" />
                </div>
              </div>
            </template>
          </Card>
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

/* ---------- 已连接：双列卡片 ---------- */
.v-cards {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 18px;
  align-items: start;
  padding: 18px;
}

.col {
  display: flex;
  flex-direction: column;
  gap: 18px;
  min-width: 0;
}

.info-card .ic-head {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 16px;
}

.ic-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex: none;
  width: 44px;
  height: 44px;
  color: var(--accent);
  background: var(--accent-soft);
  border-radius: 12px;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.04);
}

.ic-titles {
  flex: 1;
  min-width: 0;
}

.ic-name {
  overflow: hidden;
  font-size: 1.071rem;
  font-weight: 600;
  color: var(--heading);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ic-sub {
  overflow: hidden;
  font-size: 0.786rem;
  color: var(--muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kv-grid {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  gap: 8px 20px;
  align-items: baseline;
}

.kv-k {
  font-size: 0.786rem;
  color: var(--muted);
  white-space: nowrap;
  align-self: baseline;
}

.kv-v {
  overflow: hidden;
  font-size: 0.857rem;
  color: var(--text);
  text-overflow: ellipsis;
  white-space: nowrap;
  align-self: baseline;
}

.ic-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 12px;
}

.card-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.card-head.expandable {
  width: 100%;
  padding: 0;
  text-align: left;
  color: inherit;
  background: none;
  border: none;
  cursor: pointer;
}

.card-head .ch-title {
  flex: 1;
  font-size: 0.929rem;
  font-weight: 600;
  color: var(--heading);
  letter-spacing: 0.01em;
}

.chev {
  color: var(--muted);
  transition: transform var(--dur-fast) var(--ease);
}

.chev.open {
  transform: rotate(180deg);
}

.ch-badge {
  padding: 2px 10px;
  font-size: 0.786rem;
  border-radius: var(--radius-round);
}

.ch-badge.ok {
  color: var(--ok);
  background: color-mix(in srgb, var(--ok) 12%, transparent);
}

.ch-badge.warn {
  color: var(--warn);
  background: color-mix(in srgb, var(--warn) 12%, transparent);
}

.card-note {
  margin: 0;
  font-size: 0.786rem;
  line-height: 1.5;
  color: var(--muted);
}

.card-note.err {
  color: var(--err);
}

.stats-row {
  display: flex;
  gap: 32px;
  margin-top: 6px;
}

.stat-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.stat-cell b {
  font-size: 1.286rem;
  font-weight: 600;
  color: var(--heading);
  font-variant-numeric: tabular-nums;
}

.stat-cell i {
  font-size: 0.786rem;
  font-style: normal;
  color: var(--muted);
}

.sync-bar {
  margin: 10px 0 6px;
}

.sync-errs {
  margin: 8px 0 0;
  padding-left: 18px;
  font-size: 0.786rem;
  color: var(--err);
}

.other-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.other-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 4px;
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
