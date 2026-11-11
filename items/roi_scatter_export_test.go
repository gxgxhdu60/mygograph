package items_test

import (
	"bytes"
	"image/png"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/items"
)

func TestLinearRegionBidirectionalSyncAndHandleDrag(t *testing.T) {
	src := data.NewUniformSlice(0, 100.0, func() []float64 {
		arr := make([]float64, 1001)
		for i := range arr {
			arr[i] = math.Sin(float64(i) * 0.05)
		}
		return arr
	}())

	overviewPlot := mygograph.NewPlot(mygograph.WithTitle("Overview"))
	detailPlot := mygograph.NewPlot(mygograph.WithTitle("Detail"))
	overviewPlot.AddItem(items.NewCurve(src, items.WithName("Wave")))
	detailPlot.AddItem(items.NewCurve(src, items.WithName("WaveZoom")))

	region := items.NewLinearRegion(2.0, 5.0, items.WithRegionLimits(0, 10.0))
	region.BindDetailViewBox(detailPlot.ViewBox())
	overviewPlot.AddItem(region)

	gl := mygograph.NewGraphLayout()
	gl.AddRow(overviewPlot)
	gl.AddRow(detailPlot)

	tt := ui.NewTester(func(c *ui.Context) {
		gl.View(c)
	}, 500, 360)
	_ = tt.Image()

	// 1. 验证初始双向绑定已将详情图 XRange 锁定在 [2.0, 5.0]
	dx0, dx1 := detailPlot.ViewBox().XRange()
	if math.Abs(dx0-2.0) > 1e-6 || math.Abs(dx1-5.0) > 1e-6 {
		t.Fatalf("detail XRange mismatch before drag: [%v, %v]", dx0, dx1)
	}

	// 2. 在概览图中按住 LinearRegion 中心阴影带向右平移
	pr := overviewPlot.PlotRectLocal()
	vt := overviewPlot.ViewBox().Transform(pr)
	midSX := vt.DataToScreenX(3.5)
	midSY := pr.Y + pr.H*0.5
	targetSX := vt.DataToScreenX(5.0) // 向右平移 +1.5s -> 预期 [3.5, 6.5]
	tt.Press(midSX, midSY)
	tt.Move(targetSX, midSY)
	tt.Release(targetSX, midSY)

	r0, r1 := region.Region()
	dx0, dx1 = detailPlot.ViewBox().XRange()
	if math.Abs(r0-3.5) > 0.15 || math.Abs(r1-6.5) > 0.15 {
		t.Fatalf("LinearRegion body drag unexpected range: [%v, %v]", r0, r1)
	}
	if math.Abs(dx0-r0) > 1e-6 || math.Abs(dx1-r1) > 1e-6 {
		t.Fatalf("detail ViewBox failed to follow LinearRegion: region=[%v,%v], detail=[%v,%v]", r0, r1, dx0, dx1)
	}

	// 3. 反向验证：修改详情图视口 XRange 时，概览图 LinearRegion 自动无环同步
	detailPlot.ViewBox().SetXRange(1.25, 2.75, 0)
	r0, r1 = region.Region()
	if math.Abs(r0-1.25) > 1e-6 || math.Abs(r1-2.75) > 1e-6 {
		t.Fatalf("LinearRegion failed reverse sync from detail ViewBox: [%v, %v]", r0, r1)
	}

	// 4. 验证零分配区间样本提取 ExtractXY
	var bufX, bufY [256]float64
	xs, ys := region.ExtractXY(src, bufX[:0], bufY[:0])
	if len(xs) != 151 || len(ys) != 151 {
		t.Fatalf("ExtractXY count = %d, want 151 samples in [1.25, 2.75] at dt=0.01", len(xs))
	}
	allocs := testing.AllocsPerRun(10, func() {
		_, _ = region.ExtractXY(src, bufX[:0], bufY[:0])
	})
	if allocs != 0 {
		t.Fatalf("LinearRegion.ExtractXY allocations = %v, want 0", allocs)
	}
}

