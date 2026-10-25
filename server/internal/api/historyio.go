package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// historyExport GET：VACUUM INTO 生成数据库快照（配置+全部历史），下载后删除。
func (s *Server) historyExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
		return
	}
	dir := filepath.Dir(s.opt.DbPath)
	tmp := filepath.Join(dir, "guanlan-export.tmp.db")
	os.Remove(tmp)
	if _, err := s.st.DB().Exec(`VACUUM INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		http.Error(w, `{"error":"export_failed"}`, http.StatusInternalServerError)
		return
	}
	f, err := os.Open(tmp)
	if err != nil {
		os.Remove(tmp)
		http.Error(w, `{"error":"export_failed"}`, http.StatusInternalServerError)
		return
	}
	defer f.Close()
	defer os.Remove(tmp)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="guanlan-history.db"`)
	io.Copy(w, f)
}

// historyImport POST（body = 导出的 db 文件字节）：校验与当前库序列集合一致
// （"配置完全一样的机器"），一致则把三层时序数据合并进当前库。
// ?dryRun=1 只做校验与统计、不写库（前端用于先给用户预览确认，防误导入）。
func (s *Server) historyImport(w http.ResponseWriter, r *http.Request) {
	dryRun := r.URL.Query().Get("dryRun") == "1"
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method"}`, http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("X-Requested-With") != "guanlan" {
		http.Error(w, `{"error":"csrf"}`, http.StatusForbidden)
		return
	}
	dir := filepath.Dir(s.opt.DbPath)
	tmp := filepath.Join(dir, "guanlan-import.tmp.db")
	defer os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		http.Error(w, `{"error":"tmp_failed"}`, http.StatusInternalServerError)
		return
	}
	// 上限 1GB（30 天×细粒度历史 + 冗余）
	if _, err := io.Copy(out, io.LimitReader(r.Body, 1<<30)); err != nil {
		out.Close()
		http.Error(w, `{"error":"read_failed"}`, http.StatusBadRequest)
		return
	}
	out.Close()

	db := s.st.DB()
	// 只读挂载校验：必须是本应用导出的库，且序列集合与当前库一致（同配置机器）
	if _, err := db.Exec(`ATTACH DATABASE ? AS src_import`, tmp); err != nil {
		http.Error(w, `{"error":"not_a_database"}`, http.StatusBadRequest)
		return
	}
	bad := func(code string, status int) {
		db.Exec(`DETACH DATABASE src_import`)
		http.Error(w, `{"error":"`+code+`"}`, status)
	}
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM src_import.sqlite_master
		WHERE type='table' AND name IN ('metrics','points_l0','points_l1','points_l2')`).Scan(&cnt); err != nil || cnt != 4 {
		bad("not_guanlan_backup", http.StatusBadRequest)
		return
	}
	// 序列集合必须与当前库完全一致（导入只支持同配置机器导出的文件）
	var onlySrc, onlyDst int
	if err := db.QueryRow(`SELECT COUNT(*) FROM src_import.metrics
		WHERE name NOT IN (SELECT name FROM main.metrics)`).Scan(&onlySrc); err != nil {
		bad("verify_failed", http.StatusBadRequest)
		return
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM main.metrics
		WHERE name NOT IN (SELECT name FROM src_import.metrics)`).Scan(&onlyDst); err != nil {
		bad("verify_failed", http.StatusBadRequest)
		return
	}
	if onlySrc > 0 || onlyDst > 0 {
		bad("metrics_mismatch", http.StatusUnprocessableEntity)
		return
	}

	// 统计源库三层行数（dryRun 返回给前端做导入前预览）
	var l0n, l1n, l2n int64
	db.QueryRow(`SELECT COUNT(*) FROM src_import.points_l0`).Scan(&l0n)
	db.QueryRow(`SELECT COUNT(*) FROM src_import.points_l1`).Scan(&l1n)
	db.QueryRow(`SELECT COUNT(*) FROM src_import.points_l2`).Scan(&l2n)
	if dryRun {
		db.Exec(`DETACH DATABASE src_import`)
		writeJSON(w, map[string]any{
			"ok": true, "l0": l0n, "l1": l1n, "l2": l2n, "total": l0n + l1n + l2n,
		})
		return
	}

	// 合并三层时序（按主键幂等覆盖），不动当前配置（meta）
	for _, t := range []string{"points_l0", "points_l1", "points_l2"} {
		if _, err := db.Exec(`INSERT OR REPLACE INTO main.` + t + `
			SELECT s.t, m.id, s.v FROM src_import.` + t + ` s
			JOIN main.metrics m ON m.name = (SELECT name FROM src_import.metrics WHERE id = s.k)`); err != nil {
			bad("merge_failed", http.StatusInternalServerError)
			return
		}
	}
	db.Exec(`DETACH DATABASE src_import`)
	// 降采样游标失效：清零后按保留窗口重新聚合
	s.st.SetMeta("ds_l1_pos", "0")
	s.st.SetMeta("ds_l2_pos", "0")

	var rows int64
	db.QueryRow(`SELECT (SELECT COUNT(*) FROM points_l0) + (SELECT COUNT(*) FROM points_l1) + (SELECT COUNT(*) FROM points_l2)`).Scan(&rows)
	writeJSON(w, map[string]any{"imported": true, "totalRows": rows})
}
