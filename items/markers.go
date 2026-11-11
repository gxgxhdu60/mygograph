package items

import (
	"math"
	"sort"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
)

// niceCalibrationStep 将任意正物理跨度规整为最接近的 1-2-5 × 10^k 临床标准标尺步长（如 50 μV、100 μV、1 mV、200 ms、1 s）。
func niceCalibrationStep(raw float64) float64 {
	if raw <= 0 || math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 1.0
	}
	exp := math.Floor(math.Log10(raw))
	base := math.Pow(10, exp)
	f := raw / base
	switch {
	case f < 1.5:
		return 1.0 * base
	case f < 3.5:
		return 2.0 * base
	case f < 7.5:
		return 5.0 * base
	default:
		return 10.0 * base
	}
}

// drawClinicalScaleBar 在绘图区右下角绘制临床与学术规范的 L 型幅值-时间物理比例标尺（使用 RenderContext 共享字形缓存，稳态 0 allocs/op）。
func drawClinicalScaleBar(rc *mygograph.RenderContext, pr ui.Rect, barW, barH float32, xLabel, yLabel string) {
	f := ui.Font{Family: rc.Theme.FontFamily, Size: 10.5, Weight: 600, Features: "tnum"}
	fm := f.Metrics()
	xGlyphs, xTextW := rc.ShapeText(xLabel, f)
	yGlyphs, yTextW := rc.ShapeText(yLabel, f)

	// 右下角原点 (cornerX, cornerY) 为 L 型标尺左下交汇顶点：向右延伸 barW，向上延伸 barH
	marginR := float32(14)
	marginB := fm.Ascent + fm.Descent + 14
	rightEdge := pr.X + pr.W - marginR
	cornerX := rightEdge - barW
	cornerY := pr.Y + pr.H - marginB

	// 半透明衬底卡片（包裹竖杆左侧读数、L 标尺本体与横杆下方读数，防止高密波形干扰标尺辨识）
	padLeft := yTextW + 12
	boxX := max(cornerX-padLeft, pr.X+4)
	boxY := max(cornerY-barH-8, pr.Y+4)
	boxW := (rightEdge + 8) - boxX
	boxH := (pr.Y + pr.H - 4) - boxY
	card := ui.Rect{X: boxX, Y: boxY, W: boxW, H: boxH}
	rc.Painter.Fill(card, rc.Theme.ReadoutBg, 4)
	rc.Painter.Stroke(card, rc.Theme.ReadoutBorder, 4, 1)

	barCol := rc.Theme.TitleText
	// 垂直幅值标尺杆与端帽
	rc.Painter.Line(cornerX, cornerY-barH, cornerX, cornerY+1, 2.2, barCol)
	rc.Painter.Line(cornerX-3, cornerY-barH, cornerX+3, cornerY-barH, 1.5, barCol)
	// 水平时基标尺杆与端帽
	rc.Painter.Line(cornerX-1, cornerY, cornerX+barW, cornerY, 2.2, barCol)
	rc.Painter.Line(cornerX+barW, cornerY-3, cornerX+barW, cornerY+3, 1.5, barCol)

	// 垂直幅值读数（紧贴竖杆左侧居中）
	yx := max(cornerX-yTextW-6, boxX+5)
	yy := cornerY - barH*0.5 + (fm.Ascent-fm.Descent)*0.5
	rc.Painter.Glyphs(yGlyphs, yx, yy, barCol)

	// 水平时间读数（位于横杆正下方居中）
	xx := cornerX + (barW-xTextW)*0.5
	if xx+xTextW > rightEdge+4 {
		xx = rightEdge + 4 - xTextW
	}
	xy := cornerY + fm.Ascent + 4
	rc.Painter.Glyphs(xGlyphs, xx, xy, barCol)
}

// ScaleBarOption 配置独立 L 型物理比例标尺图元。
type ScaleBarOption func(*ScaleBarItem)

// WithScaleBarZ 设置比例标尺图层深度（默认 18）。
func WithScaleBarZ(z float32) ScaleBarOption {
	return func(sb *ScaleBarItem) { sb.z = z }
}

