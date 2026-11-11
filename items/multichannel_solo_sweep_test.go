package items_test

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/items"
)

func newSoloTestPlot(t *testing.T, mode items.MultiDisplayMode) (*mygograph.Plot, *items.MultiChannelItem, *ui.Tester) {
	t.Helper()
	const nCh, fs = 6, 200.0
	ring := data.NewMultiRingBufferPerChannel(nCh, fs, 4000)
	blocks := make([][]float64, nCh)
	for ch := range nCh {
		b := make([]float64, int(6*fs))
		for i := range b {
			b[i] = 50e-6 * math.Sin(float64(i)*0.1)
		}
		blocks[ch] = b
	}
	ring.PushChannelBlocks(blocks)
	mc := items.NewMultiChannel(ring, items.WithDisplayMode(mode), items.WithWindowSeconds(3), items.WithChannelScale(200e-6))
	plot := mygograph.NewPlot()
	plot.AddItem(mc)
	tt := ui.NewTester(func(c *ui.Context) { plot.View(c) }, 360, 240)
	tt.Frame()
	return plot, mc, tt
}

// 回归：Solo 还原不再依赖共享缩放历史栈——独奏期间的框选缩放快照不会被误弹出，
// 还原只恢复 Y 轴，且不会把用户正在回看历史的 X 轴强行拉回实时跟随。
func TestSoloRestoreIndependentOfZoomStackAndKeepsX(t *testing.T) {
	plot, mc, tt := newSoloTestPlot(t, items.DisplayRollMode)
	vb := plot.ViewBox()
	if !vb.IsAutoY() || !vb.IsAutoX() {
		t.Fatalf("expected auto X/Y initially")
	}
	mc.SetSoloChannel(3, vb)
	if vb.IsAutoY() {
		t.Fatalf("solo should lock Y")
	}
	// 独奏期间用户压入一次缩放历史并回看历史（X 解耦实时跟随）
	vb.PushZoom()
	vb.SetXRange(0.5, 2.0, 0)
	depth := vb.ZoomDepth()

	mc.SetSoloChannel(-1, vb)
	tt.Frame()
	if mc.SoloChannel() != -1 {
		t.Fatalf("solo not cleared")
	}
	if !vb.IsAutoY() {
		t.Fatalf("restore should re-enable Y auto-range captured before solo")
	}
	if vb.IsAutoX() {
		t.Fatalf("restore must not touch X follow state (user is reviewing history)")
	}
	if x0, x1 := vb.XRange(); math.Abs(x0-0.5) > 1e-9 || math.Abs(x1-2.0) > 1e-9 {
		t.Fatalf("restore must keep X history window, got [%v, %v]", x0, x1)
	}
	if vb.ZoomDepth() != depth {
		t.Fatalf("restore must not pop user zoom history: depth %d -> %d", depth, vb.ZoomDepth())
	}

	// 手动 Y 视口：还原时精确回到进入 Solo 前的范围
	vb.SetYRange(1, 4, 0)
	mc.SetSoloChannel(2, vb)
	mc.SetSoloChannel(5, vb) // 独奏间切换不覆盖原始快照
	mc.SetSoloChannel(-1, vb)
	if y0, y1 := vb.YRange(); math.Abs(y0-1) > 1e-9 || math.Abs(y1-4) > 1e-9 || vb.IsAutoY() {
		t.Fatalf("restore should return to manual pre-solo Y [1,4], got [%v, %v] auto=%v", y0, y1, vb.IsAutoY())
	}
}

// 回归：右键切换到擦除扫描模式及调整时基后 X 轴保持自动量程，
// 不会因 SetXRange 进入手动态而常驻 [A]/[Live] 角标与 "Resume Live Follow" 菜单项。
func TestSweepModeMenuKeepsAutoX(t *testing.T) {
	plot, mc, tt := newSoloTestPlot(t, items.DisplayRollMode)
	vb := plot.ViewBox()
	pr := plot.PlotRectLocal()
	tt.RightClickAt(pr.X+pr.W*0.5, pr.Y+pr.H*0.5)
	if err := tt.ChooseMenuItem("EEG / ECG Display", "Sweep Mode (Erase-Bar)"); err != nil {
		t.Fatalf("choose sweep: %v (menu=%v)", err, tt.Menu())
	}
	tt.Frame()
	if mc.DisplayMode() != items.DisplaySweepMode || !vb.IsAutoX() {
		t.Fatalf("sweep switch should keep AutoX, mode=%v autoX=%v", mc.DisplayMode(), vb.IsAutoX())
	}
	if x0, x1 := vb.XRange(); math.Abs(x0) > 1e-9 || math.Abs(x1-3) > 1e-9 {
		t.Fatalf("sweep XRange want [0,3], got [%v, %v]", x0, x1)
	}

	tt.RightClickAt(pr.X+pr.W*0.5, pr.Y+pr.H*0.5)
	if err := tt.ChooseMenuItem("Timebase (Window)", "5 s"); err != nil {
		t.Fatalf("choose timebase: %v", err)
	}
	tt.Frame()
	if !vb.IsAutoX() {
		t.Fatalf("timebase change in sweep should keep AutoX")
	}
	if x0, x1 := vb.XRange(); math.Abs(x0) > 1e-9 || math.Abs(x1-5) > 1e-9 {
		t.Fatalf("sweep XRange after timebase want [0,5], got [%v, %v]", x0, x1)
	}
}
