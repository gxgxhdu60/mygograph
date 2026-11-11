package items

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
)

// LineOption 配置 InfiniteLine 的函数式选项。
type LineOption func(*InfiniteLine)

// WithLineColor 设置无限参考线颜色。
func WithLineColor(col ui.Color) LineOption {
	return func(l *InfiniteLine) { l.color = ui.RGB(col.R, col.G, col.B) }
}

// WithLineWidth 设置线宽（DIP）。
func WithLineWidth(w float32) LineOption {
	return func(l *InfiniteLine) {
		if w > 0 {
			l.width = w
		}
	}
}

// WithMovable 设置参考线是否允许鼠标拖拽移动。
func WithMovable(movable bool) LineOption {
	return func(l *InfiniteLine) { l.movable = movable }
}

// WithDashed 设置是否以虚线样式绘制。
func WithDashed(dashed bool) LineOption {
	return func(l *InfiniteLine) { l.dashed = dashed }
}

// WithLineLabel 设置边缘读数角标前缀（如 "T1", "Trig"）。
func WithLineLabel(prefix string) LineOption {
	return func(l *InfiniteLine) { l.labelPrefix = prefix }
}

// WithShowTag 控制是否在坐标轴边缘显示实时物理数值标签（默认开启）。
func WithShowTag(show bool) LineOption {
	return func(l *InfiniteLine) { l.showTag = show }
}

// WithOnLineMoved 注册拖拽位置变更回调（垂直线传 X，水平线传 Y）。
func WithOnLineMoved(fn func(pos float64)) LineOption {
	return func(l *InfiniteLine) { l.onMoved = fn }
}

// InfiniteLine 表示贯穿整个视口的垂直、水平或任意倾角无限参考标尺（不参与 AutoRange 包围盒计算）。
type InfiniteLine struct {
	posX        float64
	posY        float64
	angleDeg    float64 // 90 = 垂直线 X=posX；0 = 水平线 Y=posY；其它 = 过 (posX,posY) 倾角线
	movable     bool
	dashed      bool
	showTag     bool
	labelPrefix string
	color       ui.Color
	width       float32
	visible     bool
	z           float32
	dragging    bool
	onMoved     func(pos float64)

	dashPath *ui.Path
}

// NewVLine 创建位于 X = pos 的垂直参考游标线（默认可拖拽并显示底部数值标签）。
func NewVLine(pos float64, opts ...LineOption) *InfiniteLine {
	l := &InfiniteLine{
		posX:     pos,
		angleDeg: 90,
		movable:  true,
		dashed:   false,
		showTag:  true,
		color:    ui.RGB(255, 196, 61),
		width:    1.25,
		visible:  true,
		z:        10,
		dashPath: &ui.Path{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(l)
		}
	}
	return l
}

// NewHLine 创建位于 Y = pos 的水平阈值触发线（默认可拖拽并显示左侧数值标签）。
func NewHLine(pos float64, opts ...LineOption) *InfiniteLine {
	l := &InfiniteLine{
		posY:     pos,
		angleDeg: 0,
		movable:  true,
		dashed:   true,
		showTag:  true,
		color:    ui.RGB(255, 196, 61),
		width:    1.25,
		visible:  true,
		z:        10,
		dashPath: &ui.Path{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(l)
		}
	}
	return l
}

// NewAngledLine 创建穿过物理锚点 (posX, posY)、数据空间倾角为 angleDeg 度的斜率标尺线。
func NewAngledLine(posX, posY, angleDeg float64, opts ...LineOption) *InfiniteLine {
	l := NewVLine(posX, opts...)
	l.posY = posY
	l.angleDeg = angleDeg
	return l
}

func (l *InfiniteLine) isVertical() bool {
	return math.Abs(math.Mod(math.Abs(l.angleDeg), 180)-90) < 1e-4
}

