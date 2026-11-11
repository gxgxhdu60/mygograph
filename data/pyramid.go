package data

import (
	"math"
	"sync"
	"sync/atomic"
)

// PyramidBranch 定义多级预聚合索引树的分支因子 B = 64（每层压缩 64 倍）。
//
// 1 亿样本（L0 = 100,000,000）的层级规模：
//   - L1: 1,562,500 块
//   - L2:    24,414 块
//   - L3:       382 块
//   - L4:         6 块
const (
	PyramidBranch = 64
	pyramidShift  = 6
)

// PyramidNode 保存单个汇总块内的全精度 float64 四特征统计量（保留高直流偏置微弱纹波精度）。
type PyramidNode struct {
	First    float64
	Min      float64
	Max      float64
	Last     float64
	MinFirst bool
	Valid    bool
	HasNaN   bool
}

// MinMaxPyramid 面向千万至十亿级等间隔历史采样波形的多级金字塔预聚合索引。
//
// 同时实现 UniformSource 与 XYSource 接口，并额外支持 O(1) 摊还复杂度的流式增量 Append。
type MinMaxPyramid struct {
	mu       sync.RWMutex
	t0       float64
	dt       float64
	raw      []float64
	levels   [][]PyramidNode // levels[0] 对应 L1 (N/64)，levels[k] 对应 L_{k+1} (N/64^{k+1})
	yBounds  Range
	boundsOK bool
	ver      atomic.Uint64
}

// NewMinMaxPyramid 从原始等间隔采样切片自底向上构建多级金字塔索引（零拷贝引用 ys）。
func NewMinMaxPyramid(t0, sampleRate float64, ys []float64) *MinMaxPyramid {
	dt := 1.0
	if sampleRate > 0 && isFinite(sampleRate) {
		dt = 1.0 / sampleRate
	}
	return NewMinMaxPyramidDt(t0, dt, ys)
}

// NewMinMaxPyramidDt 使用起始时间 t0 与采样周期 dt 构建多级金字塔索引。
func NewMinMaxPyramidDt(t0, dt float64, ys []float64) *MinMaxPyramid {
	if dt <= 0 || !isFinite(dt) {
		dt = 1.0
	}
	p := &MinMaxPyramid{
		t0:  t0,
		dt:  dt,
		raw: ys,
	}
	p.rebuildAll()
	p.ver.Store(1)
	return p
}

// Append 向金字塔尾部流式增量追加新采样块，仅重算受影响的尾部路径节点（O(ΔN + log_64 N)）。
func (p *MinMaxPyramid) Append(samples ...float64) {
	if len(samples) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	oldLen := len(p.raw)
	p.raw = append(p.raw, samples...)
	newLen := len(p.raw)

	// 更新全局 Y 包围盒
	for _, v := range samples {
		if !isFinite(v) {
			continue
		}
		if !p.boundsOK {
			p.yBounds = Range{Min: v, Max: v}
			p.boundsOK = true
		} else {
			if v < p.yBounds.Min {
				p.yBounds.Min = v
			}
			if v > p.yBounds.Max {
				p.yBounds.Max = v
			}
		}
	}

	// 增量更新 L1: 受影响的起始块为 oldLen >> 6
	l1Count := (newLen + PyramidBranch - 1) >> pyramidShift
	if len(p.levels) == 0 && l1Count > 0 {
		p.levels = append(p.levels, make([]PyramidNode, 0, l1Count))
	}
	if l1Count > 0 {
		l1 := p.levels[0]
		if cap(l1) < l1Count {
			grown := make([]PyramidNode, l1Count, max(l1Count*2, 16))
			copy(grown, l1)
			l1 = grown
		} else {
			l1 = l1[:l1Count]
		}
		startBlock := oldLen >> pyramidShift
		for b := startBlock; b < l1Count; b++ {
			s0 := b << pyramidShift
			s1 := min(s0+PyramidBranch, newLen)
			l1[b] = summarizeRawBlock(p.raw[s0:s1])
		}
		p.levels[0] = l1

		// 逐层向上增量传播 L2, L3, ...
		prevStart := startBlock
		for lvl := 1; ; lvl++ {
			prevNodes := p.levels[lvl-1]
			if len(prevNodes) <= PyramidBranch && lvl >= len(p.levels) {
				break
			}
			curCount := (len(prevNodes) + PyramidBranch - 1) >> pyramidShift
			if lvl >= len(p.levels) {
				p.levels = append(p.levels, make([]PyramidNode, curCount))
				prevStart = 0
			} else {
				cur := p.levels[lvl]
				if cap(cur) < curCount {
					grown := make([]PyramidNode, curCount, max(curCount*2, 16))
					copy(grown, cur)
					cur = grown
				} else {
					cur = cur[:curCount]
				}
				p.levels[lvl] = cur
			}
			curStart := prevStart >> pyramidShift
			cur := p.levels[lvl]
			for b := curStart; b < curCount; b++ {
				s0 := b << pyramidShift
				s1 := min(s0+PyramidBranch, len(prevNodes))
				cur[b] = summarizeParentBlock(prevNodes[s0:s1])
			}
			prevStart = curStart
			if curCount <= PyramidBranch && lvl == len(p.levels)-1 {
				break
			}
		}
	}

	p.ver.Add(1)
}

