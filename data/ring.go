package data

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
)

// RingBuffer 面向高频单通道实时采样流的并发安全环形缓冲区。
//
// 采用生产-消费解耦机制（DESIGN.md 陷阱 4）：后台硬件采集协程调用 Push 追加样本时，
// 仅递增原子版本号并通过一次性布尔门栓（ArmWake）按需触发单次轻量 UI 唤醒，杜绝主线程唤醒洪泛。
type RingBuffer struct {
	mu         sync.RWMutex
	buf        []float64
	cap        int
	head       int   // 下一个写入槽位 [0, cap)
	count      int   // 当前有效样本数 [0, cap]
	total      int64 // 自启动/复位以来累计写入样本总数
	t0Base     float64
	sampleRate float64
	dt         float64

	ver       atomic.Uint64
	wakeArmed atomic.Bool
	wakeMu    sync.RWMutex
	wakeFn    func()
}

// NewRingBuffer 创建指定样本容量与采样率（Hz）的单通道环形缓冲区。
func NewRingBuffer(capacity int, sampleRate float64) *RingBuffer {
	if capacity < 1 {
		capacity = 1
	}
	if sampleRate <= 0 || !isFinite(sampleRate) {
		sampleRate = 1.0
	}
	return &RingBuffer{
		buf:        make([]float64, capacity),
		cap:        capacity,
		sampleRate: sampleRate,
		dt:         1.0 / sampleRate,
	}
}

// SetWakeFunc 注册当缓冲区处于空闲挂起态收到新数据时调用的一次性轻量唤醒回调（如 ui.Services.Invalidate）。
func (r *RingBuffer) SetWakeFunc(fn func()) {
	r.wakeMu.Lock()
	r.wakeFn = fn
	r.wakeMu.Unlock()
}

// ArmWake 由渲染主线程在空闲等待新数据时置位唤醒门栓；下次 Push 将触发至多一次 WakeFunc。
func (r *RingBuffer) ArmWake() {
	r.wakeArmed.Store(true)
}

func (r *RingBuffer) notifyWake() {
	if r.wakeArmed.CompareAndSwap(true, false) {
		r.wakeMu.RLock()
		fn := r.wakeFn
		r.wakeMu.RUnlock()
		if fn != nil {
			fn()
		}
	}
}

// Reset 清空环形缓冲并重设起始基准时间戳 t0。
func (r *RingBuffer) Reset(t0 float64) {
	r.mu.Lock()
	r.head = 0
	r.count = 0
	r.total = 0
	r.t0Base = t0
	r.ver.Add(1)
	r.mu.Unlock()
	r.notifyWake()
}

// Push 批量追加采样点（零内存分配）。
func (r *RingBuffer) Push(samples ...float64) {
	n := len(samples)
	if n == 0 {
		return
	}
	r.mu.Lock()
	if n >= r.cap {
		// 若单批数据超过容量，仅保留末尾 cap 个最新样本
		tail := samples[n-r.cap:]
		copy(r.buf, tail)
		r.head = 0
		r.count = r.cap
		r.total += int64(n)
	} else {
		first := min(n, r.cap-r.head)
		copy(r.buf[r.head:r.head+first], samples[:first])
		if rem := n - first; rem > 0 {
			copy(r.buf[:rem], samples[first:])
		}
		r.head = (r.head + n) % r.cap
		r.count = min(r.count+n, r.cap)
		r.total += int64(n)
	}
	r.ver.Add(1)
	r.mu.Unlock()
	r.notifyWake()
}

// Capacity 返回环形缓冲区最大样本容量。
func (r *RingBuffer) Capacity() int { return r.cap }

// SampleRate 返回采样率（Hz）。
func (r *RingBuffer) SampleRate() float64 { return r.sampleRate }

// Dt 返回采样周期（s）。
func (r *RingBuffer) Dt() float64 { return r.dt }

// Version 返回原子递增的数据版本号。
func (r *RingBuffer) Version() uint64 { return r.ver.Load() }

// Len 返回当前缓冲区内有效样本数。
func (r *RingBuffer) Len() int {
	r.mu.RLock()
	c := r.count
	r.mu.RUnlock()
	return c
}

// LatestTime 返回最新写入样本对应的时间戳；若缓冲区为空则返回基准起始时间。
func (r *RingBuffer) LatestTime() float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.total == 0 {
		return r.t0Base
	}
	return r.t0Base + float64(r.total-1)*r.dt
}

// TimeRange 返回当前缓冲区内最早与最新样本覆盖的时间范围。
func (r *RingBuffer) TimeRange() (Range, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.count == 0 {
		return Range{}, false
	}
	firstIdx := r.total - int64(r.count)
	lastIdx := r.total - 1
	return Range{
		Min: r.t0Base + float64(firstIdx)*r.dt,
		Max: r.t0Base + float64(lastIdx)*r.dt,
	}, true
}

// XY 实现 XYSource 接口，按相对索引 [0, Len()) 读取样本。
func (r *RingBuffer) XY(i int) (x, y float64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if i < 0 || i >= r.count {
		return 0, math.NaN()
	}
	oldestSlot := (r.head - r.count + r.cap) % r.cap
	slot := (oldestSlot + i) % r.cap
	absIdx := r.total - int64(r.count) + int64(i)
	return r.t0Base + float64(absIdx)*r.dt, r.buf[slot]
}

// Bounds 实现 XYSource 接口。
func (r *RingBuffer) Bounds() (xRange, yRange Range, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.count == 0 {
		return Range{}, Range{}, false
	}
	oldestSlot := (r.head - r.count + r.cap) % r.cap
	yMin, yMax := math.Inf(1), math.Inf(-1)
	hasY := false
	firstPart := min(r.count, r.cap-oldestSlot)
	for _, v := range r.buf[oldestSlot : oldestSlot+firstPart] {
		if isFinite(v) {
			yMin = min(yMin, v)
			yMax = max(yMax, v)
			hasY = true
		}
	}
	if rem := r.count - firstPart; rem > 0 {
		for _, v := range r.buf[:rem] {
			if isFinite(v) {
				yMin = min(yMin, v)
				yMax = max(yMax, v)
				hasY = true
			}
		}
	}
	if !hasY {
		return Range{}, Range{}, false
	}
	firstIdx := r.total - int64(r.count)
	lastIdx := r.total - 1
	return Range{
		Min: r.t0Base + float64(firstIdx)*r.dt,
		Max: r.t0Base + float64(lastIdx)*r.dt,
	}, Range{Min: yMin, Max: yMax}, true
}

// MonotonicX 实现 XYSource 接口。
func (r *RingBuffer) MonotonicX() bool { return true }

