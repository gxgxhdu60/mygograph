package mygograph

import (
	"cmp"
	"math"
	"slices"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph/data"
)

type dragAction uint8

const (
	dragNone dragAction = iota
	dragItem
	dragPanPlot
	dragRectZoom
	dragRightScalePlot
	dragPanAxis
	dragScaleAxis
)

type gridCacheKey struct {
	w, h      int
	scale     float32
	vbRev     uint64
	showMajor bool
	showMinor bool
	dark      bool
}

// PlotOption 定义创建 Plot 时的函数式配置项。
type PlotOption func(*Plot)

// WithTitle 设置图表顶部居中标题。
func WithTitle(title string) PlotOption {
	return func(p *Plot) { p.title = title }
}

// WithTheme 指定图表配色主题。
func WithTheme(th Theme) PlotOption {
	return func(p *Plot) {
		p.theme = th
		p.customTheme = true
	}
}

// WithBottomAxis 替换底部 X 坐标轴（传 nil 隐藏）。
func WithBottomAxis(ax *Axis) PlotOption {
	return func(p *Plot) {
		if ax == nil {
			p.bottomAxis.SetVisible(false)
			return
		}
		p.bottomAxis = ax.SetOrientation(AxisBottom)
	}
}

// WithLeftAxis 替换左侧 Y 坐标轴（传 nil 隐藏）。
func WithLeftAxis(ax *Axis) PlotOption {
	return func(p *Plot) {
		if ax == nil {
			p.leftAxis.SetVisible(false)
			return
		}
		p.leftAxis = ax.SetOrientation(AxisLeft)
	}
}

// WithTopAxis 挂载并启用顶部辅助 X 坐标轴。
func WithTopAxis(ax *Axis) PlotOption {
	return func(p *Plot) {
		if ax == nil {
			p.topAxis.SetVisible(false)
			return
		}
		p.topAxis = ax.SetOrientation(AxisTop).SetVisible(true)
	}
}

// WithRightAxis 挂载并启用右侧辅助 Y 坐标轴。
func WithRightAxis(ax *Axis) PlotOption {
	return func(p *Plot) {
		if ax == nil {
			p.rightAxis.SetVisible(false)
			return
		}
		p.rightAxis = ax.SetOrientation(AxisRight).SetVisible(true)
	}
}

// WithGrid 配置主/次背景网格线默认显隐。
func WithGrid(major, minor bool) PlotOption {
	return func(p *Plot) {
		p.showMajorGrid = major
		p.showMinorGrid = minor
		p.bottomAxis.SetGrid(major, minor)
		p.leftAxis.SetGrid(major, minor)
	}
}

// WithLegend 控制右上角可交互图例浮窗显隐。
func WithLegend(enable bool) PlotOption {
	return func(p *Plot) { p.showLegend = enable }
}

// WithMouseMode 设置初始左键拖拽模式（PanMode 或 RectMode）。
func WithMouseMode(mode MouseMode) PlotOption {
	return func(p *Plot) { p.vb.SetMouseMode(mode) }
}

// Plot 是工业级科学绘图核心容器，集成四向坐标轴、离屏网格缓存、PyQtGraph 全套手势状态机、
// 浮动交互图例、[A] 自动量程角标、右键上下文菜单与 VSync 空闲零功耗节流。
type Plot struct {
	title       string
	theme       Theme
	customTheme bool

	vb         *ViewBox
	bottomAxis *Axis
	leftAxis   *Axis
	topAxis    *Axis
	rightAxis  *Axis

	showMajorGrid bool
	showMinorGrid bool
	showLegend    bool

	items      []Item
	sortedBuf  []Item
	legendHits []legendHitBox

	// 离屏网格光栅缓存（静态或同视口流式刷新时零开销直接贴图）
	gridCanvas *ui.Canvas
	gridKey    gridCacheKey
	majXBuf    []float32
	minXBuf    []float32
	majYBuf    []float32
	minYBuf    []float32

	// 共享浮层字形缓存（标题、图例、游标读数、角标零分配复用）
	overlayGlyphs map[glyphCacheKey]shapedEntry

	// 最近一次布局出的各子区域局部坐标（相对于 Plot 元素左上角 (0,0)）
	elemW, elemH  float32
	localPlotRect ui.Rect
	localLeftAx   ui.Rect
	localRightAx  ui.Rect
	localTopAx    ui.Rect
	localBotAx    ui.Rect
	autoBtnRect   ui.Rect
	hasAutoBtn    bool
	liveBtnRect   ui.Rect
	hasLiveBtn    bool

	// 交互手势状态机
	dragMode     dragAction
	activeItem   InteractiveItem
	dragAxisMask AxisMask
	dragStartX   float32
	dragStartY   float32
	dragLastX    float32
	dragLastY    float32
	hoverX       float32
	hoverY       float32
	contextMenuX float32
	contextMenuY float32
	pointerIn    bool
	hoverCursor  ui.Cursor
	lastMods     ui.Modifiers
	rightDragged bool

	// VSync 实时流唤醒与空闲自动休眠状态
	svc            atomic.Pointer[ui.Services]
	lastStreamSum  uint64
	lastStreamMove time.Time
	extraMenu      func(m *ui.Menu)
	exportPath     string
	exportHook     func(path string, err error)
	lastCSVExport  string
}

type legendHitBox struct {
	rect   ui.Rect // 相对于 Plot 元素左上角的局部矩形
	target Item
}

