// 示例 04：概览-详情双向联动区间截取（LinearRegion） + 二维激光光斑/干涉热力图 RectROI & LineROI 实时横截面剖析台
//
// 运行方式：
//
//	CGO_ENABLED=0 go run ./examples/04_linked_region_roi
//	CGO_ENABLED=0 go run ./examples/04_linked_region_roi -export snapshot_04.png
package main

import (
	"flag"
	"fmt"
	"log"
	"math"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/colormap"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/items"
)

type spacerBox struct{ bg ui.Color }

func (s spacerBox) View(c *ui.Context) ui.Element {
	return ui.Box(c).Fill().Background(s.bg)
}

func main() {
	exportPath := flag.String("export", "", "optional path to export 2x HiDPI PNG and exit")
	flag.Parse()

	th := mygograph.DarkTheme()

	// ========================================================================
	// 左半区：一维长序列时域信号概览 + LinearRegion 双向联动详情放大 + 散点峰值拾取
	// ========================================================================
	const (
		nSamples = 50_000
		fs       = 5000.0 // 10s 总跨度
	)
	sigY := make([]float64, nSamples)
	var peakX, peakY []float64
	for i := range nSamples {
		t := float64(i) / fs
		env := math.Exp(-0.5*math.Pow((t-4.2)/1.4, 2)) + 0.65*math.Exp(-0.5*math.Pow((t-7.4)/0.8, 2))
		y := env*math.Sin(2*math.Pi*14.0*t) + 0.18*math.Cos(2*math.Pi*63.0*t)
		sigY[i] = y
		if i > 0 && i < nSamples-1 && i%180 == 0 && y > 0.35 {
			peakX = append(peakX, t)
			peakY = append(peakY, y)
		}
	}
	sigSrc := data.NewUniformSlice(0, fs, sigY)
	peakSrc := data.NewXYSlice(peakX, peakY)

	overviewPlot := mygograph.NewPlot(
		mygograph.WithTitle("1D Signal Overview (Drag Cyan Region Edges or Center Body)"),
		mygograph.WithTheme(th),
		mygograph.WithBottomAxis(mygograph.NewTimeAxis(mygograph.AxisBottom, "Time")),
		mygograph.WithLeftAxis(mygograph.NewAxis(mygograph.AxisLeft).SetLabel("Signal").SetUnit("V")),
	)
	detailPlot := mygograph.NewPlot(
		mygograph.WithTitle("Bidirectionally Linked Zoomed Detail + Peak Scatter Snap"),
		mygograph.WithTheme(th),
		mygograph.WithBottomAxis(mygograph.NewTimeAxis(mygograph.AxisBottom, "Zoomed Time")),
		mygograph.WithLeftAxis(mygograph.NewAxis(mygograph.AxisLeft).SetLabel("Detail").SetUnit("V")),
	)

	overviewPlot.AddItem(items.NewCurve(sigSrc, items.WithName("Envelope"), items.WithColor(th.SeriesColor(0))))
	detailCurve := items.NewCurve(
		sigSrc,
		items.WithName("Detail Wave"),
		items.WithColor(th.SeriesColor(2)),
		items.WithFillBaseline(0, ui.RGBA(88, 214, 141, 38)),
	)
	peakScatter := items.NewScatter(
		peakSrc,
		items.WithScatterName("Peaks"),
		items.WithScatterColor(th.SeriesColor(1)),
		items.WithMarkerShape(items.MarkerDiamond),
		items.WithMarkerSize(6.5),
	)
	detailPlot.AddItem(detailCurve, peakScatter, items.NewCrosshair())

	region := items.NewLinearRegion(3.4, 5.1, items.WithRegionLimits(0, 10.0))
	region.BindDetailViewBox(detailPlot.ViewBox())
	overviewPlot.AddItem(region)

	leftLayout := mygograph.NewGraphLayout().SetTheme(th)
	leftLayout.AddRowFlex(1.0, overviewPlot)
	leftLayout.AddRowFlex(1.3, detailPlot)

	// ========================================================================
	// 右半区：二维高斯激光干涉光场 ImageItem + RectROI 能量统计 + LineROI 横截面剖面曲线
	// ========================================================================
	const gridW, gridH = 256, 256
	field := make([]float64, gridW*gridH)
	for r := range gridH {
		y := -5.0 + 10.0*(float64(r)+0.5)/float64(gridH) // [-5, +5] mm
		for c := range gridW {
			x := -5.0 + 10.0*(float64(c)+0.5)/float64(gridW) // [-5, +5] mm
			r2a := (x+1.2)*(x+1.2) + (y-0.8)*(y-0.8)
			r2b := (x-1.6)*(x-1.6) + (y+1.1)*(y+1.1)
			gauss := 1.0*math.Exp(-r2a/2.6) + 0.78*math.Exp(-r2b/1.9)
			fringes := 1.0 + 0.28*math.Cos(3.6*x+2.4*y)
			field[r*gridW+c] = gauss * fringes
		}
	}

	beamCtrl := colormap.NewColorControl(colormap.Plasma(), 0.0, 1.35).SetUnit("Intensity", "W/cm²")
	beamImg := items.NewImage(
		items.WithImageName("BeamField"),
		items.WithColorControl(beamCtrl),
		items.WithPhysicalRect(-5.0, 5.0, -5.0, 5.0),
		items.WithSmoothInterpolation(true),
	)
	beamImg.SetData(gridW, gridH, field)
	colorBar := colormap.NewColorBar(beamCtrl).SetTheme(th)

	profileSrc := data.NewXYSlice(nil, nil)
	profileCurve := items.NewCurve(
		profileSrc,
		items.WithName("LineROI Profile"),
		items.WithColor(th.SeriesColor(3)),
		items.WithWidth(1.8),
		items.WithFillBaseline(0, ui.RGBA(255, 196, 61, 40)),
	)

	profilePlot := mygograph.NewPlot(
		mygograph.WithTitle("Live Bilinear Cross-Section Profile Along LineROI"),
		mygograph.WithTheme(th),
		mygograph.WithBottomAxis(mygograph.NewAxis(mygograph.AxisBottom).SetLabel("Arc Distance").SetUnit("mm")),
		mygograph.WithLeftAxis(mygograph.NewAxis(mygograph.AxisLeft).SetLabel("Intensity").SetUnit("W/cm²")),
	)
	profilePlot.AddItem(profileCurve, items.NewCrosshair(items.WithSnapTarget(profileCurve)))

	var profDists, profVals []float64
	lineROI := items.NewLineROI(-3.8, 2.6, 3.6, -2.4, items.WithLineROIColor(ui.RGB(255, 210, 64)))
	refreshProfile := func() {
		profDists, profVals = lineROI.SampleProfile(beamImg, 256, profDists[:0], profVals[:0])
		profileSrc.SetData(profDists, profVals)
		if len(profDists) > 0 {
			profilePlot.ViewBox().SetXRange(0, max(profDists[len(profDists)-1], 0.1), 0)
			profilePlot.ViewBox().SetYRange(0, 1.45, 0)
		}
		profilePlot.Invalidate()
	}
	lineROI = items.NewLineROI(-3.8, 2.6, 3.6, -2.4,
		items.WithLineROIColor(ui.RGB(255, 210, 64)),
		items.WithOnLineROIChanged(func(_, _, _, _ float64) {
			refreshProfile()
		}),
	)

	var subBuf []float64
	var roiSummary string
	rectROI := items.NewRectROI(-2.6, 0.2, -0.6, 2.2)
	refreshRectStats := func() {
		_, _, sc, sr, vals := rectROI.ExtractSubGrid(beamImg, subBuf[:0])
		subBuf = vals
		var sum, peak float64
		var cnt int
		for _, v := range vals {
			if !math.IsNaN(v) {
				sum += v
				if v > peak {
					peak = v
				}
				cnt++
			}
		}
		mean := 0.0
		if cnt > 0 {
			mean = sum / float64(cnt)
		}
		x0, x1, y0, y1 := rectROI.Rect()
		roiSummary = fmt.Sprintf("RectROI [%.2f..%.2f, %.2f..%.2f] mm (%dx%d px): Mean=%.3f W/cm², Peak=%.3f W/cm²",
			x0, x1, y0, y1, sc, sr, mean, peak)
	}
	rectROI = items.NewRectROI(-2.6, 0.2, -0.6, 2.2, items.WithOnRectChanged(func(_, _, _, _ float64) {
		refreshRectStats()
	}))

	refreshProfile()
	refreshRectStats()

	beamPlot := mygograph.NewPlot(
		mygograph.WithTitle("2D Interference Beam Intensity (Drag 8-Handle RectROI or 2-Point LineROI)"),
		mygograph.WithTheme(th),
		mygograph.WithBottomAxis(mygograph.NewAxis(mygograph.AxisBottom).SetLabel("X").SetUnit("mm")),
		mygograph.WithLeftAxis(mygograph.NewAxis(mygograph.AxisLeft).SetLabel("Y").SetUnit("mm")),
		mygograph.WithGrid(false, false),
	)
	beamPlot.ViewBox().SetAspectLocked(true, 1.0)
	beamPlot.ViewBox().SetXRange(-5.0, 5.0, 0)
	beamPlot.ViewBox().SetYRange(-5.0, 5.0, 0)
	beamPlot.AddItem(beamImg, rectROI, lineROI, items.NewCrosshair(items.WithSnapTarget(beamImg)))
	beamCtrl.OnChange(beamPlot.WakeFunc())

	rightLayout := mygograph.NewGraphLayout().SetTheme(th)
	rightLayout.AddRowFlex(1.35, beamPlot)
	rightLayout.AddFixedRight(colorBar, 76)
	rightLayout.AddRowFlex(1.0, profilePlot)
	rightLayout.AddFixedRight(spacerBox{bg: th.Background}, 76)

	var splitLR float32 = 48.0

	buildUI := func(c *ui.Context) {
		ui.Column(c).Fill().Background(th.Background).Children(func() {
			ui.Row(c).FillWidth().Padding(8, 12).Gap(10).Background(th.PanelSurface).Children(func() {
				if ui.Button(c, "Reset ROIs").Clicked() {
					region.SetRegion(3.4, 5.1)
					rectROI.SetRect(-2.6, 0.2, -0.6, 2.2)
					lineROI.SetEndpoints(-3.8, 2.6, 3.6, -2.4)
				}
				if ui.Button(c, "Diagonal Line Slice").Clicked() {
					lineROI.SetEndpoints(-4.5, -4.5, 4.5, 4.5)
				}
				ui.Text(c, roiSummary).FontSize(12).TextColor(th.AxisText)
			})
			ui.SplitPercent(c, &splitLR,
				func() { leftLayout.View(c).Fill() },
				func() { rightLayout.View(c).Fill() },
			).Fill().Grow(1)
		})
	}

	if *exportPath != "" {
		if err := mygograph.SavePNGFunc(*exportPath, buildUI, 1440, 860, 2.0); err != nil {
			log.Fatal(err)
		}
		return
	}

	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:   "mygograph — 04 Linked Overview-Detail LinearRegion & 2D Beam ROI Profiler",
			Width:   1440,
			Height:  860,
			Content: ui.View(buildUI),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
