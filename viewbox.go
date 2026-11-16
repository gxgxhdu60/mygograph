package mygograph

import (
	"math"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph/data"
)

// MouseMode 定义绘图区左键拖拽主交互模式（对标 PyQtGraph 3ButtonMode / RectMode）。
type MouseMode uint8

const (
	// PanMode 三键平移模式（默认）：左键拖拽直接平移画布，右键拖拽非等比缩放。
	PanMode MouseMode = iota
	// RectMode 橡皮筋矩形框选放大模式：左键拖拽绘制选区框，松开后放大并压入 ZoomStack，Backspace 回退。
	RectMode
)

// AxisMask 指定受操作影响的坐标轴维度掩码。
type AxisMask uint8

const (
	AxisNone AxisMask = 0
	AxisX    AxisMask = 1 << iota
	AxisY
	AxisBoth = AxisX | AxisY
)

// ViewLimits 限定视口可漫游与缩放的物理边界及最小/最大跨度（避免无限缩小溢出或过度放大坍缩）。
// 未设置项使用 math.NaN()（边界）或 0（跨度）表示不限制。
type ViewLimits struct {
	XMin, XMax float64
	YMin, YMax float64
	MinXSpan   float64
	MaxXSpan   float64
	MinYSpan   float64
	MaxYSpan   float64
}

// DefaultViewLimits 返回未绑定硬边界但带安全最小跨度（1e-15）的默认视口限制。
func DefaultViewLimits() ViewLimits {
	return ViewLimits{
		XMin:     math.NaN(),
		XMax:     math.NaN(),
		YMin:     math.NaN(),
		YMax:     math.NaN(),
		MinXSpan: 1e-15,
		MinYSpan: 1e-15,
	}
}

type zoomSnapshot struct {
	xMin, xMax float64
	yMin, yMax float64
	autoX      bool
	autoY      bool
}

const maxZoomHistory = 64

// ViewBox 维护双精度相机视口、坐标变换、边界约束、缩放回退栈及跨子图双向轴联动（Cycle-Safe）。
//
// 所有内部范围（xMin, xMax, yMin, yMax）统一存储物理域数值；当 LogX / LogY 开启时，
// 自动保证正值约束并在生成 ViewportTransform 与执行像素平移/锚点缩放时在对数线性域平滑插值。
type ViewBox struct {
	xMin, xMax float64
	yMin, yMax float64

	autoX       bool
	autoY       bool
	autoPadding float64

	invertX      bool
	invertY      bool
	logX         bool
	logY         bool
	aspectLocked bool
	aspectRatio  float64 // 物理单位宽高比：(ySpan / H) = aspectRatio * (xSpan / W)，<=0 表示 1.0

	mouseMode MouseMode
	limits    ViewLimits

	zoomStack []zoomSnapshot

	linkedX []*ViewBox
	linkedY []*ViewBox
	syncing bool

	boundsFn func() (xr, yr data.Range, ok bool)
	groupBuf []*ViewBox

	onChange  func()
	listeners []func(*ViewBox)
	rev       uint64
}

// NewViewBox 创建默认视口 [0, 1] × [-1, 1]、开启双轴自动量程的 ViewBox。
func NewViewBox() *ViewBox {
	return &ViewBox{
		xMin:        0,
		xMax:        1,
		yMin:        -1,
		yMax:        1,
		autoX:       true,
		autoY:       true,
		autoPadding: 0.05,
		mouseMode:   PanMode,
		limits:      DefaultViewLimits(),
		aspectRatio: 1.0,
	}
}

// Revision 返回视口状态修订号（每次平移、缩放或范围变更递增，用于网格缓存失效判定）。
func (vb *ViewBox) Revision() uint64 { return vb.rev }

// SetOnChange 注册视口变更回调（用于通知宿主 Plot 刷新）。
func (vb *ViewBox) SetOnChange(fn func()) { vb.onChange = fn }