func (l *InfiniteLine) isHorizontal() bool {
	m := math.Mod(math.Abs(l.angleDeg), 180)
	return m < 1e-4 || math.Abs(m-180) < 1e-4
}

// Pos 返回垂直线的 X 或水平线的 Y 坐标。
func (l *InfiniteLine) Pos() float64 {
	if l.isVertical() {
		return l.posX
	}
	return l.posY
}

// SetPos 更新垂直线 X 或水平线 Y 坐标。
func (l *InfiniteLine) SetPos(pos float64) {
	if l.isVertical() {
		l.posX = pos
	} else {
		l.posY = pos
	}
	if l.onMoved != nil {
		l.onMoved(pos)
	}
}

func (l *InfiniteLine) Bounds() (data.Range, data.Range, bool) {
	// 无限参考标尺绝不参与 AutoRange，防止游标把视口撑坏
	return data.Range{}, data.Range{}, false
}

func (l *InfiniteLine) Visible() bool     { return l.visible }
func (l *InfiniteLine) SetVisible(v bool) { l.visible = v }
func (l *InfiniteLine) ZValue() float32   { return l.z }

const lineHitRadiusDIP float32 = 6.0

func (l *InfiniteLine) HitTest(lx, ly, plotW, plotH float32, vb *mygograph.ViewBox) (bool, ui.Cursor) {
	if !l.visible || !l.movable {
		return false, 0
	}
	localVt := vb.Transform(ui.Rect{X: 0, Y: 0, W: plotW, H: plotH})
	if l.isVertical() {
		sx := localVt.DataToScreenX(l.posX)
		if float32(math.Abs(float64(lx-sx))) <= lineHitRadiusDIP {
			return true, ui.CursorResizeEW
		}
		return false, 0
	}
	if l.isHorizontal() {
		sy := localVt.DataToScreenY(l.posY)
		if float32(math.Abs(float64(ly-sy))) <= lineHitRadiusDIP {
			return true, ui.CursorResizeNS
		}
		return false, 0
	}
	sx0, sy0, sx1, sy1, ok := l.clippedSegment(localVt, ui.Rect{W: plotW, H: plotH})
	if !ok {
		return false, 0
	}
	if distPointToSegment(lx, ly, sx0, sy0, sx1, sy1) <= lineHitRadiusDIP {
		return true, ui.CursorMove
	}
	return false, 0
}

func (l *InfiniteLine) HandlePointer(ev ui.InputEvent, lx, ly, plotW, plotH float32, vb *mygograph.ViewBox) bool {
	if !l.movable {
		return false
	}
	switch ev.Kind {
	case ui.InputPointerDown:
		if ev.Button == 0 {
			l.dragging = true
			return true
		}
	case ui.InputPointerMove:
		if !l.dragging {
			return false
		}
		clampedX := min(max(lx, 0), plotW)
		clampedY := min(max(ly, 0), plotH)
		dx, dy := vb.LocalToData(clampedX, clampedY, plotW, plotH)
		if l.isVertical() {
			l.posX = dx
			if l.onMoved != nil {
				l.onMoved(dx)
			}
		} else if l.isHorizontal() {
			l.posY = dy
			if l.onMoved != nil {
				l.onMoved(dy)
			}
		} else {
			l.posX, l.posY = dx, dy
			if l.onMoved != nil {
				l.onMoved(dx)
			}
		}
		return true
	case ui.InputPointerUp:
		if l.dragging {
			l.dragging = false
			return true
		}
	}
	return false
}

