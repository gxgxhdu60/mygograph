package mygograph

import "github.com/egoist/mygo/ui"

// Theme 定义科学计算与工程测量图表的配色与视觉度量规范。
//
// 为规避 mygo/ui 半透明折线退化为 CPU 路径光栅化的性能陷阱（DESIGN.md 陷阱 1），
// Palette 中的全部曲线主色均强制采用完全不透明颜色（A == 255），确保 100% 命中 GPU OpFill 快路径。
type Theme struct {
	Dark bool

	// 画布与面板背景色（不透明，确保 ClearType 子像素抗锯齿生效）
	Background     ui.Color
	PlotBackground ui.Color
	PanelSurface   ui.Color
	PanelBorder    ui.Color

	// 坐标轴、刻度与网格线配色
	AxisLine  ui.Color
	AxisText  ui.Color
	TitleText ui.Color
	MutedText ui.Color
	GridMajor ui.Color
	GridMinor ui.Color
	ZeroLine  ui.Color

	// 交互覆盖物配色（游标、十字准星、框选缩放、ROI、坏道）
	CrosshairLine   ui.Color
	ReadoutBg       ui.Color
	ReadoutBorder   ui.Color
	ReadoutText     ui.Color
	ZoomRectFill    ui.Color
	ZoomRectBorder  ui.Color
	ROIFill         ui.Color
	ROIBorder       ui.Color
	ROIHandle       ui.Color
	CursorLine      ui.Color
	SweepBarLine    ui.Color
	BadChannelColor ui.Color
	AccentColor     ui.Color

	// 字体大小（DIP）与字体族
	TitleFontSize   float32
	AxisFontSize    float32
	TickFontSize    float32
	ReadoutFontSize float32
	FontFamily      string
	MonoFontFamily  string

	// 高对比度多通道不透明曲线调色板（A == 255）
	Palette []ui.Color
}

// DarkTheme 返回面向数字示波器、脑电仪与频谱仪的高对比度暗色工程主题（默认推荐）。
func DarkTheme() Theme {
	return Theme{
		Dark:            true,
		Background:      ui.RGB(13, 16, 23),
		PlotBackground:  ui.RGB(9, 12, 18),
		PanelSurface:    ui.RGB(22, 27, 38),
		PanelBorder:     ui.RGB(48, 56, 74),
		AxisLine:        ui.RGB(130, 142, 165),
		AxisText:        ui.RGB(205, 214, 230),
		TitleText:       ui.RGB(238, 242, 250),
		MutedText:       ui.RGB(136, 146, 168),
		GridMajor:       ui.RGBA(255, 255, 255, 0.11),
		GridMinor:       ui.RGBA(255, 255, 255, 0.045),
		ZeroLine:        ui.RGBA(255, 255, 255, 0.24),
		CrosshairLine:   ui.RGBA(226, 232, 240, 0.65),
		ReadoutBg:       ui.RGBA(17, 22, 33, 0.92),
		ReadoutBorder:   ui.RGB(71, 85, 105),
		ReadoutText:     ui.RGB(241, 245, 249),
		ZoomRectFill:    ui.RGBA(56, 189, 248, 0.18),
		ZoomRectBorder:  ui.RGB(56, 189, 248),
		ROIFill:         ui.RGBA(59, 130, 246, 0.20),
		ROIBorder:       ui.RGB(96, 165, 250),
		ROIHandle:       ui.RGB(191, 219, 254),
		CursorLine:      ui.RGB(250, 204, 21),
		SweepBarLine:    ui.RGB(34, 211, 238),
		BadChannelColor: ui.RGB(90, 98, 112),
		AccentColor:     ui.RGB(56, 189, 248),
		TitleFontSize:   13,
		AxisFontSize:    12,
		TickFontSize:    11,
		ReadoutFontSize: 11,
		FontFamily:      "",
		MonoFontFamily:  "Consolas, SF Mono, JetBrains Mono, Menlo, monospace",
		Palette: []ui.Color{
			ui.RGB(56, 189, 248),  // 亮青 (Oscilloscope Ch1 Cyan)
			ui.RGB(250, 204, 21),  // 明黄 (Oscilloscope Ch2 Yellow)
			ui.RGB(244, 114, 182), // 品红 (Oscilloscope Ch3 Magenta)
			ui.RGB(74, 222, 128),  // 荧光绿 (Oscilloscope Ch4 Green)
			ui.RGB(251, 146, 60),  // 琥珀橙
			ui.RGB(167, 139, 250), // 紫罗兰
			ui.RGB(248, 113, 113), // 珊瑚红
			ui.RGB(45, 212, 191),  // 蓝绿青
			ui.RGB(96, 165, 250),  // 钴蓝
			ui.RGB(163, 230, 53),  // 柠檬绿
			ui.RGB(232, 121, 249), // 洋红紫
			ui.RGB(253, 186, 116), // 浅杏橙
			ui.RGB(103, 232, 249), // 冰青色
			ui.RGB(196, 181, 253), // 薰衣草
			ui.RGB(252, 165, 165), // 浅绯红
			ui.RGB(110, 231, 183), // 薄荷绿
		},
	}
}

