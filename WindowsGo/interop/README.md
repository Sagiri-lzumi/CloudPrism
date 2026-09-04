# interop —— 跨语言互操作黄金向量夹具

钉住一件事：**同一份输入，Go 端与 Python 端产出逐字节相同的密文；任一端产出的密文，
另一端都能正确解开。** 这是「用户可以在两个客户端之间无缝迁移密库」的唯一硬保证。

## 真源与方向

```
WindowsGo/interop/gen_vectors.py     ← 真源：Python 侧一次性生成，产物提交入库
WindowsGo/interop/testdata/**        ← 生成物（Go 侧只读，常规测试不得改写）
WindowsGo/interop/*_test.go          ← 方向 A：Python 产物 → Go 解密/重建
WindowsPy/tests/test_interop_go.py   ← 方向 B：Go 产物     → Python 解密/重建
```

**两个方向都必须通**。只测方向 A 无法排除「两端犯了同一个错因而互相自洽」——
例如两端都把 GCM nonce 长度写错成一样的值，方向 A 会全绿，而真实密库打不开。

方向 B 读的 `testdata/go/` 由 **Go 侧**写出并提交，理由见下节。

## 反向夹具为什么由 Go 生成

Python 的 `Encryptor.encrypt_and_upload` 里 IV 恒随机且**无法注入**
（[encryptor.py:82](../../WindowsPy/src/cloudprism/core/encryptor.py#L82)），
生成器靠猴补丁替换随机源才拿到确定性；Go 侧的 `cryptox.Build` 接受显式 IV，
不需要任何补丁。让 Go 定值构造、Python 用原语重建比对，是成本最低且最少侵入的做法。

`testdata/go/` 默认**只读**：文件名 nonce 与恢复块 rsalt 本质随机，若每轮测试都重写，
工作树会恒脏、提交历史里全是无意义的二进制抖动。重新生成需要显式开门：

```powershell
cd WindowsGo
$env:CLOUDPRISM_INTEROP_EMIT = "1"
go test ./interop/ -run Emit -v -count=1     # 写出 testdata/go/
$env:CLOUDPRISM_INTEROP_EMIT = ""            # 关掉，之后 go test 只读
git add interop/testdata/go                  # 必须一并提交，否则方向 B 整模块 skip
```

`TestGoArtifactsAreUpToDate` 是这套机制的**漂移守卫**：它在常规 `go test` 里就地重推导
快照并与已提交内容逐字节比对。没有它，Go 实现改了而忘了重跑 Emit 时，方向 B 会一直
绿着验证一份**过期**产物，契约名存实亡。含随机 nonce/rsalt 的产物无法字节比对，
退化为「Go 自己解得开且字段正确」的自洽校验。

方向 B 侧另有 `test_go_artifacts_are_not_stale` 做孤儿/缺项双向核对，
防止磁盘上残留上一轮的旧文件。

## 三档夹具

| 档 | `kind` | 明文 | 密文 | 尺寸 | 为什么 |
|---|---|---|---|---|---|
| 1 | `literal` | 落盘 `plain/` | 落盘 `cpenc/` | 0/1/15/16/17/51/52 | 小到人眼可核对，Go 侧直接读盘，**不依赖配方实现** |
| 2 | `recipe` | 配方重建 | 落盘 `cpenc/` | 4095/4096/65539/262149 | 省掉等量的 `plain/` 文件，仍能字节级比对密文 |
| 3 | `hash_only` | 配方重建 | 只记 sha256 | 4194307 / 8388611 / 12345678 | 并行路径必须 ≥4MiB，落盘会让 testdata 膨胀到数十 MB |

档 3 的等价性论证：Go 侧按配方重建明文 → 校验 `plain_sha256` → 用定值 salt/iv 重建密文
→ 校验 `cipher_sha256`。密文哈希相同即等价于逐字节相同（sha256 无实际碰撞风险），
而**存储成本为零**。同时把重建结果再解一遍校验还原出的明文哈希 —— 两个方向都过，
才排除「Go 重建时和 Python 犯了同一个错」。

档 3 的三条用例刻意全部**非 16 的倍数**且 ≥4MiB，因此必然踩进
`_parallel_encrypt`。这条路径上「分段未按 16 字节对齐」曾造成大文件静默数据损坏
（阶段 0 修掉的 P0 缺陷）；`TestParallelPathFixturesMatchPythonHash` 用
`parallelThreshold` 常量断言夹具确实触发并行，若 Python 侧调高门槛而夹具没重生成，
用例会**立即失败**而不是静默退化成「又测了一遍单核路径」。

配方 `recipe_plain(seed, size)`：反复计算 `sha256(seed ‖ uint32be(counter))` 首尾相接后截断。
Python 侧实现在 `gen_vectors.py`，**方向 B 用 `importlib` 直接引用它**而不是复制一份 ——
两份实现一旦分叉，测试会以「Go 的密文解不开」的形式失败，看起来像加密不兼容，
实际是测试自己把明文算错了。Go 侧同构实现在 `vectors_test.go` 的 `recipePlain`。

## 与 pkg/cryptox 黄金常量的分工

| | `pkg/cryptox/*_test.go` | 本包 |
|---|---|---|
| 期望值形态 | 硬编码 hex 常量 | 真实文件夹具 |
| 覆盖 | 原语与**协议布局逐字段** | 原语**按生产方式组合后**的端到端产物 |
| 速度 | 毫秒级 | 秒级（PBKDF2 200000 轮 × 数十次） |
| 依赖 | 无外部文件 | `testdata/` |

二者不可互相替代：前者证明「原语正确」，后者证明「组合正确」。
`Encryptor` 的单核流式与多核并行两条分支、`LocalFolderBackend` 的落盘形态、
随机访问解密、文件名加密后的云端叶子名 —— 这些 cryptox 都够不到。

一个易误解处：`vault/v1_norec.cloudprism_vault` 由 `VaultMarker.create` 产出，
其内部明文**仍含 NameLen 字段**（只是 name 为空），因为 create 不区分版本地拼装
inner。真正「v1 旧文件无 NameLen 字段」的历史布局由 cryptox 的
`goldenOddV1NoName` 覆盖，两者互补而非重复。

## 确定性随机：必须打两处补丁

生成器要把随机源换成 sha256 计数器链，而三个模块的导入方式不一致：

- `encryptor.py:24` / `filename.py:20` 是**模块级** `from Crypto.Random import
  get_random_bytes`，导入时就把引用绑定进了自己的命名空间 → 事后改
  `Crypto.Random` 对它们无效，必须改**模块属性**；
- `vault.py:233` 是**函数内**局部导入，每次调用才去 `Crypto.Random` 取 →
  必须改 `Crypto.Random` **本身**。

只打一处会漏，且漏掉的产物每轮都变、`git diff` 非空 —— 这本身就是「夹具被
随机性污染」的信号。补丁只发生在本进程内，加密逻辑一行未改。

## 重新生成完整流程

协议任一侧改动后按顺序执行：

```powershell
cd C:\Codes\CloudPrism
$env:PYTHONIOENCODING = "utf-8"

# 1. 重新生成正向夹具（约 140s；产物应逐字节可复现，git diff 为空）
.\.venv\Scripts\python.exe WindowsGo\interop\gen_vectors.py

# 2. 重新生成反向夹具
cd WindowsGo
$env:CLOUDPRISM_INTEROP_EMIT = "1"
go test ./interop/ -run Emit -v -count=1
$env:CLOUDPRISM_INTEROP_EMIT = ""

# 3. 两侧验证
go test ./interop/ -v -count=1
cd ..\WindowsPy
$env:PYTHONIOENCODING = "utf-8"
..\.venv\Scripts\python.exe -m pytest tests/test_interop_go.py -v
```

`git diff` 为空是可复现性的直接证明；若非空，先确认没有残留的随机源未被打补丁。

### 一个会误报「漂移」的细节：vectors.json 的行尾

已实测：生成器在 Windows 上写出的 `vectors.json` 是 **CRLF**（`Path.write_text`
走通用换行翻译），而 `.gitattributes` 里 `*.json text eol=lf` 会把它归一化成
**LF** 入库。于是：

- `git diff` 为空 ✅ —— 入库形态与生成平台无关，Linux 上重跑得 LF、Windows 上
  重跑得 CRLF，归一化后都是同一份内容，这才是「可复现」的正确判据；
- 但**工作区文件与新鲜克隆出来的文件不逐字节相同**（实测 20978 vs 20322 字节，
  656 个 CRLF ↔ 656 个 LF，`worktree.replace(CRLF, LF) == clone` 为 True）。

因此**不要用「哈希比对工作区」来验证可复现性**，那会把这条设计好的差异报成 DRIFT。
要比就比「重新生成后 `git diff` 是否为空」，或先对 JSON 做行尾归一化再比。
两端解析均不受影响（`json.loads` / `json.Unmarshal` 都不关心行尾）。

其余 33 个文件是 `-text`（见 `.gitattributes`），git 原样存取，工作区与克隆逐字节相同。

### 新鲜克隆已验证

`git clone --local` 后在克隆目录里直接跑，结果全绿：

```
ok  .../windowsgo/interop        2.803s
ok  .../windowsgo/pkg/cryptox    4.993s
ok  .../windowsgo/pkg/protocol   0.366s
gofmt -l .   → 空
```

即：夹具与源码都能从一次干净检出直接工作，不依赖任何未入库的本地产物。

## 规模与常规 CI

`testdata/` 实测 **34 个文件 / 419 KiB**（25 个 Python 产出 + 9 个 Go 反向夹具）。
计划预估为「10–15 个文件 / <2MB」：文件数偏多是因为 Vault Marker 覆盖了
v1/v2/v3 × filename_enc 开关 × 恢复块有无 × 名称截断的 6 种组合，且反向夹具独立成目录；
总体积仍远低于预算（大头 `rec_262149` 与两个 `go_rec_*` 合计约 390 KiB）。

**常规本地/CI 只需跑 Go 侧**（`go test ./interop/`，无需 Python 环境），维护成本≈0。
方向 B 在缺少 `testdata/go/` 或 Go 工具链时整模块 `pytest.skip`，
绝不因为跨语言关卡没配好就让 WindowsPy 的测试套件变红。