func (l *InfiniteLine) Draw(ctx *mygograph.RenderContext) {
	if !l.visible {
		return
	}
	pr := ctx.PlotRect
	col := l.color
	w := l.width
	if l.dragging {
		w += 0.75
	}

	if l.isVertical() {
		sx := ctx.Transform.DataToScreenX(l.posX)
		if !(sx >= pr.X && sx <= pr.X+pr.W) {
			return
		}
		l.strokeLine(ctx.Painter, sx, pr.Y, sx, pr.Y+pr.H, w, col)
		if l.movable {
			// 顶部示波器风格小三角拖拽握柄
			l.drawTopMarker(ctx.Painter, sx, pr.Y, col)
		}
		return
	}

	if l.isHorizontal() {
		sy := ctx.Transform.DataToScreenY(l.posY)
		if !(sy >= pr.Y && sy <= pr.Y+pr.H) {
			return
		}
		l.strokeLine(ctx.Painter, pr.X, sy, pr.X+pr.W, sy, w, col)
		return
	}

	x0, y0, x1, y1, ok := l.clippedSegment(ctx.Transform, pr)
	if ok {
		l.strokeLine(ctx.Painter, x0, y0, x1, y1, w, col)
	}
}

// DrawOverlay 实现 mygograph.OverlayItem：在坐标轴脊线上绘制高对比度游标读数胶囊签。
func (l *InfiniteLine) DrawOverlay(ctx *mygograph.RenderContext) {
	if !l.visible || !l.showTag {
		return
	}
	pr := ctx.PlotRect
	tf := ui.Font{Family: ctx.Theme.FontFamily, Size: 10.5, Weight: 600, Features: "tnum"}
	tm := tf.Metrics()

	if l.isVertical() {
		sx := ctx.Transform.DataToScreenX(l.posX)
		if !(sx >= pr.X && sx <= pr.X+pr.W) {
			return
		}
		txt := ctx.FormatX(l.posX)
		if l.labelPrefix != "" {
			txt = l.labelPrefix + ": " + txt
		}
		gs, tw := ctx.ShapeText(txt, tf)
		bw := tw + 10
		bh := tm.Ascent + tm.Descent + 5
		bx := min(max(sx-bw*0.5, pr.X), pr.X+pr.W-bw)
		by := pr.Y + pr.H + 2
		drawTagBadge(ctx.Painter, ui.Rect{X: bx, Y: by, W: bw, H: bh}, gs, bx+5, by+(bh+tm.Ascent-tm.Descent)*0.5, l.color)
		return
	}

	if l.isHorizontal() {
		sy := ctx.Transform.DataToScreenY(l.posY)
		if !(sy >= pr.Y && sy <= pr.Y+pr.H) {
			return
		}
		txt := ctx.FormatY(l.posY)
		if l.labelPrefix != "" {
			txt = l.labelPrefix + ": " + txt
		}
		gs, tw := ctx.ShapeText(txt, tf)
		bw := tw + 10
		bh := tm.Ascent + tm.Descent + 5
		bx := max(pr.X-bw-3, 2)
		by := min(max(sy-bh*0.5, pr.Y), pr.Y+pr.H-bh)
		drawTagBadge(ctx.Painter, ui.Rect{X: bx, Y: by, W: bw, H: bh}, gs, bx+5, by+(bh+tm.Ascent-tm.Descent)*0.5, l.color)
	}
}

func (l *InfiniteLine) strokeLine(p *ui.Painter, x0, y0, x1, y1, width float32, col ui.Color) {
	if !l.dashed {
		if x0 == x1 || y0 == y1 {
			p.Line(x0, y0, x1, y1, width, col)
		} else {
			pts := [2][2]float32{{x0, y0}, {x1, y1}}
			p.StrokePolyline(pts[:], width, col)
		}
		return
	}
	l.dashPath.Reset()
	l.dashPath.MoveTo(x0, y0).LineTo(x1, y1).Dash(0, 5, 4)
	p.StrokePath(l.dashPath, width, col)
}

func (l *InfiniteLine) drawTopMarker(p *ui.Painter, sx, topY float32, col ui.Color) {
	l.dashPath.Reset()
	l.dashPath.MoveTo(sx-5, topY).LineTo(sx+5, topY).LineTo(sx, topY+7).Close()
	p.FillPath(l.dashPath, col)
}

