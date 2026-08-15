package store

import (
	"os"
	"testing"
)

func TestMarketDB_Integration(t *testing.T) {
	dbPath := "../../data/market.db"
	info, err := os.Stat(dbPath)
	if os.IsNotExist(err) || (err == nil && info.Size() == 0) {
		t.Skip("market.db not found or empty, run scripts/ingest_daily.py first")
	}

	db, err := NewMarketDB(dbPath)
	if err != nil {
		t.Fatalf("NewMarketDB: %v", err)
	}
	defer db.Close()

	t.Run("SymbolCount", func(t *testing.T) {
		count := db.SymbolCount()
		if count < 1000 {
			t.Errorf("expected >1000 symbols, got %d", count)
		}
		t.Logf("Total symbols: %d", count)
	})

	t.Run("SearchByName_Chinese", func(t *testing.T) {
		tests := []struct {
			query      string
			wantSymbol string
		}{
			{"紫金矿业", "02899.HK"}, // HK comes first alphabetically, both correct
			{"贵州茅台", "600519.SS"},
			{"腾讯控股", "00700.HK"},
			{"比特币", "BTCUSDT"},
		}
		for _, tt := range tests {
			results, err := db.SearchSymbol(tt.query, 1)
			if err != nil {
				t.Errorf("SearchSymbol(%q): %v", tt.query, err)
				continue
			}
			if len(results) == 0 {
				t.Errorf("SearchSymbol(%q): no results", tt.query)
				continue
			}
			if results[0].Symbol != tt.wantSymbol {
				t.Errorf("SearchSymbol(%q) = %s, want %s", tt.query, results[0].Symbol, tt.wantSymbol)
			}
			t.Logf("  %s → %s (%s)", tt.query, results[0].Symbol, results[0].Name)
		}
	})

	t.Run("SearchByCode", func(t *testing.T) {
		results, err := db.SearchSymbol("601899", 1)
		if err != nil {
			t.Fatalf("SearchSymbol: %v", err)
		}
		if len(results) == 0 || results[0].Symbol != "601899.SS" {
			t.Errorf("expected 601899.SS, got %v", results)
		}
	})

	t.Run("SearchBySymbol", func(t *testing.T) {
		results, err := db.SearchSymbol("AAPL", 5)
		if err != nil {
			t.Fatalf("SearchSymbol: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected AAPL, got nothing")
		} else {
			t.Logf("  AAPL → %s (%s)", results[0].Symbol, results[0].Name)
		}
	})

	t.Run("SearchFuzzy", func(t *testing.T) {
		results, err := db.SearchSymbol("紫金", 5)
		if err != nil {
			t.Fatalf("SearchSymbol: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected results for 紫金")
		}
		for _, r := range results {
			t.Logf("  %s %s (%s)", r.Symbol, r.Name, r.Market)
		}
	})

	t.Run("SearchFutures", func(t *testing.T) {
		results, err := db.SearchSymbol("沪金", 1)
		if err != nil {
			t.Fatalf("SearchSymbol: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected 沪金 result")
		} else {
			t.Logf("  沪金 → %s (%s)", results[0].Symbol, results[0].Name)
		}
	})

	t.Run("GetQuote", func(t *testing.T) {
		quote, err := db.GetQuote("601899.SS")
		if err != nil {
			t.Fatalf("GetQuote: %v", err)
		}
		if quote.Close <= 0 {
			t.Errorf("expected positive close price, got %f", quote.Close)
		}
		t.Logf("  %s %s: %.2f (%+.2f%%)", quote.Symbol, quote.Name, quote.Close, quote.ChangePct)
	})

	t.Run("GetQuote_Futures", func(t *testing.T) {
		quote, err := db.GetQuote("FUT_AU0")
		if err != nil {
			t.Fatalf("GetQuote: %v", err)
		}
		t.Logf("  %s %s: %.2f", quote.Symbol, quote.Name, quote.Close)
	})

	t.Run("GetQuote_Crypto", func(t *testing.T) {
		quote, err := db.GetQuote("BTCUSDT")
		if err != nil {
			t.Fatalf("GetQuote: %v", err)
		}
		t.Logf("  %s %s: %.2f (%+.2f%%)", quote.Symbol, quote.Name, quote.Close, quote.ChangePct)
	})
}
