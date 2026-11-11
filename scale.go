package mygograph

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// Tick 表示坐标轴上的单个主刻度或次刻度。
type Tick struct {
	Value float64 // 物理数据域坐标值（对数轴同样存储正物理值，由 ViewportTransform 统一取 log10）
	Label string  // 刻度文本（次刻度或无需显示文本时为空字符串）
	Major bool    // true 表示主刻度（绘制主网格线与长刻度线）
}

// Scale 定义数学比例尺与刻度生成协议。
type Scale interface {
	// IsLog 返回该比例尺是否为以 10 为底的对数比例尺。
	IsLog() bool
	// AppendTicks 生成可见范围 [minVal, maxVal] 内的刻度序列并追加至 dst（复用切片容量）。
	// 返回生成的刻度切片与坐标轴标题单位后缀（例如 "μV"、"MHz"，无单位时返回 ""）。
	AppendTicks(minVal, maxVal float64, lengthDIP float32, unit string, dst []Tick) (ticks []Tick, unitLabel string)
	// FormatValue 为十字准星与测量游标格式化高精度物理读数。
	FormatValue(val, span float64, unit string) string
}

// SIPrefix 描述单个国际单位制（SI）工程词头。
type SIPrefix struct {
	Exp    int     // 10 的幂指数（3 的整数倍：-15 .. +15）
	Symbol string  // 词头符号（如 "f", "p", "n", "μ", "m", "", "k", "M", "G", "T", "P"）
	Scale  float64 // 数值乘数 = 10^(-Exp)，即 scaled = raw * Scale
}

var siPrefixes = [...]SIPrefix{
	{Exp: -15, Symbol: "f", Scale: 1e15},
	{Exp: -12, Symbol: "p", Scale: 1e12},
	{Exp: -9, Symbol: "n", Scale: 1e9},
	{Exp: -6, Symbol: "μ", Scale: 1e6},
	{Exp: -3, Symbol: "m", Scale: 1e3},
	{Exp: 0, Symbol: "", Scale: 1.0},
	{Exp: 3, Symbol: "k", Scale: 1e-3},
	{Exp: 6, Symbol: "M", Scale: 1e-6},
	{Exp: 9, Symbol: "G", Scale: 1e-9},
	{Exp: 12, Symbol: "T", Scale: 1e-12},
	{Exp: 15, Symbol: "P", Scale: 1e-15},
}

// SelectSIPrefix 根据参考幅值（通常取视口最大绝对值或跨度）自动选取最合适的工程 SI 词头。
func SelectSIPrefix(refAbs float64) SIPrefix {
	if !(refAbs > 0) || math.IsInf(refAbs, 0) {
		return siPrefixes[5] // Exp: 0
	}
	exp3 := int(math.Floor(math.Log10(refAbs)/3.0)) * 3
	exp3 = min(max(exp3, -15), 15)
	idx := (exp3 + 15) / 3
	return siPrefixes[idx]
}

// FormatSIValue 将单个物理量自动换算为带 SI 工程词头的紧凑字符串（如 0.00025, "V" -> "250 μV"）。
func FormatSIValue(val float64, precision int, unit string) string {
	if math.IsNaN(val) {
		return "NaN"
	}
	if math.IsInf(val, 1) {
		return "+Inf"
	}
	if math.IsInf(val, -1) {
		return "-Inf"
	}
	if unit == "" {
		return formatCleanFloat(val, precision)
	}
	if val == 0 {
		return "0 " + unit
	}
	pfx := SelectSIPrefix(math.Abs(val))
	scaled := val * pfx.Scale
	var buf [64]byte
	b := appendCleanFloat(buf[:0], scaled, max(precision, 0))
	b = append(b, ' ')
	b = append(b, pfx.Symbol...)
	b = append(b, unit...)
	return string(b)
}

// LinearScale 标准笛卡尔线性比例尺（支持 SI 工程词头自动换算与高直流偏置微弱纹波基数剥离）。
type LinearScale struct {
	// UseSI 为 true 且 Axis 配置了物理单位（如 "V", "Hz", "s", "A"）时，自动按视口量级换算工程词头。
	UseSI bool
	// TargetSpacingDIP 期望的主刻度最小屏幕间距（默认 72 DIP）。
	TargetSpacingDIP float32
}