func (l *InfiniteLine) clippedSegment(vt data.ViewportTransform, pr ui.Rect) (x0, y0, x1, y1 float32, ok bool) {
	rad := l.angleDeg * math.Pi / 180.0
	slope := math.Tan(rad)
	xLo, xHi := vt.VisibleLinearXRange()
	ya := l.posY + slope*(xLo-l.posX)
	yb := l.posY + slope*(xHi-l.posX)
	x0, y0 = vt.DataToScreenX(xLo), vt.DataToScreenY(ya)
	x1, y1 = vt.DataToScreenX(xHi), vt.DataToScreenY(yb)
	if isNaN32(x0) || isNaN32(y0) || isNaN32(x1) || isNaN32(y1) {
		return 0, 0, 0, 0, false
	}
	_ = pr
	return x0, y0, x1, y1, true
}

func drawTagBadge(p *ui.Painter, box ui.Rect, gs []ui.Glyph, gx, gy float32, bg ui.Color) {
	p.Fill(box, bg, 3)
	// 根据背景明度自适应深/浅文字色，确保高对比度可读
	luma := 0.299*float32(bg.R) + 0.587*float32(bg.G) + 0.114*float32(bg.B)
	fg := ui.RGB(10, 14, 20)
	if luma < 135 {
		fg = ui.RGB(248, 250, 252)
	}
	p.Glyphs(gs, gx, gy, fg)
}

// CursorStats 保存双游标区间 [X1, X2] 内的精密工程测量结果。
type CursorStats struct {
	X1, X2     float64
	DeltaX     float64 // X2 - X1
	FreqHz     float64 // 1 / |DeltaX|（DeltaX != 0 时）
	Y1, Y2     float64 // 水平游标或曲线在 X1/X2 处的插值幅值
	DeltaY     float64 // Y2 - Y1
	HasWaveRMS bool
	Vpp        float64 // 区间峰峰值 Max - Min
	Mean       float64 // 区间直流均值
	RMS        float64 // 区间真有效值 sqrt(mean(y^2))
}

// CursorMeasureOption 配置示波器双游标测量组件。
type CursorMeasureOption func(*CursorMeasureItem)

// WithMeasureSource 绑定待测波形数据源以实时解算区间 Vpp、Mean 与真有效值 RMS。
func WithMeasureSource(src data.XYSource) CursorMeasureOption {
	return func(cm *CursorMeasureItem) { cm.src = src }
}

// WithHorizontalCursors 同时开启两条水平电压测量游标 V1 / V2。
func WithHorizontalCursors(y1, y2 float64) CursorMeasureOption {
	return func(cm *CursorMeasureItem) {
		cm.enableH = true
		cm.h1 = NewHLine(y1, WithLineLabel("V1"), WithLineColor(ui.RGB(88, 214, 141)))
		cm.h2 = NewHLine(y2, WithLineLabel("V2"), WithLineColor(ui.RGB(175, 122, 255)))
	}
}

// CursorMeasureItem 封装示波器级双游标差值测量套件（T1/T2 + 可选 V1/V2 + Δt / 1/Δt / ΔV / Vpp / RMS 读数面板）。
type CursorMeasureItem struct {
	v1      *InfiniteLine
	v2      *InfiniteLine
	enableH bool
	h1      *InfiniteLine
	h2      *InfiniteLine
	src     data.XYSource
	visible bool
	z       float32

	activeSub *InfiniteLine
}

// NewCursorMeasure 创建位于 x1, x2 的双垂直时间游标测量仪。
func NewCursorMeasure(x1, x2 float64, opts ...CursorMeasureOption) *CursorMeasureItem {
	cm := &CursorMeasureItem{
		v1:      NewVLine(x1, WithLineLabel("T1"), WithLineColor(ui.RGB(255, 196, 61)), WithDashed(true)),
		v2:      NewVLine(x2, WithLineLabel("T2"), WithLineColor(ui.RGB(0, 229, 255)), WithDashed(true)),
		visible: true,
		z:       12,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(cm)
		}
	}
	return cm
}

