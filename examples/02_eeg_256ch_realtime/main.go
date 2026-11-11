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

	ring := data.NewMultiRingBufferPerChannel(numChannels, sampleRate, 35_000)
	stdLeads := []string{"Fp1", "Fp2", "F7", "F3", "Fz", "F4", "F8", "T7", "C3", "Cz", "C4", "T8", "P7", "P3", "Pz", "P4", "P8", "O1", "Oz", "ECG-II"}
	names := make([]string, numChannels)
	for ch := range numChannels {
		if ch < len(stdLeads) {
			names[ch] = fmt.Sprintf("%s (%03d)", stdLeads[ch], ch+1)
		} else {
			names[ch] = fmt.Sprintf("iEEG-%03d", ch+1)
		}
	}
	ring.SetChannelNames(names)

	// 合成初始 6.5 秒多导生理信号（含 Alpha 纺锤波、电极极化直流偏置、第 20 导联 mV 级心电 QRS 波群及局灶棘波）
	genSample := func(ch int, sampleIdx int64) float64 {
		t := float64(sampleIdx) / sampleRate
		// 第 20 导联 (ch=19) 模拟标准 72 BPM (RR=0.833s) 的 1.4mV 心电 ECG-II 导联（验证 EEG+ECG 混采独立通道增益）
		if ch == 19 {
			rr := math.Mod(t, 0.8333)
			pWave := 0.14e-3 * math.Exp(-0.5*math.Pow((rr-0.12)/0.018, 2))
			qWave := -0.18e-3 * math.Exp(-0.5*math.Pow((rr-0.205)/0.006, 2))
			rWave := 1.35e-3 * math.Exp(-0.5*math.Pow((rr-0.220)/0.007, 2))
			sWave := -0.32e-3 * math.Exp(-0.5*math.Pow((rr-0.236)/0.008, 2))
			tWave := 0.28e-3 * math.Exp(-0.5*math.Pow((rr-0.41)/0.036, 2))
			return pWave + qWave + rWave + sWave + tWave
		}
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
		items.WithChannelScale(200e-6), // ±100 μV 单车道脑电基准量程
		items.WithDCRemoval(true),
		items.WithScaleBar(true),
	)
	mc.SetChannelMuted(4, true)            // 预设第 5 导联为接触不良坏道灰化
	mc.SetChannelSensitivity(19, 2.0e-3)   // 第 20 导联 (ECG-II) 独立设为 2 mV/ch（0.1x 衰减与 μV 级脑电同屏清晰共显）

	markers := items.NewEventMarkers(items.WithMarkersMultiChannel(mc))
	markers.Push(1.13, "Spike", ui.RGB(248, 113, 113))
	markers.Push(2.50, "S1-Stim", ui.RGB(250, 204, 21))
	markers.Push(3.63, "Spike", ui.RGB(248, 113, 113))
	markers.Push(5.00, "S2-Stim", ui.RGB(56, 189, 248))
	markers.Push(6.13, "Spike", ui.RGB(248, 113, 113))

	chAxis := mygograph.NewChannelAxis(mygograph.AxisLeft, names).SetLabel("256 EEG+ECG Leads")
	bottomAx := mygograph.NewTimeAxis(mygograph.AxisBottom, "Sweep / Time Window")

	th := mygograph.DarkTheme()
	plot := mygograph.NewPlot(
		mygograph.WithTitle("256-Channel x 1000 Hz Real-Time EEG + ECG Monitor (Right-Click Any Lead to Adjust Gain / Timebase / Mode)"),
		mygograph.WithTheme(th),
		mygograph.WithLeftAxis(chAxis),
		mygograph.WithBottomAxis(bottomAx),
		mygograph.WithGrid(true, false),
	)
	plot.ViewBox().SetInvertY(true) // 临床标准：第 1 导联位于最上方
	plot.AddItem(mc, markers)
	ring.SetWakeFunc(plot.WakeFunc())

	var paused atomic.Bool

	buildUI := func(c *ui.Context) {
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
						plot.ResumeLiveFollow()
					} else {
						mc.SetDisplayMode(items.DisplaySweepMode)
						plot.ViewBox().SetXRange(0, mc.WindowSeconds(), 0)
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

				soloActive := mc.SoloChannel() >= 0
				soloLabel := "Solo Focus ECG-II (020)"
				if soloActive {
					soloLabel = "Restore All 256 Leads"
				}
				if ui.Button(c, soloLabel).Clicked() {
					if soloActive {
						mc.SetSoloChannel(-1, plot.ViewBox())
					} else {
						mc.SetSoloChannel(19, plot.ViewBox())
					}
					plot.Invalidate()
				}

				sensUV := mc.EffectiveSensitivity() * 1e6
				ui.Text(c, fmt.Sprintf("Sens: %.0f μV/ch | Win: %.0fs | Right-click any lead for per-lead gain, timebase & bad-channel menu", sensUV, mc.WindowSeconds())).
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
					if sIdx%2500 == 1130 {
						markers.Push(float64(sIdx)/sampleRate, "Spike", ui.RGB(248, 113, 113))
					} else if sIdx%2500 == 0 {
						markers.Push(float64(sIdx)/sampleRate, "Stim", ui.RGB(56, 189, 248))
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
