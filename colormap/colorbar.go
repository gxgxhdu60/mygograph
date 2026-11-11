package colormap

import (
	"math"
	"slices"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
)

// HistogramBins 定义直方图调窗控件的固定分桶数。
const HistogramBins = 256

// ColorControl 是连接热力图/瀑布图（ImageItem / WaterfallItem）与色棒/直方图调窗器（ColorBar / HistogramLUT）
// 的共享响应式控制器。调整窗宽窗位 [vMin, vMax] 或切换 Colormap 时仅修改标量参数，触发 0ms CPU 开销的 GPU 着色重绘。
type ColorControl struct {
	mu          sync.RWMutex
	cm          *Colormap
	vMin        float64
	vMax        float64
	dataMin     float64
	dataMax     float64
	autoLevels  bool
	unit        string
	label       string
	bins        [HistogramBins]float64 // 归一化至 [0, 1] 的样本频数分布
	hasHist     bool
	version     uint64
	subscribers []func()
}

// NewColorControl 创建绑定指定初始色表与动态范围的色彩控制器（传 nil 默认 Viridis）。
func NewColorControl(cm *Colormap, vMin, vMax float64) *ColorControl {
	if cm == nil {
		cm = Viridis()
	}
	if vMax <= vMin {
		vMax = vMin + 1.0
	}
	return &ColorControl{
		cm:         cm,
		vMin:       vMin,
		vMax:       vMax,
		dataMin:    vMin,
		dataMax:    vMax,
		autoLevels: false,
		version:    1,
	}
}

// NewAutoColorControl 创建首次载入数据时自动适配全量程 [min, max] 的色彩控制器。
func NewAutoColorControl(cm *Colormap) *ColorControl {
	c := NewColorControl(cm, 0, 1)
	c.autoLevels = true
	return c
}

func (cc *ColorControl) notifyUnlocked(subs []func()) {
	for _, fn := range subs {
		fn()
	}
}

func (cc *ColorControl) cloneSubsLocked() []func() {
	if len(cc.subscribers) == 0 {
		return nil
	}
	return slices.Clone(cc.subscribers)
}

// SetUnit 设置色棒刻度物理量纲（如 "dB"、"V"、"K"）。
func (cc *ColorControl) SetUnit(label, unit string) *ColorControl {
	cc.mu.Lock()
	cc.label = label
	cc.unit = unit
	cc.version++
	subs := cc.cloneSubsLocked()
	cc.mu.Unlock()
	cc.notifyUnlocked(subs)
	return cc
}

// LabelAndUnit 返回物理量名称与单位。
func (cc *ColorControl) LabelAndUnit() (label, unit string) {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return cc.label, cc.unit
}

// Colormap 返回当前激活的伪彩色表。
func (cc *ColorControl) Colormap() *Colormap {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return cc.cm
}

// SetColormap 切换伪彩色表（零拷贝，立即触发 GPU LUT 纹理切换）。
func (cc *ColorControl) SetColormap(cm *Colormap) {
	if cm == nil {
		return
	}
	cc.mu.Lock()
	cc.cm = cm
	cc.version++
	subs := cc.cloneSubsLocked()
	cc.mu.Unlock()
	cc.notifyUnlocked(subs)
}

// Levels 返回当前伪彩映射窗宽窗位 [vMin, vMax]。
func (cc *ColorControl) Levels() (vMin, vMax float64) {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return cc.vMin, cc.vMax
}

// SetLevels 手动设定伪彩动态范围窗宽窗位 [vMin, vMax]（O(1) 纯 Uniform 更新，无需重算数据纹理）。
func (cc *ColorControl) SetLevels(vMin, vMax float64) {
	if math.IsNaN(vMin) || math.IsNaN(vMax) || math.IsInf(vMin, 0) || math.IsInf(vMax, 0) {
		return
	}
	if vMax < vMin {
		vMin, vMax = vMax, vMin
	}
	if vMax-vMin < 1e-15 {
		vMax = vMin + 1e-9
	}
	cc.mu.Lock()
	cc.vMin = vMin
	cc.vMax = vMax
	cc.autoLevels = false
	cc.version++
	subs := cc.cloneSubsLocked()
	cc.mu.Unlock()
	cc.notifyUnlocked(subs)
}

// DataRange 返回最近一次上传数据矩阵的完整物理值域 [dataMin, dataMax]。
func (cc *ColorControl) DataRange() (dMin, dMax float64) {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return cc.dataMin, cc.dataMax
}

