package data

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRingBufferWrapAndWakeOnce(t *testing.T) {
	rb := NewRingBuffer(8, 100.0) // dt = 0.01s
	var wakes atomic.Int32
	rb.SetWakeFunc(func() { wakes.Add(1) })

	rb.ArmWake()
	// 连续高频推送 5 次，未重新 ArmWake 前只能触发 1 次唤醒
	for i := range 5 {
		rb.Push(float64(i*2), float64(i*2+1))
	}
	if got := wakes.Load(); got != 1 {
		t.Fatalf("expected exactly 1 wake, got %d", got)
	}

	// 总共写入 10 个点 [0..9]，容量 8，应保留 [2..9]
	if rb.Len() != 8 {
		t.Fatalf("Len = %d, want 8", rb.Len())
	}
	for i := range 8 {
		x, y := rb.XY(i)
		wantX := float64(2+i) * 0.01
		wantY := float64(2 + i)
		if math.Abs(x-wantX) > 1e-12 || y != wantY {
			t.Fatalf("XY(%d) = (%v, %v), want (%v, %v)", i, x, y, wantX, wantY)
		}
	}

	dst := make([]float64, 16)
	segT0, n := rb.CopyWindow(0.04, 0.08, dst)
	if n != 5 || math.Abs(segT0-0.04) > 1e-12 {
		t.Fatalf("CopyWindow: segT0=%v, n=%d", segT0, n)
	}
	for i := range n {
		if dst[i] != float64(4+i) {
			t.Fatalf("dst[%d] = %v, want %v", i, dst[i], 4+i)
		}
	}
}

func TestMultiRingBufferRollAndSweepZeroAlloc(t *testing.T) {
	const (
		channels = 64
		sr       = 1000.0
		winSec   = 2.0
	)
	mrb := NewMultiRingBuffer(channels, sr, channels*4000)

	// 写入 2.5 秒数据（跨入第 2 个扫描周期 0.5 秒处）
	frame := make([]float64, channels)
	for step := range 2500 {
		tm := float64(step) / sr
		for ch := range channels {
			// 第 1 周期幅值 +1，第 2 周期幅值 +5，验证新旧周期共存与擦除空白带
			if tm < 2.0 {
				frame[ch] = 1.0 + float64(ch)*0.01
			} else {
				frame[ch] = 5.0 + float64(ch)*0.01
			}
		}
		mrb.PushFrame(frame)
	}

	buckets := make([]MinMax, 200)
	// 测试滚动模式零分配
	allocsRoll := testing.AllocsPerRun(100, func() {
		v := mrb.DecimateWindow(7, 0.5, 2.499, len(buckets), buckets)
		if v == 0 {
			t.Fatal("expected valid buckets in roll mode")
		}
	})
	if allocsRoll != 0 {
		t.Fatalf("DecimateWindow allocated %.1f allocs/op, want 0", allocsRoll)
	}

	// 测试擦除扫描模式（窗口 2.0s，当前最新 2.499s -> 扫描头在 ~0.2495 处，擦除带 0.2s = 10% 宽度）
	var cursor float64
	allocsSweep := testing.AllocsPerRun(100, func() {
		var valid int
		cursor, valid = mrb.DecimateSweep(0, winSec, 0.2, len(buckets), buckets)
		if valid == 0 {
			t.Fatal("expected valid buckets in sweep mode")
		}
	})
	if allocsSweep != 0 {
		t.Fatalf("DecimateSweep allocated %.1f allocs/op, want 0", allocsSweep)
	}
	if cursor < 0.24 || cursor > 0.26 {
		t.Fatalf("cursorNorm = %v, want ~0.2495", cursor)
	}
	// 扫描头左侧应为新周期幅值 5.0
	if !buckets[10].Valid || math.Abs(float64(buckets[10].Max)-5.0) > 1e-4 {
		t.Fatalf("left new-pass bucket[10] = %+v, want ~5.0", buckets[10])
	}
	// 擦除间隙内（如桶 55，对应归一化位置 0.275）应为空白 Valid == false
	if buckets[55].Valid {
		t.Fatalf("erase-gap bucket[55] should be invalid/blank, got %+v", buckets[55])
	}
	// 擦除间隙右侧（如桶 120，对应归一化位置 0.60）应保留上一周期旧波形 1.0
	if !buckets[120].Valid || math.Abs(float64(buckets[120].Max)-1.0) > 1e-4 {
		t.Fatalf("right old-pass bucket[120] = %+v, want ~1.0", buckets[120])
	}
}

func TestMultiRingBufferConcurrentReadWrite(t *testing.T) {
	mrb := NewMultiRingBufferPerChannel(32, 2000.0, 4096)
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 采集写协程
	wg.Add(1)
	go func() {
		defer wg.Done()
		pkt := make([]float64, 32*16)
		for i := range pkt {
			pkt[i] = math.Sin(float64(i) * 0.1)
		}
		for range 500 {
			mrb.PushInterleaved(pkt)
		}
		close(stop)
	}()

	// 2 个并发消费者协程模拟 VSync 帧读取
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dst := make([]MinMax, 256)
			for {
				select {
				case <-stop:
					return
				default:
					tNow := mrb.LatestTime()
					mrb.DecimateWindow(0, tNow-1.0, tNow, len(dst), dst)
					mrb.DecimateSweep(15, 1.0, 0.05, len(dst), dst)
				}
			}
		}()
	}
	wg.Wait()
}

