package items_test

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/items"
)

func TestMultiChannel256ShaderSweepDCRemovalAndSolo(t *testing.T) {
	const (
		nCh = 36 // > 32 导联阈值，自动激活 multistack.kage 着色器双模快路径
		fs  = 500.0
	)
	ring := data.NewMultiRingBufferPerChannel(nCh, fs, 6000) // 12s capacity
	names := make([]string, nCh)
	for i := range nCh {
		names[i] = fmt.Sprintf("EEG-%03d", i+1)
	}
	ring.SetChannelNames(names)

	// 注入 6 秒多导联信号，其中第 5 导联携带高达 +2.5V 的电极极化直流漂移（远超 ±100μV 脑电幅值）
	blocks := make([][]float64, nCh)
	const nSamples = 3000
	for ch := range nCh {
		b := make([]float64, nSamples)
		dc := 0.0
		if ch == 5 {
			dc = 2.5 // 2.5V 巨幅直流偏置
		}
		for i := range nSamples {
			tm := float64(i) / fs
			b[i] = dc + 60e-6*math.Sin(2*math.Pi*10.0*tm+float64(ch)*0.2)
		}
		blocks[ch] = b
	}
	ring.PushChannelBlocks(blocks)

	mc := items.NewMultiChannel(
		ring,
		items.WithWindowSeconds(5.0),
		items.WithChannelScale(200e-6),
		items.WithDCRemoval(true),
	)
	mc.SetChannelMuted(2, true)

	chAxis := mygograph.NewChannelAxis(mygograph.AxisLeft, names).SetLabel("Lead")
	plot := mygograph.NewPlot(
		mygograph.WithLeftAxis(chAxis),
		mygograph.WithBottomAxis(mygograph.NewTimeAxis(mygograph.AxisBottom, "Time")),
		mygograph.WithGrid(true, false),
	)
	plot.ViewBox().SetInvertY(true)
	plot.AddItem(mc)

	tt := ui.NewTester(func(c *ui.Context) {
		plot.View(c)
	}, 200, 140)

	// 1. 验证 GPU 着色器路径（36 > 32 导联）成功渲染非背景像素
	img := tt.Image()
	pr := plot.PlotRectLocal()
	bg := mygograph.DarkTheme().PlotBackground
	coloredPixels := 0
	xScan := int(pr.X + pr.W*0.5)
	for y := int(pr.Y + 4); y < int(pr.Y+pr.H-4); y++ {
		px := img.RGBAAt(xScan, y)
		if px.R != bg.R || px.G != bg.G || px.B != bg.B {
			coloredPixels++
		}
	}
	if coloredPixels < 10 {
		t.Fatalf("expected rendered multi-channel waveform pixels in shader mode, got only %d", coloredPixels)
	}

	// 2. 验证视图层去直流基线绝不污染底层原始环形缓冲数据（第 5 导联原始样本仍含 +2.5V）
	var checkEnv [16]data.MinMax
	if n := ring.DecimateWindow(5, 1.0, 2.0, 16, checkEnv[:]); n == 0 || checkEnv[0].Min < 2.4 {
		t.Fatalf("raw MultiRingBuffer was mutated by DCRemoval: n=%d min=%v", n, checkEnv[0].Min)
	}

	// 推过 260ms 让流进入空闲零功耗稳态，后续 Advance(600ms) 无需逐 VSync 步进软件着色器
	tt.Advance(280 * time.Millisecond)

	// 3. 验证擦除扫描模式（DisplaySweepMode）正常渲染扫描头与擦除间隙
	mc.SetDisplayMode(items.DisplaySweepMode)
	tt.Frame()
	_ = tt.Image()

	// 4. 验证 Alt + 鼠标滚轮无级调节灵敏度增益
	cx, cy := pr.X+pr.W*0.5, pr.Y+pr.H*0.5
	gainBefore := mc.Gain()
	tt.HoldModifiers(ui.Alt)
	tt.Wheel(cx, cy, 0, -120)
	tt.HoldModifiers(0)
	if !(mc.Gain() > gainBefore*1.2) {
		t.Fatalf("Alt+Wheel should increase gain: before=%v after=%v", gainBefore, mc.Gain())
	}

	// 5. 验证双击单导联进入 Solo 独奏放大（自动切入 <=32 导联高精度矢量折线模式），再次双击一键还原
	tt.Advance(600 * time.Millisecond)
	tt.ClickAt(cx, cy)
	tt.ClickAt(cx, cy) // 双击
	ySolo0, ySolo1 := plot.ViewBox().YRange()
	if (ySolo1 - ySolo0) > 3.0 {
		t.Fatalf("double-click should solo-zoom single channel: YRange=[%v, %v]", ySolo0, ySolo1)
	}

	tt.Advance(600 * time.Millisecond)
	tt.ClickAt(cx, cy)
	tt.ClickAt(cx, cy) // 再次双击还原全导联视图
	yFull0, yFull1 := plot.ViewBox().YRange()
	if (yFull1 - yFull0) < 30.0 {
		t.Fatalf("second double-click should restore all 36 channels: YRange=[%v, %v]", yFull0, yFull1)
	}
}

