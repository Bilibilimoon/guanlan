package api

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProcInfo 进程概要（只读）。
type ProcInfo struct {
	Pid     int     `json:"pid"`
	Comm    string  `json:"comm"`
	Cmd     string  `json:"cmd"`
	State   string  `json:"state"`
	CPUPct  float64 `json:"cpu"`     // 单核基准，>100 表示占用多核
	RSS     uint64  `json:"rss"`     // bytes
	VmSwap  uint64  `json:"vmSwap"`  // bytes
	StartAt int64   `json:"startAt"` // unix 秒
}

// 进程 CPU 差分缓存：两次请求间按 tick 差计算（首次请求为 0）
var procMu sync.Mutex
var procPrev = map[int]uint64{}
var procPrevAt int64

const clkTck = 100 // USER_HERTZ，arm64/x86 Linux 均为 100

func listProcesses() []ProcInfo {
	procMu.Lock()
	defer procMu.Unlock()
	now := time.Now().Unix()
	dt := float64(now - procPrevAt)
	if dt <= 0 {
		dt = 1
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	pageSize := uint64(os.Getpagesize())
	cur := map[int]uint64{}
	out := make([]ProcInfo, 0, 256)

	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// comm 可能含空格：从最后一个 ')' 之后解析
		end := strings.LastIndexByte(string(b), ')')
		if end < 0 {
			continue
		}
		f := strings.Fields(string(b[end+2:]))
		if len(f) < 20 {
			continue
		}
		state := f[0]
		utime, _ := strconv.ParseUint(f[11], 10, 64)
		stime, _ := strconv.ParseUint(f[12], 10, 64)
		startTick, _ := strconv.ParseUint(f[19], 10, 64)
		ticks := utime + stime
		cur[pid] = ticks

		pi := ProcInfo{Pid: pid, State: state}
		if prev, ok := procPrev[pid]; ok && ticks >= prev {
			pi.CPUPct = float64(ticks-prev) / clkTck / dt * 100
		}

		// RSS / Swap
		if mb, err := os.ReadFile("/proc/" + e.Name() + "/statm"); err == nil {
			mf := strings.Fields(string(mb))
			if len(mf) >= 2 {
				r, _ := strconv.ParseUint(mf[1], 10, 64)
				pi.RSS = r * pageSize
			}
		}
		if sb, err := os.ReadFile("/proc/" + e.Name() + "/status"); err == nil {
			for _, line := range strings.Split(string(sb), "\n") {
				if strings.HasPrefix(line, "VmSwap:") {
					f := strings.Fields(line)
					if len(f) >= 2 {
						v, _ := strconv.ParseUint(f[1], 10, 64)
						pi.VmSwap = v * 1024
					}
					break
				}
			}
		}
		// comm / cmdline
		if cb, err := os.ReadFile("/proc/" + e.Name() + "/cmdline"); err == nil && len(cb) > 0 {
			pi.Cmd = strings.Join(strings.Fields(strings.ReplaceAll(string(cb), "\x00", " ")), " ")
			if len(pi.Cmd) > 200 {
				pi.Cmd = pi.Cmd[:200] + "…"
			}
		}
		commStart := strings.IndexByte(string(b), '(')
		if commStart >= 0 && end > commStart {
			pi.Comm = string(b[commStart+1 : end])
		}
		if pi.Cmd == "" {
			pi.Cmd = pi.Comm
		}
		// 开机起始时间：starttime(tick)/100 + boot 时间
		pi.StartAt = bootTimeUnix() + int64(startTick/clkTck)
		out = append(out, pi)
	}
	procPrev = cur
	procPrevAt = now
	return out
}

func bootTimeUnix() int64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "btime ") {
			v, _ := strconv.ParseInt(strings.TrimSpace(line[6:]), 10, 64)
			return v
		}
	}
	return 0
}

func (s *Server) processes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"procs": listProcesses(), "t": time.Now().Unix()})
}