// AddChangeListener 追加外部视口变更观察者（如 LinearRegion 双向联动监听，不覆盖宿主 Plot 的 onChange）。
func (vb *ViewBox) AddChangeListener(fn func(*ViewBox)) {
	if fn != nil {
		vb.listeners = append(vb.listeners, fn)
	}
}

// MouseMode 返回当前左键拖拽模式。
func (vb *ViewBox) MouseMode() MouseMode { return vb.mouseMode }

// SetMouseMode 切换 PanMode / RectMode。
func (vb *ViewBox) SetMouseMode(mode MouseMode) { vb.mouseMode = mode }

// InvertX 返回 X 轴是否水平镜像反转。
func (vb *ViewBox) InvertX() bool { return vb.invertX }

// SetInvertX 设置 X 轴是否反转。
func (vb *ViewBox) SetInvertX(inv bool) {
	if vb.invertX != inv {
		vb.invertX = inv
		vb.bump()
	}
}

// InvertY 返回 Y 轴是否反转（true 为图像矩阵系：原点在左上角，Y 向下递增）。
func (vb *ViewBox) InvertY() bool { return vb.invertY }

// SetInvertY 设置 Y 轴方向。
func (vb *ViewBox) SetInvertY(inv bool) {
	if vb.invertY != inv {
		vb.invertY = inv
		vb.bump()
	}
}

// LogX 返回 X 轴是否处于对数坐标模式。
func (vb *ViewBox) LogX() bool { return vb.logX }

// SetLogX 切换 X 轴对数模式，并在开启时自动修正非正边界。
func (vb *ViewBox) SetLogX(enable bool) {
	if vb.logX == enable {
		return
	}
	vb.logX = enable
	if enable {
		vb.xMin, vb.xMax = sanitizePositiveRange(vb.xMin, vb.xMax)
	}
	vb.bump()
}

// LogY 返回 Y 轴是否处于对数坐标模式。
func (vb *ViewBox) LogY() bool { return vb.logY }

// SetLogY 切换 Y 轴对数模式，并在开启时自动修正非正边界。
func (vb *ViewBox) SetLogY(enable bool) {
	if vb.logY == enable {
		return
	}
	vb.logY = enable
	if enable {
		vb.yMin, vb.yMax = sanitizePositiveRange(vb.yMin, vb.yMax)
	}
	vb.bump()
}

// AspectLocked 返回纵横比是否锁定。
func (vb *ViewBox) AspectLocked() bool { return vb.aspectLocked }

// SetAspectLocked 设置物理纵横比锁定（ratio <= 0 时默认按 1:1 等比例锁定）。
func (vb *ViewBox) SetAspectLocked(locked bool, ratio float64) {
	vb.aspectLocked = locked
	if ratio > 0 && isFinite64(ratio) {
		vb.aspectRatio = ratio
	} else if vb.aspectRatio <= 0 {
		vb.aspectRatio = 1.0
	}
	vb.bump()
}

// Limits 返回当前视口限制参数。
func (vb *ViewBox) Limits() ViewLimits { return vb.limits }

// SetLimits 配置视口漫游边界与最小/最大跨度约束，并立即约束当前视口。
func (vb *ViewBox) SetLimits(lim ViewLimits) {
	if lim.MinXSpan <= 0 {
		lim.MinXSpan = 1e-15
	}
	if lim.MinYSpan <= 0 {
		lim.MinYSpan = 1e-15
	}
	vb.limits = lim
	vb.clampRanges()
	vb.bump()
}

// XRange 返回当前可见物理 X 范围 [xMin, xMax]（保证 xMin <= xMax）。
func (vb *ViewBox) XRange() (xMin, xMax float64) { return vb.xMin, vb.xMax }

// YRange 返回当前可见物理 Y 范围 [yMin, yMax]（保证 yMin <= yMax）。
func (vb *ViewBox) YRange() (yMin, yMax float64) { return vb.yMin, vb.yMax }

// AutoRangeMask 返回当前开启自动量程跟随的轴掩码。
func (vb *ViewBox) AutoRangeMask() AxisMask {
	var m AxisMask
	if vb.autoX {
		m |= AxisX
	}
	if vb.autoY {
		m |= AxisY
	}
	return m
}

