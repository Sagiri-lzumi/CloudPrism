// Package cache 提供本地分块读取缓存（WindowsGo 端）。
//
// 定位：为流式解密代理（pkg/streaming）提供一层「读穿式」磁盘缓存，让大
// 文件（尤其是视频）**重看 / 回拖时不再重新下载**，同时保持首字节延迟
// 与现状一致 —— 不做「整文件下载完再播放」。
//
// 三条硬约束（对应需求原文）：
//
//  1. 只写程序自管目录：缓存根默认 <程序目录>/data/cache，绝无
//     %TEMP% / %APPDATA% / Program Files 兜底（见 pkg/paths 的守卫）。
//  2. 按用户设定的分块大小切块读取：分块大小同时是「是否分块」的阈值 ——
//     小于阈值整存为单独文件，大于等于阈值切成 原名-1 / 原名-2 … 并统一
//     收进一个以原名命名的子文件夹。
//  3. 缓存内容是**密文**：延续项目「明文不落盘」的安全属性；AES-CTR 解密
//     开销可忽略，缓存密文不牺牲性能，却避免明文视频裸奔在磁盘上。
//
// 布局（root = 缓存根，scope = 本密库隔离目录）：
//
//	<root>/media/<scope>/.meta/<sha256(remote)[:16]>.json   条目元信息
//	<root>/media/<scope>/movie.mp4                          小于阈值：单独文件
//	<root>/media/<scope>/movie.mp4/movie.mp4-1              大于等于阈值：原名子文件夹
//	<root>/media/<scope>/movie.mp4/movie.mp4-2
//
// 未完成的块/文件带 ".part" 后缀，覆盖区间补齐后去掉后缀 —— 资源管理器里
// 就能看出哪些还在下载中。
//
// 并发与一致性：块文件可以「稀疏 + 渐进填充」（WriteAt 到块内偏移），
// 覆盖区间记录在元信息里，因此**进度永不依赖整块下载**；元信息原子写盘，
// 崩溃最多丢失一点进度记录（表现为重新下载），不会读到错误字节。
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultChunkMB 是分块大小的默认值（MB）。
	//
	// 50MB 在「块数量合理」与「一次补齐的粒度」之间取平衡：1GB 视频切成
	// 20 块，回拖命中率高，且不会因块过小而元信息爆炸。
	DefaultChunkMB = 50
	// MinChunkMB / MaxChunkMB 是设置页允许的分块大小区间。
	MinChunkMB = 4
	MaxChunkMB = 1024

	// markerName 是作用域目录的占用标记文件。
	//
	// 用户可以把缓存根指到任意目录；标记文件确保 Store 只会在「自己创建过」
	// 的作用域目录里增删文件 —— 万一缓存根被指到个人目录且同名子目录存在，
	// Open 会直接报错而不是误删用户数据（安全兜底）。
	markerName = "scope.json"
	// markerVersion 是标记文件版本；不匹配即视为「非本程序创建」。
	markerVersion = 1

	// flushInterval 是元信息落盘的最小间隔。
	//
	// 播放时每 256KiB 就填一次块，若每次都原子写元信息会造成明显 I/O 抖动；
	// 按间隔节流 + 完成块时强制落盘，兼顾性能与可恢复性。
	flushInterval = 2 * time.Second
)

// Store 是一个密库（后端 + 密库位置）对应的分块缓存。
//
// 每个连接构造一个实例，锁库时丢弃；并发安全，可被多个代理请求共享。
// 任何磁盘错误都只降级为「缓存未命中」，绝不阻断读取链路 —— 缓存是
// 性能优化而非正确性依赖（与 pkg/thumb 的吞异常语义一致）。
type Store struct {
	root      string // 缓存根（<root>/media）
	scope     string // 作用域目录名
	dir       string // <root>/media/<scope>
	chunkSize int64  // 分块大小 = 分块阈值（字节）
	limit     int64  // 容量上限（字节），<=0 表示不限制
	log       *slog.Logger

	// mu 保护 entries/total/closed。**锁序铁律：Store.mu → entry.mu**，
	// 任何路径都不得反向持锁（反了就与 Purge 构成 ABBA 死锁，见 Put）。
	mu      sync.Mutex
	entries map[string]*entry // remote → 条目
	// total 是已占用字节的**估算值**：写入时按实际写入量累加（块文件稀疏
	// 增长，落盘占用略大于写入量），淘汰时按真实占用精确扣减。它只用于
	// 驱动容量上限判断，不做精确记账，允许小幅漂移。
	total  int64
	closed bool
}

