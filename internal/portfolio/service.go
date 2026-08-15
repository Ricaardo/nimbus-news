package portfolio

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// MarketDataProvider 行情数据提供者接口
type MarketDataProvider interface {
	GetPrice(ctx context.Context, symbol string) (float64, error)
}

// Service 投资组合服务
type Service struct {
	store    *Store
	market   MarketDataProvider
}

// NewService 创建投资组合服务
func NewService(store *Store, market MarketDataProvider) *Service {
	return &Service{
		store:  store,
		market: market,
	}
}

// AddPosition 添加/加仓
func (s *Service) AddPosition(ctx context.Context, userID, symbol string, quantity, price float64) (*Position, error) {
	if symbol == "" {
		return nil, ErrSymbolRequired
	}
	if quantity <= 0 {
		return nil, ErrInvalidQuantity
	}
	if price <= 0 {
		return nil, ErrInvalidPrice
	}

	// 查找现有持仓
	pos, err := s.store.GetPosition(ctx, userID, symbol)
	if err == ErrPositionNotFound {
		// 新建持仓
		pos = NewPosition(userID, symbol, quantity, price)
	} else if err != nil {
		return nil, err
	} else {
		// 加仓
		pos.AddQuantity(quantity, price)
	}

	// 获取当前价格
	if s.market != nil {
		if currentPrice, err := s.market.GetPrice(ctx, symbol); err == nil {
			pos.CurrentPrice = currentPrice
			pos.CalculateProfit()
		}
	}

	// 保存
	if err := s.store.SavePosition(ctx, pos); err != nil {
		return nil, err
	}

	// 记录交易
	tx := NewTransaction(userID, symbol, TransactionTypeBuy, quantity, price)
	s.store.SaveTransaction(ctx, tx)

	return pos, nil
}

// ReducePosition 减仓/卖出
func (s *Service) ReducePosition(ctx context.Context, userID, symbol string, quantity, price float64) (*Position, error) {
	if symbol == "" {
		return nil, ErrSymbolRequired
	}
	if quantity <= 0 {
		return nil, ErrInvalidQuantity
	}

	// 查找现有持仓
	pos, err := s.store.GetPosition(ctx, userID, symbol)
	if err != nil {
		return nil, err
	}

	// 减仓
	if err := pos.ReduceQuantity(quantity); err != nil {
		return nil, err
	}

	// 如果全部卖出，删除持仓
	if pos.Quantity <= 0 {
		if err := s.store.DeletePosition(ctx, userID, symbol); err != nil {
			return nil, err
		}
	} else {
		// 更新持仓
		if s.market != nil {
			if currentPrice, err := s.market.GetPrice(ctx, symbol); err == nil {
				pos.CurrentPrice = currentPrice
				pos.CalculateProfit()
			}
		}
		if err := s.store.SavePosition(ctx, pos); err != nil {
			return nil, err
		}
	}

	// 记录交易
	if price <= 0 && s.market != nil {
		price, _ = s.market.GetPrice(ctx, symbol)
	}
	tx := NewTransaction(userID, symbol, TransactionTypeSell, quantity, price)
	s.store.SaveTransaction(ctx, tx)

	return pos, nil
}

// ClearPosition 清仓
func (s *Service) ClearPosition(ctx context.Context, userID, symbol string) error {
	pos, err := s.store.GetPosition(ctx, userID, symbol)
	if err != nil {
		return err
	}

	// 记录交易
	price := pos.CurrentPrice
	if price <= 0 && s.market != nil {
		price, _ = s.market.GetPrice(ctx, symbol)
	}
	tx := NewTransaction(userID, symbol, TransactionTypeSell, pos.Quantity, price)
	s.store.SaveTransaction(ctx, tx)

	return s.store.DeletePosition(ctx, userID, symbol)
}

