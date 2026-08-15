package backtest

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/investor"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/datasources/market"
	dsyahoo "github.com/Ricaardo/nimbus-os/datasources/yahoo"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// Engine 回测引擎
type Engine struct {
	cfg           *Config
	marketService market.Service
	llmProvider   llm.Provider
	investor      *investor.InvestorSkill
}

// NewEngine 创建回测引擎
func NewEngine(cfg *Config, marketService market.Service, llmProvider llm.Provider) (*Engine, error) {
	// 获取投资人
	inv, ok := investor.Get(cfg.InvestorID)
	if !ok {
		return nil, fmt.Errorf("investor not found: %s", cfg.InvestorID)
	}

	return &Engine{
		cfg:           cfg,
		marketService: marketService,
		llmProvider:   llmProvider,
		investor:      inv,
	}, nil
}

// Run 运行回测
func (e *Engine) Run(ctx context.Context) (*Result, error) {
	startTime := time.Now()

	result := NewResult(e.cfg)

	// 获取历史K线数据
	klines, err := e.getHistoricalData(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get historical data: %w", err)
	}

	if len(klines) == 0 {
		return nil, fmt.Errorf("no historical data available")
	}

	// 初始化资金
	cash := e.cfg.InitialCapital
	position := 0.0

	// 记录起始价格
	result.StartPrice = klines[0].Close

	// 逐日遍历
	for i := 1; i < len(klines); i++ {
		prevKline := klines[i-1]
		currentKline := klines[i]

		// 跳过非交易日（如周末）
		if currentKline.Close == 0 {
			continue
		}

		// 构建上下文
		analysis := e.calculateIndicators(klines[:i+1])
		context := e.buildContext(&prevKline, analysis)

		// 获取投资决策
		decision, err := e.getDecision(ctx, context)
		if err != nil {
			// 如果决策失败，默认持有
			decision = &investor.InvestmentDecision{
				Action:     investor.ActionHold,
				Quantity:   0,
				Confidence: 0,
			}
		}

		// 执行交易
		trade := e.executeTrade(
			&currentKline,
			decision,
			&position,
			&cash,
		)

		if trade != nil {
			result.AddTrade(trade)
			result.TotalTrades++
		}

		// 计算当前总资产
		totalAsset := cash + position*currentKline.Close

		// 记录每日资产
		var dailyReturn float64
		if i > 1 {
			prevTotal := result.DailyRecord[i-2].TotalAsset
			if prevTotal > 0 {
				dailyReturn = (totalAsset - prevTotal) / prevTotal
			}
		}

		accumReturn := (totalAsset - e.cfg.InitialCapital) / e.cfg.InitialCapital

		result.AddDailyRecord(&DailyRecord{
			Date:        currentKline.Time,
			Price:       currentKline.Close,
			Position:    position,
			Cash:        cash,
			TotalAsset:  totalAsset,
			Return:      dailyReturn,
			AccumReturn: accumReturn,
		})

		// 检查是否触发止损/止盈
		position = e.checkStopLossTakeProfit(&currentKline, position, &cash, result)
	}

	// 记录结束价格
	if len(klines) > 0 {
		result.EndPrice = klines[len(klines)-1].Close
	}

	// 结算最终持仓
	if position > 0 {
		finalPrice := klines[len(klines)-1].Close
		finalAsset := cash + position*finalPrice
		result.FinalCapital = finalAsset
	} else {
		result.FinalCapital = cash
	}

	// 计算指标
	result.CalculateMetrics()

	result.RunDuration = time.Since(startTime).Milliseconds()

	return result, nil
}

// getHistoricalData 获取历史K线数据
func (e *Engine) getHistoricalData(ctx context.Context) ([]model.KLineItem, error) {
	// 尝试从市场服务获取历史数据
	days := int(e.cfg.EndDate.Sub(e.cfg.StartDate).Hours()/24) + 30

	// 使用Yahoo服务获取历史数据
	klines, err := e.marketService.GetHistoricalQuotes(ctx, e.cfg.Symbol, "1d", fmt.Sprintf("%dd", days))
	if err != nil {
		// 如果失败，尝试使用其他方式获取
		return e.getHistoricalDataFallback(ctx, days)
	}

	// 过滤日期范围
	var filtered []model.KLineItem
	for _, k := range klines {
		if !k.Time.Before(e.cfg.StartDate) && !k.Time.After(e.cfg.EndDate) {
			filtered = append(filtered, k)
		}
	}

	return filtered, nil
}