// ResetLevels 将动态调窗区间一键复位至完整数据物理极值 [dataMin, dataMax]。
func (cc *ColorControl) ResetLevels() {
	cc.mu.Lock()
	cc.vMin = cc.dataMin
	cc.vMax = cc.dataMax
	cc.autoLevels = true
	cc.version++
	subs := cc.cloneSubsLocked()
	cc.mu.Unlock()
	cc.notifyUnlocked(subs)
}

// Version 返回单调递增状态版本号。
func (cc *ColorControl) Version() uint64 {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return cc.version
}

// OnChange 注册状态变更回调。
func (cc *ColorControl) OnChange(fn func()) {
	if fn == nil {
		return
	}
	cc.mu.Lock()
	cc.subscribers = append(cc.subscribers, fn)
	cc.mu.Unlock()
}

// UpdateDataStats 当底层热力图或瀑布图数据更新时同步全量极值与 256 桶标量直方图。
func (cc *ColorControl) UpdateDataStats(dMin, dMax float64, values []float64) {
	if math.IsNaN(dMin) || math.IsNaN(dMax) || dMax <= dMin {
		dMax = dMin + 1.0
	}
	var counts [HistogramBins]uint32
	var maxCount uint32
	if len(values) > 0 {
		// 对超大规模矩阵（> 262,144 点）采用均匀步进抽样统计直方图，确保 < 0.2ms 完成分布刷新
		stride := max(len(values)/262144, 1)
		invSpan := float64(HistogramBins-1) / (dMax - dMin)
		for i := 0; i < len(values); i += stride {
			v := values[i]
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			b := int((v - dMin) * invSpan)
			if b < 0 {
				b = 0
			} else if b >= HistogramBins {
				b = HistogramBins - 1
			}
			counts[b]++
			if counts[b] > maxCount {
				maxCount = counts[b]
			}
		}
	}

	cc.mu.Lock()
	cc.dataMin = dMin
	cc.dataMax = dMax
	if cc.autoLevels {
		cc.vMin = dMin
		cc.vMax = dMax
	}
	if maxCount > 0 {
		// 采用对数压缩增强（log1p）或柔和幂次缩放使稀疏高能尖峰在直方图中清晰可见
		invMax := 1.0 / math.Log1p(float64(maxCount))
		for i := range HistogramBins {
			cc.bins[i] = math.Log1p(float64(counts[i])) * invMax
		}
		cc.hasHist = true
	} else {
		clear(cc.bins[:])
		cc.hasHist = false
	}
	cc.version++
	cc.mu.Unlock()
}

// Histogram 返回归一化至 [0, 1] 的 256 桶分布副本及是否有效。
func (cc *ColorControl) Histogram() ([HistogramBins]float64, bool) {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return cc.bins, cc.hasHist
}

// NormalizeForBase 将当前窗位 [vMin, vMax] 转换为相对于纹理编码基准 [baseMin, baseMax] 的着色器 Uniform 参数。
func (cc *ColorControl) NormalizeForBase(baseMin, baseMax float64) (vMinNorm, invSpanNorm float32) {
	vMin, vMax := cc.Levels()
	baseSpan := baseMax - baseMin
	if baseSpan <= 1e-30 {
		return 0, 1
	}
	mn := (vMin - baseMin) / baseSpan
	mx := (vMax - baseMin) / baseSpan
	span := mx - mn
	if span <= 1e-12 {
		span = 1e-12
	}
	return float32(mn), float32(1.0 / span)
}

// ColorBar 是紧凑型静态/联动科学色棒组件，右侧带 SI 国际单位刻度与右键色表切换菜单。
type ColorBar struct {
	ctrl   *ColorControl
	theme  mygograph.Theme
	scale  *mygograph.LinearScale
	ticks  []mygograph.Tick
	glyphs map[string][]ui.Glyph
}

// NewColorBar 创建绑定至 ColorControl 的色棒组件。
func NewColorBar(ctrl *ColorControl) *ColorBar {
	if ctrl == nil {
		ctrl = NewColorControl(Viridis(), 0, 1)
	}
	return &ColorBar{
		ctrl:   ctrl,
		theme:  mygograph.DarkTheme(),
		scale:  mygograph.NewLinearScale(),
		ticks:  make([]mygograph.Tick, 0, 16),
		glyphs: make(map[string][]ui.Glyph, 64),
	}
}

