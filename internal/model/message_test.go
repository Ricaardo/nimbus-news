package model

import "testing"

func TestMarketBriefingIsStructuredReport(t *testing.T) {
	if !IsStructuredReport("market-briefing") {
		t.Fatal("market-briefing should use structured report handling")
	}
}
