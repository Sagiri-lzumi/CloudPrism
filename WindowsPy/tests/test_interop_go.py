"""跨语言互操作反向验证：用 Python 解开 Go 侧产出的夹具。

与 WindowsGo/interop/interop_test.go 互为镜像：

    方向 A（Go 侧跑）  Python 产物 -> Go 解密/重建 == 逐字节相同
    方向 B（本文件跑） Go 产物     -> Python 解密/重建 == 逐字节相同

只有两个方向都通，才能排除「两端犯了同一个错因而互相自洽」的可能。

夹具来源：testdata/go/ 由 Go 侧写出并提交入库 ——

    $env:CLOUDPRISM_INTEROP_EMIT = "1"
    cd WindowsGo; go test ./interop/ -run Emit -v

夹具不存在时整模块 skip：本测试是可选的跨语言关卡，不得因为没装 Go
工具链就让 WindowsPy 的测试套件变红（常规 CI 只跑 Go 侧，维护成本≈0）。
"""

from __future__ import annotations

import importlib.util
import json
from io import BytesIO
from pathlib import Path

import pytest
from Crypto.Cipher import AES
from Crypto.Util import Counter

from cloudprism.core.decryptor import Decryptor
from cloudprism.core.session import Session
from cloudprism.crypto.filename import FilenameCipher, b32_decode_nopad
from cloudprism.crypto.header import FileHeader
from cloudprism.crypto.kdf import Kdf
from cloudprism.crypto.vault import VaultMarker
from cloudprism.storage.local_backend import LocalFolderBackend

# tests/ -> WindowsPy/ -> 仓库根
REPO_ROOT = Path(__file__).resolve().parents[2]
GO_INTEROP = REPO_ROOT / "WindowsGo" / "interop"
GO_TESTDATA = GO_INTEROP / "testdata"
GO_ARTIFACTS = GO_TESTDATA / "go"
VECTORS_JSON = GO_TESTDATA / "vectors.json"

if not (GO_ARTIFACTS.is_dir() and VECTORS_JSON.is_file()):
    pytest.skip(
        "缺少 Go 侧反向夹具（WindowsGo/interop/testdata/go/）。"
        "设置 CLOUDPRISM_INTEROP_EMIT=1 后运行 go test ./interop/ -run Emit 生成，"
        "再重跑本测试。",
        allow_module_level=True,
    )

# 模块级加载一次，供 parametrize 生成用例。
# 不能放进 fixture：parametrize 在收集阶段就要知道参数个数，
# 写死成 range(3) 一旦 Go 侧改了夹具数量就会默默漏测。
VECTORS = json.loads(VECTORS_JSON.read_text(encoding="utf-8"))
GO_CPENC_SPECS = VECTORS["go_emit"]["cpenc"]
GO_VAULT_SPECS = VECTORS["go_emit"]["vault"]


def _spec_ids(specs: list[dict]) -> list[str]:
    return [s["slug"] for s in specs]


# ---------------------------------------------------------------------------
# 夹具加载
# ---------------------------------------------------------------------------


@pytest.fixture(scope="module")
def vectors() -> dict:
    """vectors.json 的内容（由 gen_vectors.py 产出，Go 侧只读不改）。"""
    return VECTORS


