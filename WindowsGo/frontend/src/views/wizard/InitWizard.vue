<!--
  InitWizard.vue —— 初始化向导（新建 / 连接密库，含百度授权内联流程）。
  对照 Python init_wizard（QWizard 五步合并为四步面板）：
    模式（新建/连接）→ 后端类型（本地/WebDAV/百度）→ 后端配置
    （百度卡含授权表单）→ 凭据（新建=库名+文件名加密+主密码两遍；
    连接=主密码，可选「忘记密码」切恢复码开库）。
  执行走 store.openVault（Vault.Open + 成功切文件页）；新建成功的一次性
  恢复码写入 store，由 App 全局模态展示（本向导随页面切换卸载也不影响）。
  失败在面板内红字展示（后端阶段文案经全局忙碌态在底部状态栏透出）。
-->
<script setup lang="ts">
import {computed, nextTick, reactive, ref, watch} from 'vue'
import {ui, openVault, navigate, endOp, pickLocalDir} from '../../lib/store'
import {Vault, unwrap} from '../../lib/api'
import {showInfo, showWarning} from '../../lib/toast'
import Button from '../../components/fluent/Button.vue'
import PrimaryButton from '../../components/fluent/PrimaryButton.vue'
import Checkbox from '../../components/fluent/Checkbox.vue'
import Icon from '../../components/fluent/Icon.vue'
import LineEdit from '../../components/fluent/LineEdit.vue'
import ProgressBar from '../../components/fluent/ProgressBar.vue'
import ModalShell from '../../components/layout/ModalShell.vue'

const emit = defineEmits<{close: []}>()

/* ------------------------------------------------------------ 步骤机 */

// 面板序号（0 起）；lastStep 由当前选择推导（见 stepTitles）
type Mode = 'new' | 'connect'
type BackendKind = 'local' | 'webdav' | 'baidu'

const MODE_DESC: Record<Mode, string> = {
  new: '在本目录 / 云端新建一个加密密库（生成恢复码）',
  connect: '连接已创建的密库（需主密码或恢复码）',
}

const BACKEND_META: {kind: BackendKind; icon: string; title: string; desc: string}[] = [
  {kind: 'local', icon: 'folder_add', title: '本地文件夹', desc: '密库存放在本机目录，不依赖网络'},
  {kind: 'webdav', icon: 'globe', title: 'WebDAV', desc: '连接支持 WebDAV 的服务器或云盘'},
  {kind: 'baidu', icon: 'cloud', title: '百度网盘', desc: '需开放平台应用凭证并完成授权（同 Python 版）'},
]

const step = ref(0) // 0 模式 / 1 后端类型 / 2 配置 / 3 凭据
const form = reactive({
  mode: 'connect' as Mode,
  kind: 'local' as BackendKind,

  // local / webdav 参数
  localDir: '',
  url: '',
  user: '',
  pass: '',

  // baidu 授权
  baidu: {authorized: false, appKey: '', appID: ''},

  // 凭据
  vaultName: '',
  filenameEnc: false,
  master: '',
  master2: '',
  useRecovery: false,
  recovery: '',
})

const status = ref('') // 面板内错误/提示（红）
const busy = ref(false) // 执行中（阶段文案走全局忙碌态，见 store 顶部约定）

/* ---------------------------------------------------------- 最近记录 */

const recentVaults = ref<Record<string, unknown>[]>([])
let loadedRecents = false

async function loadRecents() {
  try {
    recentVaults.value = (await Vault.RecentVaults()) ?? []
  } catch {
    recentVaults.value = []
  }
}

// 初始：有记录默认「连接」，并预填最常用后端的连接参数
watch(
  () => step.value,
  async (s) => {
    status.value = ''
    if (s !== 0 && !loadedRecents) {
      loadedRecents = true
      await loadRecents()
      const first = recentVaults.value[0]
      if (first && !(form.kind === 'baidu' && !form.baidu.authorized)) {
        // 默认沿用最近一次后端类型（连接场景概率最高）
        const k = String(first.backend_type ?? '')
        if (k === 'local' || k === 'webdav' || k === 'baidu') {
          form.kind = k
          if (k === 'local') form.localDir = String(first.path ?? '')
          else if (k === 'webdav') {
            form.url = String(first.path ?? '')
            form.user = String(first.webdav_user ?? '')
          }
        }
      }
    }
    if (s === 2 && form.kind === 'baidu') void refreshBaiduStatus()
  },
)
watch(() => form.kind, async (k) => {
  status.value = ''
  if (k === 'baidu') await refreshBaiduStatus()
})