// ScaleBarItem 是适用于单通道心电（ECG）、神经电生理膜片钳或无刻度论文插图的独立 L 型物理比例标尺图元。
type ScaleBarItem struct {
	xUnit   string
	yUnit   string
	visible bool
	z       float32

	lastXVal float64
	lastYVal float64
	lastXStr string
	lastYStr string
}

// NewScaleBar 创建绑定横轴物理单位 xUnit（如 "s"）与纵轴物理单位 yUnit（如 "V"）的自适应 L 型比例标尺。
func NewScaleBar(xUnit, yUnit string, opts ...ScaleBarOption) *ScaleBarItem {
	sb := &ScaleBarItem{
		xUnit:    xUnit,
		yUnit:    yUnit,
		visible:  true,
		z:        18,
		lastXVal: math.NaN(),
		lastYVal: math.NaN(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(sb)
		}
	}
	return sb
}

func (sb *ScaleBarItem) Bounds() (data.Range, data.Range, bool) {
	return data.Range{}, data.Range{}, false
}

func (sb *ScaleBarItem) Visible() bool     { return sb.visible }
func (sb *ScaleBarItem) SetVisible(v bool) { sb.visible = v }
func (sb *ScaleBarItem) ZValue() float32   { return sb.z }
func (sb *ScaleBarItem) Draw(_ *mygograph.RenderContext) {}

// DrawOverlay 在视口角落自适应渲染整档 L 型比例标尺。
func (sb *ScaleBarItem) DrawOverlay(rc *mygograph.RenderContext) {
	if !sb.visible || rc.PlotRect.W < 140 || rc.PlotRect.H < 90 || rc.View.LogX() || rc.View.LogY() {
		return
	}
	pr := rc.PlotRect
	x0, x1 := rc.View.XRange()
	y0, y1 := rc.View.YRange()
	xSpan := math.Abs(x1 - x0)
	ySpan := math.Abs(y1 - y0)
	if xSpan <= 1e-15 || ySpan <= 1e-15 {
		return
	}
	pxPerX := float64(pr.W) / xSpan
	pxPerY := float64(pr.H) / ySpan
	calX := niceCalibrationStep(76.0 / pxPerX)
	calY := niceCalibrationStep(46.0 / pxPerY)
	barW := float32(min(max(calX*pxPerX, 28.0), float64(pr.W)*0.38))
	barH := float32(min(max(calY*pxPerY, 20.0), float64(pr.H)*0.38))
	actX := float64(barW) / pxPerX
	actY := float64(barH) / pxPerY
	if math.Abs(actX-calX)/max(calX, 1e-30) < 0.02 {
		actX = calX
	}
	if math.Abs(actY-calY)/max(calY, 1e-30) < 0.02 {
		actY = calY
	}
	if actX != sb.lastXVal || actY != sb.lastYVal {
		sb.lastXVal = actX
		sb.lastYVal = actY
		sb.lastXStr = mygograph.FormatSIValue(actX, 3, sb.xUnit)
		sb.lastYStr = mygograph.FormatSIValue(actY, 3, sb.yUnit)
	}
	drawClinicalScaleBar(rc, pr, barW, barH, sb.lastXStr, sb.lastYStr)
}

var _ mygograph.OverlayItem = (*ScaleBarItem)(nil)

// EventMarker 描述电生理/工业实时流中的单条同步打标事件（如 ERP 刺激 S1/S2、癫痫棘波或心电 PVC 早搏标注）。
type EventMarker struct {
	Time  float64  // 事件发生的物理时间戳（秒，与采样流时间轴一致）
	Label string   // 顶部简短标记文本（如 "S1"、"PVC"、"Trig"）
	Color ui.Color // 标记线与徽章主色（零值时默认采用主题 CursorLine 色）
}

// EventMarkersOption 配置批量事件打标图元。
type EventMarkersOption func(*EventMarkersItem)