func TestRectROIAndLineROIProfileExtraction(t *testing.T) {
	// 构造线性平面标量场 f(x, y) = 2*x + 3*y（物理边界 [0, 10] x [0, 10]，11x11 网格中心对齐或双线性精确可验）
	const cols, rows = 20, 20
	mat := make([]float64, cols*rows)
	for r := range rows {
		y := (float64(r) + 0.5) / float64(rows) * 10.0
		for c := range cols {
			x := (float64(c) + 0.5) / float64(cols) * 10.0
			mat[r*cols+c] = 2.0*x + 3.0*y
		}
	}

	imgItem := items.NewImage(items.WithPhysicalRect(0, 10, 0, 10))
	imgItem.SetData(cols, rows, mat)

	rectROI := items.NewRectROI(2.0, 5.0, 3.0, 7.0)
	lineROI := items.NewLineROI(1.0, 2.0, 7.0, 8.0)

	plot := mygograph.NewPlot()
	plot.AddItem(imgItem, rectROI, lineROI)
	tt := ui.NewTester(func(c *ui.Context) {
		plot.View(c)
	}, 360, 280)
	_ = tt.Image()

	// 1. 验证 RectROI 子矩阵提取
	_, _, subC, subR, vals := rectROI.ExtractSubGrid(imgItem, nil)
	if subC != 6 || subR != 8 || len(vals) != 48 {
		t.Fatalf("RectROI.ExtractSubGrid got %dx%d (len=%d), want 6x8 (48)", subC, subR, len(vals))
	}

	// 2. 验证 LineROI 双线性横截面采样精度与 0 分配复用
	var dBuf, vBuf [64]float64
	dists, prof := lineROI.SampleProfile(imgItem, 25, dBuf[:0], vBuf[:0])
	if len(dists) != 25 || len(prof) != 25 {
		t.Fatalf("SampleProfile len = %d, want 25", len(prof))
	}
	// 在网格内部 [1,7]x[2,8]，线性函数 f(x,y)=2x+3y 的双线性插值精确等于解析值！
	for i := range 25 {
		frac := float64(i) / 24.0
		px := 1.0 + frac*6.0
		py := 2.0 + frac*6.0
		want := 2.0*px + 3.0*py
		if math.Abs(prof[i]-want) > 1e-3 {
			t.Fatalf("SampleProfile[%d] at (%v,%v) = %v, want %v", i, px, py, prof[i], want)
		}
	}
	allocs := testing.AllocsPerRun(10, func() {
		_, _ = lineROI.SampleProfile(imgItem, 25, dBuf[:0], vBuf[:0])
	})
	if allocs != 0 {
		t.Fatalf("LineROI.SampleProfile allocations = %v, want 0", allocs)
	}

	// 3. 验证交互拖拽 LineROI 终点 P2
	pr := plot.PlotRectLocal()
	vt := plot.ViewBox().Transform(pr)
	p2SX := vt.DataToScreenX(7.0)
	p2SY := vt.DataToScreenY(8.0)
	newSX := vt.DataToScreenX(8.5)
	newSY := vt.DataToScreenY(6.0)
	tt.Press(p2SX, p2SY)
	tt.Move(newSX, newSY)
	tt.Release(newSX, newSY)
	_, _, x2After, y2After := lineROI.Endpoints()
	if math.Abs(x2After-8.5) > 0.2 || math.Abs(y2After-6.0) > 0.2 {
		t.Fatalf("LineROI P2 drag unexpected endpoint: (%v, %v), want ~(8.5, 6.0)", x2After, y2After)
	}
}

func TestScatter1MSpatialHashPickupLatencyAndZeroAlloc(t *testing.T) {
	const nPts = 1_000_000
	xs := make([]float64, nPts)
	ys := make([]float64, nPts)
	for i := range nPts {
		// 确定性准均匀 Halton/黄金角分布铺满 [0, 1000] x [0, 1000]
		u := float64(i) / float64(nPts)
		v := math.Mod(float64(i)*0.6180339887498949, 1.0)
		xs[i] = u * 1000.0
		ys[i] = v * 1000.0
	}
	// 植入一个已知精确坐标的测试目标点
	const targetIdx = 543210
	xs[targetIdx] = 432.125
	ys[targetIdx] = 678.875

	src := data.NewXYSlice(xs, ys)
	sc := items.NewScatter(
		src,
		items.WithScatterName("Cloud1M"),
		items.WithMarkerShape(items.MarkerDiamond),
		items.WithMarkerSize(5.0),
	)
	sc.EnsureSpatialIndex()

	vb := mygograph.NewViewBox()
	vb.SetXRange(400, 460, 0)
	vb.SetYRange(650, 710, 0)
	pr := ui.Rect{X: 50, Y: 20, W: 800, H: 600}
	vt := vb.Transform(pr)

	// 在目标点偏离 (+2.5 DIP, -1.5 DIP) 处发起悬停拾取
	lx := (vt.DataToScreenX(432.125) - pr.X) + 2.5
	ly := (vt.DataToScreenY(678.875) - pr.Y) - 1.5

	pt, ok := sc.SnapNearest(lx, ly, pr, vb)
	if !ok {
		t.Fatal("expected SnapNearest to hit target point in 1M scatter cloud")
	}
	if pt.DataX != 432.125 || pt.DataY != 678.875 {
		t.Fatalf("SnapNearest picked (%v, %v), want exact (432.125, 678.875)", pt.DataX, pt.DataY)
	}

	allocs := testing.AllocsPerRun(20, func() {
		_, _ = sc.SnapNearest(lx, ly, pr, vb)
	})
	if allocs != 0 {
		t.Fatalf("ScatterItem.SnapNearest allocations = %v, want 0 allocs/op", allocs)
	}

	const queries = 1000
	start := time.Now()
	for i := range queries {
		qx := float32(100 + (i%600))
		qy := float32(80 + ((i*7)%440))
		_, _ = sc.SnapNearest(qx, qy, pr, vb)
	}
	avg := time.Since(start) / queries
	t.Logf("1,000,000-point ScatterItem.SnapNearest avg pickup = %v", avg)
	if avg > 100*time.Microsecond {
		t.Fatalf("1M scatter hover pickup %v exceeds 0.1ms (100μs) budget", avg)
	}
}

