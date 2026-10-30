package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"guanlan/server/internal/store"
)

const line7 = `192.0.2.10 - - [30/Oct/2026:20:17:34 +0800] "GET /app/guanlan/api/realtime HTTP/1.1" 200 705 "-" "Mozilla/5.0 (Windows NT 10.0) Firefox/150.0"` + "\n"

const line404 = `2001:db8:1:2:3:4:5:6 - - [30/Oct/2026:20:15:00 +0800] "GET /admin.php HTTP/1.1" 404 153 "-" "Mozilla/5.0 zgrab/0.x"` + "\n"

func TestParseLine(t *testing.T) {
	ip, ts, method, path, status, ua, ok := parseLine(line7)
	if !ok {
		t.Fatal("parse failed")
	}
	if ip != "192.0.2.10" || method != "GET" || path != "/app/guanlan/api/realtime" || status != 200 {
		t.Fatalf("unexpected parse: %q %q %q %d", ip, method, path, status)
	}
	if ts.Unix() != 1793362654 {
		t.Fatalf("ts=%d", ts.Unix())
	}
	if ua == "" {
		t.Fatal("ua empty")
	}

	ip2, _, _, path2, status2, _, ok2 := parseLine(line404)
	if !ok2 || ip2 != "2001:db8:1:2:3:4:5:6" || path2 != "/admin.php" || status2 != 404 {
		t.Fatalf("unexpected parse2: %q %q %d %v", ip2, path2, status2, ok2)
	}
}

func TestNormalizeIP(t *testing.T) {
	cases := map[string]string{
		"192.0.2.10":                        "192.0.2.10",
		"::ffff:192.168.1.9":                    "192.168.1.9",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"fd00::1":                               "fd00::1",
		"fe80::1%eth0":                          "fe80::1",
	}
	for in, want := range cases {
		if got := normalizeIP(in); got != want {
			t.Errorf("normalizeIP(%q)=%q want %q", in, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"192.0.2.10":       "public4",
		"10.1.2.3":             "lan",
		"100.64.1.1":           "lan",
		"1.2.3.4":              "public4",
		"2001:db8::/64":       "public6",
		"fd00::1":              "lan",
	}
	for in, want := range cases {
		if got := Classify(in); got != want {
			t.Errorf("Classify(%q)=%q want %q", in, got, want)
		}
	}
}

func TestTopicAndPage(t *testing.T) {
	if got := TopicOf("/app/guanlan/api/realtime"); got != "app:guanlan" {
		t.Errorf("topic=%q", got)
	}
	if got := TopicOf("/app/photos/index.html"); got != "app:photos" {
		t.Errorf("topic=%q", got)
	}
	if got := TopicOf("/v1/accountapi/check"); got != "v1" {
		t.Errorf("topic=%q", got)
	}
	if got := TopicOf("/"); got != "root" {
		t.Errorf("topic=%q", got)
	}
	if isPage("/app/guanlan/api/realtime", "GET") {
		t.Error("api should not be a page")
	}
	if isPage("/assets/index-abc.js", "GET") {
		t.Error("asset should not be a page")
	}
	if !isPage("/app/guanlan/", "GET") {
		t.Error("app entry should be a page")
	}
	if isPage("/app/guanlan/", "POST") {
		t.Error("post should not be a page")
	}
}

func TestImportThenIncremental(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	logPath := filepath.Join(dir, "access.log")
	if err := os.WriteFile(logPath, []byte(line7+line404), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta("access_log_path", logPath); err != nil {
		t.Fatal(err)
	}

	a := New(st)
	a.Step()

	rows, err := st.QueryAccess(0)
	if err != nil {
		t.Fatal(err)
	}
	// 两条来自不同 IP；IPv6 公网已按 /64 合并
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rows))
	}

	// 追加一行后增量解析：不能重复计数旧行
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(line7)
	f.Close()
	a.Step()

	rows2, err := st.QueryAccess(0)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, r := range rows2 {
		total += r.N
	}
	if total != 3 {
		t.Fatalf("total requests=%d want 3", total)
	}

	// TCP 直连记录同样进入聚合
	a.Record("192.0.2.20", "/api/visitors", "GET", "curl/8", 401, time.Now())
	a.Step()
	rows3, err := st.QueryAccess(0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows3 {
		if r.IP == "192.0.2.20" && r.S4 == 1 && r.N == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("TCP record missing: %+v", rows3)
	}
}
