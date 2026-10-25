package collect

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

// readAll 汇总一次 /proc + /sys 采样。
func readAll() *Sample {
	sm := &Sample{
		T:          float64(time.Now().UnixNano()) / 1e9,
		Temps:      readTemps(),
		Net:        map[string]NetVal{},
		Disks:      map[string]DiskVal{},
		DiskHealth: readDiskHealth(),
		Mounts:     readMounts(),
	}
	sm.Fan = readFan()
	sm.GPUMHz, sm.GPUPct = readDevfreq("fb000000.gpu")
	sm.NPUMHz, sm.NPUPct = readDevfreq("fdab0000.npu")

	readCPU(sm)
	readMem(sm)
	readNet(sm)
	readDiskStats(sm)
	readLoadUptime(sm)
	return sm
}

// readCPU 解析 /proc/stat 的累计 tick（占用率由 deriveRates 差分）。
func readCPU(sm *Sample) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		vals := make([]uint64, 0, len(fields)-1)
		for i := 1; i < len(fields); i++ {
			v, _ := strconv.ParseUint(fields[i], 10, 64)
			vals = append(vals, v)
		}
		var tot, idle uint64
		for _, v := range vals {
			tot += v
		}
		if len(vals) > 3 {
			idle = vals[3] // idle
			if len(vals) > 4 {
				idle += vals[4] // + iowait
			}
		}
		if fields[0] == "cpu" {
			sm.cpuTotalTicks, sm.cpuIdleTicks = tot, idle
		} else {
			sm.coreTicks = append(sm.coreTicks, tot)
			sm.coreIdle = append(sm.coreIdle, idle)
		}
	}
}

// readMem 解析 /proc/meminfo（kB -> bytes）。
func readMem(sm *Sample) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var memTotal, memAvailable, swapTotal, swapFree uint64
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64) // kB
		switch fields[0] {
		case "MemTotal:":
			memTotal = v * 1024
		case "MemAvailable:":
			memAvailable = v * 1024
		case "SwapTotal:":
			swapTotal = v * 1024
		case "SwapFree:":
			swapFree = v * 1024
		}
	}
	sm.MemTotal = memTotal
	sm.MemAvailable = memAvailable
	if memTotal > memAvailable {
		sm.MemUsed = memTotal - memAvailable
	}
	sm.SwapTotal = swapTotal
	sm.SwapUsed = swapTotal - swapFree
}

// readNet 解析 /proc/net/dev；排除 lo 与瞬态 veth。
func readNet(sm *Sample) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // 表头两行
	sc.Scan()
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "lo" || strings.HasPrefix(name, "veth") {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 16 {
			continue
		}
		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)
		sm.Net[name] = NetVal{RxBytes: rx, TxBytes: tx}
	}
}

// readDiskStats 解析 /proc/diskstats；只取物理块设备（/sys/block 且非 loop/ram/zram/dm）。
func readDiskStats(sm *Sample) {
	valid := physBlocks()
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 14 {
			continue
		}
		name := fields[2]
		if !valid[name] {
			continue
		}
		rSect, _ := strconv.ParseUint(fields[5], 10, 64) // 读扇区
		wSect, _ := strconv.ParseUint(fields[9], 10, 64) // 写扇区
		sm.Disks[name] = DiskVal{ReadBytes: rSect * 512, WriteBytes: wSect * 512}
	}
}

// physBlocks 列出 /sys/block 下的物理块设备名。
func physBlocks() map[string]bool {
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
		out[n] = true
	}
	return out
}

// readLoadUptime 解析 /proc/loadavg 与 /proc/uptime。
func readLoadUptime(sm *Sample) {
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) >= 4 {
			sm.Load1, _ = strconv.ParseFloat(fields[0], 64)
			sm.Load5, _ = strconv.ParseFloat(fields[1], 64)
			sm.Load15, _ = strconv.ParseFloat(fields[2], 64)
			if slash := strings.IndexByte(fields[3], '/'); slash > 0 {
				n, _ := strconv.Atoi(fields[3][slash+1:])
				sm.Procs = n
			}
		}
	}
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) >= 1 {
			fv, _ := strconv.ParseFloat(fields[0], 64)
			sm.Uptime = int64(fv)
		}
	}
}
