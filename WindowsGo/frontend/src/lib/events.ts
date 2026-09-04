// events.ts —— 事件名常量与合帧载荷类型。
//
// 与 internal/bind/events.go 镜像：事件名改动必须两侧同步
// （Go 侧同名注释同样声明此约束）。

/** EvtFrame 状态合帧：每 100ms 一帧，载荷 StateFrame（传输活动时带任务明细）。 */
export const EvtFrame = 'st:frame'

/** EvtDropped 系统文件拖放：载荷 string[]（本地路径，前端以当前目录发起上传）。 */
export const EvtDropped = 'st:files-dropped'

/** EvtOpProgress 长操作阶段文案：载荷 string（向导/恢复码等，低频直发）。 */
export const EvtOpProgress = 'st:op-progress'

/** EvtOpDone 长操作结束：载荷 string。 */
export const EvtOpDone = 'st:op-done'

/** EvtOpError 长操作失败：载荷 string。 */
export const EvtOpError = 'st:op-error'

/** EvtLocked 自动/手动锁库广播：载荷 null，前端立即回引导态。 */
export const EvtLocked = 'st:locked'

/** 全部事件名集合（用于 Wails 侧事件订阅去重等场景）。 */
export const ALL_EVENTS = [
  EvtFrame,
  EvtDropped,
  EvtOpProgress,
  EvtOpDone,
  EvtOpError,
  EvtLocked,
] as const

// 帧载荷与 go/models.ts 的 appstate 模型对应；此处只用类型断言，
// 模型实例化统一走 models（Wails 已生成 createFrom 便捷方法）。
import type {appstate} from '../../wailsjs/go/models'

/** 合帧载荷：全局快照 + 活动传输任务明细（Tasks 仅传输活跃时存在）。 */
export interface StateFrame {
  snap: appstate.Snapshot
  tasks?: appstate.TaskView[]
}
