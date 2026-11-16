package mygograph

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestAxisFourOrientationsAndChannelDecimation(t *testing.T) {
	vb := NewViewBox()
	vb.SetRange(0, 10, -250e-6, 250e-6, 0)
	theme := DarkTheme()

	bottom := NewTimeAxis(AxisBottom, "Time")
	left := NewSIAxis(AxisLeft, "Voltage", "V")
	top := NewAxis(AxisTop).SetLabel("Sample")
	right := NewLogAxis(AxisRight, "Power", "W")

	names := make([]string, 256)
	for i := range names {
		names[i] = fmt.Sprintf("EEG %03d", i+1)
	}
	chAxis := NewChannelAxis(AxisLeft, names)
	vbCh := NewViewBox()
	vbCh.SetRange(0, 10, -0.5, 255.5, 0)
	chTicks, _ := chAxis.computeTicks(vbCh, 400)
	labeled := 0
	for _, tk := range chTicks {
		if tk.Label != "" {
			labeled++
		}
	}
	if labeled == 0 || labeled > 32 {
		t.Fatalf("256-channel axis in 400px should decimate labels cleanly, got %d labeled ticks", labeled)
	}

	// 使用 ui.Render 无头离屏光栅化校验四边坐标轴绘制与字形缓存不触发异常
	img := ui.Render(func(c *ui.Context) {
		ui.Box(c).Size(640, 420).Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(r, theme.Background, 0)
			lw := left.MeasureThickness(vb, 300, theme)
			rw := right.MeasureThickness(vb, 300, theme)
			th := top.MeasureThickness(vb, 480, theme)
			bh := bottom.MeasureThickness(vb, 480, theme)
			plotR := ui.Rect{
				X: r.X + lw,
				Y: r.Y + th,
				W: r.W - lw - rw,
				H: r.H - th - bh,
			}
			left.Draw(p, ui.Rect{X: r.X, Y: plotR.Y, W: lw, H: plotR.H}, plotR, vb, theme)
			right.Draw(p, ui.Rect{X: plotR.X + plotR.W, Y: plotR.Y, W: rw, H: plotR.H}, plotR, vb, theme)
			top.Draw(p, ui.Rect{X: plotR.X, Y: r.Y, W: plotR.W, H: th}, plotR, vb, theme)
			bottom.Draw(p, ui.Rect{X: plotR.X, Y: plotR.Y + plotR.H, W: plotR.W, H: bh}, plotR, vb, theme)
		})
	}, 640, 420, 1.0)

	if img == nil || img.Bounds().Dx() != 640 || img.Bounds().Dy() != 420 {
		t.Fatalf("unexpected rendered image bounds: %v", img.Bounds())
	}
}

func TestAxisSetLogModePreservesOriginalScale(t *testing.T) {
	ax := NewAxis(AxisBottom)
	ts := NewTimeScale()
	ax.SetScale(ts)

	ax.SetLogMode(true)
	if !ax.Scale().IsLog() {
		t.Fatal("SetLogMode(true) should activate log scale")
	}

	ax.SetLogMode(false)
	if ax.Scale() != ts {
		t.Fatalf("SetLogMode(false) should restore the original *TimeScale, got %T", ax.Scale())
	}
}
