package mygograph

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
)

// AxisOrientation 定义坐标轴在绘图区四边的方位。
type AxisOrientation uint8

const (
	AxisBottom AxisOrientation = iota
	AxisLeft
	AxisTop
	AxisRight
)

// IsHorizontal 返回坐标轴是否为水平轴（Bottom / Top）。
func (o AxisOrientation) IsHorizontal() bool {
	return o == AxisBottom || o == AxisTop
}

type shapedEntry struct {
	glyphs []ui.Glyph
	width  float32
}

type glyphCacheKey struct {
	text     string
	fontSize float32
	bold     bool
}

// ChannelTick 描述多导联波形图在通道轴上的自定义命名刻度。
type ChannelTick struct {
	Pos   float64
	Label string
}

// Axis 负责刻度规划、SI 单位标题格式化、ui.Shape 字形序列缓存、防重叠剔除渲染与网格线位置提取。
type Axis struct {
	orient         AxisOrientation
	label          string
	unit           string
	scale          Scale
	visible        bool
	showGrid       bool
	showMinorGrid  bool
	fixedThickness float32 // > 0 时强制固定轴边距厚度（用于多子图像素级左/右边缘对齐）
	minThickness   float32 // 最小保留厚度，避免流式数值跳变引发水平抖动

	// 自定义固定通道刻度（nil 时由 Scale 自动生成）
	channelTicks []ChannelTick

	// 稳态零分配缓冲与字形缓存
	ticksBuf    []Tick
	visibleBuf  []Tick
	glyphCache  map[glyphCacheKey]shapedEntry
	lastUnitSfx string
}

const (
	majorTickLen   float32 = 6.0
	minorTickLen   float32 = 3.5
	tickLabelPad   float32 = 4.0
	axisTitlePad   float32 = 5.0
	axisOuterPad   float32 = 6.0
	minLabelGapX   float32 = 10.0
	minLabelGapY   float32 = 4.0
	maxGlyphCached         = 512
)

// NewAxis 创建指定方位的默认线性坐标轴。
func NewAxis(orient AxisOrientation) *Axis {
	return &Axis{
		orient:       orient,
		scale:        NewLinearScale(),
		visible:      true,
		showGrid:     true,
		ticksBuf:     make([]Tick, 0, 64),
		visibleBuf:   make([]Tick, 0, 64),
		glyphCache:   make(map[glyphCacheKey]shapedEntry, 64),
		minThickness: defaultMinThickness(orient),
	}
}

// NewSIAxis 创建带物理标签与 SI 国际单位自动词头换算的坐标轴（如 label="Amplitude", unit="V"）。
func NewSIAxis(orient AxisOrientation, label, unit string) *Axis {
	ax := NewAxis(orient)
	ax.label = label
	ax.unit = unit
	return ax
}

// NewLogAxis 创建以 10 为底的对数坐标轴（如频谱频率轴或功率谱幅值轴）。
func NewLogAxis(orient AxisOrientation, label, unit string) *Axis {
	ax := NewAxis(orient)
	ax.label = label
	ax.unit = unit
	ax.scale = NewLogScale()
	return ax
}

// NewTimeAxis 创建自适应多尺度时间轴（跨越 ns/μs/ms/s/HH:MM:SS/YYYY-MM-DD）。
func NewTimeAxis(orient AxisOrientation, label string) *Axis {
	ax := NewAxis(orient)
	ax.label = label
	ax.scale = NewTimeScale()
	return ax
}

// NewChannelAxis 创建面向 64~256 导联脑电/逻辑分析仪的命名通道坐标轴。
// 默认第 i 个通道基线位于物理坐标 Y = float64(i)。
func NewChannelAxis(orient AxisOrientation, names []string) *Axis {
	ax := NewAxis(orient)
	ax.SetChannelNames(names)
	return ax
}

func defaultMinThickness(orient AxisOrientation) float32 {
	switch orient {
	case AxisLeft, AxisRight:
		return 54
	default:
		return 30
	}
}

// Orientation 返回坐标轴方位。
func (a *Axis) Orientation() AxisOrientation { return a.orient }

// SetOrientation 更新坐标轴方位。
func (a *Axis) SetOrientation(o AxisOrientation) *Axis {
	a.orient = o
	if a.minThickness == 0 {
		a.minThickness = defaultMinThickness(o)
	}
	return a
}

