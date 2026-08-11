"""CloudPrism Windows 客户端入口。

当前为最小占位实现，后续 GUI 步骤将在此启动 QApplication 与主窗口。
"""

import sys


def main() -> int:
    """程序入口。当前仅打印占位信息，后续接入 PySide6 主窗口。"""
    print("CloudPrism Windows 客户端 - 脚手架已就绪")
    return 0


if __name__ == "__main__":
    sys.exit(main())
