package items

import (
	"fmt"
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

// WithScaleBar 控制是否在绘图区右下角绘制临床/科研 L 型物理比例标尺（如 100 μV × 1 s，默认开启）。
func WithScaleBar(enable bool) MultiChannelOption {
	return func(mc *MultiChannelItem) { mc.showScaleBar = enable }
}

// WithAmplitudeUnit 设置物理幅值量纲单位（默认 "V"，配合 SI 词头自动显示为 μV / mV / V）。
func WithAmplitudeUnit(unit string) MultiChannelOption {
	return func(mc *MultiChannelItem) { mc.ampUnit = unit }
}

// WithZeroLines 控制是否显示各导联水平零电位基线参考线。
func WithZeroLines(enable bool) MultiChannelOption {
	return func(mc *MultiChannelItem) { mc.showZeroLines = enable }
}

// MultiChannelItem 是专为 64~256 导联高密度脑电（EEG/iEEG）、心电（ECG）、肌电（EMG）、工业振动阵列与逻辑分析仪设计的双模实时波形引擎。
//
// 核心架构（DESIGN.md 第 6 节）：
//   - 各导联 ch (0..C-1) 纵向中心对齐在物理坐标 Y = float64(ch)，与 NewChannelAxis 无缝联动；
//   - 可见导联 <= 32（或双击单导联独奏放大）时采用不透明 StrokePolyline 矢量路径；
//   - 可见导联 > 32 时自动切换为 multistack.kage 单次 DrawCall SDF 抗锯齿着色器路径，消除上千次路径提交瓶颈；
//   - 支持 Alt+滚轮全局无级增益、单导联独立倍率（EEG+ECG/EOG 混采）、坏道静音、双击独奏、L 型物理标尺及原地右键参数菜单。
type MultiChannelItem struct {
	ring    *data.MultiRingBuffer
	visible bool
	zVal    float32

	mode           MultiDisplayMode
	tier           MultiRenderTier
	windowSec      float64
	eraseGapSec    float64
	lanePeakToPeak float64
	gain           float64 // 叠加在 lanePeakToPeak 上的全局无级增益倍率（Alt+滚轮调节）
	dcRemoval      bool
	showZeroLines  bool
	showScaleBar   bool
	ampUnit        string
	lineWidth      float32
	overlapRadius  int32

	muted           []bool
	chGains         []float32 // 单导联独立倍率（如混采时将 mV 级 ECG 设为 0.1x 与 μV 级 EEG 同屏显示）
	hasCustomChGain bool
	selected        int
	soloCh          int

	// 进入 Solo 前的 Y 轴视口快照（独立于 ViewBox 缩放历史栈，避免与框选缩放互相干扰，且还原时不触碰 X 轴实时跟随状态）
	preSoloY0, preSoloY1 float64
	preSoloAutoY         bool
	hasPreSolo           bool

	// L 型物理标尺零分配文本缓存
	lastCalAmpVal  float64
	lastCalTimeVal float64
	lastCalUnit    string
	lastCalAmpStr  string
	lastCalTimeStr string

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
	chGains := make([]float32, nCh)
	for i := range chGains {
		chGains[i] = 1.0
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
		showScaleBar:   true,
		ampUnit:        "V",
		lineWidth:      1.25,
		overlapRadius:  2,
		muted:          make([]bool, nCh),
		chGains:        chGains,
		dcMeans:        make([]float32, nCh),
		selected:       -1,
		soloCh:         -1,
		lastCalAmpVal:  math.NaN(),
		lastCalTimeVal: math.NaN(),
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

// SetEffectiveSensitivity 直接按临床物理量程档位设定全局灵敏度（如 100e-6 表示 100 μV/ch，1e-3 表示 1 mV/ch）。
func (mc *MultiChannelItem) SetEffectiveSensitivity(peakToPeak float64) *MultiChannelItem {
	if peakToPeak > 0 {
		mc.SetGain(mc.lanePeakToPeak / peakToPeak)
	}
	return mc
}

// WindowSeconds 返回当前实时时基窗口宽度（秒）。
func (mc *MultiChannelItem) WindowSeconds() float64 { return mc.windowSec }

// SetWindowSeconds 动态切换走纸/扫描时基窗口宽度（如 1s / 5s / 10s / 30s）。
func (mc *MultiChannelItem) SetWindowSeconds(sec float64) *MultiChannelItem {
	if sec > 0 {
		mc.windowSec = sec
	}
	return mc
}

// ShowScaleBar 返回是否显示右下角临床 L 型物理比例标尺。
func (mc *MultiChannelItem) ShowScaleBar() bool { return mc.showScaleBar }

// SetShowScaleBar 控制右下角临床 L 型物理比例标尺显隐。
func (mc *MultiChannelItem) SetShowScaleBar(show bool) *MultiChannelItem {
	mc.showScaleBar = show
	return mc
}

// ShowZeroLines 返回是否显示各导联零电位参考线。
func (mc *MultiChannelItem) ShowZeroLines() bool { return mc.showZeroLines }

// SetShowZeroLines 控制各导联零电位基线显隐。
func (mc *MultiChannelItem) SetShowZeroLines(show bool) *MultiChannelItem {
	mc.showZeroLines = show
	return mc
}

// ChannelGain 返回指定导联相对于全局增益的独立倍率（默认 1.0）。
func (mc *MultiChannelItem) ChannelGain(ch int) float64 {
	if ch >= 0 && ch < len(mc.chGains) {
		return float64(mc.chGains[ch])
	}
	return 1.0
}

// SetChannelGain 设置单导联独立幅值倍率（例如在 ±100μV 脑电视图中将 mV 级 ECG 导联设为 0.1 倍）。
func (mc *MultiChannelItem) SetChannelGain(ch int, factor float64) *MultiChannelItem {
	if ch >= 0 && ch < len(mc.chGains) && !math.IsNaN(factor) {
		f := float32(min(max(factor, 0.001), 1000.0))
		mc.chGains[ch] = f
		mc.recheckCustomChGains()
	}
	return mc
}

// SetChannelSensitivity 按目标单车道峰峰值物理量程单独设定第 ch 导联灵敏度（如对混采 ECG 导联设置 2e-3 即 2 mV/ch）。
func (mc *MultiChannelItem) SetChannelSensitivity(ch int, peakToPeak float64) *MultiChannelItem {
	if peakToPeak > 0 {
		mc.SetChannelGain(ch, mc.lanePeakToPeak/peakToPeak)
	}
	return mc
}

// ResetChannelGains 将所有导联的独立倍率一键恢复为 1.0。
func (mc *MultiChannelItem) ResetChannelGains() {
	for i := range mc.chGains {
		mc.chGains[i] = 1.0
	}
	mc.hasCustomChGain = false
}

func (mc *MultiChannelItem) recheckCustomChGains() {
	for _, g := range mc.chGains {
		if g != 1.0 {
			mc.hasCustomChGain = true
			return
		}
	}
	mc.hasCustomChGain = false
}

// UnmuteAllChannels 一键解除全部坏道静音屏蔽。
func (mc *MultiChannelItem) UnmuteAllChannels() {
	clear(mc.muted)
	mc.palDirty = true
}

// SoloChannel 返回当前处于独奏放大视图的导联索引（-1 表示全导联视图）。
func (mc *MultiChannelItem) SoloChannel() int { return mc.soloCh }

// SetSoloChannel 切换指定导联进入单导联置顶独奏放大（传 -1 恢复全导联视图）。
// 仅改动 Y 轴：进入时快照当前 Y 视口，还原时精确恢复（原为自动量程则重新激活 Y 自动量程），X 轴实时跟随/历史回看状态保持不变。
func (mc *MultiChannelItem) SetSoloChannel(ch int, vb *mygograph.ViewBox) {
	if mc.ring == nil {
		return
	}
	nCh := mc.ring.Channels()
	if ch < 0 || ch >= nCh {
		if mc.soloCh < 0 {
			return
		}
		mc.soloCh = -1
		if vb != nil {
			switch {
			case mc.hasPreSolo && mc.preSoloAutoY:
				vb.EnableAutoRange(mygograph.AxisY)
			case mc.hasPreSolo:
				vb.SetYRange(mc.preSoloY0, mc.preSoloY1, 0)
			default:
				vb.SetYRange(-0.8, float64(nCh-1)+0.8, 0)
			}
		}
		mc.hasPreSolo = false
		return
	}
	if mc.soloCh < 0 && vb != nil {
		mc.preSoloY0, mc.preSoloY1 = vb.YRange()
		mc.preSoloAutoY = vb.IsAutoY()
		mc.hasPreSolo = true
	}
	mc.soloCh = ch
	mc.SetSelectedChannel(ch)
	if vb != nil {
		vb.SetYRange(float64(ch)-0.85, float64(ch)+0.85, 0)
	}
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
	if mc.soloCh >= 0 {
		mc.SetSoloChannel(-1, vb)
		return true
	}
	_, dy := vb.LocalToData(lx, ly, pw, ph)
	ch := int(math.Round(dy))
	if ch < 0 || ch >= mc.ring.Channels() {
		return false
	}
	mc.SetSoloChannel(ch, vb)
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
	if len(mc.chGains) < nCh {
		old := mc.chGains
		mc.chGains = make([]float32, nCh)
		copy(mc.chGains, old)
		for i := len(old); i < nCh; i++ {
			mc.chGains[i] = 1.0
		}
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
	var chScales []float32
	if mc.hasCustomChGain {
		chScales = mc.chGains[:nCh]
	}

	return mc.ring.PackShaderEnvelope(
		tStart, tEnd,
		mc.mode == DisplaySweepMode,
		mc.windowSec, mc.eraseGapSec,
		buckets,
		mc.dcRemoval,
		quantMul,
		chScales,
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
		chScale := unitScale
		if mc.hasCustomChGain && ch >= 0 && ch < len(mc.chGains) {
			chScale *= float64(mc.chGains[ch])
		}
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
			yFirst := vt.DataToScreenY(baseY + (float64(mm.First)-dcMean)*chScale)
			yMin := vt.DataToScreenY(baseY + (float64(mm.Min)-dcMean)*chScale)
			yMax := vt.DataToScreenY(baseY + (float64(mm.Max)-dcMean)*chScale)
			yLast := vt.DataToScreenY(baseY + (float64(mm.Last)-dcMean)*chScale)

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

// DrawOverlay 实现 mygograph.OverlayItem：在绘图区右下角绘制临床规范 L 型幅值-时间物理比例标尺（如 100 μV × 1 s），稳态 0 allocs/op。
func (mc *MultiChannelItem) DrawOverlay(rc *mygograph.RenderContext) {
	if !mc.visible || !mc.showScaleBar || mc.ring == nil || rc.PlotRect.W < 140 || rc.PlotRect.H < 90 {
		return
	}
	pr := rc.PlotRect
	x0, x1 := rc.View.XRange()
	if mc.mode == DisplaySweepMode {
		x0, x1 = 0, mc.windowSec
	}
	y0, y1 := rc.View.YRange()
	xSpan := math.Abs(x1 - x0)
	ySpan := math.Abs(y1 - y0)
	if xSpan <= 1e-12 || ySpan <= 1e-12 {
		return
	}

	// 计算参考导联每物理单位（如 1 V）对应的屏幕像素高度：仅 Solo 独奏时采用该导联独立倍率；
	// 全导联视图一律按全局灵敏度标定（选中导联不参与，避免混采 ECG 的 0.1x 倍率误标到全部 EEG 导联上）
	refChGain := 1.0
	if refCh := mc.soloCh; refCh >= 0 && refCh < len(mc.chGains) {
		refChGain = float64(mc.chGains[refCh])
	}
	lanePx := float64(pr.H) / ySpan
	effSens := mc.lanePeakToPeak / max(mc.gain*refChGain, 1e-12) // 1.0 车道对应的物理峰峰值
	pxPerAmp := lanePx / max(effSens, 1e-30)
	pxPerSec := float64(pr.W) / xSpan

	calTime := niceCalibrationStep(76.0 / pxPerSec)
	calAmp := niceCalibrationStep(46.0 / pxPerAmp)
	barW := float32(min(max(calTime*pxPerSec, 28.0), float64(pr.W)*0.38))
	barH := float32(min(max(calAmp*pxPerAmp, 20.0), float64(pr.H)*0.38))
	// 反向校准实际展示的整数/整档长度；仅当未被钳位时直接取整档值
	actualTime := float64(barW) / pxPerSec
	actualAmp := float64(barH) / pxPerAmp
	if math.Abs(actualTime-calTime)/max(calTime, 1e-30) < 0.02 {
		actualTime = calTime
	}
	if math.Abs(actualAmp-calAmp)/max(calAmp, 1e-30) < 0.02 {
		actualAmp = calAmp
	}

	if actualAmp != mc.lastCalAmpVal || actualTime != mc.lastCalTimeVal || mc.ampUnit != mc.lastCalUnit {
		mc.lastCalAmpVal = actualAmp
		mc.lastCalTimeVal = actualTime
		mc.lastCalUnit = mc.ampUnit
		mc.lastCalAmpStr = mygograph.FormatSIValue(actualAmp, 3, mc.ampUnit)
		mc.lastCalTimeStr = mygograph.FormatSIValue(actualTime, 3, "s")
	}

	drawClinicalScaleBar(rc, pr, barW, barH, mc.lastCalTimeStr, mc.lastCalAmpStr)
}

// BuildContextMenu 实现 mygograph.ContextMenuProvider：在实时多导联波形上右键弹出临床/科研参数调节菜单（定位所指导联、增益、时基、坏道与模式）。
func (mc *MultiChannelItem) BuildContextMenu(m *ui.Menu, lx, ly, plotW, plotH float32, vb *mygograph.ViewBox) {
	if mc.ring == nil {
		return
	}
	nCh := mc.ring.Channels()
	hitCh := -1
	if lx >= 0 && lx <= plotW && ly >= 0 && ly <= plotH && vb != nil {
		_, dy := vb.LocalToData(lx, ly, plotW, plotH)
		ch := int(math.Round(dy))
		if ch >= 0 && ch < nCh {
			hitCh = ch
		}
	}

	// 1. 当前右键指向的具体导联快捷操作（Solo 放大、坏道屏蔽、单导联独立倍率）
	if hitCh >= 0 {
		chName := mc.ring.ChannelName(hitCh)
		if chName == "" {
			chName = fmt.Sprintf("Ch-%03d", hitCh+1)
		}
		m.Submenu("Lead: "+chName, func(sub *ui.Menu) {
			if mc.soloCh == hitCh {
				if sub.Item("Restore All Leads").Chosen() {
					mc.SetSoloChannel(-1, vb)
				}
			} else {
				if sub.Item("Solo Focus " + chName).Chosen() {
					mc.SetSoloChannel(hitCh, vb)
				}
			}
			isMuted := mc.IsChannelMuted(hitCh)
			if sub.Item("Mute Bad Channel").Checked(isMuted).Chosen() {
				mc.SetChannelMuted(hitCh, !isMuted)
			}
			sub.Separator()
			curChG := mc.ChannelGain(hitCh)
			leadGainScales := []struct {
				label string
				mult  float64
			}{
				{"0.1x (mV ECG/EOG Attenuate)", 0.1},
				{"0.2x", 0.2},
				{"0.5x", 0.5},
				{"1.0x (Standard)", 1.0},
				{"2.0x", 2.0},
				{"5.0x (Micro-Signal Boost)", 5.0},
			}
			for _, lg := range leadGainScales {
				active := math.Abs(curChG-lg.mult) < lg.mult*0.05
				if sub.Item("Lead Gain " + lg.label).Radio(active).Chosen() {
					mc.SetChannelGain(hitCh, lg.mult)
				}
			}
			if mc.hasCustomChGain {
				sub.Separator()
				if sub.Item("Reset All Lead Gains (1.0x)").Chosen() {
					mc.ResetChannelGains()
				}
			}
		})
	} else if mc.soloCh >= 0 {
		if m.Item("Restore All Leads").Chosen() {
			mc.SetSoloChannel(-1, vb)
		}
	}

	anyMuted := false
	for _, mu := range mc.muted {
		if mu {
			anyMuted = true
			break
		}
	}
	if anyMuted {
		if m.Item("Unmute All Bad Channels").Chosen() {
			mc.UnmuteAllChannels()
		}
	}

	// 2. 走纸/扫描模式与信号调理子菜单
	m.Submenu("EEG / ECG Display", func(sub *ui.Menu) {
		if sub.Item("Sweep Mode (Erase-Bar)").Radio(mc.mode == DisplaySweepMode).Chosen() {
			mc.SetDisplayMode(DisplaySweepMode)
			if vb != nil {
				// 扫描模式 Bounds 固定为 [0, windowSec] 且 X 轴自动量程零留白，保持 autoX 避免误显 [A]/[Live] 角标
				vb.EnableAutoRange(mygograph.AxisX)
			}
		}
		if sub.Item("Roll Paper Mode (Live Follow)").Radio(mc.mode == DisplayRollMode).Chosen() {
			mc.SetDisplayMode(DisplayRollMode)
			if vb != nil {
				vb.EnableAutoRange(mygograph.AxisX)
			}
		}
		sub.Separator()
		if sub.Item("DC Offset Removal").Checked(mc.dcRemoval).Chosen() {
			mc.SetDCRemoval(!mc.dcRemoval)
		}
		if sub.Item("Channel Zero Baselines").Checked(mc.showZeroLines).Chosen() {
			mc.SetShowZeroLines(!mc.showZeroLines)
		}
		if sub.Item("Calibration Scale Bar (L-Bar)").Checked(mc.showScaleBar).Chosen() {
			mc.SetShowScaleBar(!mc.showScaleBar)
		}
	})

	// 3. 标准临床走纸时基（Timebase Window）档位
	m.Submenu("Timebase (Window)", func(sub *ui.Menu) {
		presets := []struct {
			label string
			sec   float64
		}{
			{"1 s (50 mm/s Fast ECG)", 1.0},
			{"2 s", 2.0},
			{"5 s", 5.0},
			{"10 s (Standard EEG / ECG)", 10.0},
			{"20 s", 20.0},
			{"30 s (PSG Sleep Epoch)", 30.0},
		}
		for _, p := range presets {
			active := math.Abs(mc.windowSec-p.sec) < 0.05
			if sub.Item(p.label).Radio(active).Chosen() {
				mc.SetWindowSeconds(p.sec)
				if vb != nil {
					vb.EnableAutoRange(mygograph.AxisX)
				}
			}
		}
	})

	// 4. 标准临床幅值灵敏度（Sensitivity）档位
	m.Submenu("Sensitivity (Amplitude)", func(sub *ui.Menu) {
		curSens := mc.EffectiveSensitivity()
		sensPresets := []struct {
			label string
			val   float64
		}{
			{"20 μV / ch (High-Gain EEG)", 20e-6},
			{"50 μV / ch", 50e-6},
			{"70 μV / ch (Clinical EEG)", 70e-6},
			{"100 μV / ch", 100e-6},
			{"200 μV / ch (Default)", 200e-6},
			{"500 μV / ch", 500e-6},
			{"1 mV / ch (Standard ECG)", 1e-3},
			{"2 mV / ch (ECG)", 2e-3},
		}
		for _, sp := range sensPresets {
			active := math.Abs(curSens-sp.val) < sp.val*0.06
			if sub.Item(sp.label).Radio(active).Chosen() {
				mc.SetEffectiveSensitivity(sp.val)
			}
		}
		sub.Separator()
		if sub.Item("Amplify Gain (x1.5)").Chosen() {
			mc.SetGain(mc.gain * 1.5)
		}
		if sub.Item("Attenuate Gain (/1.5)").Chosen() {
			mc.SetGain(mc.gain / 1.5)
		}
		if sub.Item("Reset Gain (1.0x)").Chosen() {
			mc.SetGain(1.0)
		}
	})
}

var (
	_ mygograph.OverlayItem         = (*MultiChannelItem)(nil)
	_ mygograph.ContextMenuProvider = (*MultiChannelItem)(nil)
)
