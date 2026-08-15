package store

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// MarketDB 全市场行情数据库（SQLite 只读）
type MarketDB struct {
	db   *sql.DB
	mu   sync.RWMutex
	path string
}

// MarketSymbol 标的信息
type MarketSymbol struct {
	Symbol    string
	Code      string
	Name      string
	Market    string // CN, HK, US, CRYPTO
	Exchange  string
	AssetType string
}

// MarketQuoteRecord 行情记录
type MarketQuoteRecord struct {
	Symbol    string
	Name      string
	Market    string
	TradeDate string
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	Amount    float64
	ChangePct float64
	ChangeAmt float64
	Turnover  float64
	Amplitude float64
	MarketCap float64
	PERatio   float64
	PBRatio   float64
}

// NewMarketDB 打开行情数据库
func NewMarketDB(dbPath string) (*MarketDB, error) {
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open market db: %w", err)
	}

	// 测试连接
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping market db: %w", err)
	}

	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	return &MarketDB{db: db, path: dbPath}, nil
}

// Close 关闭数据库
func (m *MarketDB) Close() error {
	if m.db != nil {
		return m.db.Close()
	}
	return nil
}

// SearchSymbol 搜索标的（按名称/代码/symbol 模糊匹配）
// 返回最多 limit 个结果，按匹配精度排序
func (m *MarketDB) SearchSymbol(query string, limit int) ([]*MarketSymbol, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	upper := strings.ToUpper(query)

	// 1. 精确匹配 symbol
	rows, err := m.db.Query(
		"SELECT symbol, code, name, market, exchange, asset_type FROM symbols WHERE UPPER(symbol) = ? LIMIT 1",
		upper,
	)
	if err == nil {
		results := scanSymbols(rows)
		if len(results) > 0 {
			return results, nil
		}
	}

	// 2. 精确匹配 code
	rows, err = m.db.Query(
		"SELECT symbol, code, name, market, exchange, asset_type FROM symbols WHERE UPPER(code) = ? LIMIT ?",
		upper, limit,
	)
	if err == nil {
		results := scanSymbols(rows)
		if len(results) > 0 {
			return results, nil
		}
	}

	// 3. 精确匹配名称
	rows, err = m.db.Query(
		"SELECT symbol, code, name, market, exchange, asset_type FROM symbols WHERE name = ? LIMIT 1",
		query,
	)
	if err == nil {
		results := scanSymbols(rows)
		if len(results) > 0 {
			return results, nil
		}
	}

	// 4. 名称前缀匹配
	rows, err = m.db.Query(
		"SELECT symbol, code, name, market, exchange, asset_type FROM symbols WHERE name LIKE ? ORDER BY LENGTH(name) ASC LIMIT ?",
		query+"%", limit,
	)
	if err == nil {
		results := scanSymbols(rows)
		if len(results) > 0 {
			return results, nil
		}
	}

	// 5. 名称包含匹配
	rows, err = m.db.Query(
		"SELECT symbol, code, name, market, exchange, asset_type FROM symbols WHERE name LIKE ? ORDER BY LENGTH(name) ASC LIMIT ?",
		"%"+query+"%", limit,
	)
	if err == nil {
		results := scanSymbols(rows)
		if len(results) > 0 {
			return results, nil
		}
	}

	// 6. symbol/code 包含匹配
	rows, err = m.db.Query(
		"SELECT symbol, code, name, market, exchange, asset_type FROM symbols WHERE UPPER(symbol) LIKE ? OR UPPER(code) LIKE ? LIMIT ?",
		"%"+upper+"%", "%"+upper+"%", limit,
	)
	if err == nil {
		results := scanSymbols(rows)
		if len(results) > 0 {
			return results, nil
		}
	}

	return nil, nil
}

// GetQuote 获取标的最新行情（从库内）
func (m *MarketDB) GetQuote(symbol string) (*MarketQuoteRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	row := m.db.QueryRow(`
		SELECT s.symbol, s.name, s.market,
		       q.trade_date, q.open, q.high, q.low, q.close,
		       q.volume, q.amount, q.change_pct, q.change_amt,
		       q.turnover, q.amplitude, q.market_cap, q.pe_ratio, q.pb_ratio
		FROM symbols s
		JOIN daily_quotes q ON s.symbol = q.symbol
		WHERE s.symbol = ?
		ORDER BY q.trade_date DESC
		LIMIT 1
	`, symbol)

	return scanQuoteRecord(row)
}

// GetQuoteByName 按名称获取最新行情
func (m *MarketDB) GetQuoteByName(name string) (*MarketQuoteRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	row := m.db.QueryRow(`
		SELECT s.symbol, s.name, s.market,
		       q.trade_date, q.open, q.high, q.low, q.close,
		       q.volume, q.amount, q.change_pct, q.change_amt,
		       q.turnover, q.amplitude, q.market_cap, q.pe_ratio, q.pb_ratio
		FROM symbols s
		JOIN daily_quotes q ON s.symbol = q.symbol
		WHERE s.name = ?
		ORDER BY q.trade_date DESC
		LIMIT 1
	`, name)

	return scanQuoteRecord(row)
}

