// Package audit 记录访问来源。
//
// 两个数据源：
//  1. fnOS nginx 的 access.log（覆盖 5666/5667/80/443 的全部 Web 访问，含其他应用）。
//     只读、增量解析；offset 与 inode 持久化在库 meta，轮转/截断后自动重头读。
//     首次启用导入最近 7 天历史，之后只增量，重复启动不会重复计数。
//  2. 观澜 TCP 只读端口直连（不经过 nginx，日志里没有），由 HTTP 中间件调 Record。
//
// 只存聚合结果（来源 IP × 小时 × 访问目标），不存原始请求行；
// 路径一律去掉 query，避免把 token/参数写进库。
package audit

import (
    "bufio"
    "io"
    "log"
    "net"
    "os"
    "strconv"
    "strings"
    "sync"
    "syscall"
    "time"

    "guanlan/server/internal/store"
)

const (
    // DefaultLogPath fnOS nginx 访问日志默认位置（只读；meta access_log_path 可覆盖）
    DefaultLogPath = "/usr/trim/nginx/logs/access.log"
    // importWindow 首次启用导入的历史时长
    importWindow = 7 * 86400
    // maxBatchBytes 单轮最多解析的字节数，避免首次导入长时间占用协程
    maxBatchBytes = 8 << 20
    // flushInterval 由调用方控制（main 里 30 秒）
    uaMax   = 120
    bufMax  = 20000
    metaOff = "access_log_off"
    metaIno = "access_log_ino"
    metaImported = "access_imported"
    metaParsed = "access_log_parsed"
)

type key struct {
    hour  int64
    ip    string
    topic string
}

type agg struct {
    n, page        int64
    s2, s3, s4, s5 int64
    first, last    int64
    ua             string
}

// Auditor 聚合访问记录并周期性落库。
type Auditor struct {
    st *store.Store

    mu        sync.Mutex
    buf       map[key]*agg
    off       int64
    ino       uint64
    offLoaded bool

    available bool
    imported  bool
    lastParse time.Time
    lastErr   string
    parsed    int64
    skipped   int64
}

func New(st *store.Store) *Auditor {
    return &Auditor{st: st, buf: map[key]*agg{}}
}

// Run 后台循环：每 interval 解析日志增量并落库一次。
// 首次启用要把最近 7 天导入完（单轮上限 8MB，17MB 日志约 3 轮），
// 导入未完成时每 2 秒续读，避免首屏长时间只有半截数据。
func (a *Auditor) Run(interval time.Duration) {
    a.Step()
    for i := 0; i < 30 && !a.importedNow(); i++ {
        time.Sleep(2 * time.Second)
        a.Step()
    }
    t := time.NewTicker(interval)
    defer t.Stop()
    for range t.C {
        a.Step()
    }
}

func (a *Auditor) importedNow() bool {
    a.mu.Lock()
    defer a.mu.Unlock()
    return a.imported
}

// Step 执行一轮：解析日志 → 写库。单测与 dev 可直接调用。
func (a *Auditor) Step() {
    a.parseLog()
    a.flush()
}

// Record TCP 直连的一次访问（由 api 中间件调用）。
func (a *Auditor) Record(ip, path, method, ua string, status int, t time.Time) {
    a.add(ip, path, method, status, t, ua)
}

// Status 数据源状态（/api/visitors 用）。
func (a *Auditor) Status() map[string]any {
    a.mu.Lock()
    defer a.mu.Unlock()
    path := DefaultLogPath
    if v, ok := a.st.GetMeta("access_log_path"); ok && strings.TrimSpace(v) != "" {
        path = strings.TrimSpace(v)
    }
    return map[string]any{
        "path":      path,
        "available": a.available,
        "imported":  a.imported,
        "lastParse": a.lastParse.Unix(),
        "parsed":    a.parsed,
        "skipped":   a.skipped,
        "error":     a.lastErr,
    }
}

