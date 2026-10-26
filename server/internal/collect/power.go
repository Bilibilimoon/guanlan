package collect

import (
	"os"
	"strings"
)

// 功耗估算：本机没有功率计芯片 —— RK3588 的 hwmon 只有温度/风扇/硬盘温度，
// Type-C PD 口未接时电压电流全 0，12V 输入电压只出现在 root 的 debugfs 且没有电流读数。
// 因此这里按经验模型估算整机功率，只用于观察趋势（UI 一律标注 EST / 估算），
// 绝对值误差约 ±30%，不能当作电费依据。
//
// 模型：整机功率 = 板级基线 + CPU/GPU/NPU 活跃增量 + 各磁盘固定功耗 + I/O 增量。
// 基线含 SoC 空闲、内存与 12V→板载 DC-DC 的转换损耗。
const (
	powerBase = 6.0 // W：板级基线（不含磁盘）
	powerCPU  = 8.0 // W：CPU 满载增量（4×A76 + 4×A55）
	powerGPU  = 3.5 // W：GPU 满载增量
	powerNPU  = 2.5 // W：NPU 满载增量

	// 磁盘功耗按盘型给固定值 + 随 I/O 忙碌程度线性上升的增量。
	// 参考值：WD30EFRX 规格 idle 4.1W / 读写 4.5W；2.5" SSD 与 eMMC 不足 1W。
	diskIdleHDD = 4.0
	diskActHDD  = 1.0
	diskIdleSSD = 0.3
	diskActSSD  = 2.0

	hddSatBps = 150e6 // B/s：机械盘顺序吞吐量级，用于把 I/O 速率折算成忙碌比例
	ssdSatBps = 400e6 // B/s：SSD/eMMC 量级
)

// devfreq/GPU/NPU 的最低频档通常也在满频的 20~30%，这段底噪不应算作负载增量。
const idleFreqPct = 20.0

// activeShare 把百分比折算到 0..1 的活跃比例，先扣掉底噪。
func activeShare(pct, floor float64) float64 {
	r := (pct - floor) / (100 - floor)
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

// ioShare 把 I/O 速率折算到 0..1 的忙碌比例。
func ioShare(bps, sat float64) float64 {
	r := bps / sat
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

// rotationalDevices 扫描 /sys/block，返回各物理块设备是否机械盘。
// 盘型不会热插拔变化，采集器构造时读一次即可。
func rotationalDevices() map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return out
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "loop") || strings.HasPrefix(n, "ram") ||
			strings.HasPrefix(n, "zram") || strings.HasPrefix(n, "dm-") ||
			strings.HasPrefix(n, "sr") || strings.HasPrefix(n, "md") ||
			(strings.HasPrefix(n, "mmcblk") && strings.Contains(n, "boot")) {
			continue
		}
		b, err := os.ReadFile("/sys/block/" + n + "/queue/rotational")
		out[n] = err == nil && strings.TrimSpace(string(b)) == "1"
	}
	return out
}

// EstimatePowerW 估算整机功率（W）。rot 为盘型表；未知设备按固态盘处理。
func EstimatePowerW(sm *Sample, rot map[string]bool) float64 {
	w := powerBase
	w += powerCPU * activeShare(sm.CPUTotal, 0)
	w += powerGPU * activeShare(sm.GPUPct, idleFreqPct)
	w += powerNPU * activeShare(sm.NPUPct, idleFreqPct)
	for d, v := range sm.Disks {
		busy := v.ReadBps + v.WriteBps
		if rot[d] {
			w += diskIdleHDD + diskActHDD*ioShare(busy, hddSatBps)
		} else {
			w += diskIdleSSD + diskActSSD*ioShare(busy, ssdSatBps)
		}
	}
	return w
}
