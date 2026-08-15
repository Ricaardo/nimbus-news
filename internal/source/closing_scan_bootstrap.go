package source

import (
	"database/sql"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ClosingScanBootstrapOptions 自检 + 回灌配置
type ClosingScanBootstrapOptions struct {
	DBPath     string // 默认 data/cache/closing_scan.db
	ScriptPath string // 默认 scripts/closing_scan_backfill.py
	PythonCmd  string // 默认 python3
	MinSamples int    // 最小样本阈值, 默认 100
	Days       int    // 回灌天数, 默认 60
}

func (o *ClosingScanBootstrapOptions) applyDefaults() {
	if o.DBPath == "" {
		o.DBPath = "data/cache/closing_scan.db"
	}
	if o.ScriptPath == "" {
		o.ScriptPath = "scripts/closing_scan_backfill.py"
	}
	if o.PythonCmd == "" {
		o.PythonCmd = "python3"
	}
	if o.MinSamples == 0 {
		o.MinSamples = 100
	}
	if o.Days == 0 {
		o.Days = 60
	}
}

var closingScanBootstrapOnce sync.Once

// BootstrapClosingScanDB 检查 closing_scan.db 样本数, 不足则异步跑 backfill
// 幂等, 多次调用只会触发一次 bootstrap
func BootstrapClosingScanDB(opts ClosingScanBootstrapOptions) {
	opts.applyDefaults()
	closingScanBootstrapOnce.Do(func() {
		go runBootstrap(opts)
	})
}

func runBootstrap(opts ClosingScanBootstrapOptions) {
	samples := countSamples(opts.DBPath)
	if samples >= opts.MinSamples {
		slog.Info("closing_scan.db ok", "samples", samples)
		return
	}

	slog.Warn("closing_scan.db under-populated, triggering backfill",
		"current_samples", samples, "threshold", opts.MinSamples,
		"days", opts.Days, "eta", "~5min")

	// 确保 scripts 路径可访问
	if _, err := os.Stat(opts.ScriptPath); err != nil {
		slog.Error("closing_scan_backfill.py not found",
			"path", opts.ScriptPath, "error", err)
		return
	}

	// 同步 spawn, 但 BootstrapClosingScanDB 本身在 goroutine 里, 不阻塞启动
	start := time.Now()
	cmd := exec.Command(opts.PythonCmd, opts.ScriptPath,
		"--days", itoa(opts.Days), "--json-only")
	cmd.Dir = projectRoot(opts.ScriptPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("closing_scan backfill failed",
			"error", err, "output", truncate(string(output), 500))
		return
	}

	final := countSamples(opts.DBPath)
	slog.Info("closing_scan backfill completed",
		"duration", time.Since(start).String(),
		"samples_before", samples, "samples_after", final)
}

// countSamples 查 limit_up_daily JOIN next_day_perf 记录数
func countSamples(dbPath string) int {
	if _, err := os.Stat(dbPath); err != nil {
		return 0
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return 0
	}
	defer db.Close()

	var n int
	row := db.QueryRow(`
		SELECT COUNT(*) FROM limit_up_daily l
		JOIN next_day_perf p ON l.date=p.date AND l.code=p.code
	`)
	if err := row.Scan(&n); err != nil {
		return 0
	}
	return n
}

// loadRecentStreaks 读最近 N 个交易日的涨停股 code → streak 映射
// 用于打板扫描的 Continuation 维度 (F2)
// 若同一只股多日出现, 取最高 streak
func loadRecentStreaks(dbPath string, days int) map[string]int {
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	// 取最近 N 个不同的日期
	rows, err := db.Query(`
		SELECT code, streak FROM limit_up_daily
		WHERE date IN (
			SELECT DISTINCT date FROM limit_up_daily
			ORDER BY date DESC LIMIT ?
		)
	`, days)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var code string
		var streak int
		if err := rows.Scan(&code, &streak); err != nil {
			continue
		}
		if streak > out[code] {
			out[code] = streak
		}
	}
	return out
}

// projectRoot 根据 scriptPath 推断项目根, 若无则返回空
func projectRoot(scriptPath string) string {
	abs, err := filepath.Abs(scriptPath)
	if err != nil {
		return ""
	}
	// scripts/closing_scan_backfill.py → 项目根目录
	return filepath.Dir(filepath.Dir(abs))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [12]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