// NewLinearScale 创建默认开启 SI 工程单位换算的线性比例尺。
func NewLinearScale() *LinearScale {
	return &LinearScale{UseSI: true, TargetSpacingDIP: 72}
}

func (s *LinearScale) IsLog() bool { return false }

func (s *LinearScale) AppendTicks(minVal, maxVal float64, lengthDIP float32, unit string, dst []Tick) ([]Tick, string) {
	out := dst[:0]
	lo, hi := min(minVal, maxVal), max(minVal, maxVal)
	span := hi - lo
	if !(span > 0) || math.IsInf(span, 0) || lengthDIP <= 8 {
		return out, unit
	}

	spacing := s.TargetSpacingDIP
	if spacing <= 16 {
		spacing = 72
	}
	maxMajor := max(int(lengthDIP/spacing), 2)
	majorStep, minorDiv := niceLinearStep(span / float64(maxMajor))
	if !(majorStep > 0) {
		return out, unit
	}

	// 检测高直流偏置下的微弱纹波场景（如 2.500000 V ± 10 μV）：
	// 当 |均值| / span >= 1e4 时自动启用 PyQtGraph 风格基数偏移标注（如 "@ +2.5 V"，刻度仅显示相对纹波 μV）！
	absMax := max(math.Abs(lo), math.Abs(hi))
	useOffset := absMax > 0 && (absMax/span) >= 1e4
	baseOffset := 0.0
	if useOffset {
		// 选取对齐在较大整数量级上的干净参考基数，使相对刻度围绕 0 对称或紧凑可读
		offsetQuantum := math.Pow(10, math.Floor(math.Log10(span))+2)
		baseOffset = math.Round((0.5*(lo+hi))/offsetQuantum) * offsetQuantum
	}

	pfx := siPrefixes[5] // Scale = 1.0
	if s.UseSI && unit != "" {
		ref := absMax
		if useOffset {
			ref = max(math.Abs(lo-baseOffset), math.Abs(hi-baseOffset))
			if ref == 0 {
				ref = span
			}
		}
		if ref == 0 {
			ref = span
		}
		pfx = SelectSIPrefix(ref)
	}

	unitSuffix := ""
	if unit != "" {
		unitSuffix = pfx.Symbol + unit
	}
	if useOffset {
		offStr := FormatSIValue(baseOffset, 6, unit)
		if baseOffset >= 0 {
			offStr = "+" + offStr
		}
		if unitSuffix != "" {
			unitSuffix = fmt.Sprintf("%s @ %s", unitSuffix, offStr)
		} else {
			unitSuffix = "@ " + offStr
		}
	}

	scaledStep := majorStep * pfx.Scale
	decimals := decimalsForStep(scaledStep)

	// 先生成次刻度，再生成主刻度（支持在 baseOffset 相对坐标系下保持精确网格）
	relLo := lo - baseOffset
	relHi := hi - baseOffset
	firstMajorIdx := int(math.Ceil(relLo/majorStep - 1e-9))
	lastMajorIdx := int(math.Floor(relHi/majorStep + 1e-9))
	if lastMajorIdx-firstMajorIdx > 512 {
		return out, unitSuffix
	}

	if minorDiv > 1 {
		minorStep := majorStep / float64(minorDiv)
		for mIdx := firstMajorIdx - 1; mIdx <= lastMajorIdx; mIdx++ {
			baseRel := float64(mIdx) * majorStep
			for sub := 1; sub < minorDiv; sub++ {
				rv := baseRel + float64(sub)*minorStep
				v := baseOffset + rv
				if v >= lo && v <= hi {
					out = append(out, Tick{Value: v, Major: false})
				}
			}
		}
	}

	var numBuf [64]byte
	for mIdx := firstMajorIdx; mIdx <= lastMajorIdx; mIdx++ {
		rv := float64(mIdx) * majorStep
		if math.Abs(rv) < majorStep*1e-12 {
			rv = 0
		}
		v := baseOffset + rv
		if v < lo-majorStep*1e-9 || v > hi+majorStep*1e-9 {
			continue
		}
		scaledVal := rv * pfx.Scale
		if math.Abs(scaledVal) < scaledStep*1e-12 {
			scaledVal = 0
		}
		lbl := string(strconv.AppendFloat(numBuf[:0], scaledVal, 'f', decimals, 64))
		out = append(out, Tick{
			Value: v,
			Label: lbl,
			Major: true,
		})
	}

	return out, unitSuffix
}

