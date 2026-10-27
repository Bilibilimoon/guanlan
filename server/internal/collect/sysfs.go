package collect

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// readTemps 汇总温度：
//   - /sys/class/thermal/thermal_zone*（SoC 各路，type 即 key）
//   - hwmon 里 name=drivetemp 的每块硬盘（key 取块设备名，退而求其次 drivetemp.N）
//
// 摄氏度（原值千分之一度）。
func readTemps() map[string]float64 {
	out := map[string]float64{}
	zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*")
	for _, z := range zones {
		tb, err := os.ReadFile(filepath.Join(z, "type"))
		if err != nil {
			continue
		}
		vb, err := os.ReadFile(filepath.Join(z, "temp"))
		if err != nil {
			continue
		}
		key := strings.TrimSpace(string(tb))
		mv, err := strconv.ParseInt(strings.TrimSpace(string(vb)), 10, 64)
		if err != nil {
			continue
		}
		out[key] = float64(mv) / 1000
	}

	// 硬盘温度：drivetemp hwmon，device 符号链接指向块设备
	hwmons, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	for i, h := range hwmons {
		nb, err := os.ReadFile(filepath.Join(h, "name"))
		if err != nil || strings.TrimSpace(string(nb)) != "drivetemp" {
			continue
		}
		key := fmt.Sprintf("drivetemp.%d", i)
		// SATA 盘的 device 本名是 SCSI 地址（如 3:0:0:0），真正的块设备名在
		// device/block/ 目录下（该目录名为 block，子目录才是 sda 这类设备名）
		if ents, err := os.ReadDir(filepath.Join(h, "device", "block")); err == nil && len(ents) > 0 {
			key = ents[0].Name() // 如 sda
		} else if dev, err := os.Readlink(filepath.Join(h, "device")); err == nil {
			if base := filepath.Base(dev); base != "" {
				key = base
			}
		}
		tempFiles, _ := filepath.Glob(filepath.Join(h, "temp*_input"))
		for _, tf := range tempFiles {
			vb, err := os.ReadFile(tf)
			if err != nil {
				continue
			}
			mv, err := strconv.ParseInt(strings.TrimSpace(string(vb)), 10, 64)
			if err != nil {
				continue
			}
			k := key
			if len(tempFiles) > 1 { // 多块盘共用一个 hwmon 时加序号
				k = key + "." + strings.TrimSuffix(strings.TrimPrefix(filepath.Base(tf), "temp"), "_input")
			}
			out[k] = float64(mv) / 1000
		}
	}
	return out
}

// readFan 读 pwmfan hwmon 的第一个 fanN_input（RPM）。
func readFan() int {
	fans, _ := filepath.Glob("/sys/class/hwmon/hwmon*/fan*_input")
	for _, f := range fans {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err == nil {
			return v
		}
	}
	return 0
}

// readDevfreq 读 devfreq 设备当前频率与相对最高频率的百分比。
// RK3588: fb000000.gpu / fdab0000.npu。
func readDevfreq(node string) (mhz, pct float64) {
	base := "/sys/class/devfreq/" + node
	b, err := os.ReadFile(base + "/cur_freq")
	if err != nil {
		return 0, 0
	}
	cur, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil {
		return 0, 0
	}
	max := cur
	if ab, err := os.ReadFile(base + "/available_frequencies"); err == nil {
		for _, f := range strings.Fields(string(ab)) {
			v, err := strconv.ParseFloat(f, 64)
			if err == nil && v > max {
				max = v
			}
		}
	}
	if max <= 0 {
		return 0, 0
	}
	return cur / 1e6, cur / max * 100
}