// getHistoricalDataFallback 获取历史数据的备选方案
// Routes through the shared datasources/yahoo package (the canonical
// Yahoo implementation) instead of hand-rolling a direct HTTP call.
func (e *Engine) getHistoricalDataFallback(ctx context.Context, days int) ([]model.KLineItem, error) {
	symbol := e.cfg.Symbol
	// Map crypto symbols to Yahoo-native format for the shared package.
	if symbol == "BTC" {
		symbol = "BTC-USD"
	} else if symbol == "ETH" {
		symbol = "ETH-USD"
	}

	bars, err := dsyahoo.GetHistoryDays(ctx, symbol, days)
	if err != nil {
		return nil, fmt.Errorf("yahoo fallback: %w", err)
	}

	klines := make([]model.KLineItem, 0, len(bars))
	for _, b := range bars {
		// Parse Yahoo's date string (YYYY-MM-DD) to time.Time.
		t, parseErr := time.Parse("2006-01-02", b.Date)
		if parseErr != nil {
			t = time.Time{}
		}
		if b.Close == 0 {
			continue
		}
		klines = append(klines, model.KLineItem{
			Time:   t,
			Open:   b.Open,
			High:   b.High,
			Low:    b.Low,
			Close:  b.Close,
			Volume: b.Volume,
		})
	}
	if len(klines) == 0 {
		return nil, fmt.Errorf("yahoo fallback: no data for %s", symbol)
	}
	return klines, nil
}

// calculateIndicators 计算技术指标
func (e *Engine) calculateIndicators(klines []model.KLineItem) *model.SecurityAnalysis {
	if len(klines) < 5 {
		return &model.SecurityAnalysis{}
	}

	// 简单计算MA
	var ma5, ma10, ma20, ma60 float64
	count := len(klines)

	// MA5
	if count >= 5 {
		sum5 := 0.0
		for i := count - 5; i < count; i++ {
			sum5 += klines[i].Close
		}
		ma5 = sum5 / 5
	}

	// MA10
	if count >= 10 {
		sum10 := 0.0
		for i := count - 10; i < count; i++ {
			sum10 += klines[i].Close
		}
		ma10 = sum10 / 10
	}

	// MA20
	if count >= 20 {
		sum20 := 0.0
		for i := count - 20; i < count; i++ {
			sum20 += klines[i].Close
		}
		ma20 = sum20 / 20
	}

	// MA60
	if count >= 60 {
		sum60 := 0.0
		for i := count - 60; i < count; i++ {
			sum60 += klines[i].Close
		}
		ma60 = sum60 / 60
	}

	// RSI
	rsi := e.calculateRSI(klines)

	// 确定趋势
	trend := "neutral"
	if ma5 > ma20 && ma20 > ma60 {
		trend = "bullish"
	} else if ma5 < ma20 && ma20 < ma60 {
		trend = "bearish"
	}

	// 支撑/阻力
	high := klines[count-1].High
	low := klines[count-1].Low
	support := low
	resistance := high

	return &model.SecurityAnalysis{
		Trend:      trend,
		MA5:        ma5,
		MA10:       ma10,
		MA20:       ma20,
		MA50:       ma60,
		RSI:        rsi,
		Support:    support,
		Resistance: resistance,
	}
}

// calculateRSI 计算RSI
func (e *Engine) calculateRSI(klines []model.KLineItem) float64 {
	if len(klines) < 15 {
		return 50.0
	}

	var gains, losses float64
	for i := len(klines) - 14; i < len(klines); i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses -= change
		}
	}

	avgGain := gains / 14
	avgLoss := losses / 14

	if avgLoss == 0 {
		return 100
	}

	rs := avgGain / avgLoss
	rsi := 100 - (100 / (1 + rs))
	return rsi
}