func (s *LinearScale) FormatValue(val, span float64, unit string) string {
	if !isFinite64(val) {
		return FormatSIValue(val, 4, unit)
	}
	if s.UseSI && unit != "" {
		ref := max(math.Abs(val), math.Abs(span))
		pfx := SelectSIPrefix(ref)
		stepHint := math.Abs(span) * 1e-3 * pfx.Scale
		dec := 3
		if stepHint > 0 {
			dec = min(max(decimalsForStep(stepHint), 2), 6)
		}
		var buf [64]byte
		b := strconv.AppendFloat(buf[:0], val*pfx.Scale, 'f', dec, 64)
		b = append(b, ' ')
		b = append(b, pfx.Symbol...)
		b = append(b, unit...)
		return string(b)
	}
	dec := 4
	if span > 0 && isFinite64(span) {
		dec = min(max(decimalsForStep(span*1e-3), 0), 9)
	}
	var buf [64]byte
	b := strconv.AppendFloat(buf[:0], val, 'f', dec, 64)
	if unit != "" {
		b = append(b, ' ')
		b = append(b, unit...)
	}
	return string(b)
}

// LogScale 以 10 为底的工程对数比例尺（适用于频谱分析、Bode 图与高动态范围物理量）。
type LogScale struct {
	UseSI bool
}

// NewLogScale 创建以 10 为底的对数比例尺。
func NewLogScale() *LogScale {
	return &LogScale{UseSI: true}
}

func (s *LogScale) IsLog() bool { return true }

func (s *LogScale) AppendTicks(minVal, maxVal float64, lengthDIP float32, unit string, dst []Tick) ([]Tick, string) {
	out := dst[:0]
	lo, hi := min(minVal, maxVal), max(minVal, maxVal)
	if hi <= 0 || lengthDIP <= 8 {
		return out, unit
	}
	if lo <= 0 {
		lo = max(hi*1e-12, 1e-300)
	}

	logLo := math.Log10(lo)
	logHi := math.Log10(hi)
	decades := logHi - logLo
	if !(decades > 0) || math.IsInf(decades, 0) {
		return out, unit
	}

	// 当对数跨度极窄（< 0.8 个数量级）时退化为线性加密刻度但保持物理正值
	if decades < 0.8 {
		lin := LinearScale{UseSI: s.UseSI, TargetSpacingDIP: 72}
		return lin.AppendTicks(lo, hi, lengthDIP, unit, out)
	}

	// 根据像素密度决定隔几个数量级输出一个主刻度（大跨度如 1e-12..1e12 自动按 2 或 3 倍十进位抽稀）
	pxPerDecade := float64(lengthDIP) / decades
	decadeStride := 1
	if pxPerDecade < 38 {
		decadeStride = max(int(math.Ceil(38.0/pxPerDecade)), 1)
	}

	dStart := int(math.Floor(logLo))
	dEnd := int(math.Ceil(logHi))
	labelSub := decadeStride == 1 && pxPerDecade >= 130 // 放大时额外为 2×10^k 与 5×10^k 显示数值标签

	for d := dStart; d <= dEnd; d++ {
		base := math.Pow(10, float64(d))
		if !isFinite64(base) || base <= 0 {
			continue
		}
		isMajorDecade := modPositive(d, decadeStride) == 0
		if base >= lo*(1-1e-12) && base <= hi*(1+1e-12) {
			lbl := ""
			if isMajorDecade {
				lbl = formatLogTick(base, d, s.UseSI, unit)
			}
			out = append(out, Tick{Value: base, Label: lbl, Major: isMajorDecade})
		}

		// 在每个数量级内生成 2..9 子倍频次刻度
		if decadeStride == 1 && pxPerDecade >= 28 {
			for mult := 2; mult <= 9; mult++ {
				v := base * float64(mult)
				if v < lo || v > hi {
					continue
				}
				lbl := ""
				if labelSub && (mult == 2 || mult == 5) {
					lbl = formatLogTick(v, d, s.UseSI, unit)
				}
				out = append(out, Tick{Value: v, Label: lbl, Major: lbl != ""})
			}
		}
	}

	// 对数轴每个刻度自带量级后缀（如 10 Hz, 1 kHz, 1 MHz），轴标题保持基础单位名
	return out, unit
}

