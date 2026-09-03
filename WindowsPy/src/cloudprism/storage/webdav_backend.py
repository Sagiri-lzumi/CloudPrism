"""WebDAV 存储后端。

基于 requests 手写 WebDAV PROPFIND/PUT/GET Range，兼容大多数公共云盘。
与 LocalFolderBackend 实现同一套 StorageBackend 接口，可无缝互换。

download_range 即 HTTP Range 请求；upload_chunked 即分块 PUT；list_dir 即
PROPFIND depth=1。
"""

from __future__ import annotations

import xml.etree.ElementTree as ET
from typing import Iterator
from urllib.parse import quote, unquote

import requests

from cloudprism.storage.backend import RemoteEntry


# WebDAV 命名空间
_NSDAV = "{DAV:}"


class WebDavBackend:
    """WebDAV 存储后端实现。"""

    def __init__(
        self,
        base_url: str,
        auth: tuple[str, str],
        session: requests.Session | None = None,
    ) -> None:
        """初始化 WebDAV 客户端。

        参数:
            base_url: WebDAV 根地址（如 https://dav.example.com/remote.php/dav/files/user/）
            auth: (用户名, 密码)
            session: 可选复用的 requests.Session
        """
        # 去掉末尾斜杠，后续路径拼接时统一加 /
        self.base_url = base_url.rstrip("/")
        self.auth = auth
        self.session = session or requests.Session()
        self.session.auth = auth

    # ------------------------------------------------------------------
    # URL 构造
    # ------------------------------------------------------------------

    def _url(self, path: str) -> str:
        """把相对路径拼成完整 URL，路径段做百分号编码。"""
        rel = path.strip("/")
        if not rel:
            return self.base_url + "/"
        # 对每一段单独编码，保留 / 分隔符
        encoded = "/".join(quote(seg) for seg in rel.split("/"))
        return f"{self.base_url}/{encoded}"

    # ------------------------------------------------------------------
    # StorageBackend 实现
    # ------------------------------------------------------------------

    def list_dir(self, path: str) -> list[RemoteEntry]:
        """PROPFIND depth=1，解析 href/size/集合。"""
        url = self._url(path)
        headers = {"Depth": "1", "Content-Type": "application/xml"}
        # PROPFIND 请求体：请求基础属性
        body = (
            '<?xml version="1.0" encoding="utf-8"?>'
            '<D:propfind xmlns:D="DAV:">'
            "<D:prop>"
            "<D:resourcetype/>"
            "<D:getcontentlength/>"
            "<D:displayname/>"
            "</D:prop>"
            "</D:propfind>"
        )
        r = self.session.request("PROPFIND", url, headers=headers, data=body)
        if r.status_code not in (207, 200):
            raise ConnectionError(
                f"PROPFIND 失败：{r.status_code} {r.reason}"
            )
        return self._parse_propstat(r.content, path)

    def _parse_propstat(self, xml_bytes: bytes, req_path: str) -> list[RemoteEntry]:
        """解析 PROPFIND 多状态响应，返回除自身外的条目。"""
        root = ET.fromstring(xml_bytes)
        out: list[RemoteEntry] = []
        # 自身 href（用于排除 . 项）
        req_href = quote(req_path.strip("/"))

        for response in root.findall(f"{_NSDAV}response"):
            href_elem = response.find(f"{_NSDAV}href")
            if href_elem is None or not href_elem.text:
                continue
            href = unquote(href_elem.text.strip())
            # 取最后一段作为名称
            name = href.rstrip("/").rsplit("/", 1)[-1]
            # 跳过自身（href 与请求路径相同）
            if href.rstrip("/").endswith(req_href.rstrip("/") or "/"):
                continue
            if not name:
                continue

            propstat = response.find(f"{_NSDAV}propstat")
            if propstat is None:
                continue
            prop = propstat.find(f"{_NSDAV}prop")
            if prop is None:
                continue

            # 是否目录：resourcetype 含 collection
            rtype = prop.find(f"{_NSDAV}resourcetype")
            is_dir = rtype is not None and rtype.find(f"{_NSDAV}collection") is not None

            size = 0
            len_elem = prop.find(f"{_NSDAV}getcontentlength")
            if len_elem is not None and len_elem.text:
                size = int(len_elem.text)

            out.append(RemoteEntry(name=name, is_dir=is_dir, size=size))
        return out

    def get_size(self, path: str) -> int:
        """HEAD 取文件大小。"""
        url = self._url(path)
        r = self.session.head(url, allow_redirects=True)
        if r.status_code >= 400:
            raise ConnectionError(f"HEAD 失败：{r.status_code} {r.reason}")
        cl = r.headers.get("Content-Length")
        return int(cl) if cl else 0

    def download_range(self, path: str, start: int, end: int) -> bytes:
        """GET Range: bytes=start-end，返回密文段（含两端）。"""
        if start < 0 or end < start:
            raise ValueError(f"非法范围：[{start}, {end}]")
        url = self._url(path)
        headers = {"Range": f"bytes={start}-{end}"}
        r = self.session.get(url, headers=headers)
        # 服务器明确回 404：文件不存在（与网络故障区分，供上层回真 404）
        if r.status_code == 404:
            raise FileNotFoundError(f"文件不存在：{path}")
        # 206 Partial Content：直接返回请求段
        if r.status_code == 206:
            return r.content
        # 200：部分服务器不支持 Range 返回整文件，本地切出 [start, end]；
        # 不能原样返回，否则调用方按请求偏移取密文会错位解密。
        if r.status_code == 200:
            data = r.content[start : end + 1]
            if len(data) != end - start + 1:
                raise ConnectionError(
                    f"200 整文件回退切片不足：需 {end - start + 1} 字节，"
                    f"实际 {len(data)} 字节"
                )
            return data
        raise ConnectionError(
            f"GET Range 失败：{r.status_code} {r.reason}"
        )

    def upload_chunked(
        self,
        local_path: str,
        remote_path: str,
        chunk: int = 1 << 20,
    ) -> Iterator[float]:
        """从本地文件分块 PUT 上传，yield 进度 0.0~1.0。

        支持断点续传：先 HEAD 取远端已传字节数，从该偏移续传。
        注意：标准 WebDAV PUT 不支持追加，本方法在远端无内容时整体 PUT；
        续传依赖服务器支持 Content-Range（部分服务器不支持，则整体重传）。
        """
        import os

        total = os.path.getsize(local_path)
        url = self._url(remote_path)

        # 断点续传：取远端已存在大小
        offset = 0
        try:
            offset = self.get_size(remote_path)
            if offset > total:
                offset = 0
        except Exception:
            offset = 0

        with open(local_path, "rb") as f:
            if offset:
                f.seek(offset)
            remaining = total - offset
            # 确保远端文件存在（首次 PUT 用空内容占位，后续分块用 Content-Range）
            written = 0
            pos = offset
            while remaining > 0:
                buf = f.read(min(chunk, remaining))
                if not buf:
                    break
                end = pos + len(buf) - 1
                headers = {"Content-Range": f"bytes {pos}-{end}/{total}"}
                r = self.session.put(url, data=buf, headers=headers)
                if r.status_code not in (200, 201, 204, 308):
                    raise ConnectionError(
                        f"PUT 分块失败：{r.status_code} {r.reason}"
                    )
                pos += len(buf)
                written += len(buf)
                remaining -= len(buf)
                yield (offset + written) / total if total else 1.0
        if total == 0:
            # 空文件：单次 PUT 空内容
            r = self.session.put(url, data=b"")
            if r.status_code not in (200, 201, 204):
                raise ConnectionError(f"PUT 空文件失败：{r.status_code}")
            yield 1.0

    def head(self, path: str) -> int:
        """取远端文件大小（断点续传基准）。"""
        return self.get_size(path)

    def mkdir(self, path: str) -> None:
        """MKCOL 创建目录。"""
        url = self._url(path)
        r = self.session.request("MKCOL", url)
        if r.status_code not in (200, 201, 405):  # 405 = 已存在
            raise ConnectionError(f"MKCOL 失败：{r.status_code} {r.reason}")

    def rename(self, old: str, new: str) -> None:
        """MOVE 重命名/移动。"""
        url = self._url(old)
        dest_url = self._url(new)
        headers = {"Destination": dest_url, "Overwrite": "T"}
        r = self.session.request("MOVE", url, headers=headers)
        if r.status_code not in (200, 201, 204):
            raise ConnectionError(f"MOVE 失败：{r.status_code} {r.reason}")

    def delete(self, path: str) -> None:
        """DELETE 删除文件或目录。"""
        url = self._url(path)
        r = self.session.delete(url)
        if r.status_code not in (200, 204, 404):  # 404 = 已不存在
            raise ConnectionError(f"DELETE 失败：{r.status_code} {r.reason}")

    def exists(self, path: str) -> bool:
        """通过 HEAD/PROPFIND 判断是否存在。"""
        url = self._url(path)
        r = self.session.head(url, allow_redirects=True)
        return r.status_code < 400
