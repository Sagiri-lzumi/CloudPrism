"""流式解密代理子包。

本地微型 HTTP 代理拦截播放器的 Range 请求，按需从后端拉取密文分块、
内存解密、回推明文流。明文不落盘，仅作内存流短暂存在。

详见 Plan/Plan.md §2.7 与 Plan/Framework_Windows_Client.md §5.8。
"""
