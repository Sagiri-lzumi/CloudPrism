// videoCover.ts —— 视频卡封面抽帧（网格里直接显示一帧画面）。
//
// 需求（2026-09-22）：视频条目在网格里要显示封面画面。浏览器**没有**读容器内嵌
// 封面的 API（mp4 的 covr、mkv 的 attachment 都不暴露给 JS），所以采用用户给出的
// 等价方案：把 <video> 定位到中间时刻取一帧。用户原话「可以提取为中心的一帧或者
// 是如果有封面就使用这个相应的封面」——内嵌封面这条路不可达，取中心帧是双方认可的
// 落点。
//
// 为什么这么贵还得做：取一帧必须先拿到 moov（时长与轨道索引）和目标时刻的编码
// 数据。好在 /s/ 端点支持 Range，浏览器只按需拉区间 —— 十几 GB 的文件通常也只要
// 几百 KB。但容器若不支持流式寻址（moov 在尾部且无法随机读、或编码浏览器不认），
// 就只能放弃。
//
// 三层降级，任何一层失败都只是「没有封面」，**绝不弹错**（卡片回退类别图标）：
//   1. 容器/编码不被 Chromium 支持（avi / flv / 部分 mkv）→ error 事件 → 放弃；
//   2. 体积超过 MAX_SOURCE_BYTES → 直接不试，省掉一次注定失败的网络往返；
//   3. 超时 TIMEOUT_MS → 放弃。
//
// 并发与缓存的两条硬约束：
//   · 抽帧要独占一个解码器 + 一次网络往返。几十张卡同时来会（a）把令牌注册表顶到
//     ErrTooManyTokens，连带把正常缩略图挤掉；（b）把远端读打满。故模块级串行闸门。
//   · 缓存的是 **Blob** 而不是 objectURL。objectURL 有归属、由拿到它的那张卡负责
//     revoke（卸载即回收）；若缓存里共享同一个 URL，先卸载的那张会把还在显示同一
//     文件的另一张的画面弄没（表现为图突然变空白）。Blob 造 URL 是零成本的。

import type {appstate} from '../types/appstate'
import {mediaUrl, revoke} from './store'

/** 抽帧结果 */
export interface VideoCover {
  blob: Blob
  /** 容器报告的时长（秒）；不可用时为 0 */
  duration: number
}

export interface CoverHandle {
  promise: Promise<VideoCover>
  /** 卡片卸载时调用：排队中的任务直接丢弃；已在跑的任务让它跑完（结果进缓存，不白费网络） */
  cancel(): void
}

/** 同时进行的抽帧上限（见文件头注释） */
const MAX_PARALLEL = 2
/** 单个文件的抽帧超时 */
const TIMEOUT_MS = 12_000
/** 超过这个体积直接放弃（容器能否流式寻址未知，代价不对称） */
const MAX_SOURCE_BYTES = 2 * 1024 * 1024 * 1024
/** 取帧时刻的上限：越靠后要拉的区间越大、GOP 越长越贵，30s 之后收益不明显 */
const SEEK_CAP_SEC = 30
/** 封面输出宽度上限（卡片承托面约 136px 宽，320 足够 2x 屏） */
const MAX_WIDTH = 320
const JPEG_QUALITY = 0.72
/** Blob 缓存条数上限（单张约 20~60KB，80 条约 2~5MB） */
const CACHE_MAX = 80

const cache = new Map<string, VideoCover>()

function cacheGet(key: string): VideoCover | undefined {
  const hit = cache.get(key)
  if (hit) {
    // Map 的迭代序即插入序：删了再插 = 把这条挪到队尾，等价于 LRU 的「刚用过」
    cache.delete(key)
    cache.set(key, hit)
  }
  return hit
}

function cachePut(key: string, v: VideoCover) {
  cache.set(key, v)
  while (cache.size > CACHE_MAX) {
    const oldest = cache.keys().next().value
    if (oldest === undefined) break
    cache.delete(oldest)
  }
}

/* ------------------------------------------------------------ 串行闸门 */

const waiting: Array<() => void> = []
let running = 0

function pump() {
  while (running < MAX_PARALLEL && waiting.length > 0) {
    running++
    waiting.shift()!()
  }
}

/** 排队执行 fn，最多 MAX_PARALLEL 个并发。stale() 为真时表示排队期间已失去意义。 */
function gate<T>(fn: () => Promise<T>, stale: () => boolean): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    waiting.push(() => {
      if (stale()) {
        running--
        pump()
        reject(new Error('cover task cancelled'))
        return
      }
      fn()
        .then(resolve, reject)
        .finally(() => {
          running--
          pump()
        })
    })
    pump()
  })
}

/* ------------------------------------------------------------ 抽帧本体 */

