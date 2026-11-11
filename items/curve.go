package items

import (
	"math"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
)

// StepMode 定义阶梯线连接方式（用于数字逻辑波形、零阶保持 DAC 或直方图轮廓）。
type StepMode uint8

const (
	StepNone StepMode = iota // 标准点对点斜线连接
	StepPost                 // 先水平延伸至 x_{i+1} 再垂直跳变至 y_{i+1}（零阶保持）
	StepPre                  // 先垂直跳变至 y_{i+1} 再水平延伸至 x_{i+1}
	StepMid                  // 在中点 (x_i+x_{i+1})/2 处垂直跳变
)

// CurveOption 配置 CurveItem 的函数式选项。
type CurveOption func(*CurveItem)

// WithName 设置曲线图例名称（非空时自动出现在 Plot 图例面板）。
func WithName(name string) CurveOption {
	return func(c *CurveItem) { c.name = name }
}

// WithColor 设置曲线颜色（为命中 mygo StrokePolyline GPU 胶囊体快路径，线色 Alpha 强制为 255 不透明）。
func WithColor(col ui.Color) CurveOption {
	return func(c *CurveItem) { c.color = ui.RGB(col.R, col.G, col.B) }
}

// WithWidth 设置曲线线宽（DIP）。
func WithWidth(width float32) CurveOption {
	return func(c *CurveItem) {
		if width > 0 {
			c.width = width
		}
	}
}

// WithDecimateMode 指定降采样模式（默认 DecimateAuto 自动启用 M4/金字塔保峰包络）。
func WithDecimateMode(mode data.DecimateMode) CurveOption {
	return func(c *CurveItem) { c.decimateMode = mode }
}

// WithStepMode 启用阶梯线模式。
func WithStepMode(mode StepMode) CurveOption {
	return func(c *CurveItem) { c.stepMode = mode }
}

// WithFillBaseline 启用曲线向指定物理 Y 基线（如 0.0）的半透明包络填充。
func WithFillBaseline(baselineY float64, fillCol ui.Color) CurveOption {
	return func(c *CurveItem) {
		c.hasBaselineFill = true
		c.baselineY = baselineY
		c.fillColor = fillCol
	}
}

// WithFillBetween 启用当前曲线与另一条参考曲线之间的置信区间阴影带填充。
func WithFillBetween(other *CurveItem, fillCol ui.Color) CurveOption {
	return func(c *CurveItem) {
		c.fillPartner = other
		c.fillColor = fillCol
	}
}

// WithCurveZ 设置曲线图层深度 ZValue（默认 0）。
func WithCurveZ(z float32) CurveOption {
	return func(c *CurveItem) { c.z = z }
}

// WithStreaming 显式标记曲线绑定了实时流数据源（RingBuffer 默认自动开启）。
func WithStreaming(active bool) CurveOption {
	return func(c *CurveItem) {
		c.streamExplicit = true
		c.streaming = active
	}
}

// WithCurveWindowSeconds 设置实时流曲线在自动跟随（Live Follow）模式下的滑动时间窗宽度（秒，0 表示铺满整个 RingBuffer）。
func WithCurveWindowSeconds(sec float64) CurveOption {
	return func(c *CurveItem) {
		if sec >= 0 {
			c.windowSec = sec
		}
	}
}

// CurveItem 是高性能一维折线/阶梯线/包络填充图元，稳态帧渲染 0 B/op、0 allocs/op。
type CurveItem struct {
	src             data.XYSource
	name            string
	color           ui.Color
	width           float32
	decimateMode    data.DecimateMode
	stepMode        StepMode
	windowSec       float64
	visible         bool
	z               float32
	hasBaselineFill bool
	baselineY       float64
	fillPartner     *CurveItem
	fillColor       ui.Color
	streamExplicit  bool
	streaming       bool

	// 复用的零分配几何缓冲与填充路径
	ptsBuf     [][2]float32
	stepBuf    [][2]float32
	partnerBuf [][2]float32
	mmBuf      []data.MinMax
	ringYBuf   []float64
	fillPath   *ui.Path
}