// GetHistory 获取历史行情
func (m *MarketDB) GetHistory(symbol string, days int) ([]*MarketQuoteRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if days <= 0 {
		days = 30
	}

	rows, err := m.db.Query(`
		SELECT s.symbol, s.name, s.market,
		       q.trade_date, q.open, q.high, q.low, q.close,
		       q.volume, q.amount, q.change_pct, q.change_amt,
		       q.turnover, q.amplitude, q.market_cap, q.pe_ratio, q.pb_ratio
		FROM symbols s
		JOIN daily_quotes q ON s.symbol = q.symbol
		WHERE s.symbol = ?
		ORDER BY q.trade_date DESC
		LIMIT ?
	`, symbol, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*MarketQuoteRecord
	for rows.Next() {
		r, err := scanQuoteRecordFromRows(rows)
		if err != nil {
			continue
		}
		records = append(records, r)
	}
	return records, nil
}

// SymbolCount 返回标的总数
func (m *MarketDB) SymbolCount() int {
	var count int
	m.db.QueryRow("SELECT COUNT(*) FROM symbols").Scan(&count)
	return count
}

// IsAvailable 检查数据库是否可用
func (m *MarketDB) IsAvailable() bool {
	return m.db != nil && m.db.Ping() == nil
}

// ============================================
// 内部工具
// ============================================

func scanSymbols(rows *sql.Rows) []*MarketSymbol {
	if rows == nil {
		return nil
	}
	defer rows.Close()

	var results []*MarketSymbol
	for rows.Next() {
		s := &MarketSymbol{}
		if err := rows.Scan(&s.Symbol, &s.Code, &s.Name, &s.Market, &s.Exchange, &s.AssetType); err != nil {
			continue
		}
		results = append(results, s)
	}
	return results
}

func scanQuoteRecord(row *sql.Row) (*MarketQuoteRecord, error) {
	r := &MarketQuoteRecord{}
	var (
		open, high, low, close       sql.NullFloat64
		volume, amount               sql.NullFloat64
		changePct, changeAmt         sql.NullFloat64
		turnover, amplitude          sql.NullFloat64
		marketCap, peRatio, pbRatio  sql.NullFloat64
	)

	err := row.Scan(
		&r.Symbol, &r.Name, &r.Market,
		&r.TradeDate, &open, &high, &low, &close,
		&volume, &amount, &changePct, &changeAmt,
		&turnover, &amplitude, &marketCap, &peRatio, &pbRatio,
	)
	if err != nil {
		return nil, err
	}

	r.Open = nullFloat(open)
	r.High = nullFloat(high)
	r.Low = nullFloat(low)
	r.Close = nullFloat(close)
	r.Volume = nullFloat(volume)
	r.Amount = nullFloat(amount)
	r.ChangePct = nullFloat(changePct)
	r.ChangeAmt = nullFloat(changeAmt)
	r.Turnover = nullFloat(turnover)
	r.Amplitude = nullFloat(amplitude)
	r.MarketCap = nullFloat(marketCap)
	r.PERatio = nullFloat(peRatio)
	r.PBRatio = nullFloat(pbRatio)

	return r, nil
}

func scanQuoteRecordFromRows(rows *sql.Rows) (*MarketQuoteRecord, error) {
	r := &MarketQuoteRecord{}
	var (
		open, high, low, close       sql.NullFloat64
		volume, amount               sql.NullFloat64
		changePct, changeAmt         sql.NullFloat64
		turnover, amplitude          sql.NullFloat64
		marketCap, peRatio, pbRatio  sql.NullFloat64
	)

	err := rows.Scan(
		&r.Symbol, &r.Name, &r.Market,
		&r.TradeDate, &open, &high, &low, &close,
		&volume, &amount, &changePct, &changeAmt,
		&turnover, &amplitude, &marketCap, &peRatio, &pbRatio,
	)
	if err != nil {
		return nil, err
	}

	r.Open = nullFloat(open)
	r.High = nullFloat(high)
	r.Low = nullFloat(low)
	r.Close = nullFloat(close)
	r.Volume = nullFloat(volume)
	r.Amount = nullFloat(amount)
	r.ChangePct = nullFloat(changePct)
	r.ChangeAmt = nullFloat(changeAmt)
	r.Turnover = nullFloat(turnover)
	r.Amplitude = nullFloat(amplitude)
	r.MarketCap = nullFloat(marketCap)
	r.PERatio = nullFloat(peRatio)
	r.PBRatio = nullFloat(pbRatio)

	return r, nil
}

func nullFloat(nf sql.NullFloat64) float64 {
	if nf.Valid {
		return nf.Float64
	}
	return 0
}