func (p *MinMaxPyramid) rebuildAll() {
	n := len(p.raw)
	p.levels = p.levels[:0]
	p.boundsOK = false
	if n == 0 {
		return
	}

	l1Count := (n + PyramidBranch - 1) >> pyramidShift
	l1 := make([]PyramidNode, l1Count)
	yMin, yMax := math.Inf(1), math.Inf(-1)
	hasY := false

	for b := range l1Count {
		s0 := b << pyramidShift
		s1 := min(s0+PyramidBranch, n)
		node := summarizeRawBlock(p.raw[s0:s1])
		l1[b] = node
		if node.Valid {
			yMin = min(yMin, node.Min)
			yMax = max(yMax, node.Max)
			hasY = true
		}
	}
	p.levels = append(p.levels, l1)
	if hasY {
		p.yBounds = Range{Min: yMin, Max: yMax}
		p.boundsOK = true
	}

	prev := l1
	for len(prev) > PyramidBranch {
		cnt := (len(prev) + PyramidBranch - 1) >> pyramidShift
		cur := make([]PyramidNode, cnt)
		for b := range cnt {
			s0 := b << pyramidShift
			s1 := min(s0+PyramidBranch, len(prev))
			cur[b] = summarizeParentBlock(prev[s0:s1])
		}
		p.levels = append(p.levels, cur)
		prev = cur
	}
}

func summarizeRawBlock(sub []float64) PyramidNode {
	firstIdx, minIdx, maxIdx, _, firstVal, minVal, maxVal, lastVal, sawNaN := scanBucketM4(sub, 0)
	if firstIdx < 0 {
		return PyramidNode{HasNaN: sawNaN}
	}
	return PyramidNode{
		First:    firstVal,
		Min:      minVal,
		Max:      maxVal,
		Last:     lastVal,
		MinFirst: minIdx <= maxIdx,
		Valid:    true,
		HasNaN:   sawNaN,
	}
}

func summarizeParentBlock(children []PyramidNode) PyramidNode {
	var node PyramidNode
	for i := range children {
		ch := &children[i]
		if ch.HasNaN {
			node.HasNaN = true
		}
		if !ch.Valid {
			continue
		}
		if !node.Valid {
			node.First = ch.First
			node.Min = ch.Min
			node.Max = ch.Max
			node.Last = ch.Last
			node.MinFirst = ch.MinFirst
			node.Valid = true
		} else {
			node.Last = ch.Last
			// 若子块同时刷新了更小的 Min 和更大的 Max，则保留该子块自身的 MinFirst；
			// 若仅刷新了单一极值，严格后出现的极值位于右侧。
			newMin := ch.Min < node.Min
			newMax := ch.Max > node.Max
			if newMin && newMax {
				node.Min = ch.Min
				node.Max = ch.Max
				node.MinFirst = ch.MinFirst
			} else if newMin {
				node.Min = ch.Min
				node.MinFirst = false
			} else if newMax {
				node.Max = ch.Max
				node.MinFirst = true
			}
		}
	}
	return node
}