// WithMaxEvents 设置滚动历史最多保留的事件条数上限（默认 16384，超出后自动淘汰最旧标记，保障 24h 监护零内存膨胀）。
func WithMaxEvents(maxCount int) EventMarkersOption {
	return func(em *EventMarkersItem) {
		if maxCount > 0 {
			em.maxEvents = maxCount
		}
	}
}

// WithMarkersMultiChannel 绑定宿主 MultiChannelItem，使事件打标在擦除扫描模式（DisplaySweepMode）下
// 也能按周期相位精确定位并随扫描擦除带自动隐去过期的上一周期旧标记。
func WithMarkersMultiChannel(mc *MultiChannelItem) EventMarkersOption {
	return func(em *EventMarkersItem) {
		em.hostMC = mc
	}
}

// WithMarkersDashed 控制事件垂直线是否使用虚线样式（默认 true）。
func WithMarkersDashed(dashed bool) EventMarkersOption {
	return func(em *EventMarkersItem) {
		em.dashed = dashed
	}
}

// EventMarkersItem 是面向成百上千条实时刺激 Trigger / 心搏注释的高吞吐事件标记图元。
// 采用单调序列二分裁剪（O(log N + K_vis)）与复用路径缓冲，稳态帧渲染 0 allocs/op。
type EventMarkersItem struct {
	mu        sync.RWMutex
	events    []EventMarker
	monotonic bool
	maxEvents int
	dashed    bool
	visible   bool
	z         float32
	hostMC    *MultiChannelItem

	dashPath *ui.Path
	visBuf   []EventMarker
}

// NewEventMarkers 创建并发安全的实时事件打标图元。
func NewEventMarkers(opts ...EventMarkersOption) *EventMarkersItem {
	em := &EventMarkersItem{
		monotonic: true,
		maxEvents: 16384,
		dashed:    true,
		visible:   true,
		z:         9,
		dashPath:  &ui.Path{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(em)
		}
	}
	return em
}

// Push 线程安全地追加一条实时事件标记（可供硬件串口/网络 Trigger 采集协程直接调用）。
func (em *EventMarkersItem) Push(timestamp float64, label string, col ui.Color) {
	if math.IsNaN(timestamp) || math.IsInf(timestamp, 0) {
		return
	}
	em.mu.Lock()
	defer em.mu.Unlock()

	if len(em.events) > 0 && timestamp < em.events[len(em.events)-1].Time {
		em.monotonic = false
	}
	if len(em.events) >= em.maxEvents {
		drop := max(em.maxEvents/8, 1)
		copy(em.events, em.events[drop:])
		em.events = em.events[:len(em.events)-drop]
	}
	em.events = append(em.events, EventMarker{
		Time:  timestamp,
		Label: label,
		Color: col,
	})
}

// Clear 清空全部已记录的事件标记。
func (em *EventMarkersItem) Clear() {
	em.mu.Lock()
	em.events = em.events[:0]
	em.monotonic = true
	em.mu.Unlock()
}

// Len 返回当前缓存的事件总数。
func (em *EventMarkersItem) Len() int {
	em.mu.RLock()
	n := len(em.events)
	em.mu.RUnlock()
	return n
}

func (em *EventMarkersItem) Bounds() (data.Range, data.Range, bool) {
	// 事件标记线不干扰数据量程计算
	return data.Range{}, data.Range{}, false
}

func (em *EventMarkersItem) Visible() bool     { return em.visible }
func (em *EventMarkersItem) SetVisible(v bool) { em.visible = v }
func (em *EventMarkersItem) ZValue() float32   { return em.z }

