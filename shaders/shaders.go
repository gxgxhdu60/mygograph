package shaders

import (
	_ "embed"
	"math"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
)

//go:embed lut16.kage
var lut16Src string

// LUT16Flags 定义 lut16.kage 的行为控制位掩码。
const (
	LUT16FlagFlipY   int32 = 1 << 0 // 将第 0 行置于物理坐标系底部（YMin）
	LUT16FlagSmooth  int32 = 1 << 1 // 开启 16-bit 解码域双线性插值
	LUT16FlagCyclicY int32 = 1 << 2 // 开启环形瀑布图循环行寻址
)

// LUT16Program 封装已编译的 16-bit 标量伪彩着色器及预绑定的零分配 Uniform 句柄。
type LUT16Program struct {
	Shader      *shader.Shader
	UVOffset    shader.Uniform
	UVScale     shader.Uniform
	RowOffset   shader.Uniform
	VMinNorm    shader.Uniform
	InvSpanNorm shader.Uniform
	Opacity     shader.Uniform
	Flags       shader.Uniform
}

var (
	lut16Once sync.Once
	lut16Prog *LUT16Program
)

// LUT16 返回全局单例编译的 16-bit 伪彩 LUT 着色器程序。
func LUT16() *LUT16Program {
	lut16Once.Do(func() {
		s := shader.MustCompile(lut16Src)
		lut16Prog = &LUT16Program{
			Shader:      s,
			UVOffset:    s.Uniform("UVOffset"),
			UVScale:     s.Uniform("UVScale"),
			RowOffset:   s.Uniform("RowOffset"),
			VMinNorm:    s.Uniform("VMinNorm"),
			InvSpanNorm: s.Uniform("InvSpanNorm"),
			Opacity:     s.Uniform("Opacity"),
			Flags:       s.Uniform("Flags"),
		}
	})
	return lut16Prog
}

// LUT16State 为单个图元实例持久持有的零分配 Uniform 值缓冲（每帧仅原地写入 uint32 槽位，0 allocs/op）。
type LUT16State struct {
	prog   *LUT16Program
	values *shader.Values
}

// NewLUT16State 为图元分配可复用的 LUT16 Uniform 缓冲。
func NewLUT16State() *LUT16State {
	p := LUT16()
	return &LUT16State{
		prog:   p,
		values: p.Shader.NewValues(),
	}
}

// Draw 使用当前图元的复用 Uniform 槽位执行零分配 GPU/CPU 着色绘制。
func (st *LUT16State) Draw(
	painter *ui.Painter,
	rect ui.Rect,
	dataBmp *ui.Bitmap,
	lutBmp *ui.Bitmap,
	uvOffX, uvOffY float32,
	uvScaleX, uvScaleY float32,
	rowOffset float32,
	vMinNorm, invSpanNorm float32,
	opacity float32,
	flags int32,
) {
	if dataBmp == nil || lutBmp == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	pr := st.prog
	st.values.
		Vec2(pr.UVOffset, uvOffX, uvOffY).
		Vec2(pr.UVScale, uvScaleX, uvScaleY).
		Float(pr.RowOffset, rowOffset).
		Float(pr.VMinNorm, vMinNorm).
		Float(pr.InvSpanNorm, invSpanNorm).
		Float(pr.Opacity, opacity).
		Int(pr.Flags, flags)

	pr.Shader.Draw(painter, rect, shader.Options{
		Values: st.values,
		Images: [4]*ui.Bitmap{dataBmp, lutBmp, nil, nil},
	})
}

// PackRow16 将一行 float64 物理标量按基准区间 [baseMin, baseMin + 1/invBaseSpan] 编码进 RGBA 字节切片：
//   - R = uint8(q >> 8), G = uint8(q & 0xFF) 承载 16-bit 无损动态精度（65536 级）
//   - B = 255 表示有效样本，B = 0 表示 NaN/Inf 空洞
//   - A = 255 严格保持不透明，防止纹理上传阶段发生预乘截断失真
func PackRow16(dstPix []byte, src []float64, baseMin, invBaseSpan float64) {
	n := len(src)
	if len(dstPix) < n*4 {
		n = len(dstPix) / 4
	}
	for i := 0; i < n; i++ {
		v := src[i]
		off := i * 4
		if math.IsNaN(v) || math.IsInf(v, 0) {
			dstPix[off] = 0
			dstPix[off+1] = 0
			dstPix[off+2] = 0
			dstPix[off+3] = 255
			continue
		}
		norm := (v - baseMin) * invBaseSpan
		if norm <= 0 {
			norm = 0
		} else if norm >= 1 {
			norm = 1
		}
		q := uint16(norm*65535.0 + 0.5)
		dstPix[off] = uint8(q >> 8)
		dstPix[off+1] = uint8(q)
		dstPix[off+2] = 255
		dstPix[off+3] = 255
	}
}
