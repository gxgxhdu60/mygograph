package items

import (
	"fmt"
	"image"
	"math"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/colormap"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/shaders"
)

// ScalarGrid2D 抽象支持二维物理坐标定位、单元读取与双线性插值采样的标量场接口（供 RectROI / LineROI 零耦合提取数据）。
type ScalarGrid2D interface {
	GridSize() (cols, rows int)
	PhysicalBounds() (x0, x1, y0, y1 float64)
	CellValue(col, row int) (float64, bool)
	SampleBilinear(x, y float64) (float64, bool)
}

// ImageOption 配置 ImageItem 或 WaterfallItem 的可选属性。
type ImageOption func(*imageConfig)

type imageConfig struct {
	name         string
	ctrl         *colormap.ColorControl
	x0, x1       float64
	y0, y1       float64
	hasBounds    bool
	smooth       bool
	originBottom bool
	opacity      float32
	zValue       float32
}

func defaultImageConfig() imageConfig {
	return imageConfig{
		originBottom: true, // 科学坐标系默认第 0 行对应物理 Y0（如 0 Hz 基频）
		opacity:      1.0,
		zValue:       0,
	}
}

// WithImageName 设置热力图/瀑布图名称（用于读数卡片与图例标识）。
func WithImageName(name string) ImageOption {
	return func(cfg *imageConfig) { cfg.name = name }
}

// WithColorControl 绑定外部共享的 ColorControl（与 ColorBar 或 HistogramLUT 实时联动）。
func WithColorControl(ctrl *colormap.ColorControl) ImageOption {
	return func(cfg *imageConfig) { cfg.ctrl = ctrl }
}

// WithPhysicalRect 指定二维网格在物理数据空间中覆盖的矩形边界 [x0, x1] × [y0, y1]。
func WithPhysicalRect(x0, x1, y0, y1 float64) ImageOption {
	return func(cfg *imageConfig) {
		cfg.x0, cfg.x1 = min(x0, x1), max(x0, x1)
		cfg.y0, cfg.y1 = min(y0, y1), max(y0, y1)
		cfg.hasBounds = true
	}
}

// WithSmoothInterpolation 开启解码后 16-bit 标量域双线性平滑插值（默认关闭以保持科学网格硬边像元清晰度）。
func WithSmoothInterpolation(enable bool) ImageOption {
	return func(cfg *imageConfig) { cfg.smooth = enable }
}

// WithOriginTopLeft 将矩阵第 0 行映射至物理 YMax 顶部（适用于标准机器视觉/显微镜图像坐标系）。
func WithOriginTopLeft(topLeft bool) ImageOption {
	return func(cfg *imageConfig) { cfg.originBottom = !topLeft }
}

// WithImageOpacity 设置热力图整体不透明度 [0, 1]（支持半透明叠加在其它底图之上）。
func WithImageOpacity(alpha float32) ImageOption {
	return func(cfg *imageConfig) { cfg.opacity = min(max(alpha, 0), 1) }
}

// WithImageZ 设置热力图图层深度。
func WithImageZ(z float32) ImageOption {
	return func(cfg *imageConfig) { cfg.zValue = z }
}

// ImageItem 是基于 16-bit RGBA 高低字节编码与 Kage GPU 片段着色器的高性能二维标量热力图/时频谱图图元。
// 数据矩阵仅在 SetData 时上传一次 GPU 纹理；拖动 HistogramLUT 调节窗宽窗位 [vMin, vMax] 或切换伪彩色表时，
// CPU 像素重算开销严格为 0ms（0 B/op）。
type ImageItem struct {
	name         string
	visible      bool
	zVal         float32
	smooth       bool
	originBottom bool
	opacity      float32

	cols, rows int
	x0, x1     float64
	y0, y1     float64
	hasBounds  bool

	raw     []float64 // 行主序 row*cols + col 原始双精度矩阵副本（供悬停精确读数与 ROI 切片使用）
	baseMin float64
	baseMax float64
	rgba    *image.RGBA
	bmp     *ui.Bitmap
	dirty   bool

	ctrl  *colormap.ColorControl
	lutSt *shaders.LUT16State
}

