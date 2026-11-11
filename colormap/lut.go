package colormap

import (
	"image"
	"image/color"
	"math"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// Colormap 表示一条预计算为 256 阶 RGBA 查找表及 GPU 1D 纹理（256x1 / 1x256）的科学伪彩色表。
type Colormap struct {
	name    string
	table   [256]ui.Color
	bmpOnce sync.Once
	bmpH    *ui.Bitmap // 256x1 水平纹理（供 lut16.kage image1Sample 采样）
	bmpV    *ui.Bitmap // 1x256 垂直纹理（顶部对应 t=1.0 最大值，底部对应 t=0.0 最小值，供 ColorBar 绘制）
}

// NewFromStops 根据控制点数组（至少 2 个色标，均匀分布在 [0, 1]）插值构建 256 阶色表。
func NewFromStops(name string, stops []ui.Color) *Colormap {
	cm := &Colormap{name: name}
	if len(stops) == 0 {
		for i := range 256 {
			v := uint8(i)
			cm.table[i] = ui.RGB(v, v, v)
		}
		return cm
	}
	if len(stops) == 1 {
		c := stops[0]
		c.A = 255
		for i := range 256 {
			cm.table[i] = c
		}
		return cm
	}
	nSeg := float64(len(stops) - 1)
	for i := range 256 {
		t := float64(i) / 255.0
		pos := t * nSeg
		idx := int(math.Floor(pos))
		if idx >= len(stops)-1 {
			c := stops[len(stops)-1]
			c.A = 255
			cm.table[i] = c
			continue
		}
		frac := pos - float64(idx)
		a, b := stops[idx], stops[idx+1]
		cm.table[i] = ui.RGBA(
			lerpByte(a.R, b.R, frac),
			lerpByte(a.G, b.G, frac),
			lerpByte(a.B, b.B, frac),
			255,
		)
	}
	return cm
}

func lerpByte(a, b uint8, t float64) uint8 {
	v := float64(a)*(1.0-t) + float64(b)*t
	return uint8(min(max(math.Round(v), 0), 255))
}

// Name 返回色表名称。
func (c *Colormap) Name() string { return c.name }

// Table 返回 256 阶预计算 RGBA 颜色数组副本。
func (c *Colormap) Table() [256]ui.Color { return c.table }

// At 对归一化标量 t ∈ [0, 1] 进行线性插值采样并返回对应颜色。
func (c *Colormap) At(t float64) ui.Color {
	if math.IsNaN(t) || t <= 0 {
		return c.table[0]
	}
	if t >= 1 {
		return c.table[255]
	}
	pos := t * 255.0
	idx := int(pos)
	if idx >= 255 {
		return c.table[255]
	}
	frac := pos - float64(idx)
	a, b := c.table[idx], c.table[idx+1]
	return ui.RGBA(
		lerpByte(a.R, b.R, frac),
		lerpByte(a.G, b.G, frac),
		lerpByte(a.B, b.B, frac),
		255,
	)
}

func (c *Colormap) ensureBitmaps() {
	c.bmpOnce.Do(func() {
		imgH := image.NewRGBA(image.Rect(0, 0, 256, 1))
		imgV := image.NewRGBA(image.Rect(0, 0, 1, 256))
		for i := range 256 {
			col := c.table[i]
			rgba := color.RGBA{R: col.R, G: col.G, B: col.B, A: 255}
			imgH.SetRGBA(i, 0, rgba)
			// 垂直色棒顶部 y=0 对应最大值 t=1.0（索引 255），底部 y=255 对应最小值 t=0.0（索引 0）
			imgV.SetRGBA(0, 255-i, rgba)
		}
		c.bmpH = ui.NewBitmap(imgH)
		c.bmpV = ui.NewBitmap(imgV)
	})
}

// Bitmap 返回 256x1 一维水平查找表纹理（供 Kage GPU 着色器采样，全局只分配一次）。
func (c *Colormap) Bitmap() *ui.Bitmap {
	c.ensureBitmaps()
	return c.bmpH
}

// VerticalBitmap 返回 1x256 一维垂直查找表纹理（顶部对应最大值，供 ColorBar 与 HistogramLUT 绘制）。
func (c *Colormap) VerticalBitmap() *ui.Bitmap {
	c.ensureBitmaps()
	return c.bmpV
}

var (
	viridisOnce   sync.Once
	viridisCM     *Colormap
	plasmaOnce    sync.Once
	plasmaCM      *Colormap
	infernoOnce   sync.Once
	infernoCM     *Colormap
	magmaOnce     sync.Once
	magmaCM       *Colormap
	cividisOnce   sync.Once
	cividisCM     *Colormap
	turboOnce     sync.Once
	turboCM       *Colormap
	coolwarmOnce  sync.Once
	coolwarmCM    *Colormap
	grayscaleOnce sync.Once
	grayscaleCM   *Colormap
)

// Viridis 返回感知均匀、色盲友好且黑白打印单调递增的默认科研首选色表。
func Viridis() *Colormap {
	viridisOnce.Do(func() {
		viridisCM = NewFromStops("Viridis", []ui.Color{
			ui.RGB(68, 1, 84), ui.RGB(72, 24, 106), ui.RGB(71, 45, 123), ui.RGB(66, 64, 134),
			ui.RGB(59, 82, 139), ui.RGB(51, 99, 141), ui.RGB(44, 114, 142), ui.RGB(38, 130, 142),
			ui.RGB(33, 145, 140), ui.RGB(31, 160, 136), ui.RGB(40, 174, 128), ui.RGB(63, 188, 115),
			ui.RGB(94, 201, 98), ui.RGB(132, 212, 75), ui.RGB(173, 220, 48), ui.RGB(216, 226, 25),
			ui.RGB(253, 231, 37),
		})
	})
	return viridisCM
}

// Plasma 返回高对比度紫-红-亮黄感知均匀色表。
func Plasma() *Colormap {
	plasmaOnce.Do(func() {
		plasmaCM = NewFromStops("Plasma", []ui.Color{
			ui.RGB(13, 8, 135), ui.RGB(46, 5, 150), ui.RGB(75, 3, 161), ui.RGB(103, 0, 168),
			ui.RGB(129, 4, 167), ui.RGB(154, 22, 159), ui.RGB(177, 42, 144), ui.RGB(197, 64, 126),
			ui.RGB(214, 85, 109), ui.RGB(229, 107, 93), ui.RGB(241, 130, 76), ui.RGB(250, 155, 61),
			ui.RGB(254, 180, 47), ui.RGB(253, 206, 38), ui.RGB(246, 233, 35), ui.RGB(240, 249, 33),
		})
	})
	return plasmaCM
}

// Inferno 返回黑-紫-橙-亮白热辐射感知均匀色表（极其适合频谱图与热成像）。
func Inferno() *Colormap {
	infernoOnce.Do(func() {
		infernoCM = NewFromStops("Inferno", []ui.Color{
			ui.RGB(0, 0, 4), ui.RGB(16, 9, 45), ui.RGB(40, 11, 83), ui.RGB(69, 10, 105),
			ui.RGB(98, 19, 110), ui.RGB(128, 31, 108), ui.RGB(156, 41, 100), ui.RGB(184, 54, 85),
			ui.RGB(209, 70, 68), ui.RGB(229, 92, 48), ui.RGB(244, 119, 26), ui.RGB(251, 150, 6),
			ui.RGB(251, 182, 26), ui.RGB(246, 215, 70), ui.RGB(245, 243, 132), ui.RGB(252, 255, 164),
		})
	})
	return infernoCM
}

// Magma 返回黑-深紫-洋红-象牙白感知均匀色表。
func Magma() *Colormap {
	magmaOnce.Do(func() {
		magmaCM = NewFromStops("Magma", []ui.Color{
			ui.RGB(0, 0, 4), ui.RGB(15, 11, 44), ui.RGB(38, 17, 85), ui.RGB(66, 15, 117),
			ui.RGB(95, 24, 127), ui.RGB(124, 35, 130), ui.RGB(153, 45, 128), ui.RGB(183, 55, 121),
			ui.RGB(212, 67, 110), ui.RGB(235, 87, 96), ui.RGB(248, 118, 92), ui.RGB(253, 153, 105),
			ui.RGB(254, 188, 130), ui.RGB(254, 221, 159), ui.RGB(252, 253, 191),
		})
	})
	return magmaCM
}

// Cividis 返回针对红绿色觉缺陷完全优化的蓝-灰-黄严格等亮度梯度色表。
func Cividis() *Colormap {
	cividisOnce.Do(func() {
		cividisCM = NewFromStops("Cividis", []ui.Color{
			ui.RGB(0, 32, 76), ui.RGB(0, 44, 106), ui.RGB(22, 57, 109), ui.RGB(49, 70, 107),
			ui.RGB(70, 82, 107), ui.RGB(89, 95, 109), ui.RGB(107, 108, 113), ui.RGB(125, 124, 120),
			ui.RGB(144, 139, 121), ui.RGB(164, 155, 119), ui.RGB(184, 172, 112), ui.RGB(205, 189, 101),
			ui.RGB(226, 207, 86), ui.RGB(246, 225, 65), ui.RGB(253, 234, 69),
		})
	})
	return cividisCM
}

// Turbo 返回 Google AI 设计的高动态细节平滑彩虹色表（消除传统 Jet 色表的假边缘伪影）。
func Turbo() *Colormap {
	turboOnce.Do(func() {
		turboCM = NewFromStops("Turbo", []ui.Color{
			ui.RGB(48, 18, 59), ui.RGB(65, 69, 171), ui.RGB(70, 117, 237), ui.RGB(57, 164, 251),
			ui.RGB(27, 208, 213), ui.RGB(36, 239, 166), ui.RGB(97, 252, 108), ui.RGB(164, 252, 60),
			ui.RGB(218, 235, 54), ui.RGB(251, 201, 56), ui.RGB(254, 153, 44), ui.RGB(243, 99, 21),
			ui.RGB(217, 56, 7), ui.RGB(177, 25, 1), ui.RGB(122, 4, 3),
		})
	})
	return turboCM
}

// Coolwarm 返回以中性白灰为中心（t=0.5 精确对应零点）的冷蓝-暖红双极性发散色表（相关矩阵/相位差专用）。
func Coolwarm() *Colormap {
	coolwarmOnce.Do(func() {
		coolwarmCM = NewFromStops("Coolwarm", []ui.Color{
			ui.RGB(59, 76, 192), ui.RGB(89, 119, 227), ui.RGB(123, 159, 249), ui.RGB(158, 190, 255),
			ui.RGB(192, 212, 245), ui.RGB(221, 221, 221), ui.RGB(242, 203, 183), ui.RGB(247, 168, 137),
			ui.RGB(238, 127, 99), ui.RGB(214, 82, 68), ui.RGB(180, 4, 38),
		})
	})
	return coolwarmCM
}

// Grayscale 返回线性医学/电子显微灰阶色表（超声 B 超、X 光与 SEM 图像标准）。
func Grayscale() *Colormap {
	grayscaleOnce.Do(func() {
		grayscaleCM = NewFromStops("Grayscale", []ui.Color{
			ui.RGB(0, 0, 0),
			ui.RGB(255, 255, 255),
		})
	})
	return grayscaleCM
}

// BuiltinColormaps 返回全部内置标准科研色表列表（供右键菜单与色表选择器遍历）。
func BuiltinColormaps() []*Colormap {
	return []*Colormap{
		Viridis(),
		Inferno(),
		Plasma(),
		Magma(),
		Turbo(),
		Cividis(),
		Coolwarm(),
		Grayscale(),
	}
}

// ByName 按名称（忽略大小写）查找内置色表；未命中时返回默认 Viridis。
func ByName(name string) *Colormap {
	for _, cm := range BuiltinColormaps() {
		if strings.EqualFold(cm.name, name) {
			return cm
		}
	}
	return Viridis()
}
