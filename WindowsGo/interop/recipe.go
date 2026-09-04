// 明文确定性配方：跨包（interop / pipeline 等）复用。
//
// 契约源头是 WindowsPy/interop/gen_vectors.py:156-166 的 recipe_plain：
// sha256(seed ‖ uint32be(counter)) 首尾相接后截断到 size。vectors.json
// 的 recipe 字段（algo=sha256_counter_v1）记录种子；>=4MiB 的并行用例
// 明文/密文均不落盘，只记两端哈希 —— 任何用方都必须先经本函数重建明文，
// 再比对 cipher 哈希，等价于逐字节比对而存储成本为零。
package interop

import "crypto/sha256"

// RecipeAlgo 是唯一支持的明文配方标识；出现别的值说明夹具由更新版的
// gen_vectors.py 生成，本端需同步升级。
const RecipeAlgo = "sha256_counter_v1"

// RecipePlain 按配方生成确定性明文：seed ‖ uint32be(counter) 的 sha256
// 摘要首尾相接后截断到 size。
func RecipePlain(seed []byte, size int64) []byte {
	out := make([]byte, 0, size+sha256.Size)
	var counter uint32
	for int64(len(out)) < size {
		h := sha256.New()
		h.Write(seed)
		h.Write([]byte{
			byte(counter >> 24), byte(counter >> 16),
			byte(counter >> 8), byte(counter),
		})
		out = h.Sum(out)
		counter++
	}
	return out[:size]
}