// NewImage 创建二维标量热力图图元。
func NewImage(opts ...ImageOption) *ImageItem {
	cfg := defaultImageConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	ctrl := cfg.ctrl
	if ctrl == nil {
		ctrl = colormap.NewAutoColorControl(colormap.Viridis())
	}
	it := &ImageItem{
		name:         cfg.name,
		visible:      true,
		zVal:         cfg.zValue,
		smooth:       cfg.smooth,
		originBottom: cfg.originBottom,
		opacity:      cfg.opacity,
		x0:           cfg.x0,
		x1:           cfg.x1,
		y0:           cfg.y0,
		y1:           cfg.y1,
		hasBounds:    cfg.hasBounds,
		ctrl:         ctrl,
		lutSt:        shaders.NewLUT16State(),
	}
	return it
}

func (im *ImageItem) ZValue() float32      { return im.zVal }
func (im *ImageItem) Visible() bool        { return im.visible }
func (im *ImageItem) SetVisible(v bool)    { im.visible = v }
func (im *ImageItem) LegendName() string   { return im.name }
func (im *ImageItem) LegendColor() ui.Color { return im.ctrl.Colormap().At(0.75) }

// Control 返回当前绑定的 ColorControl（可直接传入 NewColorBar 或 NewHistogramLUT）。
func (im *ImageItem) Control() *colormap.ColorControl { return im.ctrl }

// SetPhysicalRect 动态更新二维矩阵映射的物理坐标边界 [x0, x1] × [y0, y1]。
func (im *ImageItem) SetPhysicalRect(x0, x1, y0, y1 float64) *ImageItem {
	im.x0, im.x1 = min(x0, x1), max(x0, x1)
	im.y0, im.y1 = min(y0, y1), max(y0, y1)
	if im.x1 == im.x0 {
		im.x1 = im.x0 + 1
	}
	if im.y1 == im.y0 {
		im.y1 = im.y0 + 1
	}
	im.hasBounds = true
	return im
}

// SetSmooth 动态切换像素级硬边模式（false）与双线性插值模式（true）。
func (im *ImageItem) SetSmooth(smooth bool) *ImageItem {
	im.smooth = smooth
	return im
}

// SetData 上传尺寸为 cols × rows 的行主序标量矩阵（len(values) >= cols*rows），编码为 16-bit 纹理并同步直方图统计。
func (im *ImageItem) SetData(cols, rows int, values []float64) *ImageItem {
	if cols <= 0 || rows <= 0 || len(values) < cols*rows {
		im.cols, im.rows = 0, 0
		im.raw = im.raw[:0]
		return im
	}
	n := cols * rows
	if cap(im.raw) < n {
		im.raw = make([]float64, n)
	} else {
		im.raw = im.raw[:n]
	}
	copy(im.raw, values[:n])
	im.cols, im.rows = cols, rows
	if !im.hasBounds {
		im.x0, im.x1 = 0, float64(cols)
		im.y0, im.y1 = 0, float64(rows)
	}

	// 扫描有限极值以构建无损覆盖全动态范围的 16-bit 归一化纹理基准 [baseMin, baseMax]
	dMin := math.Inf(1)
	dMax := math.Inf(-1)
	for _, v := range im.raw {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			if v < dMin {
				dMin = v
			}
			if v > dMax {
				dMax = v
			}
		}
	}
	if dMin > dMax {
		dMin, dMax = 0, 1
	} else if dMax == dMin {
		dMax = dMin + 1
	}
	// 预留极小裕量使用户将窗位拓宽至极值之外时仍具备平滑边界过渡
	im.baseMin = dMin
	im.baseMax = dMax
	invSpan := 1.0 / (im.baseMax - im.baseMin)

	if im.rgba == nil || im.rgba.Bounds().Dx() != cols || im.rgba.Bounds().Dy() != rows {
		im.rgba = image.NewRGBA(image.Rect(0, 0, cols, rows))
		im.bmp = nil
	}
	for r := range rows {
		pixRow := im.rgba.Pix[r*im.rgba.Stride : r*im.rgba.Stride+cols*4]
		srcRow := im.raw[r*cols : (r+1)*cols]
		shaders.PackRow16(pixRow, srcRow, im.baseMin, invSpan)
	}
	im.dirty = true
	im.ctrl.UpdateDataStats(dMin, dMax, im.raw)
	return im
}

