"""便携数据路径定义。

便携化语义：设置、凭证等持久化数据一律放在程序目录旁的 ``data/`` 子目录，
跟随程序目录走——单文件 exe / 绿色免安装版拷走即整体迁移；不写注册表，
不写 %APPDATA%，不污染系统。

打包态（onefile / onedir）以 exe 所在目录为准（不能用 onefile 的
``_MEIPASS`` 临时解压目录，进程退出即销毁）；源码运行以项目
``Windows/`` 目录为准，开发期同样跟随项目目录。
"""

from __future__ import annotations

import os
import sys


def app_dir() -> str:
    """程序所在目录。

    - 打包态：``sys.executable`` 的目录（onefile 与 onedir 均正确）
    - 源码运行：本文件上溯四级（core/ -> cloudprism/ -> src/ -> Windows/）
    """
    if getattr(sys, "frozen", False):
        return os.path.dirname(os.path.abspath(sys.executable))
    return os.path.dirname(
        os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    )


def data_dir(create: bool = False) -> str:
    """程序目录旁的 ``data/`` 数据目录。

    ``create=True`` 时确保目录存在（写入时机调用）；创建失败（目录不可写）
    不抛异常，由上层降级为"不持久化但照常运行"。
    """
    d = os.path.join(app_dir(), "data")
    if create:
        try:
            os.makedirs(d, exist_ok=True)
        except OSError:
            # 程序目录不可写（如 Program Files）：不阻塞启动，设置降级为不持久化
            pass
    return d


def config_file(create: bool = False) -> str:
    """应用设置文件路径 ``data/cloudprism.ini``。"""
    if create:
        data_dir(create=True)
    return os.path.join(data_dir(), "cloudprism.ini")


def baidu_credential_file(create: bool = False) -> str:
    """百度网盘凭证文件路径 ``data/baidu.json``（内容为 DPAPI 加密）。"""
    if create:
        data_dir(create=True)
    return os.path.join(data_dir(), "baidu.json")


def qfluent_config_file(create: bool = False) -> str:
    """qfluentwidgets 库 qconfig 落盘路径 ``data/qfluent_config.json``。

    库内部主题配置，可随时重建；仅为避免库默认写工作目录而重定向。
    """
    if create:
        data_dir(create=True)
    return os.path.join(data_dir(), "qfluent_config.json")


def temp_dir(create: bool = False) -> str | None:
    """加密/索引管线的临时文件目录 ``data/tmp/``（便携自管）。

    不用系统 %TEMP%：部分沙箱/受管环境下 %TEMP% 的 ACL 不完整，
    mkstemp 会遭拒绝访问；改落产品自己管理的数据目录，
    跟随程序目录整体迁移，与便携化语义一致。
    目录创建失败时返回 None，调用方退回系统默认临时目录。
    """
    d = os.path.join(data_dir(), "tmp")
    if create:
        try:
            os.makedirs(d, exist_ok=True)
        except OSError:
            return None
    return d
