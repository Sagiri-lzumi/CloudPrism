// media.ts —— 文件预览类别判定（对照 WindowsPy/gui/preview_panel.py:39-42
// 四类扩展名集合）。网格缩略图、预览面板分流共用同一分类，避免口径分叉。

import {extOf} from './format'

/** 可内嵌流式播放的视频扩展名（大小写不敏感，比对时已小写） */
export const VIDEO_EXTS = ['.mp4', '.mkv', '.avi', '.mov', '.webm', '.flv']

/** 可内嵌播放的音频扩展名 */
export const AUDIO_EXTS = ['.mp3', '.flac', '.wav', '.aac', '.ogg', '.m4a']

/** 可请求加密缩略图并展示的图片扩展名 */
export const IMAGE_EXTS = ['.jpg', '.jpeg', '.png', '.gif', '.bmp', '.webp']

/** 可代理拉取并纯文本渲染的扩展名 */
export const TEXT_EXTS = ['.txt', '.md', '.log', '.json', '.xml', '.csv']

export type MediaKind = 'video' | 'audio' | 'image' | 'text' | 'other'

/** 按展示名扩展名归类预览类别；目录/无扩展名一律 other。 */
export function kindOf(display: string): MediaKind {
  const ext = extOf(display)
  if (VIDEO_EXTS.includes(ext)) return 'video'
  if (AUDIO_EXTS.includes(ext)) return 'audio'
  if (IMAGE_EXTS.includes(ext)) return 'image'
  if (TEXT_EXTS.includes(ext)) return 'text'
  return 'other'
}

/** 预览类别图标（网格占位 / 预览面板头部共用）。 */
export const KIND_ICON: Record<MediaKind, string> = {
  video: 'video',
  audio: 'headphone',
  image: 'photo',
  text: 'document',
  other: 'document',
}
