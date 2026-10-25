package alert

import (
	"testing"

	"guanlan/server/internal/collect"
)

func TestMetricValue(t *testing.T) {
	sm := collect.Sample{
		Temps: map[string]float64{
			"package-thermal": 50, "gpu-thermal": 60, "npu-thermal": 45, "sda": 40,
		},
		MemTotal: 1000, MemUsed: 920,
		Mounts: []collect.Mount{{Dir: "/", Total: 100, Free: 2}},
		Fan:    1400, Load1: 3,
	}
	cases := []struct {
		metric string
		want   float64
		ok     bool
	}{
		{"temp.any", 60, true},  // SoC 各路最大（不含盘温）
		{"temp.disk", 40, true}, // 盘温最大（不含 SoC）
		{"mem_pct", 92, true},
		{"fs", 98, true},
		{"fan", 1400, true},
		{"load1", 3, true},
		{"nope", 0, false},
	}
	for _, c := range cases {
		got, ok := metricValue(c.metric, sm)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s = (%v,%v), want (%v,%v)", c.metric, got, ok, c.want, c.ok)
		}
	}
}

func TestBreach(t *testing.T) {
	r := Rule{Op: ">", Threshold: 90}
	if !breach(r, 91) || breach(r, 90) {
		t.Fatal("> 判断错误（应严格大于）")
	}
	r2 := Rule{Op: "<", Threshold: 500}
	if !breach(r2, 400) || breach(r2, 500) {
		t.Fatal("< 判断错误（应严格小于）")
	}
}

func TestDefaultRulesSanity(t *testing.T) {
	ids := map[string]bool{}
	for _, r := range DefaultRules() {
		if ids[r.ID] {
			t.Fatalf("规则 ID 重复: %s", r.ID)
		}
		ids[r.ID] = true
		if r.Op != ">" && r.Op != "<" {
			t.Fatalf("%s 非法操作符 %s", r.ID, r.Op)
		}
		if r.ForMin > 15 {
			t.Fatalf("%s ForMin 超过环形缓冲能力(15min): %d", r.ID, r.ForMin)
		}
		if r.Level != "warn" && r.Level != "crit" {
			t.Fatalf("%s 非法级别 %s", r.ID, r.Level)
		}
	}
}
