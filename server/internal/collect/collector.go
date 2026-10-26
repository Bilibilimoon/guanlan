// Package collect 负责 /proc 与 /sys 的周期采样、速率差分与内存环形缓冲。
package collect

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// NetVal 单网卡累计字节数与差分速率。
type NetVal struct {
	RxBytes uint64  `json:"rxBytes"` // 累计
	TxBytes uint64  `json:"txBytes"`
	RxRate  float64 `json:"rxRate"` // B/s
	TxRate  float64 `json:"txRate"`
}

// DiskVal 单盘累计 I/O 与差分速率。
type DiskVal struct {
	ReadBytes  uint64  `json:"readBytes"` // 累计
	WriteBytes uint64  `json:"writeBytes"`
	ReadBps    float64 `json:"readBps"` // B/s
	WriteBps   float64 `json:"writeBps"`
	IOPS       float64 `json:"iops"`
}

// DiskHealth 无特权可读的磁盘健康信号。
type DiskHealth struct {
	IoErr uint64 `json:"ioErr"` // /sys/block/<d>/device/ioerr_cnt
	State string `json:"state"` // running / ...
}

// Mount 挂载点容量。
type Mount struct {
	Dir   string `json:"dir"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// UsedPct 使用率（0-100）。
func (m Mount) UsedPct() float64 {
	if m.Total == 0 {
		return 0
	}
	return float64(m.Total-m.Free) / float64(m.Total) * 100
}

// Sample 一次采样的全部指标瞬时值。
// 小写私有字段为累计计数器，仅用于与上一拍差分，不导出 JSON。
type Sample struct {
	T float64 `json:"t"` // unix 秒（亚秒精度：支持 <1s 采集间隔的差分）

	CPUTotal float64   `json:"cpu"`      // 0-100
	CPUCores []float64 `json:"cpuCores"` // 每核 0-100
	Load1    float64   `json:"load1"`
	Load5    float64   `json:"load5"`
	Load15   float64   `json:"load15"`

	MemTotal     uint64 `json:"memTotal"`
	MemAvailable uint64 `json:"memAvailable"`
	MemUsed      uint64 `json:"memUsed"` // Total - Available
	SwapTotal    uint64 `json:"swapTotal"`
	SwapUsed     uint64 `json:"swapUsed"`

	Temps map[string]float64 `json:"temps"` // °C，key 如 package/gpu/npu/sda
	Fan   int                `json:"fan"`   // RPM

	GPUMHz float64 `json:"gpuMHz"`
	NPUMHz float64 `json:"npuMHz"`
	GPUPct float64 `json:"gpuPct"` // freq/max×100
	NPUPct float64 `json:"npuPct"`

	// 整机功率估算（本机无功率计，模型推算，展示侧标注 EST）
	PowerW float64 `json:"powerW"`

	Net   map[string]NetVal  `json:"net"`
	Disks map[string]DiskVal `json:"disks"`

	// 磁盘健康信号与挂载点容量（无需入库，实时展示与告警评估用）
	DiskHealth map[string]DiskHealth `json:"diskHealth"` // key: sda/mmcblk0
	Mounts     []Mount               `json:"mounts"`     // 关键挂载点容量

	Uptime int64 `json:"uptime"` // 秒
	Procs  int   `json:"procs"`  // 进程总数

	// 累计计数器（差分基准）
	cpuTotalTicks uint64
	cpuIdleTicks  uint64
	coreTicks     []uint64
	coreIdle      []uint64
}

// Series 把样本里需要入库的标量序列展开为 name->value。
// 动态成员（温度、网卡、磁盘）以带前缀 key 表示，网卡/磁盘存速率。
func (s *Sample) Series() map[string]float64 {
	out := map[string]float64{
		"cpu":       s.CPUTotal,
		"load1":     s.Load1,
		"load5":     s.Load5,
		"load15":    s.Load15,
		"mem_used":  float64(s.MemUsed),
		"mem_pct":   pct(float64(s.MemUsed), float64(s.MemTotal)),
		"swap_used": float64(s.SwapUsed),
		"fan":       float64(s.Fan),
		"gpu_pct":   s.GPUPct,
		"npu_pct":   s.NPUPct,
		"power_w":   s.PowerW,
		"uptime":    float64(s.Uptime),
	}
	for k, v := range s.Temps {
		out["temp."+k] = v
	}
	for ifc, v := range s.Net {
		out["net."+ifc+".rx"] = v.RxRate
		out["net."+ifc+".tx"] = v.TxRate
	}
	for d, v := range s.Disks {
		out["disk."+d+".r"] = v.ReadBps
		out["disk."+d+".w"] = v.WriteBps
	}
	return out
}

func pct(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	return a / b * 100
}

// tickPct 由两拍累计 tick 差分出占用百分比。
func tickPct(tot, idle, prevTot, prevIdle uint64) float64 {
	dt := int64(tot) - int64(prevTot)
	di := int64(idle) - int64(prevIdle)
	if dt <= 0 {
		return 0
	}
	p := float64(dt-di) / float64(dt) * 100
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return p
}

// Collector 周期采集并维护最近样本的环形缓冲。
type Collector struct {
	mu    sync.RWMutex
	ring  []Sample
	head  int
	count int

	last *Sample // 差分基准（仅采集 goroutine 写，读需持锁）

	rot map[string]bool // 各块设备是否机械盘（构造时读一次，估算功耗用）

	intervalNs atomic.Int64 // 当前采集间隔，运行时可调
}

func NewCollector(capacity int) *Collector {
	return &Collector{ring: make([]Sample, capacity), rot: rotationalDevices()}
}

// Start 启动采集循环（阻塞调用者请用 go）。
func (c *Collector) Start(interval time.Duration) {
	c.SetInterval(interval)
	go func() {
		c.tick() // 首拍：建立差分基准
		for {
			time.Sleep(c.Interval())
			c.tick()
		}
	}()
}

// SetInterval 运行时调整采集间隔（夹在 100ms ~ 15s），即时生效。
func (c *Collector) SetInterval(d time.Duration) {
	if d < 100*time.Millisecond {
		d = 100 * time.Millisecond
	}
	if d > 15*time.Second {
		d = 15 * time.Second
	}
	c.intervalNs.Store(int64(d))
}

func (c *Collector) Interval() time.Duration {
	return time.Duration(c.intervalNs.Load())
}

func (c *Collector) tick() {
	sm := readAll()
	c.mu.RLock()
	prev := c.last
	c.mu.RUnlock()
	deriveRates(sm, prev)
	sm.PowerW = EstimatePowerW(sm, c.rot)

	c.mu.Lock()
	c.ring[c.head] = *sm
	c.head = (c.head + 1) % len(c.ring)
	if c.count < len(c.ring) {
		c.count++
	}
	c.last = sm
	c.mu.Unlock()
}

// deriveRates 基于上一拍的累计计数器差分出速率与 CPU 占用。
func deriveRates(sm *Sample, prev *Sample) {
	if prev == nil || sm.T <= prev.T {
		return
	}
	dt := sm.T - prev.T

	sm.CPUTotal = tickPct(sm.cpuTotalTicks, sm.cpuIdleTicks, prev.cpuTotalTicks, prev.cpuIdleTicks)
	sm.CPUCores = sm.CPUCores[:0]
	for i := range sm.coreTicks {
		if i < len(prev.coreTicks) {
			sm.CPUCores = append(sm.CPUCores,
				tickPct(sm.coreTicks[i], sm.coreIdle[i], prev.coreTicks[i], prev.coreIdle[i]))
		}
	}

	for ifc, v := range sm.Net {
		if p, ok := prev.Net[ifc]; ok {
			v.RxRate = rate(float64(v.RxBytes), float64(p.RxBytes), dt)
			v.TxRate = rate(float64(v.TxBytes), float64(p.TxBytes), dt)
			sm.Net[ifc] = v
		}
	}
	for d, v := range sm.Disks {
		if p, ok := prev.Disks[d]; ok {
			v.ReadBps = rate(float64(v.ReadBytes), float64(p.ReadBytes), dt)
			v.WriteBps = rate(float64(v.WriteBytes), float64(p.WriteBytes), dt)
			v.IOPS = (v.ReadBps + v.WriteBps) / 512 // 扇区粒度近似
			sm.Disks[d] = v
		}
	}
}

// rate 差分速率；计数器回绕/清零（网卡重置等）返回 0。
func rate(cur, prev, dt float64) float64 {
	if dt <= 0 || cur < prev {
		return 0
	}
	return (cur - prev) / dt
}

// Latest 返回最近一次采样（无数据时 T=0）。
func (c *Collector) Latest() Sample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.count == 0 {
		return Sample{}
	}
	i := (c.head - 1 + len(c.ring)) % len(c.ring)
	return c.ring[i]
}

// Snapshot 返回最近 sec 秒内的样本（时间升序）；sec<=0 表示全部。
func (c *Collector) Snapshot(sec float64) []Sample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.count == 0 {
		return nil
	}
	var out []Sample
	now := float64(time.Now().UnixNano()) / 1e9
	for n := 0; n < c.count; n++ {
		i := (c.head - 1 - n + 2*len(c.ring)) % len(c.ring)
		s := c.ring[i]
		if sec > 0 && s.T < now-sec {
			break
		}
		out = append(out, s)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Ifaces 返回最近出现过的网卡名（排序，供前端指标选择）。
func (c *Collector) Ifaces() []string {
	set := map[string]bool{}
	for _, s := range c.Snapshot(600) {
		for ifc := range s.Net {
			set[ifc] = true
		}
	}
	return sortedKeys(set)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
