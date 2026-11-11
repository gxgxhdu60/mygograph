package items_test

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/items"
)

func TestPlotPyQtGraphGesturesAndContextMenu(t *testing.T) {
	ys := make([]float64, 1001)
	for i := range ys {
		ys[i] = math.Sin(float64(i) * 0.02)
	}
	src := data.NewUniformSlice(0, 100, ys) // [0, 10s]

	plot := mygograph.NewPlot(
		mygograph.WithTitle("Oscilloscope Channel 1"),
		mygograph.WithBottomAxis(mygograph.NewTimeAxis(mygograph.AxisBottom, "Time")),
		mygograph.WithLeftAxis(mygograph.NewSIAxis(mygograph.AxisLeft, "Voltage", "V")),
		mygograph.WithLegend(true),
		mygograph.WithGrid(true, true),
	)
	curve := items.NewCurve(src, items.WithName("CH1"), items.WithFillBaseline(0, ui.RGBA(0, 212, 255, 36)))
	plot.AddItem(curve, items.NewCrosshair())

	tt := ui.NewTester(func(c *ui.Context) {
		plot.View(c)
	}, 800, 500)

	// 1. 验证初始 AutoRange 铺满 [0, 10]
	x0, x1 := plot.ViewBox().XRange()
	if math.Abs(x0-0) > 1e-6 || math.Abs(x1-10) > 1e-6 {
		t.Fatalf("initial AutoRange X = [%v, %v], want [0, 10]", x0, x1)
	}
	if plot.ViewBox().IsManualOverride() {
		t.Fatal("should not be in manual override initially")
	}

	pr := plot.PlotRectLocal()
	cx, cy := pr.X+pr.W*0.5, pr.Y+pr.H*0.5

	// 2. 左键拖拽平移（PanMode）应移动视口并显示 [A] 手动接管状态
	tt.Press(cx, cy)
	tt.Move(cx+80, cy)
	tt.Release(cx+80, cy)

	px0, _ := plot.ViewBox().XRange()
	if !(px0 < -0.5) || !plot.ViewBox().IsManualOverride() {
		t.Fatalf("left-drag pan did not shift X left: px0=%v manual=%v", px0, plot.ViewBox().IsManualOverride())
	}

	// 3. Ctrl + 鼠标滚轮（Wheel）：仅缩放 X 轴，Y 轴保持严格不变
	yBefore0, yBefore1 := plot.ViewBox().YRange()
	tt.HoldModifiers(ui.Ctrl)
	tt.Wheel(cx, cy, 0, -120)
	tt.HoldModifiers(0)
	xAfter0, xAfter1 := plot.ViewBox().XRange()
	yAfter0, yAfter1 := plot.ViewBox().YRange()
	if math.Abs((yAfter1-yAfter0)-(yBefore1-yBefore0)) > 1e-12 {
		t.Fatalf("Ctrl+Wheel should not change Y range: before=[%v,%v], after=[%v,%v]", yBefore0, yBefore1, yAfter0, yAfter1)
	}
	if (xAfter1 - xAfter0) >= 10.0 {
		t.Fatalf("Ctrl+Wheel should zoom in X: span=%v", xAfter1-xAfter0)
	}

	// Shift + 鼠标滚轮：仅缩放 Y 轴，X 轴保持严格不变
	tt.HoldModifiers(ui.Shift)
	tt.Wheel(cx, cy, 0, -120)
	tt.HoldModifiers(0)
	xAfterShift0, xAfterShift1 := plot.ViewBox().XRange()
	if math.Abs((xAfterShift1-xAfterShift0)-(xAfter1-xAfter0)) > 1e-12 {
		t.Fatalf("Shift+Wheel should not change X range: before=[%v,%v], after=[%v,%v]", xAfter0, xAfter1, xAfterShift0, xAfterShift1)
	}

	// 4. RectMode 橡皮筋框选放大 + Backspace 退回上一级视口
	tt.Advance(time.Second) // 跨过系统双击时间窗，避免与前序点击合并为双击
	plot.ViewBox().SetMouseMode(mygograph.RectMode)
	preRectX0, preRectX1 := plot.ViewBox().XRange()
	tt.ClickAt(cx, cy) // 单击聚焦元素以接收键盘事件
	tt.Advance(time.Second)
	tt.Press(pr.X+100, pr.Y+80)
	tt.Move(pr.X+300, pr.Y+240)
	tt.Release(pr.X+300, pr.Y+240)
	if plot.ViewBox().ZoomDepth() != 1 {
		t.Fatalf("RectMode zoom should push ZoomStack, depth=%d", plot.ViewBox().ZoomDepth())
	}
	tt.Key(0, ui.KeyBackspace)
	undoX0, undoX1 := plot.ViewBox().XRange()
	if math.Abs(undoX0-preRectX0) > 1e-6 || math.Abs(undoX1-preRectX1) > 1e-6 {
		t.Fatalf("Backspace PopZoom mismatch: got [%v,%v], want [%v,%v]", undoX0, undoX1, preRectX0, preRectX1)
	}

	// 5. 右键拖拽非等比缩放（向右拉放大 X，松手不应误触弹出 ContextMenu）
	tt.Advance(time.Second)
	preRDX0, preRDX1 := plot.ViewBox().XRange()
	tt.PressButton(1, cx, cy)
	tt.Move(cx+60, cy-40)
	tt.ReleaseButton(1, cx+60, cy-40)
	postRDX0, postRDX1 := plot.ViewBox().XRange()
	if (postRDX1 - postRDX0) >= (preRDX1 - preRDX0) {
		t.Fatalf("right-drag right should zoom in X: before=%v, after=%v", preRDX1-preRDX0, postRDX1-postRDX0)
	}
	if len(tt.Menu()) != 0 {
		t.Fatalf("right-button drag should not open context menu, got %v", tt.Menu())
	}

	// 6. 原地右键单击上下文菜单触发一键 AutoRange 复位
	tt.Advance(time.Second)
	tt.RightClickAt(cx, cy)
	if len(tt.Menu()) == 0 {
		t.Fatal("expected context menu items on right click")
	}
	if err := tt.ChooseMenuItem("Reset Auto Range (A)"); err != nil {
		t.Fatalf("ChooseMenuItem failed: %v", err)
	}
	if plot.ViewBox().IsManualOverride() {
		t.Fatal("Reset Auto Range menu item should clear manual override")
	}
}

