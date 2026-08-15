package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Ricaardo/nimbus-os/datasources/market"
	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/investor/investors"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/platformapp"
	"github.com/Ricaardo/nimbus-os/news/internal/scheduler"
)

func startLegacyTrader(ctx context.Context, cfg platformapp.TraderOptions, marketSvc market.Service, provider llm.Provider, channels *channel.Manager) {
	investors.RegisterAll()
	NewTrader(TraderConfig{Mode: cfg.Mode, Schedule: cfg.Schedule, Capital: cfg.Capital}, marketSvc, provider, channels).Start(ctx)
}

type TraderConfig struct {
	Mode     string
	Schedule string
	Capital  float64
}

type Trader struct {
	config      TraderConfig
	marketSvc   market.Service
	llmProvider llm.Provider
	channelMgr  *channel.Manager
}

func NewTrader(config TraderConfig, marketSvc market.Service, llmProvider llm.Provider, channelMgr *channel.Manager) *Trader {
	return &Trader{
		config:      config,
		marketSvc:   marketSvc,
		llmProvider: llmProvider,
		channelMgr:  channelMgr,
	}
}

func (t *Trader) Start(ctx context.Context) {
	fmt.Println("========== 启动交易机器人 ==========")
	fmt.Printf("模式: %s, 时间点: %s, 资金: %.0f\n", t.config.Mode, t.config.Schedule, t.config.Capital)

	times := parseSchedule(t.config.Schedule)
	fmt.Printf("交易时间点: %v\n", times)

	loc, _ := time.LoadLocation("America/New_York")
	sched := scheduler.NewSchedulerWithLocation(loc)

	timeTypes := []string{"pre_market", "morning", "lunch", "afternoon", "close"}

	for i, timeStr := range times {
		timeType := "morning"
		if i < len(timeTypes) {
			timeType = timeTypes[i]
		}

		currentTimeType := timeType
		err := sched.AddTask(&scheduler.Task{
			Name: fmt.Sprintf("trading_%s", timeStr),
			Schedule: scheduler.Schedule{
				Type:  scheduler.TypeCron,
				Times: []string{timeStr},
			},
			Handler: func(ctx context.Context) error {
				fmt.Printf("\n========== %s 交易决策 ==========\n", currentTimeType)
				return t.runTradingDecision(ctx, currentTimeType)
			},
		})
		if err != nil {
			fmt.Printf("添加任务 %s 失败: %v\n", timeStr, err)
		}
	}

	sched.Start(ctx)
	fmt.Println("交易机器人已启动")
}

func (t *Trader) runTradingDecision(ctx context.Context, timeType string) error {
	fmt.Printf("时间类型: %s\n", timeType)

	symbols := []string{"BTC", "ETH", "SOL", "SPY", "QQQ"}
	for _, symbol := range symbols {
		quote, err := t.marketSvc.GetMarketQuote(ctx, symbol)
		if err != nil {
			fmt.Printf("获取 %s 行情失败: %v\n", symbol, err)
			continue
		}

		changeEmoji := "📊"
		if quote.ChangePercent > 0 {
			changeEmoji = "📈"
		} else if quote.ChangePercent < 0 {
			changeEmoji = "📉"
		}

		content := fmt.Sprintf("**%s** %s %.2f %s%.2f%%", quote.Name, changeEmoji, quote.Price, changeEmoji, quote.ChangePercent)

		msg := &model.Message{
			Type:    model.TypeChat,
			Content: content,
		}

		for _, chName := range []string{"feishu-bot"} {
			if ch, ok := t.channelMgr.Get(chName); ok {
				ch.Send(ctx, msg)
			}
		}
	}

	return nil
}

func parseSchedule(s string) []string {
	if s == "" {
		return []string{"09:00", "10:00", "12:00", "14:00", "15:30"}
	}
	var times []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			times = append(times, t)
		}
	}
	if len(times) == 0 {
		return []string{"09:00", "10:00", "12:00", "14:00", "15:30"}
	}
	return times
}

func extractRecommendedSymbols(content string) []string {
	var symbols []string

	knownSymbols := map[string]bool{
		"BTC": true, "ETH": true, "SOL": true, "AVAX": true, "DOT": true,
		"ADA": true, "DOGE": true, "XRP": true, "LTC": true, "LINK": true,
		"XAU": true, "XAG": true, "SPY": true, "QQQ": true, "DIA": true,
		"EURUSD": true, "USDJPY": true,
	}

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		for sym := range knownSymbols {
			if strings.Contains(line, sym) {
				found := false
				for _, s := range symbols {
					if s == sym {
						found = true
						break
					}
				}
				if !found {
					symbols = append(symbols, sym)
				}
			}
		}
	}

	patterns := []string{
		`\*\*([A-Z]{2,10})\*\*`,
		`"symbol"\s*:\s*"([A-Z]{2,10})"`,
		`(?:^|\s)(BTC|ETH|SOL|AVAX|DOT|ADA|XAU|XAG)(?:$|\s)`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllStringSubmatch(content, -1)
		for _, match := range matches {
			if len(match) > 1 {
				sym := match[1]
				if knownSymbols[strings.ToUpper(sym)] {
					found := false
					for _, s := range symbols {
						if s == sym {
							found = true
							break
						}
					}
					if !found {
						symbols = append(symbols, sym)
					}
				}
			}
		}
	}

	return symbols
}