// buildContext 构建决策上下文
func (e *Engine) buildContext(kline *model.KLineItem, analysis *model.SecurityAnalysis) map[string]interface{} {
	ctx := make(map[string]interface{})

	// 基本信息
	ctx["symbol_name"] = kline.Close // 简化处理
	ctx["symbol"] = e.cfg.Symbol
	ctx["current_price"] = kline.Close
	ctx["change_pct"] = ((kline.Close - kline.Open) / kline.Open) * 100
	ctx["volume"] = kline.Volume
	ctx["high"] = kline.High
	ctx["low"] = kline.Low

	// 技术指标
	ctx["trend"] = analysis.Trend
	ctx["rsi"] = analysis.RSI
	ctx["rsi_signal"] = getRSISignal(analysis.RSI)
	ctx["ma5"] = analysis.MA5
	ctx["ma10"] = analysis.MA10
	ctx["ma20"] = analysis.MA20
	ctx["ma50"] = analysis.MA50

	// 支撑阻力
	ctx["support"] = analysis.Support
	ctx["resistance"] = analysis.Resistance

	// 持仓状态
	ctx["position_status"] = "空仓观望"
	ctx["available_cash"] = e.cfg.InitialCapital

	// 情绪
	ctx["fear_greed"] = 50
	ctx["fear_greed_tag"] = "中性"

	return ctx
}

// getRSISignal 获取RSI信号
func getRSISignal(rsi float64) string {
	if rsi > 70 {
		return "超买"
	} else if rsi < 30 {
		return "超卖"
	}
	return "中性"
}

// getDecision 获取投资决策
func (e *Engine) getDecision(ctx context.Context, context map[string]interface{}) (*investor.InvestmentDecision, error) {
	if e.llmProvider == nil {
		// 如果没有LLM，使用简单的技术分析决策
		return e.simpleDecision(context), nil
	}

	// 构建决策prompt
	decisionPrompt := e.investor.BuildDecisionPrompt(context)

	// 构建完整的prompt
	fullPrompt := fmt.Sprintf(`%s

请根据以上市场数据和技术指标，参考你的投资风格，给出投资决策。

请以JSON格式返回决策，不要有其他内容：
{"action": "买入/卖出/持有", "quantity": 建议仓位(0-100), "reason": "理由", "confidence": 信心度(0-100), "risk_level": "低/中/高", "holding_days": 持有天数}`, decisionPrompt)

	messages := []llm.Message{
		{Role: "system", Content: e.investor.SystemPrompt},
		{Role: "user", Content: fullPrompt},
	}

	// 使用Reason方法调用R1推理模型
	response, _, err := e.llmProvider.Think(ctx, messages, "")
	if err != nil {
		return nil, err
	}

	// 解析响应
	return parseDecision(response)
}

// simpleDecision 简单的技术分析决策
func (e *Engine) simpleDecision(context map[string]interface{}) *investor.InvestmentDecision {
	decision := &investor.InvestmentDecision{
		Action:      investor.ActionHold,
		Quantity:    0,
		Confidence:  50,
		RiskLevel:   investor.RiskMedium,
		HoldingDays: 7,
	}

	// 根据投资人风格使用不同的决策逻辑
	switch e.investor.Style {
	case investor.StyleVALUE:
		// 巴菲特风格：价值投资，保守逢低买入
		decision = e.valueDecision(context)
	case investor.StyleTREND:
		// 索罗斯风格：趋势投资，趋势确认后追涨杀跌
		decision = e.trendDecision(context)
	case investor.StyleCONTRARIAN:
		// 芒格风格：逆向投资，极端情况下才出手
		decision = e.contrarianDecision(context)
	case investor.StyleALL_WEATHER:
		// 达利欧风格：全天候，风险控制优先
		decision = e.allWeatherDecision(context)
	case investor.StyleMOMENTUM:
		// 林奇风格：动量投资，灵活调整
		decision = e.momentumDecision(context)
	default:
		decision = e.valueDecision(context)
	}

	return decision
}

// valueDecision 价值投资决策
func (e *Engine) valueDecision(context map[string]interface{}) *investor.InvestmentDecision {
	decision := &investor.InvestmentDecision{
		Action:      investor.ActionHold,
		Quantity:    0,
		Confidence:  50,
		RiskLevel:   investor.RiskLow,
		HoldingDays: 90,
	}

	rsi := context["rsi"].(float64)
	changePct := context["change_pct"].(float64)

	// 价值投资：RSI < 30 超卖时买入，耐心等待
	if rsi < 30 {
		decision.Action = investor.ActionBuy
		decision.Quantity = 40
		decision.Reason = fmt.Sprintf("价值投资：RSI=%.0f(严重超卖)，长期价值凸显，分批建仓", rsi)
		decision.Confidence = 80
	} else if rsi > 70 {
		// 高估时卖出
		decision.Action = investor.ActionSell
		decision.Quantity = 60
		decision.Reason = fmt.Sprintf("价值投资：RSI=%.0f(严重超买)，分批止盈", rsi)
		decision.Confidence = 75
	} else if changePct < -4 {
		// 大跌时加仓
		decision.Action = investor.ActionBuy
		decision.Quantity = 20
		decision.Reason = fmt.Sprintf("价值投资：单日大跌=%.2f%%，越跌越买", changePct)
		decision.Confidence = 70
	} else if changePct > 4 {
		// 大涨时部分止盈
		decision.Action = investor.ActionSell
		decision.Quantity = 30
		decision.Reason = fmt.Sprintf("价值投资：单日大涨=%.2f%%，部分止盈锁定利润", changePct)
		decision.Confidence = 65
	} else {
		decision.Reason = fmt.Sprintf("价值投资：RSI=%.0f(中性)，等待价值显现", rsi)
	}

	return decision
}

