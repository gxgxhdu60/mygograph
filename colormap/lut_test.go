package colormap_test

import (
	"testing"

	"github.com/gaoxi/mygograph/colormap"
)

func TestBuiltinColormapsMonotonicLuminanceAndOpaque(t *testing.T) {
	all := colormap.BuiltinColormaps()
	if len(all) != 8 {
		t.Fatalf("expected 8 builtin colormaps, got %d", len(all))
	}
	for _, cm := range all {
		tbl := cm.Table()
		for i, c := range tbl {
			if c.A != 255 {
				t.Fatalf("%s[%d] alpha=%d, want 255", cm.Name(), i, c.A)
			}
		}
		if got := colormap.ByName(cm.Name()); got != cm {
			t.Fatalf("ByName(%q) mismatch", cm.Name())
		}
	}

	// 验证 Viridis 与 Grayscale 感知亮度整体单调递增
	for _, cm := range []*colormap.Colormap{colormap.Viridis(), colormap.Grayscale()} {
		tbl := cm.Table()
		lum0 := 0.2126*float64(tbl[0].R) + 0.7152*float64(tbl[0].G) + 0.0722*float64(tbl[0].B)
		lumMid := 0.2126*float64(tbl[128].R) + 0.7152*float64(tbl[128].G) + 0.0722*float64(tbl[128].B)
		lumEnd := 0.2126*float64(tbl[255].R) + 0.7152*float64(tbl[255].G) + 0.0722*float64(tbl[255].B)
		if !(lum0 < lumMid && lumMid < lumEnd) {
			t.Fatalf("%s luminance not monotonic: start=%.1f mid=%.1f end=%.1f", cm.Name(), lum0, lumMid, lumEnd)
		}
	}

	// 验证 Coolwarm 中点 t=0.5 接近中性灰白，两端分别为冷蓝与暖红
	cw := colormap.Coolwarm()
	cLow, cMid, cHigh := cw.At(0), cw.At(0.5), cw.At(1.0)
	if !(cLow.B > cLow.R && cHigh.R > cHigh.B) {
		t.Fatalf("Coolwarm polarity wrong: low=%v high=%v", cLow, cHigh)
	}
	if int(cMid.R) < 200 || int(cMid.G) < 200 || int(cMid.B) < 200 {
		t.Fatalf("Coolwarm midpoint should be neutral light gray: got %v", cMid)
	}
}

func BenchmarkColorControlWindowLevels(b *testing.B) {
	ctrl := colormap.NewColorControl(colormap.Viridis(), -120, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lo := -110.0 + float64(i&15)
		ctrl.SetLevels(lo, -10.0)
		_, _ = ctrl.NormalizeForBase(-120, 0)
	}
}
