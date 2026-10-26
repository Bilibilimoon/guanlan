package collect

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestEstimatePowerBaseline(t *testing.T) {
	sm := &Sample{Temps: map[string]float64{}, Net: map[string]NetVal{}, Disks: map[string]DiskVal{}}
	got := EstimatePowerW(sm, map[string]bool{})
	if !near(got, powerBase) {
		t.Fatalf("空载应为基线 %.1fW，得到 %.2f", powerBase, got)
	}
}

func TestEstimatePowerCPU(t *testing.T) {
	sm := &Sample{CPUTotal: 100}
	if got := EstimatePowerW(sm, nil); !near(got, powerBase+powerCPU) {
		t.Fatalf("CPU 满载应为基线+%.1fW，得到 %.2f", powerCPU, got)
	}
	sm.CPUTotal = 50
	if got := EstimatePowerW(sm, nil); !near(got, powerBase+powerCPU/2) {
		t.Fatalf("CPU 50%% 应加一半增量，得到 %.2f", got)
	}
}

func TestEstimatePowerFreqFloor(t *testing.T) {
	// GPU/NPU 最低频档常报 20% 上下，不应算作负载
	sm := &Sample{GPUPct: idleFreqPct, NPUPct: idleFreqPct - 5}
	if got := EstimatePowerW(sm, nil); !near(got, powerBase) {
		t.Fatalf("空闲频档不应增加功耗，得到 %.2f", got)
	}
	sm.GPUPct = 100
	sm.NPUPct = 100
	if got := EstimatePowerW(sm, nil); !near(got, powerBase+powerGPU+powerNPU) {
		t.Fatalf("GPU/NPU 满载应加 %.1fW，得到 %.2f", powerGPU+powerNPU, got)
	}
}

func TestEstimatePowerDisks(t *testing.T) {
	rot := map[string]bool{"sda": true, "mmcblk0": false}
	sm := &Sample{Disks: map[string]DiskVal{
		"sda":     {}, // 机械盘空闲
		"mmcblk0": {}, // eMMC 空闲
	}}
	got := EstimatePowerW(sm, rot)
	want := powerBase + diskIdleHDD + diskIdleSSD
	if !near(got, want) {
		t.Fatalf("磁盘空闲应为 %.2fW，得到 %.2f", want, got)
	}

	// I/O 打满时增量按各自饱和速率封顶
	sm.Disks["sda"] = DiskVal{ReadBps: hddSatBps * 2}
	sm.Disks["mmcblk0"] = DiskVal{WriteBps: ssdSatBps}
	got = EstimatePowerW(sm, rot)
	want = powerBase + diskIdleHDD + diskActHDD + diskIdleSSD + diskActSSD
	if !near(got, want) {
		t.Fatalf("磁盘满载应为 %.2fW，得到 %.2f", want, got)
	}
}

func TestSeriesIncludesPower(t *testing.T) {
	sm := &Sample{Temps: map[string]float64{}, Net: map[string]NetVal{}, Disks: map[string]DiskVal{}, PowerW: 12.3}
	if got := sm.Series()["power_w"]; !near(got, 12.3) {
		t.Fatalf("Series 应包含 power_w=12.3，得到 %v", got)
	}
}
