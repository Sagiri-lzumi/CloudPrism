// theme.ts —— 主题模式应用（light/dark/auto 三态）。
//
// 与 index.html 首屏内联脚本镜像：同一 localStorage KEY、同一取值口径
// （'light'|'dark'|'auto' 合法值，其余回退浅色）。内联脚本负责 HTML
// 解析完成前防白闪，本模块负责运行期切换（设置页主题下拉）与启动兜底。
//
// 模式索引与 Go/Python 两端 settings 的 themeIndex 一致：0=跟随系统、
// 1=深色、2=浅色（对照 Python settings_store.theme_index 注释与
// app.py modes=["system","dark","light"]）。

export const THEME_KEY = 'cp-theme'

export type ThemeMode = 'light' | 'dark' | 'auto'

/** 索引 → 模式（themeIndex 语义，见文件头注释：0 系统/1 深/2 浅） */
export const MODES: readonly ThemeMode[] = ['auto', 'dark', 'light']

/** 设置页下拉文案（顺序与 MODES 对齐） */
export const MODE_LABELS = ['跟随系统', '深色', '浅色'] as const

function validMode(s: string | null | undefined): s is ThemeMode {
  return s === 'light' || s === 'dark' || s === 'auto'
}

/** 当前生效的具体模式（auto 未解析：由 CSS 媒体查询决定实际明暗）。 */
export function currentMode(): ThemeMode {
  const s = document.documentElement.dataset.theme
  return validMode(s) ? s : 'light'
}

/** 应用模式：写 html[data-theme] + 缓存到 localStorage。 */
export function setMode(mode: ThemeMode) {
  document.documentElement.dataset.theme = mode
  try {
    localStorage.setItem(THEME_KEY, mode)
  } catch {
    // localStorage 不可用（隐私模式等）：仅本会话生效
  }
}

/** 按 themeIndex 应用（Go 端返回/设置的索引直通）。 */
export function applyThemeIndex(index: number) {
  setMode(MODES[index] ?? 'light')
}

/** 启动兜底：读缓存恢复模式（首帧前内联脚本已做同样的事）。 */
export function initTheme() {
  let s: string | null = null
  try {
    s = localStorage.getItem(THEME_KEY)
  } catch {
    return // localStorage 不可用（隐私模式等）：保持默认浅色
  }
  if (validMode(s)) document.documentElement.dataset.theme = s
}