// parseLog 读取 access.log 新增部分。
func (a *Auditor) parseLog() {
    path := DefaultLogPath
    if v, ok := a.st.GetMeta("access_log_path"); ok && strings.TrimSpace(v) != "" {
        path = strings.TrimSpace(v)
    }
    fi, err := os.Stat(path)
    if err != nil {
        a.setError("日志不可读：" + err.Error())
        return
    }
    ino := inodeOf(fi)
    size := fi.Size()

    a.mu.Lock()
    if !a.offLoaded {
        a.off = metaInt64(a.st, metaOff)
        a.ino = uint64(metaInt64(a.st, metaIno))
        a.offLoaded = true
    }
    off, prevIno := a.off, a.ino
    a.mu.Unlock()

    // 轮转（inode 变）或截断（文件变小）后从头读：新文件内容与旧内容不重叠
    if prevIno != 0 && ino != prevIno {
        off = 0
    }
    if size < off {
        off = 0
    }

    now := time.Now()
    // 导入阶段可能跨多轮（单轮最多 maxBatchBytes）：期间保持 7 天过滤，
    // 读到文件末尾才算完成。完成标记只在这里置位，避免把旧日志也导进来。
    _, done := a.st.GetMeta(metaImported)
    importing := !done
    if importing {
        // 首次启用（从未写过 offset）：清空聚合表，从头导入最近 7 天
        if _, hasOff := a.st.GetMeta(metaOff); !hasOff {
            if _, err := a.st.DB().Exec(`DELETE FROM access_hourly`); err != nil {
                a.setError("清空访问表失败：" + err.Error())
                return
            }
            off = 0
        }
    }
    a.mu.Lock()
    a.imported = !importing
    a.mu.Unlock()

    f, err := os.Open(path)
    if err != nil {
        a.setError("打开日志失败：" + err.Error())
        return
    }
    defer f.Close()

    if _, err := f.Seek(off, io.SeekStart); err != nil {
        a.setError("定位日志失败：" + err.Error())
        return
    }

    cutoff := int64(0)
    if importing {
        cutoff = now.Unix() - importWindow
    }

    r := bufio.NewReaderSize(f, 64<<10)
    read := int64(0)
    var linesN, bad int64
    reachedEOF := false
    for {
        line, rerr := r.ReadString('\n')
        if len(line) > 0 && strings.HasSuffix(line, "\n") {
            read += int64(len(line))
            linesN++
            ip, t, method, p, status, ua, ok := parseLine(line)
            if ok {
                if cutoff == 0 || t.Unix() >= cutoff {
                    a.add(ip, p, method, status, t, ua)
                }
            } else if strings.TrimSpace(line) != "" {
                bad++
            }
        } else {
            // 不完整行（进程正在写）：不推进 offset，下一轮再读
            if rerr == io.EOF {
                reachedEOF = true
            }
            break
        }
        if read >= maxBatchBytes {
            break
        }
        if rerr == io.EOF {
            reachedEOF = true
            break
        }
        if rerr != nil {
            a.setError("读取日志失败：" + rerr.Error())
            break
        }
    }
    if importing && reachedEOF {
        a.st.SetMeta(metaImported, "1")
        a.mu.Lock()
        a.imported = true
        a.mu.Unlock()
    }


    newOff := off + read
    parsedTotal := metaInt64(a.st, metaParsed) + linesN
    a.st.SetMeta(metaParsed, strconv.FormatInt(parsedTotal, 10))
    a.mu.Lock()
    a.off = newOff
    a.ino = ino
    a.available = true
    a.lastParse = now
    a.lastErr = ""
    a.parsed = parsedTotal
    a.skipped += bad
    a.mu.Unlock()
    a.st.SetMeta(metaOff, strconv.FormatInt(newOff, 10))
    a.st.SetMeta(metaIno, strconv.FormatUint(ino, 10))
}

func (a *Auditor) setError(msg string) {
    a.mu.Lock()
    a.available = false
    a.lastErr = msg
    a.mu.Unlock()
}