// Label 返回坐标轴主标题。
func (a *Axis) Label() string { return a.label }

// SetLabel 设置坐标轴主标题。
func (a *Axis) SetLabel(label string) *Axis {
	a.label = label
	return a
}

// Unit 返回坐标轴基础物理单位（如 "V", "Hz", "s"）。
func (a *Axis) Unit() string { return a.unit }

// SetUnit 设置物理单位。
func (a *Axis) SetUnit(unit string) *Axis {
	a.unit = unit
	return a
}

// Scale 返回当前比例尺实例。
func (a *Axis) Scale() Scale { return a.scale }

// SetScale 替换坐标轴比例尺（如切换线性/对数/时间比例尺）。
func (a *Axis) SetScale(sc Scale) *Axis {
	if sc != nil {
		a.scale = sc
	}
	return a
}

// Visible 返回坐标轴是否可见。
func (a *Axis) Visible() bool { return a.visible }

// SetVisible 控制坐标轴显隐。
func (a *Axis) SetVisible(v bool) *Axis {
	a.visible = v
	return a
}

// ShowGrid 返回是否向绘图区投射主刻度网格线。
func (a *Axis) ShowGrid() bool { return a.showGrid }

// SetGrid 配置主/次网格线开关。
func (a *Axis) SetGrid(major, minor bool) *Axis {
	a.showGrid = major
	a.showMinorGrid = minor
	return a
}

// ShowMinorGrid 返回是否启用次刻度细网格线。
func (a *Axis) ShowMinorGrid() bool { return a.showMinorGrid }

// FixedThickness 返回强制固定的轴厚度（<= 0 表示自适应测量）。
func (a *Axis) FixedThickness() float32 { return a.fixedThickness }

// SetFixedThickness 强制指定轴区域像素厚度（供 GraphLayout 多图轴对齐调用）。
func (a *Axis) SetFixedThickness(dip float32) *Axis {
	a.fixedThickness = dip
	return a
}

// SetMinThickness 设置最小自适应轴厚度，防止实时流数值位数变化造成绘图区轻微伸缩。
func (a *Axis) SetMinThickness(dip float32) *Axis {
	a.minThickness = dip
	return a
}

// SetChannelNames 将通道名列表按 Y = 0, 1, ..., N-1 绑定为通道刻度。
func (a *Axis) SetChannelNames(names []string) *Axis {
	a.channelTicks = a.channelTicks[:0]
	for i, name := range names {
		a.channelTicks = append(a.channelTicks, ChannelTick{Pos: float64(i), Label: name})
	}
	return a
}

// SetChannelTicks 自定义任意物理坐标位置的离散通道标签（传 nil 恢复连续数学比例尺）。
func (a *Axis) SetChannelTicks(ticks []ChannelTick) *Axis {
	if ticks == nil {
		a.channelTicks = nil
		return a
	}
	a.channelTicks = append(a.channelTicks[:0], ticks...)
	return a
}

// FormatCursorValue 利用当前坐标轴比例尺与单位格式化精确游标读数。
func (a *Axis) FormatCursorValue(val, span float64) string {
	if len(a.channelTicks) > 0 {
		idx := int(math.Round(val))
		if idx >= 0 && idx < len(a.channelTicks) && math.Abs(a.channelTicks[idx].Pos-val) < 0.45 {
			return a.channelTicks[idx].Label
		}
	}
	if a.scale == nil {
		return FormatSIValue(val, 4, a.unit)
	}
	return a.scale.FormatValue(val, span, a.unit)
}

