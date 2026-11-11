// 示例 01：千万~亿级采样点超长瞬态示波器 + 多级金字塔保峰缩放 + 双游标工程测量（Δt / f / Vpp / RMS）
//
// 运行方式：
//
//	CGO_ENABLED=0 go run ./examples/01_million_points_cursor
//	CGO_ENABLED=0 go run ./examples/01_million_points_cursor -points 100000000
//	CGO_ENABLED=0 go run ./examples/01_million_points_cursor -export snapshot_01.png
package main

import (
	"flag"
	"fmt"
	"log"
	"math"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/items"
)

func main() {
	numPoints := flag.Int("points", 10_000_000, "total waveform samples (supports 100,000,000+)")
	exportPath := flag.String("export", "", "optional path to export 2x HiDPI PNG and exit without opening window")
	flag.Parse()

	n := max(*numPoints, 10_000)
	const sampleRate = 10_000_000.0 // 10 MSa/s -> dt = 100 ns

	// 生成含调幅载波 + 低频包络 + 3 处单样本纳秒级狄拉克尖峰毛刺（Dirac Glitch）的超长测试波形
	raw := make([]float64, n)
	invFs := 1.0 / sampleRate
	for i := range n {
		t := float64(i) * invFs
		carrier := 0.85 * math.Sin(2*math.Pi*1200.0*t)
		mod := 1.0 + 0.45*math.Cos(2*math.Pi*35.0*t)
		harmonic := 0.22 * math.Sin(2*math.Pi*4800.0*t+0.4)
		raw[i] = carrier*mod + harmonic
	}
	spikeIdx1 := n / 4
	spikeIdx2 := n / 2
	spikeIdx3 := (3 * n) / 4
	raw[spikeIdx1] = 4.85  // +4.85V 单样本正尖峰（仅宽 100ns，传统抽稀 100% 漏检）
	raw[spikeIdx2] = -4.60 // -4.60V 单样本负欠冲毛刺
	raw[spikeIdx3] = 4.35

	// 构建分支因子 B=64 的零分配多级极值金字塔，全览与任意倍率缩放均保持 < 0.2ms 响应且毛刺 100% 可见
	pyramid := data.NewMinMaxPyramid(0.0, sampleRate, raw)
	totalDuration := float64(n-1) / sampleRate
	tSpike1 := float64(spikeIdx1) / sampleRate

	darkMode := true
	theme := mygograph.DarkTheme()

	bottomAx := mygograph.NewTimeAxis(mygograph.AxisBottom, "Capture Time")
	leftAx := mygograph.NewAxis(mygograph.AxisLeft).SetLabel("Channel 1").SetUnit("V")

	plot := mygograph.NewPlot(
		mygograph.WithTitle(fmt.Sprintf("Ultra-Deep Capture Oscilloscope (%d Points @ 10 MSa/s, MinMaxPyramid B=64)", n)),
		mygograph.WithTheme(theme),
		mygograph.WithBottomAxis(bottomAx),
		mygograph.WithLeftAxis(leftAx),
		mygograph.WithGrid(true, true),
	)

	curve := items.NewCurve(
		pyramid,
		items.WithName("CH1 Carrier+Glitch"),
		items.WithColor(theme.SeriesColor(0)),
		items.WithWidth(1.35),
	)
	cursors := items.NewCursorMeasure(
		totalDuration*0.24,
		totalDuration*0.26,
		items.WithMeasureSource(pyramid),
		items.WithHorizontalCursors(-1.25, 1.25),
	)
	crosshair := items.NewCrosshair(items.WithSnapTarget(curve))

	plot.AddItem(curve, cursors, crosshair)

	statusMsg := "Tip: Left-drag pan | Right-drag non-uniform zoom | Ctrl/Shift+Wheel single-axis zoom | Drag T1/T2 cursors"

	buildUI := func(c *ui.Context) {
		th := mygograph.DarkTheme()
		if !darkMode {
			th = mygograph.LightTheme()
		}
		ui.Column(c).Fill().Background(th.Background).Children(func() {
			ui.Row(c).FillWidth().Padding(8, 12).Gap(8).Background(th.PanelSurface).Children(func() {
				if ui.Button(c, "Zoom to 100ns Glitch #1").Clicked() {
					plot.ViewBox().PushZoom()
					plot.ViewBox().SetXRange(tSpike1-8e-6, tSpike1+8e-6, 0)
					plot.ViewBox().SetYRange(-5.2, 5.2, 0)
					cursors.SetXPositions(tSpike1-1e-6, tSpike1+1e-6)
				}
				if ui.Button(c, "Full Span (AutoRange)").Clicked() {
					plot.AutoRange()
				}
				modeLabel := "Mode: Pan (3-Btn)"
				if plot.ViewBox().MouseMode() == mygograph.RectMode {
					modeLabel = "Mode: Rect Zoom"
				}
				if ui.Button(c, modeLabel).Clicked() {
					if plot.ViewBox().MouseMode() == mygograph.PanMode {
						plot.ViewBox().SetMouseMode(mygograph.RectMode)
					} else {
						plot.ViewBox().SetMouseMode(mygograph.PanMode)
					}
				}
				if ui.Button(c, "Switch Dark/Light").Clicked() {
					darkMode = !darkMode
					if darkMode {
						plot.SetTheme(mygograph.DarkTheme())
					} else {
						plot.SetTheme(mygograph.LightTheme())
					}
				}
				if ui.Button(c, "Export 2x PNG").Clicked() {
					if err := plot.SavePNG("oscilloscope_2x.png", 1280, 720, 2.0); err == nil {
						statusMsg = "Saved 2560x1440 HiDPI PNG -> oscilloscope_2x.png"
					}
				}
				ui.Text(c, statusMsg).FontSize(12).TextColor(th.AxisText)
			})
			plot.View(c).Fill().Grow(1)
		})
	}

	if *exportPath != "" {
		if err := mygograph.SavePNGFunc(*exportPath, buildUI, 1280, 760, 2.0); err != nil {
			log.Fatal(err)
		}
		return
	}

	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:   "mygograph — 01 Ultra-Deep Million/Hundred-Million Point Oscilloscope",
			Width:   1280,
			Height:  780,
			Content: ui.View(buildUI),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
