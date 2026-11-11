// Package data 提供零 UI 依赖的科学计算数据源抽象、并发环形缓冲、零分配降采样内核与多级金字塔索引。
package data

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

// Range 表示 float64 闭区间 [Min, Max]。
type Range struct {
	Min, Max float64
}

// Valid 判断区间边界均为有限实数且 Min <= Max。
func (r Range) Valid() bool {
	return !math.IsNaN(r.Min) && !math.IsNaN(r.Max) &&
		!math.IsInf(r.Min, 0) && !math.IsInf(r.Max, 0) &&
		r.Min <= r.Max
}

// Span 返回区间跨度 Max - Min。
func (r Range) Span() float64 {
	return r.Max - r.Min
}

// Contains 判断标量 v 是否落在闭区间 [Min, Max] 内。
func (r Range) Contains(v float64) bool {
	return v >= r.Min && v <= r.Max
}

// Union 合并两个有效区间。
func (r Range) Union(other Range) Range {
	if !r.Valid() {
		return other
	}
	if !other.Valid() {
		return r
	}
	return Range{
		Min: min(r.Min, other.Min),
		Max: max(r.Max, other.Max),
	}
}

// Pad 按比例 frac 对称外扩区间；若原始跨度为 0，则按幅值自适应给足非退化跨度。
func (r Range) Pad(frac float64) Range {
	if !r.Valid() {
		return r
	}
	span := r.Max - r.Min
	if span <= 0 {
		delta := math.Abs(r.Min) * 0.05
		if delta == 0 {
			delta = 1.0
		}
		return Range{Min: r.Min - delta, Max: r.Max + delta}
	}
	pad := span * frac
	return Range{Min: r.Min - pad, Max: r.Max + pad}
}

// MinMax 表示单个水平像素列桶内的四特征包络（First-Min-Max-Last）。
//
// MinFirst 记录该桶内最小值点是否出现在最大值点之前，以便还原精确的时序峰谷先后顺序。
type MinMax struct {
	First    float32
	Min      float32
	Max      float32
	Last     float32
	MinFirst bool
	Valid    bool
}

// XYSource 通用非均匀或均匀一维曲线数据源接口。
type XYSource interface {
	Len() int
	XY(i int) (x, y float64)
	Bounds() (xRange, yRange Range, ok bool)
	// MonotonicX 返回 true 时允许启用 O(log N) 二分裁剪与像素桶极速降采样。
	MonotonicX() bool
	// Version 返回单调递增的数据版本号，供渲染层判断缓存是否失效。
	Version() uint64
}

// RawXYProvider 可选快路径接口：暴露底层只读连续切片，避免海量点循环中的接口动态派发开销。
type RawXYProvider interface {
	RawXY() (xs, ys []float64, ok bool)
}

// UniformSource 面向固定采样率硬件信号（x_i = T0 + i * Dt），节省一半内存并支持 O(1) 下标直算。
type UniformSource interface {
	Len() int
	T0() float64
	Dt() float64
	Y(i int) float64
	YSlice() []float64
	Bounds() (xRange, yRange Range, ok bool)
	Version() uint64
}

// MultiChannelSource 面向 16~256 通道同步采样流（如 EEG / EMG / DAQ 采集卡）。
type MultiChannelSource interface {
	Channels() int
	ChannelName(ch int) string
	SampleRate() float64
	// DecimateWindow 读取指定时间窗口 [tStart, tEnd] 内某通道的像素桶包络，返回有效桶数量。
	DecimateWindow(ch int, tStart, tEnd float64, buckets int, dst []MinMax) (valid int)
	LatestTime() float64
	Version() uint64
}

// SweepCapableSource 可选接口：支持监护仪/示波器经典擦除扫描模式（Sweep / Erase-Bar Mode）。
type SweepCapableSource interface {
	MultiChannelSource
	DecimateSweep(ch int, windowSec, eraseGapSec float64, buckets int, dst []MinMax) (cursorNorm float64, valid int)
}

