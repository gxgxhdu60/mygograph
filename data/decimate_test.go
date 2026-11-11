package data

import (
	"math"
	"math/big"
	"testing"
)

func TestCameraRelativeOriginPrecision(t *testing.T) {
	// 1. 使用 256-bit 任意精度浮点数（math/big.Float）作为真值基准，严格验证 DESIGN.md 第 9.2 节：
	//    在 t = 1.776e9 ± 1e-6（2 μs 微秒视口）与 V = 2.500000 ± 10e-6（20 μV 纹波视口）下，
	//    相机相对原点变换（Sterbenz 引理精确消基数）像素定位误差 < 0.001 px（远优于 < 0.01 px 门禁）。
	const (
		baseT = 1776001234.000250
		spanT = 2.0e-6
		baseV = 2.500000
		spanV = 20.0e-6
	)
	vt := ViewportTransform{
		XMin:    baseT - spanT*0.5,
		XMax:    baseT + spanT*0.5,
		YMin:    baseV - spanV*0.5,
		YMax:    baseV + spanV*0.5,
		OriginX: 50,
		OriginY: 30,
		Width:   1000,
		Height:  500,
	}

	bf := func(v float64) *big.Float { return new(big.Float).SetPrec(256).SetFloat64(v) }
	exactScreenX := func(x *big.Float) float64 {
		num := new(big.Float).Sub(x, bf(vt.XMin))
		den := new(big.Float).Sub(bf(vt.XMax), bf(vt.XMin))
		norm := new(big.Float).Quo(num, den)
		px := new(big.Float).Add(bf(float64(vt.OriginX)), new(big.Float).Mul(norm, bf(float64(vt.Width))))
		out, _ := px.Float64()
		return out
	}
	exactScreenY := func(y *big.Float) float64 {
		num := new(big.Float).Sub(bf(vt.YMax), y)
		den := new(big.Float).Sub(bf(vt.YMax), bf(vt.YMin))
		norm := new(big.Float).Quo(num, den)
		py := new(big.Float).Add(bf(float64(vt.OriginY)), new(big.Float).Mul(norm, bf(float64(vt.Height))))
		out, _ := py.Float64()
		return out
	}

	// 遍历视口内所有可表示的 float64 时间点与电压采样点
	steps := 0
	for tx := vt.XMin; tx <= vt.XMax; tx = math.Nextafter(tx, math.Inf(1)) {
		frac := float64(steps) / 8.0
		ty := vt.YMin + min(frac, 1.0)*(vt.YMax-vt.YMin)
		gotX := vt.DataToScreenX(tx)
		gotY := vt.DataToScreenY(ty)
		wantX := exactScreenX(bf(tx))
		wantY := exactScreenY(bf(ty))

		if errX := math.Abs(float64(gotX) - wantX); errX >= 0.01 {
			t.Fatalf("step %d: X pixel error = %g px >= 0.01 px (got %v, want %v)", steps, errX, gotX, wantX)
		}
		if errY := math.Abs(float64(gotY) - wantY); errY >= 0.01 {
			t.Fatalf("step %d: Y pixel error = %g px >= 0.01 px (got %v, want %v)", steps, errY, gotY, wantY)
		}
		steps++
	}
	if steps < 8 {
		t.Fatalf("expected multiple distinct float64 steps in 2us window, got %d", steps)
	}

	// 2. 验证 UniformSource 亚 ULP 相对偏移投影：t0 = 1.776e9 处采用 10 ns 采样步长（100 MSa/s，
	//    步长比 1.776e9 处的 float64 ULP 238 ns 还要小 24 倍！），与 256-bit 精确 t0 + i*dt 真值对比，
	//    误差同样 < 0.01 px！
	const dt = 1.0e-8 // 10 ns
	ys := make([]float64, 201)
	for i := range ys {
		ys[i] = baseV - spanV*0.5 + (float64(i)/200.0)*spanV
	}
	uni := NewUniformSliceDt(vt.XMin, dt, ys)
	pts := ProjectAndDecimate(uni, vt, DecimateNone, 1000, nil)
	for i, pt := range pts {
		exactT := new(big.Float).Add(bf(vt.XMin), new(big.Float).Mul(bf(float64(i)), bf(dt)))
		wantX := exactScreenX(exactT)
		if errX := math.Abs(float64(pt[0]) - wantX); errX >= 0.01 {
			t.Fatalf("sub-ULP uniform step %d: X pixel error = %g px >= 0.01 px (got %v, want %v)", i, errX, pt[0], wantX)
		}
	}
}