func (a *Axis) computeTicks(vb *ViewBox, lengthDIP float32) ([]Tick, string) {
	var minV, maxV float64
	if a.orient.IsHorizontal() {
		minV, maxV = vb.XRange()
	} else {
		minV, maxV = vb.YRange()
	}

	if len(a.channelTicks) > 0 {
		out := a.ticksBuf[:0]
		lo, hi := min(minV, maxV), max(minV, maxV)
		span := hi - lo
		// 当导联极密（如 256 导联全览）时按像素间距自动整数步进抽稀标签，放大时自动显现全部导联名
		visCh := 0
		for _, ct := range a.channelTicks {
			if ct.Pos >= lo-0.5 && ct.Pos <= hi+0.5 {
				visCh++
			}
		}
		stride := 1
		if visCh > 0 && lengthDIP > 0 {
			maxLabels := max(int(lengthDIP/15.0), 1)
			if visCh > maxLabels {
				stride = niceChannelStride((visCh + maxLabels - 1) / maxLabels)
			}
		}
		_ = span
		for i, ct := range a.channelTicks {
			if ct.Pos < lo || ct.Pos > hi {
				continue
			}
			isMajor := i%stride == 0
			lbl := ""
			if isMajor {
				lbl = ct.Label
			}
			out = append(out, Tick{Value: ct.Pos, Label: lbl, Major: isMajor})
		}
		a.ticksBuf = out
		return out, a.unit
	}

	if a.scale == nil {
		return nil, a.unit
	}
	ticks, sfx := a.scale.AppendTicks(minV, maxV, lengthDIP, a.unit, a.ticksBuf)
	a.ticksBuf = ticks
	a.lastUnitSfx = sfx
	return ticks, sfx
}

func niceChannelStride(raw int) int {
	for _, s := range [...]int{1, 2, 4, 5, 8, 10, 16, 20, 25, 32, 50, 64, 100} {
		if raw <= s {
			return s
		}
	}
	return raw
}

func (a *Axis) fullTitle(unitSuffix string) string {
	if a.label == "" && unitSuffix == "" {
		return ""
	}
	if a.label == "" {
		return "(" + unitSuffix + ")"
	}
	if unitSuffix == "" {
		return a.label
	}
	return fmt.Sprintf("%s (%s)", a.label, unitSuffix)
}

func (a *Axis) shapeCached(text string, font ui.Font, bold bool) shapedEntry {
	if text == "" {
		return shapedEntry{}
	}
	key := glyphCacheKey{text: text, fontSize: font.Size, bold: bold}
	if entry, ok := a.glyphCache[key]; ok {
		return entry
	}
	if len(a.glyphCache) >= maxGlyphCached {
		clear(a.glyphCache)
	}
	glyphs := ui.Shape(text, font)
	var w float32
	if len(glyphs) > 0 {
		last := glyphs[len(glyphs)-1]
		w = last.X + last.Advance
	}
	entry := shapedEntry{glyphs: glyphs, width: w}
	a.glyphCache[key] = entry
	return entry
}

func tickFont(theme Theme) ui.Font {
	sz := theme.AxisFontSize
	if sz <= 0 {
		sz = 11
	}
	return ui.Font{
		Family:   theme.FontFamily,
		Size:     sz,
		Weight:   400,
		Features: "tnum",
	}
}

func titleFont(theme Theme) ui.Font {
	sz := theme.TitleFontSize
	if sz <= 0 {
		sz = 12
	}
	return ui.Font{
		Family: theme.FontFamily,
		Size:   sz,
		Weight: 500,
	}
}

// MeasureThickness 计算当前视口数值与标题所需的坐标轴区域外边距厚度（DIP）。
func (a *Axis) MeasureThickness(vb *ViewBox, lengthDIP float32, theme Theme) float32 {
	if !a.visible {
		return 0
	}
	if a.fixedThickness > 0 {
		return a.fixedThickness
	}

	tf := tickFont(theme)
	tm := tf.Metrics()
	lineH := tm.Ascent + tm.Descent

	ticks, unitSfx := a.computeTicks(vb, max(lengthDIP, 120))
	title := a.fullTitle(unitSfx)

	var titleExtra float32
	if title != "" {
		hf := titleFont(theme)
		hm := hf.Metrics()
		titleExtra = hm.Ascent + hm.Descent + axisTitlePad
	}

	if a.orient.IsHorizontal() {
		h := majorTickLen + tickLabelPad + lineH + titleExtra + axisOuterPad
		return max(float32(math.Ceil(float64(h))), a.minThickness)
	}

	var maxLabelW float32 = 24
	for i := range ticks {
		if ticks[i].Label == "" {
			continue
		}
		sh := a.shapeCached(ticks[i].Label, tf, false)
		if sh.width > maxLabelW {
			maxLabelW = sh.width
		}
	}
	w := majorTickLen + tickLabelPad + maxLabelW + titleExtra + axisOuterPad
	return max(float32(math.Ceil(float64(w))), a.minThickness)
}