// entry 是内存中的一个缓存条目。
type entry struct {
	mu   sync.Mutex   // 序列化同一条目的读与填充（同一文件基本顺序访问）
	busy atomic.Int32 // 正在 IO 的 goroutine 数，淘汰时跳过（避免边写边删）

	// lastAcc 是 meta.LastAccess 的原子镜像，**只用于淘汰排序**。
	// 淘汰在持有 Store.mu 时排序，此时不可能再去取 entry.mu（锁序相反），
	// 直接读 meta.LastAccess 就是与并发 Put 的数据竞争 —— 这里走原子量读。
	lastAcc atomic.Int64

	remote  string
	dirName string    // 落盘名（可能带 " (2)" 消歧后缀）
	meta    *metaJSON // 元信息（entry.mu 保护）

	lastFlush time.Time // 上次元信息落盘时间
	dirty     bool      // 元信息有待落盘
}

// touch 记录最近访问时间（调用方须持 entry.mu）。
func (e *entry) touch(now int64) {
	e.meta.LastAccess = now
	e.lastAcc.Store(now)
}

// Options 是 Store 的构造参数。
type Options struct {
	// Root 是缓存根目录（其下固定使用 media/ 子目录）；必填。
	Root string
	// Scope 是本密库的隔离目录名（由上层按后端/密库标识生成），必填。
	Scope string
	// ChunkMB 是分块大小（MB）；<=0 取 DefaultChunkMB。
	ChunkMB int
	// LimitMB 是容量上限（MB）；<=0 表示不限制。
	LimitMB int
	// Log 可为 nil。
	Log *slog.Logger
}

// Open 打开（必要时创建）分块缓存。
//
// 返回的错误只在「作用域目录被非本程序数据占用」时出现 —— 其余磁盘
// 问题一律降级为可用但空缓存，避免缓存故障拖垮整条连接链路。
func Open(opt Options) (*Store, error) {
	if opt.Root == "" || opt.Scope == "" {
		return nil, fmt.Errorf("cache: 缓存根目录与作用域均不能为空")
	}
	chunkMB := opt.ChunkMB
	if chunkMB <= 0 {
		chunkMB = DefaultChunkMB
	}
	s := &Store{
		root:      filepath.Join(opt.Root, "media"),
		scope:     opt.Scope,
		dir:       filepath.Join(opt.Root, "media", opt.Scope),
		chunkSize: int64(chunkMB) << 20,
		limit:     int64(opt.LimitMB) << 20,
		log:       opt.Log,
		entries:   map[string]*entry{},
	}
	if err := s.prepareDir(); err != nil {
		return nil, err
	}
	s.reload()
	return s, nil
}

