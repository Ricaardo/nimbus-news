package investor

// ActionType 操作类型
type ActionType string

const (
	ActionBuy   ActionType = "买入"
	ActionSell  ActionType = "卖出"
	ActionHold  ActionType = "持有"
)

// RiskLevel 风险等级
type RiskLevel string

const (
	RiskLow    RiskLevel = "低"
	RiskMedium RiskLevel = "中"
	RiskHigh   RiskLevel = "高"
)

// InvestmentDecision 投资决策结果
type InvestmentDecision struct {
	Action       ActionType // 操作: 买入/卖出/持有
	Quantity     float64    // 建议数量/仓位
	Reason       string     // 决策理由
	Confidence   float64    // 信心度 0-100
	RiskLevel    RiskLevel  // 风险等级: 低/中/高
	HoldingDays  int        // 建议持有天数
	StopLoss     float64    // 止损线
	TakeProfit   float64    // 止盈线
}

// ParseDecision 从LLM响应解析决策
func ParseDecision(response string) (*InvestmentDecision, error) {
	// 简单解析JSON响应
	// 这里可以添加更复杂的JSON解析逻辑

	// 默认持有决策
	return &InvestmentDecision{
		Action:      ActionHold,
		Quantity:    0,
		Reason:      "等待分析",
		Confidence:  50,
		RiskLevel:   RiskMedium,
		HoldingDays: 7,
	}, nil
}