// XYSlice 基于内存切片的并发安全通用 XYSource 实现。
type XYSlice struct {
	mu         sync.RWMutex
	xs         []float64
	ys         []float64
	xBounds    Range
	yBounds    Range
	boundsOK   bool
	monotonicX bool
	ver        atomic.Uint64
}

// NewXYSlice 从给定的 X、Y 切片创建数据源（会截取两者较短长度并预计算边界与单调性）。
func NewXYSlice(xs, ys []float64) *XYSlice {
	s := &XYSlice{}
	s.SetData(xs, ys)
	return s
}

// SetData 替换底层切片数据并递增版本号。调用方在传入后不应再原地并发修改 xs/ys。
func (s *XYSlice) SetData(xs, ys []float64) {
	n := min(len(xs), len(ys))
	xs = xs[:n]
	ys = ys[:n]
	xb, yb, ok, mono := analyzeXY(xs, ys)

	s.mu.Lock()
	s.xs = xs
	s.ys = ys
	s.xBounds = xb
	s.yBounds = yb
	s.boundsOK = ok
	s.monotonicX = mono
	s.ver.Add(1)
	s.mu.Unlock()
}

// Append 追加新的数据点，增量维护边界、单调性与版本号。
func (s *XYSlice) Append(xs, ys []float64) {
	n := min(len(xs), len(ys))
	if n == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	prevLen := len(s.xs)
	var prevLastX float64
	hasPrevFiniteX := false
	if prevLen > 0 {
		prevLastX = s.xs[prevLen-1]
		hasPrevFiniteX = isFinite(prevLastX)
	}

	s.xs = append(s.xs, xs[:n]...)
	s.ys = append(s.ys, ys[:n]...)

	for i := range n {
		x, y := xs[i], ys[i]
		if !isFinite(x) {
			s.monotonicX = false
			continue
		}
		if hasPrevFiniteX && x < prevLastX {
			s.monotonicX = false
		}
		prevLastX = x
		hasPrevFiniteX = true

		if !isFinite(y) {
			continue
		}
		if !s.boundsOK {
			s.xBounds = Range{Min: x, Max: x}
			s.yBounds = Range{Min: y, Max: y}
			s.boundsOK = true
		} else {
			if x < s.xBounds.Min {
				s.xBounds.Min = x
			}
			if x > s.xBounds.Max {
				s.xBounds.Max = x
			}
			if y < s.yBounds.Min {
				s.yBounds.Min = y
			}
			if y > s.yBounds.Max {
				s.yBounds.Max = y
			}
		}
	}
	s.ver.Add(1)
}

func (s *XYSlice) Len() int {
	s.mu.RLock()
	n := len(s.xs)
	s.mu.RUnlock()
	return n
}

func (s *XYSlice) XY(i int) (x, y float64) {
	s.mu.RLock()
	x, y = s.xs[i], s.ys[i]
	s.mu.RUnlock()
	return x, y
}

func (s *XYSlice) Bounds() (xRange, yRange Range, ok bool) {
	s.mu.RLock()
	xRange, yRange, ok = s.xBounds, s.yBounds, s.boundsOK
	s.mu.RUnlock()
	return
}

func (s *XYSlice) MonotonicX() bool {
	s.mu.RLock()
	m := s.monotonicX
	s.mu.RUnlock()
	return m
}

func (s *XYSlice) Version() uint64 { return s.ver.Load() }

// RawXY 返回当前切片快照，供同线程渲染热路径零拷贝读取。
func (s *XYSlice) RawXY() (xs, ys []float64, ok bool) {
	s.mu.RLock()
	xs, ys = s.xs, s.ys
	s.mu.RUnlock()
	return xs, ys, true
}

// UniformSlice 面向等间隔采样信号的数据源，同时实现 UniformSource 与 XYSource。
type UniformSlice struct {
	mu       sync.RWMutex
	t0       float64
	dt       float64
	ys       []float64
	yBounds  Range
	boundsOK bool
	ver      atomic.Uint64
}