// CopyWindow 将时间区间 [tStart, tEnd] 内的样本拷贝至预分配切片 dst（零堆分配），
// 返回拷贝片段首点的精确时间戳 segT0 与实际拷贝点数 n。
func (r *RingBuffer) CopyWindow(tStart, tEnd float64, dst []float64) (segT0 float64, n int) {
	if len(dst) == 0 || tEnd < tStart {
		return 0, 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.count == 0 {
		return 0, 0
	}
	firstAbs := r.total - int64(r.count)
	lastAbs := r.total - 1

	reqStart := int64(math.Floor((tStart - r.t0Base) / r.dt))
	reqEnd := int64(math.Ceil((tEnd - r.t0Base) / r.dt))
	startAbs := max(firstAbs, reqStart)
	endAbs := min(lastAbs, reqEnd)
	if startAbs > endAbs {
		return 0, 0
	}
	avail := int(endAbs - startAbs + 1)
	if avail > len(dst) {
		// 若 dst 容量小于窗口长度，优先截取靠近右边缘的最新 avail 段或前 len(dst) 点
		avail = len(dst)
	}
	oldestSlot := (r.head - r.count + r.cap) % r.cap
	offset := int(startAbs - firstAbs)
	startSlot := (oldestSlot + offset) % r.cap

	part1 := min(avail, r.cap-startSlot)
	copy(dst[:part1], r.buf[startSlot:startSlot+part1])
	if part2 := avail - part1; part2 > 0 {
		copy(dst[part1:avail], r.buf[:part2])
	}
	return r.t0Base + float64(startAbs)*r.dt, avail
}

// DecimateWindow 将时间窗口 [tStart, tEnd] 零分配降采样至 buckets 个像素桶中，返回有效桶计数。
func (r *RingBuffer) DecimateWindow(tStart, tEnd float64, buckets int, dst []MinMax) int {
	if buckets <= 0 || len(dst) < buckets || tEnd <= tStart {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return decimateRingSlice(r.buf, r.cap, r.head, r.count, r.total, r.t0Base, r.dt, tStart, tEnd, buckets, dst[:buckets])
}

// MultiRingBuffer 面向 16~256 通道同步采集流的高吞吐环形缓冲区。
//
// 内存布局优化：
//   - 采用通道主序（Channel-Major）连续存储 data[ch*capPerCh + slot]，使单通道窗口扫描 100% 顺序命中缓存行；
//   - 同步维护 IEEE-754 保序 int32 镜像层（ord32，总活跃工作集约 10MB 常驻 L3 缓存）与 B=8 分块极值摘要（blkMin/blkMax），
//     配合全通道共享的像素桶跨度表预计算，将 256 通道 × 1kHz × 10s 实时包络提取压缩至亚毫秒级（0 allocs/op）。
type MultiRingBuffer struct {
	mu           sync.RWMutex
	channels     int
	capPerCh     int
	nBlk         int
	buf          []float64 // 长度 = channels * capPerCh（保留完整 float64 原始采样精度）
	ord32        []int32   // 长度 = channels * capPerCh（IEEE-754 保序 float32 键，支持无分支 CMOV 极值聚合）
	blkMin       []int32   // 长度 = channels * nBlk（B=8 分块保序最小键）
	blkMax       []int32   // 长度 = channels * nBlk（B=8 分块保序最大键）
	nonFiniteCnt []int     // 各通道当前环形槽位内非有限值（NaN/Inf）计数
	names        []string
	head         int
	count        int
	total        int64
	t0Base       float64
	sampleRate   float64
	dt           float64

	ver       atomic.Uint64
	wakeArmed atomic.Bool
	wakeMu    sync.RWMutex
	wakeFn    func()
}

const (
	ringBlkShift = 3
	ringBlkSize  = 1 << ringBlkShift // B = 8
	ord32Invalid = int32(-1 << 31)   // math.MinInt32，严格小于一切有限 float32 的保序键 [-0x7f800000, 0x7f7fffff]
)

func float64ToOrd32(v float64) int32 {
	if !isFinite(v) {
		return ord32Invalid
	}
	f := float32(v)
	if math.IsInf(float64(f), 1) {
		f = math.MaxFloat32
	} else if math.IsInf(float64(f), -1) {
		f = -math.MaxFloat32
	}
	b := int32(math.Float32bits(f))
	return b ^ int32(uint32(b>>31)>>1)
}

func ord32ToFloat32(k int32) float32 {
	b := uint32(k ^ int32(uint32(k>>31)>>1))
	return math.Float32frombits(b)
}

// NewMultiRingBuffer 创建多通道同步环形缓冲区。
//
// totalCapacity 为所有通道总样本容量（例如 128 * 2000 * 30 表示 128 通道 2kHz 缓存 30 秒）；
// 每通道实际分配容量为 max(totalCapacity / channels, 1)。
func NewMultiRingBuffer(channels int, sampleRate float64, totalCapacity int) *MultiRingBuffer {
	if channels < 1 {
		channels = 1
	}
	perCh := max(totalCapacity/channels, 1)
	return NewMultiRingBufferPerChannel(channels, sampleRate, perCh)
}

// NewMultiRingBufferPerChannel 按每通道独立样本容量创建多通道同步环形缓冲区。
func NewMultiRingBufferPerChannel(channels int, sampleRate float64, samplesPerChannel int) *MultiRingBuffer {
	if channels < 1 {
		channels = 1
	}
	if samplesPerChannel < 1 {
		samplesPerChannel = 1
	}
	if sampleRate <= 0 || !isFinite(sampleRate) {
		sampleRate = 1.0
	}
	nBlk := (samplesPerChannel + ringBlkSize - 1) >> ringBlkShift
	names := make([]string, channels)
	for i := range channels {
		names[i] = fmt.Sprintf("Ch%02d", i+1)
	}
	return &MultiRingBuffer{
		channels:     channels,
		capPerCh:     samplesPerChannel,
		nBlk:         nBlk,
		buf:          make([]float64, channels*samplesPerChannel),
		ord32:        make([]int32, channels*samplesPerChannel),
		blkMin:       make([]int32, channels*nBlk),
		blkMax:       make([]int32, channels*nBlk),
		nonFiniteCnt: make([]int, channels),
		names:        names,
		sampleRate:   sampleRate,
		dt:           1.0 / sampleRate,
	}
}

// SetChannelNames 自定义各导联名称（如国际 10-20 脑电导联名 Fp1, Fp2, C3, C4, O1, O2...）。
func (m *MultiRingBuffer) SetChannelNames(names []string) {
	m.mu.Lock()
	for i := 0; i < len(names) && i < m.channels; i++ {
		m.names[i] = names[i]
	}
	m.ver.Add(1)
	m.mu.Unlock()
}

// SetWakeFunc 注册空闲挂起态收到首个新采样块时的轻量唤醒回调。
func (m *MultiRingBuffer) SetWakeFunc(fn func()) {
	m.wakeMu.Lock()
	m.wakeFn = fn
	m.wakeMu.Unlock()
}

// ArmWake 置位一次性唤醒门栓。
func (m *MultiRingBuffer) ArmWake() {
	m.wakeArmed.Store(true)
}

func (m *MultiRingBuffer) notifyWake() {
	if m.wakeArmed.CompareAndSwap(true, false) {
		m.wakeMu.RLock()
		fn := m.wakeFn
		m.wakeMu.RUnlock()
		if fn != nil {
			fn()
		}
	}
}

// Reset 清空所有通道历史样本并重设起始时间基准 t0。
func (m *MultiRingBuffer) Reset(t0 float64) {
	m.mu.Lock()
	for ch := range m.channels {
		if m.nonFiniteCnt[ch] != 0 {
			clear(m.ord32[ch*m.capPerCh : (ch+1)*m.capPerCh])
			m.nonFiniteCnt[ch] = 0
		}
	}
	m.head = 0
	m.count = 0
	m.total = 0
	m.t0Base = t0
	m.ver.Add(1)
	m.mu.Unlock()
	m.notifyWake()
}

func (m *MultiRingBuffer) recomputeChannelBlocks(ch, startSlot, countSlots int) {
	if countSlots <= 0 {
		return
	}
	capCh := m.capPerCh
	nBlk := m.nBlk
	chOrd := m.ord32[ch*capCh : (ch+1)*capCh]
	chBMin := m.blkMin[ch*nBlk : (ch+1)*nBlk]
	chBMax := m.blkMax[ch*nBlk : (ch+1)*nBlk]
	bStart := startSlot >> ringBlkShift
	bEnd := (startSlot + countSlots - 1) >> ringBlkShift

	if m.nonFiniteCnt[ch] == 0 {
		for blk := bStart; blk <= bEnd; blk++ {
			s0 := blk << ringBlkShift
			if s0+ringBlkSize <= capCh {
				s := chOrd[s0 : s0+ringBlkSize : s0+ringBlkSize]
				mn := min(min(min(s[0], s[1]), min(s[2], s[3])), min(min(s[4], s[5]), min(s[6], s[7])))
				mx := max(max(max(s[0], s[1]), max(s[2], s[3])), max(max(s[4], s[5]), max(s[6], s[7])))
				chBMin[blk] = mn
				chBMax[blk] = mx
			} else {
				s := chOrd[s0:capCh]
				mn, mx := s[0], s[0]
				for i := 1; i < len(s); i++ {
					mn = min(mn, s[i])
					mx = max(mx, s[i])
				}
				chBMin[blk] = mn
				chBMax[blk] = mx
			}
		}
		return
	}

	for blk := bStart; blk <= bEnd; blk++ {
		s0 := blk << ringBlkShift
		s1 := min(s0+ringBlkSize, capCh)
		mn := int32(math.MaxInt32)
		mx := ord32Invalid
		has := false
		for _, k := range chOrd[s0:s1] {
			if k != ord32Invalid {
				mn = min(mn, k)
				mx = max(mx, k)
				has = true
			}
		}
		if has {
			chBMin[blk] = mn
			chBMax[blk] = mx
		} else {
			chBMin[blk] = ord32Invalid
			chBMax[blk] = ord32Invalid
		}
	}
}

func (m *MultiRingBuffer) writeChannelContig(ch, dstSlot int, src []float64) {
	n := len(src)
	if n == 0 {
		return
	}
	base := ch*m.capPerCh + dstSlot
	copy(m.buf[base:base+n], src)
	ordDst := m.ord32[base : base+n]
	hadNF := m.nonFiniteCnt[ch] > 0
	nfDelta := 0
	for i, v := range src {
		if hadNF && ordDst[i] == ord32Invalid {
			nfDelta--
		}
		k := float64ToOrd32(v)
		if k == ord32Invalid {
			nfDelta++
		}
		ordDst[i] = k
	}
	m.nonFiniteCnt[ch] += nfDelta
	m.recomputeChannelBlocks(ch, dstSlot, n)
}

// Channels 返回总通道数。
func (m *MultiRingBuffer) Channels() int { return m.channels }

// CapacityPerChannel 返回单通道最大缓存样本数。
func (m *MultiRingBuffer) CapacityPerChannel() int { return m.capPerCh }

// SampleRate 返回每通道采样率（Hz）。
func (m *MultiRingBuffer) SampleRate() float64 { return m.sampleRate }

// Dt 返回采样周期（s）。
func (m *MultiRingBuffer) Dt() float64 { return m.dt }

// Version 返回原子递增版本号。
func (m *MultiRingBuffer) Version() uint64 { return m.ver.Load() }

// ChannelName 返回第 ch 通道的导联名称。
func (m *MultiRingBuffer) ChannelName(ch int) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if ch < 0 || ch >= m.channels {
		return ""
	}
	return m.names[ch]
}

// Count 返回当前每通道已缓存的有效样本数。
func (m *MultiRingBuffer) Count() int {
	m.mu.RLock()
	c := m.count
	m.mu.RUnlock()
	return c
}

// LatestTime 返回最新同步采样帧的时间戳。
func (m *MultiRingBuffer) LatestTime() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.total == 0 {
		return m.t0Base
	}
	return m.t0Base + float64(m.total-1)*m.dt
}

