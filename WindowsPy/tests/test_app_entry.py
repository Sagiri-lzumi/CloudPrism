"""app 入口防重入回归测试（AST 静态断言）。

打包态下并行加密的 ProcessPoolExecutor 以 spawn 方式拉起子进程，子进程会
重新执行应用入口；若 main() 首行缺少 multiprocessing.freeze_support()，
每个子进程都会启动一个完整的 GUI 主窗口（重复弹窗）且无法执行加密任务，
导致上传卡死。本测试用 AST 静态分析锁定该守卫位置，防止回归。
"""

from __future__ import annotations

import ast
from pathlib import Path

# WindowsPy/src/cloudprism/app.py（本文件位于 WindowsPy/tests/）
APP_PY = Path(__file__).resolve().parents[1] / "src" / "cloudprism" / "app.py"


def _find_main(tree: ast.Module) -> ast.FunctionDef:
    """从模块顶层找到 main() 函数定义。"""
    for node in tree.body:
        if isinstance(node, ast.FunctionDef) and node.name == "main":
            return node
    raise AssertionError("app.py 缺少顶层 main() 函数")


def _first_statement(fn: ast.FunctionDef) -> ast.stmt:
    """函数体首条语句（跳过 docstring）。"""
    first = fn.body[0]
    if (
        len(fn.body) > 1
        and isinstance(first, ast.Expr)
        and isinstance(first.value, ast.Constant)
        and isinstance(first.value.value, str)
    ):
        return fn.body[1]
    return first


def test_main_first_statement_is_freeze_support():
    """main() 首条语句必须为 multiprocessing.freeze_support()。"""
    tree = ast.parse(APP_PY.read_text(encoding="utf-8"))
    first = _first_statement(_find_main(tree))
    # 形如：multiprocessing.freeze_support()
    assert isinstance(first, ast.Expr), "freeze_support 必须是 main() 首条语句"
    call = first.value
    assert isinstance(call, ast.Call)
    assert isinstance(call.func, ast.Attribute)
    assert call.func.attr == "freeze_support"
    assert isinstance(call.func.value, ast.Name)
    assert call.func.value.id == "multiprocessing"


def test_module_imports_multiprocessing():
    """app.py 顶层导入 multiprocessing（守卫依赖）。"""
    tree = ast.parse(APP_PY.read_text(encoding="utf-8"))
    imported = any(
        isinstance(n, ast.Import)
        and any(alias.name == "multiprocessing" for alias in n.names)
        for n in tree.body
    )
    assert imported, "app.py 顶层缺少 import multiprocessing"


def test_top_level_frozen_guard_before_gui_imports():
    """打包态子进程早退守卫必须位于任何 PySide6 导入之前。

    守卫（is_forking 拦截 + freeze_support + sys.exit）若晚于 Qt 导入，
    子进程会先加载整套 Qt 栈再退出，退出时 C 扩展析构引发崩溃。"""
    tree = ast.parse(APP_PY.read_text(encoding="utf-8"))
    guard_idx = None
    for idx, node in enumerate(tree.body):
        # 守卫块：if getattr(sys, "frozen", False) and ... is_forking(...)
        if isinstance(node, ast.If):
            snippet = ast.dump(node.test)
            if "frozen" in snippet and "is_forking" in snippet:
                guard_idx = idx
                break
    assert guard_idx is not None, "缺少打包态子进程顶层早退守卫"
    # 任何 PySide6 / qfluentwidgets / cloudprism.gui 导入必须在守卫之后
    for idx, node in enumerate(tree.body):
        names: list[str] = []
        if isinstance(node, ast.Import):
            names = [a.name for a in node.names]
        elif isinstance(node, ast.ImportFrom) and node.module:
            names = [node.module]
        if any(
            n.startswith(("PySide6", "qfluentwidgets", "cloudprism.gui"))
            for n in names
        ):
            assert idx > guard_idx, "GUI 导入必须晚于子进程早退守卫"