// GetPortfolio 获取投资组合
func (s *Service) GetPortfolio(ctx context.Context, userID string) (*Portfolio, error) {
	positions, err := s.store.GetUserPositions(ctx, userID)
	if err != nil {
		return nil, err
	}

	portfolio := &Portfolio{
		UserID:    userID,
		Positions: positions,
		UpdatedAt: time.Now(),
	}

	// 更新价格并计算盈亏
	for _, pos := range positions {
		if s.market != nil {
			if currentPrice, err := s.market.GetPrice(ctx, pos.Symbol); err == nil {
				pos.CurrentPrice = currentPrice
			}
		}
		pos.CalculateProfit()

		portfolio.TotalValue += pos.MarketValue
		portfolio.TotalCost += pos.Quantity * pos.AvgCost
	}

	portfolio.TotalProfit = portfolio.TotalValue - portfolio.TotalCost
	if portfolio.TotalCost > 0 {
		portfolio.TotalProfitPct = (portfolio.TotalProfit / portfolio.TotalCost) * 100
	}

	return portfolio, nil
}

// GetSummary 获取投资组合摘要
func (s *Service) GetSummary(ctx context.Context, userID string) (*PortfolioSummary, error) {
	portfolio, err := s.GetPortfolio(ctx, userID)
	if err != nil {
		return nil, err
	}

	summary := &PortfolioSummary{
		TotalPositions: len(portfolio.Positions),
		TotalValue:     portfolio.TotalValue,
		TotalCost:      portfolio.TotalCost,
		TotalProfit:    portfolio.TotalProfit,
		TotalProfitPct: portfolio.TotalProfitPct,
		ByAssetType:    make(map[string]float64),
	}

	// 按资产类型分组
	for _, pos := range portfolio.Positions {
		assetType := pos.AssetType
		if assetType == "" {
			assetType = "other"
		}
		summary.ByAssetType[assetType] += pos.MarketValue
	}

	// 找出涨跌幅最大的持仓
	sortedByProfit := make([]*Position, len(portfolio.Positions))
	copy(sortedByProfit, portfolio.Positions)
	sort.Slice(sortedByProfit, func(i, j int) bool {
		return sortedByProfit[i].ProfitPct > sortedByProfit[j].ProfitPct
	})

	// Top 3 gainers and losers
	for i, pos := range sortedByProfit {
		if i < 3 && pos.ProfitPct > 0 {
			summary.TopGainers = append(summary.TopGainers, pos)
		}
	}
	for i := len(sortedByProfit) - 1; i >= 0 && len(summary.TopLosers) < 3; i-- {
		if sortedByProfit[i].ProfitPct < 0 {
			summary.TopLosers = append(summary.TopLosers, sortedByProfit[i])
		}
	}

	return summary, nil
}

// GetPosition 获取单个持仓
func (s *Service) GetPosition(ctx context.Context, userID, symbol string) (*Position, error) {
	pos, err := s.store.GetPosition(ctx, userID, symbol)
	if err != nil {
		return nil, err
	}

	// 更新价格
	if s.market != nil {
		if currentPrice, err := s.market.GetPrice(ctx, symbol); err == nil {
			pos.CurrentPrice = currentPrice
			pos.CalculateProfit()
		}
	}

	return pos, nil
}

// GetTransactions 获取交易记录
func (s *Service) GetTransactions(ctx context.Context, userID string, limit int) ([]*Transaction, error) {
	return s.store.GetUserTransactions(ctx, userID, limit)
}

// UpdatePrices 批量更新持仓价格
func (s *Service) UpdatePrices(ctx context.Context, userID string) error {
	positions, err := s.store.GetUserPositions(ctx, userID)
	if err != nil {
		return err
	}

	for _, pos := range positions {
		if s.market != nil {
			if currentPrice, err := s.market.GetPrice(ctx, pos.Symbol); err == nil {
				pos.CurrentPrice = currentPrice
				pos.CalculateProfit()
				pos.UpdatedAt = time.Now()
				s.store.SavePosition(ctx, pos)
			}
		}
	}

	return nil
}

// FormatProfit 格式化盈亏显示
func FormatProfit(profit, profitPct float64) string {
	sign := ""
	if profit > 0 {
		sign = "+"
	}
	return fmt.Sprintf("%s%.2f (%s%.2f%%)", sign, profit, sign, profitPct)
}

// FormatProfitWithIcon 带图标的盈亏显示
func FormatProfitWithIcon(profit, profitPct float64) string {
	icon := "➖"
	if profit > 0 {
		icon = "📈"
	} else if profit < 0 {
		icon = "📉"
	}
	return fmt.Sprintf("%s %s", icon, FormatProfit(profit, profitPct))
}