// IsManualOverride 返回用户是否已手动接管至少一个轴的量程（供 Plot 渲染左下角 [A] 复位按钮）。
func (vb *ViewBox) IsManualOverride() bool {
	return !vb.autoX || !vb.autoY
}

// IsAutoX 返回水平 X 轴当前是否处于自动跟随最新数据范围状态（用于实时流 Live Follow 指示与一键恢复）。
func (vb *ViewBox) IsAutoX() bool { return vb.autoX }

// IsAutoY 返回垂直 Y 轴当前是否处于自动量程状态。
func (vb *ViewBox) IsAutoY() bool { return vb.autoY }

// EnableAutoRange 重新激活指定轴的自动量程并清空手动缩放历史栈。
func (vb *ViewBox) EnableAutoRange(mask AxisMask) {
	if mask&AxisX != 0 {
		vb.autoX = true
	}
	if mask&AxisY != 0 {
		vb.autoY = true
	}
	if vb.autoX && vb.autoY {
		vb.zoomStack = vb.zoomStack[:0]
	}
	vb.bump()
}

// SetAutoPadding 设置自动量程的边缘留白比例（如 0.05 表示上下左右各留 5% 呼吸空间）。
func (vb *ViewBox) SetAutoPadding(padding float64) {
	if padding >= 0 && padding < 1.0 && isFinite64(padding) {
		vb.autoPadding = padding
	}
}

// SetXRange 手动设定 X 物理范围（padding >= 0 为附加留白比例），并将 X 轴切换为手动模式。
func (vb *ViewBox) SetXRange(minX, maxX, padding float64) {
	vb.setXRangeInternal(minX, maxX, padding, true)
}

// SetYRange 手动设定 Y 物理范围（padding >= 0 为附加留白比例），并将 Y 轴切换为手动模式。
func (vb *ViewBox) SetYRange(minY, maxY, padding float64) {
	vb.setYRangeInternal(minY, maxY, padding, true)
}

// SetRange 同时设定 X 与 Y 物理范围。
func (vb *ViewBox) SetRange(minX, maxX, minY, maxY, padding float64) {
	vb.setXRangeInternal(minX, maxX, padding, true)
	vb.setYRangeInternal(minY, maxY, padding, true)
}

func paddedRange(lo, hi, padding float64, isLog bool) (float64, float64) {
	if isLog {
		lo, hi = sanitizePositiveRange(lo, hi)
		if padding > 0 {
			lLo, lHi := math.Log10(lo), math.Log10(hi)
			d := (lHi - lLo) * padding
			if d == 0 {
				d = 0.5
			}
			return math.Pow(10, lLo-d), math.Pow(10, lHi+d)
		}
		return lo, hi
	}
	span := hi - lo
	if span == 0 {
		delta := max(math.Abs(lo)*0.05, 1.0)
		return lo - delta, hi + delta
	}
	if padding > 0 {
		lo -= span * padding
		hi += span * padding
	}
	return lo, hi
}

func (vb *ViewBox) setXRangeInternal(minX, maxX, padding float64, manual bool) {
	if !isFinite64(minX) || !isFinite64(maxX) {
		return
	}
	lo, hi := paddedRange(min(minX, maxX), max(minX, maxX), padding, vb.logX)
	lo, hi = vb.clampedX(lo, hi)
	auto := vb.autoX && !manual
	if lo == vb.xMin && hi == vb.xMax && auto == vb.autoX {
		return
	}
	vb.xMin, vb.xMax, vb.autoX = lo, hi, auto
	vb.bump()
	vb.propagateX()
}

func (vb *ViewBox) setYRangeInternal(minY, maxY, padding float64, manual bool) {
	if !isFinite64(minY) || !isFinite64(maxY) {
		return
	}
	lo, hi := paddedRange(min(minY, maxY), max(minY, maxY), padding, vb.logY)
	lo, hi = vb.clampedY(lo, hi)
	auto := vb.autoY && !manual
	if lo == vb.yMin && hi == vb.yMax && auto == vb.autoY {
		return
	}
	vb.yMin, vb.yMax, vb.autoY = lo, hi, auto
	vb.bump()
	vb.propagateY()
}