// PushFrame 追加单个时刻的全部通道采样帧（len(frame) >= Channels()）。
func (m *MultiRingBuffer) PushFrame(frame []float64) {
	if len(frame) < m.channels {
		return
	}
	m.mu.Lock()
	slot := m.head
	capCh := m.capPerCh
	for ch := range m.channels {
		idx := ch*capCh + slot
		v := frame[ch]
		m.buf[idx] = v
		if m.nonFiniteCnt[ch] > 0 && m.ord32[idx] == ord32Invalid {
			m.nonFiniteCnt[ch]--
		}
		k := float64ToOrd32(v)
		if k == ord32Invalid {
			m.nonFiniteCnt[ch]++
		}
		m.ord32[idx] = k
		m.recomputeChannelBlocks(ch, slot, 1)
	}
	m.head = (slot + 1) % capCh
	m.count = min(m.count+1, capCh)
	m.total++
	m.ver.Add(1)
	m.mu.Unlock()
	m.notifyWake()
}

// PushInterleaved 批量追加交错排列的多通道数据包（frames * Channels()，硬件 DMA/USB 采集卡常见格式）。
func (m *MultiRingBuffer) PushInterleaved(interleaved []float64) {
	frames := len(interleaved) / m.channels
	if frames <= 0 {
		return
	}
	m.mu.Lock()
	chs := m.channels
	capCh := m.capPerCh
	startF := 0
	if frames > capCh {
		startF = frames - capCh
	}
	keep := frames - startF
	head0 := m.head
	for f := range keep {
		slot := (head0 + f) % capCh
		base := (startF + f) * chs
		for ch := range chs {
			idx := ch*capCh + slot
			v := interleaved[base+ch]
			m.buf[idx] = v
			if m.nonFiniteCnt[ch] > 0 && m.ord32[idx] == ord32Invalid {
				m.nonFiniteCnt[ch]--
			}
			k := float64ToOrd32(v)
			if k == ord32Invalid {
				m.nonFiniteCnt[ch]++
			}
			m.ord32[idx] = k
		}
	}
	first := min(keep, capCh-head0)
	rem := keep - first
	for ch := range chs {
		m.recomputeChannelBlocks(ch, head0, first)
		if rem > 0 {
			m.recomputeChannelBlocks(ch, 0, rem)
		}
	}
	m.head = (head0 + keep) % capCh
	m.count = min(m.count+keep, capCh)
	m.total += int64(frames)
	m.ver.Add(1)
	m.mu.Unlock()
	m.notifyWake()
}

// PushChannelBlocks 批量追加按通道组织的二维采样块（len(blocks) == Channels()，每通道长度相等）。
func (m *MultiRingBuffer) PushChannelBlocks(blocks [][]float64) {
	if len(blocks) < m.channels {
		return
	}
	n := len(blocks[0])
	for ch := 1; ch < m.channels; ch++ {
		n = min(n, len(blocks[ch]))
	}
	if n <= 0 {
		return
	}
	m.mu.Lock()
	capCh := m.capPerCh
	if n >= capCh {
		for ch := range m.channels {
			m.writeChannelContig(ch, 0, blocks[ch][n-capCh:n])
		}
		m.head = 0
		m.count = capCh
		m.total += int64(n)
	} else {
		first := min(n, capCh-m.head)
		rem := n - first
		for ch := range m.channels {
			src := blocks[ch][:n]
			m.writeChannelContig(ch, m.head, src[:first])
			if rem > 0 {
				m.writeChannelContig(ch, 0, src[first:])
			}
		}
		m.head = (m.head + n) % capCh
		m.count = min(m.count+n, capCh)
		m.total += int64(n)
	}
	m.ver.Add(1)
	m.mu.Unlock()
	m.notifyWake()
}

