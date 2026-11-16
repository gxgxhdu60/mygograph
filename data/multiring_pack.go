package data

import (
	"math"
	"sync"
)

const (
	numPackHelpers    = 3 // 3 常驻休眠辅助协程 + 调用者主线程 = 4 路零分配并行分片
	maxPoolPackBucket = 4096
)

type packWorkerScratch struct {
	minBuf [maxPoolPackBucket]float32
	maxBuf [maxPoolPackBucket]float32
	valBuf [maxPoolPackBucket]byte
}

var shaderPackPool struct {
	once      sync.Once
	mu        sync.Mutex
	ring      *MultiRingBuffer
	buckets   int
	spanSlot0 []int32
	spanLen   []int32
	doDC      bool
	quantMul  float32
	chScales  []float32
	dstPix    []byte
	dstStride int
	workCh    [numPackHelpers]chan int
	doneCh    chan struct{}
	panics    [numPackHelpers]any // 辅助协程捕获的 panic，由调用者协程在汇合后重新抛出
	bufs      [numPackHelpers + 1]packWorkerScratch
}

func initShaderPackPool() {
	shaderPackPool.doneCh = make(chan struct{}, numPackHelpers)
	for i := range numPackHelpers {
		ch := make(chan int, 1)
		shaderPackPool.workCh[i] = ch
		go func(c <-chan int) {
			for workerID := range c {
				runPackHelper(workerID)
			}
		}(ch)
	}
}

// runPackHelper 执行辅助分片；任何 panic 都被捕获并交由调用者协程重新抛出，
// 且无论成功与否都会发送完成信号，避免后台协程崩溃进程或令调用者永久阻塞。
func runPackHelper(workerID int) {
	p := &shaderPackPool
	defer func() {
		if r := recover(); r != nil {
			p.panics[workerID] = r
		}
		p.doneCh <- struct{}{}
	}()
	r := p.ring
	nCh := r.channels
	ch0 := (workerID * nCh) / (numPackHelpers + 1)
	ch1 := ((workerID + 1) * nCh) / (numPackHelpers + 1)
	packChannelRange16(
		r,
		ch0, ch1,
		p.buckets,
		p.spanSlot0,
		p.spanLen,
		p.doDC,
		p.quantMul,
		p.chScales,
		p.dstPix,
		p.dstStride,
		p.bufs[workerID].minBuf[:p.buckets],
		p.bufs[workerID].maxBuf[:p.buckets],
		p.bufs[workerID].valBuf[:p.buckets],
	)
}

// packChannelsPooled 将全部导联拆分为 numPackHelpers+1 个分片并行打包；调用方须持有 m.mu 读锁。
// 池互斥锁通过 defer 释放；主线程分片 panic 时仍会等待所有辅助分片汇合后才释放，
// 辅助分片的 panic 在汇合后于调用者协程重新抛出（可被上层 recover）。
func packChannelsPooled(m *MultiRingBuffer, buckets int, sSlot0, sLen []int32, doDC bool, quantMul float32, chScales []float32, dstPix []byte, dstStride int) {
	shaderPackPool.once.Do(initShaderPackPool)
	p := &shaderPackPool
	p.mu.Lock()
	defer p.mu.Unlock()

	p.ring = m
	p.buckets = buckets
	p.spanSlot0 = sSlot0
	p.spanLen = sLen
	p.doDC = doDC
	p.quantMul = quantMul
	p.chScales = chScales
	p.dstPix = dstPix
	p.dstStride = dstStride

	for i := range numPackHelpers {
		p.workCh[i] <- i
	}

	var helperPanic any
	func() {
		defer func() {
			for range numPackHelpers {
				<-p.doneCh
			}
			for i := range p.panics {
				if p.panics[i] != nil && helperPanic == nil {
					helperPanic = p.panics[i]
				}
				p.panics[i] = nil
			}
			// 立即清空临时引用，确保 GC 不滞留任何环形缓冲或像素切片
			p.ring = nil
			p.spanSlot0 = nil
			p.spanLen = nil
			p.chScales = nil
			p.dstPix = nil
		}()
		// 调用者主线程同步执行最后一个分片，零上下文切换浪费
		ch0 := (numPackHelpers * m.channels) / (numPackHelpers + 1)
		packChannelRange16(
			m,
			ch0, m.channels,
			buckets,
			sSlot0, sLen,
			doDC, quantMul, chScales,
			dstPix, dstStride,
			p.bufs[numPackHelpers].minBuf[:buckets],
			p.bufs[numPackHelpers].maxBuf[:buckets],
			p.bufs[numPackHelpers].valBuf[:buckets],
		)
	}()
	if helperPanic != nil {
		panic(helperPanic)
	}
}