// SetXPositions 程序化设置 T1 与 T2 游标位置。
func (cm *CursorMeasureItem) SetXPositions(x1, x2 float64) {
	cm.v1.SetPos(x1)
	cm.v2.SetPos(x2)
}

// XPositions 返回当前 T1 与 T2 物理位置。
func (cm *CursorMeasureItem) XPositions() (x1, x2 float64) {
	return cm.v1.Pos(), cm.v2.Pos()
}

// Stats 实时计算当前游标差值与绑定波形在 [min(T1,T2), max(T1,T2)] 内的统计特征。
func (cm *CursorMeasureItem) Stats() CursorStats {
	x1, x2 := cm.v1.Pos(), cm.v2.Pos()
	dx := x2 - x1
	var freq float64
	if math.Abs(dx) > 1e-18 {
		freq = 1.0 / math.Abs(dx)
	}

	st := CursorStats{
		X1:     x1,
		X2:     x2,
		DeltaX: dx,
		FreqHz: freq,
	}
	if cm.enableH && cm.h1 != nil && cm.h2 != nil {
		st.Y1 = cm.h1.Pos()
		st.Y2 = cm.h2.Pos()
		st.DeltaY = st.Y2 - st.Y1
	}

	if cm.src != nil && cm.src.Len() > 0 && cm.src.MonotonicX() {
		if !cm.enableH {
			st.Y1 = sampleAtX(cm.src, x1)
			st.Y2 = sampleAtX(cm.src, x2)
			st.DeltaY = st.Y2 - st.Y1
		}
		lo, hi := min(x1, x2), max(x1, x2)
		i0 := max(data.LowerBoundX(cm.src, lo), 0)
		i1 := min(data.UpperBoundX(cm.src, hi), cm.src.Len())
		if i1 > i0 {
			// 大跨度区间最多均匀抽样 65,536 点计算实时 Vpp/RMS，保证帧内 < 0.05 ms
			stride := max((i1-i0)/65536, 1)
			yMin, yMax := math.Inf(1), math.Inf(-1)
			var sum, sumSq float64
			cnt := 0
			for i := i0; i < i1; i += stride {
				_, y := cm.src.XY(i)
				if math.IsNaN(y) || math.IsInf(y, 0) {
					continue
				}
				yMin = min(yMin, y)
				yMax = max(yMax, y)
				sum += y
				sumSq += y * y
				cnt++
			}
			// 同时确保全精度峰谷不被步进漏检：若为金字塔或 UniformSlice，用 Bounds 思想精确补充
			if cnt > 0 {
				st.HasWaveRMS = true
				st.Vpp = yMax - yMin
				st.Mean = sum / float64(cnt)
				st.RMS = math.Sqrt(sumSq / float64(cnt))
			}
		}
	}
	return st
}

func sampleAtX(src data.XYSource, targetX float64) float64 {
	n := src.Len()
	if n == 0 {
		return 0
	}
	idx := data.LowerBoundX(src, targetX)
	if idx <= 0 {
		_, y := src.XY(0)
		return y
	}
	if idx >= n {
		_, y := src.XY(n - 1)
		return y
	}
	x0, y0 := src.XY(idx - 1)
	x1, y1 := src.XY(idx)
	if x1 == x0 {
		return y0
	}
	t := (targetX - x0) / (x1 - x0)
	return y0 + t*(y1-y0)
}

func (cm *CursorMeasureItem) Bounds() (data.Range, data.Range, bool) {
	return data.Range{}, data.Range{}, false
}

func (cm *CursorMeasureItem) Visible() bool     { return cm.visible }
func (cm *CursorMeasureItem) SetVisible(v bool) { cm.visible = v }
func (cm *CursorMeasureItem) ZValue() float32   { return cm.z }

