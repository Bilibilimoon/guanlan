// Package store 窄表时序存储：metrics 字典 + points 分层表（L0/L1/L2）。
package store

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"guanlan/server/internal/collect"
)

// 各层保留时长（秒）
const (
	RetainL0 = 30 * 86400  // 原始粒度
	RetainL1 = 180 * 86400 // 5 分钟聚合
	RetainL2 = 365 * 86400 // 1 小时聚合
)

type Store struct {
	db *sql.DB

	mu    sync.RWMutex
	ids   map[string]int64 // metric name -> id 缓存
	stmts map[string]*sql.Stmt
}

// Open 打开/初始化数据库（WAL、synchronous=NORMAL、页缓存限 2MB）。
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(1)&_pragma=busy_timeout(5000)&_pragma=cache_size(-2000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// 单写连接足够（写入方只有落盘 goroutine），避免 SQLITE_BUSY
	db.SetMaxOpenConns(2)

	s := &Store{db: db, ids: map[string]int64{}, stmts: map[string]*sql.Stmt{}}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS metrics(
  id   INTEGER PRIMARY KEY,
  name TEXT UNIQUE NOT NULL
);
CREATE TABLE IF NOT EXISTS points_l0(
  t INTEGER NOT NULL, k INTEGER NOT NULL, v REAL NOT NULL,
  PRIMARY KEY(k, t)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS points_l1(
  t INTEGER NOT NULL, k INTEGER NOT NULL, v REAL NOT NULL,
  PRIMARY KEY(k, t)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS points_l2(
  t INTEGER NOT NULL, k INTEGER NOT NULL, v REAL NOT NULL,
  PRIMARY KEY(k, t)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS meta(
  key TEXT PRIMARY KEY,
  val TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_metrics_name ON metrics(name);
-- 访问记录：按（来源 IP × 小时 × 目标）聚合，避免存原始请求把库撑爆
CREATE TABLE IF NOT EXISTS access_hourly(
  hour    INTEGER NOT NULL,
  ip      TEXT NOT NULL,
  topic   TEXT NOT NULL,
  n       INTEGER NOT NULL DEFAULT 0,
  page    INTEGER NOT NULL DEFAULT 0,
  s2      INTEGER NOT NULL DEFAULT 0,
  s3      INTEGER NOT NULL DEFAULT 0,
  s4      INTEGER NOT NULL DEFAULT 0,
  s5      INTEGER NOT NULL DEFAULT 0,
  first_t INTEGER NOT NULL DEFAULT 0,
  last_t  INTEGER NOT NULL DEFAULT 0,
  ua      TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(hour, ip, topic)
) WITHOUT ROWID;
`)
	return err
}

func (s *Store) Close() error {
	for _, st := range s.stmts {
		st.Close()
	}
	return s.db.Close()
}

// DB 暴露底层连接（告警等包建表用）。
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) metricID(tx *sql.Tx, name string) (int64, error) {
	s.mu.RLock()
	id, ok := s.ids[name]
	s.mu.RUnlock()
	if ok {
		return id, nil
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO metrics(name) VALUES(?)`, name); err != nil {
		return 0, err
	}
	if err := tx.QueryRow(`SELECT id FROM metrics WHERE name=?`, name).Scan(&id); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.ids[name] = id
	s.mu.Unlock()
	return id, nil
}

// WriteSample 把一个样本的序列值写入 L0（单事务批量）。
func (s *Store) WriteSample(sm collect.Sample) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO points_l0(t,k,v) VALUES(?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	series := sm.Series()
	tSec := int64(sm.T) // 落盘取整秒（落盘粒度 ≥5s，无亚秒需求）
	for name, v := range series {
		id, err := s.metricID(tx, name)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(tSec, id, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Query 返回 level 层（l0/l1/l2）指定指标在 [start,end] 内的序列。
func (s *Store) Query(level string, names []string, start, end int64) (map[string][][2]float64, error) {
	switch level {
	case "l0", "l1", "l2":
	default:
		level = "l0"
	}
	out := map[string][][2]float64{}
	for _, name := range names {
		if len(name) > 128 {
			out[name] = nil // 超长名（异常输入）直接视为无数据
			continue
		}
		// 不在读锁内回填缓存：Query 会并发调用，读锁内写 map 会 panic
		s.mu.RLock()
		id, cached := s.ids[name]
		s.mu.RUnlock()
		if !cached {
			var idv int64
			err := s.db.QueryRow(`SELECT id FROM metrics WHERE name=?`, name).Scan(&idv)
			if err != nil {
				out[name] = nil // 未知指标（尚无数据）不算错误
				continue
			}
			id = idv
		}
		rows, err := s.db.Query(
			`SELECT t, v FROM points_`+level+` WHERE k=? AND t BETWEEN ? AND ? ORDER BY t`,
			id, start, end)
		if err != nil {
			return nil, err
		}
		pts := [][2]float64{}
		for rows.Next() {
			var t int64
			var v float64
			if err := rows.Scan(&t, &v); err != nil {
				rows.Close()
				return nil, err
			}
			pts = append(pts, [2]float64{float64(t), v})
		}
		rows.Close()
		out[name] = pts
	}
	return out, nil
}

// AccessDelta 是一条待累计的访问记录（调用方已把同一 key 的多次访问合并）。
type AccessDelta struct {
	Hour           int64 // 小时桶（本地时区整点，Unix 秒）
	IP             string
	Topic          string // 访问目标：app:<name> / assets / api 等
	N              int64
	Page           int64 // 页面打开数（排除接口轮询与静态资源）
	S2, S3, S4, S5 int64
	FirstT, LastT  int64
	UA             string
}

// UpsertAccess 批量累计写入访问聚合表：同一（小时, IP, 目标）累加计数，
// 首末时间取并集，UA 取最后一个非空值。单写入方（audit 协程），无需额外锁。
func (s *Store) UpsertAccess(ds []AccessDelta) error {
	if len(ds) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO access_hourly(hour,ip,topic,n,page,s2,s3,s4,s5,first_t,last_t,ua)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(hour,ip,topic) DO UPDATE SET
		  n=n+excluded.n, page=page+excluded.page,
		  s2=s2+excluded.s2, s3=s3+excluded.s3, s4=s4+excluded.s4, s5=s5+excluded.s5,
		  first_t=CASE
		    WHEN first_t=0 THEN excluded.first_t
		    WHEN excluded.first_t=0 THEN first_t
		    WHEN excluded.first_t<first_t THEN excluded.first_t
		    ELSE first_t END,
		  last_t=CASE WHEN excluded.last_t>last_t THEN excluded.last_t ELSE last_t END,
		  ua=CASE WHEN excluded.ua<>'' THEN excluded.ua ELSE ua END`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, d := range ds {
		if _, err := stmt.Exec(d.Hour, d.IP, d.Topic, d.N, d.Page, d.S2, d.S3, d.S4, d.S5, d.FirstT, d.LastT, d.UA); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AccessRow 是按 IP 汇总后的访问记录（供 /api/visitors）。
type AccessRow struct {
	IP             string
	N, Page        int64
	S2, S3, S4, S5 int64
	First, Last    int64
	UA             string
	Topics         map[string]int64
}

// QueryAccess 汇总 from（含）之后的访问记录，按最近访问时间倒序。
func (s *Store) QueryAccess(from int64) ([]AccessRow, error) {
	rows, err := s.db.Query(`SELECT ip, SUM(n), SUM(page), SUM(s2), SUM(s3), SUM(s4), SUM(s5),
		MIN(CASE WHEN first_t>0 THEN first_t END), MAX(last_t), MAX(ua)
		FROM access_hourly WHERE hour>=? GROUP BY ip ORDER BY MAX(last_t) DESC`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessRow{}
	idx := map[string]int{}
	for rows.Next() {
		var r AccessRow
		var first sql.NullInt64
		if err := rows.Scan(&r.IP, &r.N, &r.Page, &r.S2, &r.S3, &r.S4, &r.S5, &first, &r.Last, &r.UA); err != nil {
			return nil, err
		}
		if first.Valid {
			r.First = first.Int64
		}
		r.Topics = map[string]int64{}
		idx[r.IP] = len(out)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	trows, err := s.db.Query(`SELECT ip, topic, SUM(n) FROM access_hourly
		WHERE hour>=? GROUP BY ip, topic`, from)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var ip, topic string
		var n int64
		if err := trows.Scan(&ip, &topic, &n); err != nil {
			return nil, err
		}
		if i, ok := idx[ip]; ok {
			out[i].Topics[topic] = n
		}
	}
	return out, trows.Err()
}

// KnownMetrics 返回已有数据的指标名（供前端指标选择与估算器）。
func (s *Store) KnownMetrics() []string {
	rows, err := s.db.Query(`SELECT name FROM metrics ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	return out
}

// GetMeta / SetMeta 读写键值配置（采集间隔等，重启后恢复）。
func (s *Store) GetMeta(key string) (string, bool) {
	var v string
	err := s.db.QueryRow(`SELECT val FROM meta WHERE key=?`, key).Scan(&v)
	if err != nil {
		return "", false
	}
	return v, true
}

func (s *Store) SetMeta(key, val string) error {
	_, err := s.db.Exec(`INSERT INTO meta(key,val) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET val=excluded.val`, key, val)
	return err
}

// Retain 滚动删除各层过期数据；保留天数可由 meta 覆盖（day_l0/l1/l2）。
func (s *Store) Retain() (string, error) {
	now := nowUnix()
	ret := func(key string, def int64) int64 {
		if v, ok := s.GetMeta(key); ok {
			if d, err := strconv.ParseInt(v, 10, 64); err == nil && d >= 1 && d <= 3650 {
				return d * 86400
			}
		}
		return def
	}
	var sb strings.Builder
	for _, l := range []struct {
		name string
		def  int64
	}{
		{"l0", RetainL0}, {"l1", RetainL1}, {"l2", RetainL2},
	} {
		cutoff := now - ret("retain_"+l.name, l.def)
		res, err := s.db.Exec(`DELETE FROM points_`+l.name+` WHERE t < ?`, cutoff)
		if err != nil {
			return sb.String(), err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			fmt.Fprintf(&sb, "%s:%d ", l.name, n)
		}
	}
	// 访问记录保留期（默认 90 天，meta access_retain_days 可覆盖）
	ad := int64(90)
	if v, ok := s.GetMeta("access_retain_days"); ok {
		if d, err := strconv.ParseInt(v, 10, 64); err == nil && d >= 1 && d <= 3650 {
			ad = d
		}
	}
	cutoff := now - ad*86400
	if res, err := s.db.Exec(`DELETE FROM access_hourly WHERE hour < ?`, cutoff); err != nil {
		return sb.String(), err
	} else if n, _ := res.RowsAffected(); n > 0 {
		fmt.Fprintf(&sb, "access:%d ", n)
	}
	return sb.String(), nil
}

// Downsample 增量聚合 L0→L1（5 分钟桶）、L1→L2（1 小时桶），
// 聚合位置持久化在 meta（ds_l1_pos / ds_l2_pos），重启续跑。
func (s *Store) Downsample() error {
	now := nowUnix()
	steps := []struct {
		dst, src, posKey string
		bucket           int64
		lag              int64 // 只聚合到 now-lag，避免与实时写入竞争
		defPos           int64
	}{
		{"points_l1", "points_l0", "ds_l1_pos", 300, 3600, 0},
		{"points_l2", "points_l1", "ds_l2_pos", 3600, 86400, 0},
	}
	for _, st := range steps {
		pos := int64(0)
		if v, ok := s.GetMeta(st.posKey); ok {
			if p, err := strconv.ParseInt(v, 10, 64); err == nil {
				pos = p
			}
		}
		end := now - st.lag
		if end <= pos {
			continue
		}
		if _, err := s.db.Exec(`INSERT OR REPLACE INTO `+st.dst+`(t,k,v)
			SELECT (t/?)*?, k, AVG(v) FROM `+st.src+`
			WHERE t >= ? AND t < ? GROUP BY k, (t/?)*?`,
			st.bucket, st.bucket, pos, end, st.bucket, st.bucket); err != nil {
			return err
		}
		if err := s.SetMeta(st.posKey, strconv.FormatInt(end, 10)); err != nil {
			return err
		}
	}
	return nil
}

// Stats 返回库文件大小与各层行数（存储估算器用）。
func (s *Store) Stats(path string) map[string]any {
	out := map[string]any{"dbBytes": 0, "rows": map[string]int64{}}
	if fi, err := os.Stat(path); err == nil {
		out["dbBytes"] = fi.Size()
	}
	for _, l := range []string{"l0", "l1", "l2"} {
		var n int64
		s.db.QueryRow(`SELECT COUNT(*) FROM points_` + l).Scan(&n)
		out["rows"].(map[string]int64)[l] = n
	}
	return out
}