func (em *EventMarkersItem) Draw(rc *mygograph.RenderContext) {
	if !em.visible || rc.PlotRect.W < 4 || rc.PlotRect.H < 4 {
		return
	}
	pr := rc.PlotRect

	// 判断是否跟随擦除扫描模式（Sweep Mode）执行周期折叠映射
	sweepMode := em.hostMC != nil && em.hostMC.DisplayMode() == DisplaySweepMode && em.hostMC.Ring() != nil
	var winSec, eraseGap, tLatest, t0Base, cycleStart, cursorNorm float64
	if sweepMode {
		ring := em.hostMC.Ring()
		winSec = max(em.hostMC.WindowSeconds(), 1e-6)
		eraseGap = em.hostMC.eraseGapSec
		tLatest = ring.LatestTime()
		// 与 MultiRingBuffer.PackShaderEnvelope 保持严格一致的扫描相位对齐
		t0Base = 0
		relNow := max(tLatest-t0Base, 0)
		cycleIdx := math.Floor(relNow / winSec)
		cycleStart = t0Base + cycleIdx*winSec
		cursorNorm = min(max((relNow-cycleIdx*winSec)/winSec, 0), 1)
	}

	x0, x1 := rc.View.XRange()
	tQueryLo, tQueryHi := min(x0, x1), max(x0, x1)
	if sweepMode {
		tQueryLo = cycleStart - winSec
		tQueryHi = tLatest
	}

	em.mu.RLock()
	em.visBuf = em.visBuf[:0]
	if len(em.events) > 0 {
		if em.monotonic {
			i0 := sort.Search(len(em.events), func(i int) bool {
				return em.events[i].Time >= tQueryLo
			})
			for i := i0; i < len(em.events); i++ {
				ev := em.events[i]
				if ev.Time > tQueryHi {
					break
				}
				em.visBuf = append(em.visBuf, ev)
			}
		} else {
			for _, ev := range em.events {
				if ev.Time >= tQueryLo && ev.Time <= tQueryHi {
					em.visBuf = append(em.visBuf, ev)
				}
			}
		}
	}
	em.mu.RUnlock()

	if len(em.visBuf) == 0 {
		return
	}

	f := ui.Font{Family: rc.Theme.FontFamily, Size: 10, Weight: 600, Features: "tnum"}
	fm := f.Metrics()
	lastBadgeRight := float32(-1e9)

	for _, ev := range em.visBuf {
		var dispX float64
		if sweepMode {
			if ev.Time >= cycleStart {
				phase := (ev.Time - cycleStart) / winSec
				if phase > cursorNorm+1e-6 {
					continue
				}
				dispX = phase * winSec
			} else {
				prevStart := cycleStart - winSec
				phase := (ev.Time - prevStart) / winSec
				gapFrac := max(eraseGap/winSec, 0)
				if phase < cursorNorm+gapFrac || phase > 1.0 {
					continue
				}
				dispX = phase * winSec
			}
		} else {
			dispX = ev.Time
		}

		sx := rc.Transform.DataToScreenX(dispX)
		if sx < pr.X || sx > pr.X+pr.W {
			continue
		}
		col := ev.Color
		if col.A == 0 {
			col = rc.Theme.CursorLine
		}
		lineCol := ui.RGB(col.R, col.G, col.B)

		if em.dashed {
			em.dashPath.Reset()
			em.dashPath.MoveTo(sx, pr.Y).LineTo(sx, pr.Y+pr.H)
			em.dashPath.Dash(0, 4, 4)
			rc.Painter.StrokePath(em.dashPath, 1.15, lineCol)
		} else {
			rc.Painter.Line(sx, pr.Y, sx, pr.Y+pr.H, 1.15, lineCol)
		}

		if ev.Label != "" {
			gs, tw := rc.ShapeText(ev.Label, f)
			bw := tw + 8
			bh := fm.Ascent + fm.Descent + 4
			bx := min(max(sx+2, pr.X+2), pr.X+pr.W-bw-2)
			by := pr.Y + 4
			// 密集连续打标时自动错位阶梯下移，避免标签文字互相覆盖
			if bx < lastBadgeRight+3 {
				by += bh + 2
			} else {
				lastBadgeRight = bx + bw
			}
			badge := ui.Rect{X: bx, Y: by, W: bw, H: bh}
			rc.Painter.Fill(badge, lineCol, 3)
			rc.Painter.Glyphs(gs, badge.X+4, badge.Y+2+fm.Ascent, ui.RGB(10, 14, 20))
		}
	}
}
