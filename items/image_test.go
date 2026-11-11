package items_test

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/colormap"
	"github.com/gaoxi/mygograph/items"
)

func TestImageItemKageLUTNaNHoleAndHistogramWindowing(t *testing.T) {
	// 构造 4x2 热力矩阵：
	// 第 0 行（底部 y ∈ [0,1]）：[0, 50, 100, NaN]
	// 第 1 行（顶部 y ∈ [1,2]）：[100, 100, 100, NaN]
	mat := []float64{
		0, 50, 100, math.NaN(),
		100, 100, 100, math.NaN(),
	}
	ctrl := colormap.NewColorControl(colormap.Grayscale(), 0, 100).SetUnit("Power", "dB")
	imgItem := items.NewImage(
		items.WithImageName("Spec"),
		items.WithColorControl(ctrl),
		items.WithPhysicalRect(0, 40, 0, 20),
	).SetData(4, 2, mat)

	plot := mygograph.NewPlot(mygograph.WithGrid(false, false))
	plot.AddItem(imgItem)
	plot.ViewBox().SetRange(0, 40, 0, 20, 0)

	histLUT := colormap.NewHistogramLUT(ctrl)
	layout := mygograph.NewGraphLayout().AddRow(plot).AddFixedRight(histLUT, 115)

	tt := ui.NewTester(func(c *ui.Context) {
		layout.View(c)
	}, 640, 360)

	frame := tt.Image()
	pr := plot.PlotRectLocal()

	// 左下格中心 (x=5, y=5) 对应值 0 -> Grayscale 黑 (0,0,0)
	vt := plot.ViewBox().Transform(pr)
	xZeroPx := int(vt.DataToScreenX(5))
	yBotPx := int(vt.DataToScreenY(5))
	cZero := frame.RGBAAt(xZeroPx, yBotPx)
	if cZero.R > 4 || cZero.G > 4 || cZero.B > 4 {
		t.Fatalf("cell=0 with Grayscale [0,100] should be black, got %v", cZero)
	}

	// 左上格中心 (x=5, y=15) 对应值 100 -> Grayscale 白 (255,255,255)
	yTopPx := int(vt.DataToScreenY(15))
	cMax := frame.RGBAAt(xZeroPx, yTopPx)
	if cMax.R < 250 || cMax.G < 250 || cMax.B < 250 {
		t.Fatalf("cell=100 with Grayscale [0,100] should be white, got %v", cMax)
	}

	// 最右列 (x=35, y=15) 为 NaN -> 应透明露出 PlotBackground (#0B0E14 -> R=11,G=14,B=20)
	xNaNPx := int(vt.DataToScreenX(35))
	cNaN := frame.RGBAAt(xNaNPx, yTopPx)
	bg := mygograph.DarkTheme().PlotBackground
	if absInt(int(cNaN.R)-int(bg.R)) > 3 || absInt(int(cNaN.G)-int(bg.G)) > 3 || absInt(int(cNaN.B)-int(bg.B)) > 3 {
		t.Fatalf("NaN cell should expose PlotBackground %v, got %v", bg, cNaN)
	}

	// 动态调窗 [0, 40]：原本中灰 (50) 的第 1 列底部格 (x=15, y=5) 应立即饱和为纯白 (255)，且无需重新 SetData！
	ctrl.SetLevels(0, 40)
	tt.Frame()
	frame2 := tt.Image()
	xMidPx := int(vt.DataToScreenX(15))
	cMidSaturated := frame2.RGBAAt(xMidPx, yBotPx)
	if cMidSaturated.R < 250 {
		t.Fatalf("after SetLevels(0,40), cell=50 should saturate white (>=250), got %v", cMidSaturated)
	}

	// 验证 HistogramLUT 右键菜单可一键切换色表至 Viridis
	tt.RightClickAt(600, 180)
	if err := tt.ChooseMenuItem("Viridis"); err != nil {
		t.Fatalf("HistogramLUT context menu ChooseMenuItem(Viridis) failed: %v", err)
	}
	if ctrl.Colormap().Name() != "Viridis" {
		t.Fatalf("expected Viridis colormap, got %s", ctrl.Colormap().Name())
	}
}

func TestWaterfallSingleDrawCallRingWrapAndSnap(t *testing.T) {
	ctrl := colormap.NewColorControl(colormap.Grayscale(), 0, 100)
	wf := items.NewWaterfall(4, 4, 0, 100,
		items.WithColorControl(ctrl),
		items.WithPhysicalRect(0, 400, 0, 4),
	)

	// 连续推入 6 行（触发环形折返 6 % 4 == 2），最后保留的 4 行从旧到新应为：30, 50, 75, 100
	rows := []float64{10, 20, 30, 50, 75, 100}
	line := make([]float64, 4)
	for _, v := range rows {
		for c := range line {
			line[c] = v
		}
		wf.PushRow(line)
	}
	if wf.WriteRow() != 2 {
		t.Fatalf("expected ring WriteRow=2 after 6 pushes into 4-row waterfall, got %d", wf.WriteRow())
	}

	plot := mygograph.NewPlot(mygograph.WithGrid(false, false))
	plot.AddItem(wf)
	plot.ViewBox().SetRange(0, 400, 0, 4, 0)

	tt := ui.NewTester(func(c *ui.Context) {
		plot.View(c)
	}, 400, 300)

	img := tt.Image()
	pr := plot.PlotRectLocal()
	vt := plot.ViewBox().Transform(pr)

	// 默认 newestAtTop=true：顶部 y=3.5 应对应最新推入的 100（纯白），底部 y=0.5 应对应最旧保留行 30（约 76 灰阶）
	cx := int(vt.DataToScreenX(200))
	topColor := img.RGBAAt(cx, int(vt.DataToScreenY(3.5)))
	botColor := img.RGBAAt(cx, int(vt.DataToScreenY(0.5)))
	if topColor.R < 250 {
		t.Fatalf("top (newest) waterfall row should be white (~255), got %v", topColor)
	}
	if absInt(int(botColor.R)-76) > 8 {
		t.Fatalf("bottom (oldest=30) waterfall row should be ~76 gray, got %v", botColor)
	}

	// 验证 SnapNearest（实现 mygograph.SnapTarget）精确命中环形索引
	lx := float32(cx) - pr.X
	ly := vt.DataToScreenY(3.5) - pr.Y
	pt, ok := wf.SnapNearest(lx, ly, pr, plot.ViewBox())
	if !ok || pt.Label == "" {
		t.Fatalf("Waterfall SnapNearest failed: ok=%v lbl=%q", ok, pt.Label)
	}
}

func BenchmarkHeatmap1024x1024WindowingZeroCPU(b *testing.B) {
	const side = 1024
	mat := make([]float64, side*side)
	for i := range mat {
		mat[i] = float64(i%1024) * 0.1
	}
	ctrl := colormap.NewColorControl(colormap.Inferno(), 0, 102.4)
	img := items.NewImage(items.WithColorControl(ctrl)).SetData(side, side, mat)
	_ = img

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Gate 4：1024x1024 热力图 HistogramLUT 调窗仅更新标量 Uniform，0 B/op
		lo := float64(i&31) * 0.5
		ctrl.SetLevels(lo, 95.0)
		_, _ = ctrl.NormalizeForBase(0, 102.4)
	}
}

func BenchmarkWaterfallPushRow_1024Cols(b *testing.B) {
	wf := items.NewWaterfall(1024, 512, -120, 0)
	row := make([]float64, 1024)
	for i := range row {
		row[i] = -90.0 + 30.0*math.Sin(float64(i)*0.05)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wf.PushRow(row)
	}
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