// ApplyAutoBounds 根据当前所有可见图元的联合包围盒自动更新处于 autoX/autoY 状态的坐标轴。
func (vb *ViewBox) ApplyAutoBounds(xBounds, yBounds data.Range, hasX, hasY bool) {
	if vb.autoX && hasX && xBounds.Valid() {
		// X 轴（如时间轴）默认零留白贴边铺满，除非区间退化为单点
		xPad := 0.0
		if xBounds.Span() == 0 {
			xPad = vb.autoPadding
		}
		vb.setXRangeInternal(xBounds.Min, xBounds.Max, xPad, false)
	}
	if vb.autoY && hasY && yBounds.Valid() {
		vb.setYRangeInternal(yBounds.Min, yBounds.Max, vb.autoPadding, false)
	}
}

// applyLinkedAutoBounds 汇总本 ViewBox 及其 X/Y 联动组内全部成员的图元包围盒，
// 以联动组联合范围驱动自动量程（保证 LinkX/LinkY 下各子图视口一致）。
func (vb *ViewBox) applyLinkedAutoBounds() {
	if !vb.autoX && !vb.autoY {
		return
	}
	var selfXr, selfYr data.Range
	selfOk := false
	selfCalled := false
	boundsOf := func(m *ViewBox) (data.Range, data.Range, bool) {
		if m == vb {
			if !selfCalled {
				if vb.boundsFn != nil {
					selfXr, selfYr, selfOk = vb.boundsFn()
				}
				selfCalled = true
			}
			return selfXr, selfYr, selfOk
		}
		if m.boundsFn == nil {
			return data.Range{}, data.Range{}, false
		}
		return m.boundsFn()
	}

	var xUnion, yUnion data.Range
	hasX, hasY := false, false
	if vb.autoX {
		vb.collectLinkGroup(true)
		for _, m := range vb.groupBuf {
			if xr, _, ok := boundsOf(m); ok {
				if !hasX {
					xUnion, hasX = xr, true
				} else {
					xUnion = xUnion.Union(xr)
				}
			}
		}
	}
	if vb.autoY {
		vb.collectLinkGroup(false)
		for _, m := range vb.groupBuf {
			if _, yr, ok := boundsOf(m); ok {
				if !hasY {
					yUnion, hasY = yr, true
				} else {
					yUnion = yUnion.Union(yr)
				}
			}
		}
	}
	vb.ApplyAutoBounds(xUnion, yUnion, hasX, hasY)
}

// collectLinkGroup 收集 vb 在 linkedX（xAxis=true）或 linkedY 邻接关系上的传递闭包（含自身）至 vb.groupBuf（稳态零分配复用）。
func (vb *ViewBox) collectLinkGroup(xAxis bool) {
	vb.groupBuf = append(vb.groupBuf[:0], vb)
	for i := 0; i < len(vb.groupBuf); i++ {
		m := vb.groupBuf[i]
		peers := m.linkedX
		if !xAxis {
			peers = m.linkedY
		}
		for _, peer := range peers {
			if !containsVB(vb.groupBuf, peer) {
				vb.groupBuf = append(vb.groupBuf, peer)
			}
		}
	}
}

func containsVB(list []*ViewBox, target *ViewBox) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

// EnforceAspect 在给定绘图区像素宽高下执行纵横比锁定调整（调整 Y 跨度匹配 X 比例）。
func (vb *ViewBox) EnforceAspect(width, height float32) {
	if !vb.aspectLocked || vb.logX || vb.logY || width <= 0 || height <= 0 {
		return
	}
	ratio := vb.aspectRatio
	if ratio <= 0 {
		ratio = 1.0
	}
	xSpan := vb.xMax - vb.xMin
	wantYSpan := xSpan * (float64(height) / float64(width)) * ratio
	if wantYSpan <= 0 || !isFinite64(wantYSpan) {
		return
	}
	yMid := 0.5 * (vb.yMin + vb.yMax)
	vb.yMin = yMid - 0.5*wantYSpan
	vb.yMax = yMid + 0.5*wantYSpan
	vb.clampY()
}

