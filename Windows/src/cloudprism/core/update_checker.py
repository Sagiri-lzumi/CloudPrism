"""检查更新：查询 CloudPrism GitHub 仓库的最新 release。

使用 GitHub Releases 公共 API 匿名调用（公开仓库无需账号，匿名限额
60 次/小时，手动检查场景足够）；仅做版本对比与下载页跳转，不自动下载。
仓库未来若转为私有，需要在此补充 token 认证。
"""

from __future__ import annotations

import requests

# 最新 release 查询接口（匿名可用）
_RELEASE_API = "https://api.github.com/repos/Sagiri-lzumi/CloudPrism/releases/latest"
# Releases 下载页（404/无 html_url 时的引导链接）
RELEASES_PAGE = "https://github.com/Sagiri-lzumi/CloudPrism/releases"


def normalize_tag(tag: str) -> str:
    """归一化版本标签：去空白与 v/V 前缀（如 ``v1.0.0`` → ``1.0.0``）。"""
    return (tag or "").strip().lstrip("vV")


def compare_versions(cur: str, latest: str) -> int:
    """按点分段数字比较两个版本号。

    返回 -1 / 0 / 1，分别表示 cur 小于 / 等于 / 大于 latest。
    缺位段补 0（``1.0`` == ``1.0.0``）；非数字段容错为 0，
    避免 ``1.0.0-beta`` 之类后缀导致解析异常。
    """

    def parts(v: str) -> list[int]:
        out = []
        for seg in normalize_tag(v).split("."):
            try:
                out.append(int(seg))
            except ValueError:
                out.append(0)
        return out

    a, b = parts(cur), parts(latest)
    n = max(len(a), len(b))
    a += [0] * (n - len(a))
    b += [0] * (n - len(b))
    return (a > b) - (a < b)


def fetch_latest_release(timeout: float = 8.0) -> dict:
    """请求 GitHub 获取最新 release 信息。

    返回 ``{"tag": 归一化版本号, "name": 发布名, "url": 下载页}``。

    异常:
        LookupError: 仓库尚无任何 release（404）
        ConnectionError: 网络不可达 / 限流 / 状态码异常 / 响应非 JSON
    """
    try:
        r = requests.get(
            _RELEASE_API,
            timeout=timeout,
            headers={"Accept": "application/vnd.github+json"},
        )
    except requests.RequestException as e:
        raise ConnectionError(f"网络请求失败：{e}") from e
    if r.status_code == 404:
        raise LookupError("仓库尚未发布任何 release")
    if r.status_code == 403:
        raise ConnectionError("GitHub 请求频率受限（限流），请稍后重试")
    if r.status_code != 200:
        raise ConnectionError(f"GitHub 返回异常状态码 {r.status_code}")
    try:
        out = r.json()
    except ValueError as e:
        raise ConnectionError("GitHub 返回内容不是 JSON") from e
    tag = normalize_tag(str(out.get("tag_name", "")))
    return {
        "tag": tag,
        "name": out.get("name") or out.get("tag_name") or tag,
        "url": out.get("html_url") or RELEASES_PAGE,
    }