// GridPositions 提取当前视口下供绘图区背景网格绘制的主/次刻度屏幕坐标（DIP，零分配追加至 dst）。
func (a *Axis) GridPositions(vb *ViewBox, plotRect ui.Rect, majorDst, minorDst []float32) (majors, minors []float32) {
	majors = majorDst[:0]
	minors = minorDst[:0]
	if !a.showGrid && !a.showMinorGrid {
		return majors, minors
	}
	vt := vb.Transform(plotRect)
	length := plotRect.H
	if a.orient.IsHorizontal() {
		length = plotRect.W
	}
	ticks, _ := a.computeTicks(vb, length)
	for i := range ticks {
		var pos float32
		if a.orient.IsHorizontal() {
			pos = vt.DataToScreenX(ticks[i].Value)
			if !(pos >= plotRect.X && pos <= plotRect.X+plotRect.W) {
				continue
			}
		} else {
			pos = vt.DataToScreenY(ticks[i].Value)
			if !(pos >= plotRect.Y && pos <= plotRect.Y+plotRect.H) {
				continue
			}
		}
		if ticks[i].Major {
			if a.showGrid {
				majors = append(majors, pos)
			}
		} else if a.showMinorGrid {
			minors = append(minors, pos)
		}
	}
	return majors, minors
}