func (s *LogScale) FormatValue(val, _ float64, unit string) string {
	return FormatSIValue(val, 3, unit)
}

// TimeScale 多尺度自适应时间比例尺，自动无缝跨越 ns -> μs -> ms -> s -> HH:MM:SS -> YYYY-MM-DD。
//
// 当坐标值 >= 1e8（约 1973-03-03 UTC 之后）时自动识别为 Unix 秒级纪元时间戳，
// 否则作为从 t=0 起算的相对流逝时间（Elapsed Seconds）格式化。
type TimeScale struct {
	// Location 指定日历时间显示时区（nil 时默认使用 time.Local）。
	Location *time.Location
	// TargetSpacingDIP 期望主刻度间距（默认 88 DIP，留足时间戳宽度）。
	TargetSpacingDIP float32
}

// NewTimeScale 创建自适应多尺度时间比例尺。
func NewTimeScale() *TimeScale {
	return &TimeScale{Location: time.Local, TargetSpacingDIP: 88}
}

func (s *TimeScale) IsLog() bool { return false }

const unixEpochThreshold = 1e8

func (s *TimeScale) AppendTicks(minVal, maxVal float64, lengthDIP float32, _ string, dst []Tick) ([]Tick, string) {
	out := dst[:0]
	lo, hi := min(minVal, maxVal), max(minVal, maxVal)
	span := hi - lo
	if !(span > 0) || math.IsInf(span, 0) || lengthDIP <= 12 {
		return out, ""
	}

	isEpoch := lo >= unixEpochThreshold
	loc := s.Location
	if loc == nil {
		loc = time.Local
	}

	// 亚秒级至数十秒窗口：利用高精度十进制 SI 秒（ns / μs / ms / s）加上参考时刻后缀
	if span < 60.0 {
		lin := LinearScale{UseSI: true, TargetSpacingDIP: max(s.TargetSpacingDIP, 80)}
		if !isEpoch {
			return lin.AppendTicks(lo, hi, lengthDIP, "s", out)
		}
		// Unix 时间戳亚分钟视口：剥离整秒纪元基数，在轴标题显示精确 UTC/本地基准时刻 "HH:MM:SS"
		anchorSec := math.Floor(lo)
		relTicks, relUnit := lin.AppendTicks(lo-anchorSec, hi-anchorSec, lengthDIP, "s", out[:0])
		for i := range relTicks {
			relTicks[i].Value += anchorSec
		}
		anchorStr := time.Unix(int64(anchorSec), 0).In(loc).Format("2006-01-02 15:04:05")
		if relUnit != "" {
			return relTicks, fmt.Sprintf("+%s (%s)", relUnit, anchorStr)
		}
		return relTicks, anchorStr
	}

	spacing := s.TargetSpacingDIP
	if spacing <= 24 {
		spacing = 88
	}
	maxMajor := max(int(lengthDIP/spacing), 2)
	stepSec, minorDiv := niceTimeStepSeconds(span / float64(maxMajor))

	firstIdx := int64(math.Ceil(lo/stepSec - 1e-9))
	lastIdx := int64(math.Floor(hi/stepSec + 1e-9))
	if lastIdx-firstIdx > 512 {
		return out, ""
	}

	if minorDiv > 1 {
		minorStep := stepSec / float64(minorDiv)
		for idx := firstIdx - 1; idx <= lastIdx; idx++ {
			base := float64(idx) * stepSec
			for sub := 1; sub < minorDiv; sub++ {
				v := base + float64(sub)*minorStep
				if v >= lo && v <= hi {
					out = append(out, Tick{Value: v, Major: false})
				}
			}
		}
	}

	unitHeader := ""
	if isEpoch && span < 86400*2 {
		unitHeader = time.Unix(int64(lo), 0).In(loc).Format("2006-01-02")
	}

	for idx := firstIdx; idx <= lastIdx; idx++ {
		v := float64(idx) * stepSec
		if v < lo-stepSec*1e-9 || v > hi+stepSec*1e-9 {
			continue
		}
		var lbl string
		if isEpoch {
			tm := floatToTime(v, loc)
			switch {
			case stepSec >= 86400:
				lbl = tm.Format("2006-01-02")
			case stepSec >= 60:
				lbl = tm.Format("15:04")
			default:
				lbl = tm.Format("15:04:05")
			}
		} else {
			lbl = formatElapsedHMS(v, stepSec)
		}
		out = append(out, Tick{Value: v, Label: lbl, Major: true})
	}

	return out, unitHeader
}

