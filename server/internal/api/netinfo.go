package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// LANIP 单个网卡的全局地址（排除 loopback 与链路本地 fe80::）。
type LANIP struct {
	Iface string `json:"iface"`
	V4    string `json:"v4"`
	V6    string `json:"v6"`
}

// lanIPs 枚举各网卡的内网 v4/v6 地址。
func lanIPs() []LANIP {
	var out []LANIP
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		var e LANIP
		e.Iface = ifc.Name
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}
			if ip.To4() != nil {
				if e.V4 == "" {
					e.V4 = ip.String()
				}
			} else if e.V6 == "" {
				e.V6 = ip.String()
			}
		}
		if e.V4 != "" || e.V6 != "" {
			out = append(out, e)
		}
	}
	return out
}

// 公网出口探测源（依次 fallback；实测本机可用，ipw.cn DNS 已失效故不用）
var publicV4Sources = []string{
	"https://ip.3322.net",
	"https://ifconfig.me",
	"https://api.ip.sb/ip",
}
var publicV6Sources = []string{
	"https://v6.ident.me",
	"https://api.ip.sb/ip",
	"https://api64.ipify.org",
}

// publicIPProber 周期探测公网出口地址（每小时刷新，失败保留旧值）。
type publicIPProber struct {
	mu      sync.RWMutex
	v4, v6  string
	checked time.Time
}

func newPublicIPProber() *publicIPProber {
	p := &publicIPProber{}
	go p.loop()
	return p
}

func (p *publicIPProber) loop() {
	for {
		if v4, ok := fetchPublicIP(publicV4Sources, "tcp4"); ok {
			p.mu.Lock()
			p.v4 = v4
			p.mu.Unlock()
		}
		if v6, ok := fetchPublicIP(publicV6Sources, "tcp6"); ok {
			p.mu.Lock()
			p.v6 = v6
			p.mu.Unlock()
		}
		p.mu.Lock()
		p.checked = time.Now()
		p.mu.Unlock()
		time.Sleep(time.Hour)
	}
}

func (p *publicIPProber) snapshot() (v4, v6 string, checked time.Time) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.v4, p.v6, p.checked
}

// fetchPublicIP 依次尝试源列表，强制走指定协议族出口，返回合法地址。
func fetchPublicIP(sources []string, network string) (string, bool) {
	client := &http.Client{
		Timeout: 6 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
	for _, url := range sources {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		s := strings.TrimSpace(string(b))
		ip := net.ParseIP(s)
		if ip == nil || len(s) > 45 {
			continue // 被劫持页面塞垃圾则换下一个源
		}
		if network == "tcp4" && ip.To4() == nil {
			continue
		}
		if network == "tcp6" && ip.To4() != nil {
			continue
		}
		return s, true
	}
	return "", false
}