func (cm *CursorMeasureItem) subLines() []*InfiniteLine {
	if cm.enableH && cm.h1 != nil && cm.h2 != nil {
		return []*InfiniteLine{cm.v1, cm.v2, cm.h1, cm.h2}
	}
	return []*InfiniteLine{cm.v1, cm.v2}
}

func (cm *CursorMeasureItem) HitTest(lx, ly, plotW, plotH float32, vb *mygograph.ViewBox) (bool, ui.Cursor) {
	if !cm.visible {
		return false, 0
	}
	for _, sub := range cm.subLines() {
		if hit, cur := sub.HitTest(lx, ly, plotW, plotH, vb); hit {
			return true, cur
		}
	}
	return false, 0
}

func (cm *CursorMeasureItem) HandlePointer(ev ui.InputEvent, lx, ly, plotW, plotH float32, vb *mygograph.ViewBox) bool {
	if !cm.visible {
		return false
	}
	switch ev.Kind {
	case ui.InputPointerDown:
		for _, sub := range cm.subLines() {
			if hit, _ := sub.HitTest(lx, ly, plotW, plotH, vb); hit {
				if sub.HandlePointer(ev, lx, ly, plotW, plotH, vb) {
					cm.activeSub = sub
					return true
				}
			}
		}
	case ui.InputPointerMove:
		if cm.activeSub != nil {
			return cm.activeSub.HandlePointer(ev, lx, ly, plotW, plotH, vb)
		}
	case ui.InputPointerUp:
		if cm.activeSub != nil {
			res := cm.activeSub.HandlePointer(ev, lx, ly, plotW, plotH, vb)
			cm.activeSub = nil
			return res
		}
	}
	return false
}

func (cm *CursorMeasureItem) Draw(ctx *mygograph.RenderContext) {
	if !cm.visible {
		return
	}
	pr := ctx.PlotRect
	sx1 := ctx.Transform.DataToScreenX(cm.v1.Pos())
	sx2 := ctx.Transform.DataToScreenX(cm.v2.Pos())
	leftX := max(min(sx1, sx2), pr.X)
	rightX := min(max(sx1, sx2), pr.X+pr.W)
	if rightX > leftX {
		ctx.Painter.Fill(ui.Rect{X: leftX, Y: pr.Y, W: rightX - leftX, H: pr.H}, ui.RGBA(0, 212, 255, 16), 0)
	}
	for _, sub := range cm.subLines() {
		sub.Draw(ctx)
	}
}

func (cm *CursorMeasureItem) DrawOverlay(ctx *mygograph.RenderContext) {
	if !cm.visible {
		return
	}
	for _, sub := range cm.subLines() {
		sub.DrawOverlay(ctx)
	}

	st := cm.Stats()
	var lines [4]string
	nLines := 0
	lines[nLines] = fmt.Sprintf("ΔT: %s   1/|ΔT|: %s",
		mygograph.FormatSIValue(st.DeltaX, 3, "s"),
		mygograph.FormatSIValue(st.FreqHz, 3, "Hz"),
	)
	nLines++

	if cm.enableH || cm.src != nil {
		lines[nLines] = fmt.Sprintf("Y1: %s   Y2: %s   ΔY: %s",
			ctx.FormatY(st.Y1),
			ctx.FormatY(st.Y2),
			ctx.FormatY(st.DeltaY),
		)
		nLines++
	}
	if st.HasWaveRMS {
		lines[nLines] = fmt.Sprintf("Vpp: %s   RMS: %s",
			ctx.FormatY(st.Vpp),
			ctx.FormatY(st.RMS),
		)
		nLines++
	}

	drawInfoCard(ctx, ctx.PlotRect.X+10, ctx.PlotRect.Y+10, lines[:nLines])
}

// CrosshairOption 配置十字准星组件。
type CrosshairOption func(*Crosshair)

// WithSnap 控制十字准星是否自动吸附最近曲线/散点/热力图真实采样点（默认开启）。
func WithSnap(enable bool) CrosshairOption {
	return func(ch *Crosshair) { ch.snap = enable }
}

