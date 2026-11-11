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
	XMin, XMax       float64
	YMin, YMax       float64
	MinXSpan         float64
	MaxXSpan         float64
	MinYSpan         float64
	MaxYSpan         float64
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

	onChange  func()
	listeners []func(*ViewBox)
	rev       uint64
}

// NewViewBox 创建默认视口 [-1, 1] × [-1, 1]、开启双轴自动量程的 ViewBox。
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

func (vb *ViewBox) setXRangeInternal(minX, maxX, padding float64, manual bool) {
	if !isFinite64(minX) || !isFinite64(maxX) {
		return
	}
	lo, hi := min(minX, maxX), max(minX, maxX)
	if vb.logX {
		lo, hi = sanitizePositiveRange(lo, hi)
		if padding > 0 {
			lLo, lHi := math.Log10(lo), math.Log10(hi)
			d := (lHi - lLo) * padding
			if d == 0 {
				d = 0.5
			}
			lo, hi = math.Pow(10, lLo-d), math.Pow(10, lHi+d)
		}
	} else {
		span := hi - lo
		if span == 0 {
			delta := max(math.Abs(lo)*0.05, 1.0)
			lo -= delta
			hi += delta
		} else if padding > 0 {
			lo -= span * padding
			hi += span * padding
		}
	}
	vb.xMin, vb.xMax = lo, hi
	if manual {
		vb.autoX = false
	}
	vb.clampX()
	vb.bump()
	vb.propagateX()
}

func (vb *ViewBox) setYRangeInternal(minY, maxY, padding float64, manual bool) {
	if !isFinite64(minY) || !isFinite64(maxY) {
		return
	}
	lo, hi := min(minY, maxY), max(minY, maxY)
	if vb.logY {
		lo, hi = sanitizePositiveRange(lo, hi)
		if padding > 0 {
			lLo, lHi := math.Log10(lo), math.Log10(hi)
			d := (lHi - lLo) * padding
			if d == 0 {
				d = 0.5
			}
			lo, hi = math.Pow(10, lLo-d), math.Pow(10, lHi+d)
		}
	} else {
		span := hi - lo
		if span == 0 {
			delta := max(math.Abs(lo)*0.05, 1.0)
			lo -= delta
			hi += delta
		} else if padding > 0 {
			lo -= span * padding
			hi += span * padding
		}
	}
	vb.yMin, vb.yMax = lo, hi
	if manual {
		vb.autoY = false
	}
	vb.clampY()
	vb.bump()
	vb.propagateY()
}

// ApplyAutoBounds 根据当前所有可见图元的联合包围盒自动更新处于 autoX/autoY 状态的坐标轴。
func (vb *ViewBox) ApplyAutoBounds(xBounds, yBounds data.Range, hasX, hasY bool) {
	if vb.autoX && hasX && xBounds.Valid() {
		prevX0, prevX1 := vb.xMin, vb.xMax
		// X 轴（如时间轴）默认零留白贴边铺满，除非区间退化为单点
		xPad := 0.0
		if xBounds.Span() == 0 {
			xPad = vb.autoPadding
		}
		vb.setXRangeInternal(xBounds.Min, xBounds.Max, xPad, false)
		if vb.xMin == prevX0 && vb.xMax == prevX1 {
			// 未发生实际变化
		}
	}
	if vb.autoY && hasY && yBounds.Valid() {
		vb.setYRangeInternal(yBounds.Min, yBounds.Max, vb.autoPadding, false)
	}
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
		if vb.logX {
			lo, hi := sanitizePositiveRange(vb.xMin, vb.xMax)
			lLo, lHi := math.Log10(lo), math.Log10(hi)
			shift := -frac * (lHi - lLo)
			vb.xMin = math.Pow(10, lLo+shift)
			vb.xMax = math.Pow(10, lHi+shift)
		} else {
			shift := -frac * (vb.xMax - vb.xMin)
			vb.xMin += shift
			vb.xMax += shift
		}
		vb.autoX = false
		vb.clampX()
		changedX = true
	}

	if mask&AxisY != 0 && height > 0 && dy != 0 {
		frac := float64(dy) / float64(height)
		if !vb.invertY {
			frac = -frac
		}
		if vb.logY {
			lo, hi := sanitizePositiveRange(vb.yMin, vb.yMax)
			lLo, lHi := math.Log10(lo), math.Log10(hi)
			shift := -frac * (lHi - lLo)
			vb.yMin = math.Pow(10, lLo+shift)
			vb.yMax = math.Pow(10, lHi+shift)
		} else {
			shift := -frac * (vb.yMax - vb.yMin)
			vb.yMin += shift
			vb.yMax += shift
		}
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
		if vb.logX {
			lo, hi := sanitizePositiveRange(vb.xMin, vb.xMax)
			lLo, lHi := math.Log10(lo), math.Log10(hi)
			anchor := lLo + nx*(lHi-lLo)
			newSpan := (lHi - lLo) * scaleX
			vb.xMin = math.Pow(10, anchor-nx*newSpan)
			vb.xMax = math.Pow(10, anchor+(1-nx)*newSpan)
		} else {
			span := vb.xMax - vb.xMin
			anchor := vb.xMin + nx*span
			newSpan := span * scaleX
			vb.xMin = anchor - nx*newSpan
			vb.xMax = anchor + (1.0-nx)*newSpan
		}
		vb.autoX = false
		vb.clampX()
		changedX = true
	}

	if mask&AxisY != 0 && scaleY > 0 && scaleY != 1.0 && height > 0 {
		ny := min(max(float64(anchorLY)/float64(height), 0), 1)
		if !vb.invertY {
			ny = 1.0 - ny
		}
		if vb.logY {
			lo, hi := sanitizePositiveRange(vb.yMin, vb.yMax)
			lLo, lHi := math.Log10(lo), math.Log10(hi)
			anchor := lLo + ny*(lHi-lLo)
			newSpan := (lHi - lLo) * scaleY
			vb.yMin = math.Pow(10, anchor-ny*newSpan)
			vb.yMax = math.Pow(10, anchor+(1-ny)*newSpan)
		} else {
			span := vb.yMax - vb.yMin
			anchor := vb.yMin + ny*span
			newSpan := span * scaleY
			vb.yMin = anchor - ny*newSpan
			vb.yMax = anchor + (1.0-ny)*newSpan
		}
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

func (vb *ViewBox) clampX() {
	vb.xMin, vb.xMax = clampAxisRange(
		vb.xMin, vb.xMax,
		vb.limits.XMin, vb.limits.XMax,
		vb.limits.MinXSpan, vb.limits.MaxXSpan,
		vb.logX,
	)
}

func (vb *ViewBox) clampY() {
	vb.yMin, vb.yMax = clampAxisRange(
		vb.yMin, vb.yMax,
		vb.limits.YMin, vb.limits.YMax,
		vb.limits.MinYSpan, vb.limits.MaxYSpan,
		vb.logY,
	)
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