// SetData32 从 float32 切片载入二维标量矩阵。
func (im *ImageItem) SetData32(cols, rows int, values []float32) *ImageItem {
	n := cols * rows
	if cols <= 0 || rows <= 0 || len(values) < n {
		return im
	}
	if cap(im.raw) < n {
		im.raw = make([]float64, n)
	} else {
		im.raw = im.raw[:n]
	}
	for i := range n {
		im.raw[i] = float64(values[i])
	}
	return im.SetData(cols, rows, im.raw)
}

// GridSize 返回矩阵列数与行数（实现 ScalarGrid2D）。
func (im *ImageItem) GridSize() (cols, rows int) { return im.cols, im.rows }

// PhysicalBounds 返回矩阵覆盖的物理坐标范围（实现 ScalarGrid2D）。
func (im *ImageItem) PhysicalBounds() (x0, x1, y0, y1 float64) {
	return im.x0, im.x1, im.y0, im.y1
}

// CellValue 读取指定网格单元 (col, row) 的原始双精度标量值（实现 ScalarGrid2D）。
func (im *ImageItem) CellValue(col, row int) (float64, bool) {
	if col < 0 || col >= im.cols || row < 0 || row >= im.rows {
		return 0, false
	}
	v := im.raw[row*im.cols+col]
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// SampleBilinear 在物理坐标 (x, y) 处对二维标量场执行双线性插值采样（供 LineROI 剖面线提取使用）。
func (im *ImageItem) SampleBilinear(x, y float64) (float64, bool) {
	if im.cols <= 0 || im.rows <= 0 || x < im.x0 || x > im.x1 || y < im.y0 || y > im.y1 {
		return 0, false
	}
	u := (x - im.x0) / max(im.x1-im.x0, 1e-30)
	v := (y - im.y0) / max(im.y1-im.y0, 1e-30)
	if !im.originBottom {
		v = 1.0 - v
	}
	return bilinearFromGrid(im.raw, im.cols, im.rows, u, v)
}

func (im *ImageItem) Bounds() (xRange, yRange data.Range, ok bool) {
	if im.cols <= 0 || im.rows <= 0 {
		return data.Range{}, data.Range{}, false
	}
	return data.Range{Min: im.x0, Max: im.x1}, data.Range{Min: im.y0, Max: im.y1}, true
}

// SnapNearest 实现 mygograph.SnapTarget，支持十字光标悬停二维标量场精确单元读数（返回 Z 标量格式化信息）。
func (im *ImageItem) SnapNearest(lx, ly float32, plotRect ui.Rect, vb *mygograph.ViewBox) (mygograph.SnapPoint, bool) {
	if im.cols <= 0 || im.rows <= 0 || vb == nil {
		return mygograph.SnapPoint{}, false
	}
	queryX, queryY := vb.LocalToData(lx, ly, plotRect.W, plotRect.H)
	if queryX < im.x0 || queryX > im.x1 || queryY < im.y0 || queryY > im.y1 {
		return mygograph.SnapPoint{}, false
	}
	u := (queryX - im.x0) / max(im.x1-im.x0, 1e-30)
	v := (queryY - im.y0) / max(im.y1-im.y0, 1e-30)
	if !im.originBottom {
		v = 1.0 - v
	}
	col := min(max(int(math.Floor(u*float64(im.cols))), 0), im.cols-1)
	row := min(max(int(math.Floor(v*float64(im.rows))), 0), im.rows-1)
	val, valid := im.CellValue(col, row)
	if !valid {
		return mygograph.SnapPoint{}, false
	}
	_, unit := im.ctrl.LabelAndUnit()
	zText := mygograph.FormatSIValue(val, 4, unit)
	prefix := im.name
	if prefix == "" {
		prefix = "Z"
	}
	return mygograph.SnapPoint{
		DataX:   queryX,
		DataY:   queryY,
		ScreenX: plotRect.X + lx,
		ScreenY: plotRect.Y + ly,
		Label:   fmt.Sprintf("%s=%s [%d,%d]", prefix, zText, col, row),
	}, true
}

var _ mygograph.SnapTarget = (*ImageItem)(nil)

func (im *ImageItem) Draw(rc *mygograph.RenderContext) {
	if !im.visible || im.cols <= 0 || im.rows <= 0 || im.rgba == nil {
		return
	}
	if im.bmp == nil {
		im.bmp = ui.NewBitmap(im.rgba).Pixelated()
		im.dirty = false
	} else if im.dirty {
		im.bmp.Update(im.rgba)
		im.dirty = false
	}

	visRect, uvOffX, uvOffY, uvScaleX, uvScaleY, ok := computeClippedImageUV(
		rc.PlotRect, rc.Transform, im.x0, im.x1, im.y0, im.y1,
	)
	if !ok {
		return
	}

	flags := int32(0)
	// 当第 0 行对应物理 y0 时：若视口未开启 InvertY（y0 在屏幕底部），则需翻转纹理 V 轴
	needFlipY := im.originBottom != rc.View.InvertY()
	if needFlipY {
		flags |= shaders.LUT16FlagFlipY
	}
	// 第 0 列对应物理 x0：视口 InvertX 时 x0 位于屏幕右侧，需翻转纹理 U 轴，使画面与坐标轴及 Snap 读数一致
	if rc.View.InvertX() {
		flags |= shaders.LUT16FlagFlipX
	}
	if im.smooth {
		flags |= shaders.LUT16FlagSmooth
	}

	vMinNorm, invSpanNorm := im.ctrl.NormalizeForBase(im.baseMin, im.baseMax)
	lutBmp := im.ctrl.Colormap().Bitmap()

	im.lutSt.Draw(
		rc.Painter,
		visRect,
		im.bmp,
		lutBmp,
		uvOffX, uvOffY,
		uvScaleX, uvScaleY,
		0,
		vMinNorm, invSpanNorm,
		im.opacity,
		flags,
	)
}

// WaterfallItem 是专为 SDR 无线电频谱瀑布图、声呐回波历史与实时声学时频监控设计的单 DrawCall 环形滚动图元。
// 每次调用 PushRow 仅原地覆写纹理中的单行字节并推进环形写指针 RowOffset，由 lut16.kage 在片段着色器内执行
// fract(uv.y + RowOffset) 循环寻址，彻底消除整张大矩阵内存搬移与双矩形拼接缝隙！
type WaterfallItem struct {
	mu           sync.Mutex
	name         string
	visible      bool
	zVal         float32
	smooth       bool
	newestAtTop  bool // true（默认）：最新推入帧在顶部向下瀑布滚动；false：最新帧在底部向上推进
	opacity      float32
	cols, rows   int
	x0, x1       float64
	y0, y1       float64
	baseMin      float64
	baseMax      float64
	invBaseSpan  float64
	writeRow     int
	filledRows   int
	raw          []float64
	rgba         *image.RGBA
	bmp          *ui.Bitmap
	dirtyGPU     bool
	histSubTick  uint32
	seq          atomic.Uint64
	wakeArmed    atomic.Bool
	wakeFn       func()
	ctrl         *colormap.ColorControl
	lutSt        *shaders.LUT16State
}

// NewWaterfall 创建列宽为 cols（频率/空间采样点数）、历史深度为 rows（时间帧行数）、纹理编码量程为 [baseMin, baseMax] 的环形瀑布图。
func NewWaterfall(cols, rows int, baseMin, baseMax float64, opts ...ImageOption) *WaterfallItem {
	cols = max(cols, 1)
	rows = max(rows, 2)
	if baseMax <= baseMin {
		baseMax = baseMin + 1.0
	}
	cfg := defaultImageConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	ctrl := cfg.ctrl
	if ctrl == nil {
		ctrl = colormap.NewColorControl(colormap.Inferno(), baseMin, baseMax)
	}
	x0, x1 := 0.0, float64(cols)
	y0, y1 := 0.0, float64(rows)
	if cfg.hasBounds {
		x0, x1 = cfg.x0, cfg.x1
		y0, y1 = cfg.y0, cfg.y1
	}

	rgba := image.NewRGBA(image.Rect(0, 0, cols, rows))
	// 初始状态未填充行标记 B=0（透明背景露底），随 PushRow 逐行点亮
	for i := 3; i < len(rgba.Pix); i += 4 {
		rgba.Pix[i] = 255
	}
	raw := make([]float64, cols*rows)
	for i := range raw {
		raw[i] = math.NaN()
	}

	return &WaterfallItem{
		name:        cfg.name,
		visible:     true,
		zVal:        cfg.zValue,
		smooth:      cfg.smooth,
		newestAtTop: true,
		opacity:     cfg.opacity,
		cols:        cols,
		rows:        rows,
		x0:          x0,
		x1:          x1,
		y0:          y0,
		y1:          y1,
		baseMin:     baseMin,
		baseMax:     baseMax,
		invBaseSpan: 1.0 / (baseMax - baseMin),
		raw:         raw,
		rgba:        rgba,
		ctrl:        ctrl,
		lutSt:       shaders.NewLUT16State(),
	}
}

func (wf *WaterfallItem) ZValue() float32      { return wf.zVal }
func (wf *WaterfallItem) Visible() bool        { return wf.visible }
func (wf *WaterfallItem) SetVisible(v bool)    { wf.visible = v }
func (wf *WaterfallItem) LegendName() string   { return wf.name }
func (wf *WaterfallItem) LegendColor() ui.Color { return wf.ctrl.Colormap().At(0.8) }
func (wf *WaterfallItem) Control() *colormap.ColorControl { return wf.ctrl }

// SetNewestAtTop 控制瀑布流方向（true：最新谱线位于顶部向下滚动；false：最新谱线位于底部）。
func (wf *WaterfallItem) SetNewestAtTop(top bool) *WaterfallItem {
	wf.mu.Lock()
	wf.newestAtTop = top
	wf.mu.Unlock()
	return wf
}

// SetWakeFunc 注册后台推帧时的防洪泛 UI 唤醒回调（配合 Plot 的 StreamItem 自动休眠机制）。
func (wf *WaterfallItem) SetWakeFunc(fn func()) {
	wf.mu.Lock()
	wf.wakeFn = fn
	wf.mu.Unlock()
	wf.wakeArmed.Store(fn != nil)
}

// ArmWake 在 UI 渲染帧完成后重新武装单次唤醒门闩。
func (wf *WaterfallItem) ArmWake() {
	wf.wakeArmed.Store(true)
}

// StreamVersion 实现 mygograph.StreamItem，供 Plot 检测活跃推流并在流暂停后自动降为 0% 空闲 CPU。
func (wf *WaterfallItem) StreamVersion() (uint64, bool) {
	wf.ArmWake()
	return wf.seq.Load(), true
}

var _ mygograph.StreamItem = (*WaterfallItem)(nil)

// PushRow 线程安全地向瀑布图环形缓冲推入最新一行频谱/扫描帧（仅执行单行 O(cols) 16-bit 打包，零整图拷贝）。
func (wf *WaterfallItem) PushRow(row []float64) {
	if len(row) == 0 {
		return
	}
	wf.mu.Lock()
	rIdx := wf.writeRow
	n := min(len(row), wf.cols)
	dstRaw := wf.raw[rIdx*wf.cols : (rIdx+1)*wf.cols]
	copy(dstRaw[:n], row[:n])
	for i := n; i < wf.cols; i++ {
		dstRaw[i] = math.NaN()
	}
	pixRow := wf.rgba.Pix[rIdx*wf.rgba.Stride : rIdx*wf.rgba.Stride+wf.cols*4]
	shaders.PackRow16(pixRow, dstRaw, wf.baseMin, wf.invBaseSpan)

	wf.writeRow = (rIdx + 1) % wf.rows
	if wf.filledRows < wf.rows {
		wf.filledRows++
	}
	wf.dirtyGPU = true
	wf.histSubTick++
	shouldRefreshHist := wf.histSubTick == 1 || wf.histSubTick%16 == 0
	var wake func()
	if wf.wakeArmed.CompareAndSwap(true, false) {
		wake = wf.wakeFn
	}
	wf.mu.Unlock()

	wf.seq.Add(1)
	if shouldRefreshHist {
		wf.refreshHistogram()
	}
	if wake != nil {
		wake()
	}
}

func (wf *WaterfallItem) refreshHistogram() {
	wf.mu.Lock()
	// 在锁内对环形缓冲执行轻量级抽样到栈缓冲，避免阻塞采集线程
	var sample [2048]float64
	total := wf.cols * wf.rows
	stride := max(total/len(sample), 1)
	k := 0
	for i := 0; i < total && k < len(sample); i += stride {
		v := wf.raw[i]
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			sample[k] = v
			k++
		}
	}
	bMin, bMax := wf.baseMin, wf.baseMax
	wf.mu.Unlock()

	if k > 0 {
		wf.ctrl.UpdateDataStats(bMin, bMax, sample[:k])
	}
}

