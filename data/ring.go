package data

import (
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