// prepareDir 建立作用域目录并写入/校验占用标记。
func (s *Store) prepareDir() error {
	if err := os.MkdirAll(filepath.Join(s.dir, metaDirName), 0o755); err != nil {
		return fmt.Errorf("cache: 创建缓存目录失败: %w", err)
	}
	marker := filepath.Join(s.dir, metaDirName, markerName)
	ents, err := os.ReadDir(s.dir)
	if err == nil && len(ents) > 1 { // 除 .meta 之外还有内容
		if _, statErr := os.Stat(marker); statErr != nil {
			return fmt.Errorf("cache: 目录 %s 存在非缓存数据，已拒绝作为缓存使用", s.dir)
		}
	}
	body, err := json.Marshal(map[string]any{
		"v": markerVersion, "scope": s.scope, "chunkSize": s.chunkSize,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(marker, body, 0o644)
}

// reload 扫描元信息重建内存索引，并清理无主残留文件。
func (s *Store) reload() {
	metas := s.listMetas()
	claimed := map[string]bool{}
	for remote, m := range metas {
		if m.Entry == "" {
			continue
		}
		// lastAcc 由磁盘上的 LastAccess 播种：reload 期间无并发，直接存即可
		e := &entry{remote: remote, dirName: m.Entry, meta: m}
		e.lastAcc.Store(m.LastAccess)
		s.entries[remote] = e
		s.total += s.measure(remote, m)
		claimed[m.Entry] = true
		claimed[m.Entry+partSuffix] = true
	}

	// 无主残留清扫：条目元信息是「块文件属于谁」的唯一凭据，元信息丢了
	// 的块文件不可能再被任何请求命中（按 remote 哈希寻址），属纯垃圾。
	// 清扫范围严格限定在本作用域目录内，且已被占用标记保护。
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, ent := range ents {
		if ent.Name() == metaDirName || claimed[ent.Name()] {
			continue
		}
		_ = os.RemoveAll(filepath.Join(s.dir, ent.Name()))
	}
}

// measure 计算一个条目在磁盘上的实际占用（块文件 + 元信息）。
func (s *Store) measure(remote string, m *metaJSON) int64 {
	var n int64
	for _, c := range m.Chunks {
		dir := s.dir
		if m.Chunked {
			dir = filepath.Join(s.dir, m.Entry)
		}
		n += fileSize(filepath.Join(dir, c.File))
		n += fileSize(filepath.Join(dir, c.File+partSuffix))
	}
	return n + fileSize(s.metaPath(remote))
}

// Close 落盘全部脏元信息。幂等；关闭后读写一律降级为未命中。
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	list := make([]*entry, 0, len(s.entries))
	for _, e := range s.entries {
		list = append(list, e)
	}
	s.mu.Unlock()

	for _, e := range list {
		e.mu.Lock()
		_ = s.flushMeta(e, true)
		e.mu.Unlock()
	}
	return nil
}

// Stats 返回缓存占用字节数与条目数（供设置页展示）。
func (s *Store) Stats() (bytes int64, entries int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total, len(s.entries)
}

// Root 返回缓存根目录（<root>/media）。
func (s *Store) Root() string { return s.root }

// ScopeDir 返回本密库的作用域目录。
func (s *Store) ScopeDir() string { return s.dir }

// ChunkSize 返回生效的分块大小（字节）。
func (s *Store) ChunkSize() int64 { return s.chunkSize }

// ---------------------------------------------------------------------------
// 读路径
// ---------------------------------------------------------------------------

// Fetch 尝试用本地缓存满足密文区间 [start, end]（含两端）。
//
// 全命中返回 (data, true)；任一字节缺失返回 (nil, false)，由调用方走远端。
// 不做「部分命中拼接」：视频是顺序访问，命中要么全有（重看/回拖）要么全无
// （首看），拼接只会让逻辑复杂而无收益。
func (s *Store) Fetch(remote string, cipherSize, start, end int64) ([]byte, bool) {
	if start < 0 || end < start {
		return nil, false
	}
	e := s.lookup(remote, cipherSize)
	if e == nil {
		return nil, false
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.busy.Add(1)
	defer e.busy.Add(-1)

	data := make([]byte, end-start+1)
	for _, idx := range e.chunkIndexes(start, end) {
		chunk := e.meta.Chunks[strconv.Itoa(idx)]
		if chunk == nil {
			return nil, false
		}
		base := int64(idx) * e.meta.ChunkSize
		from, to := max64(start, base), min64(end, base+chunk.Len-1)
		if to < from {
			continue
		}
		relFrom := from - base
		if !chunk.Done && !covers(chunk.Cov, relFrom, to-base+1) {
			return nil, false
		}
		if !readAt(s.chunkPath(e, chunk), data[from-start:to-start+1], relFrom) {
			return nil, false // 文件被外部删除/损坏：按未命中处理
		}
	}
	e.touch(time.Now().Unix())
	e.dirty = true
	_ = s.flushMeta(e, false)
	return data, true
}

// lookup 取出与当前文件匹配的条目：文件大小或分块大小不一致一律视为
// 未命中（远端文件被覆盖，或用户改了分块设置导致旧布局不兼容）。
func (s *Store) lookup(remote string, cipherSize int64) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	e, ok := s.entries[remote]
	if !ok || e.meta.CipherSize != cipherSize || e.meta.ChunkSize != s.chunkSize {
		return nil
	}
	return e
}

// ---------------------------------------------------------------------------
// 写路径（渐进填充）
// ---------------------------------------------------------------------------

// Put 把已下载的密文片段 [start, start+len(data)) 写入缓存。
//
// 落在块内的部分就地 WriteAt（块文件稀疏增长），补齐整块覆盖区间后去掉
// .part 后缀转正。任何失败只记日志：缓存写不进不影响这次读取。
//
// 锁序铁律：**Store.mu → entry.mu**，全包唯一。写块阶段只持 entry.mu，
// 总量记账与淘汰放到释放 entry.mu 之后再取 Store.mu —— 反过来与 Purge
// （持 Store.mu 逐个取 entry.mu）构成 ABBA 死锁：播放中点「清空缓存」必死。
func (s *Store) Put(remote, display string, cipherSize, start int64, data []byte) {
	if len(data) == 0 || start < 0 {
		return
	}
	e, err := s.ensureEntry(remote, display, cipherSize)
	if err != nil {
		s.warn("建立缓存条目失败", err, remote)
		return
	}

	e.busy.Add(1)
	defer e.busy.Add(-1)

	s.writeChunks(e, start, data)

	s.mu.Lock()
	s.total += int64(len(data))
	s.mu.Unlock()
	s.enforceLimit(e.remote)
}

// writeChunks 在 entry.mu 保护下把片段写进所属块并落盘元信息。
//
// 只碰条目自己的状态，不取 Store.mu（锁序见 Put）。
func (s *Store) writeChunks(e *entry, start int64, data []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()

	end := start + int64(len(data)) - 1
	for _, idx := range e.chunkIndexes(start, end) {
		chunk := e.meta.Chunks[strconv.Itoa(idx)]
		if chunk == nil {
			continue
		}
		base := int64(idx) * e.meta.ChunkSize
		from, to := max64(start, base), min64(end, base+chunk.Len-1)
		if to < from {
			continue
		}
		relFrom := from - base
		if !writeAt(s.writePath(e, chunk), data[from-start:to-start+1], relFrom) {
			s.warn("写入缓存块失败", nil, e.remote)
			continue
		}
		if chunk.Done {
			continue
		}
		chunk.Cov = mergeInterval(chunk.Cov, interval{Start: relFrom, End: to - base + 1})
		if covers(chunk.Cov, 0, chunk.Len) {
			// 整块补齐：去掉 .part 后缀转正，元信息不再需要覆盖区间
			if promote(s.finalPath(e, chunk)) {
				chunk.Done, chunk.Cov = true, nil
			}
		}
	}
	e.touch(time.Now().Unix())
	e.dirty = true
	if err := s.flushMeta(e, false); err != nil {
		s.warn("写元信息失败", err, e.remote)
	}
}

// ensureEntry 取出或建立条目；分块大小/文件大小变更导致旧条目不可用时
// 丢弃重建（旧布局与新设置不兼容）。
func (s *Store) ensureEntry(remote, display string, cipherSize int64) (*entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("cache: 已关闭")
	}
	if e, ok := s.entries[remote]; ok {
		if e.meta.CipherSize == cipherSize && e.meta.ChunkSize == s.chunkSize {
			return e, nil
		}
		s.removeLocked(e) // 布局失效：清掉旧块与旧元信息
	}

	meta := &metaJSON{
		V: metaVersion, Remote: remote, Display: display,
		// 是否分块 = 是否达到阈值（分块大小与阈值是同一个值）
		Chunked:    cipherSize >= s.chunkSize,
		ChunkSize:  s.chunkSize,
		CipherSize: cipherSize,
		Chunks:     map[string]*metaChunk{},
	}
	meta.Entry = s.pickNameLocked(display, remote)
	meta.Chunks = buildChunks(meta.Entry, meta.Chunked, cipherSize, s.chunkSize)

	e := &entry{remote: remote, dirName: meta.Entry, meta: meta}
	e.lastAcc.Store(time.Now().Unix()) // 新条目最"新"，不会被立刻淘汰
	if meta.Chunked {
		if err := os.MkdirAll(filepath.Join(s.dir, meta.Entry), 0o755); err != nil {
			return nil, err
		}
	}
	if err := saveMeta(s.metaPath(remote), meta); err != nil {
		return nil, err
	}
	s.entries[remote] = e
	s.total += s.measure(remote, meta)
	return e, nil
}