func (wf *WaterfallItem) Bounds() (xRange, yRange data.Range, ok bool) {
	return data.Range{Min: wf.x0, Max: wf.x1}, data.Range{Min: wf.y0, Max: wf.y1}, true
}

// WriteRow 返回当前环形写指针位置（仅供单元测试验证单行更新行为）。
func (wf *WaterfallItem) WriteRow() int {
	wf.mu.Lock()
	defer wf.mu.Unlock()
	return wf.writeRow
}

// SnapNearest 实现 mygograph.SnapTarget，支持十字光标在瀑布图任意历史时刻与频率点上读取精确功率值。
func (wf *WaterfallItem) SnapNearest(lx, ly float32, plotRect ui.Rect, vb *mygograph.ViewBox) (mygograph.SnapPoint, bool) {
	if vb == nil {
		return mygograph.SnapPoint{}, false
	}
	queryX, queryY := vb.LocalToData(lx, ly, plotRect.W, plotRect.H)

	wf.mu.Lock()
	defer wf.mu.Unlock()
	if queryX < wf.x0 || queryX > wf.x1 || queryY < wf.y0 || queryY > wf.y1 {
		return mygograph.SnapPoint{}, false
	}
	u := (queryX - wf.x0) / max(wf.x1-wf.x0, 1e-30)
	spanY := max(wf.y1-wf.y0, 1e-30)
	var vFromTop float64
	if vb.InvertY() {
		vFromTop = (queryY - wf.y0) / spanY
	} else {
		vFromTop = (wf.y1 - queryY) / spanY
	}
	// 在纹理环形空间中：行 (writeRow + k) % rows 按 k=0..rows-1 从最旧排到最新
	var ageFrac float64
	if wf.newestAtTop {
		ageFrac = 1.0 - vFromTop
	} else {
		ageFrac = vFromTop
	}
	col := min(max(int(math.Floor(u*float64(wf.cols))), 0), wf.cols-1)
	ageIdx := min(max(int(math.Floor(ageFrac*float64(wf.rows))), 0), wf.rows-1)
	ringRow := (wf.writeRow + ageIdx) % wf.rows
	val := wf.raw[ringRow*wf.cols+col]
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return mygograph.SnapPoint{}, false
	}
	_, unit := wf.ctrl.LabelAndUnit()
	prefix := wf.name
	if prefix == "" {
		prefix = "W"
	}
	return mygograph.SnapPoint{
		DataX:   queryX,
		DataY:   queryY,
		ScreenX: plotRect.X + lx,
		ScreenY: plotRect.Y + ly,
		Label:   fmt.Sprintf("%s=%s", prefix, mygograph.FormatSIValue(val, 4, unit)),
	}, true
}

