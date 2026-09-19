# interop —— 冻结的跨语言互操作黄金向量

本目录钉住的是**密文格式的字节级契约**：`testdata/` 下的夹具是「参考实现当年产出的
真实密文」，Go 侧必须能逐字节重建、并正确解开。

> **v38 起形态变更**：Python 参考实现（原 `WindowsPy/`）已整体移除。
> 本目录随之从「两侧互为 oracle」退化为**单侧（方向 A）+ 冻结夹具**。

## 真源与方向

```
WindowsGo/interop/gen_vectors.py     ← 生成逻辑存档（依赖的 cloudprism 包已移除，无法直接运行）
WindowsGo/interop/testdata/**        ← 生成物，28 个只读夹具（含 9 个 Go 反向夹具）
WindowsGo/interop/*_test.go          ← 方向 A：参考实现产物 → Go 解密/重建 ✅ 仍全量生效
（已删除）                            ← 方向 B：Go 产物 → 参考实现解密/重建 ❌ v38 起不存在
```

**夹具即契约**：夹具与实现冲突时以夹具为准。`testdata/` 只读、**不可再生**，
常规测试不得改写（`testdata/go/` 是唯一例外，见下节）。

## 关于失去方向 B

v33 前两个方向都跑：只测方向 A 无法排除「两端犯了同一个错因而互相自洽」——例如
两端都把 GCM nonce 长度写错成一样的值，方向 A 会全绿，而真实密库打不开。方向 B
（参考实现解 Go 的产物）正是为此存在的交叉验证。

**这个交叉验证现在没有了对端。** 因此：

- 现有夹具已覆盖的格式面，回归保护是完整的（方向 A 依然逐字节比对）；
- 但**新增或修改格式将不再有任何跨实现交叉验证**。若确需变更密文格式，必须：
  1. 同步升级 `pkg/protocol` 的版本号；
  2. 补充迁移说明（老密库如何读取）；
  3. 明确接受「无第二实现交叉验证」这一风险。
- 不要再试图「重跑 `gen_vectors.py` 来对齐」——它 `import cloudprism.*`，而该包已不存在。

## 反向夹具为什么由 Go 生成（历史说明）

保留这段是为了解释 `testdata/go/` 的来历。参考实现的 `Encryptor.encrypt_and_upload`
里 IV 恒随机且**无法注入**（`encryptor.py:82`），生成器靠猴补丁替换随机源才拿到
确定性；Go 侧的 `cryptox.Build` 接受显式 IV，不需要任何补丁。让 Go 定值构造、
参考实现用原语重建比对，是成本最低且最少侵入的做法。

`testdata/go/` 默认**只读**：文件名 nonce 与恢复块 rsalt 本质随机，若每轮测试都重写，
工作树会恒脏、提交历史里全是无意义的二进制抖动。重新生成需要显式开门：

```powershell
cd WindowsGo
$env:CLOUDPRISM_INTEROP_EMIT = "1"
go test ./interop/ -run Emit -v -count=1     # 写出 testdata/go/
$env:CLOUDPRISM_INTEROP_EMIT = ""            # 关掉，之后 go test 只读
git add interop/testdata/go                  # 必须一并提交
```

`TestGoArtifactsAreUpToDate` 是这套机制的**漂移守卫**：它在常规 `go test` 里就地
重推导快照并与已提交内容逐字节比对。没有它，Go 实现改了而忘了重跑 Emit 时，
会一直绿着验证一份**过期**产物，契约名存实亡。含随机 nonce/rsalt 的产物无法字节
比对，退化为「Go 自己解得开且字段正确」的自洽校验。

> 已无对端消费者：v38 后 `testdata/go/` 仅作回归留档，保留 Emit 机制是为了不改测试结构。

## 三档夹具

| 档 | `kind` | 明文 | 密文 | 尺寸 | 为什么 |
|---|---|---|---|---|---|
| 1 | `literal` | 落盘 `plain/` | 落盘 `cpenc/` | 0/1/15/16/17/51/52 | 小到人眼可核对，Go 侧直接读盘，**不依赖配方实现** |
| 2 | `recipe` | 配方重建 | 落盘 `cpenc/` | 4095/4096/65539/262149 | 省掉等量的 `plain/` 文件，仍能字节级比对密文 |
| 3 | `hash_only` | 配方重建 | 只记 sha256 | 4194307 / 8388611 / 12345678 | 并行路径必须 ≥4MiB，落盘会让 testdata 膨胀到数十 MB |

档 3 的等价性论证：Go 侧按配方重建明文 → 校验 `plain_sha256` → 用定值 salt/iv 重建密文
→ 校验 `cipher_sha256`。密文哈希相同即等价于逐字节相同（sha256 无实际碰撞风险），
而**存储成本为零**。同时把重建结果再解一遍校验还原出的明文哈希 —— 两个方向都过，
才排除「Go 重建时和参考实现犯了同一个错」。

档 3 的三条用例刻意全部**非 16 的倍数**且 ≥4MiB，因此必然踩进并行加密路径。
这条路径上「分段未按 16 字节对齐」曾造成大文件静默数据损坏（阶段 0 修掉的 P0 缺陷）；
`TestParallelPathFixturesMatchPythonHash` 用 `parallelThreshold` 常量断言夹具确实触发
并行，若夹具没重生成而门槛变了，用例会**立即失败**而不是静默退化成「又测了一遍
单核路径」。

配方 `recipe_plain(seed, size)`：反复计算 `sha256(seed ‖ uint32be(counter))` 首尾相接后截断。
参考侧实现在 `gen_vectors.py`，Go 侧同构实现在 `vectors_test.go` 的 `recipePlain`。

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

## 确定性随机：参考侧曾打两处补丁（历史说明）

生成器要把随机源换成 sha256 计数器链，而参考实现三个模块的导入方式不一致：

- `encryptor.py:24` / `filename.py:20` 是**模块级** `from Crypto.Random import
  get_random_bytes`，导入时就把引用绑定进了自己的命名空间 → 事后改
  `Crypto.Random` 对它们无效，必须改**模块属性**；
- `vault.py:233` 是**函数内**局部导入，每次调用才去 `Crypto.Random` 取 →
  必须改 `Crypto.Random` **本身**。

只打一处会漏，且漏掉的产物每轮都变、`git diff` 非空 —— 这本身就是「夹具被随机性
污染」的信号。保留此节是因为 `gen_vectors.py` 仍在仓库中；若将来有人复活参考实现，
这就是当年的坑位记录。

## 常规验证（现在只有这一条路）

```powershell
cd WindowsGo
go test ./interop/ -v -count=1     # 无需任何 Python 环境
```

`testdata/` 实测 **34 个文件 / 419 KiB**（25 个参考实现产出 + 9 个 Go 反向夹具）。
一次性 PBKDF2 200000 轮 × 数十次，秒级完成。

## 规模与行尾细节

### 一个会误报「漂移」的细节：vectors.json 的行尾

已实测：生成器在 Windows 上写出的 `vectors.json` 是 **CRLF**（`Path.write_text`
走通用换行翻译），而 `.gitattributes` 里 `*.json text eol=lf` 会把它归一化成
**LF** 入库。于是：

- `git diff` 为空 ✅ —— 入库形态与生成平台无关；
- 但**工作区文件与新鲜克隆出来的文件不逐字节相同**（实测 20978 vs 20322 字节，
  656 个 CRLF ↔ 656 个 LF，`worktree.replace(CRLF, LF) == clone` 为 True）。

因此**不要用「哈希比对工作区」来验证一致性**，那会把这条设计好的差异报成 DRIFT。
要比就比行尾归一化后的内容。

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
