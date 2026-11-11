package data

import (
	"math"
	"unsafe"
)

// ViewportTransform 封装从 float64 物理数据空间到窗口 DIP 屏幕空间的相机相对投影参数（零 UI 包依赖）。
//
// 核心机制（DESIGN.md 陷阱 3）：所有平移消基数运算均在 float64 寄存器内完成：
//
//	lx = (tx - XMin) / (XMax - XMin) * Width
//
// 最后一步再窄化为 float32 并加上窗口局部偏移 OriginX/OriginY，确保在 1.77e9 秒级 Unix 时间戳
// 或 2.500000V ± 10μV 高偏置信号放大至微秒/微伏视口时像素定位误差 < 0.01 px。
type ViewportTransform struct {
	// 数据域视口边界；当 LogX / LogY 为 true 时，XMin/XMax 或 YMin/YMax 表示 log10(v) 域边界。
	XMin, XMax float64
	YMin, YMax float64

	// 绘图区在窗口中的左上角 DIP 坐标与宽高（对应 Draw 回调中的 r.X, r.Y, r.W, r.H）。
	OriginX float32
	OriginY float32
	Width   float32
	Height  float32

	InvertX bool
	InvertY bool // false: 数学笛卡尔系（Y 向上增大）；true: 图像矩阵系（Y 向下增大）
	LogX    bool
	LogY    bool
}

const minPositiveLogValue = 1e-300

// DataToScreenX 将单个物理 X 坐标投影至窗口 DIP X 坐标。
func (vt *ViewportTransform) DataToScreenX(x float64) float32 {
	if !isFinite(x) {
		return float32(math.NaN())
	}
	if vt.LogX {
		if x <= 0 {
			return float32(math.NaN())
		}
		x = math.Log10(max(x, minPositiveLogValue))
	}
	span := vt.XMax - vt.XMin
	if span == 0 || vt.Width <= 0 {
		return vt.OriginX
	}
	norm := (x - vt.XMin) / span
	if vt.InvertX {
		norm = 1.0 - norm
	}
	return vt.OriginX + float32(norm*float64(vt.Width))
}

// DataToScreenY 将单个物理 Y 坐标投影至窗口 DIP Y 坐标。
func (vt *ViewportTransform) DataToScreenY(y float64) float32 {
	if !isFinite(y) {
		return float32(math.NaN())
	}
	if vt.LogY {
		if y <= 0 {
			return float32(math.NaN())
		}
		y = math.Log10(max(y, minPositiveLogValue))
	}
	span := vt.YMax - vt.YMin
	if span == 0 || vt.Height <= 0 {
		return vt.OriginY + vt.Height*0.5
	}
	norm := (y - vt.YMin) / span
	if !vt.InvertY {
		norm = 1.0 - norm
	}
	return vt.OriginY + float32(norm*float64(vt.Height))
}

// VisibleLinearXRange 返回用于单调数据源二分裁剪的线性域 [xLo, xHi] 范围。
func (vt *ViewportTransform) VisibleLinearXRange() (xLo, xHi float64) {
	xLo, xHi = min(vt.XMin, vt.XMax), max(vt.XMin, vt.XMax)
	if vt.LogX {
		xLo = math.Pow(10, xLo)
		xHi = math.Pow(10, xHi)
	}
	return xLo, xHi
}

// DecimateMode 定义曲线降采样策略。
type DecimateMode uint8

const (
	// DecimateAuto 根据可见点数与像素宽度自动启用 M4 保峰像素桶降采样（默认推荐）。
	DecimateAuto DecimateMode = iota
	// DecimateNone 禁用降采样，逐点精确投影（仅适合中低点数曲线）。
	DecimateNone
	// DecimateM4 强制启用 First-Min-Max-Last 四特征无损包络像素桶降采样。
	DecimateM4
	// DecimateLTTB 启用 Largest-Triangle-Three-Buckets 感知面积保形降采样。
	DecimateLTTB
)

