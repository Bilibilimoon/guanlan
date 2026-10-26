package store

import "testing"

// TestDiskGuardThreshold 覆盖：低于阈值暂停、恢复后继续、阈值为 0 时关闭。
func TestDiskGuardThreshold(t *testing.T) {
	st, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	free := uint64(1 << 30) // 1 GB
	g := NewDiskGuard(st, t.TempDir())
	g.FreeFn = func(string) (uint64, error) { return free, nil }

	if err := st.SetMeta("disk_min_gb", "2.0"); err != nil {
		t.Fatal(err)
	}
	if g.Allow() {
		t.Fatal("剩余 1GB < 阈值 2GB 时应暂停落盘")
	}
	if !g.Status()["paused"].(bool) {
		t.Fatal("状态应为 paused")
	}

	// 空间恢复
	free = 5 << 30
	g.Invalidate()
	if !g.Allow() {
		t.Fatal("剩余 5GB > 阈值 2GB 时应恢复落盘")
	}
	if g.Status()["paused"].(bool) {
		t.Fatal("状态应恢复为 running")
	}

	// 阈值 0 = 关闭保护
	if err := st.SetMeta("disk_min_gb", "0"); err != nil {
		t.Fatal(err)
	}
	g.Invalidate()
	free = 1 << 20 // 1MB
	if !g.Allow() {
		t.Fatal("阈值为 0 时不应暂停")
	}

	// 非法阈值按 0 处理
	if err := st.SetMeta("disk_min_gb", "abc"); err != nil {
		t.Fatal(err)
	}
	g.Invalidate()
	if !g.Allow() {
		t.Fatal("非法阈值应按关闭处理")
	}
}

// TestDiskGuardStatfsError statfs 失败不应卡死落盘。
func TestDiskGuardStatfsError(t *testing.T) {
	st, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	g := NewDiskGuard(st, t.TempDir())
	g.FreeFn = func(string) (uint64, error) { return 0, errStatfs }
	if err := st.SetMeta("disk_min_gb", "2.0"); err != nil {
		t.Fatal(err)
	}
	if !g.Allow() {
		t.Fatal("statfs 失败时应按空间充足处理，避免永久停写")
	}
}

var errStatfs = &statfsError{}

type statfsError struct{}

func (e *statfsError) Error() string { return "statfs failed" }
