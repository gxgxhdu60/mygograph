package mygograph

import "github.com/egoist/mygo/ui"

// ViewWidget 抽象任何可嵌入 GraphLayout 网格单元的科学绘图组件（如 *Plot、*ColorBar、*HistogramLUT）。
type ViewWidget interface {
	View(c *ui.Context) ui.Element
}

// AxisAlignable 可选接口：允许 GraphLayout 自动测量并统一同一列所有子图的左/右 Y 轴像素宽度。
type AxisAlignable interface {
	MeasureLeftMargin(estHeight float32) float32
	MeasureRightMargin(estHeight float32) float32
	LeftAxis() *Axis
	RightAxis() *Axis
}

type layoutCell struct {
	widget ViewWidget
	flex   float32 // <= 0 表示默认权重 1.0；若 fixedW > 0 则按固定像素宽度布局（如右侧 ColorBar）
	fixedW float32
}

type layoutRow struct {
	cells []layoutCell
	flex  float32
}

// GraphLayout 管理多行多列科学子图排版、可拖拽比例分隔条（Splitters）与跨行像素级坐标轴对齐。
//
// 核心痛点解决（DESIGN.md 第 3.1 / 9.2 节）：上下堆叠的时域图与频谱图因 Y 轴数值位数不同（如 "-250.0 μV"
// vs "0 dB"），默认边距不一致会导致共享 X 时间轴产生水平错位。启用 AlignColumnAxes 后，同一列所有子图
// 自动取最大左/右轴宽度并锁定像素级对齐（误差 0 px）！
type GraphLayout struct {
	rows        []layoutRow
	alignAxes   bool
	resizable   bool
	gap         float32
	theme       Theme
	splitRatios []float32 // 垂直方向嵌套 SplitVerticalPercent 持久比例状态
}

// NewGraphLayout 创建默认开启跨行坐标轴像素级对齐与可拖拽分隔条的多子图布局器。
func NewGraphLayout() *GraphLayout {
	return &GraphLayout{
		alignAxes: true,
		resizable: true,
		gap:       2,
		theme:     DarkTheme(),
	}
}

// SetTheme 设置布局容器底色主题。
func (gl *GraphLayout) SetTheme(th Theme) *GraphLayout {
	gl.theme = th
	return gl
}

// SetAlignAxes 控制是否自动对齐同一列上下子图的左/右坐标轴边缘（默认开启）。
func (gl *GraphLayout) SetAlignAxes(enable bool) *GraphLayout {
	gl.alignAxes = enable
	return gl
}

// SetResizable 控制多行子图之间是否启用鼠标拖拽调整高度比例分隔条（默认开启）。
func (gl *GraphLayout) SetResizable(enable bool) *GraphLayout {
	gl.resizable = enable
	return gl
}

// SetGap 设置子图单元间距（DIP）。
func (gl *GraphLayout) SetGap(gap float32) *GraphLayout {
	gl.gap = max(gap, 0)
	return gl
}

// AddRow 追加新的一行并挂载一个或多个水平并排组件（默认等宽权重 1.0）。
func (gl *GraphLayout) AddRow(widgets ...ViewWidget) *GraphLayout {
	return gl.AddRowFlex(1.0, widgets...)
}

// AddRowFlex 追加指定垂直高度权重 rowFlex 的新行。
func (gl *GraphLayout) AddRowFlex(rowFlex float32, widgets ...ViewWidget) *GraphLayout {
	if rowFlex <= 0 {
		rowFlex = 1.0
	}
	var row layoutRow
	row.flex = rowFlex
	for _, w := range widgets {
		if w != nil {
			row.cells = append(row.cells, layoutCell{widget: w, flex: 1.0})
		}
	}
	gl.rows = append(gl.rows, row)
	gl.rebuildSplitRatios()
	return gl
}

// AddFixedRight 向最后一行右侧追加一个固定 DIP 宽度的附属控件（如 64px 的 ColorBar 或 130px 的 HistogramLUT）。
func (gl *GraphLayout) AddFixedRight(w ViewWidget, widthDIP float32) *GraphLayout {
	if w == nil {
		return gl
	}
	if len(gl.rows) == 0 {
		gl.rows = append(gl.rows, layoutRow{flex: 1.0})
	}
	last := &gl.rows[len(gl.rows)-1]
	last.cells = append(last.cells, layoutCell{widget: w, fixedW: max(widthDIP, 24)})
	return gl
}

