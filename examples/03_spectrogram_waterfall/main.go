// 示例 03：纯 Go 实时 STFT 时频谱瀑布图 + HistogramLUT 0ms GPU 伪彩调窗 + 上下双图 0px 像素级轴对齐
//
// 运行方式：
//
//	CGO_ENABLED=0 go run ./examples/03_spectrogram_waterfall
//	CGO_ENABLED=0 go run ./examples/03_spectrogram_waterfall -export snapshot_03.png
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"math/bits"
	"math/cmplx"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/colormap"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/items"
)

type spacerWidget struct{ bg ui.Color }

func (s spacerWidget) View(c *ui.Context) ui.Element {
	return ui.Box(c).Fill().Background(s.bg)
}

// radix2FFT 执行原地基-2 Cooley-Tukey 快速傅里叶变换（len(a) 须为 2 的整数次幂，100% 纯 Go 零外部依赖）。
func radix2FFT(a []complex128) {
	n := len(a)
	if n <= 1 {
		return
	}
	shift := 64 - bits.TrailingZeros(uint(n))
	for i := range n {
		j := int(bits.Reverse64(uint64(i)) >> shift)
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		theta := -2.0 * math.Pi / float64(size)
		wStep := cmplx.Rect(1.0, theta)
		for start := 0; start < n; start += size {
			w := complex(1.0, 0.0)
			for k := range half {
				u := a[start+k]
				t := w * a[start+k+half]
				a[start+k] = u + t
				a[start+k+half] = u - t
				w *= wStep
			}
		}
	}
}

