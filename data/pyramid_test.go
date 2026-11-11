package data

import (
	"math"
	"testing"
	"time"
)

func TestMinMaxPyramidSpikeFidelityAndIncrementalAppend(t *testing.T) {
	const total = 1_000_000
	raw := make([]float64, total)
	for i := range raw {
		raw[i] = 2.5 + 1e-4*math.Sin(float64(i)*0.002) // 带 2.5V 直流偏置的微弱信号
	}
	// 在深层位置埋入单样本正负脉冲毛刺
	raw[123_456] = 2.5 + 0.050
	raw[876_543] = 2.5 - 0.045

	batchPyr := NewMinMaxPyramid(1000.0, 1_000_000.0, raw)
	if batchPyr.Levels() < 2 {
		t.Fatalf("expected >= 2 pyramid levels for 1M points, got %d", batchPyr.Levels())
	}

	// 验证流式分块 Append 与一次性构建在各级节点统计量上完全一致
	incPyr := NewMinMaxPyramid(1000.0, 1_000_000.0, nil)
	for off := 0; off < total; {
		step := min(7777, total-off) // 故意采用非 64 对齐块长检验跨块边界增量聚合
		incPyr.Append(raw[off : off+step]...)
		off += step
	}
	if incPyr.Levels() != batchPyr.Levels() {
		t.Fatalf("incremental levels = %d, want %d", incPyr.Levels(), batchPyr.Levels())
	}
	for lvl := range batchPyr.levels {
		if len(incPyr.levels[lvl]) != len(batchPyr.levels[lvl]) {
			t.Fatalf("level %d len mismatch: %d vs %d", lvl, len(incPyr.levels[lvl]), len(batchPyr.levels[lvl]))
		}
		for idx := range batchPyr.levels[lvl] {
			if incPyr.levels[lvl][idx] != batchPyr.levels[lvl][idx] {
				t.Fatalf("level %d node %d mismatch: got %+v, want %+v",
					lvl, idx, incPyr.levels[lvl][idx], batchPyr.levels[lvl][idx])
			}
		}
	}

	// 在全览尺度（覆盖全部 100 万点 -> 400 像素桶，命中金字塔高层）校验两个单样本脉冲 100% 保留
	vt := ViewportTransform{
		XMin: 1000.0, XMax: 1001.0,
		YMin: 2.40, YMax: 2.60,
		Width: 400, Height: 300,
	}
	dst := make([][2]float32, 0, 2048)
	allocs := testing.AllocsPerRun(50, func() {
		dst = incPyr.Query(vt, 400, dst)
	})
	if allocs != 0 {
		t.Fatalf("Pyramid.Query allocated %.1f allocs/op, want 0", allocs)
	}

	wantPosY := vt.DataToScreenY(2.5 + 0.050)
	wantNegY := vt.DataToScreenY(2.5 - 0.045)
	foundPos, foundNeg := false, false
	for _, pt := range dst {
		if math.Abs(float64(pt[1]-wantPosY)) < 1e-3 {
			foundPos = true
		}
		if math.Abs(float64(pt[1]-wantNegY)) < 1e-3 {
			foundNeg = true
		}
	}
	if !foundPos || !foundNeg {
		t.Fatalf("pyramid missed Dirac spikes: foundPos=%v foundNeg=%v", foundPos, foundNeg)
	}
}

func TestMinMaxPyramid100MillionLatencyGate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 100M sample allocation in -short mode")
	}
	const n = 100_000_000
	raw := make([]float64, n)
	// 快速填充稀疏特征与单样本脉冲，控制单测初始化时间同时精确构建 1 亿点 4 层金字塔
	for i := 0; i < n; i += 4096 {
		raw[i] = math.Sin(float64(i) * 1e-6)
	}
	raw[33_333_333] = 42.0
	raw[77_777_777] = -37.5

	pyr := NewMinMaxPyramid(0, 10_000_000.0, raw) // 10 MSa/s, 10 秒总跨度
	dst := make([][2]float32, 0, 8192)

	spans := []struct {
		name       string
		tMin, tMax float64
	}{
		{"Full_100M", 0.0, 10.0},
		{"Medium_5M", 3.1, 3.6},
		{"Fine_50k", 3.332, 3.337},
		{"SingleCycle_100pts", 3.333330, 3.333340},
	}

	for _, sc := range spans {
		vt := ViewportTransform{
			XMin: sc.tMin, XMax: sc.tMax,
			YMin: -50, YMax: 50,
			Width: 2000, Height: 1000,
		}
		// 预热并验证 0 allocs/op
		allocs := testing.AllocsPerRun(20, func() {
			dst = pyr.Query(vt, 2000, dst)
		})
		if allocs != 0 {
			t.Fatalf("%s allocated %.1f allocs/op, want 0", sc.name, allocs)
		}

		// 测量 200 次连续缩放查询平均耗时，硬性门禁 < 0.3 ms
		const runs = 200
		start := time.Now()
		for range runs {
			dst = pyr.Query(vt, 2000, dst)
		}
		avgUs := float64(time.Since(start).Nanoseconds()) / float64(runs) / 1000.0
		t.Logf("100M Pyramid [%s]: avg query = %.2f μs (len=%d)", sc.name, avgUs, len(dst))
		if avgUs > 300.0 {
			t.Fatalf("100M Pyramid [%s] avg query %.2f μs exceeded 300 μs (0.3 ms) gate", sc.name, avgUs)
		}
	}
}

func BenchmarkPyramidQuery_100M_FullView(b *testing.B) {
	const n = 100_000_000
	raw := make([]float64, n)
	raw[50_000_000] = 10.0
	pyr := NewMinMaxPyramid(0, 10_000_000.0, raw)
	vt := ViewportTransform{XMin: 0, XMax: 10.0, YMin: -12, YMax: 12, Width: 2000, Height: 1000}
	dst := make([][2]float32, 0, 8192)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		dst = pyr.Query(vt, 2000, dst)
	}
}