// buildChunks 按分块布局生成块描述表。
//
// 不分块时只有一个块（索引 0，长度 = 整个密文），文件名就是原名 ——
// 与需求「小于阈值保持为单独文件」一一对应。
func buildChunks(name string, chunked bool, cipherSize, chunkSize int64) map[string]*metaChunk {
	out := map[string]*metaChunk{}
	if !chunked {
		out["0"] = &metaChunk{File: name, Len: cipherSize}
		return out
	}
	for i, off := 0, int64(0); off < cipherSize; i, off = i+1, off+chunkSize {
		n := min64(chunkSize, cipherSize-off)
		// 块文件名 = 原名-N（N 从 1 开始，符合需求给出的 -1/-2/-3 示例）
		out[strconv.Itoa(i)] = &metaChunk{File: fmt.Sprintf("%s-%d", name, i+1), Len: n}
	}
	return out
}

// pickNameLocked 选取落盘名：优先用原名（清洗后），被其它后端路径占用时
// 依次尝试「原名 (2)」「原名 (3)」…，彻底杜绝同名文件互相串扰。
func (s *Store) pickNameLocked(display, remote string) string {
	taken := map[string]string{}
	for rm, e := range s.entries {
		taken[e.dirName] = rm
	}
	base := sanitizeName(display)
	cand := base
	for n := 2; n < 1000; n++ {
		owner, ok := taken[cand]
		if !ok || owner == remote {
			break
		}
		cand = fmt.Sprintf("%s (%d)", base, n)
	}
	// 同名残留（未被任何条目认领）直接清除 —— 我们即将占用这个名字
	_ = os.RemoveAll(filepath.Join(s.dir, cand))
	_ = os.RemoveAll(filepath.Join(s.dir, cand+partSuffix))
	return cand
}