// NewPlot 创建并初始化科学绘图核心容器。
func NewPlot(opts ...PlotOption) *Plot {
	p := &Plot{
		theme:         DarkTheme(),
		vb:            NewViewBox(),
		bottomAxis:    NewAxis(AxisBottom),
		leftAxis:      NewAxis(AxisLeft),
		topAxis:       NewAxis(AxisTop).SetVisible(false).SetGrid(false, false),
		rightAxis:     NewAxis(AxisRight).SetVisible(false).SetGrid(false, false),
		showMajorGrid: true,
		showMinorGrid: false,
		showLegend:    false,
		overlayGlyphs: make(map[glyphCacheKey]shapedEntry, 64),
		hoverCursor:   ui.CursorCrosshair,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	p.vb.boundsFn = p.itemBounds
	// 同步坐标轴对数状态至 ViewBox
	p.syncScaleFlags()
	return p
}

func (p *Plot) syncScaleFlags() {
	if p.bottomAxis != nil && p.bottomAxis.Scale() != nil {
		p.vb.SetLogX(p.bottomAxis.Scale().IsLog())
	}
	if p.leftAxis != nil && p.leftAxis.Scale() != nil {
		p.vb.SetLogY(p.leftAxis.Scale().IsLog())
	}
}

// ViewBox 返回底层双精度视口控制器。
func (p *Plot) ViewBox() *ViewBox { return p.vb }

// Theme 返回当前生效的配色主题。
func (p *Plot) Theme() Theme { return p.theme }

// SetTheme 动态切换配色主题。
func (p *Plot) SetTheme(th Theme) *Plot {
	p.theme = th
	p.customTheme = true
	p.gridKey = gridCacheKey{}
	p.Invalidate()
	return p
}

// Title 返回图表标题。
func (p *Plot) Title() string { return p.title }

// SetTitle 更新图表标题。
func (p *Plot) SetTitle(title string) *Plot {
	p.title = title
	p.Invalidate()
	return p
}

// BottomAxis 返回底部 X 坐标轴。
func (p *Plot) BottomAxis() *Axis { return p.bottomAxis }

// LeftAxis 返回左侧 Y 坐标轴。
func (p *Plot) LeftAxis() *Axis { return p.leftAxis }

// TopAxis 返回顶部 X 坐标轴。
func (p *Plot) TopAxis() *Axis { return p.topAxis }

// RightAxis 返回右侧 Y 坐标轴。
func (p *Plot) RightAxis() *Axis { return p.rightAxis }

// LinkX 将当前图表的 X 轴与另一张 Plot 双向同步联动。
func (p *Plot) LinkX(other *Plot) {
	if other != nil {
		p.vb.LinkX(other.vb)
	}
}

// LinkY 将当前图表的 Y 轴与另一张 Plot 双向同步联动。
func (p *Plot) LinkY(other *Plot) {
	if other != nil {
		p.vb.LinkY(other.vb)
	}
}

// AddItem 向图表挂载一个或多个可视化图元（按 ZValue 自动分层绘制）。
func (p *Plot) AddItem(items ...Item) *Plot {
	for _, it := range items {
		if it != nil {
			p.items = append(p.items, it)
		}
	}
	p.Invalidate()
	return p
}

// RemoveItem 从图表移除指定图元。
func (p *Plot) RemoveItem(target Item) {
	for i, it := range p.items {
		if it == target {
			copy(p.items[i:], p.items[i+1:])
			p.items[len(p.items)-1] = nil
			p.items = p.items[:len(p.items)-1]
			p.Invalidate()
			return
		}
	}
}

// ClearItems 清空所有挂载图元。
func (p *Plot) ClearItems() {
	clear(p.items)
	p.items = p.items[:0]
	p.Invalidate()
}

// Items 返回当前挂载的所有图元切片。
func (p *Plot) Items() []Item { return p.items }

// AutoRange 立即扫描所有可见图元的包围盒并重置双轴自动量程。
func (p *Plot) AutoRange() {
	p.vb.EnableAutoRange(AxisBoth)
	p.updateAutoRangeFromItems()
	p.Invalidate()
}

// ResumeLiveFollow 仅重新激活水平时间轴 X 的实时自动跟随（保留用户在 Y 轴上的单导联 Solo 放大状态）。
func (p *Plot) ResumeLiveFollow() {
	p.vb.EnableAutoRange(AxisX)
	p.updateAutoRangeFromItems()
	p.Invalidate()
}

// SetPNGExportPath 设置右键菜单 "Export High-DPI PNG (2x)" 的输出文件路径（默认当前工作目录下 mygograph_export_2x.png）。
func (p *Plot) SetPNGExportPath(path string) *Plot {
	p.exportPath = path
	return p
}

// SetPNGExportHook 注册右键菜单 PNG 导出完成回调（path 为实际输出路径，err 为失败原因），供业务层提示用户或改为弹出保存对话框。
func (p *Plot) SetPNGExportHook(fn func(path string, err error)) *Plot {
	p.exportHook = fn
	return p
}

// snapshotLiveState 实现 liveStateHolder：快照宿主 Services、布局命中框、悬停与流式节流状态，返回恢复函数。
func (p *Plot) snapshotLiveState() func() {
	type liveState struct {
		services                          ui.Services
		lastMods                          ui.Modifiers
		elemW, elemH                      float32
		plotR, leftR, rightR, topR, botR  ui.Rect
		autoBtnRect, liveBtnRect          ui.Rect
		hasAutoBtn, hasLiveBtn, pointerIn bool
		hoverX, hoverY                    float32
		hoverCursor                       ui.Cursor
		lastStreamSum                     uint64
		lastStreamMove                    time.Time
	}
	st := liveState{
		p.servicesValue(), p.lastMods, p.elemW, p.elemH,
		p.localPlotRect, p.localLeftAx, p.localRightAx, p.localTopAx, p.localBotAx,
		p.autoBtnRect, p.liveBtnRect, p.hasAutoBtn, p.hasLiveBtn, p.pointerIn,
		p.hoverX, p.hoverY, p.hoverCursor, p.lastStreamSum, p.lastStreamMove,
	}
	hits := append([]legendHitBox(nil), p.legendHits...)
	return func() {
		p.setServices(st.services)
		p.lastMods, p.elemW, p.elemH = st.lastMods, st.elemW, st.elemH
		p.localPlotRect, p.localLeftAx, p.localRightAx, p.localTopAx, p.localBotAx = st.plotR, st.leftR, st.rightR, st.topR, st.botR
		p.autoBtnRect, p.liveBtnRect, p.hasAutoBtn, p.hasLiveBtn, p.pointerIn = st.autoBtnRect, st.liveBtnRect, st.hasAutoBtn, st.hasLiveBtn, st.pointerIn
		p.hoverX, p.hoverY, p.hoverCursor = st.hoverX, st.hoverY, st.hoverCursor
		p.lastStreamSum, p.lastStreamMove = st.lastStreamSum, st.lastStreamMove
		p.legendHits = append(p.legendHits[:0], hits...)
		// 离屏渲染器可能已改写网格缓存画布内容，强制窗口下一帧重建
		p.gridCanvas = nil
		p.gridKey = gridCacheKey{}
		p.Invalidate()
	}
}

// SetContextMenuHook 注册业务层自定义右键菜单扩展项。
func (p *Plot) SetContextMenuHook(fn func(m *ui.Menu)) {
	p.extraMenu = fn
}

func (p *Plot) setServices(s ui.Services) {
	if cur := p.svc.Load(); cur == nil || *cur != s {
		p.svc.Store(&s)
	}
}

func (p *Plot) servicesValue() ui.Services {
	if s := p.svc.Load(); s != nil {
		return *s
	}
	return ui.Services{}
}

// Invalidate 线程安全地请求宿主窗口在下一个 VSync 周期重绘（可供后台采集 RingBuffer 唤醒回调直接绑定）。
func (p *Plot) Invalidate() {
	if s := p.svc.Load(); s != nil {
		s.Invalidate()
	}
}

// WakeFunc 返回可直接传给 RingBuffer.SetWakeFunc 的跨协程无阻塞重绘唤醒闭包。
func (p *Plot) WakeFunc() func() {
	return p.Invalidate
}

// FormatX 使用底部（或顶部）坐标轴格式化物理 X 数值。
func (p *Plot) FormatX(val float64) string {
	x0, x1 := p.vb.XRange()
	span := math.Abs(x1 - x0)
	if p.bottomAxis != nil && p.bottomAxis.Visible() {
		return p.bottomAxis.FormatCursorValue(val, span)
	}
	if p.topAxis != nil && p.topAxis.Visible() {
		return p.topAxis.FormatCursorValue(val, span)
	}
	return FormatSIValue(val, 4, "")
}

// FormatY 使用左侧（或右侧）坐标轴格式化物理 Y 数值。
func (p *Plot) FormatY(val float64) string {
	y0, y1 := p.vb.YRange()
	span := math.Abs(y1 - y0)
	if p.leftAxis != nil && p.leftAxis.Visible() {
		return p.leftAxis.FormatCursorValue(val, span)
	}
	if p.rightAxis != nil && p.rightAxis.Visible() {
		return p.rightAxis.FormatCursorValue(val, span)
	}
	return FormatSIValue(val, 4, "")
}

// PlotRectLocal 返回最近一帧布局计算出的中心纯绘图区局部矩形（相对于 Plot 元素左上角）。
func (p *Plot) PlotRectLocal() ui.Rect { return p.localPlotRect }

// MeasureLeftMargin 计算左侧坐标轴所需边距宽度（供 GraphLayout 跨子图轴对齐测量）。
func (p *Plot) MeasureLeftMargin(estHeight float32) float32 {
	if p.leftAxis == nil || !p.leftAxis.Visible() {
		return 10
	}
	return p.leftAxis.MeasureThickness(p.vb, max(estHeight, 120), p.theme)
}

// MeasureRightMargin 计算右侧坐标轴所需边距宽度（供 GraphLayout 跨子图轴对齐测量）。
func (p *Plot) MeasureRightMargin(estHeight float32) float32 {
	if p.rightAxis == nil || !p.rightAxis.Visible() {
		return 12
	}
	return p.rightAxis.MeasureThickness(p.vb, max(estHeight, 120), p.theme)
}

// View 在声明式 UI 树中挂载并返回可链式布局的 Plot 元素句柄（默认填充父容器可用空间）。
func (p *Plot) View(c *ui.Context) ui.Element {
	p.setServices(c.Services())
	p.lastMods = c.Modifiers()
	p.syncScaleFlags()

	el := ui.Box(c).
		Fill().
		Focusable().
		FocusRingColor(ui.Color{}).
		Cursor(p.currentCursor()).
		ObserveInput(p.observeInput).
		HandleInput(p.handleInput).
		ContextMenu(p.buildContextMenu).
		Draw(p.draw)
	return el
}

func (p *Plot) observeInput(ev ui.InputEvent) {
	if ev.Kind == ui.InputPointerUp && ev.Button == 1 {
		if p.dragMode == dragRightScalePlot || p.dragMode == dragScaleAxis {
			p.dragMode = dragNone
		}
	}
}

func (p *Plot) currentCursor() ui.Cursor {
	switch p.dragMode {
	case dragPanPlot, dragPanAxis:
		return ui.CursorGrabbing
	case dragRightScalePlot, dragScaleAxis:
		return ui.CursorResizeNWSE
	case dragRectZoom:
		return ui.CursorCrosshair
	}
	if p.hoverCursor != 0 {
		return p.hoverCursor
	}
	return ui.CursorCrosshair
}

func (p *Plot) itemBounds() (xr, yr data.Range, ok bool) {
	for _, it := range p.items {
		if it == nil || !it.Visible() {
			continue
		}
		ixr, iyr, iok := it.Bounds()
		if !iok {
			continue
		}
		if !ok {
			xr, yr, ok = ixr, iyr, true
		} else {
			xr = xr.Union(ixr)
			yr = yr.Union(iyr)
		}
	}
	return xr, yr, ok
}

func (p *Plot) updateAutoRangeFromItems() {
	if p.vb.AutoRangeMask() == AxisNone {
		return
	}
	p.vb.applyLinkedAutoBounds()
}

func (p *Plot) computeSubRects(totalW, totalH float32) {
	p.elemW, p.elemH = totalW, totalH
	var titleH float32
	if p.title != "" {
		hf := titleFont(p.theme)
		hm := hf.Metrics()
		titleH = hm.Ascent + hm.Descent + 10
	} else {
		titleH = 8
	}

	topH := titleH
	if p.topAxis != nil && p.topAxis.Visible() {
		topH += p.topAxis.MeasureThickness(p.vb, max(totalW-80, 60), p.theme)
	}
	botH := float32(10)
	if p.bottomAxis != nil && p.bottomAxis.Visible() {
		botH = p.bottomAxis.MeasureThickness(p.vb, max(totalW-80, 60), p.theme)
	}

	availH := max(totalH-topH-botH, 16)
	leftW := p.MeasureLeftMargin(availH)
	rightW := p.MeasureRightMargin(availH)
	availW := max(totalW-leftW-rightW, 16)

	p.localPlotRect = ui.Rect{X: leftW, Y: topH, W: availW, H: availH}
	p.localLeftAx = ui.Rect{X: 0, Y: topH, W: leftW, H: availH}
	p.localRightAx = ui.Rect{X: leftW + availW, Y: topH, W: rightW, H: availH}
	p.localTopAx = ui.Rect{X: leftW, Y: titleH, W: availW, H: max(topH-titleH, 0)}
	p.localBotAx = ui.Rect{X: leftW, Y: topH + availH, W: availW, H: botH}
}

func (p *Plot) draw(painter *ui.Painter, r ui.Rect) {
	if r.W <= 2 || r.H <= 2 {
		return
	}

	// 1. 若开启了 AutoRange，拉取最新流式数据范围
	p.updateAutoRangeFromItems()
	p.computeSubRects(r.W, r.H)
	p.vb.EnforceAspect(p.localPlotRect.W, p.localPlotRect.H)

	plotRect := offsetRect(p.localPlotRect, r.X, r.Y)
	leftRect := offsetRect(p.localLeftAx, r.X, r.Y)
	rightRect := offsetRect(p.localRightAx, r.X, r.Y)
	topRect := offsetRect(p.localTopAx, r.X, r.Y)
	botRect := offsetRect(p.localBotAx, r.X, r.Y)

	// 2. 绘制外框与绘图区底色
	painter.Fill(r, p.theme.Background, 0)
	painter.Fill(plotRect, p.theme.PlotBackground, 0)

	// 3. 绘制顶部主标题
	if p.title != "" {
		hf := ui.Font{Family: p.theme.FontFamily, Size: p.theme.TitleFontSize + 1, Weight: 600}
		hm := hf.Metrics()
		sh := p.shapeOverlayText(p.title, hf)
		tx := plotRect.X + (plotRect.W-sh.width)*0.5
		ty := r.Y + 6 + hm.Ascent
		painter.Glyphs(sh.glyphs, tx, ty, p.theme.TitleText)
	}

	// 4. 离屏 Canvas 缓存绘制主/次网格线
	p.drawCachedGrid(painter, plotRect)

	// 5. 按 ZValue 升序稳定排序并在裁剪框内绘制所有可见图元
	p.sortedBuf = append(p.sortedBuf[:0], p.items...)
	slices.SortStableFunc(p.sortedBuf, func(a, b Item) int {
		return cmp.Compare(a.ZValue(), b.ZValue())
	})

	rc := RenderContext{
		Painter:   painter,
		PlotRect:  plotRect,
		View:      p.vb,
		Transform: p.vb.Transform(plotRect),
		Theme:     p.theme,
		Scale:     max(painter.Scale(), 1.0),
		HoverLX:   p.hoverX - p.localPlotRect.X,
		HoverLY:   p.hoverY - p.localPlotRect.Y,
		PointerIn: p.pointerIn,
		plot:      p,
	}

	painter.Clip(plotRect, 0, func() {
		for _, it := range p.sortedBuf {
			if it != nil && it.Visible() {
				it.Draw(&rc)
			}
		}
		// RectMode 橡皮筋选框叠加层
		if p.dragMode == dragRectZoom {
			rx0 := r.X + min(p.dragStartX, p.dragLastX)
			ry0 := r.Y + min(p.dragStartY, p.dragLastY)
			rw := float32(math.Abs(float64(p.dragLastX - p.dragStartX)))
			rh := float32(math.Abs(float64(p.dragLastY - p.dragStartY)))
			selR := ui.Rect{X: rx0, Y: ry0, W: rw, H: rh}
			painter.Fill(selR, p.theme.ZoomRectFill, 2)
			painter.StrokeDashed(selR, p.theme.ZoomRectBorder, 2, 1.25)
		}
	})

	// 6. 绘制绘图区精密内边框与四向坐标轴
	painter.Stroke(plotRect, p.theme.PanelBorder, 0, 1)
	if p.leftAxis != nil && p.leftAxis.Visible() {
		p.leftAxis.Draw(painter, leftRect, plotRect, p.vb, p.theme)
	}
	if p.rightAxis != nil && p.rightAxis.Visible() {
		p.rightAxis.Draw(painter, rightRect, plotRect, p.vb, p.theme)
	}
	if p.topAxis != nil && p.topAxis.Visible() {
		p.topAxis.Draw(painter, topRect, plotRect, p.vb, p.theme)
	}
	if p.bottomAxis != nil && p.bottomAxis.Visible() {
		p.bottomAxis.Draw(painter, botRect, plotRect, p.vb, p.theme)
	}

	// 7. 绘制跨出裁剪区的游标边缘读数标签（OverlayItem）
	for _, it := range p.sortedBuf {
		if it != nil && it.Visible() {
			if ov, ok := it.(OverlayItem); ok {
				ov.DrawOverlay(&rc)
			}
		}
	}

	// 8. 绘制右上角可点击交互图例（Legend）
	if p.showLegend {
		p.drawLegend(painter, r, plotRect)
	} else {
		p.legendHits = p.legendHits[:0]
	}

	// 9. 统计活跃 StreamItem 版本（用于实时流 VSync 节流及历史回看 [Live] 恢复角标）
	now := painter.Now()
	var streamSum uint64
	hasActiveStream := false
	for _, it := range p.items {
		if it == nil || !it.Visible() {
			continue
		}
		if si, ok := it.(StreamItem); ok {
			ver, active := si.StreamVersion()
			if active {
				hasActiveStream = true
				streamSum = streamSum*1099511628211 + ver + 1
			}
		}
	}

	// 10. 当用户手动平移/缩放偏离 AutoRange 时，在左下角显示 PyQtGraph 经典 [A] 一键复位角标；
	//     若当前存在实时流且 X 轴因回拖查看历史而解耦跟随，额外并列显示 [Live] 一键贴回最新采样尾部按钮。
	if p.vb.IsManualOverride() {
		btnLocal := ui.Rect{
			X: p.localPlotRect.X + 6,
			Y: p.localPlotRect.Y + p.localPlotRect.H - 24,
			W: 22,
			H: 18,
		}
		p.autoBtnRect = btnLocal
		p.hasAutoBtn = true
		btnScreen := offsetRect(btnLocal, r.X, r.Y)
		hovered := p.pointerIn && rectContains(btnLocal, p.hoverX, p.hoverY)
		bg := p.theme.ReadoutBg
		fg := p.theme.AxisText
		if hovered {
			bg = p.theme.AccentColor
			fg = ui.RGB(10, 14, 20)
		}
		painter.Fill(btnScreen, bg, 3)
		painter.Stroke(btnScreen, p.theme.ReadoutBorder, 3, 1)
		bf := ui.Font{Family: p.theme.FontFamily, Size: 10, Weight: 700}
		bm := bf.Metrics()
		sh := p.shapeOverlayText("A", bf)
		painter.Glyphs(sh.glyphs, btnScreen.X+(btnScreen.W-sh.width)*0.5, btnScreen.Y+(btnScreen.H+bm.Ascent-bm.Descent)*0.5, fg)

		if hasActiveStream && !p.vb.IsAutoX() {
			shLive := p.shapeOverlayText("Live", bf)
			liveLocal := ui.Rect{
				X: btnLocal.X + btnLocal.W + 5,
				Y: btnLocal.Y,
				W: shLive.width + 14,
				H: 18,
			}
			p.liveBtnRect = liveLocal
			p.hasLiveBtn = true
			liveScreen := offsetRect(liveLocal, r.X, r.Y)
			liveHov := p.pointerIn && rectContains(liveLocal, p.hoverX, p.hoverY)
			lBg := p.theme.ReadoutBg
			lFg := p.theme.AccentColor
			if liveHov {
				lBg = p.theme.AccentColor
				lFg = ui.RGB(10, 14, 20)
			}
			painter.Fill(liveScreen, lBg, 3)
			painter.Stroke(liveScreen, p.theme.AccentColor, 3, 1)
			painter.Glyphs(shLive.glyphs, liveScreen.X+7, liveScreen.Y+(liveScreen.H+bm.Ascent-bm.Descent)*0.5, lFg)
		} else {
			p.hasLiveBtn = false
		}
	} else {
		p.hasAutoBtn = false
		p.hasLiveBtn = false
	}

	// 11. VSync 实时流按需拉取与空闲 0% CPU 自动休眠门禁：
	//     仅当存在活跃 StreamItem 且其数据版本仍在推进（150ms 活跃窗口内）时请求下一帧 AnimationFrame！
	if hasActiveStream {
		if streamSum != p.lastStreamSum {
			p.lastStreamSum = streamSum
			p.lastStreamMove = now
			painter.AnimationFrame()
		} else if !p.lastStreamMove.IsZero() && now.Sub(p.lastStreamMove) < 150*time.Millisecond {
			painter.AnimationFrame()
		}
	}
}

func (p *Plot) drawCachedGrid(painter *ui.Painter, plotRect ui.Rect) {
	if !p.showMajorGrid && !p.showMinorGrid {
		return
	}
	cw := max(int(math.Round(float64(plotRect.W))), 1)
	ch := max(int(math.Round(float64(plotRect.H))), 1)
	key := gridCacheKey{
		w:         cw,
		h:         ch,
		scale:     painter.Scale(),
		vbRev:     p.vb.Revision(),
		showMajor: p.showMajorGrid,
		showMinor: p.showMinorGrid,
		dark:      p.theme.Dark,
	}

	if p.gridCanvas == nil {
		p.gridCanvas = ui.NewCanvas(cw, ch)
		p.gridKey = gridCacheKey{}
	} else {
		oldW, oldH := p.gridCanvas.Size()
		if oldW != cw || oldH != ch {
			p.gridCanvas = ui.NewCanvas(cw, ch)
			p.gridKey = gridCacheKey{}
		}
	}

	if p.gridKey != key {
		p.gridKey = key
		localOriginRect := ui.Rect{X: 0, Y: 0, W: float32(cw), H: float32(ch)}
		p.majXBuf, p.minXBuf = p.bottomAxis.GridPositions(p.vb, localOriginRect, p.majXBuf, p.minXBuf)
		p.majYBuf, p.minYBuf = p.leftAxis.GridPositions(p.vb, localOriginRect, p.majYBuf, p.minYBuf)

		p.gridCanvas.Draw(painter, func(cp *ui.Painter) {
			if p.showMinorGrid {
				for _, x := range p.minXBuf {
					cp.Line(x, 0, x, float32(ch), 1, p.theme.GridMinor)
				}
				for _, y := range p.minYBuf {
					cp.Line(0, y, float32(cw), y, 1, p.theme.GridMinor)
				}
			}
			if p.showMajorGrid {
				for _, x := range p.majXBuf {
					cp.Line(x, 0, x, float32(ch), 1, p.theme.GridMajor)
				}
				for _, y := range p.majYBuf {
					cp.Line(0, y, float32(cw), y, 1, p.theme.GridMajor)
				}
			}
		})
	}

	painter.Image(p.gridCanvas.Bitmap(), plotRect, ui.FillBox)
}

func (p *Plot) drawLegend(painter *ui.Painter, elemRect, plotRect ui.Rect) {
	p.legendHits = p.legendHits[:0]
	var entries []LegendEntry
	for _, it := range p.items {
		if it == nil {
			continue
		}
		if lp, ok := it.(LegendProvider); ok {
			entries = append(entries, lp.LegendEntries()...)
		}
	}
	if len(entries) == 0 {
		return
	}

	lf := ui.Font{Family: p.theme.FontFamily, Size: max(p.theme.AxisFontSize, 11), Weight: 400}
	lm := lf.Metrics()
	rowH := max(lm.Ascent+lm.Descent+6, 18)
	const (
		padX      float32 = 8
		padY      float32 = 6
		swatchW   float32 = 16
		swatchGap float32 = 6
	)

	var maxTextW float32
	for _, e := range entries {
		sh := p.shapeOverlayText(e.Name, lf)
		if sh.width > maxTextW {
			maxTextW = sh.width
		}
	}

	boxW := padX*2 + swatchW + swatchGap + maxTextW
	boxH := padY*2 + float32(len(entries))*rowH
	boxScreen := ui.Rect{
		X: plotRect.X + plotRect.W - boxW - 10,
		Y: plotRect.Y + 10,
		W: boxW,
		H: boxH,
	}
	painter.Fill(boxScreen, p.theme.ReadoutBg, 4)
	painter.Stroke(boxScreen, p.theme.ReadoutBorder, 4, 1)

	for i, e := range entries {
		ry := boxScreen.Y + padY + float32(i)*rowH
		rowScreen := ui.Rect{X: boxScreen.X + 2, Y: ry, W: boxW - 4, H: rowH}
		p.legendHits = append(p.legendHits, legendHitBox{
			rect:   offsetRect(rowScreen, -elemRect.X, -elemRect.Y),
			target: e.Target,
		})

		vis := e.Target == nil || e.Target.Visible()
		swColor := e.Color
		txtColor := p.theme.TitleText
		if !vis {
			swColor = swColor.Alpha(0.3)
			txtColor = p.theme.AxisText.Alpha(0.45)
		}
		midY := ry + rowH*0.5
		painter.Line(boxScreen.X+padX, midY, boxScreen.X+padX+swatchW, midY, 2.5, swColor)
		sh := p.shapeOverlayText(e.Name, lf)
		painter.Glyphs(sh.glyphs, boxScreen.X+padX+swatchW+swatchGap, ry+(rowH+lm.Ascent-lm.Descent)*0.5, txtColor)
	}
}

func (p *Plot) shapeOverlayText(text string, font ui.Font) shapedEntry {
	if text == "" {
		return shapedEntry{}
	}
	key := glyphCacheKey{text: text, fontSize: font.Size, bold: font.Weight >= 600}
	if sh, ok := p.overlayGlyphs[key]; ok {
		return sh
	}
	if len(p.overlayGlyphs) >= maxGlyphCached {
		clear(p.overlayGlyphs)
	}
	gs := ui.Shape(text, font)
	var w float32
	if len(gs) > 0 {
		last := gs[len(gs)-1]
		w = last.X + last.Advance
	}
	sh := shapedEntry{glyphs: gs, width: w}
	p.overlayGlyphs[key] = sh
	return sh
}

func (p *Plot) hitAxisMask(ex, ey float32) AxisMask {
	if p.bottomAxis != nil && p.bottomAxis.Visible() && rectContains(p.localBotAx, ex, ey) {
		return AxisX
	}
	if p.topAxis != nil && p.topAxis.Visible() && rectContains(p.localTopAx, ex, ey) {
		return AxisX
	}
	if p.leftAxis != nil && p.leftAxis.Visible() && rectContains(p.localLeftAx, ex, ey) {
		return AxisY
	}
	if p.rightAxis != nil && p.rightAxis.Visible() && rectContains(p.localRightAx, ex, ey) {
		return AxisY
	}
	return AxisNone
}

func (p *Plot) handleInput(ev ui.InputEvent) bool {
	if p.localPlotRect.W <= 0 || p.localPlotRect.H <= 0 {
		if p.elemW <= 0 {
			p.computeSubRects(640, 400)
		}
	}
	pw, ph := p.localPlotRect.W, p.localPlotRect.H
	lx := ev.X - p.localPlotRect.X
	ly := ev.Y - p.localPlotRect.Y
	inPlot := rectContains(p.localPlotRect, ev.X, ev.Y)

	switch ev.Kind {
	case ui.InputKeyDown:
		switch ev.Key {
		case ui.KeyBackspace, ui.KeyBack:
			if p.vb.PopZoom() {
				return true
			}
		case ui.KeyA, ui.KeyHome:
			if ev.Mods == 0 {
				p.AutoRange()
				return true
			}
		}
		return false

	case ui.InputPointerMove:
		p.hoverX, p.hoverY = ev.X, ev.Y
		p.pointerIn = inPlot

		// 1. 进行中的捕获拖拽优先处理
		switch p.dragMode {
		case dragItem:
			if p.activeItem != nil {
				p.activeItem.HandlePointer(ev, lx, ly, pw, ph, p.vb)
				return true
			}
		case dragPanPlot:
			dx, dy := ev.X-p.dragLastX, ev.Y-p.dragLastY
			p.dragLastX, p.dragLastY = ev.X, ev.Y
			p.vb.PanByPixels(dx, dy, pw, ph, AxisBoth)
			return true
		case dragPanAxis:
			dx, dy := ev.X-p.dragLastX, ev.Y-p.dragLastY
			p.dragLastX, p.dragLastY = ev.X, ev.Y
			p.vb.PanByPixels(dx, dy, pw, ph, p.dragAxisMask)
			return true
		case dragRectZoom:
			p.dragLastX, p.dragLastY = ev.X, ev.Y
			return true
		case dragRightScalePlot:
			if ev.Pressure == 0 {
				p.dragMode = dragNone
				break
			}
			if math.Hypot(float64(ev.X-p.dragStartX), float64(ev.Y-p.dragStartY)) >= 3 {
				p.rightDragged = true
			}
			if !p.rightDragged {
				return false
			}
			// PyQtGraph 招牌手感：右键水平向右拉放大 X（跨度指数缩小），向上拉放大 Y！
			dx := ev.X - p.dragLastX
			dy := ev.Y - p.dragLastY
			p.dragLastX, p.dragLastY = ev.X, ev.Y
			scaleX := math.Exp(-float64(dx) * 0.008)
			scaleY := math.Exp(float64(dy) * 0.008)
			anchLX := p.dragStartX - p.localPlotRect.X
			anchLY := p.dragStartY - p.localPlotRect.Y
			p.vb.ZoomAtAnchor(anchLX, anchLY, pw, ph, scaleX, scaleY, AxisBoth)
			return true
		case dragScaleAxis:
			if ev.Pressure == 0 {
				p.dragMode = dragNone
				break
			}
			if math.Hypot(float64(ev.X-p.dragStartX), float64(ev.Y-p.dragStartY)) >= 3 {
				p.rightDragged = true
			}
			if !p.rightDragged {
				return false
			}
			dx := ev.X - p.dragLastX
			dy := ev.Y - p.dragLastY
			p.dragLastX, p.dragLastY = ev.X, ev.Y
			scaleX := math.Exp(-float64(dx) * 0.008)
			scaleY := math.Exp(float64(dy) * 0.008)
			anchLX := p.dragStartX - p.localPlotRect.X
			anchLY := p.dragStartY - p.localPlotRect.Y
			p.vb.ZoomAtAnchor(anchLX, anchLY, pw, ph, scaleX, scaleY, p.dragAxisMask)
			return true
		}

		// 2. 空闲态悬停：通知 HoverObserver（Crosshair 等）并探测 InteractiveItem 光标形状
		needRedraw := false
		for _, it := range p.items {
			if it != nil && it.Visible() {
				if ho, ok := it.(HoverObserver); ok {
					if ho.OnHover(lx, ly, pw, ph, inPlot, p.vb) {
						needRedraw = true
					}
				}
			}
		}

		newCursor := ui.CursorCrosshair
		if (p.hasAutoBtn && rectContains(p.autoBtnRect, ev.X, ev.Y)) ||
			(p.hasLiveBtn && rectContains(p.liveBtnRect, ev.X, ev.Y)) {
			newCursor = ui.CursorPointer
			needRedraw = true
		} else if !inPlot {
			axMask := p.hitAxisMask(ev.X, ev.Y)
			switch axMask {
			case AxisX:
				newCursor = ui.CursorResizeEW
			case AxisY:
				newCursor = ui.CursorResizeNS
			default:
				newCursor = ui.CursorDefault
			}
		} else {
			// 按 Z 逆序（顶层优先）命中测试可交互图元
			for i := len(p.items) - 1; i >= 0; i-- {
				it := p.items[i]
				if it == nil || !it.Visible() {
					continue
				}
				if ii, ok := it.(InteractiveItem); ok {
					if hit, cur := ii.HitTest(lx, ly, pw, ph, p.vb); hit {
						if cur != 0 {
							newCursor = cur
						} else {
							newCursor = ui.CursorPointer
						}
						break
					}
				}
			}
		}
		if newCursor != p.hoverCursor {
			p.hoverCursor = newCursor
			needRedraw = true
		}
		return needRedraw

	case ui.InputPointerDown:
		p.hoverX, p.hoverY = ev.X, ev.Y
		p.pointerIn = inPlot
		p.dragStartX, p.dragStartY = ev.X, ev.Y
		p.dragLastX, p.dragLastY = ev.X, ev.Y

		// 1. 左键点击左下角 [A] 自动量程复位角标或 [Live] 恢复实时流尾部跟随角标
		if ev.Button == 0 && p.hasAutoBtn && rectContains(p.autoBtnRect, ev.X, ev.Y) {
			p.AutoRange()
			return true
		}
		if ev.Button == 0 && p.hasLiveBtn && rectContains(p.liveBtnRect, ev.X, ev.Y) {
			p.ResumeLiveFollow()
			return true
		}
		if ev.Button == 1 {
			p.contextMenuX, p.contextMenuY = ev.X, ev.Y
		}

		// 2. 左键点击图例项切换对应曲线显隐
		if ev.Button == 0 && p.showLegend {
			for _, lh := range p.legendHits {
				if rectContains(lh.rect, ev.X, ev.Y) && lh.target != nil {
					lh.target.SetVisible(!lh.target.Visible())
					return true
				}
			}
		}

		// 3. 左键双击一键复位对应轴或全局自动量程
		if ev.Button == 0 && ev.Clicks >= 2 {
			// 允许顶层交互图元优先响应双击（例如 MultiChannelItem 单导联置顶独奏放大）
			if inPlot {
				for i := len(p.items) - 1; i >= 0; i-- {
					it := p.items[i]
					if it == nil || !it.Visible() {
						continue
					}
					if ii, ok := it.(InteractiveItem); ok {
						if ii.HandlePointer(ev, lx, ly, pw, ph, p.vb) {
							return true
						}
					}
				}
			}
			if axMask := p.hitAxisMask(ev.X, ev.Y); axMask != AxisNone {
				p.vb.EnableAutoRange(axMask)
				p.updateAutoRangeFromItems()
				return true
			}
			p.AutoRange()
			return true
		}

		// 4. 在坐标轴区域上拖拽：左键单轴平移，右键单轴缩放
		if !inPlot {
			if axMask := p.hitAxisMask(ev.X, ev.Y); axMask != AxisNone {
				p.dragAxisMask = axMask
				if ev.Button == 0 || ev.Button == 2 {
					p.dragMode = dragPanAxis
					return true
				}
				if ev.Button == 1 {
					p.dragMode = dragScaleAxis
					p.rightDragged = false
					return false
				}
			}
			return false
		}

		// 5. 绘图区内左键按下：顶层 InteractiveItem（游标手柄/ROI）优先捕获
		if ev.Button == 0 {
			for i := len(p.items) - 1; i >= 0; i-- {
				it := p.items[i]
				if it == nil || !it.Visible() {
					continue
				}
				if ii, ok := it.(InteractiveItem); ok {
					if hit, _ := ii.HitTest(lx, ly, pw, ph, p.vb); hit {
						if ii.HandlePointer(ev, lx, ly, pw, ph, p.vb) {
							p.dragMode = dragItem
							p.activeItem = ii
							return true
						}
					}
				}
			}
			mods := ev.Mods | p.lastMods
			if p.vb.MouseMode() == RectMode || mods&ui.Shift != 0 {
				p.dragMode = dragRectZoom
			} else {
				p.dragMode = dragPanPlot
			}
			return true
		}

		// 6. 中键按下：无论处于 PanMode 还是 RectMode 始终强制全向平移
		if ev.Button == 2 {
			p.dragMode = dragPanPlot
			return true
		}

		// 7. 右键按下：记录起始锚点用于右键拖拽非等比缩放，同时返回 false 允许未拖拽时松开弹出原生 ContextMenu
		if ev.Button == 1 {
			p.dragMode = dragRightScalePlot
			p.rightDragged = false
			return false
		}

	case ui.InputPointerUp:
		mode := p.dragMode
		p.dragMode = dragNone
		switch mode {
		case dragItem:
			if p.activeItem != nil {
				ii := p.activeItem
				p.activeItem = nil
				if !ev.Canceled {
					ii.HandlePointer(ev, lx, ly, pw, ph, p.vb)
				}
				return true
			}
		case dragRectZoom:
			if !ev.Canceled {
				p.vb.ZoomToLocalRect(
					p.dragStartX-p.localPlotRect.X,
					p.dragStartY-p.localPlotRect.Y,
					ev.X-p.localPlotRect.X,
					ev.Y-p.localPlotRect.Y,
					pw, ph,
				)
			}
			return true
		case dragPanPlot, dragPanAxis, dragRightScalePlot, dragScaleAxis:
			return true
		}

	case ui.InputScroll:
		mods := ev.Mods | p.lastMods
		evWithMods := ev
		evWithMods.Mods = mods
		// 1. 允许图元优先拦截特殊修饰键滚轮（如 MultiChannelItem 的 Alt+滚轮通道增益缩放）
		if inPlot {
			for i := len(p.items) - 1; i >= 0; i-- {
				it := p.items[i]
				if it == nil || !it.Visible() {
					continue
				}
				if wi, ok := it.(WheelInterceptor); ok {
					if wi.InterceptWheel(evWithMods, lx, ly, pw, ph, p.vb) {
						return true
					}
				}
			}
		}

		// 2. 计算缩放比例因子：DY < 0（滚轮向前推）为放大（factor < 1.0），DY > 0 为缩小
		delta := ev.DY
		if delta == 0 {
			delta = ev.DX
		}
		if delta == 0 {
			return false
		}
		stepSensitivity := 0.0022
		if !ev.Precise && math.Abs(float64(delta)) >= 10 {
			stepSensitivity = 0.0020
		}
		factor := math.Exp(float64(delta) * stepSensitivity)

		// 3. 判定受影响轴：位于单个坐标轴上方或按住 Ctrl（仅 X）/ Shift（仅 Y）时执行单轴缩放！
		mask := AxisBoth
		if axMask := p.hitAxisMask(ev.X, ev.Y); axMask != AxisNone {
			mask = axMask
		} else if mods&ui.Ctrl != 0 && mods&ui.Shift == 0 {
			mask = AxisX
		} else if mods&ui.Shift != 0 && mods&ui.Ctrl == 0 {
			mask = AxisY
		}

		anchLX := min(max(lx, 0), pw)
		anchLY := min(max(ly, 0), ph)
		p.vb.ZoomAtAnchor(anchLX, anchLY, pw, ph, factor, factor, mask)
		return true

	case ui.InputZoom:
		// 触控板双指捏合缩放：ev.Scale > 1 放大（视口跨度乘 1/ev.Scale）
		if ev.Scale <= 0 {
			return false
		}
		invScale := 1.0 / float64(ev.Scale)
		anchLX := min(max(lx, 0), pw)
		anchLY := min(max(ly, 0), ph)
		p.vb.ZoomAtAnchor(anchLX, anchLY, pw, ph, invScale, invScale, AxisBoth)
		return true
	}

	return false
}

func (p *Plot) hasActiveStreamItem() bool {
	for _, it := range p.items {
		if it == nil || !it.Visible() {
			continue
		}
		if si, ok := it.(StreamItem); ok {
			if _, active := si.StreamVersion(); active {
				return true
			}
		}
	}
	return false
}

func (p *Plot) buildContextMenu(m *ui.Menu) {
	if p.rightDragged {
		p.rightDragged = false
		return
	}
	if p.hasActiveStreamItem() && !p.vb.IsAutoX() {
		if m.Item("Resume Live Follow (X)").Chosen() {
			p.ResumeLiveFollow()
		}
	}
	if m.Item("Reset Auto Range (A)").Chosen() {
		p.AutoRange()
	}
	if p.vb.ZoomDepth() > 0 {
		if m.Item("Undo Zoom (Backspace)").Chosen() {
			p.vb.PopZoom()
		}
	}
	m.Separator()

	// 允许挂载的可视化图元根据右键点击位置注入上下文感知的专属参数调节菜单（如 EEG/ECG 导联与时基灵敏度、曲线降采样等）
	lx := p.contextMenuX - p.localPlotRect.X
	ly := p.contextMenuY - p.localPlotRect.Y
	pw := max(p.localPlotRect.W, 1)
	ph := max(p.localPlotRect.H, 1)
	addedItemMenus := false
	for _, it := range p.items {
		if it == nil || !it.Visible() {
			continue
		}
		if cmpItem, ok := it.(ContextMenuProvider); ok {
			cmpItem.BuildContextMenu(m, lx, ly, pw, ph, p.vb)
			addedItemMenus = true
		}
	}
	if addedItemMenus {
		m.Separator()
	}

	m.Submenu("Mouse Mode", func(sub *ui.Menu) {
		if sub.Item("Pan Mode (3-Button)").Radio(p.vb.MouseMode() == PanMode).Chosen() {
			p.vb.SetMouseMode(PanMode)
		}
		if sub.Item("Rect Zoom Mode").Radio(p.vb.MouseMode() == RectMode).Chosen() {
			p.vb.SetMouseMode(RectMode)
		}
	})

	m.Submenu("Axes & Scale", func(sub *ui.Menu) {
		if sub.Item("Log X Scale").Checked(p.vb.LogX()).Chosen() {
			next := !p.vb.LogX()
			p.bottomAxis.SetLogMode(next)
			p.vb.SetLogX(next)
		}
		if sub.Item("Log Y Scale").Checked(p.vb.LogY()).Chosen() {
			next := !p.vb.LogY()
			p.leftAxis.SetLogMode(next)
			p.vb.SetLogY(next)
		}
		if sub.Item("Invert Y Axis").Checked(p.vb.InvertY()).Chosen() {
			p.vb.SetInvertY(!p.vb.InvertY())
		}
		if sub.Item("Lock Aspect 1:1").Checked(p.vb.AspectLocked()).Chosen() {
			p.vb.SetAspectLocked(!p.vb.AspectLocked(), 1.0)
		}
	})

	m.Submenu("Grid & Legend", func(sub *ui.Menu) {
		if sub.Item("Major Grid").Checked(p.showMajorGrid).Chosen() {
			p.showMajorGrid = !p.showMajorGrid
			p.bottomAxis.SetGrid(p.showMajorGrid, p.showMinorGrid)
			p.leftAxis.SetGrid(p.showMajorGrid, p.showMinorGrid)
		}
		if sub.Item("Minor Grid").Checked(p.showMinorGrid).Chosen() {
			p.showMinorGrid = !p.showMinorGrid
			p.bottomAxis.SetGrid(p.showMajorGrid, p.showMinorGrid)
			p.leftAxis.SetGrid(p.showMajorGrid, p.showMinorGrid)
		}
		if sub.Item("Show Legend").Checked(p.showLegend).Chosen() {
			p.showLegend = !p.showLegend
		}
	})

	// 剪贴板导出当前视口 CSV 与高清离屏 PNG 导出
	m.Separator()
	if m.Item("Copy Viewport CSV").Chosen() {
		csvText := p.buildViewportCSVString()
		if csvText != "" {
			p.lastCSVExport = csvText
			p.servicesValue().WriteClipboard(csvText)
		}
	}
	if m.Item("Export High-DPI PNG (2x)").Chosen() {
		w := max(int(p.elemW), 800)
		h := max(int(p.elemH), 480)
		path := p.exportPath
		if path == "" {
			path = "mygograph_export_2x.png"
		}
		err := p.SavePNG(path, w, h, 2.0)
		if p.exportHook != nil {
			p.exportHook(path, err)
		}
	}

	if p.extraMenu != nil {
		m.Separator()
		p.extraMenu(m)
	}
}

func (p *Plot) buildViewportCSVString() string {
	return FormatViewportCSV(p)
}

func offsetRect(r ui.Rect, dx, dy float32) ui.Rect {
	return ui.Rect{X: r.X + dx, Y: r.Y + dy, W: r.W, H: r.H}
}

func rectContains(r ui.Rect, x, y float32) bool {
	return x >= r.X && x <= r.X+r.W && y >= r.Y && y <= r.Y+r.H
}
