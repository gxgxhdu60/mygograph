package mygograph

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 回归：在窗口中显示的 Plot 执行离屏 PNG 导出后，必须恢复其宿主 Services 与布局命中框，
// 否则 Invalidate / WakeFunc 会投递到已废弃的离屏渲染器，导致实时图表停止刷新。
func TestSavePNGRestoresLiveHostState(t *testing.T) {
	p := NewPlot(WithTitle("live"))
	tt := ui.NewTester(func(c *ui.Context) { p.View(c) }, 420, 260)
	tt.Frame()

	liveSvc := p.services
	liveW, liveH := p.elemW, p.elemH
	livePlotRect := p.localPlotRect
	if liveW != 420 || liveH != 260 {
		t.Fatalf("unexpected live layout %vx%v", liveW, liveH)
	}

	out := filepath.Join(t.TempDir(), "sub", "export.png")
	if err := p.SavePNG(out, 900, 600, 2.0); err != nil {
		t.Fatalf("SavePNG: %v", err)
	}

	if p.services != liveSvc {
		t.Fatalf("SavePNG must restore live window Services")
	}
	if p.elemW != liveW || p.elemH != liveH || p.localPlotRect != livePlotRect {
		t.Fatalf("SavePNG must restore live layout: got %vx%v %+v, want %vx%v %+v",
			p.elemW, p.elemH, p.localPlotRect, liveW, liveH, livePlotRect)
	}

	gl := NewGraphLayout()
	gl.AddRow(p)
	tt2 := ui.NewTester(func(c *ui.Context) { gl.View(c) }, 500, 300)
	tt2.Frame()
	svc2 := p.services
	if err := gl.SavePNG(filepath.Join(t.TempDir(), "gl.png"), 800, 500, 1.0); err != nil {
		t.Fatalf("GraphLayout SavePNG: %v", err)
	}
	if p.services != svc2 {
		t.Fatalf("GraphLayout SavePNG must restore child Plot Services")
	}
}
