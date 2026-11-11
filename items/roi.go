package items

import (
	"math"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
)

// RegionOrientation 定义一维区间选择器 LinearRegion 的方向。
type RegionOrientation uint8

const (
	// RegionVertical 垂直带状区间 [X0, X1]，贯穿全图高度（常用于时域波形截取与联动局部放大）。
	RegionVertical RegionOrientation = iota
	// RegionHorizontal 水平带状区间 [Y0, Y1]，贯穿全图宽度（常用于幅值门限带或频带截取）。
	RegionHorizontal
)

type linearDragPart uint8

const (
	linDragNone linearDragPart = iota
	linDragMinEdge
	linDragMaxEdge
	linDragBody
)

// LinearRegionOption 配置 LinearRegion。
type LinearRegionOption func(*LinearRegion)

// WithRegionOrientation 设置区间方向（默认 RegionVertical）。
func WithRegionOrientation(ori RegionOrientation) LinearRegionOption {
	return func(r *LinearRegion) { r.ori = ori }
}

// WithRegionColors 自定义半透明填充色与不透明边界线色。
func WithRegionColors(fill, border ui.Color) LinearRegionOption {
	return func(r *LinearRegion) {
		r.fillColor = fill
		r.borderColor = ui.RGB(border.R, border.G, border.B)
		r.customColor = true
	}
}

// WithRegionLimits 限制区间允许拖拽的物理上下界 [minBound, maxBound]。
func WithRegionLimits(minBound, maxBound float64) LinearRegionOption {
	return func(r *LinearRegion) {
		r.boundMin = min(minBound, maxBound)
		r.boundMax = max(minBound, maxBound)
		r.hasLimits = true
	}
}

// WithRegionMovable 控制区间是否允许鼠标交互拖拽（默认 true）。
func WithRegionMovable(movable bool) LinearRegionOption {
	return func(r *LinearRegion) { r.movable = movable }
}

// WithOnRegionChanged 注册区间边界变更回调（拖拽边沿、整体平移或调用 SetRegion 时触发）。
func WithOnRegionChanged(fn func(minVal, maxVal float64)) LinearRegionOption {
	return func(r *LinearRegion) { r.onChanged = fn }
}

// LinearRegion 是一维可拖拽/可缩放半透明区间选择器（对标 PyQtGraph LinearRegionItem）。
//
// 支持独立拖拽左右（或上下）边界调整跨度，以及按住中心阴影带保持宽度整体平移，
// 可一键与详情子图 ViewBox 建立双向无环联动（BindDetailViewBox）。
type LinearRegion struct {
	v0, v1      float64
	ori         RegionOrientation
	movable     bool
	visible     bool
	z           float32
	customColor bool
	fillColor   ui.Color
	borderColor ui.Color
	hasLimits   bool
	boundMin    float64
	boundMax    float64

	dragPart   linearDragPart
	dragStartV float64
	origV0     float64
	origV1     float64
	syncGuard  bool
	onChanged  func(minVal, maxVal float64)
}