// WithSnapTarget 显式绑定唯一吸附目标（不指定时自动搜索 Plot 中所有可见 SnapTarget）。
func WithSnapTarget(target mygograph.SnapTarget) CrosshairOption {
	return func(ch *Crosshair) { ch.target = target }
}

// Crosshair 实现跟随鼠标的示波器级十字准星，支持真实数据点就近吸附与四象限防出界读数浮窗。
type Crosshair struct {
	snap    bool
	target  mygograph.SnapTarget
	visible bool
	z       float32

	inside  bool
	hoverLX float32
	hoverLY float32
	dashP   *ui.Path
}

// NewCrosshair 创建默认开启数据点自动吸附的十字准星。
func NewCrosshair(opts ...CrosshairOption) *Crosshair {
	ch := &Crosshair{
		snap:    true,
		visible: true,
		z:       20,
		dashP:   &ui.Path{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(ch)
		}
	}
	return ch
}

func (ch *Crosshair) Bounds() (data.Range, data.Range, bool) {
	return data.Range{}, data.Range{}, false
}

func (ch *Crosshair) Visible() bool     { return ch.visible }
func (ch *Crosshair) SetVisible(v bool) { ch.visible = v }
func (ch *Crosshair) ZValue() float32   { return ch.z }

// OnHover 实现 mygograph.HoverObserver：被动追踪指针且绝不拦截平移/缩放拖拽事件。
func (ch *Crosshair) OnHover(lx, ly, _, _ float32, inside bool, _ *mygograph.ViewBox) bool {
	if !ch.visible {
		return false
	}
	if ch.inside == inside && (!inside || (ch.hoverLX == lx && ch.hoverLY == ly)) {
		return false
	}
	ch.inside = inside
	ch.hoverLX = lx
	ch.hoverLY = ly
	return true
}

func (ch *Crosshair) resolvePoint(ctx *mygograph.RenderContext) (sx, sy float32, dx, dy float64, label string, snapped bool) {
	pr := ctx.PlotRect
	sx = pr.X + ch.hoverLX
	sy = pr.Y + ch.hoverLY
	dx, dy = ctx.View.LocalToData(ch.hoverLX, ch.hoverLY, pr.W, pr.H)

	if !ch.snap {
		return sx, sy, dx, dy, "", false
	}

	if ch.target != nil {
		if pt, ok := ch.target.SnapNearest(ch.hoverLX, ch.hoverLY, pr, ctx.View); ok {
			return pt.ScreenX, pt.ScreenY, pt.DataX, pt.DataY, pt.Label, true
		}
		return sx, sy, dx, dy, "", false
	}

	bestDist := math.Inf(1)
	var best mygograph.SnapPoint
	found := false
	for _, it := range ctx.Items() {
		if it == nil || !it.Visible() {
			continue
		}
		st, ok := it.(mygograph.SnapTarget)
		if !ok {
			continue
		}
		pt, ok := st.SnapNearest(ch.hoverLX, ch.hoverLY, pr, ctx.View)
		if !ok {
			continue
		}
		ddx := float64(pt.ScreenX - sx)
		ddy := float64(pt.ScreenY - sy)
		d2 := ddx*ddx + ddy*ddy
		if d2 < bestDist {
			bestDist = d2
			best = pt
			found = true
		}
	}
	if found {
		return best.ScreenX, best.ScreenY, best.DataX, best.DataY, best.Label, true
	}
	return sx, sy, dx, dy, "", false
}

