"""百度网盘 OAuth2 授权对话框。

流程（授权码模式，redirect_uri=oob）：
  1. 用户填写开放平台申请到的 Appid / AppKey / SecretKey（SignKey 可选）；
  2. 点击「打开授权页面」-> 浏览器打开百度授权页；
  3. 用户登录并同意后，页面显示一次性 code，粘贴回本对话框；
  4. 点击「完成授权」-> 用 code 换取 access_token（30 天）与
     refresh_token（长效），经 DPAPI 加密后写入本地凭证存储。

凭证申请流程详见 Plan/百度网盘开放平台申请指南.md。
"""

from __future__ import annotations

import webbrowser

import requests
from PySide6.QtCore import Qt
from PySide6.QtWidgets import (
    QDialog,
    QFormLayout,
    QHBoxLayout,
    QLabel,
    QLineEdit,
    QPushButton,
    QVBoxLayout,
)

from cloudprism.storage.baidu_backend import BaiduCredentialStore

# 授权地址（oob 模式：无回调，页面直接展示 code）
AUTHORIZE_URL = (
    "https://openapi.baidu.com/oauth/2.0/authorize"
    "?response_type=code"
    "&client_id={app_key}"
    "&redirect_uri=oob"
    "&scope=basic,netdisk"
    "&device_id={app_id}"
)
TOKEN_URL = "https://openapi.baidu.com/oauth/2.0/token"


def build_auth_url(app_key: str, app_id: str = "") -> str:
    """构造浏览器授权页地址。"""
    return AUTHORIZE_URL.format(app_key=app_key, app_id=app_id)


def exchange_code(
    app_key: str,
    secret_key: str,
    code: str,
    session: requests.Session | None = None,
) -> dict:
    """用授权码换取 token，返回含 access_token/refresh_token 的字典。"""
    s = session or requests.Session()
    r = s.get(
        TOKEN_URL,
        params={
            "grant_type": "authorization_code",
            "code": code.strip(),
            "client_id": app_key.strip(),
            "client_secret": secret_key.strip(),
            "redirect_uri": "oob",
        },
    )
    data = r.json()
    if "access_token" not in data:
        raise ConnectionError(f"授权失败：{data}")
    return data


class BaiduAuthDialog(QDialog):
    """凭证填写 + 浏览器授权 + code 换 token 的一站式对话框。"""

    def __init__(
        self,
        store: BaiduCredentialStore | None = None,
        parent=None,
        http_session: requests.Session | None = None,
    ) -> None:
        super().__init__(parent)
        self.setWindowTitle("百度网盘授权")
        self.setMinimumWidth(480)
        self._store = store or BaiduCredentialStore()
        self._http = http_session
        self.token_data: dict | None = None  # 授权成功后的 token 信息

        lay = QVBoxLayout(self)

        hint = QLabel(
            "凭证获取方法见《Plan/百度网盘开放平台申请指南》。\n"
            "AppKey / SecretKey 仅保存在本机（DPAPI 加密），不会上传。",
            self,
        )
        hint.setStyleSheet("color: #5c5c5c; font-size: 12px;")
        hint.setWordWrap(True)
        lay.addWidget(hint)

        # ---- 凭证输入 ----
        form = QFormLayout()
        self._appid_edit = QLineEdit(self)
        self._appkey_edit = QLineEdit(self)
        self._secret_edit = QLineEdit(self)
        self._secret_edit.setEchoMode(QLineEdit.EchoMode.Password)
        self._signkey_edit = QLineEdit(self)
        self._signkey_edit.setEchoMode(QLineEdit.EchoMode.Password)
        form.addRow("Appid：", self._appid_edit)
        form.addRow("AppKey：", self._appkey_edit)
        form.addRow("SecretKey：", self._secret_edit)
        form.addRow("SignKey（可选）：", self._signkey_edit)
        lay.addLayout(form)

        # 已保存凭证回填（SecretKey 除外，需用户确认时重新输入亦可留空复用）
        saved = self._store.load()
        if saved:
            self._appid_edit.setText(saved.get("app_id", ""))
            self._appkey_edit.setText(saved.get("app_key", ""))
            self._secret_edit.setText(saved.get("secret_key", ""))
            self._signkey_edit.setText(saved.get("sign_key", ""))

        # ---- 第一步：打开授权页 ----
        step1_row = QHBoxLayout()
        self._open_btn = QPushButton("第一步：打开授权页面", self)
        self._open_btn.clicked.connect(self._open_auth_page)
        step1_row.addWidget(self._open_btn)
        step1_row.addStretch()
        lay.addLayout(step1_row)

        # ---- 第二步：粘贴 code ----
        lay.addWidget(QLabel("第二步：授权后页面会显示一串 code，粘贴到下方：", self))
        self._code_edit = QLineEdit(self)
        self._code_edit.setPlaceholderText("授权页面显示的 code（一次性）")
        lay.addWidget(self._code_edit)

        # ---- 完成授权 ----
        self._finish_btn = QPushButton("完成授权", self)
        self._finish_btn.clicked.connect(self._finish_auth)
        lay.addWidget(self._finish_btn)

        self._status = QLabel("", self)
        self._status.setWordWrap(True)
        lay.addWidget(self._status)

        lay.addStretch()

    # ------------------------------------------------------------------
    # 内部动作
    # ------------------------------------------------------------------

    def _collect_credentials(self) -> dict | None:
        """收集并校验凭证输入。"""
        creds = {
            "app_id": self._appid_edit.text().strip(),
            "app_key": self._appkey_edit.text().strip(),
            "secret_key": self._secret_edit.text().strip(),
            "sign_key": self._signkey_edit.text().strip(),
        }
        if not creds["app_key"] or not creds["secret_key"]:
            self._status.setText("请先填写 AppKey 与 SecretKey")
            self._status.setStyleSheet("color: #c00;")
            return None
        return creds

    def _open_auth_page(self) -> None:
        """打开浏览器授权页。"""
        creds = self._collect_credentials()
        if creds is None:
            return
        url = build_auth_url(creds["app_key"], creds["app_id"])
        webbrowser.open(url)
        self._status.setText("已在浏览器打开授权页；登录并同意后复制 code")
        self._status.setStyleSheet("color: #5c5c5c;")

    def _finish_auth(self) -> None:
        """用 code 换 token 并加密保存。"""
        creds = self._collect_credentials()
        if creds is None:
            return
        code = self._code_edit.text().strip()
        if not code:
            self._status.setText("请粘贴授权页面显示的 code")
            self._status.setStyleSheet("color: #c00;")
            return
        self._finish_btn.setEnabled(False)
        self._status.setText("正在换取 token…")
        try:
            import time

            data = exchange_code(
                creds["app_key"], creds["secret_key"], code, session=self._http
            )
            creds.update(
                access_token=data["access_token"],
                refresh_token=data.get("refresh_token", ""),
                expires_at=time.time() + int(data.get("expires_in", 0)),
            )
            self._store.save(creds)
            self.token_data = data
            self._status.setText("✓ 授权成功，凭证已加密保存")
            self._status.setStyleSheet("color: #0a0; font-weight: bold;")
            self.accept()
        except Exception as e:  # noqa: BLE001
            self._status.setText(f"授权失败：{e}")
            self._status.setStyleSheet("color: #c00;")
            self._finish_btn.setEnabled(True)