/* ---------------------------------------------------------- 百度授权 */

const baiduBusy = ref(false)
const baiduForm = reactive({appID: '', appKey: '', secret: '', signKey: '', code: ''})
const baiduUrl = ref('')

/** 查询并回填授权状态（已授权时展示掩码 AppKey）。 */
async function refreshBaiduStatus() {
  try {
    const info = await Vault.BaiduStatus()
    form.baidu.authorized = !!info.authorized
    form.baidu.appKey = info.appKey ?? ''
    form.baidu.appID = info.appId ?? ''
  } catch {
    form.baidu.authorized = false
  }
}

/** 打开百度授权页（oob：页面直接展示 code；URL 在下方可复制兜底）。 */
async function openBaiduPage() {
  const appKey = baiduForm.appKey.trim()
  if (!appKey) {
    status.value = '请先填写 AppKey（开放平台应用凭证）'
    return
  }
  try {
    baiduUrl.value = await Vault.BaiduAuthURL(baiduForm.appID.trim(), appKey)
    status.value = '✓ 已打开浏览器授权页，登录并同意后把页面展示的 code 粘贴到下方'
  } catch (e) {
    status.value = unwrap(e).message
  }
}

/** 粘贴 code 后完成授权：换 token 加密落盘并刷新状态。 */
async function finishBaiduAuth() {
  const code = baiduForm.code.trim()
  if (!code) {
    status.value = '请先粘贴授权页展示的 code（一次性，过期需重新打开授权页）'
    return
  }
  baiduBusy.value = true
  status.value = ''
  try {
    await Vault.BaiduSaveAuth(
      baiduForm.appID.trim(),
      baiduForm.appKey.trim(),
      baiduForm.secret.trim(),
      baiduForm.signKey.trim(),
      code,
    )
    await refreshBaiduStatus()
    status.value = '✓ 授权成功：凭证已加密保存在本地'
    showInfo('百度网盘授权完成')
  } catch (e) {
    status.value = '授权失败：' + unwrap(e).message
  } finally {
    baiduBusy.value = false
  }
}

/** 清除本地授权（危险动作前无需密码——仅影响本机连接能力）。 */
async function clearBaiduAuth() {
  try {
    await Vault.BaiduClearAuth()
    await refreshBaiduStatus()
    status.value = ''
    showWarning('已清除本地百度授权记录')
  } catch (e) {
    status.value = unwrap(e).message
  }
}

/* ---------------------------------------------------------- 步进门禁 */

const canNext = computed(() => {
  if (step.value === 0) return true
  if (step.value === 1) return true
  if (step.value === 2) {
    if (form.kind === 'local') return form.localDir.trim() !== ''
    if (form.kind === 'webdav') {
      return /^https?:\/\/.+/i.test(form.url.trim()) && form.user.trim() !== ''
    }
    // baidu：必须已完成授权（access_token 已加密落盘）
    return form.baidu.authorized
  }
  // 凭据步
  if (form.mode === 'connect') {
    return form.useRecovery ? form.recovery.trim() !== '' : form.master !== ''
  }
  // 新建：两次主密码一致且非空；密码 ≥ 6 位（与 Python 向导同口径）
  return (
    form.master.length >= 6 &&
    form.master === form.master2 &&
    (form.vaultName.trim().length === 0 || form.vaultName.trim().length <= 40)
  )
})

const stepTitles = [
  {n: 1, title: '新建还是连接？'},
  {n: 2, title: '选择密库位置'},
  {n: 3, title: form.kind === 'baidu' && !form.baidu.authorized ? '百度网盘授权' : '配置密库位置'},
  {n: 4, title: form.mode === 'new' ? '设置主密码' : '验证主密码'},
]

