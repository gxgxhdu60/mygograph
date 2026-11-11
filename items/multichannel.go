package items

import (
	"image"
	"image/color"
	"math"

	"github.com/egoist/mygo/ui"
	"github.com/gaoxi/mygograph"
	"github.com/gaoxi/mygograph/data"
	"github.com/gaoxi/mygograph/shaders"
)

// MultiDisplayMode 定义多通道实时流的走纸刷新模式。
type MultiDisplayMode uint8

const (
	// DisplayRollMode 滚动走纸模式：新数据从右侧连续推入，整体历史向左平滑平移。
	DisplayRollMode MultiDisplayMode = iota
	// DisplaySweepMode 监护仪擦除扫描模式：时间轴固定为 [0, WindowSec]，扫描头从左向右推进并擦除前方旧波形。
	DisplaySweepMode
)

// MultiRenderTier 控制多通道渲染管线的双模切换策略。
type MultiRenderTier uint8

const (
	// TierAuto 自适应双模（默认）：可见导联数 <= 32 走矢量折线快路径，> 32 走单次 DrawCall Kage 着色器路径。
	TierAuto MultiRenderTier = iota
	// TierVector 强制使用不透明 StrokePolyline 矢量折线路径。
	TierVector
	// TierShader 强制使用 multistack.kage 16-bit 包络纹理 GPU 着色器路径。
	TierShader
)

const (
	defaultVectorThreshold = 32
	defaultMaxLaneAmp      = 4.0 // 允许大振幅棘波跨越相邻 ±4 个导联而不被硬截断
)

// MultiChannelOption 配置 MultiChannelItem。
type MultiChannelOption func(*MultiChannelItem)

// WithDisplayMode 设置初始走纸模式（DisplayRollMode 或 DisplaySweepMode）。
func WithDisplayMode(mode MultiDisplayMode) MultiChannelOption {
	return func(mc *MultiChannelItem) { mc.mode = mode }
}

// WithWindowSeconds 设置实时跟随窗口宽度（秒，默认 10s）。
func WithWindowSeconds(sec float64) MultiChannelOption {
	return func(mc *MultiChannelItem) {
		if sec > 0 {
			mc.windowSec = sec
		}
	}
}

// WithEraseGapSeconds 设置擦除扫描模式下的空白擦除带宽度（秒，默认 0.18s）。
func WithEraseGapSeconds(sec float64) MultiChannelOption {
	return func(mc *MultiChannelItem) {
		if sec >= 0 {
			mc.eraseGapSec = sec
		}
	}
}

// WithChannelScale 设置单导联 1.0 车道高度对应的物理峰峰值量程（例如脑电图 200e-6 表示 ±100μV 填满单车道）。
func WithChannelScale(peakToPeak float64) MultiChannelOption {
	return func(mc *MultiChannelItem) {
		if peakToPeak > 0 {
			mc.lanePeakToPeak = peakToPeak
		}
	}
}

// WithDCRemoval 启用视图层实时去直流基线漂移（动态扣除各通道当前窗口均值，绝不篡改底层原始缓冲）。
func WithDCRemoval(enable bool) MultiChannelOption {
	return func(mc *MultiChannelItem) { mc.dcRemoval = enable }
}

// WithRenderTier 指定渲染层双模策略（默认 TierAuto）。
func WithRenderTier(tier MultiRenderTier) MultiChannelOption {
	return func(mc *MultiChannelItem) { mc.tier = tier }
}

// MultiChannelItem 是专为 64~256 导联高密度脑电（EEG/iEEG）、肌电（EMG）、工业振动阵列与逻辑分析仪设计的双模实时波形引擎。
//
// 核心架构（DESIGN.md 第 6 节）：
//   - 各导联 ch (0..C-1) 纵向中心对齐在物理坐标 Y = float64(ch)，与 NewChannelAxis 无缝联动；
//   - 可见导联 <= 32（或双击单导联独奏放大）时采用不透明 StrokePolyline 矢量路径；
//   - 可见导联 > 32 时自动切换为 multistack.kage 单次 DrawCall SDF 抗锯齿着色器路径，消除上千次路径提交瓶颈；
//   - 支持 Alt+滚轮无级调节灵敏度增益、坏道静音灰化、双击单导联独奏放大/还原与视图级去直流偏置。
type MultiChannelItem struct {
	ring    *data.MultiRingBuffer
	visible bool
	zVal    float32

	mode           MultiDisplayMode
	tier           MultiRenderTier
	windowSec      float64
	eraseGapSec    float64
	lanePeakToPeak float64
	gain           float64 // 叠加在 lanePeakToPeak 上的无级增益倍率（Alt+滚轮调节）
	dcRemoval      bool
	showZeroLines  bool
	lineWidth      float32
	overlapRadius  int32

	muted    []bool
	selected int
	soloCh   int

	// 零分配稳态复用缓冲区（预分配一次，每帧 0 B/op 复用，min/max/valid/spans 常驻 CPU L1 缓存）
	envBuf       []data.MinMax
	spanSlot0    []int32
	spanLen      []int32
	minScratch   []float32
	maxScratch   []float32
	validScratch []byte
	dcMeans      []float32
	polyBuf      [][2]float32
	envRGBA      *image.RGBA
	envBmp       *ui.Bitmap
	palRGBA      *image.RGBA
	palBmp       *ui.Bitmap
	palDirty     bool
	lastThemeD   bool
	shaderSt     *shaders.MultiStackState
}

