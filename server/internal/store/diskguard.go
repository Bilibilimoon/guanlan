package store

import (
	"log"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// DiskGuard 盯住数据目录所在文件系统的剩余空间：
// 低于设置阈值（meta disk_min_gb，单位 GB）时暂停历史落盘，
// 空间恢复后自动继续。采集与实时曲线不受影响，只是不再写库。
type DiskGuard struct {
	st  *Store
	dir string

	// FreeFn 默认读 statfs；测试可注入固定值
	FreeFn func(dir string) (uint64, error)

	mu        sync.Mutex
	lastCheck time.Time
	paused    bool
	freeGB    float64
	minGB     float64
}

// NewDiskGuard 创建守卫；dir 为数据目录（判定该目录所在盘）。
func NewDiskGuard(st *Store, dir string) *DiskGuard {
	return &DiskGuard{st: st, dir: dir, FreeFn: DiskFreeBytes}
}

// DiskFreeBytes 返回路径所在文件系统对当前用户可用的字节数。
func DiskFreeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// statfs 很便宜，但落盘周期最短 5 秒，没必要每次都查；20 秒缓存
// 既能让"空间恢复"及时生效，也不会把 statfs 打得太频繁。
const diskCheckInterval = 20 * time.Second

func (g *DiskGuard) refreshLocked(now time.Time) (changed bool) {
	if now.Sub(g.lastCheck) < diskCheckInterval {
		return false
	}
	g.lastCheck = now
	g.minGB = 0
	if v, ok := g.st.GetMeta("disk_min_gb"); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 100000 {
			g.minGB = f
		}
	}
	if g.minGB > 0 {
		// statfs 失败时不要把落盘卡死：按"空间充足"处理
		if free, err := g.FreeFn(g.dir); err == nil {
			g.freeGB = float64(free) / (1 << 30)
		} else {
			g.freeGB = -1
		}
	} else if free, err := g.FreeFn(g.dir); err == nil {
		// 阈值关闭时也刷新剩余空间，设置页要展示
		g.freeGB = float64(free) / (1 << 30)
	}
	paused := g.minGB > 0 && g.freeGB >= 0 && g.freeGB < g.minGB
	changed = paused != g.paused
	if changed {
		if paused {
			log.Printf("磁盘剩余 %.1f GB 低于阈值 %.1f GB：暂停历史落盘", g.freeGB, g.minGB)
		} else {
			log.Printf("磁盘剩余 %.1f GB：恢复历史落盘", g.freeGB)
		}
	}
	g.paused = paused
	return changed
}

// Allow 返回当前是否允许写历史（并顺便刷新状态）。
func (g *DiskGuard) Allow() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refreshLocked(time.Now())
	return !g.paused
}

// Invalidate 让下一次 Allow/Status 立即重新读阈值与磁盘空间（设置改动后调用）。
func (g *DiskGuard) Invalidate() {
	g.mu.Lock()
	g.lastCheck = time.Time{}
	g.mu.Unlock()
}

// Status 供 /api/meta 展示：阈值、剩余、是否暂停。
func (g *DiskGuard) Status() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refreshLocked(time.Now())
	return map[string]any{
		"minGb":  g.minGB,
		"freeGb": g.freeGB,
		"paused": g.paused,
	}
}