// Transform 生成面向绘图区窗口矩形 plotRect 的高精度相机相对坐标变换参数。
func (vb *ViewBox) Transform(plotRect ui.Rect) data.ViewportTransform {
	vt := data.ViewportTransform{
		XMin:    vb.xMin,
		XMax:    vb.xMax,
		YMin:    vb.yMin,
		YMax:    vb.yMax,
		OriginX: plotRect.X,
		OriginY: plotRect.Y,
		Width:   plotRect.W,
		Height:  plotRect.H,
		InvertX: vb.invertX,
		InvertY: vb.invertY,
		LogX:    vb.logX,
		LogY:    vb.logY,
	}
	if vb.logX {
		lo, hi := sanitizePositiveRange(vb.xMin, vb.xMax)
		vt.XMin = math.Log10(lo)
		vt.XMax = math.Log10(hi)
	}
	if vb.logY {
		lo, hi := sanitizePositiveRange(vb.yMin, vb.yMax)
		vt.YMin = math.Log10(lo)
		vt.YMax = math.Log10(hi)
	}
	return vt
}

// ScreenToData 将窗口 DIP 坐标 (px, py) 逆向解算为 float64 物理数据坐标。
func (vb *ViewBox) ScreenToData(px, py float32, plotRect ui.Rect) (x, y float64) {
	return vb.LocalToData(px-plotRect.X, py-plotRect.Y, plotRect.W, plotRect.H)
}

// LocalToData 将相对于绘图区左上角的局部坐标 (lx, ly) 精确逆映射为 float64 物理数据坐标。
func (vb *ViewBox) LocalToData(lx, ly, width, height float32) (x, y float64) {
	nx := 0.0
	if width > 0 {
		nx = float64(lx) / float64(width)
	}
	if vb.invertX {
		nx = 1.0 - nx
	}
	if vb.logX {
		lo, hi := sanitizePositiveRange(vb.xMin, vb.xMax)
		lLo, lHi := math.Log10(lo), math.Log10(hi)
		x = math.Pow(10, lLo+nx*(lHi-lLo))
	} else {
		x = vb.xMin + nx*(vb.xMax-vb.xMin)
	}

	ny := 0.0
	if height > 0 {
		ny = float64(ly) / float64(height)
	}
	if !vb.invertY {
		ny = 1.0 - ny
	}
	if vb.logY {
		lo, hi := sanitizePositiveRange(vb.yMin, vb.yMax)
		lLo, lHi := math.Log10(lo), math.Log10(hi)
		y = math.Pow(10, lLo+ny*(lHi-lLo))
	} else {
		y = vb.yMin + ny*(vb.yMax-vb.yMin)
	}
	return x, y
}

// PanByPixels 将视口按屏幕指针位移 (dx, dy) 平移（手抓纸张跟随移动语义），自动接管对应轴的手动模式。
func (vb *ViewBox) PanByPixels(dx, dy, width, height float32, mask AxisMask) {
	changedX, changedY := false, false
	if mask&AxisX != 0 && width > 0 && dx != 0 {
		frac := float64(dx) / float64(width)
		if vb.invertX {
			frac = -frac
		}
		vb.xMin, vb.xMax = panAxis(vb.xMin, vb.xMax, frac, vb.logX)
		vb.autoX = false
		vb.clampX()
		changedX = true
	}

	if mask&AxisY != 0 && height > 0 && dy != 0 {
		frac := float64(dy) / float64(height)
		if !vb.invertY {
			frac = -frac
		}
		vb.yMin, vb.yMax = panAxis(vb.yMin, vb.yMax, frac, vb.logY)
		vb.autoY = false
		vb.clampY()
		changedY = true
	}

	if changedX || changedY {
		vb.bump()
		if changedX {
			vb.propagateX()
		}
		if changedY {
			vb.propagateY()
		}
	}
}