// chunkIndexes 返回区间 [start, end] 覆盖到的块索引序列。
func (e *entry) chunkIndexes(start, end int64) []int {
	first := int(start / e.meta.ChunkSize)
	last := int(end / e.meta.ChunkSize)
	out := make([]int, 0, last-first+1)
	for i := first; i <= last; i++ {
		if _, ok := e.meta.Chunks[strconv.Itoa(i)]; ok {
			out = append(out, i)
		}
	}
	return out
}

// finalPath 返回块文件转正后的最终路径。
func (s *Store) finalPath(e *entry, c *metaChunk) string {
	dir := s.dir
	if e.meta.Chunked {
		dir = filepath.Join(s.dir, e.dirName)
	}
	return filepath.Join(dir, c.File)
}

// writePath 返回写入用的路径：未完成的块一律写 ".part"。
//
// 「未完成 → .part，补齐 → 去后缀」是需求可读性要求的一部分：用户在
// 资源管理器里能直接看出哪些块还在下载中。
func (s *Store) writePath(e *entry, c *metaChunk) string {
	if c.Done {
		return s.finalPath(e, c)
	}
	return s.finalPath(e, c) + partSuffix
}

// chunkPath 计算读取用的块文件路径。
//
// 转正与否以元信息为准；元信息说「未完成」时优先探测 .part，探测不到
// 再回退转正名 —— 兼容「.part 写入后由外部改名」等异常中间态，读不到
// 内容时调用方按未命中处理，不会读到错误字节。
func (s *Store) chunkPath(e *entry, c *metaChunk) string {
	final := s.finalPath(e, c)
	if c.Done {
		return final
	}
	if part := final + partSuffix; fileExists(part) {
		return part
	}
	return final
}

// ---------------------------------------------------------------------------
// 淘汰
// ---------------------------------------------------------------------------

// enforceLimit 超出上限时按最近访问时间淘汰整条（最旧优先）。
//
// 正在 IO 的条目（busy>0）与本次刚写入的条目跳过，避免「边写边删」。
func (s *Store) enforceLimit(keep string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limit <= 0 || s.total <= s.limit {
		return
	}
	list := make([]*entry, 0, len(s.entries))
	for _, e := range s.entries {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].lastAcc.Load() < list[j].lastAcc.Load()
	})
	for _, e := range list {
		if s.total <= s.limit {
			return
		}
		if e.remote == keep || e.busy.Load() > 0 {
			continue
		}
		s.removeLocked(e)
	}
}

// removeLocked 删除条目对应的磁盘内容与内存记录。
//
// 调用方须持有 Store.mu；本函数自己按 **Store.mu → entry.mu** 的顺序取
// entry.mu（取 entry.mu 是为了不与并发 Put 的块内写入互相踩 —— 它保证
// 「删文件」发生在写者收手之后）。同一顺序也是不得反转的全局约定，见 Put。
func (s *Store) removeLocked(e *entry) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// 先按真实占用扣账，再删文件（删完就量不到了）
	s.total -= s.measure(e.remote, e.meta)
	if s.total < 0 {
		s.total = 0
	}
	if e.meta.Chunked {
		_ = os.RemoveAll(filepath.Join(s.dir, e.dirName))
	}
	for _, c := range e.meta.Chunks {
		_ = os.Remove(filepath.Join(s.dir, c.File))
		_ = os.Remove(filepath.Join(s.dir, c.File+partSuffix))
	}
	_ = os.Remove(s.metaPath(e.remote))
	delete(s.entries, e.remote)
}