/**
 * 用离屏 <video> 取中间帧并编码成 JPEG。
 * 失败原因全部收敛成 Error（调用方只需要「有没有封面」这一个判断）。
 */
function extractFrame(url: string): Promise<VideoCover> {
  return new Promise<VideoCover>((resolve, reject) => {
    const video = document.createElement('video')
    // preload=metadata：只要 moov，不要预缓冲整段；真正需要的那一段由 seek 触发
    video.preload = 'metadata'
    video.muted = true
    video.playsInline = true
    // 不设 crossOrigin：/s/ 是同源相对路径，同源画布本来就不被污染
    video.src = url

    let settled = false
    let duration = 0
    let timeout = 0
    let seekFallback = 0

    const cleanup = () => {
      window.clearTimeout(timeout)
      window.clearTimeout(seekFallback)
      video.removeEventListener('loadedmetadata', onMeta)
      video.removeEventListener('loadeddata', onData)
      video.removeEventListener('seeked', onSeeked)
      video.removeEventListener('error', onError)
      // 断开解码器与在途请求：只把 src 置空不 load()，浏览器仍会继续缓冲
      video.removeAttribute('src')
      video.load()
    }

    const fail = (msg: string) => {
      if (settled) return
      settled = true
      cleanup()
      reject(new Error(msg))
    }

    const done = (blob: Blob) => {
      if (settled) return
      settled = true
      cleanup()
      resolve({blob, duration})
    }

    /** 把「当前显示的帧」画进画布并编码。任何一步不满足都只是失败，不是异常。 */
    const grab = () => {
      if (settled) return
      const vw = video.videoWidth
      const vh = video.videoHeight
      if (!vw || !vh) {
        fail('视频尚未解出画面尺寸')
        return
      }
      const w = Math.min(MAX_WIDTH, vw)
      const h = Math.max(1, Math.round((vh * w) / vw))
      const canvas = document.createElement('canvas')
      canvas.width = w
      canvas.height = h
      const ctx = canvas.getContext('2d')
      if (!ctx) {
        fail('无法创建 2D 画布')
        return
      }
      ctx.drawImage(video, 0, 0, w, h)
      canvas.toBlob((b) => (b ? done(b) : fail('JPEG 编码失败')), 'image/jpeg', JPEG_QUALITY)
    }

    function onSeeked() {
      grab()
    }

    function onData() {
      grab()
    }

    function onError() {
      fail('容器或编码不被浏览器支持')
    }

    function onMeta() {
      // 部分容器（webm/若干 mkv）在未 seek 前把 duration 报成 Infinity
      duration = Number.isFinite(video.duration) && video.duration > 0 ? video.duration : 0
      const target = duration > 0 ? Math.min(duration / 2, SEEK_CAP_SEC) : 0
      if (target <= 0) {
        // 时长不可用：退化成「首帧可解就取」
        if (video.readyState >= 2) grab()
        else video.addEventListener('loadeddata', onData, {once: true})
        return
      }
      video.addEventListener('seeked', onSeeked, {once: true})
      // 保险丝：seeked 在个别内核上对「本该发生的 seek」不派发（例如目标时刻与
      // 当前位置在时间轴上等价），此时只要已有可解码数据就直接取帧，别干等到总超时。
      seekFallback = window.setTimeout(() => {
        if (video.readyState >= 2) grab()
      }, 3000)
      video.currentTime = target
    }

    video.addEventListener('loadedmetadata', onMeta, {once: true})
    video.addEventListener('error', onError, {once: true})
    timeout = window.setTimeout(() => fail('抽帧超时'), TIMEOUT_MS)
  })
}

/* ------------------------------------------------------------ 对外入口 */

/**
 * 请求某个视频条目的封面。
 *
 * 调用方拿 promise；组件卸载时调 cancel()。**必须**处理 reject ——
 * 没有封面是正常结局，不是错误态。
 */
export function requestVideoCover(e: appstate.FileEntry): CoverHandle {
  let cancelled = false
  const promise = (async (): Promise<VideoCover> => {
    const hit = cacheGet(e.remote)
    if (hit) return hit
    if (e.size > MAX_SOURCE_BYTES) throw new Error('文件过大，跳过封面抽帧')

    // 令牌在这里签发、在 finally 里归还：封面一旦到手（或注定拿不到），
    // 就没必要再占着注册表里的一个名额。
    const url = await mediaUrl(e)
    try {
      const out = await gate(() => extractFrame(url), () => cancelled)
      cachePut(e.remote, out)
      return out
    } finally {
      void revoke(url).catch(() => {})
    }
  })()
  return {
    promise,
    cancel: () => {
      cancelled = true
    },
  }
}