const pwdHint = computed(() =>
  form.master !== form.master2 ? '两次输入的主密码不一致' : '',
)

/* -------------------------------------------------------------- 执行 */

function next() {
  if (!canNext.value) return
  step.value = Math.min(step.value + 1, 3)
}

function back() {
  if (step.value > 0) step.value--
}

async function finish() {
  if (busy.value) return
  busy.value = true
  status.value = ''
  try {
    const req = {
      kind: form.kind,
      localDir: form.kind === 'local' ? form.localDir.trim() : '',
      url: form.kind === 'webdav' ? form.url.trim() : '',
      user: form.kind === 'webdav' ? form.user.trim() : '',
      pass: form.kind === 'webdav' ? form.pass : '',
      vaultName: form.mode === 'new' ? form.vaultName.trim() : '',
      filenameEnc: form.mode === 'new' ? form.filenameEnc : false,
      create: form.mode === 'new',
      masterPassword: form.useRecovery ? '' : form.master,
      recoveryCode: form.useRecovery ? form.recovery.trim() : '',
      vaultPath: '',
    }
    await openVault(req)
    emit('close') // openVault 已切文件页；恢复码由 App 全局模态展示
  } catch (e) {
    status.value = unwrap(e).message
  } finally {
    busy.value = false
    endOp() // 后端只发 progress：忙碌复位由调用方 finally 保证
  }
}

function cancel() {
  if (busy.value) return
  emit('close')
}

/** 目录浏览选择（本地卡「浏览…」钮）：网页版选择器，选完即回填路径。
 *  不再走主机原生对话框 —— 它 owner=0、没有属主窗口，会弹到浏览器窗口后面，
 *  看起来像「后台莫名跳出个框」。取消返回 null，静默即可。 */
async function browseLocalDir() {
  const dir = await pickLocalDir({title: '选择密库存放的本地文件夹', start: form.localDir})
  if (dir) form.localDir = dir
}

/* 未连接时整个向导页可见；向导内回车在凭据步直接提交 */
function enterAt(e: KeyboardEvent) {
  if (e.key === 'Enter') {
    e.preventDefault()
    if (step.value === 3 && canNext.value) void finish()
    else next()
  }
}

const bodyEl = ref<HTMLElement>()

/**
 * 换步后把焦点收回面板内。
 *
 * 各步是 v-if 分支，换步会整棵重建、被聚焦的节点随之销毁，焦点掉回 body。
 * 而 ModalShell 的 Tab 循环挂在面板上 —— 焦点在 body 时收不到 keydown，
 * Tab 会直接跑到面板外的页面区（模态还开着，键盘却已经出去了）。
 * 所以换步后主动聚焦新分支里的第一个可聚焦项，键盘流不中断。
 */
watch(step, async () => {
  await nextTick()
  const sel = 'button:not([disabled]),input:not([disabled]),[tabindex]:not([tabindex="-1"])'
  bodyEl.value?.querySelector<HTMLElement>(sel)?.focus()
})
</script>