// ZoomAtAnchor 以绘图区局部坐标 (anchorLX, anchorLY) 所指物理点为不动点，按比例因子 (scaleX, scaleY) 缩放跨度。
//
// scale < 1.0 表示放大（跨度变窄），scale > 1.0 表示缩小（跨度变宽），scale == 1.0 表示该轴不变。
func (vb *ViewBox) ZoomAtAnchor(anchorLX, anchorLY, width, height float32, scaleX, scaleY float64, mask AxisMask) {
	if vb.aspectLocked && mask == AxisBoth {
		// 纵横比锁定时保持双轴等比同步缩放
		s := scaleX
		if math.Abs(scaleY-1.0) > math.Abs(scaleX-1.0) {
			s = scaleY
		}
		scaleX, scaleY = s, s
	}

	changedX, changedY := false, false
	if mask&AxisX != 0 && scaleX > 0 && scaleX != 1.0 && width > 0 {
		nx := min(max(float64(anchorLX)/float64(width), 0), 1)
		if vb.invertX {
			nx = 1.0 - nx
		}
		vb.xMin, vb.xMax = zoomAxis(vb.xMin, vb.xMax, nx, scaleX, vb.logX)
		vb.autoX = false
		vb.clampX()
		changedX = true
	}

	if mask&AxisY != 0 && scaleY > 0 && scaleY != 1.0 && height > 0 {
		ny := min(max(float64(anchorLY)/float64(height), 0), 1)
		if !vb.invertY {
			ny = 1.0 - ny
		}
		vb.yMin, vb.yMax = zoomAxis(vb.yMin, vb.yMax, ny, scaleY, vb.logY)
		vb.autoY = false
		vb.clampY()
		changedY = true
	}

	if changedX || changedY {
		vb.bump()
		if changedX {
			vb.propagateX()
		}
		if changedY {
			vb.propagateY()
		}
	}
}

// PushZoom 将当前视口状态压入回退栈（用于 RectMode 框选放大前记录历史）。
func (vb *ViewBox) PushZoom() {
	snap := zoomSnapshot{
		xMin:  vb.xMin,
		xMax:  vb.xMax,
		yMin:  vb.yMin,
		yMax:  vb.yMax,
		autoX: vb.autoX,
		autoY: vb.autoY,
	}
	if len(vb.zoomStack) >= maxZoomHistory {
		copy(vb.zoomStack, vb.zoomStack[1:])
		vb.zoomStack[len(vb.zoomStack)-1] = snap
		return
	}
	vb.zoomStack = append(vb.zoomStack, snap)
}

// PopZoom 弹出并恢复上一次缩放前的视口状态（无历史记录时返回 false）。
func (vb *ViewBox) PopZoom() bool {
	n := len(vb.zoomStack)
	if n == 0 {
		return false
	}
	snap := vb.zoomStack[n-1]
	vb.zoomStack = vb.zoomStack[:n-1]
	vb.xMin, vb.xMax = snap.xMin, snap.xMax
	vb.yMin, vb.yMax = snap.yMin, snap.yMax
	vb.autoX, vb.autoY = snap.autoX, snap.autoY
	vb.clampRanges()
	vb.bump()
	vb.propagateX()
	vb.propagateY()
	return true
}

// ZoomDepth 返回当前缩放历史栈深度。
func (vb *ViewBox) ZoomDepth() int { return len(vb.zoomStack) }

// ZoomToLocalRect 将绘图区内的局部像素选框 (lx0, ly0)-(lx1, ly1) 压栈并放大为新视口。
func (vb *ViewBox) ZoomToLocalRect(lx0, ly0, lx1, ly1, width, height float32) bool {
	if math.Abs(float64(lx1-lx0)) < 4 || math.Abs(float64(ly1-ly0)) < 4 {
		return false
	}
	vb.PushZoom()
	dx0, dy0 := vb.LocalToData(lx0, ly0, width, height)
	dx1, dy1 := vb.LocalToData(lx1, ly1, width, height)
	vb.SetRange(min(dx0, dx1), max(dx0, dx1), min(dy0, dy1), max(dy0, dy1), 0)
	return true
}

