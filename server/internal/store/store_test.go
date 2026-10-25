package store

import (
	"path/filepath"
	"testing"
	"time"

	"guanlan/server/internal/collect"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestWriteAndQuery(t *testing.T) {
	s := openTest(t)
	sm := collect.Sample{T: 1000, CPUTotal: 50, Fan: 1400}
	if err := s.WriteSample(sm); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := s.Query("l0", []string{"cpu", "fan", "不存在"}, 0, 2000)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got["cpu"]) != 1 || got["cpu"][0][1] != 50 {
		t.Fatalf("cpu = %v", got["cpu"])
	}
	if len(got["fan"]) != 1 {
		t.Fatalf("fan = %v", got["fan"])
	}
	if got["不存在"] != nil {
		t.Fatalf("未知指标应为 nil")
	}
	// 表名白名单：非法 level 回落 l0
	if _, err := s.Query("evil; DROP TABLE metrics", []string{"cpu"}, 0, 2000); err != nil {
		t.Fatalf("level 白名单应回落不报错: %v", err)
	}
}

func TestWriteOverwritesSameSecond(t *testing.T) {
	s := openTest(t)
	s.WriteSample(collect.Sample{T: 1000, CPUTotal: 10})
	s.WriteSample(collect.Sample{T: 1000.9, CPUTotal: 20}) // 同秒覆盖
	got, _ := s.Query("l0", []string{"cpu"}, 0, 2000)
	if len(got["cpu"]) != 1 || got["cpu"][0][1] != 20 {
		t.Fatalf("同秒应覆盖: %v", got["cpu"])
	}
}

func TestDownsample(t *testing.T) {
	s := openTest(t)
	// L0 写 10 分钟数据（每分钟 2 点）
	for i := int64(0); i < 20; i++ {
		s.WriteSample(collect.Sample{T: float64(100000 + i*30), CPUTotal: float64(i)})
	}
	if err := s.Downsample(); err != nil {
		t.Fatalf("downsample: %v", err)
	}
	got, err := s.Query("l1", []string{"cpu"}, 0, 200000)
	if err != nil {
		t.Fatalf("query l1: %v", err)
	}
	if len(got["cpu"]) == 0 {
		t.Fatal("L1 应有聚合数据")
	}
	// 5 分钟桶对齐：t 应为 300 的倍数
	for _, p := range got["cpu"] {
		if int64(p[0])%300 != 0 {
			t.Fatalf("L1 时间未按 5 分钟桶对齐: %v", p[0])
		}
	}
	// 幂等：重复跑不产生重复行
	s.Downsample()
	got2, _ := s.Query("l1", []string{"cpu"}, 0, 200000)
	if len(got2["cpu"]) != len(got["cpu"]) {
		t.Fatal("Downsample 应幂等")
	}
}

func TestRetainAndMeta(t *testing.T) {
	s := openTest(t)
	now := time.Now().Unix()
	s.WriteSample(collect.Sample{T: float64(now - 60), CPUTotal: 1}) // 1 分钟前的数据
	if _, err := s.Retain(); err != nil {
		t.Fatalf("retain: %v", err)
	}
	got, _ := s.Query("l0", []string{"cpu"}, 0, now+100)
	if len(got["cpu"]) != 1 {
		t.Fatal("新数据不应被清理")
	}
	// 极老数据应被清理
	s.WriteSample(collect.Sample{T: 1000, CPUTotal: 1}) // 1970 年
	s.Retain()
	got2, _ := s.Query("l0", []string{"cpu"}, 0, now+100)
	if len(got2["cpu"]) != 1 {
		t.Fatal("过期数据应被清理")
	}
	if err := s.SetMeta("k", "v"); err != nil {
		t.Fatalf("setmeta: %v", err)
	}
	if v, ok := s.GetMeta("k"); !ok || v != "v" {
		t.Fatal("meta 读写失败")
	}
}

func TestConcurrentQuery(t *testing.T) {
	// 回归：并发查询未知指标不应 panic（旧实现读锁内写缓存 map）
	s := openTest(t)
	s.WriteSample(collect.Sample{T: 1000, CPUTotal: 1})
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				s.Query("l0", []string{"cpu", "unknown-metric"}, 0, 2000)
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
