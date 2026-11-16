package mygograph_test

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/items"
)

type rangeProbe struct {
	*items.CurveItem
	xs *[][2]float64
	ys *[][2]float64
}

func (p *rangeProbe) Draw(rc *mygograph.RenderContext) {
	x0, x1 := rc.View.XRange()
	y0, y1 := rc.View.YRange()
	*p.xs = append(*p.xs, [2]float64{x0, x1})
	*p.ys = append(*p.ys, [2]float64{y0, y1})
	p.CurveItem.Draw(rc)
}

func newRangeProbe(xs *[][2]float64, ys *[][2]float64, xr, yr [2]float64) *rangeProbe {
	xVals := []float64{xr[0], (xr[0] + xr[1]) / 2, xr[1]}
	yVals := []float64{yr[0], (yr[0] + yr[1]) / 2, yr[1]}
	return &rangeProbe{
		CurveItem: items.NewCurve(data.NewXYSlice(xVals, yVals)),
		xs:        xs,
		ys:        ys,
	}
}

// 回归：LinkX 下自动量程必须使用联动组联合范围，而非各子图自身图元范围。
func TestLinkedXAutoRangeUsesGroupUnion(t *testing.T) {
	a := mygograph.NewPlot()
	b := mygograph.NewPlot()
	var ax, ay, bx, by [][2]float64
	a.AddItem(newRangeProbe(&ax, &ay, [2]float64{0, 10}, [2]float64{0, 1}))
	b.AddItem(newRangeProbe(&bx, &by, [2]float64{0, 20}, [2]float64{0, 1}))
	a.LinkX(b)

	gl := mygograph.NewGraphLayout()
	gl.AddRow(a, b)
	tt := ui.NewTester(func(c *ui.Context) { gl.View(c) }, 600, 600)
	a.Invalidate()
	b.Invalidate()
	tt.Frame()
	tt.Frame()

	if len(ax) == 0 || len(bx) == 0 {
		t.Fatalf("no draws recorded: A=%d B=%d", len(ax), len(bx))
	}
	for i, r := range ax {
		if r[0] != 0 || r[1] != 20 {
			t.Fatalf("plot A draw %d X range = [%v,%v], want [0,20]", i, r[0], r[1])
		}
	}
	for i, r := range bx {
		if r[0] != 0 || r[1] != 20 {
			t.Fatalf("plot B draw %d X range = [%v,%v], want [0,20]", i, r[0], r[1])
		}
	}
}

func TestLinkedXAutoRangeUnionThreePlotChain(t *testing.T) {
	a := mygograph.NewPlot()
	b := mygograph.NewPlot()
	c := mygograph.NewPlot()
	var ax, ay, bx, by, cx, cy [][2]float64
	a.AddItem(newRangeProbe(&ax, &ay, [2]float64{0, 10}, [2]float64{0, 1}))
	b.AddItem(newRangeProbe(&bx, &by, [2]float64{0, 20}, [2]float64{0, 1}))
	c.AddItem(newRangeProbe(&cx, &cy, [2]float64{0, 30}, [2]float64{0, 1}))
	a.LinkX(b)
	b.LinkX(c)

	gl := mygograph.NewGraphLayout()
	gl.AddRow(a, b, c)
	tt := ui.NewTester(func(c *ui.Context) { gl.View(c) }, 900, 400)
	a.Invalidate()
	b.Invalidate()
	c.Invalidate()
	tt.Frame()
	tt.Frame()

	for name, recs := range map[string][][2]float64{"A": ax, "B": bx, "C": cx} {
		if len(recs) == 0 {
			t.Fatalf("plot %s recorded no draws", name)
		}
		for i, r := range recs {
			if r[0] != 0 || r[1] != 30 {
				t.Fatalf("plot %s draw %d X range = [%v,%v], want [0,30]", name, i, r[0], r[1])
			}
		}
	}
}

// 回归：LinkY 下自动量程两图必须收敛到完全相同的 Y 范围（含 autoPadding）。
func TestLinkedYAutoRangeUsesGroupUnion(t *testing.T) {
	a := mygograph.NewPlot()
	b := mygograph.NewPlot()
	var ax, ay, bx, by [][2]float64
	a.AddItem(newRangeProbe(&ax, &ay, [2]float64{0, 10}, [2]float64{0, 1}))
	b.AddItem(newRangeProbe(&bx, &by, [2]float64{0, 10}, [2]float64{-2, 5}))
	a.LinkY(b)

	gl := mygograph.NewGraphLayout()
	gl.AddRow(a, b)
	tt := ui.NewTester(func(c *ui.Context) { gl.View(c) }, 600, 600)
	a.Invalidate()
	b.Invalidate()
	tt.Frame()
	tt.Frame()

	if len(ay) == 0 || len(by) == 0 {
		t.Fatalf("no draws recorded: A=%d B=%d", len(ay), len(by))
	}
	for i, r := range ay {
		if r[0] != -2.35 || r[1] != 5.35 {
			t.Fatalf("plot A draw %d Y range = [%v,%v], want union [-2.35,5.35]", i, r[0], r[1])
		}
	}
	for i, r := range by {
		if r != ay[min(i, len(ay)-1)] {
			t.Fatalf("plot B draw %d Y range = [%v,%v], want equal to A", i, r[0], r[1])
		}
	}
}

type boundsCounter struct {
	*items.CurveItem
	boundsCalls *int
	drawCalls   *int
}

func (p *boundsCounter) Bounds() (xr, yr data.Range, ok bool) {
	*p.boundsCalls++
	return p.CurveItem.Bounds()
}

func (p *boundsCounter) Draw(rc *mygograph.RenderContext) {
	*p.drawCalls++
	p.CurveItem.Draw(rc)
}

// 回归：未联动 Plot 每次绘制对每个图元最多调用一次 Bounds()（boundsFn 结果在 X/Y 两组循环间复用）。
func TestAutoRangeCallsItemBoundsOncePerFrame(t *testing.T) {
	p := mygograph.NewPlot()
	var boundsCalls, drawCalls int
	p.AddItem(&boundsCounter{
		CurveItem:   items.NewCurve(data.NewXYSlice([]float64{0, 5, 10}, []float64{0, 1, 0})),
		boundsCalls: &boundsCalls,
		drawCalls:   &drawCalls,
	})
	tt := ui.NewTester(func(c *ui.Context) { p.View(c) }, 400, 300)
	p.Invalidate()
	tt.Frame()
	tt.Frame()
	if drawCalls == 0 {
		t.Fatal("no draws recorded")
	}
	if boundsCalls != drawCalls {
		t.Fatalf("Bounds() called %d times across %d draws, want exactly once per draw", boundsCalls, drawCalls)
	}
}