// LinkX 建立当前 ViewBox 与目标 ViewBox 的双向无环 X 轴同步关系（幂等，防重入死循环）。
func (vb *ViewBox) LinkX(other *ViewBox) {
	if other == nil || other == vb {
		return
	}
	addUniqueVB(&vb.linkedX, other)
	addUniqueVB(&other.linkedX, vb)
	other.syncXFrom(vb)
}

// LinkY 建立当前 ViewBox 与目标 ViewBox 的双向无环 Y 轴同步关系（幂等，防重入死循环）。
func (vb *ViewBox) LinkY(other *ViewBox) {
	if other == nil || other == vb {
		return
	}
	addUniqueVB(&vb.linkedY, other)
	addUniqueVB(&other.linkedY, vb)
	other.syncYFrom(vb)
}

// UnlinkAll 解除当前 ViewBox 与其它所有子图的 X/Y 联动。
func (vb *ViewBox) UnlinkAll() {
	for _, peer := range vb.linkedX {
		removeVB(&peer.linkedX, vb)
	}
	for _, peer := range vb.linkedY {
		removeVB(&peer.linkedY, vb)
	}
	vb.linkedX = vb.linkedX[:0]
	vb.linkedY = vb.linkedY[:0]
}

func (vb *ViewBox) propagateX() {
	if vb.syncing || len(vb.linkedX) == 0 {
		return
	}
	vb.syncing = true
	for _, peer := range vb.linkedX {
		peer.syncXFrom(vb)
	}
	vb.syncing = false
}

func (vb *ViewBox) syncXFrom(src *ViewBox) {
	if vb.syncing {
		return
	}
	if vb.xMin == src.xMin && vb.xMax == src.xMax && vb.autoX == src.autoX {
		return
	}
	vb.syncing = true
	vb.xMin, vb.xMax = src.xMin, src.xMax
	vb.autoX = src.autoX
	vb.clampX()
	vb.bump()
	for _, peer := range vb.linkedX {
		if peer != src {
			peer.syncXFrom(vb)
		}
	}
	vb.syncing = false
}

func (vb *ViewBox) propagateY() {
	if vb.syncing || len(vb.linkedY) == 0 {
		return
	}
	vb.syncing = true
	for _, peer := range vb.linkedY {
		peer.syncYFrom(vb)
	}
	vb.syncing = false
}

func (vb *ViewBox) syncYFrom(src *ViewBox) {
	if vb.syncing {
		return
	}
	if vb.yMin == src.yMin && vb.yMax == src.yMax && vb.autoY == src.autoY {
		return
	}
	vb.syncing = true
	vb.yMin, vb.yMax = src.yMin, src.yMax
	vb.autoY = src.autoY
	vb.clampY()
	vb.bump()
	for _, peer := range vb.linkedY {
		if peer != src {
			peer.syncYFrom(vb)
		}
	}
	vb.syncing = false
}

func (vb *ViewBox) clampRanges() {
	vb.clampX()
	vb.clampY()
}

func (vb *ViewBox) clampedX(lo, hi float64) (float64, float64) {
	return clampAxisRange(
		lo, hi,
		vb.limits.XMin, vb.limits.XMax,
		vb.limits.MinXSpan, vb.limits.MaxXSpan,
		vb.logX,
	)
}

func (vb *ViewBox) clampedY(lo, hi float64) (float64, float64) {
	return clampAxisRange(
		lo, hi,
		vb.limits.YMin, vb.limits.YMax,
		vb.limits.MinYSpan, vb.limits.MaxYSpan,
		vb.logY,
	)
}

func (vb *ViewBox) clampX() {
	vb.xMin, vb.xMax = vb.clampedX(vb.xMin, vb.xMax)
}

func (vb *ViewBox) clampY() {
	vb.yMin, vb.yMax = vb.clampedY(vb.yMin, vb.yMax)
}

