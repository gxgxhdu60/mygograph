package mygograph

import (
	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph/data"
)

// RenderContext 封装单帧绘图周期向所有图元传递的渲染上下文与零分配工具集。
type RenderContext struct {
	Painter   *ui.Painter
	PlotRect  ui.Rect                // 中心纯绘图区在窗口中的 DIP 矩形
	View      *ViewBox               // 当前相机视口模型
	Transform data.ViewportTransform // 预构建的 float64 -> 窗口 DIP 相机相对投影
	Theme     Theme                  // 当前工程主题
	Scale     float32                // 物理像素缩放倍率（如 1.0, 1.5, 2.0）
	HoverLX   float32                // 鼠标在绘图区内的局部 X（[0, PlotRect.W]）
	HoverLY   float32                // 鼠标在绘图区内的局部 Y（[0, PlotRect.H]）
	PointerIn bool                   // 鼠标当前是否位于绘图区内部

	plot *Plot
}

// PixelBuckets 返回当前绘图区水平物理像素桶数 ceil(PlotRect.W * Scale)，供 M4 / 金字塔降采样使用。
func (rc *RenderContext) PixelBuckets() int {
	s := rc.Scale
	if s <= 0 {
		s = 1
	}
	w := int(rc.PlotRect.W*s + 0.999)
	if w < 1 {
		return 1
	}
	return w
}

// FormatX 使用绘图区关联的水平坐标轴比例尺格式化物理 X 读数。
func (rc *RenderContext) FormatX(val float64) string {
	if rc.plot != nil {
		return rc.plot.FormatX(val)
	}
	x0, x1 := rc.View.XRange()
	return FormatSIValue(val, 4, "") + " (" + FormatSIValue(x1-x0, 2, "") + ")"
}

// FormatY 使用绘图区关联的垂直坐标轴比例尺格式化物理 Y 读数。
func (rc *RenderContext) FormatY(val float64) string {
	if rc.plot != nil {
		return rc.plot.FormatY(val)
	}
	return FormatSIValue(val, 4, "")
}

// ShapeText 使用 Plot 共享的 ui.Glyph 缓存整形单行文本（稳态 0 allocs/op），返回字形切片与像素宽度。
func (rc *RenderContext) ShapeText(text string, font ui.Font) ([]ui.Glyph, float32) {
	if rc.plot != nil {
		sh := rc.plot.shapeOverlayText(text, font)
		return sh.glyphs, sh.width
	}
	gs := ui.Shape(text, font)
	var w float32
	if len(gs) > 0 {
		last := gs[len(gs)-1]
		w = last.X + last.Advance
	}
	return gs, w
}

// Items 返回宿主 Plot 当前挂载的全部图元切片（只读，供 Crosshair 自动就近吸附查询）。
func (rc *RenderContext) Items() []Item {
	if rc.plot == nil {
		return nil
	}
	return rc.plot.items
}

// Item 定义挂载到 Plot 画布中的所有可视化图元的基础接口。
type Item interface {
	// Bounds 返回图元在物理数据空间的包围盒；静态标尺等无界辅助线返回 ok=false，不干扰 AutoRange。
	Bounds() (xRange, yRange data.Range, ok bool)
	// Draw 在已裁剪的绘图区 PlotRect 内执行绘制。
	Draw(ctx *RenderContext)
	// Visible 返回图元是否可见。
	Visible() bool
	// SetVisible 切换图元可见性。
	SetVisible(visible bool)
	// ZValue 控制图元绘制层序（数值越小越靠底：热力图 -10 -> ROI 背景 -2 -> 曲线 0 -> 游标/准星 +10）。
	ZValue() float32
}

// OverlayItem 可选接口：允许游标边缘标签、读数浮窗在绘图区裁剪框之外（如紧贴坐标轴脊线处）绘制附加标注。
type OverlayItem interface {
	DrawOverlay(ctx *RenderContext)
}

// InteractiveItem 可选接口：支持鼠标命中测试、指针光标形态反馈与拖拽交互（如 InfiniteLine、ROI、多导联双击独奏）。
//
// 所有坐标 (lx, ly) 均已转换为相对于绘图区左上角 PlotRect.(X,Y) 的局部 DIP 坐标 [0, plotW] × [0, plotH]。
type InteractiveItem interface {
	HitTest(lx, ly, plotW, plotH float32, vb *ViewBox) (hit bool, cursor ui.Cursor)
	HandlePointer(ev ui.InputEvent, lx, ly, plotW, plotH float32, vb *ViewBox) (consumed bool)
}

// HoverObserver 可选接口：被动监听指针移动而不抢占左键平移/框选手势（供 Crosshair 与悬停提示框使用）。
type HoverObserver interface {
	OnHover(lx, ly, plotW, plotH float32, inside bool, vb *ViewBox) (changed bool)
}

// WheelInterceptor 可选接口：允许特定图元优先拦截带修饰键的滚轮事件（如 MultiChannelItem 的 Alt+滚轮增益调节）。
type WheelInterceptor interface {
	InterceptWheel(ev ui.InputEvent, lx, ly, plotW, plotH float32, vb *ViewBox) (consumed bool)
}

// StreamItem 可选接口：暴露底层实时流数据版本号，使 Plot 在流活跃时挂载 VSync AnimationFrame，
// 而在流暂停时自动休眠至 0% 空闲 CPU 占用。
type StreamItem interface {
	StreamVersion() (version uint64, active bool)
}

// LegendEntry 描述图例面板中的单个可点击条目。
type LegendEntry struct {
	Name   string
	Color  ui.Color
	Target Item // 非 nil 时点击图例项可一键切换对应曲线显隐
}

// LegendProvider 可选接口：向 Plot 浮动图例面板提供图例项。
type LegendProvider interface {
	LegendEntries() []LegendEntry
}

// SnapPoint 描述图元向十字准星（Crosshair）返回的最近真实物理采样点信息。
type SnapPoint struct {
	DataX   float64
	DataY   float64
	ScreenX float32 // 窗口 DIP X
	ScreenY float32 // 窗口 DIP Y
	Label   string  // 额外标签（如曲线名或矩阵标量值）
}

// SnapTarget 可选接口：支持十字准星就近吸附真实数据点（Snap-to-Data）。
type SnapTarget interface {
	SnapNearest(lx, ly float32, plotRect ui.Rect, vb *ViewBox) (pt SnapPoint, ok bool)
}

// CSVSeries 描述导出当前视口可见数据时单个图元贡献的列集合。
type CSVSeries struct {
	Headers []string
	Columns [][]float64
}

// ViewportCSVProvider 可选接口：导出图元在当前视口 [xMin, xMax] 内的全精度原始采样数据列。
type ViewportCSVProvider interface {
	ExportViewportCSV(vb *ViewBox) (CSVSeries, bool)
}
