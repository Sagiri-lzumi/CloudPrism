"""密库快速连接对话框。

从最近密库记录一键重连：后端参数来自记录，仅需输入主密码
（WebDAV 后端另需其服务器密码）。主密码与服务器密码均不落盘。
"""

from __future__ import annotations

from PySide6.QtWidgets import (
    QDialog,
    QFormLayout,
    QHBoxLayout,
    QLabel,
    QVBoxLayout,
)

# Fluent 组件（均继承自对应 Qt 原生控件，标准 API 全兼容）
from qfluentwidgets import CheckBox, LineEdit, PrimaryPushButton, PushButton

from cloudprism.core.backend_factory import build_backend_from_params
from cloudprism.core.session import Session
from cloudprism.core.vault_manager import VaultManager
from cloudprism.gui.theme import semantic_color


class QuickConnectDialog(QDialog):
    """最近密库一键重连对话框。

    连接成功后产物存于实例属性：
      backend / metadata / session
    """

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

        # 连接成功产物
        self.backend = None
        self.metadata = None
        self.session: Session | None = None

        lay = QVBoxLayout(self)

        # ---- 密库摘要（只读） ----
        summary = QLabel(
            f"类型：{record.get('label', record.get('backend_type', '?'))}\n"
            f"位置：{record.get('path', '-')}\n"
            f"库：{record.get('vault_name', '-')} · "
            f"上次连接：{record.get('last_used', '-')}",
            self,
        )
        summary.setStyleSheet(f"color: {semantic_color('muted')}; font-size: 13px;")
        summary.setWordWrap(True)
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

        self._status = QLabel("", self)
        self._status.setWordWrap(True)
        self._status.setStyleSheet(f"color: {semantic_color('err')};")
        lay.addWidget(self._status)

        lay.addStretch()

    # ------------------------------------------------------------------
    # 连接动作
    # ------------------------------------------------------------------

    def _on_recovery_toggled(self, checked: bool) -> None:
        """展开/收起恢复码输入框；勾选后主密码非必填。"""
        self._recovery_edit.setVisible(checked)
        if checked:
            self._status.setText("")

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
        self._status.setText("")
        try:
            backend = self._factory(
                btype,
                local_dir=path if btype == "local" else "",
                webdav_url=path if btype == "webdav" else "",
                webdav_user=self._record.get("webdav_user", ""),
                webdav_pass=webdav_pass,
            )
            vm = VaultManager(backend)
            if use_recovery:
                meta = vm.open_vault_with_recovery(recovery)
                if meta is None:
                    self._status.setText("恢复码无效，或该密库未启用恢复码")
                    return
                pw = vm.recovered_password or ""
            else:
                meta = vm.open_vault(pw)
                if meta is None:
                    self._status.setText("主密码错误，或该位置不存在密库")
                    return
            self.backend = backend
            self.metadata = meta
            self.session = Session(pw)
            self.accept()
        except Exception as e:  # noqa: BLE001
            self._status.setText(f"连接失败：{e}")
        finally:
            self._connect_btn.setEnabled(True)