// Levels 返回当前预聚合汇总层数（不含 L0 原始数据层）。
func (p *MinMaxPyramid) Levels() int {
	p.mu.RLock()
	k := len(p.levels)
	p.mu.RUnlock()
	return k
}

// Len 实现 UniformSource / XYSource。
func (p *MinMaxPyramid) Len() int {
	p.mu.RLock()
	n := len(p.raw)
	p.mu.RUnlock()
	return n
}

func (p *MinMaxPyramid) T0() float64 {
	p.mu.RLock()
	v := p.t0
	p.mu.RUnlock()
	return v
}

func (p *MinMaxPyramid) Dt() float64 {
	p.mu.RLock()
	v := p.dt
	p.mu.RUnlock()
	return v
}

func (p *MinMaxPyramid) Y(i int) float64 {
	p.mu.RLock()
	v := p.raw[i]
	p.mu.RUnlock()
	return v
}

func (p *MinMaxPyramid) YSlice() []float64 {
	p.mu.RLock()
	raw := p.raw
	p.mu.RUnlock()
	return raw
}

func (p *MinMaxPyramid) XY(i int) (x, y float64) {
	p.mu.RLock()
	x = p.t0 + float64(i)*p.dt
	y = p.raw[i]
	p.mu.RUnlock()
	return x, y
}

func (p *MinMaxPyramid) Bounds() (xRange, yRange Range, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := len(p.raw)
	if n == 0 || !p.boundsOK {
		return Range{}, Range{}, false
	}
	return Range{
		Min: p.t0,
		Max: p.t0 + float64(n-1)*p.dt,
	}, p.yBounds, true
}

func (p *MinMaxPyramid) MonotonicX() bool { return true }
func (p *MinMaxPyramid) Version() uint64  { return p.ver.Load() }