// trendDecision 趋势投资决策
func (e *Engine) trendDecision(context map[string]interface{}) *investor.InvestmentDecision {
	decision := &investor.InvestmentDecision{
		Action:      investor.ActionHold,
		Quantity:    0,
		Confidence:  50,
		RiskLevel:   investor.RiskMedium,
		HoldingDays: 14,
	}

	rsi := context["rsi"].(float64)
	trend := context["trend"].(string)
	changePct := context["change_pct"].(float64)

	// 趋势投资：顺势而为
	if trend == "bullish" && rsi < 65 {
		decision.Action = investor.ActionBuy
		decision.Quantity = 50
		decision.Reason = fmt.Sprintf("趋势投资：上升趋势确认（RSI %.1f），顺势买入", rsi)
		decision.Confidence = 75
	} else if trend == "bearish" && rsi > 35 {
		decision.Action = investor.ActionSell
		decision.Quantity = 70
		decision.Reason = fmt.Sprintf("趋势投资：下降趋势确认（RSI %.1f），果断卖出", rsi)
		decision.Confidence = 75
	} else if changePct > 2 && trend != "bearish" {
		// 放量上涨追入
		decision.Action = investor.ActionBuy
		decision.Quantity = 30
		decision.Reason = fmt.Sprintf("趋势投资：放量上涨=%.2f%%，顺势而为", changePct)
		decision.Confidence = 60
	} else if changePct < -2 && trend != "bullish" {
		// 放量下跌杀跌
		decision.Action = investor.ActionSell
		decision.Quantity = 40
		decision.Reason = fmt.Sprintf("趋势投资：放量下跌=%.2f%%，顺势而为", changePct)
		decision.Confidence = 60
	} else {
		decision.Reason = fmt.Sprintf("趋势投资：趋势不明，等待确认")
	}

	return decision
}

// contrarianDecision 逆向投资决策
func (e *Engine) contrarianDecision(context map[string]interface{}) *investor.InvestmentDecision {
	decision := &investor.InvestmentDecision{
		Action:      investor.ActionHold,
		Quantity:    0,
		Confidence:  50,
		RiskLevel:   investor.RiskMedium,
		HoldingDays: 60,
	}

	rsi := context["rsi"].(float64)
	changePct := context["change_pct"].(float64)

	// 逆向投资：只在极端情况下出手
	if rsi < 25 || changePct < -5 {
		// 极度恐慌时买入
		decision.Action = investor.ActionBuy
		decision.Quantity = 50
		decision.Reason = fmt.Sprintf("逆向投资：市场极度恐慌(RSI=%.0f, 跌幅=%.2f%%)，反向买入", rsi, changePct)
		decision.Confidence = 85
	} else if rsi > 75 || changePct > 5 {
		// 极度贪婪时卖出
		decision.Action = investor.ActionSell
		decision.Quantity = 80
		decision.Reason = fmt.Sprintf("逆向投资：市场极度贪婪(RSI=%.0f, 涨幅=%.2f%%)，反向卖出", rsi, changePct)
		decision.Confidence = 85
	} else if rsi < 35 {
		decision.Action = investor.ActionBuy
		decision.Quantity = 25
		decision.Reason = fmt.Sprintf("逆向投资：RSI=%.0f超卖，可考虑建仓", rsi)
		decision.Confidence = 60
	} else {
		decision.Reason = fmt.Sprintf("逆向投资：未到极端位置，继续等待机会(RSI=%.0f)", rsi)
	}

	return decision
}

