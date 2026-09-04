// format.ts —— 展示格式化（字节/时长/百分比），全部纯函数。
// 口径对照 Python 端：大小 1024 进制、1 位小数；时长秒转 h:mm:ss。

/** 字节数 → 可读字符串（B/KB/MB/GB/TB，1024 进制，1 位小数）。 */
export function fmtSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '-'
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = bytes
  let u = -1
  do {
    v /= 1024
    u++
  } while (v >= 1024 && u < units.length - 1)
  // 整数（如 512 MB）不带小数
  const s = v >= 100 ? Math.round(v).toString() : v.toFixed(1).replace(/\.0$/, '')
  return `${s} ${units[u]}`
}

/** 秒数 → 时长（mm:ss；超过 1 小时 h:mm:ss）。用于视频进度与连接时长。 */
export function fmtDur(totalSec: number): string {
  if (!Number.isFinite(totalSec) || totalSec < 0) return '0:00'
  const s = Math.floor(totalSec % 60)
  const m = Math.floor(totalSec / 60) % 60
  const h = Math.floor(totalSec / 3600)
  const ss = String(s).padStart(2, '0')
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`
}

/** 连接时长 → 中文简述（「3 分钟」「1.5 小时」；状态条与密库信息页共用）。 */
export function fmtConnectSec(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) return '-'
  if (sec < 60) return `${Math.floor(sec)} 秒`
  const min = sec / 60
  if (min < 60) return `${Math.floor(min)} 分钟`
  const h = min / 60
  if (h < 48) return `${h >= 10 ? Math.round(h) : Math.round(h * 10) / 10} 小时`
  return `${Math.floor(h / 24)} 天`
}

/** 0-1 比例 → 百分比整数（0-100）。 */
export function fmtPct(ratio: number): number {
  if (!Number.isFinite(ratio)) return 0
  return Math.min(100, Math.max(0, Math.round(ratio * 100)))
}

/** 文件展示名 → 小写扩展名（含点）；无扩展名返回 ''。 */
export function extOf(display: string): string {
  const i = display.lastIndexOf('.')
  return i > 0 ? display.slice(i).toLowerCase() : ''
}