// NewUniformSlice 使用起始时间 t0、采样率 sampleRate (Hz) 与幅值切片创建等间隔数据源。
func NewUniformSlice(t0, sampleRate float64, ys []float64) *UniformSlice {
	dt := 1.0
	if sampleRate > 0 && isFinite(sampleRate) {
		dt = 1.0 / sampleRate
	}
	u := &UniformSlice{}
	u.SetData(t0, dt, ys)
	return u
}

// NewUniformSliceDt 使用起始时间 t0 与采样周期 dt 创建等间隔数据源。
func NewUniformSliceDt(t0, dt float64, ys []float64) *UniformSlice {
	u := &UniformSlice{}
	u.SetData(t0, dt, ys)
	return u
}

// SetData 更新起始时间、步长与采样点，并重新计算幅值包围盒。
func (u *UniformSlice) SetData(t0, dt float64, ys []float64) {
	if dt <= 0 || !isFinite(dt) {
		dt = 1.0
	}
	yb, ok := analyzeY(ys)
	u.mu.Lock()
	u.t0 = t0
	u.dt = dt
	u.ys = ys
	u.yBounds = yb
	u.boundsOK = ok
	u.ver.Add(1)
	u.mu.Unlock()
}

func (u *UniformSlice) Len() int {
	u.mu.RLock()
	n := len(u.ys)
	u.mu.RUnlock()
	return n
}

func (u *UniformSlice) T0() float64 {
	u.mu.RLock()
	v := u.t0
	u.mu.RUnlock()
	return v
}

func (u *UniformSlice) Dt() float64 {
	u.mu.RLock()
	v := u.dt
	u.mu.RUnlock()
	return v
}

func (u *UniformSlice) Y(i int) float64 {
	u.mu.RLock()
	v := u.ys[i]
	u.mu.RUnlock()
	return v
}

func (u *UniformSlice) YSlice() []float64 {
	u.mu.RLock()
	ys := u.ys
	u.mu.RUnlock()
	return ys
}

func (u *UniformSlice) XY(i int) (x, y float64) {
	u.mu.RLock()
	x = u.t0 + float64(i)*u.dt
	y = u.ys[i]
	u.mu.RUnlock()
	return x, y
}

func (u *UniformSlice) Bounds() (xRange, yRange Range, ok bool) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	n := len(u.ys)
	if n == 0 || !u.boundsOK {
		return Range{}, Range{}, false
	}
	xMin := u.t0
	xMax := u.t0 + float64(n-1)*u.dt
	return Range{Min: xMin, Max: xMax}, u.yBounds, true
}

func (u *UniformSlice) MonotonicX() bool { return true }
func (u *UniformSlice) Version() uint64  { return u.ver.Load() }

// FuncSource 基于数学解析函数 y = fn(x) 的按需采样数据源。
type FuncSource struct {
	xMin, xMax float64
	n          int
	dx         float64
	fn         func(float64) float64
	yBounds    Range
	boundsOK   bool
	ver        uint64
}

// NewFuncSource 在 [xMin, xMax] 上均匀生成 samples 个函数采样点视图。
func NewFuncSource(xMin, xMax float64, samples int, fn func(float64) float64) *FuncSource {
	if xMax < xMin {
		xMin, xMax = xMax, xMin
	}
	if samples < 2 {
		samples = 2
	}
	dx := (xMax - xMin) / float64(samples-1)
	f := &FuncSource{
		xMin: xMin,
		xMax: xMax,
		n:    samples,
		dx:   dx,
		fn:   fn,
		ver:  1,
	}
	yMin, yMax := math.Inf(1), math.Inf(-1)
	ok := false
	for i := range samples {
		y := fn(xMin + float64(i)*dx)
		if isFinite(y) {
			yMin = min(yMin, y)
			yMax = max(yMax, y)
			ok = true
		}
	}
	if ok {
		f.yBounds = Range{Min: yMin, Max: yMax}
		f.boundsOK = true
	}
	return f
}