// allWeatherDecision 全天候投资决策
func (e *Engine) allWeatherDecision(context map[string]interface{}) *investor.InvestmentDecision {
	decision := &investor.InvestmentDecision{
		Action:      investor.ActionHold,
		Quantity:    0,
		Confidence:  50,
		RiskLevel:   investor.RiskLow,
		HoldingDays: 30,
	}

	rsi := context["rsi"].(float64)
	changePct := context["change_pct"].(float64)

	// 全天候策略：保守稳健，风控优先
	if rsi < 30 {
		decision.Action = investor.ActionBuy
		decision.Quantity = 25 // 仓位较轻
		decision.Reason = fmt.Sprintf("全天候：RSI=%.0f超卖，轻仓买入", rsi)
		decision.Confidence = 70
	} else if rsi > 70 {
		decision.Action = investor.ActionSell
		decision.Quantity = 40
		decision.Reason = fmt.Sprintf("全天候：RSI=%.0f超买，轻仓止盈", rsi)
		decision.Confidence = 70
	} else if changePct < -3 {
		decision.Action = investor.ActionBuy
		decision.Quantity = 15 // 越跌越买，但仓位更轻
		decision.Reason = fmt.Sprintf("全天候：大跌=%.2f%%，轻仓低吸", changePct)
		decision.Confidence = 60
	} else if changePct > 3 {
		decision.Action = investor.ActionSell
		decision.Quantity = 25
		decision.Reason = fmt.Sprintf("全天候：大涨=%.2f%%，轻仓止盈", changePct)
		decision.Confidence = 60
	} else {
		decision.Reason = fmt.Sprintf("全天候：RSI=%.0f，维持平衡", rsi)
	}

	return decision
}

// momentumDecision 动量投资决策
func (e *Engine) momentumDecision(context map[string]interface{}) *investor.InvestmentDecision {
	decision := &investor.InvestmentDecision{
		Action:      investor.ActionHold,
		Quantity:    0,
		Confidence:  50,
		RiskLevel:   investor.RiskHigh,
		HoldingDays: 21,
	}

	rsi := context["rsi"].(float64)
	trend := context["trend"].(string)
	changePct := context["change_pct"].(float64)

	// 动量投资：灵活进取，趋势破了就离场
	if trend == "bullish" && rsi < 60 {
		decision.Action = investor.ActionBuy
		decision.Quantity = 60 // 仓位较重
		decision.Reason = fmt.Sprintf("动量投资：上升趋势中，积极做多")
		decision.Confidence = 70
	} else if trend == "bearish" && rsi > 40 {
		decision.Action = investor.ActionSell
		decision.Quantity = 80 // 果断离场
		decision.Reason = fmt.Sprintf("动量投资：下降趋势形成，果断卖出")
		decision.Confidence = 70
	} else if rsi < 25 {
		decision.Action = investor.ActionBuy
		decision.Quantity = 40
		decision.Reason = fmt.Sprintf("动量投资：RSI=%.0f严重超卖，抢反弹", rsi)
		decision.Confidence = 55
	} else if rsi > 75 {
		decision.Action = investor.ActionSell
		decision.Quantity = 60
		decision.Reason = fmt.Sprintf("动量投资：RSI=%.0f严重超买，反弹离场", rsi)
		decision.Confidence = 55
	} else if changePct > 2.5 {
		decision.Action = investor.ActionBuy
		decision.Quantity = 35
		decision.Reason = fmt.Sprintf("动量投资：强势上涨=%.2f%%，顺势追涨", changePct)
		decision.Confidence = 60
	} else if changePct < -2.5 {
		decision.Action = investor.ActionSell
		decision.Quantity = 50
		decision.Reason = fmt.Sprintf("动量投资：强势下跌=%.2f%%，止损离场", changePct)
		decision.Confidence = 60
	} else {
		decision.Reason = fmt.Sprintf("动量投资：趋势不明，保持灵活")
	}

	return decision
}