func panAxis(lo, hi, frac float64, isLog bool) (float64, float64) {
	if isLog {
		lo, hi = sanitizePositiveRange(lo, hi)
		lLo, lHi := math.Log10(lo), math.Log10(hi)
		shift := -frac * (lHi - lLo)
		return math.Pow(10, lLo+shift), math.Pow(10, lHi+shift)
	}
	shift := -frac * (hi - lo)
	return lo + shift, hi + shift
}

func zoomAxis(lo, hi, n, scale float64, isLog bool) (float64, float64) {
	if isLog {
		lo, hi = sanitizePositiveRange(lo, hi)
		lLo, lHi := math.Log10(lo), math.Log10(hi)
		anchor := lLo + n*(lHi-lLo)
		newSpan := (lHi - lLo) * scale
		return math.Pow(10, anchor-n*newSpan), math.Pow(10, anchor+(1-n)*newSpan)
	}
	anchor := lo + n*(hi-lo)
	newSpan := (hi - lo) * scale
	return anchor - n*newSpan, anchor + (1.0-n)*newSpan
}

func clampAxisRange(lo, hi, hardMin, hardMax, minSpan, maxSpan float64, isLog bool) (float64, float64) {
	if lo > hi {
		lo, hi = hi, lo
	}
	if isLog {
		lo, hi = sanitizePositiveRange(lo, hi)
	}
	span := hi - lo

	// 1. 约束最大跨度
	if maxSpan > 0 && span > maxSpan {
		mid := 0.5 * (lo + hi)
		lo = mid - 0.5*maxSpan
		hi = mid + 0.5*maxSpan
		span = maxSpan
	}

	// 2. 约束最小跨度（同时保护 IEEE-754 在大数值基数下的最小有效多 ULP 跨度，防止视口坍缩为 0）
	ulpFloor := math.Abs(max(math.Abs(lo), math.Abs(hi))) * 1e-14
	effMinSpan := max(minSpan, ulpFloor, 1e-30)
	if span < effMinSpan {
		mid := 0.5 * (lo + hi)
		lo = mid - 0.5*effMinSpan
		hi = mid + 0.5*effMinSpan
		span = effMinSpan
	}

	// 3. 约束物理硬边界（保持当前跨度平移回界内，若硬边界窄于跨度则贴齐双边）
	hasMin := !math.IsNaN(hardMin) && !math.IsInf(hardMin, 0)
	hasMax := !math.IsNaN(hardMax) && !math.IsInf(hardMax, 0)
	if hasMin && hasMax && hardMax-hardMin <= span {
		return hardMin, hardMax
	}
	if hasMin && lo < hardMin {
		lo = hardMin
		hi = hardMin + span
	}
	if hasMax && hi > hardMax {
		hi = hardMax
		lo = hardMax - span
		if hasMin && lo < hardMin {
			lo = hardMin
		}
	}
	if isLog {
		lo, hi = sanitizePositiveRange(lo, hi)
	}
	return lo, hi
}

func sanitizePositiveRange(lo, hi float64) (float64, float64) {
	if hi <= 0 || !isFinite64(hi) {
		hi = 10.0
	}
	if lo <= 0 || !isFinite64(lo) || lo >= hi {
		lo = max(hi*1e-3, 1e-30)
		if lo >= hi {
			hi = lo * 10.0
		}
	}
	return lo, hi
}

func (vb *ViewBox) bump() {
	vb.rev++
	if vb.onChange != nil {
		vb.onChange()
	}
	for _, fn := range vb.listeners {
		fn(vb)
	}
}

func addUniqueVB(list *[]*ViewBox, target *ViewBox) {
	for _, item := range *list {
		if item == target {
			return
		}
	}
	*list = append(*list, target)
}

func removeVB(list *[]*ViewBox, target *ViewBox) {
	s := *list
	for i, item := range s {
		if item == target {
			copy(s[i:], s[i+1:])
			s[len(s)-1] = nil
			*list = s[:len(s)-1]
			return
		}
	}
}
