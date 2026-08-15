package portfolio

import (
	"context"
	"os"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestPosition(t *testing.T) {
	pos := NewPosition("user1", "BTCUSDT", 1.0, 50000)

	if pos.UserID != "user1" {
		t.Errorf("Expected UserID 'user1', got '%s'", pos.UserID)
	}
	if pos.Symbol != "BTCUSDT" {
		t.Errorf("Expected Symbol 'BTCUSDT', got '%s'", pos.Symbol)
	}
	if pos.Quantity != 1.0 {
		t.Errorf("Expected Quantity 1.0, got %f", pos.Quantity)
	}
	if pos.AvgCost != 50000 {
		t.Errorf("Expected AvgCost 50000, got %f", pos.AvgCost)
	}
}

func TestPosition_AddQuantity(t *testing.T) {
	pos := NewPosition("user1", "BTCUSDT", 1.0, 50000)

	// 加仓 0.5 @ 52000
	pos.AddQuantity(0.5, 52000)

	if pos.Quantity != 1.5 {
		t.Errorf("Expected Quantity 1.5, got %f", pos.Quantity)
	}

	// 平均成本 = (1*50000 + 0.5*52000) / 1.5 = 76000/1.5 ≈ 50666.67
	expectedAvgCost := (1.0*50000 + 0.5*52000) / 1.5
	if pos.AvgCost < expectedAvgCost-0.01 || pos.AvgCost > expectedAvgCost+0.01 {
		t.Errorf("Expected AvgCost %.2f, got %f", expectedAvgCost, pos.AvgCost)
	}
}

func TestPosition_ReduceQuantity(t *testing.T) {
	pos := NewPosition("user1", "BTCUSDT", 1.0, 50000)

	// 减仓 0.3
	err := pos.ReduceQuantity(0.3)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if pos.Quantity != 0.7 {
		t.Errorf("Expected Quantity 0.7, got %f", pos.Quantity)
	}

	// 尝试减仓超过持仓数量
	err = pos.ReduceQuantity(1.0)
	if err != ErrInsufficientQuantity {
		t.Errorf("Expected ErrInsufficientQuantity, got %v", err)
	}
}

func TestPosition_CalculateProfit(t *testing.T) {
	pos := NewPosition("user1", "BTCUSDT", 1.0, 50000)
	pos.CurrentPrice = 55000

	pos.CalculateProfit()

	if pos.MarketValue != 55000 {
		t.Errorf("Expected MarketValue 55000, got %f", pos.MarketValue)
	}
	if pos.Profit != 5000 {
		t.Errorf("Expected Profit 5000, got %f", pos.Profit)
	}
	if pos.ProfitPct != 10.0 {
		t.Errorf("Expected ProfitPct 10.0, got %f", pos.ProfitPct)
	}
}

func TestStore(t *testing.T) {
	// 创建临时数据库
	tmpFile, err := os.CreateTemp("", "portfolio_test_*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	db, err := bolt.Open(tmpFile.Name(), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// 测试保存持仓
	pos := NewPosition("user1", "BTCUSDT", 1.0, 50000)
	err = store.SavePosition(ctx, pos)
	if err != nil {
		t.Fatalf("SavePosition failed: %v", err)
	}

	// 测试获取持仓
	got, err := store.GetPosition(ctx, "user1", "BTCUSDT")
	if err != nil {
		t.Fatalf("GetPosition failed: %v", err)
	}
	if got.Quantity != 1.0 {
		t.Errorf("Expected Quantity 1.0, got %f", got.Quantity)
	}

	// 测试获取不存在的持仓
	_, err = store.GetPosition(ctx, "user1", "ETHUSDT")
	if err != ErrPositionNotFound {
		t.Errorf("Expected ErrPositionNotFound, got %v", err)
	}

	// 测试获取用户所有持仓
	pos2 := NewPosition("user1", "ETHUSDT", 10.0, 3000)
	store.SavePosition(ctx, pos2)

	positions, err := store.GetUserPositions(ctx, "user1")
	if err != nil {
		t.Fatalf("GetUserPositions failed: %v", err)
	}
	if len(positions) != 2 {
		t.Errorf("Expected 2 positions, got %d", len(positions))
	}

	// 测试删除持仓
	err = store.DeletePosition(ctx, "user1", "BTCUSDT")
	if err != nil {
		t.Fatalf("DeletePosition failed: %v", err)
	}

	positions, _ = store.GetUserPositions(ctx, "user1")
	if len(positions) != 1 {
		t.Errorf("Expected 1 position after delete, got %d", len(positions))
	}
}

func TestTransaction(t *testing.T) {
	tx := NewTransaction("user1", "BTCUSDT", TransactionTypeBuy, 1.0, 50000)

	if tx.UserID != "user1" {
		t.Errorf("Expected UserID 'user1', got '%s'", tx.UserID)
	}
	if tx.Type != TransactionTypeBuy {
		t.Errorf("Expected Type TransactionTypeBuy, got '%s'", tx.Type)
	}
	if tx.ID == "" {
		t.Error("Expected non-empty ID")
	}
}

func TestService(t *testing.T) {
	// 创建临时数据库
	tmpFile, err := os.CreateTemp("", "portfolio_service_test_*.db")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	db, err := bolt.Open(tmpFile.Name(), 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	store, _ := NewStore(db)
	service := NewService(store, nil) // 无行情数据

	ctx := context.Background()

	// 测试添加持仓
	pos, err := service.AddPosition(ctx, "user1", "BTCUSDT", 1.0, 50000)
	if err != nil {
		t.Fatalf("AddPosition failed: %v", err)
	}
	if pos.Quantity != 1.0 {
		t.Errorf("Expected Quantity 1.0, got %f", pos.Quantity)
	}

	// 测试加仓
	pos, err = service.AddPosition(ctx, "user1", "BTCUSDT", 0.5, 52000)
	if err != nil {
		t.Fatalf("AddPosition (加仓) failed: %v", err)
	}
	if pos.Quantity != 1.5 {
		t.Errorf("Expected Quantity 1.5, got %f", pos.Quantity)
	}

	// 测试减仓
	pos, err = service.ReducePosition(ctx, "user1", "BTCUSDT", 0.5, 55000)
	if err != nil {
		t.Fatalf("ReducePosition failed: %v", err)
	}
	if pos.Quantity != 1.0 {
		t.Errorf("Expected Quantity 1.0 after reduce, got %f", pos.Quantity)
	}

	// 测试获取组合
	portfolio, err := service.GetPortfolio(ctx, "user1")
	if err != nil {
		t.Fatalf("GetPortfolio failed: %v", err)
	}
	if len(portfolio.Positions) != 1 {
		t.Errorf("Expected 1 position, got %d", len(portfolio.Positions))
	}

	// 测试清仓
	err = service.ClearPosition(ctx, "user1", "BTCUSDT")
	if err != nil {
		t.Fatalf("ClearPosition failed: %v", err)
	}

	portfolio, _ = service.GetPortfolio(ctx, "user1")
	if len(portfolio.Positions) != 0 {
		t.Errorf("Expected 0 positions after clear, got %d", len(portfolio.Positions))
	}

	// 测试交易记录
	txns, err := service.GetTransactions(ctx, "user1", 10)
	if err != nil {
		t.Fatalf("GetTransactions failed: %v", err)
	}
	// 应该有 3 条记录: 买入, 加仓, 减仓, 清仓
	if len(txns) < 3 {
		t.Errorf("Expected at least 3 transactions, got %d", len(txns))
	}
}

func TestFormatProfit(t *testing.T) {
	tests := []struct {
		profit    float64
		profitPct float64
		expected  string
	}{
		{100, 10, "+100.00 (+10.00%)"},
		{-50, -5, "-50.00 (-5.00%)"},
		{0, 0, "0.00 (0.00%)"},
	}

	for _, tt := range tests {
		result := FormatProfit(tt.profit, tt.profitPct)
		if result != tt.expected {
			t.Errorf("FormatProfit(%f, %f) = %s, want %s", tt.profit, tt.profitPct, result, tt.expected)
		}
	}
}
