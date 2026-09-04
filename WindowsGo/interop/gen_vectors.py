"""跨语言互操作黄金向量生成器（Python 侧真源，一次性运行后把产物提交入库）。

用法（cwd 任意，依赖 .venv 的 editable 安装）：

    .venv\\Scripts\\python.exe WindowsGo\\interop\\gen_vectors.py

产出（全部落在 WindowsGo/interop/testdata/）：

    vectors.json          向量索引：定值 salt/iv、尺寸、sha256、Go 侧应产出的输入
    plain/*.bin           小尺寸明文原件（人眼可核对，也是 Go 侧的比对基准）
    cpenc/*.cpenc         Python **真实生产路径**产出的密文容器
    vault/*.cloudprism_vault   Python 产出的 Vault Marker（v1/v2/v3 × 恢复块有无）

反向夹具 testdata/go/ 由 Go 侧生成，本脚本不读不写：

    $env:CLOUDPRISM_INTEROP_EMIT = "1"
    go test ./interop/ -run Emit

设计要点（改动前请先读完）：

1. **走真实生产路径，不复刻原语**。密文一律经 Encryptor.encrypt_and_upload +
   LocalFolderBackend 产出，因此夹具同时覆盖单核流式与多核并行两条分支
   —— 并行分段未 16 字节对齐曾是「大文件静默数据损坏」的 P0 缺陷
   （见 encryptor.py:205-210 的对齐处理），夹具必须能钉住它。

2. **大文件不落盘**。>=4MiB 的并行用例若把明文与密文都提交，testdata 会
   膨胀到数十 MB。改为只记「确定性配方」（sha256 计数器链）+ 两端哈希：
   Go 侧本地重建明文与密文，比对 cipher_sha256 即等价于逐字节比对，
   存储成本为零而覆盖度不变。

3. **随机源被替换为确定性字节流**。encrypt_and_upload 的 IV 恒随机且不可注入
   （encryptor.py:82），恢复块盐与文件名 nonce 同理。夹具要的是「可复现」而
   非「不可预测」：换成 sha256 计数器链后，重跑本脚本产物逐字节相同，
   `git diff` 为空 —— 这才能证明夹具没被人手改过。替换只发生在本进程内，
   加密逻辑本身一行未改。

4. **每个夹具都先由 Python 自己验一遍**。生成后立即用 Decryptor /
   VaultMarker.verify 回读断言，任何失败都在生成阶段暴露。于是 Go 侧测试
   一旦红，方向是明确的：问题在 Go，不在夹具。
"""

from __future__ import annotations

import hashlib
import json
import platform
import shutil
import sys
import tempfile
from pathlib import Path

# ---------------------------------------------------------------------------
# 路径与常量
# ---------------------------------------------------------------------------

HERE = Path(__file__).resolve().parent
TESTDATA = HERE / "testdata"

# 主密码：刻意混入非 ASCII，锁死两端「密码按 UTF-8 编码后进 PBKDF2」的约定。
# 若某一端误用本地代码页（Windows 上是 GBK），派生密钥立刻不同，全部夹具变红。
MASTER_PASSWORD = "CloudPrism-Interop-主密码-pässwörd"

# 小尺寸明文：明文与密文都提交，Go 侧直接读盘比对，不依赖配方实现。
# 取值刻意压在 AES 块边界与文件头长度（51）两侧。
LITERAL_SIZES = (0, 1, 15, 16, 17, 51, 52)

# 中尺寸明文：只提交密文，明文由配方在两端各自重建（省掉等量的 plain/ 文件）。
RECIPE_SIZES = (4095, 4096, 65539, 262149)

# 并行路径用例：(尺寸, max_workers)。尺寸全部非 16 的倍数且 >=4MiB，
# 才会真正走进 _parallel_encrypt（门槛见 encryptor.py:100）。
# 只记哈希不落盘，理由见模块 docstring 第 2 条。
PARALLEL_CASES = (
    (4 * 1024 * 1024 + 3, 2),      # 恰好越过并行门槛，2 段
    (8 * 1024 * 1024 + 3, 4),      # 段长 2MiB+2，非对齐余数 2
    (12_345_678, 4),               # 段长 308641973 → 对齐后 308641968，余数 5
)

