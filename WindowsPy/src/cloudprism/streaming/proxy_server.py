"""本地流式解密代理服务器。

拦截播放器（QMediaPlayer/外部播放器）的 HTTP Range 请求，按需从存储后端
拉取密文分块、内存解密、回推明文流。明文不落盘。

工作流程：
    1. 播放器 GET /<remote_path>，携带 Range: bytes=start-end
    2. 代理取文件头（缓存），派生密钥
    3. RangeMapper 把明文区间换算为密文区间
    4. 后端 download_range 拉取密文块
    5. AesCtrStreamCipher 解密并切片到请求区间
    6. 回 206 Partial Content + Content-Range，明文仅在内存

详见 Plan/Plan.md §2.7 与 Plan/Framework_Windows_Client.md §5.8。
"""

from __future__ import annotations

import logging
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from io import BytesIO
from typing import Any

from cloudprism.core.session import Session
from cloudprism.crypto.header import FileHeader, HeaderError
from cloudprism.crypto.stream_cipher import AesCtrStreamCipher
from cloudprism.storage.backend import StorageBackend
from cloudprism.streaming.range_mapper import RangeMapper

logger = logging.getLogger(__name__)


# Range 头解析正则：bytes=start-end（end 可省略）
_RANGE_RE = re.compile(r"bytes=(\d*)-(\d*)")

# 单次 GET 响应字节上限：开口区间（如播放器首请求 bytes=0-）按此截断为
# 多个有界 206，避免代理一次性拉取并全量解密整个密文文件（大视频 +
# 网盘后端下会造分钟级首字节延迟与等量内存）。播放器按 RFC 自动续请。
MAX_RESPONSE_BYTES = 2 * 1024 * 1024


def parse_range_header(
    range_header: str | None, total: int
) -> tuple[int, int] | None:
    """解析 Range 请求头为 [start, end]（含两端，明文偏移）。

    无 Range 头或解析失败时返回整个文件 [0, total-1]；
    end 省略（bytes=N-）时取到文件末尾；
    start 越界（>= total）时返回 None（调用方应回 416，
    不能回退整文件，否则尾部探测也会触发全量下载）。
    """
    if not range_header or total <= 0:
        return (0, max(total - 1, 0))
    m = _RANGE_RE.search(range_header)
    if not m:
        return (0, max(total - 1, 0))
    s_str, e_str = m.group(1), m.group(2)
    start = int(s_str) if s_str else 0
    end = int(e_str) if e_str else total - 1
    # 限制在文件范围内
    end = min(end, total - 1)
    if start > end or start >= total:
        return None
    return (start, end)


class ProxyState:
    """代理共享状态：会话、后端、文件头缓存。

    由外部注入；线程安全（文件头缓存加锁）。
    """

    def __init__(self, session: Session, backend: StorageBackend) -> None:
        self.session = session
        self.backend = backend
        self._header_cache: dict[str, FileHeader] = {}
        self._size_cache: dict[str, int] = {}
        self._lock = threading.Lock()

    def get_header(self, remote_path: str) -> FileHeader | None:
        """取（或拉取并缓存）文件头。

        文件不存在或非合法密文 -> None（真 404）；
        网络等后端异常向上抛出，由处理器回 502（不能把瞬时故障伪装成文件不存在）。
        """
        with self._lock:
            if remote_path in self._header_cache:
                return self._header_cache[remote_path]
        try:
            # 前 64 字节足够覆盖 51B 头
            head = self.backend.download_range(remote_path, 0, 63)
            header = FileHeader.parse(BytesIO(head))
        except (HeaderError, FileNotFoundError, ValueError):
            return None
        with self._lock:
            self._header_cache[remote_path] = header
        return header

    def get_size(self, remote_path: str) -> int:
        """取（或缓存）密文文件大小，避免每请求一次 HEAD/list 往返。"""
        with self._lock:
            if remote_path in self._size_cache:
                return self._size_cache[remote_path]
        size = self.backend.get_size(remote_path)
        with self._lock:
            self._size_cache[remote_path] = size
        return size