func (f *FuncSource) Len() int { return f.n }

func (f *FuncSource) XY(i int) (x, y float64) {
	x = f.xMin + float64(i)*f.dx
	return x, f.fn(x)
}

func (f *FuncSource) Bounds() (xRange, yRange Range, ok bool) {
	if !f.boundsOK {
		return Range{}, Range{}, false
	}
	return Range{Min: f.xMin, Max: f.xMax}, f.yBounds, true
}

func (f *FuncSource) MonotonicX() bool { return true }
func (f *FuncSource) Version() uint64  { return f.ver }

// LowerBoundX 在单调递增的 XYSource 中二分查找满足 X(i) >= target 的最小下标（范围 [0, Len()]）。
func LowerBoundX(src XYSource, target float64) int {
	if raw, ok := src.(RawXYProvider); ok {
		if xs, _, rawOK := raw.RawXY(); rawOK {
			return sort.Search(len(xs), func(i int) bool { return xs[i] >= target })
		}
	}
	if uni, ok := src.(UniformSource); ok {
		return lowerBoundUniform(uni.T0(), uni.Dt(), uni.Len(), target)
	}
	n := src.Len()
	return sort.Search(n, func(i int) bool {
		x, _ := src.XY(i)
		return x >= target
	})
}

// UpperBoundX 在单调递增的 XYSource 中二分查找满足 X(i) > target 的最小下标（范围 [0, Len()]）。
func UpperBoundX(src XYSource, target float64) int {
	if raw, ok := src.(RawXYProvider); ok {
		if xs, _, rawOK := raw.RawXY(); rawOK {
			return sort.Search(len(xs), func(i int) bool { return xs[i] > target })
		}
	}
	if uni, ok := src.(UniformSource); ok {
		return upperBoundUniform(uni.T0(), uni.Dt(), uni.Len(), target)
	}
	n := src.Len()
	return sort.Search(n, func(i int) bool {
		x, _ := src.XY(i)
		return x > target
	})
}

func lowerBoundUniform(t0, dt float64, n int, target float64) int {
	if n <= 0 {
		return 0
	}
	idx := int(math.Ceil((target - t0) / dt))
	if idx < 0 {
		return 0
	}
	if idx > n {
		return n
	}
	return idx
}

func upperBoundUniform(t0, dt float64, n int, target float64) int {
	if n <= 0 {
		return 0
	}
	idx := int(math.Floor((target-t0)/dt)) + 1
	if idx < 0 {
		return 0
	}
	if idx > n {
		return n
	}
	return idx
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func analyzeXY(xs, ys []float64) (xb, yb Range, ok, monotonic bool) {
	n := min(len(xs), len(ys))
	if n == 0 {
		return Range{}, Range{}, false, true
	}
	monotonic = true
	xMin, xMax := math.Inf(1), math.Inf(-1)
	yMin, yMax := math.Inf(1), math.Inf(-1)
	var prevX float64
	hasPrevX := false

	for i := range n {
		x, y := xs[i], ys[i]
		if !isFinite(x) {
			monotonic = false
			continue
		}
		if hasPrevX && x < prevX {
			monotonic = false
		}
		prevX = x
		hasPrevX = true

		if isFinite(y) {
			xMin = min(xMin, x)
			xMax = max(xMax, x)
			yMin = min(yMin, y)
			yMax = max(yMax, y)
			ok = true
		}
	}
	if !ok {
		return Range{}, Range{}, false, monotonic
	}
	return Range{Min: xMin, Max: xMax}, Range{Min: yMin, Max: yMax}, true, monotonic
}

func analyzeY(ys []float64) (yb Range, ok bool) {
	yMin, yMax := math.Inf(1), math.Inf(-1)
	for _, y := range ys {
		if isFinite(y) {
			yMin = min(yMin, y)
			yMax = max(yMax, y)
			ok = true
		}
	}
	if !ok {
		return Range{}, false
	}
	return Range{Min: yMin, Max: yMax}, true
}