func (s *TimeScale) FormatValue(val, span float64, _ string) string {
	if !isFinite64(val) {
		return "NaN"
	}
	if val >= unixEpochThreshold {
		loc := s.Location
		if loc == nil {
			loc = time.Local
		}
		sec, frac := math.Modf(val)
		tm := time.Unix(int64(sec), int64(math.Round(frac*1e9))).In(loc)
		switch {
		case span < 1e-3:
			return tm.Format("15:04:05.000000000")
		case span < 1.0:
			return tm.Format("15:04:05.000000")
		case span < 60.0:
			return tm.Format("15:04:05.000")
		default:
			return tm.Format("2006-01-02 15:04:05")
		}
	}
	if math.Abs(val) < 60.0 && span < 60.0 {
		return FormatSIValue(val, 4, "s")
	}
	return formatElapsedHMS(val, max(span*1e-3, 1e-3))
}

func niceLinearStep(rawStep float64) (step float64, minorDiv int) {
	if !(rawStep > 0) || math.IsInf(rawStep, 0) {
		return 0, 1
	}
	exp := math.Floor(math.Log10(rawStep))
	pow10 := math.Pow(10, exp)
	frac := rawStep / pow10
	switch {
	case frac <= 1.0:
		return 1.0 * pow10, 5
	case frac <= 2.0:
		return 2.0 * pow10, 4
	case frac <= 2.5:
		return 2.5 * pow10, 5
	case frac <= 5.0:
		return 5.0 * pow10, 5
	default:
		return 10.0 * pow10, 5
	}
}

var timeStepsSec = [...]struct {
	sec      float64
	minorDiv int
}{
	{1, 5},
	{2, 4},
	{5, 5},
	{10, 5},
	{15, 3},
	{30, 6},
	{60, 6},         // 1 min
	{120, 4},        // 2 min
	{300, 5},        // 5 min
	{600, 5},        // 10 min
	{900, 3},        // 15 min
	{1800, 6},       // 30 min
	{3600, 6},       // 1 h
	{7200, 4},       // 2 h
	{14400, 4},      // 4 h
	{21600, 6},      // 6 h
	{43200, 6},      // 12 h
	{86400, 4},      // 1 d
	{86400 * 2, 4},  // 2 d
	{86400 * 7, 7},  // 7 d
	{86400 * 14, 7}, // 14 d
	{86400 * 30, 6}, // 30 d
}

func niceTimeStepSeconds(rawSec float64) (float64, int) {
	for _, ts := range timeStepsSec {
		if rawSec <= ts.sec {
			return ts.sec, ts.minorDiv
		}
	}
	dayStep, div := niceLinearStep(rawSec / 86400.0)
	return dayStep * 86400.0, div
}

func floatToTime(sec float64, loc *time.Location) time.Time {
	iSec, frac := math.Modf(sec)
	return time.Unix(int64(iSec), int64(math.Round(frac*1e9))).In(loc)
}