// ProjectAndDecimate 将任意 XYSource 在视口 vt 下裁剪、降采样并投影到预分配切片 dst（复用底层容量，稳态 0 allocs/op）。
//
// buckets 通常取视口水平像素宽度 int(ceil(vt.Width * scale))（<= 0 时自动按 ceil(vt.Width) 推导）。
func ProjectAndDecimate(src XYSource, vt ViewportTransform, mode DecimateMode, buckets int, dst [][2]float32) [][2]float32 {
	out := dst[:0]
	if src == nil || vt.Width <= 0 || vt.Height <= 0 {
		return out
	}
	if buckets <= 0 {
		buckets = max(int(math.Ceil(float64(vt.Width))), 1)
	}

	// 快路径 1：等间隔采样源 UniformSource（O(1) 下标裁剪 + 无逐点时间乘法桶扫描）
	if uni, ok := src.(UniformSource); ok && !vt.LogX {
		// 若数据源本身是预聚合金字塔，直接调用 O(buckets) 金字塔加速查询
		if pyr, isPyr := src.(*MinMaxPyramid); isPyr && (mode == DecimateAuto || mode == DecimateM4) {
			return pyr.Query(vt, buckets, out)
		}
		if us, isUS := src.(*UniformSlice); isUS && (mode == DecimateAuto || mode == DecimateM4) {
			us.mu.RLock()
			t0, dt, ys, l1 := us.t0, us.dt, us.ys, us.l1
			us.mu.RUnlock()
			if len(l1) > 0 && dt > 0 {
				xLo, xHi := vt.VisibleLinearXRange()
				i0 := max(int(math.Floor((xLo-t0)/dt)), 0)
				i1 := min(int(math.Ceil((xHi-t0)/dt))+1, len(ys))
				if i0 >= i1 {
					return out
				}
				if i1-i0 > PyramidBranch*2*buckets {
					return queryPyramidLevel(t0, dt, len(ys), i0, i1, l1, pyramidShift, vt, buckets, out)
				}
			}
			return projectUniformSlice(t0, dt, ys, vt, mode, buckets, out)
		}
		if ys := uni.YSlice(); ys != nil {
			return projectUniformSlice(uni.T0(), uni.Dt(), ys, vt, mode, buckets, out)
		}
	}

	// 快路径 2：暴露连续内存切片的通用数据源
	if raw, ok := src.(RawXYProvider); ok {
		if xs, ys, rawOK := raw.RawXY(); rawOK {
			return projectRawXY(xs, ys, src.MonotonicX(), vt, mode, buckets, out)
		}
	}

	// 通用接口路径
	return projectGenericXY(src, vt, mode, buckets, out)
}

// DecimateM4Uniform 直接对等间隔连续切片执行 O(1) 窗口裁剪与像素桶 M4 无损包络投影（稳态 0 allocs/op）。
func DecimateM4Uniform(t0, dt float64, ys []float64, vt ViewportTransform, buckets int, dst [][2]float32) [][2]float32 {
	if buckets <= 0 {
		buckets = max(int(math.Ceil(float64(vt.Width))), 1)
	}
	return projectUniformSlice(t0, dt, ys, vt, DecimateM4, buckets, dst[:0])
}

