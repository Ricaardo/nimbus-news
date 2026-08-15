package backtest

import "time"

// Config 回测配置
type Config struct {
	InvestorID     string        // 投资人ID: "buffett", "soros"等
	Symbol         string        // 交易标的: "BTC", "ETH"等
	StartDate      time.Time     // 回测开始日期
	EndDate        time.Time     // 回测结束日期
	InitialCapital float64       // 初始资金
	CommissionRate float64       // 手续费率 (默认0.001)
	UseAI          bool          // 是否使用AI决策（调用LLM）
	DataSource     string        // 数据源: "api" 或 "local"
}