// Purge 清空本密库的全部缓存（保留作用域目录与占用标记）。
//
// 逐个条目「加锁→删除→解锁」，不在两轮之间长持 Store.mu：清一个大缓存
// 可能删几 GB 文件，长持会让正在播放的 Fetch/lookup 全部排队。entry.mu
// 由 removeLocked 内部按统一锁序取，这里**不能再自己取**（会自锁死）。
func (s *Store) Purge() error {
	s.mu.Lock()
	remotes := make([]string, 0, len(s.entries))
	for rm := range s.entries {
		remotes = append(remotes, rm)
	}
	s.mu.Unlock()

	for _, rm := range remotes {
		s.mu.Lock()
		if e, ok := s.entries[rm]; ok {
			s.removeLocked(e)
		}
		s.mu.Unlock()
	}
	return nil
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// flushMeta 按间隔节流落盘元信息；force 为真时立即落盘。
func (s *Store) flushMeta(e *entry, force bool) error {
	if !e.dirty {
		return nil
	}
	if !force && time.Since(e.lastFlush) < flushInterval {
		return nil
	}
	err := saveMeta(s.metaPath(e.remote), e.meta)
	if err == nil {
		e.lastFlush, e.dirty = time.Now(), false
	}
	return err
}

// warn 记录缓存层警告（Log 为 nil 时静默）。
func (s *Store) warn(msg string, err error, remote string) {
	if s.log == nil {
		return
	}
	if err != nil {
		s.log.Warn("缓存："+msg, "remote", remote, "err", err)
		return
	}
	s.log.Warn("缓存："+msg, "remote", remote)
}

// covers 报告合并区间表是否完整覆盖 [start, start+length)。
func covers(list []interval, start, length int64) bool {
	want := start + length
	for _, iv := range list {
		if iv.Start <= start && iv.End >= want {
			return true
		}
	}
	return false
}

// mergeInterval 把新区间并入有序区间表并合并相邻/重叠段。
func mergeInterval(list []interval, add interval) []interval {
	out := append(list, add)
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	merged := out[:0]
	for _, iv := range out {
		if n := len(merged); n > 0 && iv.Start <= merged[n-1].End {
			if iv.End > merged[n-1].End {
				merged[n-1].End = iv.End
			}
			continue
		}
		merged = append(merged, iv)
	}
	return merged
}

// scopeHash 生成作用域目录名所用的短哈希。
//
// 目的不是保密而是隔离：同一台机器上连接不同密库（甚至同名密库）时缓存
// 必须物理分开，否则会命中另一个密库的密文（解密失败或字节错乱）。
func scopeHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// BuildScope 由密库标识拼出可读的作用域目录名。
//
// 形如 "a1b2c3d4-我的密库"：哈希保证唯一，尾巴保证人可辨认 —— 用户
// 打开缓存目录时能对上是哪个密库的缓存。
func BuildScope(label string, parts ...string) string {
	head := scopeHash(parts...)
	tail := sanitizeName(label)
	if tail == "" || tail == "unnamed" {
		return head
	}
	return head + "-" + tail
}

// fileSize 返回文件大小；不存在或是目录返回 0。
func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return 0
	}
	return st.Size()
}

// fileExists 报告普通文件是否存在。
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// readAt 从文件读取 [off, off+len(buf)) 到 buf；短读/失败返回 false。
func readAt(path string, buf []byte, off int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	n, err := f.ReadAt(buf, off)
	return err == nil && n == len(buf)
}

// writeAt 把 data 写到文件的 off 偏移（不存在则创建，允许稀疏增长）。
func writeAt(path string, data []byte, off int64) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	n, err := f.WriteAt(data, off)
	return err == nil && n == len(data)
}

// promote 把 ".part" 临时文件转正；失败返回 false（保持未完成状态）。
func promote(path string) bool {
	part := path + partSuffix
	if !fileExists(part) {
		return fileExists(path) // 已经是转正状态
	}
	// Windows 上 rename 覆盖已存在目标会失败，先删目标
	_ = os.Remove(path)
	return os.Rename(part, path) == nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