// DecimateWindow 读取第 ch 通道在 [tStart, tEnd] 窗口内的像素列包络（滚动走纸模式 Roll Mode，零分配）。
func (m *MultiRingBuffer) DecimateWindow(ch int, tStart, tEnd float64, buckets int, dst []MinMax) int {
	if ch < 0 || ch >= m.channels || buckets <= 0 || len(dst) < buckets || tEnd <= tStart {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	chBuf := m.buf[ch*m.capPerCh : (ch+1)*m.capPerCh]
	return decimateRingSlice(chBuf, m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, tStart, tEnd, buckets, dst[:buckets])
}

// DecimateSweep 按监护仪/示波器擦除扫描模式（Sweep / Erase-Bar Mode）生成固定窗口 [0, windowSec] 的像素桶包络：
//   - 扫描头左侧 [0, cursorPhase] 绘制当前周期最新波形；
//   - 扫描头右侧 [cursorPhase, cursorPhase + eraseGapSec] 保持空白擦除间隙（Valid == false）；
//   - 擦除间隙右侧至窗口末端绘制上一扫描周期保留的历史旧波形。
//
// 返回归一化扫描头位置 cursorNorm ∈ [0, 1] 与有效桶总数（全程 0 堆内存分配）。
func (m *MultiRingBuffer) DecimateSweep(ch int, windowSec, eraseGapSec float64, buckets int, dst []MinMax) (cursorNorm float64, valid int) {
	if ch < 0 || ch >= m.channels || buckets <= 0 || len(dst) < buckets || windowSec <= 0 {
		return 0, 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := dst[:buckets]
	clear(out)
	if m.count == 0 {
		return 0, 0
	}

	tNow := m.t0Base + float64(m.total-1)*m.dt
	relNow := tNow - m.t0Base
	cycleIdx := math.Floor(relNow / windowSec)
	cycleStart := m.t0Base + cycleIdx*windowSec
	cursorPhase := relNow - cycleIdx*windowSec
	cursorNorm = min(max(cursorPhase/windowSec, 0), 1)

	chBuf := m.buf[ch*m.capPerCh : (ch+1)*m.capPerCh]

	// 1. 当前扫描周期的新波形段：映射到桶区间 [0, newBuckets)
	newBuckets := int(math.Floor(cursorNorm * float64(buckets)))
	if newBuckets > 0 {
		tSegEnd := cycleStart + float64(newBuckets)/float64(buckets)*windowSec
		valid += decimateRingSliceNoClear(chBuf, m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, cycleStart, tSegEnd, newBuckets, out[:newBuckets])
	} else if m.count > 0 {
		// 周期刚翻转的第一个桶
		valid += decimateRingSliceNoClear(chBuf, m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, cycleStart, cycleStart+windowSec/float64(buckets), 1, out[:1])
	}

	// 2. 跳过擦除空白间隙，在右侧填充上一周期历史波形段
	gapFrac := max(eraseGapSec/windowSec, 0)
	oldStartBucket := int(math.Ceil((cursorNorm + gapFrac) * float64(buckets)))
	if oldStartBucket < buckets {
		oldBuckets := buckets - oldStartBucket
		prevCycleStart := cycleStart - windowSec
		tOldStart := prevCycleStart + float64(oldStartBucket)/float64(buckets)*windowSec
		tOldEnd := prevCycleStart + windowSec
		valid += decimateRingSliceNoClear(chBuf, m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, tOldStart, tOldEnd, oldBuckets, out[oldStartBucket:buckets])
	}

	return cursorNorm, valid
}

// DecimateChannelsWindow 在单次读锁临界区内批量聚合 [chStart, chEnd) 导联的滚动窗口像素包络（dst 长度需 >= (chEnd-chStart)*buckets）。
func (m *MultiRingBuffer) DecimateChannelsWindow(chStart, chEnd int, tStart, tEnd float64, buckets int, dst []MinMax) int {
	chStart = max(chStart, 0)
	chEnd = min(chEnd, m.channels)
	nCh := chEnd - chStart
	if nCh <= 0 || buckets <= 0 || len(dst) < nCh*buckets || tEnd <= tStart {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	totalValid := 0
	for i := range nCh {
		ch := chStart + i
		chBuf := m.buf[ch*m.capPerCh : (ch+1)*m.capPerCh]
		out := dst[i*buckets : (i+1)*buckets]
		totalValid += decimateRingSlice(chBuf, m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, tStart, tEnd, buckets, out)
	}
	return totalValid
}

// DecimateChannelsSweep 在单次读锁临界区内批量聚合 [chStart, chEnd) 导联的擦除扫描模式包络（dst 长度需 >= (chEnd-chStart)*buckets）。
func (m *MultiRingBuffer) DecimateChannelsSweep(chStart, chEnd int, windowSec, eraseGapSec float64, buckets int, dst []MinMax) (cursorNorm float64, totalValid int) {
	chStart = max(chStart, 0)
	chEnd = min(chEnd, m.channels)
	nCh := chEnd - chStart
	if nCh <= 0 || buckets <= 0 || len(dst) < nCh*buckets || windowSec <= 0 {
		return 0, 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	clear(dst[:nCh*buckets])
	if m.count == 0 {
		return 0, 0
	}

	tNow := m.t0Base + float64(m.total-1)*m.dt
	relNow := tNow - m.t0Base
	cycleIdx := math.Floor(relNow / windowSec)
	cycleStart := m.t0Base + cycleIdx*windowSec
	cursorPhase := relNow - cycleIdx*windowSec
	cursorNorm = min(max(cursorPhase/windowSec, 0), 1)

	newBuckets := int(math.Floor(cursorNorm * float64(buckets)))
	gapFrac := max(eraseGapSec/windowSec, 0)
	oldStartBucket := int(math.Ceil((cursorNorm + gapFrac) * float64(buckets)))

	for i := range nCh {
		ch := chStart + i
		chBuf := m.buf[ch*m.capPerCh : (ch+1)*m.capPerCh]
		out := dst[i*buckets : (i+1)*buckets]
		if newBuckets > 0 {
			tSegEnd := cycleStart + float64(newBuckets)/float64(buckets)*windowSec
			totalValid += decimateRingSliceNoClear(chBuf, m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, cycleStart, tSegEnd, newBuckets, out[:newBuckets])
		} else {
			totalValid += decimateRingSliceNoClear(chBuf, m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, cycleStart, cycleStart+windowSec/float64(buckets), 1, out[:1])
		}
		if oldStartBucket < buckets {
			oldBuckets := buckets - oldStartBucket
			prevCycleStart := cycleStart - windowSec
			tOldStart := prevCycleStart + float64(oldStartBucket)/float64(buckets)*windowSec
			tOldEnd := prevCycleStart + windowSec
			totalValid += decimateRingSliceNoClear(chBuf, m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, tOldStart, tOldEnd, oldBuckets, out[oldStartBucket:buckets])
		}
	}
	return cursorNorm, totalValid
}

// ScanWindowMinMax 在单次读锁内预计算一次像素桶跨度表，并利用 L3 驻留保序 int32 镜像层 + B=8 分块极值表
// 批量扫描全部通道在滚动窗口 [tStart, tEnd] 内的 [Min, Max] 包络，每完成一通道立即回调 visit(ch)（0 allocs/op）。
func (m *MultiRingBuffer) ScanWindowMinMax(
	tStart, tEnd float64,
	buckets int,
	spanSlot0, spanLen []int32,
	dstMin, dstMax []float32,
	dstValid []byte,
	visit func(ch int),
) {
	if buckets <= 0 || len(spanSlot0) < buckets || len(spanLen) < buckets ||
		len(dstMin) < buckets || len(dstMax) < buckets || len(dstValid) < buckets || visit == nil {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	sSlot0 := spanSlot0[:buckets]
	sLen := spanLen[:buckets]
	clear(sLen)
	if m.count > 0 && tEnd > tStart {
		buildRingWindowSpansNoClear(m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, tStart, tEnd, buckets, sSlot0, sLen)
	}
	m.scanAllChannelsBySpans(buckets, sSlot0, sLen, dstMin[:buckets], dstMax[:buckets], dstValid[:buckets], visit)
}

// ScanSweepMinMax 在单次读锁内预计算一次擦除扫描模式像素桶跨度表，并批量扫描全部通道的 [Min, Max] 包络（0 allocs/op）。
func (m *MultiRingBuffer) ScanSweepMinMax(
	windowSec, eraseGapSec float64,
	buckets int,
	spanSlot0, spanLen []int32,
	dstMin, dstMax []float32,
	dstValid []byte,
	visit func(ch int),
) (cursorNorm float64) {
	if buckets <= 0 || len(spanSlot0) < buckets || len(spanLen) < buckets ||
		len(dstMin) < buckets || len(dstMax) < buckets || len(dstValid) < buckets || windowSec <= 0 || visit == nil {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	sSlot0 := spanSlot0[:buckets]
	sLen := spanLen[:buckets]
	clear(sLen)
	if m.count > 0 {
		tNow := m.t0Base + float64(m.total-1)*m.dt
		relNow := tNow - m.t0Base
		cycleIdx := math.Floor(relNow / windowSec)
		cycleStart := m.t0Base + cycleIdx*windowSec
		cursorPhase := relNow - cycleIdx*windowSec
		cursorNorm = min(max(cursorPhase/windowSec, 0), 1)

		newBuckets := int(math.Floor(cursorNorm * float64(buckets)))
		if newBuckets > 0 {
			tSegEnd := cycleStart + float64(newBuckets)/float64(buckets)*windowSec
			buildRingWindowSpansNoClear(m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, cycleStart, tSegEnd, newBuckets, sSlot0[:newBuckets], sLen[:newBuckets])
		} else {
			buildRingWindowSpansNoClear(m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, cycleStart, cycleStart+windowSec/float64(buckets), 1, sSlot0[:1], sLen[:1])
		}
		gapFrac := max(eraseGapSec/windowSec, 0)
		oldStart := int(math.Ceil((cursorNorm + gapFrac) * float64(buckets)))
		if oldStart < buckets {
			oldB := buckets - oldStart
			prevStart := cycleStart - windowSec
			tOld0 := prevStart + float64(oldStart)/float64(buckets)*windowSec
			tOld1 := prevStart + windowSec
			buildRingWindowSpansNoClear(m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, tOld0, tOld1, oldB, sSlot0[oldStart:buckets], sLen[oldStart:buckets])
		}
	}
	m.scanAllChannelsBySpans(buckets, sSlot0, sLen, dstMin[:buckets], dstMax[:buckets], dstValid[:buckets], visit)
	return cursorNorm
}

const (
	numPackHelpers    = 3 // 3 常驻休眠辅助协程 + 调用者主线程 = 4 路零分配并行分片
	maxPoolPackBucket = 4096
)

type packWorkerScratch struct {
	minBuf [maxPoolPackBucket]float32
	maxBuf [maxPoolPackBucket]float32
	valBuf [maxPoolPackBucket]byte
}

var shaderPackPool struct {
	once      sync.Once
	mu        sync.Mutex
	ring      *MultiRingBuffer
	buckets   int
	spanSlot0 []int32
	spanLen   []int32
	doDC      bool
	quantMul  float32
	dstPix    []byte
	dstStride int
	workCh    [numPackHelpers]chan int
	doneCh    chan struct{}
	bufs      [numPackHelpers + 1]packWorkerScratch
}

func initShaderPackPool() {
	shaderPackPool.doneCh = make(chan struct{}, numPackHelpers)
	for i := range numPackHelpers {
		ch := make(chan int, 1)
		shaderPackPool.workCh[i] = ch
		go func(c <-chan int) {
			for workerID := range c {
				r := shaderPackPool.ring
				nCh := r.channels
				ch0 := (workerID * nCh) / (numPackHelpers + 1)
				ch1 := ((workerID + 1) * nCh) / (numPackHelpers + 1)
				packChannelRange16(
					r,
					ch0, ch1,
					shaderPackPool.buckets,
					shaderPackPool.spanSlot0,
					shaderPackPool.spanLen,
					shaderPackPool.doDC,
					shaderPackPool.quantMul,
					shaderPackPool.dstPix,
					shaderPackPool.dstStride,
					shaderPackPool.bufs[workerID].minBuf[:shaderPackPool.buckets],
					shaderPackPool.bufs[workerID].maxBuf[:shaderPackPool.buckets],
					shaderPackPool.bufs[workerID].valBuf[:shaderPackPool.buckets],
				)
				shaderPackPool.doneCh <- struct{}{}
			}
		}(ch)
	}
}

// PackShaderEnvelope 在单次读锁内完成全部导联的极值降采样、可选实时去直流（DC Removal）与 16-bit RGBA 包络纹理打包（0 allocs/op）。
// 当通道数 >= 64 且 buckets <= 4096 时，自动唤醒 4 路零分配并行工作分片，在多核降频/满载压力下仍确保 256ch x 1kHz x 10s 远低于 6ms 门禁。
func (m *MultiRingBuffer) PackShaderEnvelope(
	tStart, tEnd float64,
	sweepMode bool,
	windowSec, eraseGapSec float64,
	buckets int,
	doDC bool,
	quantMul float32,
	dstPix []byte,
	dstStride int,
	spanSlot0, spanLen []int32,
	minScratch, maxScratch []float32,
	valScratch []byte,
) (cursorNorm float64) {
	cursorNorm = -1
	if buckets <= 0 || len(spanSlot0) < buckets || len(spanLen) < buckets ||
		len(minScratch) < buckets || len(maxScratch) < buckets || len(valScratch) < buckets {
		return cursorNorm
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	sSlot0 := spanSlot0[:buckets]
	sLen := spanLen[:buckets]
	clear(sLen)

	if sweepMode {
		cursorNorm = 0
		if m.count > 0 && windowSec > 0 {
			tNow := m.t0Base + float64(m.total-1)*m.dt
			relNow := tNow - m.t0Base
			cycleIdx := math.Floor(relNow / windowSec)
			cycleStart := m.t0Base + cycleIdx*windowSec
			cursorPhase := relNow - cycleIdx*windowSec
			cursorNorm = min(max(cursorPhase/windowSec, 0), 1)

			newBuckets := int(math.Floor(cursorNorm * float64(buckets)))
			if newBuckets > 0 {
				tSegEnd := cycleStart + float64(newBuckets)/float64(buckets)*windowSec
				buildRingWindowSpansNoClear(m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, cycleStart, tSegEnd, newBuckets, sSlot0[:newBuckets], sLen[:newBuckets])
			} else {
				buildRingWindowSpansNoClear(m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, cycleStart, cycleStart+windowSec/float64(buckets), 1, sSlot0[:1], sLen[:1])
			}
			gapFrac := max(eraseGapSec/windowSec, 0)
			oldStart := int(math.Ceil((cursorNorm + gapFrac) * float64(buckets)))
			if oldStart < buckets {
				oldB := buckets - oldStart
				prevStart := cycleStart - windowSec
				tOld0 := prevStart + float64(oldStart)/float64(buckets)*windowSec
				tOld1 := prevStart + windowSec
				buildRingWindowSpansNoClear(m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, tOld0, tOld1, oldB, sSlot0[oldStart:buckets], sLen[oldStart:buckets])
			}
		}
	} else if m.count > 0 && tEnd > tStart {
		buildRingWindowSpansNoClear(m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, tStart, tEnd, buckets, sSlot0, sLen)
	}

	if m.channels >= 64 && buckets <= maxPoolPackBucket {
		shaderPackPool.once.Do(initShaderPackPool)
		shaderPackPool.mu.Lock()
		shaderPackPool.ring = m
		shaderPackPool.buckets = buckets
		shaderPackPool.spanSlot0 = sSlot0
		shaderPackPool.spanLen = sLen
		shaderPackPool.doDC = doDC
		shaderPackPool.quantMul = quantMul
		shaderPackPool.dstPix = dstPix
		shaderPackPool.dstStride = dstStride

		for i := range numPackHelpers {
			shaderPackPool.workCh[i] <- i
		}
		// 调用者主线程同步执行第 4 分片，零上下文切换浪费
		ch0 := (numPackHelpers * m.channels) / (numPackHelpers + 1)
		packChannelRange16(
			m,
			ch0, m.channels,
			buckets,
			sSlot0, sLen,
			doDC, quantMul,
			dstPix, dstStride,
			shaderPackPool.bufs[numPackHelpers].minBuf[:buckets],
			shaderPackPool.bufs[numPackHelpers].maxBuf[:buckets],
			shaderPackPool.bufs[numPackHelpers].valBuf[:buckets],
		)
		for range numPackHelpers {
			<-shaderPackPool.doneCh
		}
		// 立即清空临时引用，确保 GC 不滞留任何环形缓冲或像素切片
		shaderPackPool.ring = nil
		shaderPackPool.spanSlot0 = nil
		shaderPackPool.spanLen = nil
		shaderPackPool.dstPix = nil
		shaderPackPool.mu.Unlock()
		return cursorNorm
	}

	packChannelRange16(m, 0, m.channels, buckets, sSlot0, sLen, doDC, quantMul, dstPix, dstStride, minScratch[:buckets], maxScratch[:buckets], valScratch[:buckets])
	return cursorNorm
}

func packChannelRange16(
	m *MultiRingBuffer,
	chStart, chEnd int,
	buckets int,
	sSlot0, sLen []int32,
	doDC bool,
	quantMul float32,
	pix []byte,
	stride int,
	dMin, dMax []float32,
	dVal []byte,
) {
	capCh := m.capPerCh
	nBlk := m.nBlk

	for ch := chStart; ch < chEnd; ch++ {
		chOrd := m.ord32[ch*capCh : (ch+1)*capCh]
		chBMin := m.blkMin[ch*nBlk : (ch+1)*nBlk]
		chBMax := m.blkMax[ch*nBlk : (ch+1)*nBlk]

		var sum float32
		var cnt int

		if m.nonFiniteCnt[ch] == 0 {
			for b := range buckets {
				n := int(sLen[b])
				if n <= 0 {
					dVal[b] = 0
					continue
				}
				slot0 := int(sSlot0[b])
				var mnK, mxK int32
				if slot0+n <= capCh {
					if n < ringBlkSize*2 {
						seg := chOrd[slot0 : slot0+n]
						mn0, mx0 := seg[0], seg[0]
						if n > 1 {
							mn1, mx1 := seg[1], seg[1]
							i := 2
							for ; i+3 < n; i += 4 {
								s := seg[i : i+4 : i+4]
								mn0 = min(mn0, min(s[0], s[2]))
								mx0 = max(mx0, max(s[0], s[2]))
								mn1 = min(mn1, min(s[1], s[3]))
								mx1 = max(mx1, max(s[1], s[3]))
							}
							mn0 = min(mn0, mn1)
							mx0 = max(mx0, mx1)
							for ; i < n; i++ {
								v := seg[i]
								mn0 = min(mn0, v)
								mx0 = max(mx0, v)
							}
						}
						mnK, mxK = mn0, mx0
					} else {
						mnK, mxK = scanWideOrd32Clean(chOrd, chBMin, chBMax, slot0, slot0+n)
					}
				} else {
					mn1, mx1 := scanContigOrd32Clean(chOrd, chBMin, chBMax, slot0, capCh)
					mn2, mx2 := scanContigOrd32Clean(chOrd, chBMin, chBMax, 0, slot0+n-capCh)
					mnK = min(mn1, mn2)
					mxK = max(mx1, mx2)
				}
				vLo := math.Float32frombits(uint32(mnK ^ int32(uint32(mnK>>31)>>1)))
				vHi := math.Float32frombits(uint32(mxK ^ int32(uint32(mxK>>31)>>1)))
				dMin[b] = vLo
				dMax[b] = vHi
				dVal[b] = 1
				if doDC {
					sum += vLo + vHi
					cnt++
				}
			}
		} else {
			for b := range buckets {
				n := int(sLen[b])
				if n <= 0 {
					dVal[b] = 0
					continue
				}
				slot0 := int(sSlot0[b])
				var mnK, mxK int32
				var ok bool
				if slot0+n <= capCh {
					mnK, mxK, ok = scanContigOrd32WithNaN(chOrd, chBMin, chBMax, slot0, slot0+n)
				} else {
					mn1, mx1, ok1 := scanContigOrd32WithNaN(chOrd, chBMin, chBMax, slot0, capCh)
					mn2, mx2, ok2 := scanContigOrd32WithNaN(chOrd, chBMin, chBMax, 0, slot0+n-capCh)
					if ok1 && ok2 {
						mnK, mxK, ok = min(mn1, mn2), max(mx1, mx2), true
					} else if ok1 {
						mnK, mxK, ok = mn1, mx1, true
					} else if ok2 {
						mnK, mxK, ok = mn2, mx2, true
					}
				}
				if ok {
					vLo := ord32ToFloat32(mnK)
					vHi := ord32ToFloat32(mxK)
					dMin[b] = vLo
					dMax[b] = vHi
					dVal[b] = 1
					if doDC {
						sum += vLo + vHi
						cnt++
					}
				} else {
					dVal[b] = 0
				}
			}
		}

		var dcMean float32
		if doDC && cnt > 0 {
			dcMean = sum / float32(cnt*2)
		}
		bias := float32(32768.5) - dcMean*quantMul
		rowMinPix := pix[(2*ch)*stride : (2*ch)*stride+buckets*4]
		rowMaxPix := pix[(2*ch+1)*stride : (2*ch+1)*stride+buckets*4]

		for b := range buckets {
			off := b * 4
			pMin := rowMinPix[off : off+4 : off+4]
			pMax := rowMaxPix[off : off+4 : off+4]
			if dVal[b] == 0 {
				pMin[0], pMin[1], pMin[2], pMin[3] = 0, 0, 0, 255
				pMax[0], pMax[1], pMax[2], pMax[3] = 0, 0, 0, 255
				continue
			}
			qfLo := dMin[b]*quantMul + bias
			if qfLo < 1.5 {
				qfLo = 1.5
			} else if qfLo > 65535.5 {
				qfLo = 65535.5
			}
			qLo := uint16(qfLo)
			pMin[0] = uint8(qLo >> 8)
			pMin[1] = uint8(qLo)
			pMin[2] = 255
			pMin[3] = 255

			qfHi := dMax[b]*quantMul + bias
			if qfHi < 1.5 {
				qfHi = 1.5
			} else if qfHi > 65535.5 {
				qfHi = 65535.5
			}
			qHi := uint16(qfHi)
			pMax[0] = uint8(qHi >> 8)
			pMax[1] = uint8(qHi)
			pMax[2] = 255
			pMax[3] = 255
		}
	}
}

func buildRingWindowSpansNoClear(capSize, head, count int, total int64, t0Base, dt, tStart, tEnd float64, buckets int, spanSlot0, spanLen []int32) {
	if count == 0 || buckets <= 0 || tEnd <= tStart {
		return
	}
	firstAbs := total - int64(count)
	lastAbs := total - 1
	reqStart := int64(math.Ceil((tStart - t0Base) / dt))
	reqEnd := int64(math.Floor((tEnd - t0Base) / dt))
	startAbs := max(firstAbs, reqStart)
	endAbs := min(lastAbs, reqEnd)
	if startAbs > endAbs {
		return
	}
	oldestSlot := (head - count + capSize) % capSize

	if endAbs-startAbs+1 >= int64(buckets) {
		offStart := (tStart - t0Base) / dt
		stepIdx := ((tEnd - tStart) / float64(buckets)) / dt
		s0 := max(firstAbs, int64(math.Ceil(offStart)))
		for b := range buckets {
			var s1 int64
			if b == buckets-1 {
				s1 = endAbs
			} else {
				s1 = min(lastAbs, int64(math.Ceil(offStart+float64(b+1)*stepIdx))-1)
			}
			if s0 <= s1 {
				spanSlot0[b] = int32((oldestSlot + int(s0-firstAbs)) % capSize)
				spanLen[b] = int32(s1 - s0 + 1)
			}
			s0 = max(firstAbs, s1+1)
		}
		return
	}

	scale := float64(buckets) / (tEnd - tStart)
	for absIdx := startAbs; absIdx <= endAbs; absIdx++ {
		b := int(((t0Base - tStart) + float64(absIdx)*dt) * scale)
		if b < 0 {
			continue
		}
		if b >= buckets {
			b = buckets - 1
		}
		if spanLen[b] == 0 {
			spanSlot0[b] = int32((oldestSlot + int(absIdx-firstAbs)) % capSize)
			spanLen[b] = 1
		} else {
			spanLen[b]++
		}
	}
}

func (m *MultiRingBuffer) scanAllChannelsBySpans(
	buckets int,
	spanSlot0, spanLen []int32,
	dstMin, dstMax []float32,
	dstValid []byte,
	visit func(ch int),
) {
	capCh := m.capPerCh
	nBlk := m.nBlk
	sSlot0 := spanSlot0[:buckets]
	sLen := spanLen[:buckets]
	dMin := dstMin[:buckets]
	dMax := dstMax[:buckets]
	dVal := dstValid[:buckets]

	for ch := range m.channels {
		chOrd := m.ord32[ch*capCh : (ch+1)*capCh]
		chBMin := m.blkMin[ch*nBlk : (ch+1)*nBlk]
		chBMax := m.blkMax[ch*nBlk : (ch+1)*nBlk]

		if m.nonFiniteCnt[ch] == 0 {
			for b := range buckets {
				n := int(sLen[b])
				if n <= 0 {
					dVal[b] = 0
					continue
				}
				slot0 := int(sSlot0[b])
				var mnK, mxK int32
				if slot0+n <= capCh {
					if n < ringBlkSize*2 {
						// 内联高频快路径（如 1kHz x 10s -> 1000 桶，n=10）：零函数调用开销 + 纯 CMOV 无分支聚合
						seg := chOrd[slot0 : slot0+n]
						mn0, mx0 := seg[0], seg[0]
						if n > 1 {
							mn1, mx1 := seg[1], seg[1]
							i := 2
							for ; i+3 < n; i += 4 {
								s := seg[i : i+4 : i+4]
								mn0 = min(mn0, min(s[0], s[2]))
								mx0 = max(mx0, max(s[0], s[2]))
								mn1 = min(mn1, min(s[1], s[3]))
								mx1 = max(mx1, max(s[1], s[3]))
							}
							mn0 = min(mn0, mn1)
							mx0 = max(mx0, mx1)
							for ; i < n; i++ {
								v := seg[i]
								mn0 = min(mn0, v)
								mx0 = max(mx0, v)
							}
						}
						mnK, mxK = mn0, mx0
					} else {
						mnK, mxK = scanWideOrd32Clean(chOrd, chBMin, chBMax, slot0, slot0+n)
					}
				} else {
					mn1, mx1 := scanContigOrd32Clean(chOrd, chBMin, chBMax, slot0, capCh)
					mn2, mx2 := scanContigOrd32Clean(chOrd, chBMin, chBMax, 0, slot0+n-capCh)
					mnK = min(mn1, mn2)
					mxK = max(mx1, mx2)
				}
				bMin := uint32(mnK ^ int32(uint32(mnK>>31)>>1))
				bMax := uint32(mxK ^ int32(uint32(mxK>>31)>>1))
				dMin[b] = math.Float32frombits(bMin)
				dMax[b] = math.Float32frombits(bMax)
				dVal[b] = 1
			}
		} else {
			for b := range buckets {
				n := int(sLen[b])
				if n <= 0 {
					dVal[b] = 0
					continue
				}
				slot0 := int(sSlot0[b])
				var mnK, mxK int32
				var ok bool
				if slot0+n <= capCh {
					mnK, mxK, ok = scanContigOrd32WithNaN(chOrd, chBMin, chBMax, slot0, slot0+n)
				} else {
					mn1, mx1, ok1 := scanContigOrd32WithNaN(chOrd, chBMin, chBMax, slot0, capCh)
					mn2, mx2, ok2 := scanContigOrd32WithNaN(chOrd, chBMin, chBMax, 0, slot0+n-capCh)
					if ok1 && ok2 {
						mnK, mxK, ok = min(mn1, mn2), max(mx1, mx2), true
					} else if ok1 {
						mnK, mxK, ok = mn1, mx1, true
					} else if ok2 {
						mnK, mxK, ok = mn2, mx2, true
					}
				}
				if ok {
					dMin[b] = ord32ToFloat32(mnK)
					dMax[b] = ord32ToFloat32(mxK)
					dVal[b] = 1
				} else {
					dVal[b] = 0
				}
			}
		}
		visit(ch)
	}
}

// scanWideOrd32Clean 利用 B=8 分块极值表扫描跨度 >= 16 的连续区间，减少 87.5% 内存读取。
func scanWideOrd32Clean(chOrd, chBMin, chBMax []int32, slot0, slotEnd int) (mn, mx int32) {
	b0 := (slot0 + ringBlkSize - 1) >> ringBlkShift
	b1 := slotEnd >> ringBlkShift
	mn = chBMin[b0]
	mx = chBMax[b0]
	for blk := b0 + 1; blk < b1; blk++ {
		mn = min(mn, chBMin[blk])
		mx = max(mx, chBMax[blk])
	}
	headEnd := b0 << ringBlkShift
	for s := slot0; s < headEnd; s++ {
		v := chOrd[s]
		mn = min(mn, v)
		mx = max(mx, v)
	}
	for s := b1 << ringBlkShift; s < slotEnd; s++ {
		v := chOrd[s]
		mn = min(mn, v)
		mx = max(mx, v)
	}
	return mn, mx
}

// scanContigOrd32Clean 扫描无 NaN/Inf 的连续保序 int32 区间 [slot0, slotEnd)（环形回绕边界辅助）。
func scanContigOrd32Clean(chOrd, chBMin, chBMax []int32, slot0, slotEnd int) (mn, mx int32) {
	n := slotEnd - slot0
	if n < ringBlkSize*2 {
		seg := chOrd[slot0:slotEnd]
		mn, mx = seg[0], seg[0]
		for i := 1; i < n; i++ {
			v := seg[i]
			mn = min(mn, v)
			mx = max(mx, v)
		}
		return mn, mx
	}
	return scanWideOrd32Clean(chOrd, chBMin, chBMax, slot0, slotEnd)
}

func scanContigOrd32WithNaN(chOrd, chBMin, chBMax []int32, slot0, slotEnd int) (mn, mx int32, ok bool) {
	mn = math.MaxInt32
	mx = ord32Invalid
	n := slotEnd - slot0
	if n < ringBlkSize*2 {
		for _, k := range chOrd[slot0:slotEnd] {
			if k != ord32Invalid {
				mn = min(mn, k)
				mx = max(mx, k)
				ok = true
			}
		}
		return mn, mx, ok
	}

	b0 := (slot0 + ringBlkSize - 1) >> ringBlkShift
	b1 := slotEnd >> ringBlkShift
	for blk := b0; blk < b1; blk++ {
		bmn := chBMin[blk]
		if bmn != ord32Invalid {
			mn = min(mn, bmn)
			mx = max(mx, chBMax[blk])
			ok = true
		}
	}
	headEnd := b0 << ringBlkShift
	for s := slot0; s < headEnd; s++ {
		k := chOrd[s]
		if k != ord32Invalid {
			mn = min(mn, k)
			mx = max(mx, k)
			ok = true
		}
	}
	for s := b1 << ringBlkShift; s < slotEnd; s++ {
		k := chOrd[s]
		if k != ord32Invalid {
			mn = min(mn, k)
			mx = max(mx, k)
			ok = true
		}
	}
	return mn, mx, ok
}

// decimateRingSlice 在已加读锁的环形切片上执行 O(N_visible) 零分配像素桶包络聚合。
func decimateRingSlice(buf []float64, capSize, head, count int, total int64, t0Base, dt, tStart, tEnd float64, buckets int, dst []MinMax) int {
	clear(dst)
	return decimateRingSliceNoClear(buf, capSize, head, count, total, t0Base, dt, tStart, tEnd, buckets, dst)
}

func decimateRingSliceNoClear(buf []float64, capSize, head, count int, total int64, t0Base, dt, tStart, tEnd float64, buckets int, dst []MinMax) int {
	if count == 0 || buckets <= 0 || tEnd <= tStart {
		return 0
	}
	firstAbs := total - int64(count)
	lastAbs := total - 1

	reqStart := int64(math.Ceil((tStart - t0Base) / dt))
	reqEnd := int64(math.Floor((tEnd - t0Base) / dt))
	startAbs := max(firstAbs, reqStart)
	endAbs := min(lastAbs, reqEnd)
	if startAbs > endAbs {
		return 0
	}

	oldestSlot := (head - count + capSize) % capSize

	// 高密度快路径（每像素桶 >= 2 个样本）：消除逐样本时间戳乘法与环形取模分支，直接复用无分支 CMOVQ scanBucketM4 内核
	if endAbs-startAbs+1 >= int64(buckets)*2 {
		offStart := (tStart - t0Base) / dt
		stepIdx := ((tEnd - tStart) / float64(buckets)) / dt
		validCount := 0
		s0 := max(firstAbs, int64(math.Ceil(offStart)))
		for b := range buckets {
			var s1 int64
			if b == buckets-1 {
				s1 = endAbs
			} else {
				s1 = min(lastAbs, int64(math.Ceil(offStart+float64(b+1)*stepIdx))-1)
			}
			if s0 <= s1 {
				n := int(s1 - s0 + 1)
				slot0 := (oldestSlot + int(s0-firstAbs)) % capSize
				if slot0+n <= capSize {
					fIdx, minIdx, maxIdx, _, fVal, minVal, maxVal, lVal, _ := scanBucketM4(buf[slot0:slot0+n], 0)
					if fIdx >= 0 {
						dst[b] = MinMax{
							First:    float32(fVal),
							Min:      float32(minVal),
							Max:      float32(maxVal),
							Last:     float32(lVal),
							MinFirst: minIdx <= maxIdx,
							Valid:    true,
						}
						validCount++
					}
				} else {
					n1 := capSize - slot0
					f1, mn1, mx1, _, fv1, mnv1, mxv1, lv1, _ := scanBucketM4(buf[slot0:capSize], 0)
					f2, mn2, mx2, _, fv2, mnv2, mxv2, lv2, _ := scanBucketM4(buf[:n-n1], n1)
					if f1 >= 0 && f2 >= 0 {
						minIdx, minVal := mn1, mnv1
						if mnv2 < minVal {
							minVal = mnv2
							minIdx = mn2
						}
						maxIdx, maxVal := mx1, mxv1
						if mxv2 > maxVal {
							maxVal = mxv2
							maxIdx = mx2
						}
						dst[b] = MinMax{
							First:    float32(fv1),
							Min:      float32(minVal),
							Max:      float32(maxVal),
							Last:     float32(lv2),
							MinFirst: minIdx <= maxIdx,
							Valid:    true,
						}
						validCount++
					} else if f1 >= 0 {
						dst[b] = MinMax{
							First: float32(fv1), Min: float32(mnv1), Max: float32(mxv1), Last: float32(lv1),
							MinFirst: mn1 <= mx1, Valid: true,
						}
						validCount++
					} else if f2 >= 0 {
						dst[b] = MinMax{
							First: float32(fv2), Min: float32(mnv2), Max: float32(mxv2), Last: float32(lv2),
							MinFirst: mn2 <= mx2, Valid: true,
						}
						validCount++
					}
				}
			}
			s0 = max(firstAbs, s1+1)
		}
		return validCount
	}

	scale := float64(buckets) / (tEnd - tStart)
	validCount := 0
	slot := (oldestSlot + int(startAbs-firstAbs)) % capSize
	for absIdx := startAbs; absIdx <= endAbs; absIdx++ {
		y := buf[slot]
		slot++
		if slot == capSize {
			slot = 0
		}
		if !isFinite(y) {
			continue
		}
		t := (t0Base - tStart) + float64(absIdx)*dt
		b := int(t * scale)
		if b < 0 {
			continue
		}
		if b >= buckets {
			b = buckets - 1
		}
		yf := float32(y)
		cell := &dst[b]
		if !cell.Valid {
			cell.First = yf
			cell.Min = yf
			cell.Max = yf
			cell.Last = yf
			cell.MinFirst = true
			cell.Valid = true
			validCount++
		} else {
			cell.Last = yf
			if yf < cell.Min {
				cell.Min = yf
				cell.MinFirst = false
			}
			if yf > cell.Max {
				cell.Max = yf
				cell.MinFirst = true
			}
		}
	}
	return validCount
}