# 开启文件名加密的条目（叶子名是 Base32 密文而非展示名）。
# 覆盖「filename_enc 开/关」两种真实密库形态。
FILENAME_ENC_SLUGS = frozenset({"lit_16", "rec_4096", "par_8388611_w4"})

# 随机访问解密用例：(start, end)，取自 rec_262149。
# 覆盖对齐起点、非对齐起点、中段、以及末尾非对齐（end == 明文总长）。
RANGE_CASES = ((0, 16), (3, 19), (1000, 1032), (262100, 262149))

# 文件名加密夹具：展示名刻意含 CJK、空格、括号与多点号，
# 这些都是云盘端最容易出编码问题的字符。
FILENAME_SAMPLES = (
    "报告 2026 (最终版).txt",
    "a",
    "视频-第 3 集.1080p.mkv",
    "emoji-🎬-name.mp4",
)

# Vault Marker 夹具：(slug, version, 名称, filename_enc, 是否带恢复块)。
# v1/v2 无名称字段、v3 才有恢复块尾部，解析端必须容错（见 vault.py:173-186）。
VAULT_CASES = (
    ("v3_enc_rec", 3, "我的密库 Vault-1", True, True),
    ("v3_plain_norec", 3, "MyVault", False, False),
    ("v2_enc_norec", 2, "", True, False),
    ("v1_norec", 1, "", False, False),
    ("v3_name_trunc", 3, "字" * 40, True, True),   # 超上限，须截断到 32 字符
    ("v3_empty_name", 3, "", True, False),
)


# ---------------------------------------------------------------------------
# 确定性随机与明文配方
# ---------------------------------------------------------------------------


class DeterministicRandom:
    """把 get_random_bytes 换成 sha256 计数器链，使夹具可复现。

    签名与 Crypto.Random.get_random_bytes 一致，可直接顶替模块级引用。
    内部维护余量缓冲，保证任意长度请求都能满足且各次调用不重叠。
    """

    def __init__(self, domain: bytes) -> None:
        self._domain = domain
        self._counter = 0
        self._buf = b""

    def __call__(self, n: int) -> bytes:
        while len(self._buf) < n:
            self._buf += hashlib.sha256(
                self._domain + self._counter.to_bytes(8, "big")
            ).digest()
            self._counter += 1
        out, self._buf = self._buf[:n], self._buf[n:]
        return out


def install_deterministic_random() -> None:
    """替换随机源。只影响本进程，不改任何源文件。

    必须同时打两处，因为三个模块的导入方式不一致：
      - encryptor.py:24 / filename.py:20 是**模块级** `from Crypto.Random import
        get_random_bytes`，导入时就把引用绑定到了自己的命名空间，
        事后改 Crypto.Random 对它们无效 → 必须改模块属性；
      - vault.py:233 是**函数内**局部导入，每次调用才去 Crypto.Random 取 →
        必须改 Crypto.Random 本身。
    两处各用独立的字节流，使 IV / 恢复块盐 / 文件名 nonce 互不干扰。
    """
    import Crypto.Random as crypto_random

    from cloudprism.crypto import filename as filename_mod
    from cloudprism.core import encryptor as encryptor_mod

    crypto_random.get_random_bytes = DeterministicRandom(b"cp-interop-vault")
    encryptor_mod.get_random_bytes = DeterministicRandom(b"cp-interop-enc")
    filename_mod.get_random_bytes = DeterministicRandom(b"cp-interop-fn")


def recipe_plain(seed: bytes, size: int) -> bytes:
    """按配方生成确定性明文：sha256(seed ‖ uint32be(counter)) 首尾相接后截断。

    两端各写一份实现，配方本身即契约。Go 侧实现见 interop/recipe.go。
    """
    out = bytearray()
    counter = 0
    while len(out) < size:
        out += hashlib.sha256(seed + counter.to_bytes(4, "big")).digest()
        counter += 1
    return bytes(out[:size])


def seed_for(slug: str) -> bytes:
    """由 slug 派生 16 字节配方种子（确定性，与运行环境无关）。"""
    return hashlib.sha256(b"cp-interop-seed:" + slug.encode("utf-8")).digest()[:16]


