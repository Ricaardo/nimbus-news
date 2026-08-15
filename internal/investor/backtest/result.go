package backtest

import (
	"encoding/json"
	"fmt"
	"time"
)

// Trade 交易记录
type Trade struct {
	Date        time.Time `json:"date"`         // 交易日期
	Action      string    `json:"action"`        // 买入/卖出/持有
	Price       float64   `json:"price"`         // 成交价格
	Quantity    float64   `json:"quantity"`      // 成交数量
	Amount      float64   `json:"amount"`        // 成交金额
	Commission  float64   `json:"commission"`   // 手续费
	Position    float64   `json:"position"`      // 交易后持仓
	Cash        float64   `json:"cash"`          // 交易后现金
	TotalAsset  float64   `json:"total_asset"`   // 总资产
	Reason      string    `json:"reason"`        // 交易理由
	Confidence  float64   `json:"confidence"`    // 信心度
	RiskLevel   string    `json:"risk_level"`    // 风险等级
}

// DailyRecord 每日资产记录
type DailyRecord struct {
	Date        time.Time `json:"date"`         // 日期
	Price       float64   `json:"price"`         // 当日收盘价
	Position    float64   `json:"position"`     // 持仓数量
	Cash        float64   `json:"cash"`          // 现金
	TotalAsset  float64   `json:"total_asset"`   // 总资产
	Return      float64   `json:"return"`        // 当日收益率
	AccumReturn float64   `json:"accum_return"`  // 累计收益率
}

// Result 回测结果
type Result struct {
	InvestorID     string         `json:"investor_id"`     // 投资人ID
	Symbol         string         `json:"symbol"`         // 交易标的
	StartDate      time.Time      `json:"start_date"`     // 回测开始日期
	EndDate        time.Time      `json:"end_date"`       // 回测结束日期
	InitialCapital float64        `json:"initial_capital"` // 初始资金
	FinalCapital   float64        `json:"final_capital"`  // 最终资金

	// 收益指标
	TotalReturn    float64 `json:"total_return"`    // 总收益率
	AnnualReturn   float64 `json:"annual_return"`   // 年化收益率
	BestDayReturn  float64 `json:"best_day_return"` // 单日最大涨幅
	WorstDayReturn float64 `json:"worst_day_return"` // 单日最大跌幅

	// 风险指标
	MaxDrawdown float64 `json:"max_drawdown"` // 最大回撤
	Volatility  float64 `json:"volatility"`   // 波动率
	SharpeRatio float64 `json:"sharpe_ratio"` // 夏普比率
	WinRate     float64 `json:"win_rate"`     // 胜率

	// 交易统计
	TotalTrades   int     `json:"total_trades"`  // 总交易次数
	WinningTrades int     `json:"winning_trades"` // 盈利交易次数
	LosingTrades  int     `json:"losing_trades"`  // 亏损交易次数
	AvgWin        float64 `json:"avg_win"`       // 平均盈利
	AvgLoss       float64 `json:"avg_loss"`      // 平均亏损

	// 详细记录
	Trades      []*Trade      `json:"trades"`      // 交易记录
	DailyRecord []*DailyRecord `json:"daily_record"` // 每日资产记录

	// 执行信息
	StartPrice  float64 `json:"start_price"`  // 起始价格
	EndPrice    float64 `json:"end_price"`   // 结束价格
	RunDuration int64   `json:"run_duration"` // 运行耗时(毫秒)
}

// NewResult 创建回测结果
func NewResult(cfg *Config) *Result {
	return &Result{
		InvestorID:     cfg.InvestorID,
		Symbol:         cfg.Symbol,
		StartDate:      cfg.StartDate,
		EndDate:        cfg.EndDate,
		InitialCapital:  cfg.InitialCapital,
		TotalTrades:    0,
		Trades:         make([]*Trade, 0),
		DailyRecord:    make([]*DailyRecord, 0),
	}
}

// AddTrade 添加交易记录
func (r *Result) AddTrade(trade *Trade) {
	r.Trades = append(r.Trades, trade)
}

// AddDailyRecord 添加每日记录
func (r *Result) AddDailyRecord(record *DailyRecord) {
	r.DailyRecord = append(r.DailyRecord, record)
}

// CalculateMetrics 计算指标
func (r *Result) CalculateMetrics() {
	if len(r.DailyRecord) == 0 {
		return
	}

	// 最终资产
	r.FinalCapital = r.DailyRecord[len(r.DailyRecord)-1].TotalAsset

	// 总收益率
	r.TotalReturn = (r.FinalCapital - r.InitialCapital) / r.InitialCapital

	// 计算年化收益率
	days := float64(r.EndDate.Sub(r.StartDate).Hours() / 24)
	if days > 0 {
		r.AnnualReturn = (1 + r.TotalReturn) * (365 / days)
	}

	// 计算最大回撤
	var maxAsset float64 = r.InitialCapital
	r.MaxDrawdown = 0
	for _, record := range r.DailyRecord {
		if record.TotalAsset > maxAsset {
			maxAsset = record.TotalAsset
		}
		drawdown := (maxAsset - record.TotalAsset) / maxAsset
		if drawdown > r.MaxDrawdown {
			r.MaxDrawdown = drawdown
		}
	}

	// 计算波动率
	if len(r.DailyRecord) > 1 {
		var sumReturn, sumSqReturn float64
		for i := 1; i < len(r.DailyRecord); i++ {
			dayReturn := r.DailyRecord[i].Return
			sumReturn += dayReturn
			sumSqReturn += dayReturn * dayReturn
		}
		avgReturn := sumReturn / float64(len(r.DailyRecord)-1)
		variance := (sumSqReturn / float64(len(r.DailyRecord)-1)) - (avgReturn * avgReturn)
		r.Volatility = variance * 100
	}

	// 计算夏普比率 (假设无风险利率为3%)
	if r.Volatility > 0 {
		r.SharpeRatio = (r.AnnualReturn - 0.03) / r.Volatility
	}

	// 计算胜率
	if r.TotalTrades > 0 {
		r.WinRate = float64(r.WinningTrades) / float64(r.TotalTrades)
	}

	// 单日最大涨跌幅
	r.BestDayReturn = -999
	r.WorstDayReturn = 999
	for _, record := range r.DailyRecord {
		if record.Return > r.BestDayReturn {
			r.BestDayReturn = record.Return
		}
		if record.Return < r.WorstDayReturn {
			r.WorstDayReturn = record.Return
		}
	}
}

// ToJSON 序列化为JSON
func (r *Result) ToJSON() (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Summary 回测结果摘要
func (r *Result) Summary() string {
	return fmt.Sprintf(`## 回测结果

| 指标 | 值 |
|------|-----|
| 投资人 | %s |
| 标的 | %s |
| 回测期间 | %s ~ %s |
| 初始资金 | ¥%.2f |
| 最终资金 | ¥%.2f |
| 总收益率 | %.2f%% |
| 年化收益率 | %.2f%% |
| 最大回撤 | %.2f%% |
| 波动率 | %.2f%% |
| 夏普比率 | %.2f |
| 交易次数 | %d |
| 胜率 | %.2f%% |
`,
		r.InvestorID,
		r.Symbol,
		r.StartDate.Format("2006-01-02"),
		r.EndDate.Format("2006-01-02"),
		r.InitialCapital,
		r.FinalCapital,
		r.TotalReturn*100,
		r.AnnualReturn*100,
		r.MaxDrawdown*100,
		r.Volatility,
		r.SharpeRatio,
		r.TotalTrades,
		r.WinRate*100,
	)
}