// NewMultiChannel 创建绑定至 MultiRingBuffer 的高密度多导联实时波形图元。
func NewMultiChannel(ring *data.MultiRingBuffer, opts ...MultiChannelOption) *MultiChannelItem {
	nCh := 1
	if ring != nil && ring.Channels() > 0 {
		nCh = ring.Channels()
	}
	mc := &MultiChannelItem{
		ring:           ring,
		visible:        true,
		mode:           DisplayRollMode,
		tier:           TierAuto,
		windowSec:      10.0,
		eraseGapSec:    0.18,
		lanePeakToPeak: 200e-6, // 默认 ±100 μV 典型脑电图量程
		gain:           1.0,
		dcRemoval:      false,
		showZeroLines:  false,
		lineWidth:      1.25,
		overlapRadius:  2,
		muted:          make([]bool, nCh),
		dcMeans:        make([]float32, nCh),
		selected:       -1,
		soloCh:         -1,
		palDirty:       true,
		shaderSt:       shaders.NewMultiStackState(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(mc)
		}
	}
	return mc
}

func (mc *MultiChannelItem) ZValue() float32   { return mc.zVal }
func (mc *MultiChannelItem) Visible() bool     { return mc.visible }
func (mc *MultiChannelItem) SetVisible(v bool) { mc.visible = v }

// Ring 返回底层绑定的多通道环形缓冲区。
func (mc *MultiChannelItem) Ring() *data.MultiRingBuffer { return mc.ring }

// StreamVersion 实现 mygograph.StreamItem：流活跃时请求下一 VSync 帧，流暂停后自动进入 0% CPU 休眠。
func (mc *MultiChannelItem) StreamVersion() (uint64, bool) {
	if mc.ring == nil {
		return 0, false
	}
	mc.ring.ArmWake()
	return mc.ring.Version(), true
}

var _ mygograph.StreamItem = (*MultiChannelItem)(nil)

// DisplayMode 返回当前走纸刷新模式。
func (mc *MultiChannelItem) DisplayMode() MultiDisplayMode { return mc.mode }

// SetDisplayMode 切换滚动走纸模式（DisplayRollMode）与擦除扫描模式（DisplaySweepMode）。
func (mc *MultiChannelItem) SetDisplayMode(mode MultiDisplayMode) *MultiChannelItem {
	mc.mode = mode
	return mc
}

// DCRemoval 返回是否开启视图级实时去直流偏置。
func (mc *MultiChannelItem) DCRemoval() bool { return mc.dcRemoval }

// SetDCRemoval 动态开关视图级去直流基线漂移。
func (mc *MultiChannelItem) SetDCRemoval(enable bool) *MultiChannelItem {
	mc.dcRemoval = enable
	return mc
}

// Gain 返回当前波形幅值增益倍率。
func (mc *MultiChannelItem) Gain() float64 { return mc.gain }

// SetGain 设置幅值灵敏度增益倍率（限制在 [0.02, 500] 安全区间）。
func (mc *MultiChannelItem) SetGain(g float64) *MultiChannelItem {
	mc.gain = min(max(g, 0.02), 500.0)
	return mc
}

// EffectiveSensitivity 返回单导联车道当前等效的物理峰峰值量程（例如 μV/ch）。
func (mc *MultiChannelItem) EffectiveSensitivity() float64 {
	return mc.lanePeakToPeak / max(mc.gain, 1e-9)
}

// SetChannelMuted 标记或解除坏道静音灰化状态。
func (mc *MultiChannelItem) SetChannelMuted(ch int, muted bool) {
	if ch >= 0 && ch < len(mc.muted) {
		mc.muted[ch] = muted
		mc.palDirty = true
	}
}