func main() {
	exportPath := flag.String("export", "", "optional path to export 2x HiDPI PNG and exit")
	flag.Parse()

	const (
		sampleRate  = 16000.0 // 16 kHz 采样率 -> 奈奎斯特带宽 0 ~ 8 kHz
		fftSize     = 1024
		freqBins    = fftSize / 2 // 512 个正频率谱线桶
		hopSize     = 256         // 62.5 帧/秒 STFT 谱线更新率
		historyRows = 320         // 约 5.1 秒滚动历史深度
		waveWinSec  = 0.25        // 顶部时域示波窗展示最近 250ms 瞬时波形
	)

	hannWin := make([]float64, fftSize)
	var winSum float64
	for i := range fftSize {
		w := 0.5 * (1.0 - math.Cos(2.0*math.Pi*float64(i)/float64(fftSize-1)))
		hannWin[i] = w
		winSum += w
	}
	normFactor := 2.0 / max(winSum, 1.0)

	waveRing := data.NewRingBuffer(int(sampleRate*2.0), sampleRate)
	colorCtrl := colormap.NewColorControl(colormap.Turbo(), -88.0, -6.0).SetUnit("Power", "dBFS")

	// 环形瀑布图：横轴为频率 [0, 8000] Hz，纵轴为回溯历史时间 [0, 5.12] s（最新帧位于顶部）
	histSpanSec := float64(historyRows*hopSize) / sampleRate
	waterfall := items.NewWaterfall(
		freqBins,
		historyRows,
		-120.0,
		6.0,
		items.WithImageName("STFT Waterfall"),
		items.WithColorControl(colorCtrl),
		items.WithPhysicalRect(0, sampleRate*0.5, 0, histSpanSec),
	)
	histLUT := colormap.NewHistogramLUT(colorCtrl)

	// 信号合成器：线性调频 Chirp (500Hz -> 6800Hz) + 2400Hz 调幅双边带载波 + 周期性宽带脉冲 + 本底白噪
	var rngState uint64 = 0x9e3779b97f4a7c15
	nextNoise := func() float64 {
		rngState ^= rngState << 13
		rngState ^= rngState >> 7
		rngState ^= rngState << 17
		u := float64(rngState>>11) * (1.0 / (1 << 53))
		return (u - 0.5) * 0.006
	}

	genSample := func(idx int64) float64 {
		t := float64(idx) / sampleRate
		// 3.2s 周期往返扫频 Chirp
		period := 3.2
		tau := math.Mod(t, period)
		phaseChirp := 2 * math.Pi * (500.0*tau + 0.5*((6800.0-500.0)/period)*tau*tau)
		chirp := 0.42 * math.Sin(phaseChirp)

		// 2.4 kHz 调幅载波 + 5.2 kHz 高次谐波
		am := 0.25 * (1.0 + 0.7*math.Sin(2*math.Pi*18.0*t)) * math.Sin(2*math.Pi*2400.0*t)
		harm := 0.12 * math.Sin(2*math.Pi*5200.0*t)

		// 每 0.8s 一次瞬态宽带脉冲咔嗒声（在瀑布图上形成水平贯穿宽频亮线）
		var click float64
		if idx%int64(sampleRate*0.8) < 6 {
			click = 0.65
		}
		return chirp + am + harm + click + nextNoise()
	}

	fftBuf := make([]complex128, fftSize)
	slidingWin := make([]float64, fftSize)
	dbRow := make([]float64, freqBins)
	hopSamples := make([]float64, hopSize)

	var sampleCursor int64
	stepSTFT := func() {
		for i := range hopSize {
			hopSamples[i] = genSample(sampleCursor)
			sampleCursor++
		}
		waveRing.Push(hopSamples...)
		copy(slidingWin, slidingWin[hopSize:])
		copy(slidingWin[fftSize-hopSize:], hopSamples)

		for i := range fftSize {
			fftBuf[i] = complex(slidingWin[i]*hannWin[i], 0)
		}
		radix2FFT(fftBuf)
		for k := range freqBins {
			mag := cmplx.Abs(fftBuf[k]) * normFactor
			db := 20.0 * math.Log10(max(mag, 1e-6))
			dbRow[k] = min(max(db, -120.0), 6.0)
		}
		waterfall.PushRow(dbRow)
	}

	// 预热填充 260 行频谱历史使启动即呈现丰富纹理
	for range 260 {
		stepSTFT()
	}

	th := mygograph.DarkTheme()

	wavePlot := mygograph.NewPlot(
		mygograph.WithTitle("Real-Time Acoustic / Vibration Waveform (16 kHz)"),
		mygograph.WithTheme(th),
		mygograph.WithBottomAxis(mygograph.NewTimeAxis(mygograph.AxisBottom, "Time")),
		mygograph.WithLeftAxis(mygograph.NewAxis(mygograph.AxisLeft).SetLabel("Amplitude").SetUnit("V")),
	)
	waveCurve := items.NewCurve(waveRing, items.WithName("ADC In"), items.WithColor(th.SeriesColor(0)))
	wavePlot.AddItem(waveCurve, items.NewCrosshair(items.WithSnapTarget(waveCurve)))

	specPlot := mygograph.NewPlot(
		mygograph.WithTitle("16-bit GPU Single-DrawCall Cyclic-UV STFT Waterfall (Drag HistogramLUT Sliders for 0ms Windowing)"),
		mygograph.WithTheme(th),
		mygograph.WithBottomAxis(mygograph.NewAxis(mygograph.AxisBottom).SetLabel("Frequency").SetUnit("Hz")),
		mygograph.WithLeftAxis(mygograph.NewAxis(mygograph.AxisLeft).SetLabel("History Lag").SetUnit("s")),
		mygograph.WithGrid(false, false),
	)
	specPlot.ViewBox().SetInvertY(true)
	specPlot.ViewBox().SetXRange(0, sampleRate*0.5, 0)
	specPlot.ViewBox().SetYRange(0, histSpanSec, 0)
	specPlot.AddItem(waterfall, items.NewCrosshair(items.WithSnapTarget(waterfall)))

	waveRing.SetWakeFunc(wavePlot.WakeFunc())
	waterfall.SetWakeFunc(specPlot.WakeFunc())
	colorCtrl.OnChange(specPlot.WakeFunc())

	// 使用 GraphLayout 自动同步上下子图左侧 Y 轴像素边距（误差 0 px）并右侧挂载固定宽度 HistogramLUT
	layout := mygograph.NewGraphLayout().SetTheme(th)
	layout.AddRowFlex(1.0, wavePlot)
	layout.AddFixedRight(spacerWidget{bg: th.Background}, 140) // 保持上下绘图区右边缘严格垂直对齐
	layout.AddRowFlex(1.9, specPlot)
	layout.AddFixedRight(histLUT, 140)

	palettes := []*colormap.Colormap{
		colormap.Turbo(),
		colormap.Viridis(),
		colormap.Inferno(),
		colormap.Plasma(),
		colormap.Magma(),
		colormap.Coolwarm(),
	}
	palIdx := 0
	var paused atomic.Bool

	buildUI := func(c *ui.Context) {
		tLatest := waveRing.LatestTime()
		if tLatest > waveWinSec {
			wavePlot.ViewBox().SetXRange(tLatest-waveWinSec, tLatest, 0)
			wavePlot.ViewBox().SetYRange(-1.25, 1.25, 0)
		}

		ui.Column(c).Fill().Background(th.Background).Children(func() {
			ui.Row(c).FillWidth().Padding(8, 12).Gap(8).Background(th.PanelSurface).Children(func() {
				pLabel := "Pause STFT"
				if paused.Load() {
					pLabel = "Resume STFT"
				}
				if ui.Button(c, pLabel).Clicked() {
					paused.Store(!paused.Load())
				}
				if ui.Button(c, fmt.Sprintf("Colormap: %s", colorCtrl.Colormap().Name())).Clicked() {
					palIdx = (palIdx + 1) % len(palettes)
					colorCtrl.SetColormap(palettes[palIdx])
				}
				if ui.Button(c, "Reset dB Window [-88, -6]").Clicked() {
					colorCtrl.SetLevels(-88.0, -6.0)
				}
				if ui.Button(c, "High Contrast [-62, -10]").Clicked() {
					colorCtrl.SetLevels(-62.0, -10.0)
				}
				loDB, hiDB := colorCtrl.Levels()
				ui.Text(c, fmt.Sprintf("Window: [%.1f, %.1f] dBFS | Right-click HistogramLUT to pick any Colormap", loDB, hiDB)).
					FontSize(12).TextColor(th.AxisText)
			})
			layout.View(c).Fill().Grow(1)
		})
	}

	if *exportPath != "" {
		if err := mygograph.SavePNGFunc(*exportPath, buildUI, 1320, 820, 2.0); err != nil {
			log.Fatal(err)
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		ticker := time.NewTicker(16 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !paused.Load() {
					stepSTFT()
				}
			}
		}
	}()

	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:   "mygograph — 03 Pure-Go STFT Spectrogram Waterfall & 0ms GPU HistogramLUT",
			Width:   1320,
			Height:  820,
			Content: ui.View(buildUI),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