// PackShaderEnvelope 在单次读锁内完成全部导联的极值降采样、可选实时去直流（DC Removal）、单导联独立倍率（chScales，传 nil 则统一为 1.0）与 16-bit RGBA 包络纹理打包（0 allocs/op）。
// 当通道数 >= 64 且 buckets <= 4096 时，自动唤醒 4 路零分配并行工作分片，在多核降频/满载压力下仍确保 256ch x 1kHz x 10s 远低于 6ms 门禁。
func (m *MultiRingBuffer) PackShaderEnvelope(
	tStart, tEnd float64,
	sweepMode bool,
	windowSec, eraseGapSec float64,
	buckets int,
	doDC bool,
	quantMul float32,
	chScales []float32,
	dstPix []byte,
	dstStride int,
	spanSlot0, spanLen []int32,
	minScratch, maxScratch []float32,
	valScratch []byte,
) (cursorNorm float64) {
	cursorNorm = -1
	if buckets <= 0 || len(spanSlot0) < buckets || len(spanLen) < buckets ||
		len(minScratch) < buckets || len(maxScratch) < buckets || len(valScratch) < buckets {
		return cursorNorm
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	// 目标纹理须容纳 2*channels 行、每行 buckets 个 RGBA 像素；不满足时（如通道数变化后纹理尚未重建）
	// 直接放弃本帧，避免在后台分片协程中越界写入。
	if m.channels > 0 {
		rowBytes := buckets * 4
		if dstStride < rowBytes || len(dstPix) < (2*m.channels-1)*dstStride+rowBytes {
			return cursorNorm
		}
	}

	sSlot0 := spanSlot0[:buckets]
	sLen := spanLen[:buckets]
	clear(sLen)

	if sweepMode {
		cursorNorm = 0
		if m.count > 0 && windowSec > 0 {
			var segs [2]sweepSeg
			var nSeg int
			cursorNorm, segs, nSeg = m.sweepSegments(windowSec, eraseGapSec, buckets)
			for _, s := range segs[:nSeg] {
				buildRingWindowSpansNoClear(m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, s.t0, s.t1, s.b1-s.b0, sSlot0[s.b0:s.b1], sLen[s.b0:s.b1])
			}
		}
	} else if m.count > 0 && tEnd > tStart {
		buildRingWindowSpansNoClear(m.capPerCh, m.head, m.count, m.total, m.t0Base, m.dt, tStart, tEnd, buckets, sSlot0, sLen)
	}

	if m.channels >= 64 && buckets <= maxPoolPackBucket {
		packChannelsPooled(m, buckets, sSlot0, sLen, doDC, quantMul, chScales, dstPix, dstStride)
		return cursorNorm
	}

	packChannelRange16(m, 0, m.channels, buckets, sSlot0, sLen, doDC, quantMul, chScales, dstPix, dstStride, minScratch[:buckets], maxScratch[:buckets], valScratch[:buckets])
	return cursorNorm
}

func packChannelRange16(
	m *MultiRingBuffer,
	chStart, chEnd int,
	buckets int,
	sSlot0, sLen []int32,
	doDC bool,
	quantMul float32,
	chScales []float32,
	pix []byte,
	stride int,
	dMin, dMax []float32,
	dVal []byte,
) {
	for ch := chStart; ch < chEnd; ch++ {
		m.scanChannelSpans(ch, sSlot0, sLen, dMin, dMax, dVal)

		var sum float32
		var cnt int
		if doDC {
			for b := range buckets {
				if dVal[b] != 0 {
					sum += dMin[b] + dMax[b]
					cnt++
				}
			}
		}
		var dcMean float32
		if doDC && cnt > 0 {
			dcMean = sum / float32(cnt*2)
		}
		chQMul := quantMul
		if ch < len(chScales) {
			chQMul *= chScales[ch]
		}
		bias := float32(32768.5) - dcMean*chQMul
		rowMinPix := pix[(2*ch)*stride : (2*ch)*stride+buckets*4]
		rowMaxPix := pix[(2*ch+1)*stride : (2*ch+1)*stride+buckets*4]

		for b := range buckets {
			off := b * 4
			pMin := rowMinPix[off : off+4 : off+4]
			pMax := rowMaxPix[off : off+4 : off+4]
			if dVal[b] == 0 {
				pMin[0], pMin[1], pMin[2], pMin[3] = 0, 0, 0, 255
				pMax[0], pMax[1], pMax[2], pMax[3] = 0, 0, 0, 255
				continue
			}
			qfLo := dMin[b]*chQMul + bias
			if qfLo < 1.5 {
				qfLo = 1.5
			} else if qfLo > 65535.5 {
				qfLo = 65535.5
			}
			qLo := uint16(qfLo)
			pMin[0] = uint8(qLo >> 8)
			pMin[1] = uint8(qLo)
			pMin[2] = 255
			pMin[3] = 255

			qfHi := dMax[b]*chQMul + bias
			if qfHi < 1.5 {
				qfHi = 1.5
			} else if qfHi > 65535.5 {
				qfHi = 65535.5
			}
			qHi := uint16(qfHi)
			pMax[0] = uint8(qHi >> 8)
			pMax[1] = uint8(qHi)
			pMax[2] = 255
			pMax[3] = 255
		}
	}
}

func buildRingWindowSpansNoClear(capSize, head, count int, total int64, t0Base, dt, tStart, tEnd float64, buckets int, spanSlot0, spanLen []int32) {
	if count == 0 || buckets <= 0 || tEnd <= tStart {
		return
	}
	firstAbs := total - int64(count)
	lastAbs := total - 1
	reqStart := int64(math.Ceil((tStart - t0Base) / dt))
	reqEnd := int64(math.Floor((tEnd - t0Base) / dt))
	startAbs := max(firstAbs, reqStart)
	endAbs := min(lastAbs, reqEnd)
	if startAbs > endAbs {
		return
	}
	oldestSlot := (head - count + capSize) % capSize

	if endAbs-startAbs+1 >= int64(buckets) {
		offStart := (tStart - t0Base) / dt
		stepIdx := ((tEnd - tStart) / float64(buckets)) / dt
		s0 := max(firstAbs, int64(math.Ceil(offStart)))
		for b := range buckets {
			var s1 int64
			if b == buckets-1 {
				s1 = endAbs
			} else {
				s1 = min(lastAbs, int64(math.Ceil(offStart+float64(b+1)*stepIdx))-1)
			}
			if s0 <= s1 {
				spanSlot0[b] = int32((oldestSlot + int(s0-firstAbs)) % capSize)
				spanLen[b] = int32(s1 - s0 + 1)
			}
			s0 = max(firstAbs, s1+1)
		}
		return
	}

	scale := float64(buckets) / (tEnd - tStart)
	for absIdx := startAbs; absIdx <= endAbs; absIdx++ {
		b := int(((t0Base - tStart) + float64(absIdx)*dt) * scale)
		if b < 0 {
			continue
		}
		if b >= buckets {
			b = buckets - 1
		}
		if spanLen[b] == 0 {
			spanSlot0[b] = int32((oldestSlot + int(absIdx-firstAbs)) % capSize)
			spanLen[b] = 1
		} else {
			spanLen[b]++
		}
	}
}

// scanChannelSpans 按预计算桶跨度表扫描单通道 [Min, Max] 包络至 dMin/dMax/dVal（dVal[b]==0 表示该桶无有效样本）。
func (m *MultiRingBuffer) scanChannelSpans(ch int, sSlot0, sLen []int32, dMin, dMax []float32, dVal []byte) {
	capCh := m.capPerCh
	nBlk := m.nBlk
	chOrd := m.ord32[ch*capCh : (ch+1)*capCh]
	chBMin := m.blkMin[ch*nBlk : (ch+1)*nBlk]
	chBMax := m.blkMax[ch*nBlk : (ch+1)*nBlk]
	buckets := len(sLen)

	if m.nonFiniteCnt[ch] == 0 {
		for b := range buckets {
			n := int(sLen[b])
			if n <= 0 {
				dVal[b] = 0
				continue
			}
			slot0 := int(sSlot0[b])
			var mnK, mxK int32
			if slot0+n <= capCh {
				if n < ringBlkSize*2 {
					// 内联高频快路径（如 1kHz x 10s -> 1000 桶，n=10）：零函数调用开销 + 纯 CMOV 无分支聚合
					seg := chOrd[slot0 : slot0+n]
					mn0, mx0 := seg[0], seg[0]
					if n > 1 {
						mn1, mx1 := seg[1], seg[1]
						i := 2
						for ; i+3 < n; i += 4 {
							s := seg[i : i+4 : i+4]
							mn0 = min(mn0, min(s[0], s[2]))
							mx0 = max(mx0, max(s[0], s[2]))
							mn1 = min(mn1, min(s[1], s[3]))
							mx1 = max(mx1, max(s[1], s[3]))
						}
						mn0 = min(mn0, mn1)
						mx0 = max(mx0, mx1)
						for ; i < n; i++ {
							v := seg[i]
							mn0 = min(mn0, v)
							mx0 = max(mx0, v)
						}
					}
					mnK, mxK = mn0, mx0
				} else {
					mnK, mxK = scanWideOrd32Clean(chOrd, chBMin, chBMax, slot0, slot0+n)
				}
			} else {
				mn1, mx1 := scanContigOrd32Clean(chOrd, chBMin, chBMax, slot0, capCh)
				mn2, mx2 := scanContigOrd32Clean(chOrd, chBMin, chBMax, 0, slot0+n-capCh)
				mnK = min(mn1, mn2)
				mxK = max(mx1, mx2)
			}
			dMin[b] = ord32ToFloat32(mnK)
			dMax[b] = ord32ToFloat32(mxK)
			dVal[b] = 1
		}
		return
	}

	for b := range buckets {
		n := int(sLen[b])
		if n <= 0 {
			dVal[b] = 0
			continue
		}
		slot0 := int(sSlot0[b])
		var mnK, mxK int32
		var ok bool
		if slot0+n <= capCh {
			mnK, mxK, ok = scanContigOrd32WithNaN(chOrd, chBMin, chBMax, slot0, slot0+n)
		} else {
			mn1, mx1, ok1 := scanContigOrd32WithNaN(chOrd, chBMin, chBMax, slot0, capCh)
			mn2, mx2, ok2 := scanContigOrd32WithNaN(chOrd, chBMin, chBMax, 0, slot0+n-capCh)
			if ok1 && ok2 {
				mnK, mxK, ok = min(mn1, mn2), max(mx1, mx2), true
			} else if ok1 {
				mnK, mxK, ok = mn1, mx1, true
			} else if ok2 {
				mnK, mxK, ok = mn2, mx2, true
			}
		}
		if ok {
			dMin[b] = ord32ToFloat32(mnK)
			dMax[b] = ord32ToFloat32(mxK)
			dVal[b] = 1
		} else {
			dVal[b] = 0
		}
	}
}

func (m *MultiRingBuffer) scanAllChannelsBySpans(
	buckets int,
	spanSlot0, spanLen []int32,
	dstMin, dstMax []float32,
	dstValid []byte,
	visit func(ch int),
) {
	sSlot0 := spanSlot0[:buckets]
	sLen := spanLen[:buckets]
	dMin := dstMin[:buckets]
	dMax := dstMax[:buckets]
	dVal := dstValid[:buckets]

	for ch := range m.channels {
		m.scanChannelSpans(ch, sSlot0, sLen, dMin, dMax, dVal)
		visit(ch)
	}
}

// scanWideOrd32Clean 利用 B=8 分块极值表扫描跨度 >= 16 的连续区间，减少 87.5% 内存读取。
func scanWideOrd32Clean(chOrd, chBMin, chBMax []int32, slot0, slotEnd int) (mn, mx int32) {
	b0 := (slot0 + ringBlkSize - 1) >> ringBlkShift
	b1 := slotEnd >> ringBlkShift
	mn = chBMin[b0]
	mx = chBMax[b0]
	for blk := b0 + 1; blk < b1; blk++ {
		mn = min(mn, chBMin[blk])
		mx = max(mx, chBMax[blk])
	}
	headEnd := b0 << ringBlkShift
	for s := slot0; s < headEnd; s++ {
		v := chOrd[s]
		mn = min(mn, v)
		mx = max(mx, v)
	}
	for s := b1 << ringBlkShift; s < slotEnd; s++ {
		v := chOrd[s]
		mn = min(mn, v)
		mx = max(mx, v)
	}
	return mn, mx
}

// scanContigOrd32Clean 扫描无 NaN/Inf 的连续保序 int32 区间 [slot0, slotEnd)（环形回绕边界辅助）。
func scanContigOrd32Clean(chOrd, chBMin, chBMax []int32, slot0, slotEnd int) (mn, mx int32) {
	n := slotEnd - slot0
	if n < ringBlkSize*2 {
		seg := chOrd[slot0:slotEnd]
		mn, mx = seg[0], seg[0]
		for i := 1; i < n; i++ {
			v := seg[i]
			mn = min(mn, v)
			mx = max(mx, v)
		}
		return mn, mx
	}
	return scanWideOrd32Clean(chOrd, chBMin, chBMax, slot0, slotEnd)
}

func scanContigOrd32WithNaN(chOrd, chBMin, chBMax []int32, slot0, slotEnd int) (mn, mx int32, ok bool) {
	mn = math.MaxInt32
	mx = ord32Invalid
	n := slotEnd - slot0
	if n < ringBlkSize*2 {
		for _, k := range chOrd[slot0:slotEnd] {
			if k != ord32Invalid {
				mn = min(mn, k)
				mx = max(mx, k)
				ok = true
			}
		}
		return mn, mx, ok
	}

	b0 := (slot0 + ringBlkSize - 1) >> ringBlkShift
	b1 := slotEnd >> ringBlkShift
	for blk := b0; blk < b1; blk++ {
		bmn := chBMin[blk]
		if bmn != ord32Invalid {
			mn = min(mn, bmn)
			mx = max(mx, chBMax[blk])
			ok = true
		}
	}
	headEnd := b0 << ringBlkShift
	for s := slot0; s < headEnd; s++ {
		k := chOrd[s]
		if k != ord32Invalid {
			mn = min(mn, k)
			mx = max(mx, k)
			ok = true
		}
	}
	for s := b1 << ringBlkShift; s < slotEnd; s++ {
		k := chOrd[s]
		if k != ord32Invalid {
			mn = min(mn, k)
			mx = max(mx, k)
			ok = true
		}
	}
	return mn, mx, ok
}
