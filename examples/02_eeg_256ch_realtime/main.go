// 示例 02：256 导联 × 1000Hz 高密度脑电（EEG/iEEG）双模 GPU 实时监护仪（滚动走纸 / 擦除扫描 / 去直流 / 独奏放大）
//
// 运行方式：
//
//	CGO_ENABLED=0 go run ./examples/02_eeg_256ch_realtime
//	CGO_ENABLED=0 go run ./examples/02_eeg_256ch_realtime -export snapshot_02.png
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/items"
)

func main() {
	exportPath := flag.String("export", "", "optional path to export 2x HiDPI PNG and exit")
	flag.Parse()

	const (
		numChannels = 256
		sampleRate  = 1000.0 // 1 kHz / channel -> 256,000 samples/sec
		windowSec   = 10.0
	)

	ring := data.NewMultiRingBufferPerChannel(numChannels, sampleRate, 15_000)
	stdLeads := []string{"Fp1", "Fp2", "F7", "F3", "Fz", "F4", "F8", "T7", "C3", "Cz", "C4", "T8", "P7", "P3", "Pz", "P4", "P8", "O1", "Oz", "O2"}
	names := make([]string, numChannels)
	for ch := range numChannels {
		if ch < len(stdLeads) {
			names[ch] = fmt.Sprintf("%s (%03d)", stdLeads[ch], ch+1)
		} else {
			names[ch] = fmt.Sprintf("iEEG-%03d", ch+1)
		}
	}
	ring.SetChannelNames(names)

	// 合成一段初始 6 秒生理脑电数据（含 Alpha 纺锤波、电极极化大直流偏置及局灶棘慢波发放）
	genSample := func(ch int, sampleIdx int64) float64 {
		t := float64(sampleIdx) / sampleRate
		phase := float64(ch) * 0.27
		// 10 Hz Alpha 节律纺锤包络 + 22 Hz Beta 微弱背景
		spindle := 0.5 + 0.5*math.Sin(2*math.Pi*0.4*t+float64(ch)*0.05)
		alpha := 48e-6 * spindle * math.Sin(2*math.Pi*10.2*t+phase)
		beta := 16e-6 * math.Sin(2*math.Pi*22.5*t+phase*1.7)

		// 模拟前额导联高达 +1.8mV 的电极极化缓变直流基线漂移（开启 DC Removal 后自动拉平）
		var dcDrift float64
		if ch%16 == 0 {
			dcDrift = 1.8e-3 + 120e-6*math.Sin(2*math.Pi*0.12*t)
		}

		// 在 Ch028..Ch035 模拟每 2.5 秒一次的局灶性高幅棘波放电（跨越相邻导联而不硬截断）
		var spike float64
		if ch >= 28 && ch <= 35 {
			cyc := math.Mod(t, 2.5)
			if cyc > 1.10 && cyc < 1.16 {
				u := (cyc - 1.13) / 0.008
				spike = 280e-6 * math.Exp(-0.5*u*u)
			}
		}
		return dcDrift + alpha + beta + spike
	}

	const preloadSamples = 6500
	initBlocks := make([][]float64, numChannels)
	for ch := range numChannels {
		blk := make([]float64, preloadSamples)
		for i := range preloadSamples {
			blk[i] = genSample(ch, int64(i))
		}
		initBlocks[ch] = blk
	}
	ring.PushChannelBlocks(initBlocks)

	mc := items.NewMultiChannel(
		ring,
		items.WithDisplayMode(items.DisplaySweepMode),
		items.WithWindowSeconds(windowSec),
		items.WithEraseGapSeconds(0.18),
		items.WithChannelScale(200e-6), // ±100 μV 单车道基准量程
		items.WithDCRemoval(true),
	)
	mc.SetChannelMuted(4, true) // 预设第 5 导联为接触不良坏道灰化

	chAxis := mygograph.NewChannelAxis(mygograph.AxisLeft, names).SetLabel("256 EEG Leads")
	bottomAx := mygograph.NewTimeAxis(mygograph.AxisBottom, "Sweep / Time Window")

	th := mygograph.DarkTheme()
	plot := mygograph.NewPlot(
		mygograph.WithTitle("256-Channel x 1000 Hz Real-Time High-Density EEG/iEEG Monitor (Single-DrawCall GPU Kage Shader)"),
		mygograph.WithTheme(th),
		mygograph.WithLeftAxis(chAxis),
		mygograph.WithBottomAxis(bottomAx),
		mygograph.WithGrid(true, false),
	)
	plot.ViewBox().SetInvertY(true) // 临床标准：第 1 导联位于最上方
	plot.AddItem(mc)
	ring.SetWakeFunc(plot.WakeFunc())

	var paused atomic.Bool
	soloActive := false

	buildUI := func(c *ui.Context) {
		if mc.DisplayMode() == items.DisplayRollMode && !soloActive {
			tNow := ring.LatestTime()
			if tNow > windowSec {
				plot.ViewBox().SetXRange(tNow-windowSec, tNow, 0)
			}
		}

		ui.Column(c).Fill().Background(th.Background).Children(func() {
			ui.Row(c).FillWidth().Padding(8, 12).Gap(8).Background(th.PanelSurface).Children(func() {
				pauseLabel := "Pause Stream (0% Idle CPU)"
				if paused.Load() {
					pauseLabel = "Resume 256ch Stream"
				}
				if ui.Button(c, pauseLabel).Clicked() {
					paused.Store(!paused.Load())
				}

				modeLabel := "Mode: Sweep (Erase-Bar)"
				if mc.DisplayMode() == items.DisplayRollMode {
					modeLabel = "Mode: Roll Paper"
				}
				if ui.Button(c, modeLabel).Clicked() {
					if mc.DisplayMode() == items.DisplaySweepMode {
						mc.SetDisplayMode(items.DisplayRollMode)
					} else {
						mc.SetDisplayMode(items.DisplaySweepMode)
						plot.ViewBox().SetXRange(0, windowSec, 0)
					}
					plot.Invalidate()
				}

				dcLabel := "DC Removal: ON"
				if !mc.DCRemoval() {
					dcLabel = "DC Removal: OFF"
				}
				if ui.Button(c, dcLabel).Clicked() {
					mc.SetDCRemoval(!mc.DCRemoval())
					plot.Invalidate()
				}

				if ui.Button(c, "Gain + (Alt+Wheel)").Clicked() {
					mc.SetGain(mc.Gain() * 1.35)
					plot.Invalidate()
				}
				if ui.Button(c, "Gain -").Clicked() {
					mc.SetGain(mc.Gain() / 1.35)
					plot.Invalidate()
				}

				soloLabel := "Solo Focus Ch031"
				if soloActive {
					soloLabel = "Restore All 256 Leads"
				}
				if ui.Button(c, soloLabel).Clicked() {
					soloActive = !soloActive
					if soloActive {
						mc.SetSelectedChannel(30)
						plot.ViewBox().SetYRange(29.1, 30.9, 0)
					} else {
						mc.SetSelectedChannel(-1)
						plot.ViewBox().SetYRange(-0.8, float64(numChannels-1)+0.8, 0)
					}
				}

				sensUV := mc.EffectiveSensitivity() * 1e6
				ui.Text(c, fmt.Sprintf("Sensitivity: %.0f μV/ch | Double-click any lead to Solo-Zoom", sensUV)).
					FontSize(12).TextColor(th.AxisText)
			})
			plot.View(c).Fill().Grow(1)
		})
	}

	if *exportPath != "" {
		if err := mygograph.SavePNGFunc(*exportPath, buildUI, 1360, 860, 2.0); err != nil {
			log.Fatal(err)
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动 50Hz 硬件 DMA 模拟协程（每 20ms 推入 20 帧 x 256 导联 = 1000 Hz/导联）
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		const batchFrames = 20
		interleaved := make([]float64, batchFrames*numChannels)
		sampleCursor := int64(preloadSamples)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if paused.Load() {
					continue
				}
				for f := range batchFrames {
					sIdx := sampleCursor + int64(f)
					base := f * numChannels
					for ch := range numChannels {
						interleaved[base+ch] = genSample(ch, sIdx)
					}
				}
				sampleCursor += batchFrames
				ring.PushInterleaved(interleaved)
			}
		}
	}()

	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:   "mygograph — 02 256-Channel Real-Time EEG/iEEG Dual-Tier GPU Engine",
			Width:   1360,
			Height:  860,
			Content: ui.View(buildUI),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