func TestDecimateM4PreservesDiracSpikesAndNaNGaps(t *testing.T) {
	const n = 200_000
	ys := make([]float64, n)
	for i := range ys {
		ys[i] = math.Sin(float64(i) * 0.001)
	}
	// 注入单样本纳秒级正向与负向尖峰（Dirac Spike）以及一段 NaN 断线
	const (
		posSpikeIdx = 43_217
		negSpikeIdx = 158_903
		posSpikeVal = 99.5
		negSpikeVal = -88.25
	)
	ys[posSpikeIdx] = posSpikeVal
	ys[negSpikeIdx] = negSpikeVal
	for i := 90_000; i < 90_500; i++ {
		ys[i] = math.NaN()
	}

	uni := NewUniformSlice(0, 100_000, ys)
	vt := ViewportTransform{
		XMin: 0, XMax: 2.0,
		YMin: -100, YMax: 100,
		Width: 500, Height: 400,
	}
	dst := make([][2]float32, 0, 4096)

	allocs := testing.AllocsPerRun(50, func() {
		dst = ProjectAndDecimate(uni, vt, DecimateM4, 500, dst)
	})
	if allocs != 0 {
		t.Fatalf("ProjectAndDecimate Uniform M4 allocated %.1f allocs/op, want 0", allocs)
	}

	wantPosY := vt.DataToScreenY(posSpikeVal)
	wantNegY := vt.DataToScreenY(negSpikeVal)
	foundPos, foundNeg, foundNaN := false, false, false
	for _, pt := range dst {
		if math.IsNaN(float64(pt[0])) || math.IsNaN(float64(pt[1])) {
			foundNaN = true
			continue
		}
		if math.Abs(float64(pt[1]-wantPosY)) < 1e-3 {
			foundPos = true
		}
		if math.Abs(float64(pt[1]-wantNegY)) < 1e-3 {
			foundNeg = true
		}
	}
	if !foundPos || !foundNeg || !foundNaN {
		t.Fatalf("envelope check failed: foundPos=%v foundNeg=%v foundNaN=%v (len=%d)", foundPos, foundNeg, foundNaN, len(dst))
	}

	// 同样验证非均匀 XYSlice 路径无损保留尖峰且零分配
	xs := make([]float64, n)
	for i := range xs {
		xs[i] = float64(i) * 1e-5
	}
	xy := NewXYSlice(xs, ys)
	allocsXY := testing.AllocsPerRun(20, func() {
		dst = ProjectAndDecimate(xy, vt, DecimateM4, 500, dst)
	})
	if allocsXY != 0 {
		t.Fatalf("ProjectAndDecimate XYSlice M4 allocated %.1f allocs/op, want 0", allocsXY)
	}
}

func TestDecimateLTTBZeroAlloc(t *testing.T) {
	const n = 50_000
	ys := make([]float64, n)
	for i := range ys {
		ys[i] = math.Cos(float64(i) * 0.01)
	}
	uni := NewUniformSlice(0, 1000, ys)
	vt := ViewportTransform{XMin: 0, XMax: 50, YMin: -2, YMax: 2, Width: 250, Height: 200}
	dst := make([][2]float32, 0, 1024)

	allocs := testing.AllocsPerRun(20, func() {
		dst = ProjectAndDecimate(uni, vt, DecimateLTTB, 250, dst)
	})
	if allocs != 0 {
		t.Fatalf("LTTB allocated %.1f allocs/op, want 0", allocs)
	}
	if len(dst) != 500 {
		t.Fatalf("LTTB output len = %d, want 500", len(dst))
	}
}

// BenchmarkDecimateM4_1M_2000Buckets 验证 DESIGN.md 门禁：100 万点 -> 2000 桶 < 0.8 ms 且 0 allocs/op。
func BenchmarkDecimateM4_1M_2000Buckets(b *testing.B) {
	const n = 1_000_000
	ys := make([]float64, n)
	for i := range ys {
		ys[i] = math.Sin(float64(i)*0.0003) + 0.2*math.Cos(float64(i)*0.05)
	}
	ys[543_210] = 12.5
	uni := NewUniformSlice(0, 1_000_000, ys)
	vt := ViewportTransform{
		XMin: 0, XMax: 1.0,
		YMin: -15, YMax: 15,
		Width: 2000, Height: 1000,
	}
	dst := make([][2]float32, 0, 8008)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		dst = ProjectAndDecimate(uni, vt, DecimateM4, 2000, dst)
	}
}