def salt_for(slug: str) -> bytes:
    """由 slug 派生 16 字节 KDF 盐。

    每个条目用不同的盐 → 不同的派生密钥。若 Go 侧某处把密钥写死或复用了
    上一个文件的密钥，多盐夹具会立刻暴露；单一盐则可能碰巧全绿。
    """
    return hashlib.sha256(b"cp-interop-salt:" + slug.encode("utf-8")).digest()[:16]


def sha256_hex(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


# 全局唯一的文件名加密密钥（主密码 + 定值盐派生）。
# 三处夹具（.cpenc 叶子名、filenames 数组、go_emit.filename）共用同一把，
# 与真实密库「一个密库一把文件名密钥」的形态一致；分叉成多把只会让
# vectors.json 多出无信息量的字段。
FILENAME_KEY_SALT = hashlib.sha256(b"cp-interop-fnkey-salt").digest()[:16]


def filename_key_info() -> dict:
    """返回文件名加密密钥及其盐（hex），并顺带把 Kdf 导入到调用方作用域外。"""
    from cloudprism.crypto.kdf import Kdf

    return {
        "salt": FILENAME_KEY_SALT.hex(),
        "key": Kdf.derive_key(MASTER_PASSWORD, FILENAME_KEY_SALT).hex(),
    }


def filename_key() -> bytes:
    from cloudprism.crypto.kdf import Kdf

    return Kdf.derive_key(MASTER_PASSWORD, FILENAME_KEY_SALT)


# ---------------------------------------------------------------------------
# .cpenc 夹具
# ---------------------------------------------------------------------------


def build_cpenc_fixtures(scratch: Path) -> list[dict]:
    """产出全部 .cpenc 夹具，返回 vectors.json 的 files 数组。"""
    from cloudprism.core.decryptor import Decryptor
    from cloudprism.core.encryptor import Encryptor
    from cloudprism.core.session import Session
    from cloudprism.crypto.filename import FilenameCipher
    from cloudprism.crypto.header import FileHeader
    from cloudprism.storage.local_backend import LocalFolderBackend

    session = Session(MASTER_PASSWORD)
    fn_key = filename_key()

    plain_dir = TESTDATA / "plain"
    cpenc_dir = TESTDATA / "cpenc"

    cases: list[tuple[str, int, int, str]] = []   # (slug, size, workers, kind)
    for size in LITERAL_SIZES:
        cases.append((f"lit_{size}", size, 1, "literal"))
    for size in RECIPE_SIZES:
        cases.append((f"rec_{size}", size, 1, "recipe"))
    for size, workers in PARALLEL_CASES:
        cases.append((f"par_{size}_w{workers}", size, workers, "hash_only"))

    entries: list[dict] = []
    for slug, size, workers, kind in cases:
        seed = seed_for(slug)
        plain = recipe_plain(seed, size)
        assert len(plain) == size

        # 每个条目独立的 staging 根：LocalFolderBackend 要求 root 已存在
        staging = scratch / slug / "staging"
        staging.mkdir(parents=True, exist_ok=True)
        backend = LocalFolderBackend(staging)
        encryptor = Encryptor(session, backend)
        decryptor = Decryptor(session, backend)

        src = scratch / slug / "src.bin"
        src.write_bytes(plain)

        filename_enc = slug in FILENAME_ENC_SLUGS
        display_name = (
            f"{slug}-报告 (1).txt" if filename_enc else f"{slug}.bin"
        )
        leaf = (
            FilenameCipher.encrypt(display_name, fn_key)
            if filename_enc
            else display_name
        ) + ".cpenc"

        # 走真实生产路径：盐注入以命中 Session 缓存（也顺带覆盖盐复用分支），
        # IV 由被替换的确定性随机源给出，max_workers 决定是否进并行分支。
        salt = salt_for(slug)
        for _ in encryptor.encrypt_and_upload(
            str(src), leaf, max_workers=workers, salt=salt
        ):
            pass

        cipher = (staging / leaf).read_bytes()
        header = FileHeader.parse_bytes(cipher)

        # ---- Python 侧自验：夹具必须自己就能解开，否则 Go 红了无从判责 ----
        roundtrip = scratch / slug / "roundtrip.bin"
        for _ in decryptor.download_and_decrypt(leaf, str(roundtrip)):
            pass
        assert roundtrip.read_bytes() == plain, f"{slug} Python 自解密不一致"
        assert header.salt == salt, f"{slug} 头内盐与注入盐不符"
        assert len(cipher) == header.header_length + size, f"{slug} 长度关系被破坏"

        entry = {
            "slug": slug,
            "kind": kind,
            "size": size,
            "max_workers": workers,
            "recipe": {"algo": "sha256_counter_v1", "seed": seed.hex()},
            "salt": header.salt.hex(),
            "iv": header.iv.hex(),
            "flags": header.flags,
            "version": header.version,
            "header_length": header.header_length,
            "display_name": display_name,
            "filename_enc": filename_enc,
            "leaf": leaf,
            "plain_sha256": sha256_hex(plain),
            "cipher_sha256": sha256_hex(cipher),
        }

        if kind == "literal":
            # 明文落盘：Go 侧直接读，不必信任自己的配方实现
            (plain_dir / f"{slug}.bin").write_bytes(plain)
            entry["plain_path"] = f"plain/{slug}.bin"
        else:
            entry["plain_path"] = None

        if kind == "hash_only":
            # 大文件不提交，只留哈希；Go 侧重建后比对
            entry["cipher_path"] = None
        else:
            (cpenc_dir / f"{slug}.cpenc").write_bytes(cipher)
            entry["cipher_path"] = f"cpenc/{slug}.cpenc"

        entries.append(entry)
        print(f"  [cpenc] {slug:<20} size={size:<9} workers={workers} "
              f"kind={kind:<9} iv={header.iv.hex()[:16]}…")

    # ---- 随机访问解密夹具（Go 侧用 CTR.XORAt + 非零 blockIdx 复刻）----
    target = next(e for e in entries if e["slug"] == "rec_262149")
    staging = scratch / "rec_262149" / "staging"
    backend = LocalFolderBackend(staging)
    decryptor = Decryptor(Session(MASTER_PASSWORD), backend)
    plain = recipe_plain(seed_for("rec_262149"), 262149)
    ranges = []
    for start, end in RANGE_CASES:
        got = decryptor.decrypt_range_to_bytes(target["leaf"], start=start, end=end)
        # 双重自验：随机访问的结果必须与整文件明文的对应切片一致
        assert got == plain[start:end], f"range {start}:{end} 与整文件切片不符"
        ranges.append({
            "start": start,
            "end": end,
            "expect_hex": got.hex(),
        })
    target["ranges"] = ranges
    print(f"  [range] rec_262149 随机访问 {len(ranges)} 组已自验")

    return entries


# ---------------------------------------------------------------------------
# Vault Marker 夹具
# ---------------------------------------------------------------------------


def build_vault_fixtures() -> tuple[list[dict], list[dict]]:
    """产出 Vault Marker 夹具与文件名加密夹具。"""
    from cloudprism.crypto.filename import FilenameCipher
    from cloudprism.crypto.vault import VaultMarker, VaultMetadata

    fn_key = filename_key()

    vault_dir = TESTDATA / "vault"
    vaults: list[dict] = []

    for slug, version, name, filename_enc, with_recovery in VAULT_CASES:
        # 定值 vault_id/salt/iv：Go 侧用同一组输入必须产出逐字节相同的 Marker。
        # 随机源已被替换为确定性字节流，故重跑本脚本产物不变。
        vault_id = hashlib.sha256(b"cp-interop-vid:" + slug.encode()).digest()[:16]
        vsalt = hashlib.sha256(b"cp-interop-vsalt:" + slug.encode()).digest()[:16]
        viv = hashlib.sha256(b"cp-interop-viv:" + slug.encode()).digest()[:16]

        # 恢复块用固定 secret（10 字节 → Base32 无填充 16 字符）
        secret = hashlib.sha256(b"cp-interop-rec:" + slug.encode()).digest()[:10]
        blob = VaultMarker.build_recovery_blob(secret, MASTER_PASSWORD) if with_recovery else b""
        # 自验：恢复块必须能解回主密码
        if with_recovery:
            assert VaultMarker.decrypt_recovery_blob(blob, secret) == MASTER_PASSWORD

        meta = VaultMetadata(
            version=version,
            vault_id=vault_id,
            salt=vsalt,
            iv=viv,
            filename_enc=filename_enc,
            protocol_version=1,
            name=name,
        )
        data = VaultMarker.create(meta, MASTER_PASSWORD, blob)

        got = VaultMarker.verify(data, MASTER_PASSWORD)
        assert got is not None, f"{slug} Python 自己校验失败"
        # 名称按【字符数】截断到上限（vault.py:96 是 meta.name[:32]）
        assert got.name == name[:32], f"{slug} 名称截断行为不符"
        assert got.has_recovery == with_recovery, f"{slug} 恢复块探测不符"
        assert VaultMarker.verify(data, "wrong-password") is None, \
            f"{slug} 错误密码竟然通过"

        head, tail = VaultMarker.split_recovery_tail(data)
        (vault_dir / f"{slug}.cloudprism_vault").write_bytes(data)

        vaults.append({
            "slug": slug,
            "path": f"vault/{slug}.cloudprism_vault",
            "password": MASTER_PASSWORD,
            "vault_id": vault_id.hex(),
            "salt": vsalt.hex(),
            "iv": viv.hex(),
            "version": version,
            "protocol_version": 1,
            "name_in": name,
            "recovery_secret": secret.hex(),
            "recovery_blob": blob.hex(),       # Go 侧原样传入 CreateMarker 以复现字节
            "recovery_code": _b32(secret),
            "bytes_sha256": sha256_hex(data),
            "length": len(data),
            "expect": {
                "version": got.version,
                "name": got.name,
                "filename_enc": got.filename_enc,
                "protocol_version": got.protocol_version,
                "has_recovery": got.has_recovery,
                "vault_id": got.vault_id.hex(),
                "salt": got.salt.hex(),
                "iv": got.iv.hex(),
            },
            "split": {"head_len": len(head), "tail_hex": tail.hex()},
        })
        print(f"  [vault] {slug:<18} v{version} len={len(data):<4} "
              f"rec={'Y' if with_recovery else 'N'} name={got.name!r}")

    # ---- 文件名加密夹具 ----
    filenames = []
    for plain in FILENAME_SAMPLES:
        enc = FilenameCipher.encrypt(plain, fn_key)
        assert FilenameCipher.decrypt(enc, fn_key) == plain
        filenames.append({"plain": plain, "enc": enc})
        print(f"  [fn   ] {plain!r} -> {enc[:24]}…")

    return vaults, filenames


def _b32(secret: bytes) -> str:
    """恢复码编码（与 VaultManager.encode_recovery_code 同规则）。"""
    from cloudprism.crypto.filename import b32_encode_nopad

    return b32_encode_nopad(secret)


# ---------------------------------------------------------------------------
# Go 侧反向夹具的输入规格
# ---------------------------------------------------------------------------


def build_go_emit_spec() -> dict:
    """描述 Go 侧应产出哪些反向夹具（testdata/go/）。

    这里只给**输入**（定值 salt/iv/配方/期望字段），不给期望字节：
    期望字节就是提交入库的 go/ 文件本身，Go 测试在常规运行时会重新推导
    并与已提交内容比对（漂移守卫），无需在此重复记录哈希。

    文件名夹具是唯一的例外 —— nonce 随机，Go 每次产出都不同，
    故只在 EMIT 模式写一次，常规运行仅做「解得开且等于期望明文」的自洽校验。
    """
    cpenc = []
    for slug, size in (("go_lit_17", 17), ("go_rec_4095", 4095), ("go_rec_65539", 65539)):
        cpenc.append({
            "slug": slug,
            "path": f"go/cpenc/{slug}.cpenc",
            "size": size,
            "recipe": {"algo": "sha256_counter_v1", "seed": seed_for(slug).hex()},
            "salt": salt_for(slug).hex(),
            # IV 同样定值：Go 无法注入随机 IV 到「已提交的期望字节」里，
            # 反向夹具必须是纯确定性构造，否则每次重跑都会污染工作树。
            "iv": hashlib.sha256(b"cp-interop-go-iv:" + slug.encode()).digest()[:16].hex(),
            "flags": 0,
            "version": 1,
        })

    vaults = []
    for slug, version, name, filename_enc, with_recovery in (
        ("go_v3_rec", 3, "Go 端密库", True, True),
        ("go_v1", 1, "", False, False),
    ):
        secret = hashlib.sha256(b"cp-interop-go-rec:" + slug.encode()).digest()[:10]
        vaults.append({
            "slug": slug,
            "path": f"go/vault/{slug}.cloudprism_vault",
            "password": MASTER_PASSWORD,
            "vault_id": hashlib.sha256(b"cp-interop-go-vid:" + slug.encode()).digest()[:16].hex(),
            "salt": hashlib.sha256(b"cp-interop-go-vsalt:" + slug.encode()).digest()[:16].hex(),
            "iv": hashlib.sha256(b"cp-interop-go-viv:" + slug.encode()).digest()[:16].hex(),
            "version": version,
            "protocol_version": 1,
            "name": name,
            "filename_enc": filename_enc,
            # 恢复块由 Go 侧 BuildRecoveryBlob 产出（含随机盐），
            # 因此带恢复块的 Marker 无法做字节级漂移守卫，只做 Python 可解开。
            "recovery_secret": secret.hex() if with_recovery else None,
            "recovery_code": _b32(secret) if with_recovery else None,
            "expect_name": name[:32],
            "expect_has_recovery": with_recovery,
        })

    filenames = {
        "salt": FILENAME_KEY_SALT.hex(),
        "key": filename_key().hex(),
        "items": [
            {"index": i, "path": f"go/filename/{i:02d}.txt", "plain": plain}
            for i, plain in enumerate(FILENAME_SAMPLES)
        ],
    }

    return {"cpenc": cpenc, "vault": vaults, "filename": filenames}


# ---------------------------------------------------------------------------
# 入口
# ---------------------------------------------------------------------------


def main() -> int:
    # Windows 控制台默认 GBK，打印 CJK 会 UnicodeEncodeError，故重配 stdout
    sys.stdout.reconfigure(encoding="utf-8")

    from cloudprism import constants

    install_deterministic_random()

    for sub in ("plain", "cpenc", "vault"):
        d = TESTDATA / sub
        if d.exists():
            shutil.rmtree(d)      # 清空旧产物，避免上次生成的孤儿文件混进来
        d.mkdir(parents=True)

    # 临时工作区落在 WindowsPy/data/tmp/（已被 .gitignore 的 data/ 覆盖），
    # 不用系统 %TEMP%：部分沙箱环境 %TEMP% 的 ACL 不完整。
    from cloudprism.core.paths import temp_dir

    scratch = Path(tempfile.mkdtemp(prefix="interop-", dir=temp_dir(create=True)))
    print(f"scratch = {scratch}")

    try:
        print("[1/3] 生成 .cpenc 夹具 …")
        files = build_cpenc_fixtures(scratch)

        print("[2/3] 生成 Vault Marker 与文件名夹具 …")
        vaults, filenames = build_vault_fixtures()

        print("[3/3] 生成 Go 侧反向夹具规格 …")
        go_emit = build_go_emit_spec()
    finally:
        shutil.rmtree(scratch, ignore_errors=True)

    vectors = {
        "meta": {
            "generator": "WindowsGo/interop/gen_vectors.py",
            "schema": 1,
            "note": "由 Python 侧一次性生成并提交，Go 侧只读；重跑本脚本产物应逐字节相同",
            "python": sys.version.split()[0],
            "platform": platform.platform(),
            "master_password": MASTER_PASSWORD,
            "protocol_version": constants.VERSION,
            "vault_version": constants.VAULT_VERSION,
            "kdf": {
                "algo": constants.KDF_ALGO,
                "iterations": constants.KDF_ITERATIONS,
                "key_len": constants.KEY_LEN,
            },
        },
        "filename_key": filename_key_info(),
        "files": files,
        "vaults": vaults,
        "filenames": filenames,
        "go_emit": go_emit,
    }

    out = TESTDATA / "vectors.json"
    out.write_text(
        json.dumps(vectors, ensure_ascii=False, indent=1) + "\n", encoding="utf-8"
    )

    total = sum(p.stat().st_size for p in TESTDATA.rglob("*") if p.is_file())
    nfiles = sum(1 for p in TESTDATA.rglob("*") if p.is_file())
    print(f"\nvectors.json 已写入：{out}")
    print(f"testdata 合计 {nfiles} 个文件 / {total / 1024:.1f} KiB")
    return 0


if __name__ == "__main__":   # ProcessPoolExecutor 在 Windows 用 spawn，必须有此守卫
    raise SystemExit(main())
