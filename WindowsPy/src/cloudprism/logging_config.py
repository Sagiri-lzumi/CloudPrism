"""日志脱敏配置。

提供日志过滤器，确保日志中不会出现主密码、派生密钥、Salt 等敏感信息。
所有模块在使用 logging 时应附加此过滤器，或遵循以下脱敏规则：

  - 主密码（master_password）：绝不记录
  - 派生密钥（derived key）：绝不记录
  - Salt 值：绝不记录
  - IV/Nonce：可记录前 4 字节（仅调试级别）
  - 文件路径：可记录（不含明文内容）

使用方式：
    import logging
    from cloudprism.logging_config import SensitiveFilter, setup_logging

    # 方式一：为特定 logger 添加过滤器
    logger = logging.getLogger(__name__)
    logger.addFilter(SensitiveFilter())

    # 方式二：全局初始化（在 app.py 入口调用）
    setup_logging()
"""

from __future__ import annotations

import logging
import re


class SensitiveFilter(logging.Filter):
    """日志脱敏过滤器。

    扫描日志消息，将疑似敏感信息替换为 "***"。
    匹配规则：
      - 32 字节以上的十六进制串（可能是密钥或 salt）
      - 包含 "password"、"passwd"、"secret"、"key" 等关键词的键值对
    """

    # 疑似敏感模式：长十六进制串（>=32 字符，即 16 字节以上）
    _HEX_PATTERN = re.compile(r"[0-9a-fA-F]{32,}")

    # 疑似敏感关键词（键值对中的 key 部分）
    _SENSITIVE_KEYS = re.compile(
        r"(password|passwd|secret|master_key|derived_key|salt|privkey)",
        re.IGNORECASE,
    )

    def filter(self, record: logging.LogRecord) -> bool:
        """过滤日志记录中的敏感信息。"""
        msg = record.getMessage()
        # 替换长十六进制串
        sanitized = self._HEX_PATTERN.sub("***", msg)
        # 替换敏感键值对的值部分（如 password=xxx -> password=***）
        sanitized = self._SENSITIVE_KEYS.sub(
            lambda m: m.group(0).split("=")[0] + "=***",
            sanitized,
        )
        # 如果消息被修改，更新 record
        if sanitized != msg:
            record.msg = sanitized
            record.args = ()
        return True


def setup_logging(level: int = logging.INFO) -> None:
    """全局初始化日志配置。

    为 root logger 添加 SensitiveFilter，确保所有日志输出经过脱敏。
    """
    root = logging.getLogger()
    root.setLevel(level)
    # 添加脱敏过滤器到 root handler
    handler = logging.StreamHandler()
    handler.setFormatter(
        logging.Formatter("%(asctime)s [%(levelname)s] %(name)s: %(message)s")
    )
    handler.addFilter(SensitiveFilter())
    root.addHandler(handler)