var _ mygograph.SnapTarget = (*WaterfallItem)(nil)

func (wf *WaterfallItem) Draw(rc *mygograph.RenderContext) {
	if !wf.visible {
		return
	}
	visRect, uvOffX, uvOffY, uvScaleX, uvScaleY, ok := computeClippedImageUV(
		rc.PlotRect, rc.Transform, wf.x0, wf.x1, wf.y0, wf.y1,
	)
	if !ok {
		return
	}

	wf.mu.Lock()
	if wf.bmp == nil {
		wf.bmp = ui.NewBitmap(wf.rgba).Pixelated()
		wf.dirtyGPU = false
	} else if wf.dirtyGPU {
		wf.bmp.Update(wf.rgba)
		wf.dirtyGPU = false
	}
	rowOffset := float32(wf.writeRow) / float32(wf.rows)
	newestTop := wf.newestAtTop
	bmp := wf.bmp
	bMin, bMax := wf.baseMin, wf.baseMax
	smooth := wf.smooth
	opacity := wf.opacity
	wf.mu.Unlock()

	flags := shaders.LUT16FlagCyclicY
	// computeClippedImageUV 始终将屏幕上边缘映射为 uv.y=0、屏幕下边缘映射为 uv.y=1；
	// 环形纹理中 uv=0(+RowOffset) 为最旧帧，uv=1(-eps+RowOffset) 为最新帧。
	// 因此当 newestAtTop=true 时始终翻转 uv.y，使最新谱线稳定位于屏幕顶部并向下滚动。
	if newestTop {
		flags |= shaders.LUT16FlagFlipY
	}
	if rc.View.InvertX() {
		flags |= shaders.LUT16FlagFlipX
	}
	if smooth {
		flags |= shaders.LUT16FlagSmooth
	}

	vMinNorm, invSpanNorm := wf.ctrl.NormalizeForBase(bMin, bMax)
	lutBmp := wf.ctrl.Colormap().Bitmap()

	wf.lutSt.Draw(
		rc.Painter,
		visRect,
		bmp,
		lutBmp,
		uvOffX, uvOffY,
		uvScaleX, uvScaleY,
		rowOffset,
		vMinNorm, invSpanNorm,
		opacity,
		flags,
	)
}