func TestGraphLayoutPixelExactAxisAlignmentAndLinkX(t *testing.T) {
	// 子图 1：微伏级长数值标签（"-500.0 μV"）
	pTop := mygograph.NewPlot(
		mygograph.WithLeftAxis(mygograph.NewSIAxis(mygograph.AxisLeft, "EEG Microvolt", "V")),
	)
	pTop.ViewBox().SetRange(0, 10, -500e-6, 500e-6, 0)

	// 子图 2：简短个位数标签（"0" ~ "1"）
	pBot := mygograph.NewPlot(
		mygograph.WithLeftAxis(mygograph.NewAxis(mygograph.AxisLeft).SetLabel("Gate")),
	)
	pBot.ViewBox().SetRange(0, 10, 0, 1, 0)
	pTop.LinkX(pBot)

	layout := mygograph.NewGraphLayout().
		AddRowFlex(2, pTop).
		AddRowFlex(1, pBot)

	_ = ui.NewTester(func(c *ui.Context) {
		layout.View(c)
	}, 900, 600)

	rTop := pTop.PlotRectLocal()
	rBot := pBot.PlotRectLocal()
	if rTop.X != rBot.X || rTop.W != rBot.W {
		t.Fatalf("subplot X axes misaligned! top=(X:%v, W:%v) vs bot=(X:%v, W:%v)", rTop.X, rTop.W, rBot.X, rBot.W)
	}
}

func TestCursorMeasureSineRMSAndDrag(t *testing.T) {
	// 构造精确 50 Hz、峰值 2.0 V 的正弦波（真有效值 RMS = 2 / sqrt(2) = sqrt(2) ≈ 1.41421356 V，Vpp = 4.0 V）
	const fs = 100_000.0
	const n = 10_001 // 0.100 s（整 5 个周期）
	ys := make([]float64, n)
	for i := range ys {
		tSec := float64(i) / fs
		ys[i] = 2.0 * math.Sin(2*math.Pi*50.0*tSec)
	}
	src := data.NewUniformSlice(0, fs, ys)

	cm := items.NewCursorMeasure(0.020, 0.040, items.WithMeasureSource(src)) // 整 1 个 50Hz 周期 (20 ms)
	st := cm.Stats()
	if math.Abs(st.DeltaX-0.020) > 1e-9 || math.Abs(st.FreqHz-50.0) > 1e-6 {
		t.Fatalf("cursor ΔT/Freq mismatch: ΔT=%v Freq=%v", st.DeltaX, st.FreqHz)
	}
	if math.Abs(st.Vpp-4.0) > 1e-3 {
		t.Fatalf("cursor Vpp = %v, want 4.0", st.Vpp)
	}
	wantRMS := math.Sqrt2
	if math.Abs(st.RMS-wantRMS) > 2e-3 {
		t.Fatalf("cursor True RMS = %v, want %v", st.RMS, wantRMS)
	}

	// 在无头 Tester 中拖拽 T1 游标验证鼠标交互闭环
	plot := mygograph.NewPlot()
	plot.AddItem(items.NewCurve(src), cm)
	tt := ui.NewTester(func(c *ui.Context) {
		plot.View(c)
	}, 800, 400)

	pr := plot.PlotRectLocal()
	vt := plot.ViewBox().Transform(pr)
	t1ScreenX := vt.DataToScreenX(0.020)
	midY := pr.Y + pr.H*0.5

	tt.Move(t1ScreenX, midY)
	if tt.Cursor() != ui.CursorResizeEW {
		t.Fatalf("hovering vertical cursor should show CursorResizeEW, got %v", tt.Cursor())
	}
	tt.Press(t1ScreenX, midY)
	tt.Move(t1ScreenX+60, midY)
	tt.Release(t1ScreenX+60, midY)

	newT1, _ := cm.XPositions()
	if !(newT1 > 0.022) {
		t.Fatalf("dragging T1 cursor right should advance T1, got %v", newT1)
	}
}

func TestStreamIdleZeroCPUGate(t *testing.T) {
	ring := data.NewRingBuffer(4096, 1000)
	ring.Push(0, 1, 2, 1, 0)

	plot := mygograph.NewPlot()
	ring.SetWakeFunc(plot.WakeFunc())
	plot.AddItem(items.NewCurve(ring))

	tt := ui.NewTester(func(c *ui.Context) {
		plot.View(c)
	}, 640, 360)

	// 刚推入流数据时应请求下一帧 VSync
	if !tt.FramePending() {
		t.Fatal("expected AnimationFrame pending while stream just updated")
	}

	// 停止推流并推进时钟超过 150ms 静默阈值后，Plot 必须停止请求 AnimationFrame（空闲 0% CPU 门禁！）
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("idle stream must NOT keep requesting AnimationFrame (0% idle CPU gate)")
	}
}