@pytest.fixture(scope="module")
def gen():
    """按路径加载 gen_vectors.py，直接复用它的 recipe_plain。

    刻意不在本文件里复制一份配方实现：两份实现一旦分叉，测试会以
    「Go 的密文解不开」的形式失败，看起来像加密不兼容，实际是测试自己
    把明文算错了。引用生成器本身消除了这一整类假故障。

    gen_vectors.py 的模块级代码只有常量定义，副作用全在 main() 里且受
    `if __name__ == "__main__"` 守卫，因此按其它模块名加载是安全的。
    """
    path = GO_INTEROP / "gen_vectors.py"
    if not path.is_file():
        pytest.skip(f"生成器不存在：{path}", allow_module_level=False)
    spec = importlib.util.spec_from_file_location("cp_interop_gen_vectors", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _go_artifact(rel: str) -> Path:
    """把 go_emit 里的相对路径（如 go/cpenc/x.cpenc）映射到磁盘路径。

    按分隔符拆开再 joinpath，而不是直接拼字符串：夹具里存的是 `/`，
    直接给 Path 在 Windows 上虽然也能用，但拼出来的字符串无法用于
    relative_to 比较（见 test_go_artifacts_are_not_stale）。
    """
    return GO_TESTDATA.joinpath(*rel.split("/"))


def _rebuild_cpenc(password: str, plain: bytes, salt: bytes, iv: bytes,
                   flags: int, version: int) -> bytes:
    """用 Python 原语重建一个 .cpenc 容器。

    与 Encryptor.encrypt_to_bytes 的做法一致（同样的 Counter.new(128,
    initial_value=int_be(iv), allow_wraparound=True)），只是把 IV 换成
    夹具给定的定值 —— 生产路径的 IV 恒随机且不可注入，无法用于字节比对。
    """
    key = Kdf.derive_key(password, salt)
    header = FileHeader.build(salt, iv, flags=flags, version=version)
    ctr = Counter.new(128, initial_value=int.from_bytes(iv, "big"),
                      allow_wraparound=True)
    return header + AES.new(key, AES.MODE_CTR, counter=ctr).encrypt(plain)


# ---------------------------------------------------------------------------
# .cpenc：Go 产出 -> Python 解开
# ---------------------------------------------------------------------------


def test_go_cpenc_artifacts_exist(vectors):
    """go_emit 声明的每个 .cpenc 夹具都必须真实存在。

    单独一条：否则后面的用例会以「文件读不到」的形式失败，
    看不出是 Go 没生成还是路径写错。
    """
    specs = vectors["go_emit"]["cpenc"]
    assert specs, "go_emit.cpenc 为空，Go 侧没有声明任何反向夹具"
    for spec in specs:
        path = _go_artifact(spec["path"])
        assert path.is_file(), f"缺少反向夹具 {spec['path']}"


@pytest.mark.parametrize("spec", GO_CPENC_SPECS, ids=_spec_ids(GO_CPENC_SPECS))
def test_go_cpenc_header_and_bytes(vectors, gen, spec):
    """Go 产出的容器：头部字段与规格一致，且整体与 Python 重建逐字节相同。"""
    data = _go_artifact(spec["path"]).read_bytes()

    salt = bytes.fromhex(spec["salt"])
    iv = bytes.fromhex(spec["iv"])

    # 1. 头部可解析且字段对得上
    header = FileHeader.parse(BytesIO(data))
    assert header.salt == salt
    assert header.iv == iv
    assert header.flags == spec["flags"]
    assert header.version == spec["version"]
    assert header.header_length == 19 + len(salt) + len(iv)
    assert len(data) == header.header_length + spec["size"]

    # 2. 明文按配方重建后，Python 的产物必须与 Go 的逐字节相同
    seed = bytes.fromhex(spec["recipe"]["seed"])
    assert spec["recipe"]["algo"] == "sha256_counter_v1"
    plain = gen.recipe_plain(seed, spec["size"])
    assert len(plain) == spec["size"]

    rebuilt = _rebuild_cpenc(vectors["meta"]["master_password"], plain,
                             salt, iv, spec["flags"], spec["version"])
    assert len(rebuilt) == len(data), (
        f"{spec['slug']}: Python 重建长度 {len(rebuilt)}，Go 产物 {len(data)}"
    )
    diff = next((i for i, (a, b) in enumerate(zip(rebuilt, data)) if a != b), None)
    assert rebuilt == data, (
        f"{spec['slug']}: Python 重建的容器与 Go 产物不一致，首个差异在偏移 {diff}"
    )


@pytest.mark.parametrize("spec", GO_CPENC_SPECS, ids=_spec_ids(GO_CPENC_SPECS))
def test_go_cpenc_decrypts_through_production_path(vectors, gen, spec, tmp_path):
    """走真实的 Decryptor + LocalFolderBackend 解开 Go 产出的密文。

    上一条用原语直接重建比对，这一条用生产管线解密比对 —— 两者都过，
    才说明 Go 的产物在真实读取路径上也是可用的，而不只是字节巧合相同。
    """
    root = _go_artifact(spec["path"]).parent
    backend = LocalFolderBackend(root)
    session = Session(vectors["meta"]["master_password"])
    decryptor = Decryptor(session, backend)

    out = tmp_path / f"{spec['slug']}.plain"
    progress = list(decryptor.download_and_decrypt(Path(spec["path"]).name, str(out)))

    assert progress, "解密过程没有产出任何进度"
    assert progress[-1] == pytest.approx(1.0), "进度未走到 1.0"

    plain = gen.recipe_plain(bytes.fromhex(spec["recipe"]["seed"]), spec["size"])
    assert out.read_bytes() == plain, f"{spec['slug']}: 解密结果与配方明文不符"


# ---------------------------------------------------------------------------
# Vault Marker：Go 产出 -> Python 校验
# ---------------------------------------------------------------------------


@pytest.mark.parametrize("spec", GO_VAULT_SPECS, ids=_spec_ids(GO_VAULT_SPECS))
def test_go_vault_marker_verifies(vectors, spec):
    """Go 构造的 Marker 必须能被 Python 正确校验并解出全部字段。"""
    data = _go_artifact(spec["path"]).read_bytes()

    meta = VaultMarker.verify(data, spec["password"])
    assert meta is not None, f"{spec['slug']}: Python 校验 Go 产出的 Marker 失败"

    assert meta.version == spec["version"]
    assert meta.name == spec["expect_name"]
    assert meta.filename_enc == spec["filename_enc"]
    assert meta.protocol_version == spec["protocol_version"]
    assert meta.has_recovery == spec["expect_has_recovery"]
    assert meta.vault_id == bytes.fromhex(spec["vault_id"])
    assert meta.salt == bytes.fromhex(spec["salt"])
    assert meta.iv == bytes.fromhex(spec["iv"])

    # 名称上限是【字符数】，且必须仍是合法 UTF-8（按字节截会切坏多字节字符）
    assert len(meta.name) <= 32
    assert len(meta.name.encode("utf-8")) >= len(meta.name)


@pytest.mark.parametrize("spec", GO_VAULT_SPECS, ids=_spec_ids(GO_VAULT_SPECS))
def test_go_vault_marker_rejects_wrong_password(vectors, spec):
    """错误主密码必须被 GCM 标签拦下（verify 返回 None）。"""
    data = _go_artifact(spec["path"]).read_bytes()

    assert VaultMarker.verify(data, "绝非正确的主密码-not-the-password") is None
    assert VaultMarker.verify(data, "") is None
    # 主密码只差一个字符也必须失败
    assert VaultMarker.verify(data, spec["password"] + "x") is None


def test_go_recovery_blob_decrypts_to_master_password(vectors):
    """Go 侧 BuildRecoveryBlob 的产物必须能被 Python 解开成主密码。

    这条同时验证了 Go 的恢复块布局（rsalt ‖ ct ‖ tag）与 GCM-16 的 nonce
    用法 —— 恢复码是用户忘记主密码时的唯一出路且服务端零参与，
    两端规则一旦不一致就没有任何补救机会。
    """
    specs = [s for s in vectors["go_emit"]["vault"] if s.get("recovery_secret")]
    assert specs, "go_emit.vault 里没有任何带恢复块的条目"

    for spec in specs:
        data = _go_artifact(spec["path"]).read_bytes()
        secret = bytes.fromhex(spec["recovery_secret"])

        # 恢复码 <-> 随机密钥：Base32 无填充，10 字节 ↔ 16 字符
        assert b32_decode_nopad(spec["recovery_code"]) == secret
        assert len(secret) == 10
        assert len(spec["recovery_code"]) == 16

        head, tail = VaultMarker.split_recovery_tail(data)
        assert len(tail) >= 2, f"{spec['slug']}: 恢复块尾部装不下长度字段"
        blob_len = int.from_bytes(tail[:2], "big")
        blob = tail[2:]
        assert blob_len == len(blob), (
            f"{spec['slug']}: 声明恢复块 {blob_len} 字节，实际 {len(blob)} 字节"
        )
        assert len(head) + len(tail) == len(data), "切分丢了字节"

        assert VaultMarker.decrypt_recovery_blob(blob, secret) == spec["password"], (
            f"{spec['slug']}: Go 产出的恢复块解不出主密码"
        )
        # 错误的恢复密钥必须失败，且不得抛未预期的异常类型
        wrong = bytes([secret[0] ^ 0xFF]) + secret[1:]
        assert VaultMarker.decrypt_recovery_blob(blob, wrong) is None


# ---------------------------------------------------------------------------
# 文件名加密：Go 产出 -> Python 解开
# ---------------------------------------------------------------------------


def test_go_filename_artifacts_decrypt(vectors):
    """Go 侧 EncryptFilename 的产物必须能被 Python 解回原展示名。

    覆盖 CJK、空格、括号、多点号与 emoji（4 字节 UTF-8）—— 文件名一旦解错，
    用户看到的就是乱码目录树，且不会有任何报错。
    """
    spec = vectors["go_emit"]["filename"]
    key = bytes.fromhex(spec["key"])

    # 密钥必须能由主密码 + 夹具里的盐派生出来，否则夹具自相矛盾
    assert Kdf.derive_key(vectors["meta"]["master_password"],
                          bytes.fromhex(spec["salt"])) == key

    items = spec["items"]
    assert items, "go_emit.filename.items 为空"
    for item in items:
        path = _go_artifact(item["path"])
        assert path.is_file(), f"缺少反向夹具 {item['path']}"
        # Go 侧写入时末尾带换行（便于人眼查看），读取时去掉
        encoded = path.read_text(encoding="utf-8").strip()
        assert FilenameCipher.decrypt(encoded, key) == item["plain"], (
            f"{item['path']}: 解出的名字与期望不符"
        )

        # 结构也必须是 nonce(12) ‖ ct ‖ tag(16)
        raw = b32_decode_nopad(encoded)
        assert len(raw) == 12 + len(item["plain"].encode("utf-8")) + 16


# ---------------------------------------------------------------------------
# 一致性兜底
# ---------------------------------------------------------------------------


def test_go_artifacts_are_not_stale(vectors):
    """反向夹具必须与 vectors.json 的声明一一对应，不留孤儿也不缺项。

    Go 侧改了 go_emit 规格却忘了重跑 Emit 时，磁盘上会留下上一轮的旧文件。
    那种情况下本模块的其它用例可能仍然全绿（验证的是过期产物），
    互操作契约名存实亡 —— 这条断言把「忘了重新生成」变成明确的失败。
    """
    declared = {spec["path"] for spec in vectors["go_emit"]["cpenc"]}
    declared |= {spec["path"] for spec in vectors["go_emit"]["vault"]}
    # filename 是 {salt, key, items} 字典而非列表，别漏了 items
    declared |= {item["path"] for item in vectors["go_emit"]["filename"]["items"]}

    on_disk = {
        str(p.relative_to(GO_TESTDATA)).replace("\\", "/")
        for p in GO_ARTIFACTS.rglob("*")
        if p.is_file()
    }

    assert declared - on_disk == set(), f"声明了但磁盘上没有：{sorted(declared - on_disk)}"
    assert on_disk - declared == set(), f"磁盘上有但未声明（孤儿夹具）：{sorted(on_disk - declared)}"


def test_vectors_schema_matches_python_constants(vectors):
    """夹具里记录的协议参数必须与 Python 侧常量一致。

    单侧升版本而忘了重新生成夹具时，其它用例会以一堆莫名其妙的方式变红；
    这条断言把它提前成一个指向明确的信号。
    """
    from cloudprism import constants

    meta = vectors["meta"]
    assert meta["schema"] == 1
    assert meta["protocol_version"] == constants.VERSION
    assert meta["vault_version"] == constants.VAULT_VERSION
    assert meta["kdf"]["iterations"] == constants.KDF_ITERATIONS
    assert meta["kdf"]["key_len"] == constants.KEY_LEN
    assert meta["kdf"]["algo"] == constants.KDF_ALGO

    # 文件名密钥同样要能由主密码派生出来
    fn = vectors["filename_key"]
    assert Kdf.derive_key(meta["master_password"], bytes.fromhex(fn["salt"])) == \
        bytes.fromhex(fn["key"])