func computeClippedImageUV(
	plotRect ui.Rect,
	vt data.ViewportTransform,
	x0, x1, y0, y1 float64,
) (visRect ui.Rect, uvOffX, uvOffY, uvScaleX, uvScaleY float32, ok bool) {
	sx0 := vt.DataToScreenX(x0)
	sx1 := vt.DataToScreenX(x1)
	sy0 := vt.DataToScreenY(y0)
	sy1 := vt.DataToScreenY(y1)

	fullLeft := min(sx0, sx1)
	fullRight := max(sx0, sx1)
	fullTop := min(sy0, sy1)
	fullBot := max(sy0, sy1)
	fullW := fullRight - fullLeft
	fullH := fullBot - fullTop
	if fullW < 0.5 || fullH < 0.5 {
		return ui.Rect{}, 0, 0, 0, 0, false
	}

	visLeft := max(fullLeft, plotRect.X)
	visRight := min(fullRight, plotRect.X+plotRect.W)
	visTop := max(fullTop, plotRect.Y)
	visBot := min(fullBot, plotRect.Y+plotRect.H)
	if visRight <= visLeft || visBot <= visTop {
		return ui.Rect{}, 0, 0, 0, 0, false
	}

	visRect = ui.Rect{
		X: visLeft,
		Y: visTop,
		W: visRight - visLeft,
		H: visBot - visTop,
	}
	uvOffX = (visLeft - fullLeft) / fullW
	uvScaleX = visRect.W / fullW
	uvOffY = (visTop - fullTop) / fullH
	uvScaleY = visRect.H / fullH
	return visRect, uvOffX, uvOffY, uvScaleX, uvScaleY, true
}

