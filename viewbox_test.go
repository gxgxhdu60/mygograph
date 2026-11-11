package mygograph

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestViewBoxAnchorZoomInvarianceAndStack(t *testing.T) {
	vb := NewViewBox()
	vb.SetRange(10.0, 110.0, -50.0, 150.0, 0)
	if !vb.IsManualOverride() {
		t.Fatal("SetRange should mark manual override")
	}

	const (
		w, h       float32 = 800, 400
		anchX      float32 = 240 // 30% 水平处 -> X = 40.0
		anchY      float32 = 100 // 25% 顶距处 -> Y = 100.0
	)
	beforeX, beforeY := vb.LocalToData(anchX, anchY, w, h)
	vb.ZoomAtAnchor(anchX, anchY, w, h, 0.25, 0.4, AxisBoth)
	afterX, afterY := vb.LocalToData(anchX, anchY, w, h)

	if math.Abs(afterX-beforeX) > 1e-9 || math.Abs(afterY-beforeY) > 1e-9 {
		t.Fatalf("anchor point drifted: before=(%v,%v), after=(%v,%v)", beforeX, beforeY, afterX, afterY)
	}

	// 测试 RectMode 框选压栈与 PopZoom 回退
	x0, x1 := vb.XRange()
	y0, y1 := vb.YRange()
	if !vb.ZoomToLocalRect(100, 50, 300, 250, w, h) {
		t.Fatal("ZoomToLocalRect failed")
	}
	if vb.ZoomDepth() != 1 {
		t.Fatalf("ZoomDepth = %d, want 1", vb.ZoomDepth())
	}
	if !vb.PopZoom() {
		t.Fatal("PopZoom returned false")
	}
	rx0, rx1 := vb.XRange()
	ry0, ry1 := vb.YRange()
	if math.Abs(rx0-x0) > 1e-12 || math.Abs(rx1-x1) > 1e-12 || math.Abs(ry0-y0) > 1e-12 || math.Abs(ry1-y1) > 1e-12 {
		t.Fatalf("PopZoom mismatch: got [%v,%v]×[%v,%v], want [%v,%v]×[%v,%v]", rx0, rx1, ry0, ry1, x0, x1, y0, y1)
	}
}

func TestViewBoxCycleSafeLinkXAndLimits(t *testing.T) {
	a := NewViewBox()
	b := NewViewBox()
	c := NewViewBox()

	// 构造环形连接图 A <-> B <-> C <-> A，验证防重入保护无死递归
	a.LinkX(b)
	b.LinkX(c)
	c.LinkX(a)

	a.SetXRange(2.5, 12.5, 0)
	bx0, bx1 := b.XRange()
	cx0, cx1 := c.XRange()
	if bx0 != 2.5 || bx1 != 12.5 || cx0 != 2.5 || cx1 != 12.5 {
		t.Fatalf("linked X ranges out of sync: B=[%v,%v] C=[%v,%v]", bx0, bx1, cx0, cx1)
	}

	// 验证硬边界与最小跨度约束
	lim := DefaultViewLimits()
	lim.XMin = 0
	lim.XMax = 20
	lim.MinXSpan = 1.0
	c.SetLimits(lim)
	a.PanByPixels(-5000, 0, 500, 300, AxisX) // 向右大幅平移应停在 XMax=20
	cx0, cx1 = c.XRange()
	if cx1 > 20.0+1e-9 || cx0 < 0 {
		t.Fatalf("limits violated after pan: C=[%v,%v]", cx0, cx1)
	}

	// 验证 ScreenToData 与 Transform.DataToScreen 正逆一致
	rect := ui.Rect{X: 60, Y: 20, W: 640, H: 360}
	vt := a.Transform(rect)
	px := vt.DataToScreenX(14.25)
	py := vt.DataToScreenY(0.35)
	dx, dy := a.ScreenToData(px, py, rect)
	if math.Abs(dx-14.25) > 1e-4 || math.Abs(dy-0.35) > 1e-4 {
		t.Fatalf("ScreenToData inverse mismatch: got (%v,%v)", dx, dy)
	}
}