// Draw 在 axisRect 区域内渲染轴脊线、主/次刻度线、防重叠字形标签与工程单位轴标题。
func (a *Axis) Draw(p *ui.Painter, axisRect, plotRect ui.Rect, vb *ViewBox, theme Theme) {
	if !a.visible || axisRect.W <= 0 || axisRect.H <= 0 {
		return
	}

	vt := vb.Transform(plotRect)
	tf := tickFont(theme)
	tm := tf.Metrics()

	length := plotRect.H
	if a.orient.IsHorizontal() {
		length = plotRect.W
	}
	ticks, unitSfx := a.computeTicks(vb, length)
	title := a.fullTitle(unitSfx)

	switch a.orient {
	case AxisBottom:
		spineY := plotRect.Y + plotRect.H
		p.Line(plotRect.X, spineY, plotRect.X+plotRect.W, spineY, 1, theme.AxisLine)

		lastRight := float32(-1e9)
		baseline := spineY + majorTickLen + tickLabelPad + tm.Ascent
		// 先画所有刻度线，再带碰撞剔除绘制主刻度标签
		for i := range ticks {
			x := vt.DataToScreenX(ticks[i].Value)
			if !(x >= plotRect.X-0.5 && x <= plotRect.X+plotRect.W+0.5) {
				continue
			}
			tLen := minorTickLen
			if ticks[i].Major {
				tLen = majorTickLen
			}
			p.Line(x, spineY, x, spineY+tLen, 1, theme.AxisLine)
			if ticks[i].Label == "" {
				continue
			}
			sh := a.shapeCached(ticks[i].Label, tf, false)
			lx := x - sh.width*0.5
			// 贴边防截断微调
			lx = max(lx, plotRect.X-sh.width*0.35)
			lx = min(lx, plotRect.X+plotRect.W-sh.width*0.65)
			if lx >= lastRight+minLabelGapX {
				p.Glyphs(sh.glyphs, lx, baseline, theme.AxisText)
				lastRight = lx + sh.width
			}
		}

		if title != "" {
			hf := titleFont(theme)
			hm := hf.Metrics()
			sh := a.shapeCached(title, hf, true)
			tx := plotRect.X + (plotRect.W-sh.width)*0.5
			ty := baseline + tm.Descent + axisTitlePad + hm.Ascent
			p.Glyphs(sh.glyphs, tx, ty, theme.TitleText)
		}

	case AxisTop:
		spineY := plotRect.Y
		p.Line(plotRect.X, spineY, plotRect.X+plotRect.W, spineY, 1, theme.AxisLine)

		lastRight := float32(-1e9)
		baseline := spineY - majorTickLen - tickLabelPad - tm.Descent
		for i := range ticks {
			x := vt.DataToScreenX(ticks[i].Value)
			if !(x >= plotRect.X-0.5 && x <= plotRect.X+plotRect.W+0.5) {
				continue
			}
			tLen := minorTickLen
			if ticks[i].Major {
				tLen = majorTickLen
			}
			p.Line(x, spineY-tLen, x, spineY, 1, theme.AxisLine)
			if ticks[i].Label == "" {
				continue
			}
			sh := a.shapeCached(ticks[i].Label, tf, false)
			lx := x - sh.width*0.5
			if lx >= lastRight+minLabelGapX {
				p.Glyphs(sh.glyphs, lx, baseline, theme.AxisText)
				lastRight = lx + sh.width
			}
		}

		if title != "" {
			hf := titleFont(theme)
			hm := hf.Metrics()
			sh := a.shapeCached(title, hf, true)
			tx := plotRect.X + (plotRect.W-sh.width)*0.5
			ty := baseline - tm.Ascent - axisTitlePad - hm.Descent
			p.Glyphs(sh.glyphs, tx, ty, theme.TitleText)
		}

	case AxisLeft:
		spineX := plotRect.X
		p.Line(spineX, plotRect.Y, spineX, plotRect.Y+plotRect.H, 1, theme.AxisLine)

		lineH := tm.Ascent + tm.Descent
		var placedYs [128]float32
		placedN := 0

		for i := range ticks {
			y := vt.DataToScreenY(ticks[i].Value)
			if !(y >= plotRect.Y-0.5 && y <= plotRect.Y+plotRect.H+0.5) {
				continue
			}
			tLen := minorTickLen
			if ticks[i].Major {
				tLen = majorTickLen
			}
			p.Line(spineX-tLen, y, spineX, y, 1, theme.AxisLine)
			if ticks[i].Label == "" {
				continue
			}
			if overlapsVertical(placedYs[:placedN], y, lineH+minLabelGapY) {
				continue
			}
			sh := a.shapeCached(ticks[i].Label, tf, false)
			lx := spineX - majorTickLen - tickLabelPad - sh.width
			ly := y + (tm.Ascent-tm.Descent)*0.5
			p.Glyphs(sh.glyphs, lx, ly, theme.AxisText)
			if placedN < len(placedYs) {
				placedYs[placedN] = y
				placedN++
			}
		}

		if title != "" {
			hf := titleFont(theme)
			hm := hf.Metrics()
			sh := a.shapeCached(title, hf, true)
			cx := axisRect.X + axisOuterPad + hm.Ascent*0.5
			cy := plotRect.Y + plotRect.H*0.5
			p.Transform(ui.Rotation(-90), cx, cy, func() {
				p.Glyphs(sh.glyphs, cx-sh.width*0.5, cy+(hm.Ascent-hm.Descent)*0.35, theme.TitleText)
			})
		}

	case AxisRight:
		spineX := plotRect.X + plotRect.W
		p.Line(spineX, plotRect.Y, spineX, plotRect.Y+plotRect.H, 1, theme.AxisLine)

		lineH := tm.Ascent + tm.Descent
		var placedYs [128]float32
		placedN := 0

		for i := range ticks {
			y := vt.DataToScreenY(ticks[i].Value)
			if !(y >= plotRect.Y-0.5 && y <= plotRect.Y+plotRect.H+0.5) {
				continue
			}
			tLen := minorTickLen
			if ticks[i].Major {
				tLen = majorTickLen
			}
			p.Line(spineX, y, spineX+tLen, y, 1, theme.AxisLine)
			if ticks[i].Label == "" {
				continue
			}
			if overlapsVertical(placedYs[:placedN], y, lineH+minLabelGapY) {
				continue
			}
			sh := a.shapeCached(ticks[i].Label, tf, false)
			lx := spineX + majorTickLen + tickLabelPad
			ly := y + (tm.Ascent-tm.Descent)*0.5
			p.Glyphs(sh.glyphs, lx, ly, theme.AxisText)
			if placedN < len(placedYs) {
				placedYs[placedN] = y
				placedN++
			}
		}

		if title != "" {
			hf := titleFont(theme)
			hm := hf.Metrics()
			sh := a.shapeCached(title, hf, true)
			cx := axisRect.X + axisRect.W - axisOuterPad - hm.Ascent*0.5
			cy := plotRect.Y + plotRect.H*0.5
			p.Transform(ui.Rotation(90), cx, cy, func() {
				p.Glyphs(sh.glyphs, cx-sh.width*0.5, cy+(hm.Ascent-hm.Descent)*0.35, theme.TitleText)
			})
		}
	}
}

func overlapsVertical(placed []float32, y, minDist float32) bool {
	for _, py := range placed {
		if float32(math.Abs(float64(py-y))) < minDist {
			return true
		}
	}
	return false
}