func formatElapsedHMS(sec, stepSec float64) string {
	neg := sec < 0
	absSec := math.Abs(sec)
	totalSec := int64(math.Floor(absSec + 1e-9))
	hours := totalSec / 3600
	mins := (totalSec % 3600) / 60
	secs := totalSec % 60
	sign := ""
	if neg {
		sign = "-"
	}
	if stepSec < 1.0 {
		frac := absSec - float64(totalSec)
		ms := int(math.Round(frac * 1000))
		if ms >= 1000 {
			ms = 0
			secs++
		}
		if hours > 0 {
			return fmt.Sprintf("%s%02d:%02d:%02d.%03d", sign, hours, mins, secs, ms)
		}
		return fmt.Sprintf("%s%02d:%02d.%03d", sign, mins, secs, ms)
	}
	if hours > 0 {
		return fmt.Sprintf("%s%02d:%02d:%02d", sign, hours, mins, secs)
	}
	return fmt.Sprintf("%s%02d:%02d", sign, mins, secs)
}

func formatLogTick(val float64, decade int, useSI bool, unit string) string {
	if useSI && unit != "" && decade >= -15 && decade <= 15 {
		pfx := SelectSIPrefix(val)
		scaled := val * pfx.Scale
		var buf [48]byte
		b := appendCleanFloat(buf[:0], scaled, 2)
		if pfx.Symbol != "" || unit != "" {
			b = append(b, ' ')
			b = append(b, pfx.Symbol...)
			b = append(b, unit...)
		}
		return string(b)
	}
	if decade >= -3 && decade <= 4 {
		return formatCleanFloat(val, max(-decade, 0))
	}
	// 使用清晰紧凑的十的幂上标字符表示法（如 10⁻⁶, 10⁶）
	lead := int(math.Round(val / math.Pow(10, float64(decade))))
	expStr := formatSuperscriptInt(decade)
	if lead <= 1 {
		return "10" + expStr
	}
	return strconv.Itoa(lead) + "×10" + expStr
}

var superscriptDigits = [10]string{"⁰", "¹", "²", "³", "⁴", "⁵", "⁶", "⁷", "⁸", "⁹"}

func formatSuperscriptInt(n int) string {
	if n == 0 {
		return "⁰"
	}
	var buf [32]byte
	b := buf[:0]
	if n < 0 {
		b = append(b, "⁻"...)
		n = -n
	}
	var digits [20]int
	cnt := 0
	for n > 0 {
		digits[cnt] = n % 10
		n /= 10
		cnt++
	}
	for i := cnt - 1; i >= 0; i-- {
		b = append(b, superscriptDigits[digits[i]]...)
	}
	return string(b)
}

func decimalsForStep(step float64) int {
	if !(step > 0) || math.IsInf(step, 0) {
		return 0
	}
	// 考虑 2.5 × 10^k 尾数需要多保留 1 位小数
	logStep := math.Log10(step)
	dec := int(math.Ceil(-logStep - 1e-9))
	rem := step * math.Pow(10, float64(max(dec, 0)))
	if math.Abs(rem-math.Round(rem)) > 1e-4 {
		dec++
	}
	return min(max(dec, 0), 9)
}

func formatCleanFloat(val float64, maxDecimals int) string {
	var buf [64]byte
	return string(appendCleanFloat(buf[:0], val, maxDecimals))
}

func appendCleanFloat(dst []byte, val float64, maxDecimals int) []byte {
	b := strconv.AppendFloat(dst, val, 'f', maxDecimals, 64)
	if maxDecimals <= 0 {
		return b
	}
	// 去除末尾多余的 0 与孤立小数点
	dotIdx := -1
	for i := len(dst); i < len(b); i++ {
		if b[i] == '.' {
			dotIdx = i
			break
		}
	}
	if dotIdx < 0 {
		return b
	}
	end := len(b)
	for end > dotIdx+1 && b[end-1] == '0' {
		end--
	}
	if end == dotIdx+1 && b[end-1] == '0' {
		end = dotIdx
	} else if end == dotIdx+1 {
		// 形如 "1.5" 保留原样
	}
	if end > 0 && b[end-1] == '.' {
		end--
	}
	return b[:end]
}

func modPositive(a, m int) int {
	if m <= 0 {
		return 0
	}
	r := a % m
	if r < 0 {
		r += m
	}
	return r
}

func isFinite64(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