// executeTrade 执行交易
func (e *Engine) executeTrade(kline *model.KLineItem, decision *investor.InvestmentDecision, position *float64, cash *float64) *Trade {
	price := kline.Close
	action := decision.Action

	if action == investor.ActionBuy {
		// 买入 — 百分比仓位→整数股数
		maxShares := math.Floor(*cash / price / (1 + e.cfg.CommissionRate))
		allocShares := math.Floor(decision.Quantity / 100 * maxShares)
		quantity := math.Max(0, allocShares)
		if quantity >= 1.0 {
			amount := quantity * price
			commission := amount * e.cfg.CommissionRate
			if *cash >= amount+commission {
				*cash -= (amount + commission)
				*position += quantity

				return &Trade{
					Date:       kline.Time,
					Action:     "买入",
					Price:      price,
					Quantity:   quantity,
					Amount:     amount,
					Commission: commission,
					Position:   *position,
					Cash:       *cash,
					TotalAsset: *cash + *position*price,
					Reason:     decision.Reason,
					Confidence: decision.Confidence,
					RiskLevel:  string(decision.RiskLevel),
				}
			}
		}
	} else if action == investor.ActionSell {
		// 卖出 — 百分比持仓→整数股数
		if *position >= 1.0 {
			quantity := math.Floor(decision.Quantity / 100 * *position)
			if quantity < 1.0 {
				quantity = *position
			}
			if quantity > *position {
				quantity = *position
			}
			amount := quantity * price
			commission := amount * e.cfg.CommissionRate

			*cash += (amount - commission)
			*position -= quantity

			return &Trade{
				Date:       kline.Time,
				Action:     "卖出",
				Price:      price,
				Quantity:   quantity,
				Amount:     amount,
				Commission: commission,
				Position:   *position,
				Cash:       *cash,
				TotalAsset: *cash + *position*price,
				Reason:     decision.Reason,
				Confidence: decision.Confidence,
				RiskLevel:  string(decision.RiskLevel),
			}
		}
	}

	return nil
}

// checkStopLossTakeProfit 检查止损止盈
func (e *Engine) checkStopLossTakeProfit(kline *model.KLineItem, position float64, cash *float64, result *Result) float64 {
	if position <= 0 {
		return position
	}

	// 获取入场价格（简化处理：取最后一次买入价格）
	var entryPrice float64
	for i := len(result.Trades) - 1; i >= 0; i-- {
		if result.Trades[i].Action == "买入" {
			entryPrice = result.Trades[i].Price
			break
		}
	}

	if entryPrice == 0 {
		return position
	}

	currentPrice := kline.Close
	returnPct := (currentPrice - entryPrice) / entryPrice

	// 止损
	if returnPct <= e.investor.StopLossPct {
		amount := position * currentPrice * (1 - e.cfg.CommissionRate)
		*cash += amount
		result.TotalTrades++

		result.AddTrade(&Trade{
			Date:       kline.Time,
			Action:     "止损卖出",
			Price:      currentPrice,
			Quantity:   position,
			Amount:     amount,
			Commission: position * currentPrice * e.cfg.CommissionRate,
			Position:   0,
			Cash:       *cash,
			TotalAsset: *cash,
			Reason:     fmt.Sprintf("触发止损 %.2f%%", returnPct*100),
			Confidence: 0,
			RiskLevel:  "高",
		})

		return 0
	}

	// 止盈
	if returnPct >= e.investor.TakeProfitPct {
		amount := position * currentPrice * (1 - e.cfg.CommissionRate)
		*cash += amount
		result.TotalTrades++

		result.AddTrade(&Trade{
			Date:       kline.Time,
			Action:     "止盈卖出",
			Price:      currentPrice,
			Quantity:   position,
			Amount:     amount,
			Commission: position * currentPrice * e.cfg.CommissionRate,
			Position:   0,
			Cash:       *cash,
			TotalAsset: *cash,
			Reason:     fmt.Sprintf("触发止盈 %.2f%%", returnPct*100),
			Confidence: 0,
			RiskLevel:  "低",
		})

		return 0
	}

	return position
}

// parseDecision 解析LLM响应
func parseDecision(response string) (*investor.InvestmentDecision, error) {
	// 简单JSON解析
	decision := &investor.InvestmentDecision{
		Action:      investor.ActionHold,
		Quantity:    0,
		Reason:      "解析失败",
		Confidence:  50,
		RiskLevel:   investor.RiskMedium,
		HoldingDays: 7,
	}

	// 尝试提取action
	if contains(response, "买入") || contains(response, "买") {
		decision.Action = investor.ActionBuy
	} else if contains(response, "卖出") || contains(response, "卖") {
		decision.Action = investor.ActionSell
	}

	// 尝试提取quantity
	decision.Quantity = extractNumber(response, 0, 100)

	// 尝试提取confidence
	decision.Confidence = extractNumber(response, 0, 100)

	return decision, nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr) >= 0)
}

func containsAt(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func extractNumber(s string, min, max float64) float64 {
	// 简化处理：返回默认值
	return math.Round((min + max) / 2)
}