// Query 自适应选取最优金字塔层级，在任意缩放跨度（10 点至 10 亿点）下以 O(buckets) 复杂度
// 生成与全量折线像素包络严格一致的屏幕折线序列（稳态 0 B/op, 0 allocs/op）。
func (p *MinMaxPyramid) Query(vt ViewportTransform, buckets int, dst [][2]float32) [][2]float32 {
	out := dst[:0]
	if vt.Width <= 0 || vt.Height <= 0 {
		return out
	}
	if buckets <= 0 {
		buckets = max(int(math.Ceil(float64(vt.Width))), 1)
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	n := len(p.raw)
	if n == 0 {
		return out
	}
	if vt.LogX {
		// 对数时间轴直接走二分裁剪原始切片路径
		return projectGenericXY(p, vt, DecimateM4, buckets, out)
	}

	xLo, xHi := vt.VisibleLinearXRange()
	offLo := xLo - p.t0
	offHi := xHi - p.t0
	i0 := max(int(math.Floor(offLo/p.dt)), 0)
	i1 := min(int(math.Ceil(offHi/p.dt))+1, n)
	if i0 >= i1 {
		return out
	}
	visSamples := i1 - i0

	// 当视口内原始样本数不超过每桶 2 个 L1 块（<= 128 * buckets，如 2000px 下 <= 25.6 万点）时，
	// 直接扫描 L0 原始连续切片，耗时 < 0.1 ms 且保留精确亚像素点位置。
	if len(p.levels) == 0 || visSamples <= PyramidBranch*2*buckets {
		return projectUniformSlice(p.t0, p.dt, p.raw, vt, DecimateM4, buckets, out)
	}

	// 自动选取最高层级 lvlIdx（对应步长 stride = 64^(lvlIdx+1)），满足每像素桶至少包含 >= 2 个汇总块
	lvlIdx := 0
	for lvlIdx+1 < len(p.levels) {
		nextShift := uint((lvlIdx + 2) * pyramidShift)
		if (visSamples >> nextShift) < buckets*2 {
			break
		}
		lvlIdx++
	}

	nodes := p.levels[lvlIdx]
	shift := uint((lvlIdx + 1) * pyramidShift)
	return queryPyramidLevel(p.t0, p.dt, n, i0, i1, nodes, shift, vt, buckets, out)
}

func queryPyramidLevel(t0, dt float64, totalSamples, i0, i1 int, nodes []PyramidNode, shift uint, vt ViewportTransform, buckets int, out [][2]float32) [][2]float32 {
	blockSamples := 1 << shift
	blockDt := dt * float64(blockSamples)

	bStart := max(i0>>shift, 0)
	bEnd := min(((i1-1)>>shift)+1, len(nodes))
	if bStart >= bEnd {
		return out
	}

	xLo, xHi := vt.VisibleLinearXRange()
	offLo := xLo - t0
	offHi := xHi - t0
	xSpan := xHi - xLo
	if xSpan <= 0 {
		return out
	}

	w64 := float64(vt.Width)
	h64 := float64(vt.Height)
	ox := vt.OriginX
	oy := vt.OriginY
	sx := w64 / (vt.XMax - vt.XMin)
	offMin := t0 - vt.XMin
	offMax := vt.XMax - t0
	ySpan := vt.YMax - vt.YMin
	if ySpan == 0 {
		ySpan = 1
	}
	sy := h64 / ySpan
	invX := vt.InvertX
	invY := vt.InvertY
	logY := vt.LogY

	projRelXY := func(relT, yVal float64) [2]float32 {
		var px float32
		if invX {
			px = ox + float32((offMax-relT)*sx)
		} else {
			px = ox + float32((offMin+relT)*sx)
		}
		yv := yVal
		if logY {
			if yv <= 0 {
				return [2]float32{float32(math.NaN()), float32(math.NaN())}
			}
			yv = math.Log10(max(yv, minPositiveLogValue))
		}
		var py float32
		if invY {
			py = oy + float32((yv-vt.YMin)*sy)
		} else {
			py = oy + float32((vt.YMax-yv)*sy)
		}
		return [2]float32{px, py}
	}

	bucketSpan := xSpan / float64(buckets)
	curB := bStart
	lastWasNaN := false

	// 左保护汇总块
	if float64(curB+1)*blockDt <= offLo && curB < bEnd {
		nd := &nodes[curB]
		if nd.Valid {
			out = append(out, projRelXY(float64(curB)*blockDt+0.5*blockDt, nd.Last))
		}
		curB++
	}

	invBlockDt := 1.0 / blockDt
	maxRelT := float64(totalSamples-1) * dt
	for col := range buckets {
		if curB >= bEnd {
			break
		}
		colRightRel := offLo + float64(col+1)*bucketSpan
		endB := int(math.Floor(colRightRel*invBlockDt)) + 1
		if col == buckets-1 {
			endB = min(int(math.Floor(offHi*invBlockDt))+1, bEnd)
		}
		if endB > bEnd {
			endB = bEnd
		}
		if endB <= curB {
			continue
		}

		agg := summarizeParentBlock(nodes[curB:endB])
		colLeftRel := float64(curB) * blockDt
		colRightRelActual := min(float64(endB)*blockDt-dt, maxRelT)
		colMidRel := 0.5 * (colLeftRel + colRightRelActual)
		curB = endB

		if !agg.Valid {
			if agg.HasNaN && len(out) > 0 && !lastWasNaN {
				out = append(out, [2]float32{float32(math.NaN()), float32(math.NaN())})
				lastWasNaN = true
			}
			continue
		}
		if agg.HasNaN && len(out) > 0 && !lastWasNaN {
			out = append(out, [2]float32{float32(math.NaN()), float32(math.NaN())})
		}
		lastWasNaN = false

		out = append(out, projRelXY(colLeftRel, agg.First))
		if agg.MinFirst {
			if agg.Min != agg.First {
				out = append(out, projRelXY(colMidRel, agg.Min))
			}
			if agg.Max != agg.Min {
				out = append(out, projRelXY(colMidRel, agg.Max))
			}
		} else {
			if agg.Max != agg.First {
				out = append(out, projRelXY(colMidRel, agg.Max))
			}
			if agg.Min != agg.Max {
				out = append(out, projRelXY(colMidRel, agg.Min))
			}
		}
		if agg.Last != agg.Min && agg.Last != agg.Max || colRightRelActual > colLeftRel {
			out = append(out, projRelXY(colRightRelActual, agg.Last))
		}
	}

	if curB < bEnd {
		nd := &nodes[curB]
		if nd.Valid {
			out = append(out, projRelXY(float64(curB)*blockDt, nd.First))
		}
	}
	return out
}