func (ch *Crosshair) Draw(ctx *mygograph.RenderContext) {
	if !ch.visible || !ch.inside {
		return
	}
	pr := ctx.PlotRect
	sx, sy, dx, dy, label, snapped := ch.resolvePoint(ctx)
	if !(sx >= pr.X && sx <= pr.X+pr.W && sy >= pr.Y && sy <= pr.Y+pr.H) {
		// 若吸附点 Y 超出上下边界，准星线仍显示在指针或钳位位置
		sx = min(max(sx, pr.X), pr.X+pr.W)
		sy = min(max(sy, pr.Y), pr.Y+pr.H)
	}

	ch.dashP.Reset()
	ch.dashP.MoveTo(sx, pr.Y).LineTo(sx, pr.Y+pr.H)
	ch.dashP.MoveTo(pr.X, sy).LineTo(pr.X+pr.W, sy)
	ch.dashP.Dash(0, 4, 4)
	ctx.Painter.StrokePath(ch.dashP, 1.0, ctx.Theme.CrosshairLine)

	if snapped {
		ctx.Painter.Fill(ui.Rect{X: sx - 4, Y: sy - 4, W: 8, H: 8}, ctx.Theme.AccentColor, 4)
		ctx.Painter.Fill(ui.Rect{X: sx - 2, Y: sy - 2, W: 4, H: 4}, ctx.Theme.PlotBackground, 2)
	}

	// 组装读数文本并执行四象限智能避让布局（靠近右/下边缘时自动翻转到左/上方）
	var lines [3]string
	n := 0
	if label != "" {
		lines[n] = label
		n++
	}
	lines[n] = "X: " + ctx.FormatX(dx)
	n++
	lines[n] = "Y: " + ctx.FormatY(dy)
	n++

	f := ui.Font{Family: ctx.Theme.FontFamily, Size: 11, Weight: 500, Features: "tnum"}
	fm := f.Metrics()
	rowH := fm.Ascent + fm.Descent + 3
	var maxW float32
	for i := range n {
		_, w := ctx.ShapeText(lines[i], f)
		if w > maxW {
			maxW = w
		}
	}
	boxW := maxW + 14
	boxH := float32(n)*rowH + 8

	bx := sx + 12
	by := sy - boxH - 10
	if bx+boxW > pr.X+pr.W-4 {
		bx = sx - boxW - 12
	}
	if by < pr.Y+4 {
		by = sy + 12
	}
	bx = max(bx, pr.X+4)
	by = min(by, pr.Y+pr.H-boxH-4)

	drawInfoCard(ctx, bx, by, lines[:n])
}

func drawInfoCard(ctx *mygograph.RenderContext, bx, by float32, lines []string) {
	if len(lines) == 0 {
		return
	}
	f := ui.Font{Family: ctx.Theme.FontFamily, Size: 11, Weight: 500, Features: "tnum"}
	fm := f.Metrics()
	rowH := fm.Ascent + fm.Descent + 3
	var maxW float32
	for _, s := range lines {
		_, w := ctx.ShapeText(s, f)
		if w > maxW {
			maxW = w
		}
	}
	card := ui.Rect{
		X: bx,
		Y: by,
		W: maxW + 14,
		H: float32(len(lines))*rowH + 8,
	}
	ctx.Painter.Fill(card, ctx.Theme.ReadoutBg, 4)
	ctx.Painter.Stroke(card, ctx.Theme.ReadoutBorder, 4, 1)
	for i, s := range lines {
		gs, _ := ctx.ShapeText(s, f)
		gy := card.Y + 5 + float32(i)*rowH + fm.Ascent
		ctx.Painter.Glyphs(gs, card.X+7, gy, ctx.Theme.ReadoutText)
	}
}

func distPointToSegment(px, py, x0, y0, x1, y1 float32) float32 {
	vx, vy := x1-x0, y1-y0
	wx, wy := px-x0, py-y0
	c1 := wx*vx + wy*vy
	if c1 <= 0 {
		return float32(math.Hypot(float64(px-x0), float64(py-y0)))
	}
	c2 := vx*vx + vy*vy
	if c2 <= c1 {
		return float32(math.Hypot(float64(px-x1), float64(py-y1)))
	}
	b := c1 / c2
	bx, by := x0+b*vx, y0+b*vy
	return float32(math.Hypot(float64(px-bx), float64(py-by)))
}