<template>
  <!-- 浮层结构（遮罩/面板/页头/焦点/Esc）走 ModalShell：
       Esc 由它挂 window 处理，因此「刚打开」和「回车翻步后」都能关掉
       （此前挂在遮罩上的 @keydown 只在焦点恰好在面板内时才生效，实测两条
       真实路径都关不掉）。执行中交出 esc 与关闭钮，避免半途被 Esc 打断。 -->
  <ModalShell
    :open="true"
    title="初始化 CloudPrism"
    :sub="stepTitles[step].title"
    icon="connect"
    :width="600"
    :closable="!busy"
    :esc="!busy"
    @close="cancel"
  >
    <!-- 步骤指示：放条带槽（页头之下、滚动体之上），滚动时不会跟着内容滚走 -->
    <template #bar>
      <div class="wiz-steps" aria-hidden="true">
        <span
          v-for="(s, i) in stepTitles"
          :key="s.n"
          class="ws-dot"
          :class="{on: step === i, done: step > i}"
        />
      </div>
    </template>

    <!-- 面板体：Enter 步进仍绑在这一层（焦点在体内任何控件上都能冒泡到这里） -->
    <div ref="bodyEl" class="wiz-body" @keydown="enterAt">
      <!-- 步 0：模式 -->
      <div v-if="step === 0" class="radio-col">
        <button
          type="button"
          class="radio-card"
          :class="{on: form.mode === 'new'}"
          @click="form.mode = 'new'"
        >
          <Icon name="add" :size="18" class="rc-ic" />
          <span class="rc-txt">
            <b>新建密库</b>
            <i>首次使用：创建全新加密库，含恢复码备份</i>
          </span>
          <span class="rc-dot" />
        </button>
        <button
          type="button"
          class="radio-card"
          :class="{on: form.mode === 'connect'}"
          @click="form.mode = 'connect'"
        >
          <Icon name="folder" :size="18" class="rc-ic" />
          <span class="rc-txt">
            <b>连接已有密库</b>
            <i>从本地目录或云端恢复/继续使用</i>
          </span>
          <span class="rc-dot" />
        </button>
        <p class="wiz-hint">{{ MODE_DESC[form.mode] }}</p>
      </div>

      <!-- 步 1：后端类型 -->
      <div v-else-if="step === 1" class="radio-col">
        <button
          v-for="b in BACKEND_META"
          :key="b.kind"
          type="button"
          class="radio-card"
          :class="{on: form.kind === b.kind}"
          @click="form.kind = b.kind"
        >
          <Icon :name="b.icon" :size="18" class="rc-ic" />
          <span class="rc-txt">
            <b>{{ b.title }}</b>
            <i>{{ b.desc }}</i>
          </span>
          <span class="rc-dot" />
        </button>
        <p class="wiz-hint">
          连接参数（账号等）仅保存在本机设置；所有密码只驻内存、绝不落盘。
        </p>
      </div>

      <!-- 步 2：配置 -->
      <div v-else-if="step === 2" class="form-col">
        <!-- 本地：目录选择 -->
        <template v-if="form.kind === 'local'">
          <div class="fld">
            <label>密库存放目录</label>
            <div class="dir-row">
              <LineEdit
                v-model="form.localDir"
                clearable
                placeholder="选择或输入本地文件夹路径"
              />
              <Button icon="folder" title="浏览选择目录" @click="browseLocalDir">
                浏览…
              </Button>
            </div>
            <p class="wiz-hint">
              目录无需为空：多个密库可共存（同名目录内检测既有密库时会提示）。
            </p>
          </div>
        </template>

        <!-- WebDAV -->
        <template v-else-if="form.kind === 'webdav'">
          <div class="fld">
            <label>服务器地址</label>
            <LineEdit
              v-model="form.url"
              clearable
              placeholder="https://dav.example.com/remote.php/dav/files/me"
            />
          </div>
          <div class="fld">
            <label>用户名</label>
            <LineEdit v-model="form.user" clearable placeholder="WebDAV 账号" />
          </div>
          <div class="fld">
            <label>密码（不落盘）</label>
            <LineEdit v-model="form.pass" password placeholder="服务器密码（应用专用密码）" />
          </div>
        </template>

        <!-- 百度：授权状态 + 内嵌授权 -->
        <template v-else>
          <div v-if="form.baidu.authorized" class="ok-box">
            <Icon name="completed" :size="16" />
            <span>已授权（AppKey：{{ form.baidu.appKey || '未知' }}），凭证已加密保存</span>
            <Button iconOnly icon="cancel" title="清除本机授权" @click="clearBaiduAuth" />
          </div>
          <template v-else>
            <div class="fld">
              <label>开放平台凭证（申请见《Plan/百度网盘开放平台申请指南》）</label>
              <div class="cred-grid">
                <LineEdit v-model="baiduForm.appID" placeholder="Appid（可选）" />
                <LineEdit v-model="baiduForm.appKey" placeholder="AppKey（必填）" />
                <LineEdit v-model="baiduForm.secret" password placeholder="SecretKey（必填）" />
                <LineEdit v-model="baiduForm.signKey" password placeholder="SignKey（可选）" />
              </div>
            </div>
            <div class="fld">
              <label>第一步：打开授权页</label>
              <Button icon="globe" :disabled="baiduBusy" @click="openBaiduPage">
                打开授权页面
              </Button>
              <p v-if="baiduUrl" class="url-line" :title="baiduUrl">{{ baiduUrl }}</p>
            </div>
            <div class="fld">
              <label>第二步：粘贴 code 并完成授权</label>
              <div class="dir-row">
                <LineEdit v-model="baiduForm.code" placeholder="授权页展示的一次性 code" />
                <PrimaryButton
                  icon="completed"
                  :disabled="baiduBusy"
                  @click="finishBaiduAuth"
                >
                  完成授权
                </PrimaryButton>
              </div>
            </div>
          </template>
        </template>
      </div>

      <!-- 步 3：凭据 -->
      <div v-else class="form-col">
        <!-- 新建：库名 + 文件名加密 + 主密码两遍 -->
        <template v-if="form.mode === 'new'">
          <div class="fld">
            <label>密库名称（可选）</label>
            <LineEdit
              v-model="form.vaultName"
              clearable
              placeholder="给密库起个名字，默认自动编号"
            />
          </div>
          <div class="fld">
            <label>文件名加密</label>
            <div class="two-radios">
              <button
                type="button"
                class="mini-radio"
                :class="{on: !form.filenameEnc}"
                @click="form.filenameEnc = false"
              >
                关闭（云端可见原始文件名）
              </button>
              <button
                type="button"
                class="mini-radio"
                :class="{on: form.filenameEnc}"
                @click="form.filenameEnc = true"
              >
                开启（文件名一并加密，更安全）
              </button>
            </div>
            <p class="wiz-hint">启动后不可中途修改（需重建密库迁移）。</p>
          </div>
          <div class="fld">
            <label>主密码（≥ 6 位，忘记后凭恢复码找回）</label>
            <LineEdit v-model="form.master" password placeholder="设置主密码" />
          </div>
          <div class="fld">
            <label>再次输入主密码</label>
            <LineEdit v-model="form.master2" password placeholder="重复主密码" />
            <p v-if="pwdHint" class="err-hint">{{ pwdHint }}</p>
          </div>
        </template>

        <!-- 连接：主密码 / 恢复码 -->
        <template v-else>
          <Checkbox v-model="form.useRecovery">
            忘记主密码？改用恢复码开库
          </Checkbox>
          <div v-if="!form.useRecovery" class="fld">
            <label>主密码</label>
            <LineEdit v-model="form.master" password placeholder="输入主密码" />
          </div>
          <div v-else class="fld">
            <label>恢复码</label>
            <LineEdit
              v-model="form.recovery"
              clearable
              placeholder="XXXX-XXXX-XXXX-XXXX（一次性备份的恢复码）"
            />
          </div>
        </template>

        <!-- 执行期状态（✓ 前缀 = 成功/提示，其余按错误红字） -->
        <div v-if="status" class="status-line" :class="{err: !status.startsWith('✓')}">
          {{ status }}
        </div>
        <div v-if="busy" class="busy-row">
          <div class="busy-track"><ProgressBar indeterminate /></div>
        </div>
      </div>
    </div>

    <template #foot-lead>
      {{ form.mode === 'new' ? '新建密库' : '连接密库' }} ·
      {{ BACKEND_META.find((b) => b.kind === form.kind)?.title }}
    </template>

    <template #actions>
      <Button v-if="step > 0" :disabled="busy" @click="back">上一步</Button>
      <PrimaryButton v-if="step < 3" :disabled="!canNext || busy" @click="next">
        下一步
      </PrimaryButton>
      <PrimaryButton
        v-else
        icon="completed"
        :disabled="!canNext || busy"
        @click="finish"
      >
        {{ busy ? '执行中…' : (form.mode === 'new' ? '创建密库' : '连接') }}
      </PrimaryButton>
    </template>
  </ModalShell>
