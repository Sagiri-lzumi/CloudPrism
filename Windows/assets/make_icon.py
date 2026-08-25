"""生成 CloudPrism 应用图标（一次性工具脚本）。

绘制内容：Fluent 蓝色渐变圆角底 + 白色棱镜三角 + 色散光束。
产出：icon.png（256x256）与 icon.ico（PNG 压缩条目）。

用法：.venv/Scripts/python.exe Windows/assets/make_icon.py
"""

from __future__ import annotations

import os
import struct

from PySide6.QtCore import QPointF, QRectF, Qt
from PySide6.QtGui import (
    QBrush,
    QColor,
    QGuiApplication,
    QImage,
    QLinearGradient,
    QPainter,
    QPainterPath,
    QPen,
    QPolygonF,
)

SIZE = 256


def draw_icon(img: QImage) -> None:
    """在给定画布上绘制图标。"""
    p = QPainter(img)
    p.setRenderHint(QPainter.RenderHint.Antialiasing, True)
    img.fill(Qt.GlobalColor.transparent)

    # ---- 背景：蓝色渐变圆角矩形 ----
    grad = QLinearGradient(0, 0, SIZE, SIZE)
    grad.setColorAt(0.0, QColor("#1b7fd4"))
    grad.setColorAt(1.0, QColor("#005a9e"))
    path = QPainterPath()
    r = SIZE * 0.22
    path.addRoundedRect(QRectF(0, 0, SIZE, SIZE), r, r)
    p.setPen(Qt.PenStyle.NoPen)
    p.fillPath(path, QBrush(grad))

    # ---- 棱镜三角形（白色，微微透明） ----
    tri = QPolygonF(
        [
            QPointF(SIZE * 0.50, SIZE * 0.22),   # 顶点
            QPointF(SIZE * 0.78, SIZE * 0.74),   # 右下
            QPointF(SIZE * 0.26, SIZE * 0.74),   # 左下
        ]
    )
    p.setPen(QPen(QColor(255, 255, 255, 235), SIZE * 0.035,
                  Qt.PenStyle.SolidLine, Qt.PenCapStyle.RoundCap,
                  Qt.PenJoinStyle.RoundJoin))
    p.setBrush(QColor(255, 255, 255, 46))
    p.drawPolygon(tri)

    # ---- 入射光束（左侧水平进入棱镜） ----
    p.setPen(QPen(QColor(255, 255, 255, 220), SIZE * 0.03,
                  Qt.PenStyle.SolidLine, Qt.PenCapStyle.RoundCap))
    p.drawLine(QPointF(SIZE * 0.08, SIZE * 0.50), QPointF(SIZE * 0.42, SIZE * 0.50))

    # ---- 色散光束（从棱镜右侧扇出） ----
    colors = ["#ff6b6b", "#ffd93d", "#6bcb77", "#4cc2ff"]
    origin = QPointF(SIZE * 0.62, SIZE * 0.50)
    angles = [16, 6, -5, -16]  # 相对水平的扇出角度（度）
    for color, ang in zip(colors, angles):
        import math
        rad = math.radians(ang)
        end = QPointF(
            origin.x() + SIZE * 0.34 * math.cos(rad),
            origin.y() - SIZE * 0.34 * math.sin(rad),
        )
        pen = QPen(QColor(color), SIZE * 0.026,
                   Qt.PenStyle.SolidLine, Qt.PenCapStyle.RoundCap)
        p.setPen(pen)
        p.drawLine(origin, end)
    p.end()


def main() -> None:
    # 离屏平台：无显示器环境下也可用 QPainter 绘制
    os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")
    QGuiApplication([])

    here = os.path.dirname(os.path.abspath(__file__))
    png_path = os.path.join(here, "icon.png")
    ico_path = os.path.join(here, "icon.ico")

    img = QImage(SIZE, SIZE, QImage.Format.Format_ARGB32)
    draw_icon(img)
    img.save(png_path, "PNG")
    print(f"已生成 {png_path}")

    # ---- 组装 ICO：PNG 压缩条目（Windows Vista+ 支持） ----
    with open(png_path, "rb") as f:
        png_bytes = f.read()
    n = 1
    # ICONDIR: reserved(2) type(2) count(2)
    header = struct.pack("<HHH", 0, 1, n)
    # ICONDIRENTRY: w,h(1B, 0=256) colors reserved planes bpp size offset
    entry = struct.pack(
        "<BBBBHHII",
        0, 0, 0, 0, 1, 32, len(png_bytes), 6 + 16 * n,
    )
    with open(ico_path, "wb") as f:
        f.write(header + entry + png_bytes)
    print(f"已生成 {ico_path}")


if __name__ == "__main__":
    main()
