package data

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
)

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

// T0 返回首个采样点的物理时间基准（由 Reset(t0) 设定，擦除扫描模式的周期相位以此为原点）。
func (m *MultiRingBuffer) T0() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.t0Base
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

type sweepSeg struct {
	b0, b1 int
	t0, t1 float64
}

// sweepSegments 计算擦除扫描模式下当前周期与上一周期分别映射的桶区间 [b0,b1) 及时间区间 [t0,t1]。
// 调用方须持有读锁；前置条件：m.count > 0、windowSec > 0、buckets > 0。
func (m *MultiRingBuffer) sweepSegments(windowSec, eraseGapSec float64, buckets int) (cursorNorm float64, segs [2]sweepSeg, n int) {
	tNow := m.t0Base + float64(m.total-1)*m.dt
	relNow := tNow - m.t0Base
	cycleIdx := math.Floor(relNow / windowSec)
	cycleStart := m.t0Base + cycleIdx*windowSec
	cursorPhase := relNow - cycleIdx*windowSec
	cursorNorm = min(max(cursorPhase/windowSec, 0), 1)

	newEnd := max(int(math.Floor(cursorNorm*float64(buckets))), 1)
	segs[n] = sweepSeg{
		b0: 0, b1: newEnd,
		t0: cycleStart,
		t1: cycleStart + float64(newEnd)/float64(buckets)*windowSec,
	}
	n++
	gapFrac := max(eraseGapSec/windowSec, 0)
	oldStart := max(int(math.Ceil((cursorNorm+gapFrac)*float64(buckets))), newEnd)
	if oldStart < buckets {
		prevStart := cycleStart - windowSec
		segs[n] = sweepSeg{
			b0: oldStart, b1: buckets,
			t0: prevStart + float64(oldStart)/float64(buckets)*windowSec,
			t1: prevStart + windowSec,
		}
		n++
	}
	return cursorNorm, segs, n
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

	cursorNorm, segs, nSeg := m.sweepSegments(windowSec, eraseGapSec, buckets)
	chBuf := m.buf[ch*m.capPerCh : (ch+1)*m.capPerCh]
	for _, s := range segs[:nSeg] {
		valid += decimateRingSliceNoClear(chBuf, m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, s.t0, s.t1, s.b1-s.b0, out[s.b0:s.b1])
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

	cursorNorm, segs, nSeg := m.sweepSegments(windowSec, eraseGapSec, buckets)
	for i := range nCh {
		ch := chStart + i
		chBuf := m.buf[ch*m.capPerCh : (ch+1)*m.capPerCh]
		out := dst[i*buckets : (i+1)*buckets]
		for _, s := range segs[:nSeg] {
			totalValid += decimateRingSliceNoClear(chBuf, m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, s.t0, s.t1, s.b1-s.b0, out[s.b0:s.b1])
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
		var segs [2]sweepSeg
		var nSeg int
		cursorNorm, segs, nSeg = m.sweepSegments(windowSec, eraseGapSec, buckets)
		for _, s := range segs[:nSeg] {
			buildRingWindowSpansNoClear(m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, s.t0, s.t1, s.b1-s.b0, sSlot0[s.b0:s.b1], sLen[s.b0:s.b1])
		}
	}
	m.scanAllChannelsBySpans(buckets, sSlot0, sLen, dstMin[:buckets], dstMax[:buckets], dstValid[:buckets], visit)
	return cursorNorm
}