// 回归：扫描头恰好落在周期边界（newBuckets==0）且 eraseGapSec=0 时，
// 桶 0 只能包含当前周期样本，上一周期段不得回写覆盖同一桶。
func TestMultiRingBufferSweepCycleBoundaryNoOverlap(t *testing.T) {
	const (
		sr      = 1000.0
		winSec  = 1.0
		buckets = 200
	)
	mrb := NewMultiRingBufferPerChannel(1, sr, 2000)
	frame := []float64{100.0}
	for range 1000 {
		mrb.PushFrame(frame)
	}
	frame[0] = 1.0
	mrb.PushFrame(frame)

	dst := make([]MinMax, buckets)
	cursor, valid := mrb.DecimateSweep(0, winSec, 0, buckets, dst)
	if cursor != 0 {
		t.Fatalf("cursorNorm = %v, want 0 at exact cycle boundary", cursor)
	}
	if valid == 0 {
		t.Fatal("expected valid buckets")
	}
	if !dst[0].Valid || dst[0].Min != 1.0 || dst[0].Max != 1.0 {
		t.Fatalf("boundary bucket[0] = %+v, want Min=Max=1 (current cycle only)", dst[0])
	}
	if !dst[1].Valid || dst[1].Min != 100.0 || dst[1].Max != 100.0 {
		t.Fatalf("bucket[1] = %+v, want previous cycle Min=Max=100", dst[1])
	}
}

// 校验 mid-cycle 下 ScanSweepMinMax / PackShaderEnvelope 与 DecimateSweep 结果一致。
func TestMultiRingBufferSweepScanPackAgreement(t *testing.T) {
	const (
		nCh     = 4
		sr      = 1000.0
		winSec  = 2.0
		buckets = 200
	)
	mrb := NewMultiRingBufferPerChannel(nCh, sr, 4000)
	frame := make([]float64, nCh)
	for i := range 2500 {
		for ch := range nCh {
			frame[ch] = float64(i%97) + float64(ch)
		}
		mrb.PushFrame(frame)
	}

	dst := make([]MinMax, buckets)
	cursor, _ := mrb.DecimateSweep(0, winSec, 0.2, buckets, dst)

	s0 := make([]int32, buckets)
	sl := make([]int32, buckets)
	dMin := make([]float32, buckets)
	dMax := make([]float32, buckets)
	dVal := make([]byte, buckets)
	var scanMin, scanMax []float32
	var scanVal []byte
	cursor2 := mrb.ScanSweepMinMax(winSec, 0.2, buckets, s0, sl, dMin, dMax, dVal, func(ch int) {
		if ch == 0 {
			scanMin = append([]float32(nil), dMin...)
			scanMax = append([]float32(nil), dMax...)
			scanVal = append([]byte(nil), dVal...)
		}
	})
	if cursor != cursor2 {
		t.Fatalf("cursor mismatch: decimate=%v scan=%v", cursor, cursor2)
	}
	for b := range buckets {
		if dst[b].Valid != (scanVal[b] != 0) {
			t.Fatalf("bucket %d valid mismatch: decimate=%v scan=%v", b, dst[b].Valid, scanVal[b])
		}
		if dst[b].Valid && (dst[b].Min != scanMin[b] || dst[b].Max != scanMax[b]) {
			t.Fatalf("bucket %d: decimate min/max=%v/%v, scan=%v/%v", b, dst[b].Min, dst[b].Max, scanMin[b], scanMax[b])
		}
	}

	pix := make([]byte, 2*nCh*buckets*4)
	stride := buckets * 4
	minS := make([]float32, buckets)
	maxS := make([]float32, buckets)
	valS := make([]byte, buckets)
	s0p := make([]int32, buckets)
	slp := make([]int32, buckets)
	cursor3 := mrb.PackShaderEnvelope(0, 0, true, winSec, 0.2, buckets, false, 1.0, nil, pix, stride, s0p, slp, minS, maxS, valS)
	if cursor3 != cursor {
		t.Fatalf("PackShaderEnvelope cursor = %v, want %v", cursor3, cursor)
	}
	for b := range buckets {
		off := b * 4
		pMin := pix[off : off+4]
		empty := pMin[0] == 0 && pMin[1] == 0 && pMin[2] == 0
		if dst[b].Valid == empty {
			t.Fatalf("bucket %d pack validity mismatch: pix=%v decimate=%+v", b, pMin, dst[b])
		}
		if !dst[b].Valid {
			continue
		}
		q := float64(uint16(pMin[0])<<8|uint16(pMin[1])) - 32768.5
		if math.Abs(q-float64(dst[b].Min)) > 1.0 {
			t.Fatalf("bucket %d packed min %v vs decimate %v", b, q, dst[b].Min)
		}
	}
}