// IsChannelMuted 查询指定导联是否处于坏道静音状态。
func (mc *MultiChannelItem) IsChannelMuted(ch int) bool {
	return ch >= 0 && ch < len(mc.muted) && mc.muted[ch]
}

// SelectedChannel 返回当前高亮选中的导联索引（-1 表示无）。
func (mc *MultiChannelItem) SelectedChannel() int { return mc.selected }

// SetSelectedChannel 设置高亮导联索引（传 -1 取消高亮）。
func (mc *MultiChannelItem) SetSelectedChannel(ch int) {
	if mc.selected != ch {
		mc.selected = ch
		mc.palDirty = true
	}
}

func (mc *MultiChannelItem) Bounds() (xRange, yRange data.Range, ok bool) {
	if mc.ring == nil || mc.ring.Channels() == 0 {
		return data.Range{}, data.Range{}, false
	}
	nCh := mc.ring.Channels()
	yRange = data.Range{Min: -0.8, Max: float64(nCh-1) + 0.8}
	if mc.mode == DisplaySweepMode {
		return data.Range{Min: 0, Max: mc.windowSec}, yRange, true
	}
	tLatest := mc.ring.LatestTime()
	if tLatest <= mc.windowSec {
		return data.Range{Min: 0, Max: mc.windowSec}, yRange, true
	}
	return data.Range{Min: tLatest - mc.windowSec, Max: tLatest}, yRange, true
}

// InterceptWheel 实现 mygograph.WheelInterceptor：按住 Alt 滚动鼠标滚轮时无级调节全导联幅值增益（μV/div）。
func (mc *MultiChannelItem) InterceptWheel(ev ui.InputEvent, lx, ly, pw, ph float32, vb *mygograph.ViewBox) bool {
	if ev.Mods&ui.Alt == 0 {
		return false
	}
	delta := ev.DY
	if delta == 0 {
		delta = ev.DX
	}
	if delta == 0 {
		return false
	}
	// 向上推滚轮（delta < 0）增大增益放大微弱脑电波，向下拉减小增益防大伪迹削顶
	factor := math.Exp(-float64(delta) * 0.0025)
	mc.SetGain(mc.gain * factor)
	return true
}

// HitTest 实现 InteractiveItem：允许 Ctrl+单击快速静音坏道，或双击任意导联进入单导联独奏放大检视。
func (mc *MultiChannelItem) HitTest(lx, ly, pw, ph float32, vb *mygograph.ViewBox) (bool, ui.Cursor) {
	return false, 0 // 单击拖拽交给 ViewBox 平移视口；双击由 Plot.handleInput 优先转发给 HandlePointer
}

// HandlePointer 响应双击单导联置顶独奏放大（Solo Zoom）与再次双击一键还原全导联铺满。
func (mc *MultiChannelItem) HandlePointer(ev ui.InputEvent, lx, ly, pw, ph float32, vb *mygograph.ViewBox) bool {
	if ev.Kind != ui.InputPointerDown || ev.Button != 0 || ev.Clicks < 2 || mc.ring == nil {
		return false
	}
	nCh := mc.ring.Channels()
	if mc.soloCh >= 0 {
		mc.soloCh = -1
		if !vb.PopZoom() {
			vb.SetYRange(-0.8, float64(nCh-1)+0.8, 0)
		}
		return true
	}
	_, dy := vb.LocalToData(lx, ly, pw, ph)
	ch := int(math.Round(dy))
	if ch < 0 || ch >= nCh {
		return false
	}
	mc.soloCh = ch
	mc.SetSelectedChannel(ch)
	vb.PushZoom()
	vb.SetYRange(float64(ch)-0.85, float64(ch)+0.85, 0)
	return true
}

