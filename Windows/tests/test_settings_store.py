"""SettingsStore 单元测试：最近密库记录持久化。

用临时 ini 文件注入，避免触碰真实注册表/配置目录。
"""

from __future__ import annotations

import json

import pytest

pytest.importorskip("PySide6")

from PySide6.QtCore import QSettings

from cloudprism.core.settings_store import SettingsStore


@pytest.fixture
def store(tmp_path):
    """注入临时 ini 的 SettingsStore。"""
    qs = QSettings(str(tmp_path / "test.ini"), QSettings.Format.IniFormat)
    return SettingsStore(settings=qs)


def _rec(path: str, btype: str = "local") -> dict:
    return {"backend_type": btype, "label": "本地文件夹", "path": path}


def test_recent_vaults_empty_by_default(store):
    """未记录时返回空列表。"""
    assert store.recent_vaults() == []


def test_remember_adds_with_auto_key_and_time(store):
    """记录自动生成 key 与 last_used。"""
    store.remember_vault(_rec("D:/v1"))
    items = store.recent_vaults()
    assert len(items) == 1
    assert items[0]["key"] == "local|D:/v1"
    assert items[0]["last_used"]


def test_remember_dedup_moves_to_top(store):
    """重复连接同库：去重并置顶。"""
    store.remember_vault(_rec("D:/v1"))
    store.remember_vault(_rec("D:/v2"))
    store.remember_vault(_rec("D:/v1"))  # 重复
    items = store.recent_vaults()
    assert [r["path"] for r in items] == ["D:/v1", "D:/v2"]


def test_remember_caps_at_max(store):
    """超出上限淘汰最旧。"""
    for i in range(SettingsStore.MAX_RECENT_VAULTS + 3):
        store.remember_vault(_rec(f"D:/v{i}"))
    items = store.recent_vaults()
    assert len(items) == SettingsStore.MAX_RECENT_VAULTS
    assert items[0]["path"] == "D:/v10"  # 最新在前
    assert items[-1]["path"] == "D:/v3"  # 最旧三条被淘汰


def test_forget_vault(store):
    """按 key 删除。"""
    store.remember_vault(_rec("D:/v1"))
    store.remember_vault(_rec("D:/v2"))
    store.forget_vault("local|D:/v1")
    assert [r["path"] for r in store.recent_vaults()] == ["D:/v2"]
    # 删除不存在的 key 不报错
    store.forget_vault("nope")


def test_corrupted_json_returns_empty(store):
    """损坏的 JSON 容错为空列表。"""
    store._s.setValue("vaults/recent", "{not-json")
    assert store.recent_vaults() == []
    store._s.setValue("vaults/recent", json.dumps({"a": 1}))  # 非列表
    assert store.recent_vaults() == []


def test_roundtrip_unicode(store):
    """中文路径完整往返。"""
    store.remember_vault(_rec("D:/我的 密库/目录"))
    items = store.recent_vaults()
    assert items[0]["path"] == "D:/我的 密库/目录"