// add 把一次访问并入当前批次。
func (a *Auditor) add(ip, path, method string, status int, t time.Time, ua string) {
    ip = normalizeIP(ip)
    if ip == "" || ip == "127.0.0.1" || ip == "::1" {
        return // 回环是 NAS 内部探测，记录没有意义
    }
    path = stripQuery(path)
    if path == "" {
        path = "/"
    }
    topic := TopicOf(path)
    k := key{hour: HourStart(t), ip: ip, topic: topic}
    ts := t.Unix()
    if len(ua) > uaMax {
        ua = ua[:uaMax]
    }

    a.mu.Lock()
    defer a.mu.Unlock()
    e := a.buf[k]
    if e == nil {
        if len(a.buf) >= bufMax {
            return // 防御：极端情况下丢弃，避免内存无上限
        }
        e = &agg{first: ts}
        a.buf[k] = e
    }
    e.n++
    if isPage(path, method) {
        e.page++
    }
    switch {
    case status >= 500:
        e.s5++
    case status >= 400:
        e.s4++
    case status >= 300:
        e.s3++
    case status >= 200:
        e.s2++
    }
    if ts > e.last {
        e.last = ts
    }
    if ts < e.first || e.first == 0 {
        e.first = ts
    }
    if ua != "" && ua != "-" {
        e.ua = ua
    }
}

// flush 把内存批次写入聚合表；写库失败时放回批次，下一轮重试。
func (a *Auditor) flush() {
    a.mu.Lock()
    if len(a.buf) == 0 {
        a.mu.Unlock()
        return
    }
    batch := a.buf
    a.buf = map[key]*agg{}
    a.mu.Unlock()

    ds := make([]store.AccessDelta, 0, len(batch))
    for k, v := range batch {
        ds = append(ds, store.AccessDelta{
            Hour: k.hour, IP: k.ip, Topic: k.topic,
            N: v.n, Page: v.page, S2: v.s2, S3: v.s3, S4: v.s4, S5: v.s5,
            FirstT: v.first, LastT: v.last, UA: v.ua,
        })
    }
    if err := a.st.UpsertAccess(ds); err != nil {
        log.Printf("访问记录落库失败: %v", err)
        a.mu.Lock()
        if len(a.buf) > bufMax {
            a.buf = map[key]*agg{}
        } else {
            for k, v := range batch {
                if cur := a.buf[k]; cur != nil {
                    cur.n += v.n
                    cur.page += v.page
                    cur.s2 += v.s2
                    cur.s3 += v.s3
                    cur.s4 += v.s4
                    cur.s5 += v.s5
                    if v.first > 0 && (cur.first == 0 || v.first < cur.first) {
                        cur.first = v.first
                    }
                    if v.last > cur.last {
                        cur.last = v.last
                    }
                    if v.ua != "" {
                        cur.ua = v.ua
                    }
                } else {
                    a.buf[k] = v
                }
            }
        }
        a.mu.Unlock()
    }
}

// ---- 解析与归一化（导出供单测与 api 复用）----

// parseLine 解析一行 nginx combined 日志。
func parseLine(line string) (ip string, t time.Time, method, path string, status int, ua string, ok bool) {
    line = strings.TrimRight(line, "\r\n")
    sp := strings.IndexByte(line, ' ')
    if sp <= 0 {
        return
    }
    ip = line[:sp]
    rest := line[sp+1:]
    lb := strings.IndexByte(rest, '[')
    if lb < 0 {
        return
    }
    rb := strings.IndexByte(rest[lb:], ']')
    if rb < 0 {
        return
    }
    ts := rest[lb+1 : lb+rb]
    tt, err := time.Parse("02/Jan/2006:15:04:05 -0700", ts)
    if err != nil {
        return
    }
    t = tt

    after := rest[lb+rb+1:]
    q1 := strings.IndexByte(after, '"')
    if q1 < 0 {
        return
    }
    q2 := strings.IndexByte(after[q1+1:], '"')
    if q2 < 0 {
        return
    }
    req := after[q1+1 : q1+1+q2]
    parts := strings.SplitN(req, " ", 3)
    if len(parts) < 2 {
        return
    }
    method, path = parts[0], parts[1]

    tail := after[q1+1+q2+1:]
    fs := strings.Fields(tail)
    if len(fs) == 0 {
        return
    }
    n, err := strconv.Atoi(fs[0])
    if err != nil {
        return
    }
    status = n

    lastQ := strings.LastIndexByte(line, '"')
    if lastQ > 0 {
        if prevQ := strings.LastIndexByte(line[:lastQ], '"'); prevQ >= 0 {
            ua = line[prevQ+1 : lastQ]
        }
    }
    if ua == "-" {
        ua = ""
    }
    ok = true
    return
}

