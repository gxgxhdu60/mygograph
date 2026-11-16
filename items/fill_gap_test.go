package items_test

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/items"
)

// 回归：FillBetween 不得跨 upper 曲线的 NaN 断点桥接填充，缺口区域应保持绘图区底色。
func TestFillBetweenDoesNotBridgeNaNGap(t *testing.T) {
	xs := []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	us := []float64{1, 1, 1, 1, 1, math.NaN(), 1, 1, 1, 1, 1}
	ls := []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	fillCol := ui.RGB(255, 0, 255)

	lower := items.NewCurve(data.NewXYSlice(xs, ls), items.WithDecimateMode(data.DecimateNone))
	upper := items.NewCurve(
		data.NewXYSlice(xs, us),
		items.WithDecimateMode(data.DecimateNone),
		items.WithFillBetween(lower, fillCol),
	)
	plot := mygograph.NewPlot(mygograph.WithGrid(false, false))
	plot.AddItem(lower, upper)

	tt := ui.NewTester(func(c *ui.Context) { plot.View(c) }, 400, 300)
	tt.Frame()
	tt.Frame()
	img := tt.Image()
	pr := plot.PlotRectLocal()
	vt := plot.ViewBox().Transform(pr)
	bg := mygograph.DarkTheme().PlotBackground

	gapX := int(vt.DataToScreenX(5.0) + 0.5)
	fillX := int(vt.DataToScreenX(2.0) + 0.5)
	midY := int(vt.DataToScreenY(0.5) + 0.5)

	gapPx := img.RGBAAt(gapX, midY)
	if gapPx.R != bg.R || gapPx.G != bg.G || gapPx.B != bg.B {
		t.Fatalf("pixel inside NaN gap at (%d,%d) = %+v, want background %+v", gapX, midY, gapPx, bg)
	}
	fillPx := img.RGBAAt(fillX, midY)
	if fillPx.R != 255 || fillPx.G != 0 || fillPx.B != 255 {
		t.Fatalf("pixel inside filled run at (%d,%d) = %+v, want opaque magenta", fillX, midY, fillPx)
	}
}
