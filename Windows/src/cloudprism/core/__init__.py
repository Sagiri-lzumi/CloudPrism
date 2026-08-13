"""核心子包：加解密管线、主密码会话。

Encryptor/Decryptor 是加密层（crypto/）与存储层（storage/）的汇合点：
- Encryptor 读本地明文 -> 分块 CTR 加密 -> 写文件头+密文 -> 上传后端
- Decryptor 从后端下载 -> 解析文件头 -> 分块解密 -> 写本地明文

Session 持有主密码（仅内存），为加解密派生密钥。
"""
