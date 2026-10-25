package collect

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// readDiskHealth 读取各物理盘的 ioerr_cnt 与链路状态（无特权可读的健康信号）。
func readDiskHealth() map[string]DiskHealth {
	out := map[string]DiskHealth{}
	for dev := range physBlocks() {
		h := DiskHealth{}
		if b, err := os.ReadFile("/sys/block/" + dev + "/device/ioerr_cnt"); err == nil {
			// 格式 "ioerr_cnt 123"，取数字
			fields := strings.Fields(string(b))
			if len(fields) >= 2 {
				v, _ := strconv.ParseUint(fields[1], 10, 64)
				h.IoErr = v
			}
		}
		if b, err := os.ReadFile("/sys/block/" + dev + "/device/state"); err == nil {
			h.State = strings.TrimSpace(string(b))
		}
		if h.IoErr > 0 || h.State != "" {
			out[dev] = h
		}
	}
	return out
}

// readMounts 关键挂载点容量（/ 与 /vol* 上的真实文件系统）。
func readMounts() []Mount {
	var out []Mount
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	seen := map[string]bool{}
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		dir, fstype := fields[1], fields[2]
		switch fstype {
		case "ext4", "ext3", "btrfs", "xfs", "f2fs":
		default:
			continue
		}
		if !strings.HasPrefix(dir, "/vol") && dir != "/" {
			continue
		}
		if seen[dir] {
			continue
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(dir, &st); err != nil {
			continue
		}
		seen[dir] = true
		out = append(out, Mount{
			Dir:   dir,
			Total: uint64(st.Blocks) * uint64(st.Bsize),
			Free:  uint64(st.Bavail) * uint64(st.Bsize),
		})
	}
	return out
}