class DecryptingProxyHandler(BaseHTTPRequestHandler):
    """解密代理请求处理器。

    类属性 state 由 start_proxy 注入（http.server 的 handler 机制）。
    """

    # 共享状态（由外部注入）
    state: ProxyState = None  # type: ignore[assignment]

    # 协议版本：支持 HTTP/1.1 keep-alive，播放器体验更好
    protocol_version = "HTTP/1.1"

    # ------------------------------------------------------------------
    # 工具方法
    # ------------------------------------------------------------------

    def _remote_path(self) -> str:
        """从 URL 提取后端相对路径（去掉前导 / 与查询串）。"""
        path = self.path.split("?", 1)[0].lstrip("/")
        # URL 解码（路径中的中文/特殊字符）
        from urllib.parse import unquote

        return unquote(path)

    def _send_bytes(
        self,
        status: int,
        data: bytes,
        content_type: str = "application/octet-stream",
        extra_headers: dict[str, str] | None = None,
    ) -> None:
        """发送字节响应。"""
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Accept-Ranges", "bytes")
        for k, v in (extra_headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        if data or self.command != "HEAD":
            self.wfile.write(data)

    # ------------------------------------------------------------------
    # HTTP 方法
    # ------------------------------------------------------------------

    def do_HEAD(self) -> None:
        """HEAD：返回明文总大小（播放器探明时长用）。"""
        remote = self._remote_path()
        try:
            header = self.state.get_header(remote)
            if header is None:
                self._send_bytes(404, b"not found")
                return
            cipher_size = self.state.get_size(remote)
        except (BrokenPipeError, ConnectionResetError):
            return
        except Exception:
            logger.warning("HEAD 后端异常 %s", remote, exc_info=True)
            self._send_bytes(502, b"backend error")
            return
        total = RangeMapper.plaintext_total(header, cipher_size)
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(total))
        self.send_header("Accept-Ranges", "bytes")
        self.end_headers()

    def do_GET(self) -> None:
        """GET：拦截 Range，按需拉密文块、内存解密、回明文流。

        单次响应受 MAX_RESPONSE_BYTES 上限：超限区间截断后回 206，
        播放器自动续请下一段（避免一次性拉取并全量解密整文件）。
        """
        remote = self._remote_path()
        try:
            header = self.state.get_header(remote)
            if header is None:
                self._send_bytes(404, b"not found")
                return

            # 明文总量（大小经缓存，不再每请求一次后端往返）
            cipher_size = self.state.get_size(remote)
            total = RangeMapper.plaintext_total(header, cipher_size)

            # 空文件
            if total <= 0:
                self._send_bytes(
                    206, b"", extra_headers={"Content-Range": f"bytes 0-0/0"}
                )
                return

            # 解析 Range（明文偏移，含两端）；越界回 416 而非整文件
            rng = parse_range_header(self.headers.get("Range"), total)
            if rng is None:
                self._send_bytes(
                    416, b"",
                    extra_headers={"Content-Range": f"bytes */{total}"},
                )
                return
            start, end = rng

            # 上限截断：播放器的开口区间（如 bytes=0-）变为多次有界请求，
            # Content-Range 反映实际发送段，总长不变，播放器按续请拼接。
            end = min(end, start + MAX_RESPONSE_BYTES - 1)

            # 换算密文区间并拉取
            cr = RangeMapper.plaintext_to_cipher(header, start, end + 1)
            # ct_end 上限为密文文件末尾
            ct_end_incl = min(cr.ct_end, cipher_size) - 1
            ct = self.state.backend.download_range(remote, cr.ct_start, ct_end_incl)

            # 内存解密并切片到 [start, end]
            key = self.state.session.derive_key(header.salt)
            cipher = AesCtrStreamCipher(key, header.iv)
            pt = cipher.decrypt_range(ct, cr.first_block, start, end + 1)

            # 206 响应
            self._send_bytes(
                206,
                pt,
                extra_headers={
                    "Content-Range": f"bytes {start}-{end}/{total}",
                },
            )
        except (BrokenPipeError, ConnectionResetError):
            # 播放器中止/断开连接（切换文件、seek 属常态），安静退出
            return
        except Exception:
            logger.warning("GET 处理失败 %s", remote, exc_info=True)
            try:
                self._send_bytes(502, b"backend error")
            except Exception:
                pass

    def log_message(self, format: str, *args: Any) -> None:
        """静默默认访问日志（避免刷屏；需要时再开）。"""
        pass


def start_proxy(
    session: Session,
    backend: StorageBackend,
    host: str = "127.0.0.1",
    port: int = 0,
) -> tuple[ThreadingHTTPServer, int]:
    """启动本地解密代理（含后台服务线程）。

    参数:
        session: 主密码会话
        backend: 存储后端
        host: 监听地址，默认仅本机
        port: 监听端口，0 = 自动分配

    返回:
        (server, port)：port 为实际监听端口；
        停止代理调用 server.shutdown() + server.server_close()
        （或使用 stop_proxy 辅助函数）
    """
    # 每次调用生成独立 handler 子类并绑定各自状态，
    # 避免多个代理实例共存时共享类属性 state 互相覆盖
    handler = type(
        "BoundDecryptingProxyHandler",
        (DecryptingProxyHandler,),
        {"state": ProxyState(session, backend)},
    )
    server = ThreadingHTTPServer((host, port), handler)
    actual_port = server.server_address[1]
    # 后台线程运行服务循环；daemon 线程随主进程退出
    service_thread = threading.Thread(
        target=server.serve_forever, daemon=True, name="cloudprism-proxy"
    )
    service_thread.start()
    return server, actual_port


def stop_proxy(server: ThreadingHTTPServer) -> None:
    """停止代理：退出服务循环并关闭监听套接字。"""
    server.shutdown()       # 停止 serve_forever 循环
    server.server_close()   # 关闭监听套接字