</template>

<style scoped>
/* 遮罩 / 面板 / 页头 / 页脚已收进 components/layout/ModalShell.vue，
   本组件只剩「向导特有」的内容样式：步骤条、单选卡列、表单列。 */

/* 步骤指示 */
.wiz-steps {
  display: flex;
  gap: 6px;
}

.ws-dot {
  flex: 1;
  height: 3px;
  background: color-mix(in srgb, var(--text) 12%, transparent);
  border-radius: var(--radius-round);
}

.ws-dot.on {
  background: var(--accent);
}

.ws-dot.done {
  background: color-mix(in srgb, var(--accent) 55%, transparent);
}

/* 体内层：只负责 Enter 步进的冒泡边界 + 各步之间不跳动的最小高度 */
.wiz-body {
  min-height: 300px;
}

/* 单选卡列 */
.radio-col {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.radio-card {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
  padding: 13px 14px;
  text-align: left;
  color: var(--text);
  background: var(--bg-page);
  border: 1px solid var(--stroke);
  border-radius: var(--radius-card);
  cursor: pointer;
  transition: border-color var(--dur-fast) var(--ease), background var(--dur-fast) var(--ease);
}

.radio-card:hover {
  background: color-mix(in srgb, var(--accent) 5%, var(--bg-page));
}

.radio-card.on {
  border-color: var(--accent);
  background: var(--accent-soft);
}

.rc-ic {
  flex: none;
  color: var(--muted);
}

.radio-card.on .rc-ic {
  color: var(--accent);
}

.rc-txt {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.rc-txt b {
  font-size: 0.929rem;
  font-weight: 600;
}

.rc-txt i {
  font-size: 0.786rem;
  font-style: normal;
  color: var(--text2);
}

.rc-dot {
  flex: none;
  width: 14px;
  height: 14px;
  border: 1.5px solid var(--stroke);
  border-radius: 50%;
}

.radio-card.on .rc-dot {
  border: 4.5px solid var(--accent);
}

/* 表单列 */
.form-col {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.fld label {
  display: block;
  margin-bottom: 6px;
  font-size: 0.857rem;
  color: var(--text);
}

.dir-row {
  display: flex;
  gap: 8px;
}

.dir-row .cp-line-edit {
  flex: 1;
}

.cred-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}

/* 手机：凭证两列并排会把输入框压到不可用，改单列堆叠 */
@media (max-width: 640px) {
  .cred-grid {
    grid-template-columns: 1fr;
  }
}

.two-radios {
  display: flex;
  gap: 8px;
}

.mini-radio {
  flex: 1;
  padding: 9px 10px;
  font-size: 0.786rem;
  color: var(--text2);
  background: var(--bg-page);
  border: 1px solid var(--stroke);
  border-radius: var(--radius-ctrl);
  cursor: pointer;
}

.mini-radio.on {
  color: var(--accent);
  border-color: var(--accent);
  background: var(--accent-soft);
}

.wiz-hint {
  margin: 2px 0 0;
  font-size: 0.786rem;
  line-height: 1.5;
  color: var(--muted);
}

.err-hint {
  margin: 4px 0 0;
  font-size: 0.786rem;
  color: var(--err);
}

.ok-box {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  font-size: 0.857rem;
  color: var(--ok);
  background: color-mix(in srgb, var(--ok) 10%, transparent);
  border-radius: var(--radius-ctrl);
}

.ok-box .cp-btn {
  margin-left: auto;
}

.url-line {
  margin: 6px 0 0;
  overflow: hidden;
  font-size: 0.714rem;
  color: var(--muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.status-line {
  padding: 8px 10px;
  font-size: 0.786rem;
  line-height: 1.4;
  color: var(--text2);
  background: color-mix(in srgb, var(--text) 6%, transparent);
  border-radius: var(--radius-ctrl);
}

.status-line.err {
  color: var(--err);
  background: color-mix(in srgb, var(--err) 10%, transparent);
}

.busy-row {
  margin-top: -6px;
}

.busy-track {
  height: 4px;
}
</style>