// NewCurve 创建绑定任意 XYSource（切片、函数、金字塔或环形流缓冲）的曲线图元。
func NewCurve(src data.XYSource, opts ...CurveOption) *CurveItem {
	_, isRing := src.(*data.RingBuffer)
	c := &CurveItem{
		src:          src,
		color:        ui.RGB(0, 212, 255),
		width:        1.5,
		decimateMode: data.DecimateAuto,
		visible:      true,
		streaming:    isRing,
		ptsBuf:       make([][2]float32, 0, 2048),
		fillPath:     &ui.Path{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
}

// Source 返回当前绑定的数据源。
func (c *CurveItem) Source() data.XYSource { return c.src }

// SetSource 动态替换数据源。
func (c *CurveItem) SetSource(src data.XYSource) {
	c.src = src
	if !c.streamExplicit {
		_, c.streaming = src.(*data.RingBuffer)
	}
}

// Name 返回曲线名称。
func (c *CurveItem) Name() string { return c.name }

// Color 返回曲线不透明线色。
func (c *CurveItem) Color() ui.Color { return c.color }

// SetColor 更新曲线线色（自动保持 A=255 不透明以走 GPU 快路径）。
func (c *CurveItem) SetColor(col ui.Color) {
	c.color = ui.RGB(col.R, col.G, col.B)
}

func (c *CurveItem) Visible() bool       { return c.visible }
func (c *CurveItem) SetVisible(v bool)   { c.visible = v }
func (c *CurveItem) ZValue() float32     { return c.z }
func (c *CurveItem) SetZValue(z float32) { c.z = z }

// StreamVersion 实现 mygograph.StreamItem，驱动实时流 VSync 刷新并在流暂停后自动休眠。
func (c *CurveItem) StreamVersion() (uint64, bool) {
	if c.src == nil || !c.streaming {
		return 0, false
	}
	return c.src.Version(), true
}

// LegendEntries 实现 mygograph.LegendProvider。
func (c *CurveItem) LegendEntries() []mygograph.LegendEntry {
	if c.name == "" {
		return nil
	}
	return []mygograph.LegendEntry{{
		Name:   c.name,
		Color:  c.color,
		Target: c,
	}}
}

// Bounds 实现 mygograph.Item。
func (c *CurveItem) Bounds() (xRange, yRange data.Range, ok bool) {
	if c.src == nil {
		return data.Range{}, data.Range{}, false
	}
	xr, yr, ok := c.src.Bounds()
	if ok && c.windowSec > 0 {
		if _, isRing := c.src.(*data.RingBuffer); isRing {
			if xr.Max <= c.windowSec {
				xr = data.Range{Min: 0, Max: c.windowSec}
			} else {
				xr = data.Range{Min: xr.Max - c.windowSec, Max: xr.Max}
			}
		}
	}
	return xr, yr, ok
}

// SetDecimateMode 动态切换曲线降采样策略（如 DecimateAuto / DecimateM4 / DecimateLTTB / DecimateNone）。
func (c *CurveItem) SetDecimateMode(mode data.DecimateMode) *CurveItem {
	c.decimateMode = mode
	return c
}

// DecimateMode 返回当前曲线降采样策略。
func (c *CurveItem) DecimateMode() data.DecimateMode { return c.decimateMode }

// SetStepMode 动态切换阶梯连接样式。
func (c *CurveItem) SetStepMode(mode StepMode) *CurveItem {
	c.stepMode = mode
	return c
}

// SetWidth 动态修改曲线线宽（DIP）。
func (c *CurveItem) SetWidth(width float32) *CurveItem {
	if width > 0 {
		c.width = width
	}
	return c
}

// WindowSeconds 返回实时流曲线当前的自动跟随时间窗长度（秒，0 表示全缓冲跨度）。
func (c *CurveItem) WindowSeconds() float64 { return c.windowSec }

// SetWindowSeconds 动态设置实时流曲线的自动跟随时间窗长度（秒，0 表示全缓冲跨度）。
func (c *CurveItem) SetWindowSeconds(sec float64) *CurveItem {
	if sec >= 0 {
		c.windowSec = sec
	}
	return c
}

func (c *CurveItem) projectPoints(vt data.ViewportTransform, buckets int, dst [][2]float32) [][2]float32 {
	if c.src == nil {
		return dst[:0]
	}
	if ring, isRing := c.src.(*data.RingBuffer); isRing && !vt.LogX {
		// 通知 RingBuffer 重新武装下一次采集唤醒信号
		ring.ArmWake()
		xLo, xHi := vt.VisibleLinearXRange()
		dt := ring.Dt()
		estSamples := 0
		if dt > 0 {
			estSamples = int((xHi-xLo)/dt) + 3
		}
		if estSamples > 0 && estSamples <= max(buckets*4, 4096) {
			if cap(c.ringYBuf) < estSamples {
				c.ringYBuf = make([]float64, estSamples*2)
			}
			segT0, n := ring.CopyWindow(xLo-dt, xHi+dt, c.ringYBuf[:estSamples])
			if n == 0 {
				return dst[:0]
			}
			return data.DecimateM4Uniform(segT0, dt, c.ringYBuf[:n], vt, buckets, dst)
		}
		if cap(c.mmBuf) < buckets {
			c.mmBuf = make([]data.MinMax, buckets)
		} else {
			c.mmBuf = c.mmBuf[:buckets]
		}
		validCnt := ring.DecimateWindow(xLo, xHi, buckets, c.mmBuf)
		out := dst[:0]
		if validCnt == 0 {
			return out
		}
		bSpan := (xHi - xLo) / float64(buckets)
		for b := range buckets {
			mm := c.mmBuf[b]
			if !mm.Valid {
				continue
			}
			bx0 := xLo + float64(b)*bSpan
			bxMid := bx0 + 0.5*bSpan
			bx1 := bx0 + bSpan
			out = append(out, [2]float32{vt.DataToScreenX(bx0), vt.DataToScreenY(float64(mm.First))})
			if mm.Min != mm.First {
				out = append(out, [2]float32{vt.DataToScreenX(bxMid), vt.DataToScreenY(float64(mm.Min))})
			}
			if mm.Max != mm.Min {
				out = append(out, [2]float32{vt.DataToScreenX(bxMid), vt.DataToScreenY(float64(mm.Max))})
			}
			if mm.Last != mm.Max {
				out = append(out, [2]float32{vt.DataToScreenX(bx1), vt.DataToScreenY(float64(mm.Last))})
			}
		}
		return out
	}
	return data.ProjectAndDecimate(c.src, vt, c.decimateMode, buckets, dst)
}

// Draw 实现 mygograph.Item。
func (c *CurveItem) Draw(ctx *mygograph.RenderContext) {
	if !c.visible || c.src == nil {
		return
	}
	buckets := ctx.PixelBuckets()
	c.ptsBuf = c.projectPoints(ctx.Transform, buckets, c.ptsBuf)
	if len(c.ptsBuf) == 0 {
		return
	}

	drawPts := c.ptsBuf
	if c.stepMode != StepNone && len(drawPts) >= 2 {
		c.stepBuf = expandStepPoints(drawPts, c.stepMode, c.stepBuf[:0])
		drawPts = c.stepBuf
	}

	// 1. 若配置了基线包络或双曲线置信区间填充，先绘制底部阴影面
	if c.fillPartner != nil && c.fillPartner.src != nil && c.fillColor.A > 0 {
		c.partnerBuf = c.fillPartner.projectPoints(ctx.Transform, buckets, c.partnerBuf)
		if len(c.partnerBuf) >= 2 && len(drawPts) >= 2 {
			c.drawFillBetween(ctx.Painter, drawPts, c.partnerBuf)
		}
	} else if c.hasBaselineFill && c.fillColor.A > 0 && len(drawPts) >= 2 {
		baseScreenY := ctx.Transform.DataToScreenY(c.baselineY)
		if !math.IsNaN(float64(baseScreenY)) {
			baseScreenY = min(max(baseScreenY, ctx.PlotRect.Y-2), ctx.PlotRect.Y+ctx.PlotRect.H+2)
			c.drawBaselineFill(ctx.Painter, drawPts, baseScreenY)
		}
	}

	// 2. 使用强制不透明颜色（A==255）调用 StrokePolyline，命中 GPU OpFill 硬件快路径
	if len(drawPts) == 1 {
		pt := drawPts[0]
		if !isNaN32(pt[0]) && !isNaN32(pt[1]) {
			r := c.width + 1.0
			ctx.Painter.Fill(ui.Rect{X: pt[0] - r, Y: pt[1] - r, W: r * 2, H: r * 2}, c.color, r)
		}
		return
	}
	ctx.Painter.StrokePolyline(drawPts, c.width, c.color)
}

func (c *CurveItem) drawBaselineFill(p *ui.Painter, pts [][2]float32, baseY float32) {
	c.fillPath.Reset()
	inSeg := false
	var startX, lastX float32
	for _, pt := range pts {
		if isNaN32(pt[0]) || isNaN32(pt[1]) {
			if inSeg {
				c.fillPath.LineTo(lastX, baseY)
				c.fillPath.LineTo(startX, baseY)
				c.fillPath.Close()
				inSeg = false
			}
			continue
		}
		if !inSeg {
			startX = pt[0]
			c.fillPath.MoveTo(pt[0], baseY)
			c.fillPath.LineTo(pt[0], pt[1])
			inSeg = true
		} else {
			c.fillPath.LineTo(pt[0], pt[1])
		}
		lastX = pt[0]
	}
	if inSeg {
		c.fillPath.LineTo(lastX, baseY)
		c.fillPath.LineTo(startX, baseY)
		c.fillPath.Close()
	}
	p.FillPath(c.fillPath, c.fillColor)
}

func (c *CurveItem) drawFillBetween(p *ui.Painter, upper, lower [][2]float32) {
	c.fillPath.Reset()
	started := false
	for _, pt := range upper {
		if isNaN32(pt[0]) || isNaN32(pt[1]) {
			continue
		}
		if !started {
			c.fillPath.MoveTo(pt[0], pt[1])
			started = true
		} else {
			c.fillPath.LineTo(pt[0], pt[1])
		}
	}
	if !started {
		return
	}
	for i := len(lower) - 1; i >= 0; i-- {
		pt := lower[i]
		if isNaN32(pt[0]) || isNaN32(pt[1]) {
			continue
		}
		c.fillPath.LineTo(pt[0], pt[1])
	}
	c.fillPath.Close()
	p.FillPath(c.fillPath, c.fillColor)
}

func expandStepPoints(pts [][2]float32, mode StepMode, out [][2]float32) [][2]float32 {
	for i, pt := range pts {
		if isNaN32(pt[0]) || isNaN32(pt[1]) {
			out = append(out, pt)
			continue
		}
		if i > 0 {
			prev := pts[i-1]
			if !isNaN32(prev[0]) && !isNaN32(prev[1]) {
				switch mode {
				case StepPost:
					out = append(out, [2]float32{pt[0], prev[1]})
				case StepPre:
					out = append(out, [2]float32{prev[0], pt[1]})
				case StepMid:
					mx := 0.5 * (prev[0] + pt[0])
					out = append(out, [2]float32{mx, prev[1]}, [2]float32{mx, pt[1]})
				}
			}
		}
		out = append(out, pt)
	}
	return out
}

// SnapNearest 实现 mygograph.SnapTarget：对单调序列以 O(log N) 二分检索距离鼠标 X 最近的真实采样点（亿级点 < 1 μs）。
func (c *CurveItem) SnapNearest(lx, ly float32, plotRect ui.Rect, vb *mygograph.ViewBox) (mygograph.SnapPoint, bool) {
	if !c.visible || c.src == nil {
		return mygograph.SnapPoint{}, false
	}
	n := c.src.Len()
	if n == 0 {
		return mygograph.SnapPoint{}, false
	}

	vt := vb.Transform(plotRect)
	targetX, _ := vb.LocalToData(lx, ly, plotRect.W, plotRect.H)
	targetScreenX := plotRect.X + lx
	targetScreenY := plotRect.Y + ly

	if c.src.MonotonicX() {
		idx := data.LowerBoundX(c.src, targetX)
		// 在 idx 邻域 [-2, +2] 检查最贴近指针的真实有效点
		start := max(idx-2, 0)
		end := min(idx+3, n)
		bestDist := math.Inf(1)
		var best mygograph.SnapPoint
		found := false
		for i := start; i < end; i++ {
			x, y := c.src.XY(i)
			sx := vt.DataToScreenX(x)
			sy := vt.DataToScreenY(y)
			if isNaN32(sx) || isNaN32(sy) {
				continue
			}
			// 横向优先加权欧氏距离，符合示波器沿 X 时间轴追踪体验
			dx := float64(sx - targetScreenX)
			dy := float64(sy - targetScreenY)
			d2 := dx*dx + 0.15*dy*dy
			if d2 < bestDist {
				bestDist = d2
				best = mygograph.SnapPoint{
					DataX:   x,
					DataY:   y,
					ScreenX: sx,
					ScreenY: sy,
					Label:   c.name,
				}
				found = true
			}
		}
		return best, found
	}

	// 非单调参数曲线（如李萨如图形）：在合理点数内查找欧氏最近点
	step := max(n/8192, 1)
	bestDist := math.Inf(1)
	var best mygograph.SnapPoint
	found := false
	for i := 0; i < n; i += step {
		x, y := c.src.XY(i)
		sx := vt.DataToScreenX(x)
		sy := vt.DataToScreenY(y)
		if isNaN32(sx) || isNaN32(sy) {
			continue
		}
		dx := float64(sx - targetScreenX)
		dy := float64(sy - targetScreenY)
		d2 := dx*dx + dy*dy
		if d2 < bestDist {
			bestDist = d2
			best = mygograph.SnapPoint{DataX: x, DataY: y, ScreenX: sx, ScreenY: sy, Label: c.name}
			found = true
		}
	}
	return best, found
}

// ExportViewportCSV 实现 mygograph.ViewportCSVProvider：提取当前可见时间窗内的全精度采样点。
func (c *CurveItem) ExportViewportCSV(vb *mygograph.ViewBox) (mygograph.CSVSeries, bool) {
	if !c.visible || c.src == nil || c.src.Len() == 0 {
		return mygograph.CSVSeries{}, false
	}
	n := c.src.Len()
	xLo, xHi := vb.XRange()
	i0, i1 := 0, n
	if c.src.MonotonicX() {
		i0 = max(data.LowerBoundX(c.src, xLo), 0)
		i1 = min(data.UpperBoundX(c.src, xHi), n)
	}
	if i0 >= i1 {
		return mygograph.CSVSeries{}, false
	}

	// 保护剪贴板/单次 CSV 导出上限（默认最多导出 200,000 行，超出时按整数步进等距抽样）
	const maxExportRows = 200_000
	count := i1 - i0
	stride := max((count+maxExportRows-1)/maxExportRows, 1)
	capHint := (count + stride - 1) / stride
	xs := make([]float64, 0, capHint)
	ys := make([]float64, 0, capHint)
	for i := i0; i < i1; i += stride {
		x, y := c.src.XY(i)
		xs = append(xs, x)
		ys = append(ys, y)
	}

	baseName := c.name
	if baseName == "" {
		baseName = "Curve"
	}
	return mygograph.CSVSeries{
		Headers: []string{baseName + "_X", baseName + "_Y"},
		Columns: [][]float64{xs, ys},
	}, true
}

// BuildContextMenu 实现 mygograph.ContextMenuProvider：复刻 PyQtGraph PlotDataItem 右键参数调节菜单（降采样模式、阶梯线、线宽与实时时基窗口）。
func (c *CurveItem) BuildContextMenu(m *ui.Menu, _, _, _, _ float32, vb *mygograph.ViewBox) {
	title := "Curve Options"
	if c.name != "" {
		title = "Curve: " + c.name
	}
	m.Submenu(title, func(sub *ui.Menu) {
		sub.Submenu("Downsample", func(ds *ui.Menu) {
			isM4 := c.decimateMode == data.DecimateAuto || c.decimateMode == data.DecimateM4
			if ds.Item("Peak Envelope (M4 Auto)").Radio(isM4).Chosen() {
				c.decimateMode = data.DecimateAuto
			}
			if ds.Item("Visual Smooth (LTTB)").Radio(c.decimateMode == data.DecimateLTTB).Chosen() {
				c.decimateMode = data.DecimateLTTB
			}
			if ds.Item("Off (Raw Points)").Radio(c.decimateMode == data.DecimateNone).Chosen() {
				c.decimateMode = data.DecimateNone
			}
		})
		sub.Submenu("Step Mode", func(sm *ui.Menu) {
			if sm.Item("Linear Connect").Radio(c.stepMode == StepNone).Chosen() {
				c.stepMode = StepNone
			}
			if sm.Item("Step Post (ZOH)").Radio(c.stepMode == StepPost).Chosen() {
				c.stepMode = StepPost
			}
			if sm.Item("Step Mid").Radio(c.stepMode == StepMid).Chosen() {
				c.stepMode = StepMid
			}
		})
		sub.Submenu("Line Width", func(lw *ui.Menu) {
			widths := []struct {
				label string
				w     float32
			}{
				{"Thin (1.0 px)", 1.0},
				{"Standard (1.5 px)", 1.5},
				{"Bold (2.5 px)", 2.5},
			}
			for _, item := range widths {
				active := math.Abs(float64(c.width-item.w)) < 0.15
				if lw.Item(item.label).Radio(active).Chosen() {
					c.width = item.w
				}
			}
		})
		if _, isRing := c.src.(*data.RingBuffer); isRing {
			sub.Separator()
			sub.Submenu("Stream Timebase", func(tb *ui.Menu) {
				wins := []struct {
					label string
					sec   float64
				}{
					{"250 ms", 0.25},
					{"1 s", 1.0},
					{"2 s", 2.0},
					{"5 s", 5.0},
					{"10 s", 10.0},
					{"Full Buffer", 0.0},
				}
				for _, w := range wins {
					active := math.Abs(c.windowSec-w.sec) < 0.02
					if tb.Item(w.label).Radio(active).Chosen() {
						c.windowSec = w.sec
						if vb != nil {
							vb.EnableAutoRange(mygograph.AxisX)
						}
					}
				}
			})
		}
	})
}

var _ mygograph.ContextMenuProvider = (*CurveItem)(nil)

func isNaN32(v float32) bool {
	return v != v || math.IsInf(float64(v), 0)
}