func bilinearFromGrid(raw []float64, cols, rows int, u, v float64) (float64, bool) {
	gx := u*float64(cols) - 0.5
	gy := v*float64(rows) - 0.5
	x0 := int(math.Floor(gx))
	y0 := int(math.Floor(gy))
	fx := gx - float64(x0)
	fy := gy - float64(y0)
	x1 := min(max(x0+1, 0), cols-1)
	y1 := min(max(y0+1, 0), rows-1)
	x0 = min(max(x0, 0), cols-1)
	y0 = min(max(y0, 0), rows-1)

	var sum, wSum float64
	sample := func(c, r int, w float64) {
		val := raw[r*cols+c]
		if !math.IsNaN(val) && !math.IsInf(val, 0) {
			sum += val * w
			wSum += w
		}
	}
	sample(x0, y0, (1-fx)*(1-fy))
	sample(x1, y0, fx*(1-fy))
	sample(x0, y1, (1-fx)*fy)
	sample(x1, y1, fx*fy)
	if wSum <= 1e-9 {
		return 0, false
	}
	return sum / wSum, true
}

func buildColorControlMenu(sub *ui.Menu, ctrl *colormap.ColorControl) {
	if ctrl == nil {
		return
	}
	if sub.Item("Auto Reset Contrast Levels").Chosen() {
		ctrl.ResetLevels()
	}
	sub.Separator()
	curName := ctrl.Colormap().Name()
	for _, cm := range colormap.BuiltinColormaps() {
		if sub.Item("Colormap: " + cm.Name()).Radio(cm.Name() == curName).Chosen() {
			ctrl.SetColormap(cm)
		}
	}
}