// SetTheme 设置色棒配色主题。
func (cb *ColorBar) SetTheme(th mygograph.Theme) *ColorBar {
	cb.theme = th
	return cb
}

// Control 返回关联的 ColorControl。
func (cb *ColorBar) Control() *ColorControl { return cb.ctrl }

// View 挂载色棒声明式组件（实现 mygograph.ViewWidget 接口）。
func (cb *ColorBar) View(c *ui.Context) ui.Element {
	return ui.Box(c).
		Fill().
		ContextMenu(func(m *ui.Menu) {
			buildColormapMenu(m, cb.ctrl)
		}).
		Draw(cb.draw)
}

func (cb *ColorBar) shapeText(s string, font ui.Font) []ui.Glyph {
	if g, ok := cb.glyphs[s]; ok {
		return g
	}
	if len(cb.glyphs) >= 256 {
		clear(cb.glyphs)
	}
	g := ui.Shape(s, font)
	cb.glyphs[s] = g
	return g
}

func (cb *ColorBar) draw(p *ui.Painter, r ui.Rect) {
	if r.W < 16 || r.H < 24 {
		return
	}
	p.Fill(r, cb.theme.Background, 0)

	padTop := float32(18)
	padBot := float32(16)
	barW := min(max(r.W*0.26, 14), 22)
	barRect := ui.Rect{
		X: r.X + 6,
		Y: r.Y + padTop,
		W: barW,
		H: max(r.H-padTop-padBot, 16),
	}

	cm := cb.ctrl.Colormap()
	p.Image(cm.VerticalBitmap(), barRect, ui.FillBox)
	p.Stroke(barRect, cb.theme.AxisLine, 0, 1)

	vMin, vMax := cb.ctrl.Levels()
	label, unit := cb.ctrl.LabelAndUnit()
	var unitLabel string
	cb.ticks, unitLabel = cb.scale.AppendTicks(vMin, vMax, barRect.H, unit, cb.ticks[:0])

	font := ui.Font{Family: cb.theme.MonoFontFamily, Size: cb.theme.TickFontSize}
	fm := font.Metrics()
	span := vMax - vMin
	if span <= 0 {
		span = 1
	}

	for _, tk := range cb.ticks {
		frac := (tk.Value - vMin) / span
		if frac < -1e-4 || frac > 1+1e-4 {
			continue
		}
		y := barRect.Y + barRect.H*float32(1.0-frac)
		tLen := float32(3)
		if tk.Major {
			tLen = 5
		}
		p.Line(barRect.X+barRect.W, y, barRect.X+barRect.W+tLen, y, 1, cb.theme.AxisLine)
		if tk.Major && tk.Label != "" {
			gl := cb.shapeText(tk.Label, font)
			p.Glyphs(gl, barRect.X+barRect.W+tLen+3, y+(fm.Ascent-fm.Descent)*0.36, cb.theme.AxisText)
		}
	}

	title := label
	if unitLabel != "" {
		if title != "" {
			title += " (" + unitLabel + ")"
		} else {
			title = unitLabel
		}
	}
	if title != "" {
		tf := ui.Font{Family: cb.theme.FontFamily, Size: cb.theme.TickFontSize, Weight: 600}
		gl := cb.shapeText(title, tf)
		p.Glyphs(gl, barRect.X, r.Y+12, cb.theme.TitleText)
	}
}

type histDragMode uint8

const (
	histDragNone histDragMode = iota
	histDragMin
	histDragMax
	histDragBand
)

// HistogramLUT 复刻 PyQtGraph 标志性的交互式直方图伪彩调窗器（HistogramLUTItem）：
//   - 左侧实时绘制 256 桶数值频数包络曲线，叠加可拖拽的 [vMin, vMax] 双水平调窗手柄与半透明高亮带；
//   - 中部实时映射当前窗位压缩后的垂直伪彩色阶条；
//   - 右侧绘制全量程 SI 工程刻度，支持滚轮缩放窗宽、双击一键复位全量程与右键切换 8 种科研色表。
type HistogramLUT struct {
	ctrl   *ColorControl
	theme  mygograph.Theme
	scale  *mygograph.LinearScale
	ticks  []mygograph.Tick
	ptsBuf [][2]float32
	glyphs map[string][]ui.Glyph

	// 局部布局缓存与拖拽状态机
	histRect    ui.Rect
	barRect     ui.Rect
	dragMode    histDragMode
	dragStartY  float32
	dragInitMin float64
	dragInitMax float64
	hoverCursor ui.Cursor
}

