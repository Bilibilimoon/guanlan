// 观澜 —— fnOS 系统监控：采集 / 存储 / API 单二进制入口。
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"guanlan/server/internal/alert"
	"guanlan/server/internal/api"
	"guanlan/server/internal/collect"
	"guanlan/server/internal/store"
)

var appVersion = "0.5.9"

func main() {
	var (
		addr    = flag.String("addr", ":9780", "HTTP 监听地址（开发模式）；fnOS 网关模式下改走 Unix socket")
		sock    = flag.String("sock", "", "Unix socket 路径（fnOS 网关模式，优先于 addr）")
		port    = flag.Int("port", 0, "可选 TCP 端口（0=不开；fnOS 应用设置以环境变量 port 传入）")
		dataDir = flag.String("data", defaultDataDir(), "数据目录（SQLite 落盘位置）")
		dev     = flag.Bool("dev", false, "开发模式：跳过管理员校验（fnOS 网关运行时必须为 false）")
	)
	flag.Parse()

	// fnOS 应用设置修改端口后以环境变量 port 传入（config_callback 重启生效）
	tcpPort := *port
	if tcpPort == 0 {
		if v := os.Getenv("port"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
				tcpPort = n
			}
		}
	} else {
		os.Setenv("port", strconv.Itoa(tcpPort))
	}

	os.MkdirAll(*dataDir, 0o755)

	// 采集/落盘间隔：持久化在库 meta 里，运行时可经 /api/config 调整
	st, err := store.Open(filepath.Join(*dataDir, "guanlan.db"))
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()

	interval := 2 * time.Second
	if v, ok := st.GetMeta("interval_ms"); ok {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil && ms >= 100 && ms <= 15000 {
			interval = time.Duration(ms) * time.Millisecond
		}
	}
	var dbIntervalMs int64 = 15000
	if v, ok := st.GetMeta("db_interval_ms"); ok {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil && ms >= 5000 && ms <= 60000 {
			dbIntervalMs = ms
		}
	}

	// 环形缓冲容量：按最小间隔 0.1s × 15 分钟预备
	col := collect.NewCollector(9000)
	col.Start(interval)

	// 落盘：按动态 dbInterval 从 ring 取最新样本写入 L0
	var dbDyn atomic.Int64
	dbDyn.Store(dbIntervalMs)
	go func() {
		for {
			time.Sleep(time.Duration(dbDyn.Load()) * time.Millisecond)
			if sm := col.Latest(); sm.T > 0 {
				if err := st.WriteSample(sm); err != nil {
					log.Printf("落盘失败: %v", err)
				}
			}
		}
	}()

	// 滚动保留 + 降采样：启动时各跑一次，此后每小时
	go func() {
		run := func() {
			if err := st.Downsample(); err != nil {
				log.Printf("降采样失败: %v", err)
			}
			if _, err := st.Retain(); err != nil {
				log.Printf("保留清理失败: %v", err)
			}
		}
		run()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			run()
		}
	}()

	// 告警评估：每 30 秒
	alertEval := alert.New(col, st)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			alertEval.Eval()
		}
	}()

	h := api.New(col, st, api.Options{
		Dev:     *dev,
		Version: appVersion,
		Alert:   alertEval,
		DbPath:  filepath.Join(*dataDir, "guanlan.db"),
		SetConfig: func(intervalMs, dbMs int64) error {
			if intervalMs > 0 {
				col.SetInterval(time.Duration(intervalMs) * time.Millisecond)
				if err := st.SetMeta("interval_ms", strconv.FormatInt(intervalMs, 10)); err != nil {
					return err
				}
			}
			if dbMs > 0 {
				dbDyn.Store(dbMs)
				if err := st.SetMeta("db_interval_ms", strconv.FormatInt(dbMs, 10)); err != nil {
					return err
				}
			}
			return nil
		},
	})
	log.Printf("观澜 v%s 启动：http://%s （采集 %s / 落盘 %s / 数据 %s）",
		appVersion, *addr, col.Interval(), time.Duration(dbDyn.Load())*time.Millisecond, *dataDir)
	if *sock == "" {
		log.Fatal(http.ListenAndServe(*addr, h))
		return
	}
	// fnOS 统一网关模式：监听 Unix socket（main 生命周期脚本传入路径），
	// 并剥掉可能的 /app/guanlan 前缀（前端用相对路径，通常已无前缀）
	os.Remove(*sock)
	ln, err := net.Listen("unix", *sock)
	if err != nil {
		log.Fatalf("监听 socket 失败: %v", err)
	}
	os.Chmod(*sock, 0o660)
	log.Printf("socket 模式：%s", *sock)

	// 可选 TCP：局域网直连只读访问（写/管理仅网关），端口来自 fnOS 应用设置
	if tcpPort > 0 {
		go func() {
			log.Printf("TCP 只读模式：0.0.0.0:%d", tcpPort)
			if err := http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", tcpPort), api.MarkTCP(h)); err != nil {
				log.Printf("TCP 监听失败: %v", err)
			}
		}()
	}
	log.Fatal(http.Serve(ln, stripPrefix(h, "/app/guanlan")))
}

// stripPrefix 剥掉统一网关注入的应用路径前缀。
func stripPrefix(h http.Handler, prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= len(prefix) && r.URL.Path[:len(prefix)] == prefix {
			r.URL.Path = r.URL.Path[len(prefix):]
			if r.URL.Path == "" {
				r.URL.Path = "/"
			}
		}
		h.ServeHTTP(w, r)
	})
}

// defaultDataDir: fnOS 环境用 TRIM_PKGVAR，否则当前目录 data/
func defaultDataDir() string {
	if v := os.Getenv("TRIM_PKGVAR"); v != "" {
		return v
	}
	return "data"
}
