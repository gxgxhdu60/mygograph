package mygograph

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

func init() {
	csvExportHook = FormatViewportCSV
}

// Viewable 抽象所有可通过 View(c *ui.Context) 挂载渲染的图表组件（如 *Plot、*GraphLayout、*ColorBar、*HistogramLUT）。
type Viewable interface {
	View(c *ui.Context) ui.Element
}

// RenderWidget 在无窗口离屏环境下按指定逻辑尺寸（width × height DIP）与超采样倍率（scale，如 2.0 对应 Retina / 3.0 对应论文级 300DPI）
// 渲染任意图表组件，返回物理分辨率为 (width*scale) × (height*scale) 的 *image.RGBA 位图。
func RenderWidget(w Viewable, width, height int, scale float32) *image.RGBA {
	if w == nil {
		return image.NewRGBA(image.Rect(0, 0, max(width, 1), max(height, 1)))
	}
	return RenderFunc(func(c *ui.Context) {
		w.View(c)
	}, width, height, scale)
}

// RenderFunc 在无窗口离屏环境下渲染自定义 mygo 视图闭包。
func RenderFunc(view func(c *ui.Context), width, height int, scale float32) *image.RGBA {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	if scale <= 0 || math.IsNaN(float64(scale)) || math.IsInf(float64(scale), 0) {
		scale = 1.0
	}
	// 首帧完成布局收敛与 AutoRange 计算，次帧输出稳定对齐的最终超采样帧
	_ = ui.Render(view, width, height, scale)
	return ui.Render(view, width, height, scale)
}

// EncodePNG 将离屏超采样渲染结果编码为 PNG 字节流写入 w。
func EncodePNG(w io.Writer, v Viewable, width, height int, scale float32) error {
	if w == nil {
		return fmt.Errorf("mygograph: nil writer")
	}
	img := RenderWidget(v, width, height, scale)
	return png.Encode(w, img)
}

// SavePNG 将任意图表或复合排版组件（Plot / GraphLayout）以指定逻辑尺寸和 HiDPI 倍采样率导出为高清 PNG 文件。
func SavePNG(path string, v Viewable, width, height int, scale float32) error {
	if path == "" {
		return fmt.Errorf("mygograph: empty output path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	var buf bytes.Buffer
	if err := EncodePNG(&buf, v, width, height, scale); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// SavePNGFunc 将自定义 mygo 视图函数（例如组合了顶部工具栏 + GraphLayout + 右侧 HistogramLUT 的完整面板）导出为高清 PNG 文件。
func SavePNGFunc(path string, view func(c *ui.Context), width, height int, scale float32) error {
	if path == "" {
		return fmt.Errorf("mygograph: empty output path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	img := RenderFunc(view, width, height, scale)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// SavePNG 便捷方法：将当前单图以 width × height DIP 和 scale 超采样倍率保存为 PNG。
func (p *Plot) SavePNG(path string, width, height int, scale float32) error {
	return SavePNG(path, p, width, height, scale)
}

// SavePNG 便捷方法：将当前多子图对齐排版矩阵保存为高清 PNG。
func (gl *GraphLayout) SavePNG(path string, width, height int, scale float32) error {
	return SavePNG(path, gl, width, height, scale)
}

// FormatViewportCSV 收集 Plot 内所有可见且实现了 ViewportCSVProvider 的图元在当前视口内的全精度数据，
// 格式化为标准 RFC 4180 CSV 文本（供右键菜单 Copy Viewport CSV 与文件导出共用）。
func FormatViewportCSV(p *Plot) string {
	if p == nil {
		return ""
	}
	var sb strings.Builder
	_ = WriteViewportCSV(&sb, p)
	return sb.String()
}

// WriteViewportCSV 将当前视口可见全精度数据流式写入 w。
func WriteViewportCSV(w io.Writer, p *Plot) error {
	if w == nil || p == nil {
		return nil
	}
	var allHeaders []string
	var allCols [][]float64
	maxRows := 0

	for _, it := range p.items {
		if it == nil || !it.Visible() {
			continue
		}
		prov, ok := it.(ViewportCSVProvider)
		if !ok {
			continue
		}
		ser, ok := prov.ExportViewportCSV(p.vb)
		if !ok || len(ser.Columns) == 0 {
			continue
		}
		for idx, col := range ser.Columns {
			hdr := fmt.Sprintf("Col_%d", len(allCols)+1)
			if idx < len(ser.Headers) && ser.Headers[idx] != "" {
				hdr = ser.Headers[idx]
			}
			allHeaders = append(allHeaders, hdr)
			allCols = append(allCols, col)
			if len(col) > maxRows {
				maxRows = len(col)
			}
		}
	}

	if len(allCols) == 0 || maxRows == 0 {
		return nil
	}

	var lineBuf []byte
	// 1. 写入表头行
	for cIdx, h := range allHeaders {
		if cIdx > 0 {
			lineBuf = append(lineBuf, ',')
		}
		lineBuf = appendCSVEscaped(lineBuf, h)
	}
	lineBuf = append(lineBuf, '\n')
	if _, err := w.Write(lineBuf); err != nil {
		return err
	}

	// 2. 逐行写入全精度 IEEE-754 float64 采样值（不同曲线采样点数不等时短列自动留空对齐）
	for r := range maxRows {
		lineBuf = lineBuf[:0]
		for cIdx, col := range allCols {
			if cIdx > 0 {
				lineBuf = append(lineBuf, ',')
			}
			if r < len(col) {
				v := col[r]
				if math.IsNaN(v) {
					lineBuf = append(lineBuf, "NaN"...)
				} else if math.IsInf(v, 1) {
					lineBuf = append(lineBuf, "+Inf"...)
				} else if math.IsInf(v, -1) {
					lineBuf = append(lineBuf, "-Inf"...)
				} else {
					lineBuf = strconv.AppendFloat(lineBuf, v, 'g', -1, 64)
				}
			}
		}
		lineBuf = append(lineBuf, '\n')
		if _, err := w.Write(lineBuf); err != nil {
			return err
		}
	}
	return nil
}

// SaveViewportCSV 将 Plot 当前视口内的原始精度数据保存为 CSV 文件。
func SaveViewportCSV(path string, p *Plot) error {
	if path == "" {
		return fmt.Errorf("mygograph: empty csv output path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	var buf bytes.Buffer
	if err := WriteViewportCSV(&buf, p); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func appendCSVEscaped(dst []byte, s string) []byte {
	if !strings.ContainsAny(s, ",\"\r\n") {
		return append(dst, s...)
	}
	dst = append(dst, '"')
	for i := range len(s) {
		ch := s[i]
		if ch == '"' {
			dst = append(dst, '"', '"')
		} else {
			dst = append(dst, ch)
		}
	}
	dst = append(dst, '"')
	return dst
}