// NewHistogramLUT 创建绑定至 ColorControl 的交互式直方图调窗控件。
func NewHistogramLUT(ctrl *ColorControl) *HistogramLUT {
	if ctrl == nil {
		ctrl = NewColorControl(Viridis(), 0, 1)
	}
	return &HistogramLUT{
		ctrl:   ctrl,
		theme:  mygograph.DarkTheme(),
		scale:  mygograph.NewLinearScale(),
		ticks:  make([]mygograph.Tick, 0, 16),
		ptsBuf: make([][2]float32, 0, HistogramBins+4),
		glyphs: make(map[string][]ui.Glyph, 128),
	}
}

// SetTheme 设置直方图调窗器主题配色。
func (h *HistogramLUT) SetTheme(th mygograph.Theme) *HistogramLUT {
	h.theme = th
	return h
}

// Control 返回关联的 ColorControl。
func (h *HistogramLUT) Control() *ColorControl { return h.ctrl }

// View 挂载 HistogramLUT 声明式组件（无缝嵌入 GraphLayout.AddFixedRight）。
func (h *HistogramLUT) View(c *ui.Context) ui.Element {
	cur := h.hoverCursor
	if h.dragMode == histDragMin || h.dragMode == histDragMax {
		cur = ui.CursorResizeNS
	} else if h.dragMode == histDragBand {
		cur = ui.CursorGrabbing
	}
	return ui.Box(c).
		Fill().
		Cursor(cur).
		HandleInput(h.handleInput).
		ContextMenu(func(m *ui.Menu) {
			buildColormapMenu(m, h.ctrl)
		}).
		Draw(h.draw)
}

func (h *HistogramLUT) shapeText(s string, font ui.Font) []ui.Glyph {
	if g, ok := h.glyphs[s]; ok {
		return g
	}
	if len(h.glyphs) >= 256 {
		clear(h.glyphs)
	}
	g := ui.Shape(s, font)
	h.glyphs[s] = g
	return g
}

func (h *HistogramLUT) computeLayout(w, totalH float32) {
	padTop := float32(20)
	padBot := float32(16)
	innerH := max(totalH-padTop-padBot, 24)
	histW := min(max(w*0.36, 28), 64)
	barW := float32(14)
	h.histRect = ui.Rect{X: 6, Y: padTop, W: histW, H: innerH}
	h.barRect = ui.Rect{X: 6 + histW + 4, Y: padTop, W: barW, H: innerH}
}

func (h *HistogramLUT) valToLocalY(v, dMin, dMax float64) float32 {
	span := dMax - dMin
	if span <= 1e-30 {
		return h.histRect.Y + h.histRect.H*0.5
	}
	frac := (v - dMin) / span
	return h.histRect.Y + h.histRect.H*float32(1.0-frac)
}

func (h *HistogramLUT) localYToVal(ly float32, dMin, dMax float64) float64 {
	if h.histRect.H <= 1 {
		return dMin
	}
	frac := 1.0 - float64(ly-h.histRect.Y)/float64(h.histRect.H)
	return dMin + frac*(dMax-dMin)
}

