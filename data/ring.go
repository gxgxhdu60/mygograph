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
// 内存布局优化：采用通道主序（Channel-Major）连续存储 data[ch*capPerCh + slot]，
// 使单通道在时间窗口内的数千点降采样扫描 100% 顺序命中 CPU L1/L2 缓存行，杜绝跨步缓存失效。
type MultiRingBuffer struct {
	mu         sync.RWMutex
	channels   int
	capPerCh   int
	buf        []float64 // 长度 = channels * capPerCh
	names      []string
	head       int
	count      int
	total      int64
	t0Base     float64
	sampleRate float64
	dt         float64

	ver       atomic.Uint64
	wakeArmed atomic.Bool
	wakeMu    sync.RWMutex
	wakeFn    func()
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
	names := make([]string, channels)
	for i := range channels {
		names[i] = fmt.Sprintf("Ch%02d", i+1)
	}
	return &MultiRingBuffer{
		channels:   channels,
		capPerCh:   samplesPerChannel,
		buf:        make([]float64, channels*samplesPerChannel),
		names:      names,
		sampleRate: sampleRate,
		dt:         1.0 / sampleRate,
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
	m.head = 0
	m.count = 0
	m.total = 0
	m.t0Base = t0
	m.ver.Add(1)
	m.mu.Unlock()
	m.notifyWake()
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
		m.buf[ch*capCh+slot] = frame[ch]
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
	for f := range keep {
		slot := (m.head + f) % capCh
		base := (startF + f) * chs
		for ch := range chs {
			m.buf[ch*capCh+slot] = interleaved[base+ch]
		}
	}
	m.head = (m.head + keep) % capCh
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
			chSlice := m.buf[ch*capCh : (ch+1)*capCh]
			copy(chSlice, blocks[ch][n-capCh:n])
		}
		m.head = 0
		m.count = capCh
		m.total += int64(n)
	} else {
		first := min(n, capCh-m.head)
		rem := n - first
		for ch := range m.channels {
			chSlice := m.buf[ch*capCh : (ch+1)*capCh]
			src := blocks[ch][:n]
			copy(chSlice[m.head:m.head+first], src[:first])
			if rem > 0 {
				copy(chSlice[:rem], src[first:])
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

	// 包含窗口两侧各外扩 1 个采样点确保边缘连线不断裂，这里精确限制在窗口时间桶内
	reqStart := int64(math.Ceil((tStart - t0Base) / dt))
	reqEnd := int64(math.Floor((tEnd - t0Base) / dt))
	startAbs := max(firstAbs, reqStart)
	endAbs := min(lastAbs, reqEnd)
	if startAbs > endAbs {
		return 0
	}

	oldestSlot := (head - count + capSize) % capSize
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
		t := t0Base + float64(absIdx)*dt
		b := int((t - tStart) * scale)
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