// normalizeIP 归一化来源地址：去 zone、IPv4-mapped 转 v4、公网 IPv6 按 /64 合并。
func normalizeIP(s string) string {
    s = strings.TrimSpace(s)
    if i := strings.IndexByte(s, '%'); i >= 0 {
        s = s[:i]
    }
    if len(s) > 7 && strings.HasPrefix(strings.ToLower(s), "::ffff:") {
        s = s[7:]
    }
    p := net.ParseIP(s)
    if p == nil {
        return s
    }
    if p.To4() == nil && !p.IsLoopback() && !p.IsLinkLocalUnicast() && !p.IsPrivate() {
        // 公网 IPv6 隐私地址每天变：按 /64 归并到同一设备前缀
        b := p.To16()
        prefix := make(net.IP, 16)
        copy(prefix, b[:8])
        return prefix.String() + "/64"
    }
    return p.String()
}

// Classify 来源归属：lan / public4 / public6 / other。
func Classify(ip string) string {
    raw := ip
    if i := strings.IndexByte(raw, '/'); i >= 0 {
        raw = raw[:i]
    }
    p := net.ParseIP(raw)
    if p == nil {
        return "other"
    }
    if p.IsLoopback() {
        return "loop"
    }
    if p.IsLinkLocalUnicast() {
        return "lan"
    }
    if p.To4() != nil {
        if p.IsPrivate() || cgnat.Contains(p) {
            return "lan"
        }
        return "public4"
    }
    if p.IsPrivate() {
        return "lan"
    }
    return "public6"
}

var cgnat = func() *net.IPNet {
    _, n, _ := net.ParseCIDR("100.64.0.0/10")
    return n
}()

// TopicOf 访问目标：app:<应用名>，其余取路径首段。
func TopicOf(path string) string {
    path = strings.TrimPrefix(path, "/")
    if path == "" {
        return "root"
    }
    seg := path
    if i := strings.IndexByte(seg, '/'); i >= 0 {
        seg = seg[:i]
    }
    if seg == "app" {
        rest := strings.TrimPrefix(path, "app/")
        if j := strings.IndexByte(rest, '/'); j >= 0 {
            rest = rest[:j]
        }
        if rest != "" {
            return "app:" + rest
        }
    }
    if seg == "assets" || seg == "static" {
        return "assets"
    }
    return seg
}

var assetExt = map[string]bool{
    ".js": true, ".mjs": true, ".css": true, ".map": true, ".json": true,
    ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
    ".ico": true, ".webp": true, ".woff": true, ".woff2": true, ".ttf": true,
}

// isPage 是否算一次「页面打开」：排除接口轮询与静态资源，避免 2 秒一条的
// /api/realtime 把访问量算爆。
func isPage(path, method string) bool {
    if method != "GET" && method != "HEAD" {
        return false
    }
    if strings.Contains(path, "/api/") || strings.HasPrefix(path, "/assets/") {
        return false
    }
    slash := strings.LastIndexByte(path, '/')
    if dot := strings.LastIndexByte(path, '.'); dot > slash {
        if assetExt[strings.ToLower(path[dot:])] {
            return false
        }
    }
    return true
}

func stripQuery(path string) string {
    if i := strings.IndexByte(path, '?'); i >= 0 {
        return path[:i]
    }
    return path
}

// hourStart 本地时区整点（Unix 秒）。
// HourStart 本地时区整点（Unix 秒）；api 查询范围对齐小时桶用。
func HourStart(t time.Time) int64 {
    lt := t.In(time.Local)
    y, m, d := lt.Date()
    return time.Date(y, m, d, lt.Hour(), 0, 0, 0, lt.Location()).Unix()
}

func inodeOf(fi os.FileInfo) uint64 {
    if st, ok := fi.Sys().(*syscall.Stat_t); ok {
        return uint64(st.Ino)
    }
    return 0
}

func metaInt64(st *store.Store, k string) int64 {
    if v, ok := st.GetMeta(k); ok {
        if n, err := strconv.ParseInt(v, 10, 64); err == nil {
            return n
        }
    }
    return 0
}