func (h *HistogramLUT) handleInput(ev ui.InputEvent) bool {
	if h.histRect.H <= 0 {
		h.computeLayout(120, 320)
	}
	dMin, dMax := h.ctrl.DataRange()
	vMin, vMax := h.ctrl.Levels()
	yMinPx := h.valToLocalY(vMin, dMin, dMax) // 屏幕下方（对应较小值 vMin）
	yMaxPx := h.valToLocalY(vMax, dMin, dMax) // 屏幕上方（对应较大值 vMax）
	activeRight := h.barRect.X + h.barRect.W + 6

	switch ev.Kind {
	case ui.InputPointerMove:
		if h.dragMode != histDragNone {
			dSpan := dMax - dMin
			if dSpan <= 1e-30 || h.histRect.H <= 1 {
				return true
			}
			deltaVal := -float64(ev.Y-h.dragStartY) / float64(h.histRect.H) * dSpan
			eps := max(dSpan*1e-4, 1e-12)
			switch h.dragMode {
			case histDragMax:
				newMax := max(h.dragInitMax+deltaVal, h.dragInitMin+eps)
				h.ctrl.SetLevels(h.dragInitMin, newMax)
			case histDragMin:
				newMin := min(h.dragInitMin+deltaVal, h.dragInitMax-eps)
				h.ctrl.SetLevels(newMin, h.dragInitMax)
			case histDragBand:
				h.ctrl.SetLevels(h.dragInitMin+deltaVal, h.dragInitMax+deltaVal)
			}
			return true
		}

		var nextCur ui.Cursor
		if ev.X >= h.histRect.X-4 && ev.X <= activeRight {
			if math.Abs(float64(ev.Y-yMaxPx)) <= 6 || math.Abs(float64(ev.Y-yMinPx)) <= 6 {
				nextCur = ui.CursorResizeNS
			} else if ev.Y > min(yMaxPx, yMinPx) && ev.Y < max(yMaxPx, yMinPx) {
				nextCur = ui.CursorGrab
			}
		}
		if nextCur != h.hoverCursor {
			h.hoverCursor = nextCur
			return true
		}
		return false

	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		if ev.Clicks >= 2 {
			h.ctrl.ResetLevels()
			return true
		}
		if ev.X < h.histRect.X-4 || ev.X > activeRight {
			return false
		}
		h.dragStartY = ev.Y
		h.dragInitMin = vMin
		h.dragInitMax = vMax
		dMaxHandle := math.Abs(float64(ev.Y - yMaxPx))
		dMinHandle := math.Abs(float64(ev.Y - yMinPx))
		if dMaxHandle <= 7 && dMaxHandle <= dMinHandle {
			h.dragMode = histDragMax
			return true
		}
		if dMinHandle <= 7 {
			h.dragMode = histDragMin
			return true
		}
		if ev.Y > min(yMaxPx, yMinPx) && ev.Y < max(yMaxPx, yMinPx) {
			h.dragMode = histDragBand
			return true
		}
		return false

	case ui.InputPointerUp:
		if h.dragMode != histDragNone {
			h.dragMode = histDragNone
			return true
		}

	case ui.InputScroll:
		if ev.DY == 0 {
			return false
		}
		factor := math.Exp(float64(ev.DY) * 0.0020)
		anchor := h.localYToVal(ev.Y, dMin, dMax)
		newMin := anchor - (anchor-vMin)*factor
		newMax := anchor + (vMax-anchor)*factor
		h.ctrl.SetLevels(newMin, newMax)
		return true
	}
	return false
}

