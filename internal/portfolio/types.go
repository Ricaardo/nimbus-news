package portfolio

import (
	"time"
)

// Position 持仓记录
type Position struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	Symbol       string    `json:"symbol"`         // 标的代码
	Name         string    `json:"name"`           // 标的名称
	Quantity     float64   `json:"quantity"`       // 持仓数量
	AvgCost      float64   `json:"avg_cost"`       // 平均成本
	CurrentPrice float64   `json:"current_price"`  // 当前价格（实时更新）
	MarketValue  float64   `json:"market_value"`   // 市值
	Profit       float64   `json:"profit"`         // 盈亏金额
	ProfitPct    float64   `json:"profit_pct"`     // 盈亏百分比
	Currency     string    `json:"currency"`       // 币种
	AssetType    string    `json:"asset_type"`     // 资产类型: stock/crypto/etf/index
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Transaction 交易记录
type Transaction struct {
	ID        string          `json:"id"`
	UserID    string          `json:"user_id"`
	Symbol    string          `json:"symbol"`
	Type      TransactionType `json:"type"`       // buy/sell
	Quantity  float64         `json:"quantity"`   // 数量
	Price     float64         `json:"price"`      // 成交价格
	Fee       float64         `json:"fee"`        // 手续费
	Notes     string          `json:"notes"`      // 备注
	CreatedAt time.Time       `json:"created_at"`
}

// TransactionType 交易类型
type TransactionType string

const (
	TransactionTypeBuy  TransactionType = "buy"
	TransactionTypeSell TransactionType = "sell"
)

// Portfolio 投资组合
type Portfolio struct {
	UserID       string      `json:"user_id"`
	Positions    []*Position `json:"positions"`
	TotalValue   float64     `json:"total_value"`    // 总市值
	TotalCost    float64     `json:"total_cost"`     // 总成本
	TotalProfit  float64     `json:"total_profit"`   // 总盈亏
	TotalProfitPct float64   `json:"total_profit_pct"` // 总盈亏百分比
	DayChange    float64     `json:"day_change"`     // 日涨跌
	DayChangePct float64     `json:"day_change_pct"` // 日涨跌百分比
	UpdatedAt    time.Time   `json:"updated_at"`
}

// PortfolioSummary 投资组合摘要
type PortfolioSummary struct {
	TotalPositions int                `json:"total_positions"`
	TotalValue     float64            `json:"total_value"`
	TotalCost      float64            `json:"total_cost"`
	TotalProfit    float64            `json:"total_profit"`
	TotalProfitPct float64            `json:"total_profit_pct"`
	ByAssetType    map[string]float64 `json:"by_asset_type"` // 按资产类型分布
	TopGainers     []*Position        `json:"top_gainers"`   // 涨幅最大
	TopLosers      []*Position        `json:"top_losers"`    // 跌幅最大
}

// CalculateProfit 计算持仓盈亏
func (p *Position) CalculateProfit() {
	p.MarketValue = p.Quantity * p.CurrentPrice
	totalCost := p.Quantity * p.AvgCost
	p.Profit = p.MarketValue - totalCost
	if totalCost > 0 {
		p.ProfitPct = (p.Profit / totalCost) * 100
	}
}

// NewPosition 创建新持仓
func NewPosition(userID, symbol string, quantity, price float64) *Position {
	now := time.Now()
	return &Position{
		ID:        generateID(),
		UserID:    userID,
		Symbol:    symbol,
		Quantity:  quantity,
		AvgCost:   price,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// AddQuantity 加仓（更新平均成本）
func (p *Position) AddQuantity(quantity, price float64) {
	totalCost := p.Quantity*p.AvgCost + quantity*price
	p.Quantity += quantity
	if p.Quantity > 0 {
		p.AvgCost = totalCost / p.Quantity
	}
	p.UpdatedAt = time.Now()
}

// ReduceQuantity 减仓
func (p *Position) ReduceQuantity(quantity float64) error {
	if quantity > p.Quantity {
		return ErrInsufficientQuantity
	}
	p.Quantity -= quantity
	p.UpdatedAt = time.Now()
	return nil
}

// NewTransaction 创建交易记录
func NewTransaction(userID, symbol string, txType TransactionType, quantity, price float64) *Transaction {
	return &Transaction{
		ID:        generateID(),
		UserID:    userID,
		Symbol:    symbol,
		Type:      txType,
		Quantity:  quantity,
		Price:     price,
		CreatedAt: time.Now(),
	}
}

// 生成唯一 ID
func generateID() string {
	return time.Now().Format("20060102150405") + "_" + randomString(6)
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[time.Now().UnixNano()%int64(len(letters))]
		time.Sleep(time.Nanosecond)
	}
	return string(b)
}