func projectUniformSlice(t0, dt float64, ys []float64, vt ViewportTransform, mode DecimateMode, buckets int, out [][2]float32) [][2]float32 {
	n := len(ys)
	if n == 0 || dt <= 0 {
		return out
	}
	xLo, xHi := vt.VisibleLinearXRange()
	offLo := xLo - t0
	offHi := xHi - t0
	// 左右各保留 1 个越界保护点，保证跨越视口边界的折线段斜率完全准确
	i0 := max(int(math.Floor(offLo/dt)), 0)
	i1 := min(int(math.Ceil(offHi/dt))+1, n)
	if i0 >= i1 {
		return out
	}

	visCount := i1 - i0
	if mode == DecimateNone || (mode != DecimateLTTB && visCount <= buckets*4) {
		return projectDirectUniform(t0, dt, ys, i0, i1, vt, out)
	}
	if mode == DecimateLTTB {
		target := max(buckets*2, 3)
		if visCount <= target {
			return projectDirectUniform(t0, dt, ys, i0, i1, vt, out)
		}
		return decimateLTTBUniform(t0, dt, ys, i0, i1, vt, target, out)
	}

	// M4 超高速分桶扫描：按桶反推精确样本下标区间 [s0, s1)，内层循环零时间乘法、零浮点转整！
	xSpan := xHi - xLo
	if xSpan <= 0 {
		return projectDirectUniform(t0, dt, ys, i0, i1, vt, out)
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

	// 先在 float64 消去大时间戳基数 (t0 - vt.XMin)，再累加 idx*dt，保留皮秒/纳秒级亚 ULP 相对精度
	projPt := func(idx int, yVal float64) [2]float32 {
		relT := float64(idx) * dt
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

	// 若左保护点位于 xLo 左侧，先发射左保护锚点
	cur := i0
	if float64(cur)*dt < offLo {
		yv := ys[cur]
		if isFinite(yv) {
			out = append(out, projPt(cur, yv))
		}
		cur++
	}

	bucketDt := xSpan / float64(buckets)
	invDt := 1.0 / dt
	lastWasNaN := false

	for b := range buckets {
		if cur >= i1 {
			break
		}
		bRightRel := offLo + float64(b+1)*bucketDt
		endIdx := int(math.Floor(bRightRel*invDt)) + 1
		if b == buckets-1 {
			// 最后一个桶覆盖至 xHi 内最后一点
			endIdx = min(int(math.Floor(offHi*invDt))+1, i1)
		}
		if endIdx > i1 {
			endIdx = i1
		}
		if endIdx <= cur {
			continue
		}

		sub := ys[cur:endIdx]
		firstIdx, minIdx, maxIdx, lastIdx, firstVal, minVal, maxVal, lastVal, sawNaN := scanBucketM4(sub, cur)
		cur = endIdx

		if firstIdx < 0 {
			if sawNaN && len(out) > 0 && !lastWasNaN {
				out = append(out, [2]float32{float32(math.NaN()), float32(math.NaN())})
				lastWasNaN = true
			}
			continue
		}
		if sawNaN && len(out) > 0 && !lastWasNaN {
			// 桶内含断线点时插入折线断点标记
			out = append(out, [2]float32{float32(math.NaN()), float32(math.NaN())})
		}
		lastWasNaN = false

		// 按严格原始时间先后顺序发射最多 4 个去重特征点：(first, extremum1, extremum2, last)
		out = append(out, projPt(firstIdx, firstVal))
		idxA, valA := minIdx, minVal
		idxB, valB := maxIdx, maxVal
		if idxA > idxB {
			idxA, valA, idxB, valB = idxB, valB, idxA, valA
		}
		if idxA != firstIdx {
			out = append(out, projPt(idxA, valA))
		}
		if idxB != firstIdx && idxB != idxA {
			out = append(out, projPt(idxB, valB))
		}
		if lastIdx != firstIdx && lastIdx != idxA && lastIdx != idxB {
			out = append(out, projPt(lastIdx, lastVal))
		}
	}

	// 若右保护点位于 xHi 右侧，补齐右边缘出口连接点
	if cur < i1 {
		yv := ys[cur]
		if isFinite(yv) {
			out = append(out, projPt(cur, yv))
		}
	}
	return out
}

const (
	posInfOrd int64 = 0x7FF0000000000000
	negInfOrd int64 = ^int64(0x7FF0000000000000) // -0x7FF0000000000001，对应 -Inf；负号 NaN 比它更小
)

func rawToOrd(b int64) int64 {
	return b ^ int64(uint64(b>>63)>>1)
}

// scanBucketM4 利用 IEEE-754 保序整数双射 + Go int64 无分支 min/max (CMOVQ) 极速扫描像素桶。
//
// 核心原理：
//  1. 直接以 int64 切片视图加载 IEEE-754 位模式至通用寄存器，消除 XMM -> GP 跨寄存器域搬运开销；
//  2. o = b ^ int64(uint64(b>>63)>>1) 对全体有限 float64 严格保序且落于 (negInfOrd, posInfOrd) 内，
//     而全体 NaN 与 ±Inf 必触发 minOrd <= negInfOrd 或 maxOrd >= posInfOrd，零分支捕获异常桶；
//  3. 4 路展开且不在热路径跨分支保留 o0..o3 生命周期，实现零栈溢出（Zero Register Spill）。
func scanBucketM4(sub []float64, baseIdx int) (firstIdx, minIdx, maxIdx, lastIdx int, firstVal, minVal, maxVal, lastVal float64, sawNaN bool) {
	m := len(sub)
	if m == 0 {
		return -1, -1, -1, -1, 0, 0, 0, 0, false
	}

	raw := unsafe.Slice((*int64)(unsafe.Pointer(unsafe.SliceData(sub))), m)
	minOrd := rawToOrd(raw[0])
	maxOrd := minOrd
	minLocal, maxLocal := 0, 0

	if mid := m >> 1; mid > 0 {
		oMid := rawToOrd(raw[mid])
		if oMid < minOrd {
			minOrd = oMid
			minLocal = mid
		}
		if oMid > maxOrd {
			maxOrd = oMid
			maxLocal = mid
		}
	}
	if m > 1 {
		oLast := rawToOrd(raw[m-1])
		if oLast < minOrd {
			minOrd = oLast
			minLocal = m - 1
		}
		if oLast > maxOrd {
			maxOrd = oLast
			maxLocal = m - 1
		}
	}

	if m > 2 {
		rem := raw[1 : m-1]
		k := 1
		for ; len(rem) >= 4; rem, k = rem[4:], k+4 {
			o0 := rawToOrd(rem[0])
			o1 := rawToOrd(rem[1])
			o2 := rawToOrd(rem[2])
			o3 := rawToOrd(rem[3])

			bMin := min(min(o0, o1), min(o2, o3))
			bMax := max(max(o0, o1), max(o2, o3))

			if bMin < minOrd {
				minOrd = bMin
				for j := range 4 {
					if rawToOrd(rem[j]) == bMin {
						minLocal = k + j
						break
					}
				}
			}
			if bMax > maxOrd {
				maxOrd = bMax
				for j := range 4 {
					if rawToOrd(rem[j]) == bMax {
						maxLocal = k + j
						break
					}
				}
			}
		}
		for i, rk := range rem {
			o := rawToOrd(rk)
			if o < minOrd {
				minOrd = o
				minLocal = k + i
			}
			if o > maxOrd {
				maxOrd = o
				maxLocal = k + i
			}
		}
	}

	if minOrd > negInfOrd && maxOrd < posInfOrd {
		return baseIdx, baseIdx + minLocal, baseIdx + maxLocal, baseIdx + m - 1,
			sub[0], sub[minLocal], sub[maxLocal], sub[m-1], false
	}

	return scanBucketM4Fallback(sub, baseIdx)
}

func scanBucketM4Fallback(sub []float64, baseIdx int) (firstIdx, minIdx, maxIdx, lastIdx int, firstVal, minVal, maxVal, lastVal float64, sawNaN bool) {
	firstIdx, minIdx, maxIdx, lastIdx = -1, -1, -1, -1
	for i, v := range sub {
		if v != v || v > math.MaxFloat64 || v < -math.MaxFloat64 {
			sawNaN = true
			continue
		}
		absIdx := baseIdx + i
		if firstIdx < 0 {
			firstIdx, minIdx, maxIdx, lastIdx = absIdx, absIdx, absIdx, absIdx
			firstVal, minVal, maxVal, lastVal = v, v, v, v
		} else {
			lastIdx = absIdx
			lastVal = v
			if v < minVal {
				minVal = v
				minIdx = absIdx
			}
			if v > maxVal {
				maxVal = v
				maxIdx = absIdx
			}
		}
	}
	return firstIdx, minIdx, maxIdx, lastIdx, firstVal, minVal, maxVal, lastVal, sawNaN
}

func projectDirectUniform(t0, dt float64, ys []float64, i0, i1 int, vt ViewportTransform, out [][2]float32) [][2]float32 {
	xSpan := vt.XMax - vt.XMin
	sx := 0.0
	if xSpan != 0 && vt.Width > 0 {
		sx = float64(vt.Width) / xSpan
	}
	offMin := t0 - vt.XMin
	offMax := vt.XMax - t0
	ox := vt.OriginX
	invX := vt.InvertX
	logX := vt.LogX

	projX := func(idx int) float32 {
		if logX {
			return vt.DataToScreenX(t0 + float64(idx)*dt)
		}
		if xSpan == 0 || vt.Width <= 0 {
			return ox
		}
		relT := float64(idx) * dt
		if invX {
			return ox + float32((offMax-relT)*sx)
		}
		return ox + float32((offMin+relT)*sx)
	}

	lastWasNaN := false
	for i := i0; i < i1; i++ {
		y := ys[i]
		if !isFinite(y) || (vt.LogY && y <= 0) {
			if len(out) > 0 && !lastWasNaN {
				out = append(out, [2]float32{float32(math.NaN()), float32(math.NaN())})
				lastWasNaN = true
			}
			continue
		}
		px := projX(i)
		if math.IsNaN(float64(px)) {
			if len(out) > 0 && !lastWasNaN {
				out = append(out, [2]float32{float32(math.NaN()), float32(math.NaN())})
				lastWasNaN = true
			}
			continue
		}
		out = append(out, [2]float32{px, vt.DataToScreenY(y)})
		lastWasNaN = false
	}
	return out
}

func projectRawXY(xs, ys []float64, monotonicX bool, vt ViewportTransform, mode DecimateMode, buckets int, out [][2]float32) [][2]float32 {
	n := min(len(xs), len(ys))
	if n == 0 {
		return out
	}
	i0, i1 := 0, n
	if monotonicX {
		xLo, xHi := vt.VisibleLinearXRange()
		// 二分定位并左右各留 1 个视口衔接锚点
		lo := lowerBoundSlice(xs[:n], xLo)
		hi := upperBoundSlice(xs[:n], xHi)
		i0 = max(lo-1, 0)
		i1 = min(hi+1, n)
		if i0 >= i1 {
			return out
		}
	}

	visCount := i1 - i0
	if !monotonicX || mode == DecimateNone || (mode != DecimateLTTB && visCount <= buckets*4) {
		return projectDirectSlices(xs, ys, i0, i1, vt, out)
	}
	if mode == DecimateLTTB {
		target := max(buckets*2, 3)
		if visCount <= target {
			return projectDirectSlices(xs, ys, i0, i1, vt, out)
		}
		return decimateLTTBSlices(xs, ys, i0, i1, vt, target, out)
	}

	// 单调非均匀 XY 的单遍 M4 像素桶降采样
	xMin := vt.XMin
	xMax := vt.XMax
	xSpan := xMax - xMin
	if xSpan == 0 {
		return projectDirectSlices(xs, ys, i0, i1, vt, out)
	}
	bucketScale := float64(buckets) / xSpan
	logX := vt.LogX
	invX := vt.InvertX

	curBucket := -1
	firstIdx, minIdx, maxIdx, lastIdx := -1, -1, -1, -1
	var minVal, maxVal float64
	lastWasNaN := false

	flushBucket := func() {
		if firstIdx < 0 {
			return
		}
		out = append(out, [2]float32{vt.DataToScreenX(xs[firstIdx]), vt.DataToScreenY(ys[firstIdx])})
		idxA, idxB := minIdx, maxIdx
		if idxA > idxB {
			idxA, idxB = idxB, idxA
		}
		if idxA != firstIdx {
			out = append(out, [2]float32{vt.DataToScreenX(xs[idxA]), vt.DataToScreenY(ys[idxA])})
		}
		if idxB != firstIdx && idxB != idxA {
			out = append(out, [2]float32{vt.DataToScreenX(xs[idxB]), vt.DataToScreenY(ys[idxB])})
		}
		if lastIdx != firstIdx && lastIdx != idxA && lastIdx != idxB {
			out = append(out, [2]float32{vt.DataToScreenX(xs[lastIdx]), vt.DataToScreenY(ys[lastIdx])})
		}
		firstIdx = -1
		lastWasNaN = false
	}

	_ = ys[i1-1]
	for i := i0; i < i1; i++ {
		x, y := xs[i], ys[i]
		if !isFinite(x) || !isFinite(y) || (logX && x <= 0) || (vt.LogY && y <= 0) {
			flushBucket()
			curBucket = -1
			if len(out) > 0 && !lastWasNaN {
				out = append(out, [2]float32{float32(math.NaN()), float32(math.NaN())})
				lastWasNaN = true
			}
			continue
		}
		tx := x
		if logX {
			tx = math.Log10(max(tx, minPositiveLogValue))
		}
		var b int
		if invX {
			b = int(math.Floor((xMax - tx) * bucketScale))
		} else {
			b = int(math.Floor((tx - xMin) * bucketScale))
		}
		// 越界保护锚点独立归桶 -2 / buckets+1
		if b < 0 {
			b = -2
		} else if b >= buckets {
			b = buckets + 1
		}

		if b != curBucket {
			flushBucket()
			curBucket = b
			firstIdx, minIdx, maxIdx, lastIdx = i, i, i, i
			minVal, maxVal = y, y
		} else {
			lastIdx = i
			if y < minVal {
				minVal = y
				minIdx = i
			}
			if y > maxVal {
				maxVal = y
				maxIdx = i
			}
		}
	}
	flushBucket()
	return out
}

func projectDirectSlices(xs, ys []float64, i0, i1 int, vt ViewportTransform, out [][2]float32) [][2]float32 {
	lastWasNaN := false
	_ = ys[i1-1]
	for i := i0; i < i1; i++ {
		x, y := xs[i], ys[i]
		px, py := vt.DataToScreenX(x), vt.DataToScreenY(y)
		if math.IsNaN(float64(px)) || math.IsNaN(float64(py)) {
			if len(out) > 0 && !lastWasNaN {
				out = append(out, [2]float32{float32(math.NaN()), float32(math.NaN())})
				lastWasNaN = true
			}
			continue
		}
		out = append(out, [2]float32{px, py})
		lastWasNaN = false
	}
	return out
}

func projectGenericXY(src XYSource, vt ViewportTransform, mode DecimateMode, buckets int, out [][2]float32) [][2]float32 {
	n := src.Len()
	if n == 0 {
		return out
	}
	i0, i1 := 0, n
	mono := src.MonotonicX()
	if mono {
		xLo, xHi := vt.VisibleLinearXRange()
		i0 = max(LowerBoundX(src, xLo)-1, 0)
		i1 = min(UpperBoundX(src, xHi)+1, n)
		if i0 >= i1 {
			return out
		}
	}

	visCount := i1 - i0
	if !mono || mode == DecimateNone || visCount <= buckets*4 {
		lastWasNaN := false
		for i := i0; i < i1; i++ {
			x, y := src.XY(i)
			px, py := vt.DataToScreenX(x), vt.DataToScreenY(y)
			if math.IsNaN(float64(px)) || math.IsNaN(float64(py)) {
				if len(out) > 0 && !lastWasNaN {
					out = append(out, [2]float32{float32(math.NaN()), float32(math.NaN())})
					lastWasNaN = true
				}
				continue
			}
			out = append(out, [2]float32{px, py})
			lastWasNaN = false
		}
		return out
	}

	// 通用接口 M4 降采样
	xMin, xMax := vt.XMin, vt.XMax
	xSpan := xMax - xMin
	if xSpan == 0 {
		return out
	}
	bucketScale := float64(buckets) / xSpan
	curBucket := -1
	hasFirst := false
	var fx, fy, minX, minY, maxX, maxY, lx, ly float64
	minIdx, maxIdx, firstIdx, lastIdx := -1, -1, -1, -1
	lastWasNaN := false

	flush := func() {
		if !hasFirst {
			return
		}
		out = append(out, [2]float32{vt.DataToScreenX(fx), vt.DataToScreenY(fy)})
		idxA, ax, ay := minIdx, minX, minY
		idxB, bx, by := maxIdx, maxX, maxY
		if idxA > idxB {
			idxA, ax, ay, idxB, bx, by = idxB, bx, by, idxA, ax, ay
		}
		if idxA != firstIdx {
			out = append(out, [2]float32{vt.DataToScreenX(ax), vt.DataToScreenY(ay)})
		}
		if idxB != firstIdx && idxB != idxA {
			out = append(out, [2]float32{vt.DataToScreenX(bx), vt.DataToScreenY(by)})
		}
		if lastIdx != firstIdx && lastIdx != idxA && lastIdx != idxB {
			out = append(out, [2]float32{vt.DataToScreenX(lx), vt.DataToScreenY(ly)})
		}
		hasFirst = false
		lastWasNaN = false
	}

	for i := i0; i < i1; i++ {
		x, y := src.XY(i)
		if !isFinite(x) || !isFinite(y) || (vt.LogX && x <= 0) || (vt.LogY && y <= 0) {
			flush()
			curBucket = -1
			if len(out) > 0 && !lastWasNaN {
				out = append(out, [2]float32{float32(math.NaN()), float32(math.NaN())})
				lastWasNaN = true
			}
			continue
		}
		tx := x
		if vt.LogX {
			tx = math.Log10(max(tx, minPositiveLogValue))
		}
		var b int
		if vt.InvertX {
			b = int(math.Floor((xMax - tx) * bucketScale))
		} else {
			b = int(math.Floor((tx - xMin) * bucketScale))
		}
		if b < 0 {
			b = -2
		} else if b >= buckets {
			b = buckets + 1
		}
		if b != curBucket {
			flush()
			curBucket = b
			hasFirst = true
			firstIdx, minIdx, maxIdx, lastIdx = i, i, i, i
			fx, fy = x, y
			minX, minY = x, y
			maxX, maxY = x, y
			lx, ly = x, y
		} else {
			lastIdx = i
			lx, ly = x, y
			if y < minY {
				minIdx = i
				minX, minY = x, y
			}
			if y > maxY {
				maxIdx = i
				maxX, maxY = x, y
			}
		}
	}
	flush()
	return out
}

// decimateLTTBSlices 在屏幕归一化空间执行零分配 Largest-Triangle-Three-Buckets 感知面积降采样。
func decimateLTTBSlices(xs, ys []float64, i0, i1 int, vt ViewportTransform, threshold int, out [][2]float32) [][2]float32 {
	dataLen := i1 - i0
	if threshold >= dataLen || threshold < 3 {
		return projectDirectSlices(xs, ys, i0, i1, vt, out)
	}

	// 找到首个和最后一个有限有效点
	for i0 < i1 && (!isFinite(xs[i0]) || !isFinite(ys[i0])) {
		i0++
	}
	for i1 > i0 && (!isFinite(xs[i1-1]) || !isFinite(ys[i1-1])) {
		i1--
	}
	dataLen = i1 - i0
	if dataLen <= threshold {
		return projectDirectSlices(xs, ys, i0, i1, vt, out)
	}

	every := float64(dataLen-2) / float64(threshold-2)
	a := i0
	out = append(out, [2]float32{vt.DataToScreenX(xs[a]), vt.DataToScreenY(ys[a])})

	for bucket := range threshold - 2 {
		avgStart := i0 + int(math.Floor(float64(bucket+1)*every)) + 1
		avgEnd := min(i0+int(math.Floor(float64(bucket+2)*every))+1, i1)

		var avgX, avgY float64
		avgCnt := 0
		for j := avgStart; j < avgEnd; j++ {
			if isFinite(xs[j]) && isFinite(ys[j]) {
				avgX += xs[j]
				avgY += ys[j]
				avgCnt++
			}
		}
		if avgCnt > 0 {
			avgX /= float64(avgCnt)
			avgY /= float64(avgCnt)
		} else {
			avgX, avgY = xs[i1-1], ys[i1-1]
		}

		rangeOffs := i0 + int(math.Floor(float64(bucket)*every)) + 1
		rangeTo := min(i0+int(math.Floor(float64(bucket+1)*every))+1, i1-1)

		ax, ay := xs[a], ys[a]
		maxArea := -1.0
		nextA := rangeOffs
		for j := rangeOffs; j < rangeTo; j++ {
			if !isFinite(xs[j]) || !isFinite(ys[j]) {
				continue
			}
			area := math.Abs((ax-avgX)*(ys[j]-ay) - (ax-xs[j])*(avgY-ay))
			if area > maxArea {
				maxArea = area
				nextA = j
			}
		}
		out = append(out, [2]float32{vt.DataToScreenX(xs[nextA]), vt.DataToScreenY(ys[nextA])})
		a = nextA
	}

	out = append(out, [2]float32{vt.DataToScreenX(xs[i1-1]), vt.DataToScreenY(ys[i1-1])})
	return out
}

func decimateLTTBUniform(t0, dt float64, ys []float64, i0, i1 int, vt ViewportTransform, threshold int, out [][2]float32) [][2]float32 {
	for i0 < i1 && !isFinite(ys[i0]) {
		i0++
	}
	for i1 > i0 && !isFinite(ys[i1-1]) {
		i1--
	}
	dataLen := i1 - i0
	if threshold >= dataLen || threshold < 3 {
		return projectDirectUniform(t0, dt, ys, i0, i1, vt, out)
	}

	xSpan := vt.XMax - vt.XMin
	sx := 0.0
	if xSpan != 0 && vt.Width > 0 {
		sx = float64(vt.Width) / xSpan
	}
	offMin := t0 - vt.XMin
	offMax := vt.XMax - t0
	projX := func(idx int) float32 {
		if vt.LogX {
			return vt.DataToScreenX(t0 + float64(idx)*dt)
		}
		relT := float64(idx) * dt
		if vt.InvertX {
			return vt.OriginX + float32((offMax-relT)*sx)
		}
		return vt.OriginX + float32((offMin+relT)*sx)
	}

	every := float64(dataLen-2) / float64(threshold-2)
	a := i0
	out = append(out, [2]float32{projX(a), vt.DataToScreenY(ys[a])})

	for bucket := range threshold - 2 {
		avgStart := i0 + int(math.Floor(float64(bucket+1)*every)) + 1
		avgEnd := min(i0+int(math.Floor(float64(bucket+2)*every))+1, i1)

		var avgIdxSum, avgY float64
		avgCnt := 0
		for j := avgStart; j < avgEnd; j++ {
			if isFinite(ys[j]) {
				avgIdxSum += float64(j)
				avgY += ys[j]
				avgCnt++
			}
		}
		var avgIdx float64
		if avgCnt > 0 {
			avgIdx = avgIdxSum / float64(avgCnt)
			avgY /= float64(avgCnt)
		} else {
			avgIdx = float64(i1 - 1)
			avgY = ys[i1-1]
		}

		rangeOffs := i0 + int(math.Floor(float64(bucket)*every)) + 1
		rangeTo := min(i0+int(math.Floor(float64(bucket+1)*every))+1, i1-1)

		aIdx, ay := float64(a), ys[a]
		maxArea := -1.0
		nextA := rangeOffs
		for j := rangeOffs; j < rangeTo; j++ {
			if !isFinite(ys[j]) {
				continue
			}
			// dt 为常数因子，省去三角形面积中的 dt 乘法，几何最优点完全等价
			area := math.Abs((aIdx-avgIdx)*(ys[j]-ay) - (aIdx-float64(j))*(avgY-ay))
			if area > maxArea {
				maxArea = area
				nextA = j
			}
		}
		out = append(out, [2]float32{projX(nextA), vt.DataToScreenY(ys[nextA])})
		a = nextA
	}

	out = append(out, [2]float32{projX(i1 - 1), vt.DataToScreenY(ys[i1-1])})
	return out
}

func lowerBoundSlice(xs []float64, target float64) int {
	lo, hi := 0, len(xs)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if xs[mid] < target {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func upperBoundSlice(xs []float64, target float64) int {
	lo, hi := 0, len(xs)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if xs[mid] <= target {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}
