package source

import (
	"context"
	"os"
	"strconv"
	"testing"
)

// TestFetchFREDObservations exercises the live shared datasources/fred path
// (keyed api.stlouisfed.org). Skips when FRED_API_KEY is unset.
func TestFetchFREDObservations(t *testing.T) {
	key := os.Getenv("FRED_API_KEY")
	if key == "" {
		t.Skip("FRED_API_KEY unset")
	}
	s := &USMacroReportSource{apiKey: key}
	obs, err := s.fetchFREDObservations(context.Background(), "DGS10", 2)
	if err != nil {
		t.Skipf("FRED unavailable: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("want 2 obs, got %d", len(obs))
	}
	// newest first
	if obs[0].Date < obs[1].Date {
		t.Fatalf("expected newest-first, got %s before %s", obs[0].Date, obs[1].Date)
	}
	if obs[0].Value != "." {
		if _, err := strconv.ParseFloat(obs[0].Value, 64); err != nil {
			t.Fatalf("value not parseable: %q", obs[0].Value)
		}
	}
}
