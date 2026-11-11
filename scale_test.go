package mygograph

import (
	"strings"
	"testing"
	"time"
)

func TestSIPrefixAndHighDCOffsetLinearScale(t *testing.T) {
	if got := FormatSIValue(0.00025, 2, "V"); got != "250 μV" {
		t.Fatalf("FormatSIValue(0.00025, V) = %q, want %q", got, "250 μV")
	}
	if got := FormatSIValue(12_500_000, 2, "Hz"); got != "12.5 MHz" {
		t.Fatalf("FormatSIValue(12.5M, Hz) = %q, want %q", got, "12.5 MHz")
	}
	if got := FormatSIValue(-4.5e-9, 2, "s"); got != "-4.5 ns" {
		t.Fatalf("FormatSIValue(-4.5ns) = %q, want %q", got, "-4.5 ns")
	}

	lin := NewLinearScale()
	ticks, sfx := lin.AppendTicks(-500e-6, 500e-6, 500, "V", nil)
	if sfx != "μV" {
		t.Fatalf("unit suffix = %q, want μV", sfx)
	}
	hasMajor := false
	for _, tk := range ticks {
		if tk.Major && tk.Label != "" {
			hasMajor = true
		}
	}
	if !hasMajor {
		t.Fatal("expected major labeled ticks in [-500uV, 500uV]")
	}

	// 高直流偏置微弱纹波：2.500000 V ± 10 μV 应自动切换至基数偏移标注
	dcTicks, dcSfx := lin.AppendTicks(2.499990, 2.500010, 500, "V", nil)
	if !strings.Contains(dcSfx, "@ +2.5 V") || !strings.Contains(dcSfx, "μV") {
		t.Fatalf("DC offset suffix = %q, expected μV @ +2.5 V", dcSfx)
	}
	if len(dcTicks) == 0 {
		t.Fatal("expected ticks in DC offset micro-ripple window")
	}
}

func TestLogScaleAndTimeScale(t *testing.T) {
	logSc := NewLogScale()
	// 传入含非正下界 [-10, 100000] 时应安全钳位至正区间而不崩溃
	ticks, sfx := logSc.AppendTicks(-10, 100_000, 600, "Hz", nil)
	if sfx != "Hz" {
		t.Fatalf("log scale unit = %q, want Hz", sfx)
	}
	if len(ticks) < 5 {
		t.Fatalf("expected decade and minor log ticks, got %d", len(ticks))
	}

	ts := &TimeScale{Location: time.UTC, TargetSpacingDIP: 88}
	// 1. Unix 纪元亚毫秒视口
	baseEpoch := 1776001200.0
	epTicks, epHdr := ts.AppendTicks(baseEpoch+0.001, baseEpoch+0.003, 600, "", nil)
	if len(epTicks) == 0 || !strings.Contains(epHdr, "ms") {
		t.Fatalf("epoch millisecond window ticks=%d hdr=%q", len(epTicks), epHdr)
	}

	// 2. 多小时日历时间视口
	dayTicks, _ := ts.AppendTicks(baseEpoch, baseEpoch+3600*12, 800, "", nil)
	foundColon := false
	for _, tk := range dayTicks {
		if strings.Contains(tk.Label, ":") {
			foundColon = true
			break
		}
	}
	if !foundColon {
		t.Fatalf("expected HH:MM ticks in 12h epoch window, got %+v", dayTicks)
	}
}
