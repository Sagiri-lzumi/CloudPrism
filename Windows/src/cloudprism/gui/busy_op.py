"""后台耗时操作助手：把阻塞的密库操作放入后台线程执行。

PBKDF2 派生单次耗时数秒（迭代次数与 Android 端协议绑定不可调），
建库/开库若同步跑在 GUI 线程会让界面冻结十余秒。本模块提供轻量
QThread 封装与 run_busy() 调度入口：

- BusyOpThread：持有 callable，结束发 done(返回值) / error(异常描述)；
- run_busy()：sync=False 走后台线程；sync=True 原地同步执行（测试用）。

调用方在 done / error 回调中回到主线程更新界面。
"""

from __future__ import annotations

from PySide6.QtCore import QThread, Signal


class BusyOpThread(QThread):
    """执行单个 callable 的后台线程（建库/开库等密库重操作）。

    用法::

        th = BusyOpThread(op, parent=self)
        th.done.connect(on_done)
        th.error.connect(on_error)
        th.start()

    注意：callable 在工作线程执行，回调经信号回到主线程；
    调用方须持有线程引用（返回值）防止被 GC。
    """

    # 执行成功，参数为 callable 返回值
    done = Signal(object)
    # 执行失败，参数为异常描述（面向用户的文案由调用方组织）
    error = Signal(str)

    def __init__(self, op, parent=None) -> None:
        super().__init__(parent)
        self._op = op

    def run(self) -> None:
        """工作线程入口：执行 callable 并发射结果信号。"""
        try:
            result = self._op()
        except Exception as exc:  # noqa: BLE001
            self.error.emit(str(exc))
            return
        self.done.emit(result)


def run_busy(op, on_done, on_error, parent=None, sync: bool = False):
    """调度耗时操作：默认后台线程执行，sync=True 时原地同步执行。

    参数:
        op: 无参 callable，返回值经 on_done 回传
        on_done: 成功回调（主线程）
        on_error: 失败回调（主线程），参数为异常描述
        parent: 后台线程的父对象（防回收），同步模式忽略
        sync: True 时同步执行（单元测试路径，避免事件循环依赖）

    返回:
        后台线程实例（调用方须保存引用防 GC）；同步模式返回 None
    """
    if sync:
        # 同步路径：与后台路径语义一致（异常经 on_error 汇报）
        try:
            on_done(op())
        except Exception as exc:  # noqa: BLE001
            on_error(str(exc))
        return None
    th = BusyOpThread(op, parent=parent)
    th.done.connect(on_done)
    th.error.connect(on_error)
    th.start()
    return th