func (gl *GraphLayout) rebuildSplitRatios() {
	n := len(gl.rows)
	if n <= 1 {
		gl.splitRatios = gl.splitRatios[:0]
		return
	}
	gl.splitRatios = make([]float32, n-1)
	var suffixSum float32
	for i := n - 1; i >= 0; i-- {
		suffixSum += gl.rows[i].flex
		if i < n-1 && suffixSum > 0 {
			gl.splitRatios[i] = (gl.rows[i].flex / suffixSum) * 100.0
		}
	}
}

// SynchronizeAxes 扫描各列所有实现 AxisAlignable 的子图（如 *Plot），统一设定列内最大左/右轴固定厚度。
func (gl *GraphLayout) SynchronizeAxes() {
	if !gl.alignAxes || len(gl.rows) <= 1 {
		return
	}
	maxCols := 0
	for _, r := range gl.rows {
		if len(r.cells) > maxCols {
			maxCols = len(r.cells)
		}
	}

	for col := range maxCols {
		var maxLeft, maxRight float32
		count := 0
		for _, r := range gl.rows {
			if col >= len(r.cells) || r.cells[col].fixedW > 0 {
				continue
			}
			aa, ok := r.cells[col].widget.(AxisAlignable)
			if !ok {
				continue
			}
			// 先临时解除上次设置的 FixedThickness 以测量真实自然宽度
			if la := aa.LeftAxis(); la != nil {
				la.SetFixedThickness(0)
			}
			if ra := aa.RightAxis(); ra != nil {
				ra.SetFixedThickness(0)
			}
			lw := aa.MeasureLeftMargin(240)
			rw := aa.MeasureRightMargin(240)
			if lw > maxLeft {
				maxLeft = lw
			}
			if rw > maxRight {
				maxRight = rw
			}
			count++
		}
		if count <= 1 {
			continue
		}
		for _, r := range gl.rows {
			if col >= len(r.cells) || r.cells[col].fixedW > 0 {
				continue
			}
			aa, ok := r.cells[col].widget.(AxisAlignable)
			if !ok {
				continue
			}
			if la := aa.LeftAxis(); la != nil && la.Visible() && maxLeft > 0 {
				la.SetFixedThickness(maxLeft)
			}
			if ra := aa.RightAxis(); ra != nil && ra.Visible() && maxRight > 0 {
				ra.SetFixedThickness(maxRight)
			}
		}
	}
}

// View 在声明式 UI 树中构建完整多子图网格（支持拖拽分隔条与跨行轴对齐）。
func (gl *GraphLayout) View(c *ui.Context) ui.Element {
	gl.SynchronizeAxes()

	n := len(gl.rows)
	if n == 0 {
		return ui.Box(c).Fill().Background(gl.theme.Background)
	}
	if n == 1 {
		return gl.buildRow(c, 0).Fill().Background(gl.theme.Background)
	}

	if gl.resizable {
		if len(gl.splitRatios) != n-1 {
			gl.rebuildSplitRatios()
		}
		return gl.buildVerticalSplitChain(c, 0).Fill().Background(gl.theme.Background)
	}

	return ui.Column(c).Fill().Gap(gl.gap).Background(gl.theme.Background).Children(func() {
		for i := range gl.rows {
			gl.buildRow(c, i).Fill().Grow(gl.rows[i].flex)
		}
	})
}

func (gl *GraphLayout) buildVerticalSplitChain(c *ui.Context, idx int) ui.Element {
	if idx >= len(gl.rows)-1 {
		return gl.buildRow(c, idx).Fill()
	}
	return ui.SplitVerticalPercent(c, &gl.splitRatios[idx],
		func() {
			gl.buildRow(c, idx).Fill()
		},
		func() {
			gl.buildVerticalSplitChain(c, idx+1).Fill()
		},
	).Fill()
}

func (gl *GraphLayout) buildRow(c *ui.Context, rowIdx int) ui.Element {
	row := &gl.rows[rowIdx]
	if len(row.cells) == 1 && row.cells[0].fixedW <= 0 {
		return row.cells[0].widget.View(c).Fill()
	}
	return ui.Row(c).Fill().Gap(gl.gap).Children(func() {
		for _, cell := range row.cells {
			el := cell.widget.View(c)
			if cell.fixedW > 0 {
				el.Width(cell.fixedW).FillHeight()
			} else {
				el.Fill().Grow(max(cell.flex, 1.0))
			}
		}
	})
}