func (mc *MultiChannelItem) ensureBuffers(nCh, buckets int, th mygograph.Theme) {
	if cap(mc.minScratch) < buckets {
		mc.spanSlot0 = make([]int32, buckets)
		mc.spanLen = make([]int32, buckets)
		mc.minScratch = make([]float32, buckets)
		mc.maxScratch = make([]float32, buckets)
		mc.validScratch = make([]byte, buckets)
	} else {
		mc.spanSlot0 = mc.spanSlot0[:buckets]
		mc.spanLen = mc.spanLen[:buckets]
		mc.minScratch = mc.minScratch[:buckets]
		mc.maxScratch = mc.maxScratch[:buckets]
		mc.validScratch = mc.validScratch[:buckets]
	}
	if len(mc.dcMeans) < nCh {
		mc.dcMeans = make([]float32, nCh)
	}
	if len(mc.muted) < nCh {
		old := mc.muted
		mc.muted = make([]bool, nCh)
		copy(mc.muted, old)
		mc.palDirty = true
	}
	if mc.envRGBA == nil || mc.envRGBA.Bounds().Dx() != buckets || mc.envRGBA.Bounds().Dy() != nCh*2 {
		mc.envRGBA = image.NewRGBA(image.Rect(0, 0, buckets, nCh*2))
		mc.envBmp = nil
	}
	if mc.palRGBA == nil || mc.palRGBA.Bounds().Dx() != nCh || mc.palDirty || mc.lastThemeD != th.Dark {
		if mc.palRGBA == nil || mc.palRGBA.Bounds().Dx() != nCh {
			mc.palRGBA = image.NewRGBA(image.Rect(0, 0, nCh, 1))
			mc.palBmp = nil
		}
		for ch := range nCh {
			c := mc.channelColor(ch, th)
			mc.palRGBA.SetRGBA(ch, 0, color.RGBA{R: c.R, G: c.G, B: c.B, A: 255})
		}
		if mc.palBmp == nil {
			mc.palBmp = ui.NewBitmap(mc.palRGBA).Pixelated()
		} else {
			mc.palBmp.Update(mc.palRGBA)
		}
		mc.palDirty = false
		mc.lastThemeD = th.Dark
	}
}

func (mc *MultiChannelItem) channelColor(ch int, th mygograph.Theme) ui.Color {
	if ch >= 0 && ch < len(mc.muted) && mc.muted[ch] {
		c := th.BadChannelColor
		c.A = 255
		return c
	}
	if ch == mc.selected {
		c := th.AccentColor
		c.A = 255
		return c
	}
	c := th.SeriesColor(ch)
	c.A = 255
	return c
}

// PrepareShaderFrame 执行多导联窗口降采样、可选去直流基线及 16-bit RGBA 包络纹理原地编码（供 Draw 与 Gate 3 零分配基准压测共用）。
func (mc *MultiChannelItem) PrepareShaderFrame(tStart, tEnd float64, buckets int, th mygograph.Theme) (sweepCursorNorm float64) {
	if mc.ring == nil || buckets <= 0 {
		return -1
	}
	nCh := mc.ring.Channels()
	mc.ensureBuffers(nCh, buckets, th)

	normScale := float32(mc.gain / (max(mc.lanePeakToPeak, 1e-30) * defaultMaxLaneAmp))
	quantMul := normScale * 32767.0

	return mc.ring.PackShaderEnvelope(
		tStart, tEnd,
		mc.mode == DisplaySweepMode,
		mc.windowSec, mc.eraseGapSec,
		buckets,
		mc.dcRemoval,
		quantMul,
		mc.envRGBA.Pix,
		mc.envRGBA.Stride,
		mc.spanSlot0[:buckets],
		mc.spanLen[:buckets],
		mc.minScratch[:buckets],
		mc.maxScratch[:buckets],
		mc.validScratch[:buckets],
	)
}

