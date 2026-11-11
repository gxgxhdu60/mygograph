package items

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
)

// 回归：全导联视图下 L 型标尺按全局灵敏度标定；Solo 还原后残留的选中导联（带 0.1x 独立倍率）不应改变标尺读数。
func TestScaleBarIgnoresSelectedChannelGainOutsideSolo(t *testing.T) {
	const nCh, fs = 6, 200.0
	ring := data.NewMultiRingBufferPerChannel(nCh, fs, 4000)
	blocks := make([][]float64, nCh)
	for ch := range nCh {
		b := make([]float64, int(4*fs))
		for i := range b {
			b[i] = 50e-6 * math.Sin(float64(i)*0.1)
		}
		blocks[ch] = b
	}
	ring.PushChannelBlocks(blocks)
	mc := NewMultiChannel(ring, WithWindowSeconds(3), WithChannelScale(200e-6), WithScaleBar(true))
	mc.SetChannelGain(1, 0.1)
	plot := mygograph.NewPlot()
	plot.AddItem(mc)
	tt := ui.NewTester(func(c *ui.Context) { plot.View(c) }, 480, 320)
	tt.Frame()
	base := mc.lastCalAmpStr
	if base == "" {
		t.Fatalf("scale bar amplitude label not rendered")
	}

	vb := plot.ViewBox()
	mc.SetSoloChannel(1, vb)
	mc.SetSoloChannel(-1, vb)
	if mc.selected != 1 {
		t.Fatalf("precondition: selected channel should remain 1, got %d", mc.selected)
	}
	tt.Frame()
	if mc.lastCalAmpStr != base {
		t.Fatalf("scale bar changed by selected channel gain outside solo: %q -> %q", base, mc.lastCalAmpStr)
	}
}
