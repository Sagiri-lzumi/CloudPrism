// Package pipeline 提供加密上传 / 下载解密的数据管线。
//
// 与 Python 端 Encryptor / Decryptor / RangeMapper 的职责对齐
// （WindowsPy/src/cloudprism/core/encryptor.py、core/decryptor.py、
// streaming/range_mapper.py），但并发模型不同：Go 端用 goroutine +
// ReaderAt/WriterAt 原地并行，无进程池开销，分段粒度收紧到 ShardAlign
// （1MiB），峰值内存 = workers × 1MiB。
//
// AES-CTR 的随机访问特性是并行的根基：计数器块索引 = 主体内偏移 / 16
// （文件头不参与计数，两端一致），分片偏移恒为 16 的倍数，各片独立从块首
// 生成密钥流，并行产物与单核顺序产物逐字节一致 —— CTR 无完整性校验，
// 任何错位都静默损坏数据，对齐纪律由 shard.go 的常量与测试共同钉死
// （WindowsPy 端曾因分段未对齐静默损坏 ≥8MiB 非对齐文件，阶段 0 已修）。
//
// 本包不感知后端类型（Backend 接口只搬运字节），不做上传调度（归传输
// 队列），只回答一个问题：给定输入，产出逐字节正确的密文/明文。
package pipeline

import "github.com/Sagiri-lzumi/cloudprism/windowsgo/pkg/protocol"

// CipherRange 是密文文件中的字节区间（含文件头偏移）。
// 对照 range_mapper.py:25-31 的 CipherRange。
//
// CtStart / CtEnd 是密文文件内的偏移（含头），可直接用于后端
// DownloadRange 的 [CtStart, CtEnd]（含两端）；FirstBlock 是解密时
// 定位计数器的明文主体块索引。
type CipherRange struct {
	CtStart    int64 // 密文起始偏移（含）
	CtEnd      int64 // 密文结束偏移（不含）
	FirstBlock int64 // 起始块索引（解密时定位计数器）
}

// PlaintextToCipher 把明文区间 [start, end) 换算为密文区间。
//
// 数学（AES-CTR，块大小 16，HL = header_length）：
//
//	first_block = start // BS
//	last_block  = (end - 1) // BS
//	密文起始 = HL + first_block*BS
//	密文结束 = HL + (last_block+1)*BS   （不含）
//
// 两端（Windows/Android）必须使用同一换算数学；调用方需保证 start < end
// 且 end 不超过明文总长（越界由调用方 clamp）。
// 对照 range_mapper.py:40-64。
func PlaintextToCipher(headerLen, start, end int64) CipherRange {
	bs := int64(protocol.BlockSize)
	firstBlock := start / bs
	lastBlock := (end - 1) / bs
	return CipherRange{
		CtStart:    headerLen + firstBlock*bs,
		CtEnd:      headerLen + (lastBlock+1)*bs,
		FirstBlock: firstBlock,
	}
}

// PlaintextTotal 明文总长度 = 密文文件大小 - 头部长度（流加密无填充）。
// 对照 range_mapper.py:67-69 的 plaintext_total。
func PlaintextTotal(headerLen, cipherSize int64) int64 {
	return cipherSize - headerLen
}
