// Package cryptox 实现 CloudPrism 加密协议的全部密码学原语：
// PBKDF2 密钥派生、AES-CTR 随机访问流加密、AES-GCM（12/16 两种 nonce 长度）、
// .cpenc 文件头编解码、文件名加密与 Vault Marker 创建/校验。
//
// # 分层约束
//
// 本包与 pkg/protocol 共同构成 Go 端的最内层：除 protocol 外不 import 任何
// 项目内其它包，只依赖标准库，因而可独立编译与单测，未来抽出到 Android 端
// 无需改动一行。上层（session / pipeline / streaming / vault）只能经本包
// 触碰密码学，禁止自行拼装格式字节。
//
// # 与 WindowsPy 的兼容契约
//
// 所有格式与算法均以 WindowsPy/src/cloudprism 下的 Python 代码为唯一真源，
// 每个函数都标注了对应源文件与行号。字节级兼容由 *_test.go 中的黄金向量锁定：
// 这些十六进制常量由 Python 侧真实运行一次后打印得到（不是 Go 侧自查拼装），
// 因此「Go 与 Python 产出逐字节相等」是被实证而非被推断的。
//
// # 两条最易踩的坑
//
//  1. GCM nonce 长度分叉：Vault Marker 与恢复块用 16 字节（NewGCM16），
//     文件名与缩略图缓存用 12 字节（NewGCM12）。混用即全盘失效。
//  2. CTR 分段必须 16 字节对齐：各段独立从块首生成密钥流，未对齐会让该段
//     整体错位 offset%16 字节，密文永久损坏且解密不报错（CTR 无完整性校验）。
//     WindowsPy 端曾因此在 ≥8MiB 的非对齐文件上静默损坏数据，已于阶段 0 修复；
//     Go 端由 pkg/pipeline 强制 1MiB 对齐分段，绝不复刻该缺陷。
package cryptox