func (mc *MultiChannelItem) Draw(rc *mygograph.RenderContext) {
	if !mc.visible || mc.ring == nil || mc.ring.Channels() == 0 || rc.PlotRect.W < 2 || rc.PlotRect.H < 2 {
		return
	}
	nCh := mc.ring.Channels()
	y0, y1 := rc.View.YRange()
	visChLo := max(int(math.Floor(y0))-int(mc.overlapRadius), 0)
	visChHi := min(int(math.Ceil(y1))+int(mc.overlapRadius), nCh-1)
	if visChLo > visChHi {
		return
	}
	visCount := visChHi - visChLo + 1

	useShader := mc.tier == TierShader || (mc.tier == TierAuto && visCount > defaultVectorThreshold)
	buckets := max(min(int(math.Ceil(float64(rc.PlotRect.W))), 4096), 16)
	x0, x1 := rc.View.XRange()
	if mc.mode == DisplaySweepMode {
		x0, x1 = 0, mc.windowSec
	}

	if useShader {
		sweepNorm := mc.PrepareShaderFrame(x0, x1, buckets, rc.Theme)
		if mc.envBmp == nil {
			mc.envBmp = ui.NewBitmap(mc.envRGBA).Pixelated()
		} else {
			mc.envBmp.Update(mc.envRGBA)
		}

		flags := int32(0)
		if rc.View.InvertY() {
			flags |= shaders.MultiStackFlagInvertY
		}
		if mc.showZeroLines {
			flags |= shaders.MultiStackFlagShowZeroLine
		}
		mc.shaderSt.Draw(
			rc.Painter,
			rc.PlotRect,
			mc.envBmp,
			mc.palBmp,
			int32(nCh),
			float32(y0),
			float32(max(y1-y0, 1e-6)),
			float32(defaultMaxLaneAmp),
			mc.lineWidth*0.5,
			mc.overlapRadius,
			flags,
		)
		if sweepNorm >= 0 {
			mc.drawSweepBar(rc, sweepNorm)
		}
		return
	}

	// <= 32 可见导联（或双击独奏放大）：高精度矢量折线快路径（100% 不透明颜色命中 GPU OpFill）
	need := visCount * buckets
	if cap(mc.envBuf) < need {
		mc.envBuf = make([]data.MinMax, need)
	} else {
		mc.envBuf = mc.envBuf[:need]
	}

	sweepNorm := -1.0
	if mc.mode == DisplaySweepMode {
		sweepNorm, _ = mc.ring.DecimateChannelsSweep(visChLo, visChHi+1, mc.windowSec, mc.eraseGapSec, buckets, mc.envBuf)
	} else {
		mc.ring.DecimateChannelsWindow(visChLo, visChHi+1, x0, x1, buckets, mc.envBuf)
	}

	unitScale := mc.gain / max(mc.lanePeakToPeak, 1e-30)
	vt := rc.Transform

	for i := range visCount {
		ch := visChLo + i
		chEnv := mc.envBuf[i*buckets : (i+1)*buckets]
		var dcMean float64
		if mc.dcRemoval {
			var sum float64
			var cnt int
			for b := range buckets {
				if chEnv[b].Valid {
					sum += float64(chEnv[b].Min+chEnv[b].Max) * 0.5
					cnt++
				}
			}
			if cnt > 0 {
				dcMean = sum / float64(cnt)
			}
		}

		mc.polyBuf = mc.polyBuf[:0]
		baseY := float64(ch)
		col := mc.channelColor(ch, rc.Theme)
		w := mc.lineWidth
		if ch == mc.selected {
			w += 0.45
		}

		flushSeg := func() {
			if len(mc.polyBuf) >= 2 {
				rc.Painter.StrokePolyline(mc.polyBuf, w, col)
			} else if len(mc.polyBuf) == 1 {
				pt := mc.polyBuf[0]
				rc.Painter.Fill(ui.Rect{X: pt[0] - 0.8, Y: pt[1] - 0.8, W: 1.6, H: 1.6}, col, 0)
			}
			mc.polyBuf = mc.polyBuf[:0]
		}

		for b := range buckets {
			mm := chEnv[b]
			if !mm.Valid {
				flushSeg()
				continue
			}
			px := rc.PlotRect.X + (float32(b)+0.5)/float32(buckets)*rc.PlotRect.W
			yFirst := vt.DataToScreenY(baseY + (float64(mm.First)-dcMean)*unitScale)
			yMin := vt.DataToScreenY(baseY + (float64(mm.Min)-dcMean)*unitScale)
			yMax := vt.DataToScreenY(baseY + (float64(mm.Max)-dcMean)*unitScale)
			yLast := vt.DataToScreenY(baseY + (float64(mm.Last)-dcMean)*unitScale)

			if math.Abs(float64(yMax-yMin)) < 0.35 {
				mc.polyBuf = append(mc.polyBuf, [2]float32{px, yFirst})
			} else if mm.MinFirst {
				mc.polyBuf = append(mc.polyBuf,
					[2]float32{px, yFirst},
					[2]float32{px, yMin},
					[2]float32{px, yMax},
					[2]float32{px, yLast},
				)
			} else {
				mc.polyBuf = append(mc.polyBuf,
					[2]float32{px, yFirst},
					[2]float32{px, yMax},
					[2]float32{px, yMin},
					[2]float32{px, yLast},
				)
			}
		}
		flushSeg()
	}

	if sweepNorm >= 0 {
		mc.drawSweepBar(rc, sweepNorm)
	}
}

func (mc *MultiChannelItem) drawSweepBar(rc *mygograph.RenderContext, sweepNorm float64) {
	sx := rc.PlotRect.X + float32(sweepNorm)*rc.PlotRect.W
	rc.Painter.Line(sx, rc.PlotRect.Y, sx, rc.PlotRect.Y+rc.PlotRect.H, 1.5, rc.Theme.SweepBarLine)
}
