package collect

import "testing"

func TestDeriveRatesCPU(t *testing.T) {
	prev := &Sample{
		T:             100,
		cpuTotalTicks: 10000, cpuIdleTicks: 9000,
		coreTicks: []uint64{1000, 2000}, coreIdle: []uint64{900, 1800},
	}
	sm := &Sample{
		T:             102,
		cpuTotalTicks: 11000, cpuIdleTicks: 9800,
		coreTicks: []uint64{1100, 2200}, coreIdle: []uint64{990, 1980},
	}
	deriveRates(sm, prev)
	// 总: dt=1000 di=800 → 20%
	if sm.CPUTotal != 20 {
		t.Fatalf("CPUTotal = %v, want 20", sm.CPUTotal)
	}
	// 每核: dt=100 di=90 → 10%
	if len(sm.CPUCores) != 2 || sm.CPUCores[0] != 10 || sm.CPUCores[1] != 10 {
		t.Fatalf("CPUCores = %v", sm.CPUCores)
	}
}

func TestDeriveRatesSameTimestamp(t *testing.T) {
	// 亚秒间隔修复的回归：相同时间戳不应清零或 panic
	prev := &Sample{T: 100, cpuTotalTicks: 1000, cpuIdleTicks: 500}
	sm := &Sample{T: 100, cpuTotalTicks: 1100, cpuIdleTicks: 550}
	deriveRates(sm, prev)
	if sm.CPUTotal != 0 {
		t.Fatalf("相同时间戳应跳过差分，CPUTotal = %v", sm.CPUTotal)
	}
}

func TestDeriveRatesSubSecond(t *testing.T) {
	// 0.5s 间隔的差分应正常（历史 bug：秒级时间戳导致 CPU=0）
	prev := &Sample{T: 100.0, cpuTotalTicks: 1000, cpuIdleTicks: 500,
		Net: map[string]NetVal{"eth0": {RxBytes: 1000, TxBytes: 2000}}}
	sm := &Sample{T: 100.5, cpuTotalTicks: 1500, cpuIdleTicks: 700,
		Net: map[string]NetVal{"eth0": {RxBytes: 1500, TxBytes: 2100}}}
	deriveRates(sm, prev)
	if sm.CPUTotal != 60 { // dt=500 di=200 → 40%? 计算：(500-200)/500=60%
		t.Fatalf("CPUTotal = %v, want 60", sm.CPUTotal)
	}
	if sm.Net["eth0"].RxRate != 1000 || sm.Net["eth0"].TxRate != 200 {
		t.Fatalf("net rates = %v", sm.Net["eth0"])
	}
}

func TestRateCounterWrap(t *testing.T) {
	// 计数器回绕/清零（网卡重置）应返回 0 而非负速率
	if got := rate(100, 5000, 2); got != 0 {
		t.Fatalf("rate(wrap) = %v, want 0", got)
	}
	if got := rate(6000, 5000, 2); got != 500 {
		t.Fatalf("rate(normal) = %v, want 500", got)
	}
}

func TestSeries(t *testing.T) {
	sm := &Sample{
		T: 1, CPUTotal: 12.5, Load1: 1, Fan: 1400,
		MemTotal: 100, MemUsed: 50,
		Temps: map[string]float64{"gpu-thermal": 40, "sda": 35},
		Net:   map[string]NetVal{"eth0": {RxRate: 100, TxRate: 50}},
		Disks: map[string]DiskVal{"sda": {ReadBps: 10, WriteBps: 20}},
	}
	s := sm.Series()
	if s["cpu"] != 12.5 || s["temp.gpu-thermal"] != 40 || s["net.eth0.rx"] != 100 ||
		s["disk.sda.w"] != 20 || s["mem_pct"] != 50 || s["fan"] != 1400 {
		t.Fatalf("Series = %v", s)
	}
}

func TestRingSnapshot(t *testing.T) {
	c := NewCollector(8)
	c.tick()
	if c.Latest().T == 0 {
		t.Fatal("tick 后应有样本")
	}
	got := c.Snapshot(900)
	if len(got) < 1 {
		t.Fatal("Snapshot 应至少 1 条")
	}
	for i := 1; i < len(got); i++ {
		if got[i].T < got[i-1].T {
			t.Fatal("Snapshot 应升序")
		}
	}
}