func (h *HistogramLUT) draw(p *ui.Painter, r ui.Rect) {
	if r.W < 36 || r.H < 32 {
		return
	}
	h.computeLayout(r.W, r.H)
	p.Fill(r, h.theme.Background, 0)

	histBox := ui.Rect{X: r.X + h.histRect.X, Y: r.Y + h.histRect.Y, W: h.histRect.W, H: h.histRect.H}
	barBox := ui.Rect{X: r.X + h.barRect.X, Y: r.Y + h.barRect.Y, W: h.barRect.W, H: h.barRect.H}

	p.Fill(histBox, h.theme.PlotBackground, 2)

	dMin, dMax := h.ctrl.DataRange()
	vMin, vMax := h.ctrl.Levels()
	cm := h.ctrl.Colormap()

	// 1. 绘制 256 桶水平频数包络曲线（底部对应 dMin，顶部对应 dMax，向右展开）
	bins, hasHist := h.ctrl.Histogram()
	if hasHist {
		var fillPath ui.Path
		fillPath.MoveTo(histBox.X+1, histBox.Y+histBox.H)
		h.ptsBuf = h.ptsBuf[:0]
		for i := range HistogramBins {
			frac := float32(i) / float32(HistogramBins-1)
			py := histBox.Y + histBox.H*(1.0-frac)
			px := histBox.X + 1 + float32(bins[i])*(histBox.W-3)
			h.ptsBuf = append(h.ptsBuf, [2]float32{px, py})
			fillPath.LineTo(px, py)
		}
		fillPath.LineTo(histBox.X+1, histBox.Y)
		fillPath.Close()
		p.FillPath(&fillPath, ui.RGBA(h.theme.AccentColor.R, h.theme.AccentColor.G, h.theme.AccentColor.B, 52))
		p.StrokePolyline(h.ptsBuf, 1.15, h.theme.AccentColor)
	}

	// 2. 绘制当前 [vMin, vMax] 调窗选区半透明遮罩与上下两条水平控制线
	yMinScreen := r.Y + h.valToLocalY(vMin, dMin, dMax)
	yMaxScreen := r.Y + h.valToLocalY(vMax, dMin, dMax)
	bandTop := max(min(yMaxScreen, yMinScreen), histBox.Y)
	bandBot := min(max(yMaxScreen, yMinScreen), histBox.Y+histBox.H)
	if bandBot > bandTop {
		p.Fill(ui.Rect{
			X: histBox.X,
			Y: bandTop,
			W: histBox.W,
			H: bandBot - bandTop,
		}, h.theme.ZoomRectFill, 0)
	}
	p.Stroke(histBox, h.theme.PanelBorder, 2, 1)

	// 绘制 vMax / vMin 水平抓手横线与三角锚标
	handleC := h.theme.Palette[1] // 醒目琥珀黄调窗线
	for _, hy := range [2]float32{yMaxScreen, yMinScreen} {
		cy := min(max(hy, histBox.Y), histBox.Y+histBox.H)
		p.Line(histBox.X, cy, barBox.X+barBox.W, cy, 1.6, handleC)
	}

	// 3. 绘制动态调窗实时映射后的垂直伪彩色带（> vMax 饱和为顶色，< vMin 饱和为底色，中间渲染 VerticalBitmap）
	p.Clip(barBox, 0, func() {
		topColor := cm.At(1.0)
		botColor := cm.At(0.0)
		spanY := yMinScreen - yMaxScreen
		if yMaxScreen > barBox.Y {
			p.Fill(ui.Rect{X: barBox.X, Y: barBox.Y, W: barBox.W, H: min(yMaxScreen, barBox.Y+barBox.H) - barBox.Y}, topColor, 0)
		}
		if yMinScreen < barBox.Y+barBox.H {
			topY := max(yMinScreen, barBox.Y)
			p.Fill(ui.Rect{X: barBox.X, Y: topY, W: barBox.W, H: barBox.Y + barBox.H - topY}, botColor, 0)
		}
		if spanY >= 1.0 {
			p.Image(cm.VerticalBitmap(), ui.Rect{
				X: barBox.X,
				Y: yMaxScreen,
				W: barBox.W,
				H: spanY,
			}, ui.FillBox)
		}
	})
	p.Stroke(barBox, h.theme.AxisLine, 0, 1)

	// 4. 绘制右侧物理量程 SI 刻度与色表标题
	label, unit := h.ctrl.LabelAndUnit()
	var unitLabel string
	h.ticks, unitLabel = h.scale.AppendTicks(dMin, dMax, barBox.H, unit, h.ticks[:0])

	font := ui.Font{Family: h.theme.MonoFontFamily, Size: h.theme.TickFontSize}
	fm := font.Metrics()
	dSpan := dMax - dMin
	if dSpan <= 0 {
		dSpan = 1
	}
	for _, tk := range h.ticks {
		frac := (tk.Value - dMin) / dSpan
		if frac < -1e-4 || frac > 1+1e-4 {
			continue
		}
		ty := barBox.Y + barBox.H*float32(1.0-frac)
		tLen := float32(3)
		if tk.Major {
			tLen = 5
		}
		p.Line(barBox.X+barBox.W, ty, barBox.X+barBox.W+tLen, ty, 1, h.theme.AxisLine)
		if tk.Major && tk.Label != "" {
			gl := h.shapeText(tk.Label, font)
			p.Glyphs(gl, barBox.X+barBox.W+tLen+3, ty+(fm.Ascent-fm.Descent)*0.36, h.theme.AxisText)
		}
	}

	header := cm.Name()
	if unitLabel != "" {
		header += " (" + unitLabel + ")"
	} else if label != "" {
		header = label + " [" + cm.Name() + "]"
	}
	tf := ui.Font{Family: h.theme.FontFamily, Size: h.theme.TickFontSize, Weight: 600}
	p.Glyphs(h.shapeText(header, tf), histBox.X, r.Y+13, h.theme.TitleText)
}

func buildColormapMenu(m *ui.Menu, ctrl *ColorControl) {
	if ctrl == nil {
		return
	}
	if m.Item("Reset Window Levels (Double-Click)").Chosen() {
		ctrl.ResetLevels()
	}
	m.Separator()
	cur := ctrl.Colormap()
	for _, cm := range BuiltinColormaps() {
		active := cur != nil && cur.Name() == cm.Name()
		if m.Item(cm.Name()).Radio(active).Chosen() {
			ctrl.SetColormap(cm)
		}
	}
}