// NewLinearRegion 创建初始覆盖 [v0, v1] 的一维区间选择器。
func NewLinearRegion(v0, v1 float64, opts ...LinearRegionOption) *LinearRegion {
	r := &LinearRegion{
		v0:      min(v0, v1),
		v1:      max(v0, v1),
		ori:     RegionVertical,
		movable: true,
		visible: true,
		z:       -2, // 默认位于背景网格之上、数据曲线之下
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	r.clampToLimits()
	return r
}

func (r *LinearRegion) Bounds() (data.Range, data.Range, bool) {
	return data.Range{}, data.Range{}, false
}

func (r *LinearRegion) Visible() bool       { return r.visible }
func (r *LinearRegion) SetVisible(v bool)   { r.visible = v }
func (r *LinearRegion) ZValue() float32     { return r.z }
func (r *LinearRegion) SetZValue(z float32) { r.z = z }

// Region 返回当前有序区间边界 (minVal, maxVal)。
func (r *LinearRegion) Region() (minVal, maxVal float64) {
	return r.v0, r.v1
}

// SetRegion 更新区间边界并触发回调（内置防重入守卫，杜绝双向联动死循环）。
func (r *LinearRegion) SetRegion(v0, v1 float64) {
	lo, hi := min(v0, v1), max(v0, v1)
	r.v0, r.v1 = lo, hi
	r.clampToLimits()
	if r.v0 == lo && r.v1 == hi {
		// 已对齐限制
	}
	if !r.syncGuard && r.onChanged != nil {
		r.syncGuard = true
		r.onChanged(r.v0, r.v1)
		r.syncGuard = false
	}
}

// SetOnChanged 设置区间变更回调。
func (r *LinearRegion) SetOnChanged(fn func(minVal, maxVal float64)) *LinearRegion {
	r.onChanged = fn
	return r
}

// BindDetailViewBox 将当前区间选择器与下游详情子图 ViewBox 建立双向实时联动：
//   - 拖拽概览图中的 LinearRegion 立即更新详情图的 XRange（或 YRange）；
//   - 在详情图中滚轮缩放或平移也自动反向同步 LinearRegion 位置，且由无环守卫保证绝不递归震荡。
func (r *LinearRegion) BindDetailViewBox(detail *mygograph.ViewBox) *LinearRegion {
	if detail == nil {
		return r
	}
	if r.ori == RegionVertical {
		detail.SetXRange(r.v0, r.v1, 0)
	} else {
		detail.SetYRange(r.v0, r.v1, 0)
	}
	prev := r.onChanged
	r.onChanged = func(lo, hi float64) {
		if r.ori == RegionVertical {
			detail.SetXRange(lo, hi, 0)
		} else {
			detail.SetYRange(lo, hi, 0)
		}
		if prev != nil {
			prev(lo, hi)
		}
	}
	detail.AddChangeListener(func(vb *mygograph.ViewBox) {
		if r.syncGuard {
			return
		}
		r.syncGuard = true
		if r.ori == RegionVertical {
			r.v0, r.v1 = vb.XRange()
		} else {
			r.v0, r.v1 = vb.YRange()
		}
		r.clampToLimits()
		r.syncGuard = false
	})
	return r
}

// IndexRange 返回单调递增数据源 src 落在当前区间 [Min, Max] 内的半开样本索引范围 [i0, i1)。
func (r *LinearRegion) IndexRange(src data.XYSource) (i0, i1 int) {
	if src == nil || src.Len() == 0 {
		return 0, 0
	}
	if src.MonotonicX() && r.ori == RegionVertical {
		i0 = max(data.LowerBoundX(src, r.v0), 0)
		i1 = min(data.UpperBoundX(src, r.v1), src.Len())
		if i0 > i1 {
			return i0, i0
		}
		return i0, i1
	}
	return 0, src.Len()
}

// ExtractXY 提取数据源落在当前区间内的精确 (X, Y) 子序列至复用切片 dstX, dstY（容量充足时 0 allocs/op）。
func (r *LinearRegion) ExtractXY(src data.XYSource, dstX, dstY []float64) (xs, ys []float64) {
	xs = dstX[:0]
	ys = dstY[:0]
	if src == nil || src.Len() == 0 {
		return xs, ys
	}
	if src.MonotonicX() && r.ori == RegionVertical {
		i0, i1 := r.IndexRange(src)
		for i := i0; i < i1; i++ {
			x, y := src.XY(i)
			xs = append(xs, x)
			ys = append(ys, y)
		}
		return xs, ys
	}
	n := src.Len()
	for i := range n {
		x, y := src.XY(i)
		v := x
		if r.ori == RegionHorizontal {
			v = y
		}
		if v >= r.v0 && v <= r.v1 {
			xs = append(xs, x)
			ys = append(ys, y)
		}
	}
	return xs, ys
}

func (r *LinearRegion) clampToLimits() {
	if !r.hasLimits {
		return
	}
	span := r.v1 - r.v0
	maxSpan := r.boundMax - r.boundMin
	if span >= maxSpan {
		r.v0, r.v1 = r.boundMin, r.boundMax
		return
	}
	if r.v0 < r.boundMin {
		r.v0 = r.boundMin
		r.v1 = r.v0 + span
	}
	if r.v1 > r.boundMax {
		r.v1 = r.boundMax
		r.v0 = r.v1 - span
	}
}

const roiEdgeHitDIP float32 = 6.5

func (r *LinearRegion) hitPart(lx, ly, pw, ph float32, vb *mygograph.ViewBox) (linearDragPart, ui.Cursor) {
	vt := vb.Transform(ui.Rect{W: pw, H: ph})
	if r.ori == RegionVertical {
		sx0 := vt.DataToScreenX(r.v0)
		sx1 := vt.DataToScreenX(r.v1)
		d0 := float32(math.Abs(float64(lx - sx0)))
		d1 := float32(math.Abs(float64(lx - sx1)))
		if d0 <= roiEdgeHitDIP && d0 <= d1 {
			return linDragMinEdge, ui.CursorResizeEW
		}
		if d1 <= roiEdgeHitDIP {
			return linDragMaxEdge, ui.CursorResizeEW
		}
		lo, hi := min(sx0, sx1), max(sx0, sx1)
		if lx > lo && lx < hi && ly >= 0 && ly <= ph {
			return linDragBody, ui.CursorMove
		}
		return linDragNone, 0
	}

	sy0 := vt.DataToScreenY(r.v0)
	sy1 := vt.DataToScreenY(r.v1)
	d0 := float32(math.Abs(float64(ly - sy0)))
	d1 := float32(math.Abs(float64(ly - sy1)))
	if d0 <= roiEdgeHitDIP && d0 <= d1 {
		return linDragMinEdge, ui.CursorResizeNS
	}
	if d1 <= roiEdgeHitDIP {
		return linDragMaxEdge, ui.CursorResizeNS
	}
	lo, hi := min(sy0, sy1), max(sy0, sy1)
	if ly > lo && ly < hi && lx >= 0 && lx <= pw {
		return linDragBody, ui.CursorMove
	}
	return linDragNone, 0
}

func (r *LinearRegion) HitTest(lx, ly, pw, ph float32, vb *mygograph.ViewBox) (bool, ui.Cursor) {
	if !r.visible || !r.movable {
		return false, 0
	}
	part, cur := r.hitPart(lx, ly, pw, ph, vb)
	return part != linDragNone, cur
}

func (r *LinearRegion) HandlePointer(ev ui.InputEvent, lx, ly, pw, ph float32, vb *mygograph.ViewBox) bool {
	if !r.visible || !r.movable {
		return false
	}
	switch ev.Kind {
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		part, _ := r.hitPart(lx, ly, pw, ph, vb)
		if part == linDragNone {
			return false
		}
		dx, dy := vb.LocalToData(lx, ly, pw, ph)
		r.dragPart = part
		if r.ori == RegionVertical {
			r.dragStartV = dx
		} else {
			r.dragStartV = dy
		}
		r.origV0, r.origV1 = r.v0, r.v1
		return true

	case ui.InputPointerMove:
		if r.dragPart == linDragNone {
			return false
		}
		cx := min(max(lx, 0), pw)
		cy := min(max(ly, 0), ph)
		dx, dy := vb.LocalToData(cx, cy, pw, ph)
		curV := dx
		if r.ori == RegionHorizontal {
			curV = dy
		}
		switch r.dragPart {
		case linDragMinEdge:
			nv0 := curV
			nv1 := r.origV1
			if r.hasLimits {
				nv0 = min(max(nv0, r.boundMin), r.boundMax)
			}
			r.SetRegion(nv0, nv1)
		case linDragMaxEdge:
			nv0 := r.origV0
			nv1 := curV
			if r.hasLimits {
				nv1 = min(max(nv1, r.boundMin), r.boundMax)
			}
			r.SetRegion(nv0, nv1)
		case linDragBody:
			delta := curV - r.dragStartV
			r.SetRegion(r.origV0+delta, r.origV1+delta)
		}
		return true

	case ui.InputPointerUp:
		if r.dragPart != linDragNone {
			r.dragPart = linDragNone
			return true
		}
	}
	return false
}

func (r *LinearRegion) Draw(ctx *mygograph.RenderContext) {
	if !r.visible {
		return
	}
	pr := ctx.PlotRect
	fillCol := ctx.Theme.ROIFill
	borderCol := ctx.Theme.ROIBorder
	if r.customColor {
		fillCol = r.fillColor
		borderCol = r.borderColor
	}

	if r.ori == RegionVertical {
		sx0 := ctx.Transform.DataToScreenX(r.v0)
		sx1 := ctx.Transform.DataToScreenX(r.v1)
		left := max(min(sx0, sx1), pr.X)
		right := min(max(sx0, sx1), pr.X+pr.W)
		if right > left {
			ctx.Painter.Fill(ui.Rect{X: left, Y: pr.Y, W: right - left, H: pr.H}, fillCol, 0)
		}
		midY := pr.Y + pr.H*0.5
		if sx0 >= pr.X && sx0 <= pr.X+pr.W {
			ctx.Painter.Line(sx0, pr.Y, sx0, pr.Y+pr.H, 1.5, borderCol)
			ctx.Painter.Fill(ui.Rect{X: sx0 - 2.5, Y: midY - 12, W: 5, H: 24}, ctx.Theme.ROIHandle, 2)
		}
		if sx1 >= pr.X && sx1 <= pr.X+pr.W {
			ctx.Painter.Line(sx1, pr.Y, sx1, pr.Y+pr.H, 1.5, borderCol)
			ctx.Painter.Fill(ui.Rect{X: sx1 - 2.5, Y: midY - 12, W: 5, H: 24}, ctx.Theme.ROIHandle, 2)
		}
		return
	}

	sy0 := ctx.Transform.DataToScreenY(r.v0)
	sy1 := ctx.Transform.DataToScreenY(r.v1)
	top := max(min(sy0, sy1), pr.Y)
	bot := min(max(sy0, sy1), pr.Y+pr.H)
	if bot > top {
		ctx.Painter.Fill(ui.Rect{X: pr.X, Y: top, W: pr.W, H: bot - top}, fillCol, 0)
	}
	midX := pr.X + pr.W*0.5
	if sy0 >= pr.Y && sy0 <= pr.Y+pr.H {
		ctx.Painter.Line(pr.X, sy0, pr.X+pr.W, sy0, 1.5, borderCol)
		ctx.Painter.Fill(ui.Rect{X: midX - 12, Y: sy0 - 2.5, W: 24, H: 5}, ctx.Theme.ROIHandle, 2)
	}
	if sy1 >= pr.Y && sy1 <= pr.Y+pr.H {
		ctx.Painter.Line(pr.X, sy1, pr.X+pr.W, sy1, 1.5, borderCol)
		ctx.Painter.Fill(ui.Rect{X: midX - 12, Y: sy1 - 2.5, W: 24, H: 5}, ctx.Theme.ROIHandle, 2)
	}
}

var (
	_ mygograph.Item            = (*LinearRegion)(nil)
	_ mygograph.InteractiveItem = (*LinearRegion)(nil)
)

// ============================================================================
// RectROI —— 二维矩形感兴趣区域（8 控制柄缩放 + 整体平移 + 热力图子矩阵提取）
// ============================================================================

type rectHandle uint8

const (
	rectHandleNone rectHandle = iota
	rectHandleBody
	rectHandleLeft
	rectHandleRight
	rectHandleBottom // Y0 边
	rectHandleTop    // Y1 边
	rectHandleX0Y0
	rectHandleX1Y0
	rectHandleX0Y1
	rectHandleX1Y1
)

// RectROIOption 配置 RectROI。
type RectROIOption func(*RectROI)

// WithOnRectChanged 注册矩形 ROI 范围变更回调。
func WithOnRectChanged(fn func(x0, x1, y0, y1 float64)) RectROIOption {
	return func(r *RectROI) { r.onChanged = fn }
}

// WithRectColors 自定义矩形 ROI 填充色与边框色。
func WithRectColors(fill, border ui.Color) RectROIOption {
	return func(r *RectROI) {
		r.fillColor = fill
		r.borderColor = ui.RGB(border.R, border.G, border.B)
		r.customColor = true
	}
}

// RectROI 是支持四角/四边共 8 个控制柄缩放与中心整体拖拽的二维矩形感兴趣区域。
type RectROI struct {
	x0, x1      float64
	y0, y1      float64
	movable     bool
	visible     bool
	z           float32
	customColor bool
	fillColor   ui.Color
	borderColor ui.Color

	activeHandle rectHandle
	startDX      float64
	startDY      float64
	origX0       float64
	origX1       float64
	origY0       float64
	origY1       float64
	onChanged    func(x0, x1, y0, y1 float64)
}

// NewRectROI 创建覆盖物理矩形 [x0, x1] × [y0, y1] 的二维感兴趣区域。
func NewRectROI(x0, x1, y0, y1 float64, opts ...RectROIOption) *RectROI {
	r := &RectROI{
		x0:      min(x0, x1),
		x1:      max(x0, x1),
		y0:      min(y0, y1),
		y1:      max(y0, y1),
		movable: true,
		visible: true,
		z:       6,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

func (r *RectROI) Bounds() (data.Range, data.Range, bool) {
	return data.Range{}, data.Range{}, false
}

func (r *RectROI) Visible() bool       { return r.visible }
func (r *RectROI) SetVisible(v bool)   { r.visible = v }
func (r *RectROI) ZValue() float32     { return r.z }
func (r *RectROI) SetZValue(z float32) { r.z = z }

// Rect 返回当前规范化的物理矩形范围 (x0, x1, y0, y1)，满足 x0 <= x1 且 y0 <= y1。
func (r *RectROI) Rect() (x0, x1, y0, y1 float64) {
	return r.x0, r.x1, r.y0, r.y1
}

// SetRect 更新矩形物理坐标并触发 OnChanged 回调。
func (r *RectROI) SetRect(x0, x1, y0, y1 float64) {
	r.x0, r.x1 = min(x0, x1), max(x0, x1)
	r.y0, r.y1 = min(y0, y1), max(y0, y1)
	if r.onChanged != nil {
		r.onChanged(r.x0, r.x1, r.y0, r.y1)
	}
}

// ExtractSubGrid 从二维标量场（如 ImageItem / WaterfallItem）中提取落在当前 RectROI 内的子矩阵（行主序，零分配复用 dst）。
func (r *RectROI) ExtractSubGrid(grid ScalarGrid2D, dst []float64) (col0, row0, subCols, subRows int, values []float64) {
	values = dst[:0]
	if grid == nil {
		return 0, 0, 0, 0, values
	}
	cols, rows := grid.GridSize()
	if cols <= 0 || rows <= 0 {
		return 0, 0, 0, 0, values
	}
	gx0, gx1, gy0, gy1 := grid.PhysicalBounds()
	spanX := max(gx1-gx0, 1e-30)
	spanY := max(gy1-gy0, 1e-30)

	c0 := int(math.Floor((r.x0 - gx0) / spanX * float64(cols)))
	c1 := int(math.Ceil((r.x1 - gx0) / spanX * float64(cols)))
	r0 := int(math.Floor((r.y0 - gy0) / spanY * float64(rows)))
	r1 := int(math.Ceil((r.y1 - gy0) / spanY * float64(rows)))

	c0 = min(max(c0, 0), cols)
	c1 = min(max(c1, c0), cols)
	r0 = min(max(r0, 0), rows)
	r1 = min(max(r1, r0), rows)
	subCols = c1 - c0
	subRows = r1 - r0
	if subCols <= 0 || subRows <= 0 {
		return c0, r0, 0, 0, values
	}

	need := subCols * subRows
	if cap(values) < need {
		values = make([]float64, 0, need)
	}
	for rr := r0; rr < r1; rr++ {
		for cc := c0; cc < c1; cc++ {
			v, ok := grid.CellValue(cc, rr)
			if !ok {
				v = math.NaN()
			}
			values = append(values, v)
		}
	}
	return c0, r0, subCols, subRows, values
}

const handleHitRadiusDIP float32 = 7.0

func (r *RectROI) hitHandle(lx, ly, pw, ph float32, vb *mygograph.ViewBox) (rectHandle, ui.Cursor) {
	vt := vb.Transform(ui.Rect{W: pw, H: ph})
	sx0 := vt.DataToScreenX(r.x0)
	sx1 := vt.DataToScreenX(r.x1)
	sy0 := vt.DataToScreenY(r.y0)
	sy1 := vt.DataToScreenY(r.y1)

	near := func(px, py float32) bool {
		return float32(math.Hypot(float64(lx-px), float64(ly-py))) <= handleHitRadiusDIP
	}
	// 根据屏幕空间方向自适应对角缩放光标
	diagCursor := func(px, py, ox, oy float32) ui.Cursor {
		if (px-ox)*(py-oy) >= 0 {
			return ui.CursorResizeNWSE
		}
		return ui.CursorResizeNESW
	}

	// 1. 优先命中四角控制柄
	if near(sx0, sy0) {
		return rectHandleX0Y0, diagCursor(sx0, sy0, sx1, sy1)
	}
	if near(sx1, sy0) {
		return rectHandleX1Y0, diagCursor(sx1, sy0, sx0, sy1)
	}
	if near(sx0, sy1) {
		return rectHandleX0Y1, diagCursor(sx0, sy1, sx1, sy0)
	}
	if near(sx1, sy1) {
		return rectHandleX1Y1, diagCursor(sx1, sy1, sx0, sy0)
	}

	// 2. 四边中点及四条边线命中
	minSX, maxSX := min(sx0, sx1), max(sx0, sx1)
	minSY, maxSY := min(sy0, sy1), max(sy0, sy1)
	if ly >= minSY-handleHitRadiusDIP && ly <= maxSY+handleHitRadiusDIP {
		if float32(math.Abs(float64(lx-sx0))) <= roiEdgeHitDIP {
			return rectHandleLeft, ui.CursorResizeEW
		}
		if float32(math.Abs(float64(lx-sx1))) <= roiEdgeHitDIP {
			return rectHandleRight, ui.CursorResizeEW
		}
	}
	if lx >= minSX-handleHitRadiusDIP && lx <= maxSX+handleHitRadiusDIP {
		if float32(math.Abs(float64(ly-sy0))) <= roiEdgeHitDIP {
			return rectHandleBottom, ui.CursorResizeNS
		}
		if float32(math.Abs(float64(ly-sy1))) <= roiEdgeHitDIP {
			return rectHandleTop, ui.CursorResizeNS
		}
	}

	// 3. 矩形内部整体拖拽平移
	if lx > minSX && lx < maxSX && ly > minSY && ly < maxSY {
		return rectHandleBody, ui.CursorMove
	}
	return rectHandleNone, 0
}

func (r *RectROI) HitTest(lx, ly, pw, ph float32, vb *mygograph.ViewBox) (bool, ui.Cursor) {
	if !r.visible || !r.movable {
		return false, 0
	}
	h, cur := r.hitHandle(lx, ly, pw, ph, vb)
	return h != rectHandleNone, cur
}

func (r *RectROI) HandlePointer(ev ui.InputEvent, lx, ly, pw, ph float32, vb *mygograph.ViewBox) bool {
	if !r.visible || !r.movable {
		return false
	}
	switch ev.Kind {
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		h, _ := r.hitHandle(lx, ly, pw, ph, vb)
		if h == rectHandleNone {
			return false
		}
		r.activeHandle = h
		r.startDX, r.startDY = vb.LocalToData(lx, ly, pw, ph)
		r.origX0, r.origX1 = r.x0, r.x1
		r.origY0, r.origY1 = r.y0, r.y1
		return true

	case ui.InputPointerMove:
		if r.activeHandle == rectHandleNone {
			return false
		}
		cx := min(max(lx, 0), pw)
		cy := min(max(ly, 0), ph)
		dx, dy := vb.LocalToData(cx, cy, pw, ph)
		nx0, nx1, ny0, ny1 := r.origX0, r.origX1, r.origY0, r.origY1
		switch r.activeHandle {
		case rectHandleBody:
			ddx, ddy := dx-r.startDX, dy-r.startDY
			nx0 += ddx
			nx1 += ddx
			ny0 += ddy
			ny1 += ddy
		case rectHandleLeft:
			nx0 = dx
		case rectHandleRight:
			nx1 = dx
		case rectHandleBottom:
			ny0 = dy
		case rectHandleTop:
			ny1 = dy
		case rectHandleX0Y0:
			nx0, ny0 = dx, dy
		case rectHandleX1Y0:
			nx1, ny0 = dx, dy
		case rectHandleX0Y1:
			nx0, ny1 = dx, dy
		case rectHandleX1Y1:
			nx1, ny1 = dx, dy
		}
		r.SetRect(nx0, nx1, ny0, ny1)
		return true

	case ui.InputPointerUp:
		if r.activeHandle != rectHandleNone {
			r.activeHandle = rectHandleNone
			return true
		}
	}
	return false
}

func (r *RectROI) Draw(ctx *mygograph.RenderContext) {
	if !r.visible {
		return
	}
	sx0 := ctx.Transform.DataToScreenX(r.x0)
	sx1 := ctx.Transform.DataToScreenX(r.x1)
	sy0 := ctx.Transform.DataToScreenY(r.y0)
	sy1 := ctx.Transform.DataToScreenY(r.y1)

	left, right := min(sx0, sx1), max(sx0, sx1)
	top, bot := min(sy0, sy1), max(sy0, sy1)
	box := ui.Rect{X: left, Y: top, W: right - left, H: bot - top}

	fillCol := ctx.Theme.ROIFill
	borderCol := ctx.Theme.ROIBorder
	if r.customColor {
		fillCol = r.fillColor
		borderCol = r.borderColor
	}
	if box.W > 0 && box.H > 0 {
		ctx.Painter.Fill(box, fillCol, 0)
	}
	ctx.Painter.Stroke(box, borderCol, 0, 1.5)

	// 绘制 8 个高对比度正方形控制柄（四角 + 四边中点）
	midX := (left + right) * 0.5
	midY := (top + bot) * 0.5
	pts := [8][2]float32{
		{left, top}, {right, top}, {left, bot}, {right, bot},
		{midX, top}, {midX, bot}, {left, midY}, {right, midY},
	}
	for _, pt := range pts {
		hr := ui.Rect{X: pt[0] - 3.5, Y: pt[1] - 3.5, W: 7, H: 7}
		ctx.Painter.Fill(hr, ctx.Theme.ROIHandle, 1.5)
		ctx.Painter.Stroke(hr, ctx.Theme.PlotBackground, 1.5, 1.0)
	}
}

var (
	_ mygograph.Item            = (*RectROI)(nil)
	_ mygograph.InteractiveItem = (*RectROI)(nil)
)

// ============================================================================
// LineROI —— 任意角度剖面采样线段 ROI（双端点拖拽 + 热力图双线性插值横截面提取）
// ============================================================================

type lineROIPart uint8

const (
	lineROINone lineROIPart = iota
	lineROIP1
	lineROIP2
	lineROIBody
)

// LineROIOption 配置 LineROI。
type LineROIOption func(*LineROI)

// WithOnLineROIChanged 注册剖面线段端点变更回调。
func WithOnLineROIChanged(fn func(x1, y1, x2, y2 float64)) LineROIOption {
	return func(l *LineROI) { l.onChanged = fn }
}

// WithLineROIColor 自定义剖面线段颜色。
func WithLineROIColor(col ui.Color) LineROIOption {
	return func(l *LineROI) {
		l.color = ui.RGB(col.R, col.G, col.B)
		l.customColor = true
	}
}

// LineROI 是连接 (X1, Y1) -> (X2, Y2) 的任意倾角剖面分析线段（对标 PyQtGraph LineSegmentROI）。
//
// 支持独立拖拽首尾端点或平移整条剖面线，并通过 SampleProfile 对底层二维标量场执行双线性插值横截面提取。
type LineROI struct {
	x1, y1      float64
	x2, y2      float64
	movable     bool
	visible     bool
	z           float32
	customColor bool
	color       ui.Color

	part      lineROIPart
	startDX   float64
	startDY   float64
	origX1    float64
	origY1    float64
	origX2    float64
	origY2    float64
	onChanged func(x1, y1, x2, y2 float64)
}

// NewLineROI 创建从 (x1, y1) 到 (x2, y2) 的二维剖面提取线段。
func NewLineROI(x1, y1, x2, y2 float64, opts ...LineROIOption) *LineROI {
	l := &LineROI{
		x1:      x1,
		y1:      y1,
		x2:      x2,
		y2:      y2,
		movable: true,
		visible: true,
		z:       7,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(l)
		}
	}
	return l
}

func (l *LineROI) Bounds() (data.Range, data.Range, bool) {
	return data.Range{}, data.Range{}, false
}

func (l *LineROI) Visible() bool       { return l.visible }
func (l *LineROI) SetVisible(v bool)   { l.visible = v }
func (l *LineROI) ZValue() float32     { return l.z }
func (l *LineROI) SetZValue(z float32) { l.z = z }

// Endpoints 返回当前首尾端点物理坐标 (x1, y1, x2, y2)。
func (l *LineROI) Endpoints() (x1, y1, x2, y2 float64) {
	return l.x1, l.y1, l.x2, l.y2
}

// Length 返回剖面线段在物理数据空间中的欧氏长度。
func (l *LineROI) Length() float64 {
	return math.Hypot(l.x2-l.x1, l.y2-l.y1)
}

// SetEndpoints 更新两端点物理坐标并触发回调。
func (l *LineROI) SetEndpoints(x1, y1, x2, y2 float64) {
	l.x1, l.y1, l.x2, l.y2 = x1, y1, x2, y2
	if l.onChanged != nil {
		l.onChanged(x1, y1, x2, y2)
	}
}

// SampleProfile 沿剖面线段 (X1, Y1) -> (X2, Y2) 等距采集 numSamples 个双线性插值标量点，
// 返回距起点 (X1, Y1) 的物理弧长距离 dists 与对应标量值 vals（复用 dstDist/dstVal 时 0 allocs/op）。
func (l *LineROI) SampleProfile(grid ScalarGrid2D, numSamples int, dstDist, dstVal []float64) (dists, vals []float64) {
	dists = dstDist[:0]
	vals = dstVal[:0]
	if grid == nil || numSamples <= 0 {
		return dists, vals
	}
	if cap(dists) < numSamples {
		dists = make([]float64, 0, numSamples)
	}
	if cap(vals) < numSamples {
		vals = make([]float64, 0, numSamples)
	}
	totalLen := l.Length()
	if numSamples == 1 {
		v, ok := grid.SampleBilinear(l.x1, l.y1)
		if !ok {
			v = math.NaN()
		}
		dists = append(dists, 0)
		vals = append(vals, v)
		return dists, vals
	}
	invN := 1.0 / float64(numSamples-1)
	dx := l.x2 - l.x1
	dy := l.y2 - l.y1
	for i := range numSamples {
		t := float64(i) * invN
		px := l.x1 + t*dx
		py := l.y1 + t*dy
		v, ok := grid.SampleBilinear(px, py)
		if !ok {
			v = math.NaN()
		}
		dists = append(dists, t*totalLen)
		vals = append(vals, v)
	}
	return dists, vals
}

func (l *LineROI) hitPart(lx, ly, pw, ph float32, vb *mygograph.ViewBox) (lineROIPart, ui.Cursor) {
	vt := vb.Transform(ui.Rect{W: pw, H: ph})
	sx1, sy1 := vt.DataToScreenX(l.x1), vt.DataToScreenY(l.y1)
	sx2, sy2 := vt.DataToScreenX(l.x2), vt.DataToScreenY(l.y2)

	if float32(math.Hypot(float64(lx-sx1), float64(ly-sy1))) <= handleHitRadiusDIP+1 {
		return lineROIP1, ui.CursorPointer
	}
	if float32(math.Hypot(float64(lx-sx2), float64(ly-sy2))) <= handleHitRadiusDIP+1 {
		return lineROIP2, ui.CursorPointer
	}
	if distPointToSegment(lx, ly, sx1, sy1, sx2, sy2) <= roiEdgeHitDIP {
		return lineROIBody, ui.CursorMove
	}
	return lineROINone, 0
}

func (l *LineROI) HitTest(lx, ly, pw, ph float32, vb *mygograph.ViewBox) (bool, ui.Cursor) {
	if !l.visible || !l.movable {
		return false, 0
	}
	p, cur := l.hitPart(lx, ly, pw, ph, vb)
	return p != lineROINone, cur
}

func (l *LineROI) HandlePointer(ev ui.InputEvent, lx, ly, pw, ph float32, vb *mygograph.ViewBox) bool {
	if !l.visible || !l.movable {
		return false
	}
	switch ev.Kind {
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		p, _ := l.hitPart(lx, ly, pw, ph, vb)
		if p == lineROINone {
			return false
		}
		l.part = p
		l.startDX, l.startDY = vb.LocalToData(lx, ly, pw, ph)
		l.origX1, l.origY1 = l.x1, l.y1
		l.origX2, l.origY2 = l.x2, l.y2
		return true

	case ui.InputPointerMove:
		if l.part == lineROINone {
			return false
		}
		cx := min(max(lx, 0), pw)
		cy := min(max(ly, 0), ph)
		dx, dy := vb.LocalToData(cx, cy, pw, ph)
		switch l.part {
		case lineROIP1:
			l.SetEndpoints(dx, dy, l.origX2, l.origY2)
		case lineROIP2:
			l.SetEndpoints(l.origX1, l.origY1, dx, dy)
		case lineROIBody:
			ddx, ddy := dx-l.startDX, dy-l.startDY
			l.SetEndpoints(l.origX1+ddx, l.origY1+ddy, l.origX2+ddx, l.origY2+ddy)
		}
		return true

	case ui.InputPointerUp:
		if l.part != lineROINone {
			l.part = lineROINone
			return true
		}
	}
	return false
}

func (l *LineROI) Draw(ctx *mygograph.RenderContext) {
	if !l.visible {
		return
	}
	sx1 := ctx.Transform.DataToScreenX(l.x1)
	sy1 := ctx.Transform.DataToScreenY(l.y1)
	sx2 := ctx.Transform.DataToScreenX(l.x2)
	sy2 := ctx.Transform.DataToScreenY(l.y2)

	col := ctx.Theme.ROIBorder
	if l.customColor {
		col = l.color
	}
	ctx.Painter.Line(sx1, sy1, sx2, sy2, 2.0, col)

	// 起点绘制实心圆环控制柄，终点绘制菱形/圆标示剖面正方向
	ctx.Painter.Fill(ui.Rect{X: sx1 - 4.5, Y: sy1 - 4.5, W: 9, H: 9}, ctx.Theme.ROIHandle, 4.5)
	ctx.Painter.Stroke(ui.Rect{X: sx1 - 4.5, Y: sy1 - 4.5, W: 9, H: 9}, ctx.Theme.PlotBackground, 4.5, 1.2)

	ctx.Painter.Fill(ui.Rect{X: sx2 - 4.5, Y: sy2 - 4.5, W: 9, H: 9}, col, 2.0)
	ctx.Painter.Stroke(ui.Rect{X: sx2 - 4.5, Y: sy2 - 4.5, W: 9, H: 9}, ctx.Theme.ROIHandle, 2.0, 1.2)
}

var (
	_ mygograph.Item            = (*LineROI)(nil)
	_ mygograph.InteractiveItem = (*LineROI)(nil)
)
