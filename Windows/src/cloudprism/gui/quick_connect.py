"""密库快速连接对话框。

从最近密库记录一键重连：后端参数来自记录，仅需输入主密码
（WebDAV 后端另需其服务器密码）。主密码与服务器密码均不落盘。
"""

from __future__ import annotations

from PySide6.QtCore import Signal
from PySide6.QtWidgets import (
    QDialog,
    QFormLayout,
    QHBoxLayout,
    QLabel,
    QVBoxLayout,
)

# Fluent 组件（均继承自对应 Qt 原生控件，标准 API 全兼容）
from qfluentwidgets import (
    CheckBox,
    IndeterminateProgressBar,
    LineEdit,
    PrimaryPushButton,
    PushButton,
)

from cloudprism.core.backend_factory import build_backend_from_params
from cloudprism.core.session import Session
from cloudprism.core.vault_manager import VaultManager
from cloudprism.gui.busy_op import run_busy
from cloudprism.gui.theme import semantic_color


class _ConnectError(Exception):
    """连接业务错误：消息直接展示在对话框状态栏。"""


class QuickConnectDialog(QDialog):
    """最近密库一键重连对话框。

    连接成功后产物存于实例属性：
      backend / metadata / session
    """

    # 开库含数秒级 PBKDF2 派生，默认后台线程执行避免冻结界面；
    # 测试置 True 走同步路径（无需事件循环）
    sync_ops = False

    # 后台开库阶段进度（文案来自 vault_manager 的 progress_cb，
    # 跨线程 emit 自动排队投递主线程）
    progressed = Signal(str)

    def __init__(
        self,
        record: dict,
        parent=None,
        backend_factory=build_backend_from_params,
    ) -> None:
        super().__init__(parent)
        self.setWindowTitle("快速连接密库")
        self.setMinimumWidth(440)
        self._record = dict(record)
        self._factory = backend_factory

        # 子目录密库位置（空=后端根目录）：决定 Marker 查找路径，
        # 缺失时重连会开错位置而误报密码错误
        self.vault_path = (record.get("vault_path") or "").strip("/")

        # 连接成功产物
        self.backend = None
        self.metadata = None
        self.session: Session | None = None

        lay = QVBoxLayout(self)

        # ---- 密库摘要（只读） ----
        location = record.get("path", "-")
        if self.vault_path:
            location = f"{location} / {self.vault_path}"
        summary = QLabel(
            f"类型：{record.get('label', record.get('backend_type', '?'))}\n"
            f"位置：{location}\n"
            f"库：{record.get('vault_name', '-')} · "
            f"上次连接：{record.get('last_used', '-')}",
            self,
        )
        summary.setStyleSheet(f"color: {semantic_color('muted')}; font-size: 13px;")
        summary.setWordWrap(True)
        self._summary = summary  # 测试辅助入口
        lay.addWidget(summary)

        # ---- 凭证输入 ----
        form = QFormLayout()

        # WebDAV 需先输入服务器密码（主密码之外）
        self._webdav_pass_edit = None
        if record.get("backend_type") == "webdav":
            user = record.get("webdav_user", "")
            self._webdav_pass_edit = LineEdit(self)
            self._webdav_pass_edit.setEchoMode(LineEdit.EchoMode.Password)
            self._webdav_pass_edit.setPlaceholderText(
                f"WebDAV 账号 {user} 的服务器密码" if user else "WebDAV 服务器密码"
            )
            form.addRow("服务器密码：", self._webdav_pass_edit)

        self._pw_edit = LineEdit(self)
        self._pw_edit.setEchoMode(LineEdit.EchoMode.Password)
        self._pw_edit.setPlaceholderText("密库主密码（仅存内存，不落盘）")
        self._pw_edit.returnPressed.connect(self._connect)
        form.addRow("主密码：", self._pw_edit)

        lay.addLayout(form)

        # ---- 恢复码开库（折叠输入框：勾选后展开，代替主密码） ----
        self._recovery_check = CheckBox("使用恢复码开库（忘记主密码时）", self)
        self._recovery_check.toggled.connect(self._on_recovery_toggled)
        lay.addWidget(self._recovery_check)

        self._recovery_edit = LineEdit(self)
        self._recovery_edit.setPlaceholderText(
            "XXXX-XXXX-XXXX-XXXX（建库时生成，离线保存）"
        )
        self._recovery_edit.returnPressed.connect(self._connect)
        self._recovery_edit.setVisible(False)
        lay.addWidget(self._recovery_edit)

        # ---- 按钮行 ----
        btn_row = QHBoxLayout()
        self._connect_btn = PrimaryPushButton("连接", self)
        self._connect_btn.setDefault(True)
        self._connect_btn.clicked.connect(self._connect)
        cancel_btn = PushButton("取消", self)
        cancel_btn.clicked.connect(self.reject)
        btn_row.addStretch()
        btn_row.addWidget(self._connect_btn)
        btn_row.addWidget(cancel_btn)
        lay.addLayout(btn_row)

        # ---- 不定进度条（后台开库期间显示；默认隐藏） ----
        self._busy_bar = IndeterminateProgressBar(self)
        self._busy_bar.setVisible(False)
        lay.addWidget(self._busy_bar)

        self._status = QLabel("", self)
        self._status.setWordWrap(True)
        self._status.setStyleSheet(f"color: {semantic_color('err')};")
        lay.addWidget(self._status)

        lay.addStretch()

        # 阶段进度 -> 状态栏实时展示（避免数秒空白等待误以为卡死）
        self.progressed.connect(self._on_progress)

    # ------------------------------------------------------------------
    # 连接动作
    # ------------------------------------------------------------------

    def _on_recovery_toggled(self, checked: bool) -> None:
        """展开/收起恢复码输入框；勾选后主密码非必填。"""
        self._recovery_edit.setVisible(checked)
        if checked:
            self._status.setText("")

    def _on_progress(self, msg: str) -> None:
        """阶段进度文案实时写入状态栏（进度用中性色，区别于错误红）。"""
        self._status.setStyleSheet(f"color: {semantic_color('muted')};")
        self._status.setText(msg)

    def _connect(self) -> None:
        """构造后端并打开密库（主密码或恢复码）；失败在对话框内提示。"""
        use_recovery = self._recovery_check.isChecked()
        recovery = self._recovery_edit.text().strip()
        pw = self._pw_edit.text()
        if use_recovery:
            if not recovery:
                self._status.setText("请输入恢复码")
                return
        elif not pw:
            self._status.setText("请输入主密码")
            return
        webdav_pass = ""
        if self._webdav_pass_edit is not None:
            webdav_pass = self._webdav_pass_edit.text()
            if not webdav_pass:
                self._status.setText("请输入 WebDAV 服务器密码")
                return

        btype = self._record.get("backend_type", "")
        path = self._record.get("path", "")
        self._connect_btn.setEnabled(False)
        self._busy_bar.setVisible(True)
        self._status.setStyleSheet(f"color: {semantic_color('muted')};")
        self._status.setText("正在校验主密码，约需数秒…")
        try:
            backend = self._factory(
                btype,
                local_dir=path if btype == "local" else "",
                webdav_url=path if btype == "webdav" else "",
                webdav_user=self._record.get("webdav_user", ""),
                webdav_pass=webdav_pass,
            )
        except Exception as e:  # noqa: BLE001
            self._connect_btn.setEnabled(True)
            self._busy_bar.setVisible(False)
            self._status.setStyleSheet(f"color: {semantic_color('err')};")
            self._status.setText(f"连接失败：{e}")
            return

        def op():
            """后台重操作：开库校验（含数秒级 PBKDF2 派生）。"""
            try:
                vm = VaultManager(backend)
                if use_recovery:
                    meta = vm.open_vault_with_recovery(
                        recovery, self.vault_path,
                        progress_cb=self.progressed.emit,
                    )
                    if meta is None:
                        raise _ConnectError(
                            "恢复码无效，或该位置不存在带恢复码的密库"
                        )
                    return meta, vm.recovered_password or ""
                meta = vm.open_vault(
                    pw, self.vault_path,
                    progress_cb=self.progressed.emit,
                )
                if meta is None:
                    # 区分"位置无密库"与"密码错误"，避免误导性报错
                    if not vm.has_vault(self.vault_path):
                        raise _ConnectError("该位置不存在密库，请检查密库位置记录")
                    raise _ConnectError("主密码错误，请重试")
                return meta, pw
            except _ConnectError:
                raise
            except Exception as e:  # noqa: BLE001
                raise _ConnectError(f"连接失败：{e}")

        def on_done(result):
            meta, final_pw = result
            self._connect_btn.setEnabled(True)
            self._busy_bar.setVisible(False)
            self.backend = backend
            self.metadata = meta
            self.session = Session(final_pw)
            self.accept()

        def on_error(msg: str):
            self._connect_btn.setEnabled(True)
            self._busy_bar.setVisible(False)
            self._status.setStyleSheet(f"color: {semantic_color('err')};")
            self._status.setText(msg or "未知错误")

        # 同步模式（测试）原地执行；异步模式持有线程引用防 GC
        self._op_thread = run_busy(
            op, on_done, on_error, parent=self, sync=self.sync_ops,
        )