func TestMultiRingScanWindowB8BlockAndNaNAccuracy(t *testing.T) {
	ring := data.NewMultiRingBufferPerChannel(2, 1000.0, 500)
	// 先推入 350 帧再推入 400 帧强制触发环形回绕，且第 1 通道夹杂 NaN 与单样本尖峰脉冲
	blk0 := make([][]float64, 2)
	blk0[0] = make([]float64, 350)
	blk0[1] = make([]float64, 350)
	ring.PushChannelBlocks(blk0)

	blk1 := make([][]float64, 2)
	blk1[0] = make([]float64, 400)
	blk1[1] = make([]float64, 400)
	for i := range 400 {
		blk1[0][i] = math.Sin(float64(i) * 0.1)
		blk1[1][i] = -0.5 * math.Cos(float64(i)*0.07)
	}
	blk1[0][123] = 42.5  // 正尖峰毛刺（落入 B=8 块内部）
	blk1[0][124] = -37.25 // 负尖峰毛刺
	blk1[1][200] = math.NaN()
	blk1[1][201] = 19.75
	ring.PushChannelBlocks(blk1)

	const buckets = 10 // 每桶 40 样本（>= 16，触发 B=8 块极值表快路径）
	slot0 := make([]int32, buckets)
	sLen := make([]int32, buckets)
	minArr := make([]float32, buckets)
	maxArr := make([]float32, buckets)
	valArr := make([]byte, buckets)
	ref := make([]data.MinMax, buckets)

	ring.ScanWindowMinMax(0.35, 0.75, buckets, slot0, sLen, minArr, maxArr, valArr, func(ch int) {
		ring.DecimateWindow(ch, 0.35, 0.75, buckets, ref)
		for b := range buckets {
			if (valArr[b] != 0) != ref[b].Valid {
				t.Fatalf("ch=%d b=%d valid mismatch: got %v want %v", ch, b, valArr[b] != 0, ref[b].Valid)
			}
			if valArr[b] != 0 {
				if math.Abs(float64(minArr[b]-ref[b].Min)) > 1e-5 || math.Abs(float64(maxArr[b]-ref[b].Max)) > 1e-5 {
					t.Fatalf("ch=%d b=%d minmax mismatch: got [%v, %v] want [%v, %v]",
						ch, b, minArr[b], maxArr[b], ref[b].Min, ref[b].Max)
				}
			}
		}
	})
}

func TestMultiChannel256Gate3ZeroAllocAndLatency(t *testing.T) {
	const (
		nCh     = 256
		fs      = 1000.0
		winSec  = 10.0
		samples = 10000 // 256 * 10,000 = 2,560,000 点实时窗口
		buckets = 1000
	)
	ring := data.NewMultiRingBufferPerChannel(nCh, fs, 12000)
	blocks := make([][]float64, nCh)
	for ch := range nCh {
		b := make([]float64, samples)
		for i := range samples {
			b[i] = 80e-6 * math.Sin(float64(i)*0.03+float64(ch)*0.1)
		}
		blocks[ch] = b
	}
	ring.PushChannelBlocks(blocks)

	mc := items.NewMultiChannel(
		ring,
		items.WithWindowSeconds(winSec),
		items.WithDCRemoval(true),
		items.WithRenderTier(items.TierShader),
	)
	th := mygograph.DarkTheme()
	// 预热一次完成首帧纹理缓冲分配
	mc.PrepareShaderFrame(0, winSec, buckets, th)

	allocs := testing.AllocsPerRun(10, func() {
		mc.PrepareShaderFrame(0, winSec, buckets, th)
	})
	if allocs != 0 {
		t.Fatalf("Gate 3 steady-state allocations = %v, want 0 allocs/op", allocs)
	}

	// AllocsPerRun 内部连续触发 10 次 STW GC 清空缓存并降频，预热 4 帧进入稳态后采集 30 帧平均时延
	for range 4 {
		mc.PrepareShaderFrame(0, winSec, buckets, th)
	}
	const runs = 30
	start := time.Now()
	for range runs {
		mc.PrepareShaderFrame(0, winSec, buckets, th)
	}
	avg := time.Since(start) / runs
	t.Logf("Gate 3 [256ch x 1000Hz x 10s = 2.56M pts -> 1000 buckets + DC removal + 16-bit GPU pack]: avg = %v", avg)
	if avg > 6*time.Millisecond {
		t.Fatalf("Gate 3 latency %v exceeds 6ms budget", avg)
	}
}

func BenchmarkMultiChannel256_1kHz_10s(b *testing.B) {
	const (
		nCh     = 256
		fs      = 1000.0
		winSec  = 10.0
		samples = 10000
		buckets = 1000
	)
	ring := data.NewMultiRingBufferPerChannel(nCh, fs, 12000)
	blocks := make([][]float64, nCh)
	for ch := range nCh {
		arr := make([]float64, samples)
		for i := range samples {
			arr[i] = 80e-6 * math.Sin(float64(i)*0.03+float64(ch)*0.1)
		}
		blocks[ch] = arr
	}
	ring.PushChannelBlocks(blocks)

	mc := items.NewMultiChannel(
		ring,
		items.WithWindowSeconds(winSec),
		items.WithDCRemoval(true),
		items.WithRenderTier(items.TierShader),
	)
	th := mygograph.DarkTheme()
	mc.PrepareShaderFrame(0, winSec, buckets, th)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mc.PrepareShaderFrame(0, winSec, buckets, th)
	}
}
