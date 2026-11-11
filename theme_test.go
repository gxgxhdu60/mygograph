package mygograph

import "testing"

func TestThemesOpaquePalette(t *testing.T) {
	for _, tc := range []struct {
		name string
		th   Theme
	}{
		{"Dark", DarkTheme()},
		{"Light", LightTheme()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.th.Palette) < 12 {
				t.Fatalf("palette too small: got %d", len(tc.th.Palette))
			}
			if tc.th.Background.A != 255 || tc.th.PlotBackground.A != 255 {
				t.Fatalf("backgrounds must be opaque for subpixel text: bg=%v plotBg=%v", tc.th.Background, tc.th.PlotBackground)
			}
			for i := -5; i < 32; i++ {
				c := tc.th.SeriesColor(i)
				if c.A != 255 {
					t.Fatalf("SeriesColor(%d) alpha = %d, want 255 for GPU StrokePolyline fast path", i, c.A)
				}
			}
		})
	}
}
