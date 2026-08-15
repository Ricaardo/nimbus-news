package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/investor/backtest"
	_ "github.com/Ricaardo/nimbus-os/news/internal/log"
	"github.com/Ricaardo/nimbus-os/news/internal/platformapp"

	"github.com/Ricaardo/nimbus-os/datasources/market"
)

func main() {
	configPath := flag.String("config", "config.yaml", "config file path")
	apiPort := flag.Int("api-port", 8081, "API server port")
	tradingMode := flag.String("trading", "off", "交易模式: off/daily/once")
	tradingSchedule := flag.String("schedule", "09:00,10:00,12:00,14:00,15:30", "美股交易时间点")
	capital := flag.Float64("capital", 1000000, "初始资金")
	backtestMode := flag.Bool("backtest", false, "run backtest mode")
	investorID := flag.String("investor", "buffett", "investor ID for backtest")
	symbol := flag.String("symbol", "BTC", "symbol for backtest")
	days := flag.Int("days", 365, "backtest days")
	flag.Parse()

	if *backtestMode {
		runBacktest(*investorID, *symbol, *days)
		return
	}

	app, err := platformapp.New(platformapp.Options{
		ConfigPath:       *configPath,
		ListenAddr:       fmt.Sprintf("127.0.0.1:%d", *apiPort),
		Trading:          platformapp.TraderOptions{Mode: *tradingMode, Schedule: *tradingSchedule, Capital: *capital},
		StartTrader:      startLegacyTrader,
		BootstrapClosing: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform: %v\n", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "platform: %v\n", err)
		os.Exit(1)
	}
}

func runBacktest(investorID, symbol string, days int) {
	fmt.Printf("开始回测: 投资人=%s, 标的=%s, 天数=%d\n", investorID, symbol, days)

	ctx := context.Background()
	marketService := market.NewMarketService()

	endDate := time.Now()
	startDate := endDate.AddDate(0, 0, -days)

	cfg := &backtest.Config{
		InvestorID:     investorID,
		Symbol:         symbol,
		StartDate:      startDate,
		EndDate:        endDate,
		InitialCapital: 1000000.0,
		CommissionRate: 0.001,
		UseAI:          true,
	}

	engine, err := backtest.NewEngine(cfg, marketService, nil)
	if err != nil {
		fmt.Printf("创建回测引擎失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("正在运行回测...")
	result, err := engine.Run(ctx)
	if err != nil {
		fmt.Printf("回测失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n" + result.Summary())

	if len(result.Trades) > 0 {
		fmt.Println("\n## 交易记录")
		fmt.Println("| 日期 | 操作 | 价格 | 数量 | 理由 |")
		fmt.Println("|------|------|------|------|------|")
		for _, trade := range result.Trades {
			reason := trade.Reason
			if len(reason) > 30 {
				reason = reason[:30] + "..."
			}
			fmt.Printf("| %s | %s | %.2f | %.4f | %s |\n",
				trade.Date.Format("2006-01-02"),
				trade.Action,
				trade.Price,
				trade.Quantity,
				reason,
			)
		}
	}

	fmt.Println("\n回测完成!")
}