// BuildContextMenu 实现 mygograph.ContextMenuProvider：支持直接在热力图上右键切换色表、平滑插值与重置窗位。
func (im *ImageItem) BuildContextMenu(m *ui.Menu, _, _, _, _ float32, _ *mygograph.ViewBox) {
	title := "Heatmap Options"
	if im.name != "" {
		title = "Heatmap: " + im.name
	}
	m.Submenu(title, func(sub *ui.Menu) {
		if sub.Item("Smooth Bilinear Filter").Checked(im.smooth).Chosen() {
			im.smooth = !im.smooth
		}
		sub.Separator()
		buildColorControlMenu(sub, im.ctrl)
	})
}

// BuildContextMenu 实现 mygograph.ContextMenuProvider：支持在实时瀑布图上右键切换色表、流向与平滑滤波。
func (wf *WaterfallItem) BuildContextMenu(m *ui.Menu, _, _, _, _ float32, _ *mygograph.ViewBox) {
	title := "Waterfall Options"
	if wf.name != "" {
		title = "Waterfall: " + wf.name
	}
	m.Submenu(title, func(sub *ui.Menu) {
		wf.mu.Lock()
		smooth := wf.smooth
		top := wf.newestAtTop
		wf.mu.Unlock()

		if sub.Item("Newest Frame at Top").Checked(top).Chosen() {
			wf.SetNewestAtTop(!top)
		}
		if sub.Item("Smooth Bilinear Filter").Checked(smooth).Chosen() {
			wf.mu.Lock()
			wf.smooth = !wf.smooth
			wf.mu.Unlock()
		}
		sub.Separator()
		buildColorControlMenu(sub, wf.ctrl)
	})
}

var (
	_ mygograph.ContextMenuProvider = (*ImageItem)(nil)
	_ mygograph.ContextMenuProvider = (*WaterfallItem)(nil)
)