// LightTheme 返回面向学术论文排版、打印截图与日间实验室环境的亮色科研主题。
func LightTheme() Theme {
	return Theme{
		Dark:            false,
		Background:      ui.RGB(248, 250, 252),
		PlotBackground:  ui.RGB(255, 255, 255),
		PanelSurface:    ui.RGB(241, 245, 249),
		PanelBorder:     ui.RGB(203, 213, 225),
		AxisLine:        ui.RGB(71, 85, 105),
		AxisText:        ui.RGB(30, 41, 59),
		TitleText:       ui.RGB(15, 23, 42),
		MutedText:       ui.RGB(100, 116, 139),
		GridMajor:       ui.RGBA(15, 23, 42, 0.10),
		GridMinor:       ui.RGBA(15, 23, 42, 0.045),
		ZeroLine:        ui.RGBA(15, 23, 42, 0.25),
		CrosshairLine:   ui.RGBA(51, 65, 85, 0.65),
		ReadoutBg:       ui.RGBA(255, 255, 255, 0.94),
		ReadoutBorder:   ui.RGB(148, 163, 184),
		ReadoutText:     ui.RGB(15, 23, 42),
		ZoomRectFill:    ui.RGBA(2, 132, 199, 0.14),
		ZoomRectBorder:  ui.RGB(2, 132, 199),
		ROIFill:         ui.RGBA(37, 99, 235, 0.14),
		ROIBorder:       ui.RGB(37, 99, 235),
		ROIHandle:       ui.RGB(29, 78, 216),
		CursorLine:      ui.RGB(217, 119, 6),
		SweepBarLine:    ui.RGB(8, 145, 178),
		BadChannelColor: ui.RGB(160, 174, 192),
		AccentColor:     ui.RGB(2, 132, 199),
		TitleFontSize:   13,
		AxisFontSize:    12,
		TickFontSize:    11,
		ReadoutFontSize: 11,
		FontFamily:      "",
		MonoFontFamily:  "Consolas, SF Mono, JetBrains Mono, Menlo, monospace",
		Palette: []ui.Color{
			ui.RGB(2, 132, 199),  // 深海蓝
			ui.RGB(220, 38, 38),  // 信号红
			ui.RGB(22, 163, 74),  // 森林绿
			ui.RGB(217, 119, 6),  // 琥珀深橙
			ui.RGB(124, 58, 237), // 深紫
			ui.RGB(13, 148, 136), // 青碧色
			ui.RGB(219, 39, 119), // 玫瑰红
			ui.RGB(79, 70, 229),  // 靛蓝
			ui.RGB(101, 163, 13), // 橄榄绿
			ui.RGB(234, 88, 12),  // 橘红
			ui.RGB(8, 145, 178),  // 湖青
			ui.RGB(147, 51, 234), // 紫晶
			ui.RGB(180, 83, 9),   // 赭石棕
			ui.RGB(5, 150, 105),  // 翡翠绿
			ui.RGB(190, 24, 93),  // 深洋红
			ui.RGB(51, 65, 85),   // 石墨灰
		},
	}
}

// ThemeFromUI 根据 mygo/ui 上下文当前亮暗外观自动选择匹配的科研主题。
func ThemeFromUI(ut *ui.Theme) Theme {
	if ut != nil && !ut.Dark {
		return LightTheme()
	}
	return DarkTheme()
}

// SeriesColor 按通道或序列索引循环返回完全不透明（A == 255）的调色板颜色。
func (t *Theme) SeriesColor(index int) ui.Color {
	if len(t.Palette) == 0 {
		if t.Dark {
			return ui.RGB(56, 189, 248)
		}
		return ui.RGB(2, 132, 199)
	}
	if index < 0 {
		index = -index
	}
	c := t.Palette[index%len(t.Palette)]
	c.A = 255 // 强制 A=255 确保 StrokePolyline 硬件加速快路径
	return c
}
