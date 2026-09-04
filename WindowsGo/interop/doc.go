// Package interop 承载 Go 与 Python 两端的跨语言互操作夹具。
//
// 本包只有测试代码，不产出任何二进制。夹具（testdata/）由 Python 侧的
// gen_vectors.py 一次性生成并提交入库，Go 侧只读；反向夹具（testdata/go/）
// 由 Go 侧在 CLOUDPRISM_INTEROP_EMIT=1 时写出，Python 侧的
// WindowsPy/tests/test_interop_go.py 只读。两侧互为对方的 oracle。
//
// 与 pkg/cryptox 自带的黄金向量测试的分工：
//
//   - cryptox/*_test.go 用**硬编码的 hex 常量**逐字段钉住协议布局，
//     跑得快、不依赖任何外部文件，是常规 CI 的门禁；
//   - 本包用**真实文件夹具**钉住端到端产物，覆盖 cryptox 够不到的部分：
//     Encryptor 的单核流式与多核并行两条生产分支、LocalFolderBackend 的
//     落盘形态、随机访问解密、以及文件名加密后的云端叶子名。
//
// 二者不可互相替代：前者证明「原语正确」，后者证明「原语按生产方式组合后
// 仍与 Python 逐字节一致」。协议任一侧改动后，重跑 gen_vectors.py +
// go test ./interop/... 即可暴露不兼容。
//
// 详见本目录 README.md。
package interop
