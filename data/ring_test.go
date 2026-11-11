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