func TestExportHiDPIPNGAndContextMenuViewportCSV(t *testing.T) {
	xs := []float64{0.0, 1.0, 2.0, 3.0, 4.0}
	ys := []float64{10.5, 20.25, -5.125, 42.875, 99.0}
	plot := mygograph.NewPlot(mygograph.WithTitle("Export Verification"))
	plot.AddItem(items.NewCurve(data.NewXYSlice(xs, ys), items.WithName("SensorA")))
	plot.ViewBox().SetXRange(0.5, 3.5, 0) // 仅导出视口内 x ∈ [1.0, 3.0] 的 3 个点

	// 1. 验证 2x 超采样离屏 PNG 导出（逻辑 320x200 DIP -> 物理 640x400 px）
	var pngBuf bytes.Buffer
	if err := mygograph.EncodePNG(&pngBuf, plot, 320, 200, 2.0); err != nil {
		t.Fatalf("EncodePNG failed: %v", err)
	}
	decoded, err := png.Decode(&pngBuf)
	if err != nil {
		t.Fatalf("png.Decode failed: %v", err)
	}
	if decoded.Bounds().Dx() != 640 || decoded.Bounds().Dy() != 400 {
		t.Fatalf("2x supersampled PNG bounds = %v, want 640x400", decoded.Bounds())
	}

	tmpDir := t.TempDir()
	pngPath := filepath.Join(tmpDir, "figure_2x.png")
	if err := plot.SavePNG(pngPath, 320, 200, 2.0); err != nil {
		t.Fatalf("Plot.SavePNG failed: %v", err)
	}

	// 2. 验证视口全精度 CSV 导出及右键菜单剪贴板复制
	csvPath := filepath.Join(tmpDir, "viewport.csv")
	if err := mygograph.SaveViewportCSV(csvPath, plot); err != nil {
		t.Fatalf("SaveViewportCSV failed: %v", err)
	}
	csvText := mygograph.FormatViewportCSV(plot)
	lines := strings.Split(strings.TrimSpace(csvText), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected header + 3 viewport rows, got %d lines:\n%s", len(lines), csvText)
	}
	if lines[0] != "SensorA_X,SensorA_Y" || lines[3] != "3,42.875" {
		t.Fatalf("unexpected CSV content:\n%s", csvText)
	}

	// 触发右键上下文菜单 -> 点击 "Copy Viewport CSV" 并验证系统剪贴板写入
	tt := ui.NewTester(func(c *ui.Context) {
		plot.View(c)
	}, 360, 240)
	pr := plot.PlotRectLocal()
	tt.Advance(time.Second)
	tt.RightClickAt(pr.X+pr.W*0.5, pr.Y+pr.H*0.5)
	if err := tt.ChooseMenuItem("Copy Viewport CSV"); err != nil {
		t.Fatalf("clicking Copy Viewport CSV menu item failed: %v", err)
	}
	if gotClip := tt.Clipboard(); gotClip != csvText {
		t.Fatalf("clipboard mismatch:\ngot:  %q\nwant: %q", gotClip, csvText)
	}
}

func BenchmarkScatter1MSnapNearest(b *testing.B) {
	const nPts = 1_000_000
	xs := make([]float64, nPts)
	ys := make([]float64, nPts)
	for i := range nPts {
		xs[i] = float64(i) / float64(nPts) * 1000.0
		ys[i] = math.Mod(float64(i)*0.6180339887498949, 1.0) * 1000.0
	}
	sc := items.NewScatter(data.NewXYSlice(xs, ys))
	sc.EnsureSpatialIndex()

	vb := mygograph.NewViewBox()
	vb.SetXRange(200, 300, 0)
	vb.SetYRange(400, 500, 0)
	pr := ui.Rect{X: 50, Y: 20, W: 800, H: 600}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lx := float32(100 + (i & 511))
		ly := float32(60 + ((i * 3) & 255))
		_, _ = sc.SnapNearest(lx, ly, pr, vb)
	}
}
