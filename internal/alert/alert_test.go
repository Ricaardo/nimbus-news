package alert

import (
	"context"
	"testing"
	"time"
)

func TestAlertTypes(t *testing.T) {
	// Test alert type constants
	if AlertTypePrice != "price" {
		t.Errorf("AlertTypePrice = %s, want price", AlertTypePrice)
	}
	if AlertTypeChange != "change" {
		t.Errorf("AlertTypeChange = %s, want change", AlertTypeChange)
	}
	if AlertTypeNews != "news" {
		t.Errorf("AlertTypeNews = %s, want news", AlertTypeNews)
	}
}

func TestAlertConditions(t *testing.T) {
	// Test condition constants
	if ConditionAbove != "above" {
		t.Errorf("ConditionAbove = %s, want above", ConditionAbove)
	}
	if ConditionBelow != "below" {
		t.Errorf("ConditionBelow = %s, want below", ConditionBelow)
	}
	if ConditionCross != "cross" {
		t.Errorf("ConditionCross = %s, want cross", ConditionCross)
	}
}

func TestAlert_IsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{
			name:      "no expiration",
			expiresAt: time.Time{},
			want:      false,
		},
		{
			name:      "not expired",
			expiresAt: time.Now().Add(time.Hour),
			want:      false,
		},
		{
			name:      "expired",
			expiresAt: time.Now().Add(-time.Hour),
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Alert{ExpiresAt: tt.expiresAt}
			if got := a.IsExpired(); got != tt.want {
				t.Errorf("Alert.IsExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAlert_InCooldown(t *testing.T) {
	tests := []struct {
		name          string
		cooldown      time.Duration
		lastTriggered time.Time
		want          bool
	}{
		{
			name:          "no cooldown",
			cooldown:      0,
			lastTriggered: time.Now(),
			want:          false,
		},
		{
			name:          "in cooldown",
			cooldown:      time.Hour,
			lastTriggered: time.Now().Add(-time.Minute),
			want:          true,
		},
		{
			name:          "cooldown passed",
			cooldown:      time.Minute,
			lastTriggered: time.Now().Add(-time.Hour),
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Alert{
				Cooldown:      tt.cooldown,
				LastTriggered: tt.lastTriggered,
			}
			if got := a.InCooldown(); got != tt.want {
				t.Errorf("Alert.InCooldown() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConditionText(t *testing.T) {
	tests := []struct {
		cond AlertCondition
		want string
	}{
		{ConditionAbove, "高于"},
		{ConditionBelow, "低于"},
		{ConditionCross, "触及"},
		{ConditionChangeUp, "涨幅达"},
		{ConditionChangeDown, "跌幅达"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.cond), func(t *testing.T) {
			if got := ConditionText(tt.cond); got != tt.want {
				t.Errorf("ConditionText(%s) = %s, want %s", tt.cond, got, tt.want)
			}
		})
	}
}

func TestAlertTypeText(t *testing.T) {
	tests := []struct {
		t    AlertType
		want string
	}{
		{AlertTypePrice, "价格"},
		{AlertTypeChange, "涨跌幅"},
		{AlertTypeNews, "新闻"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.t), func(t *testing.T) {
			if got := AlertTypeText(tt.t); got != tt.want {
				t.Errorf("AlertTypeText(%s) = %s, want %s", tt.t, got, tt.want)
			}
		})
	}
}

func TestEngineConfig_Defaults(t *testing.T) {
	cfg := DefaultEngineConfig()

	if cfg.CheckInterval != 30*time.Second {
		t.Errorf("CheckInterval = %v, want 30s", cfg.CheckInterval)
	}
	if cfg.MaxAlertsPerUser != 50 {
		t.Errorf("MaxAlertsPerUser = %d, want 50", cfg.MaxAlertsPerUser)
	}
	if cfg.DefaultCooldown != 5*time.Minute {
		t.Errorf("DefaultCooldown = %v, want 5m", cfg.DefaultCooldown)
	}
	if !cfg.Enabled {
		t.Error("Enabled should be true by default")
	}
}

// MockMarketProvider for testing
type MockMarketProvider struct {
	prices map[string]float64
}

func (m *MockMarketProvider) GetPrice(ctx context.Context, symbol string) (float64, error) {
	if price, ok := m.prices[symbol]; ok {
		return price, nil
	}
	return 0, nil
}

func TestEngine_ShouldTrigger(t *testing.T) {
	engine := &Engine{
		config:      DefaultEngineConfig(),
		lastTrigger: make(map[string]time.Time),
	}

	tests := []struct {
		name         string
		alert        *Alert
		currentPrice float64
		want         bool
	}{
		{
			name: "above - should trigger",
			alert: &Alert{
				ID:        "1",
				Condition: ConditionAbove,
				Value:     100,
			},
			currentPrice: 110,
			want:         true,
		},
		{
			name: "above - should not trigger",
			alert: &Alert{
				ID:        "2",
				Condition: ConditionAbove,
				Value:     100,
			},
			currentPrice: 90,
			want:         false,
		},
		{
			name: "below - should trigger",
			alert: &Alert{
				ID:        "3",
				Condition: ConditionBelow,
				Value:     100,
			},
			currentPrice: 90,
			want:         true,
		},
		{
			name: "below - should not trigger",
			alert: &Alert{
				ID:        "4",
				Condition: ConditionBelow,
				Value:     100,
			},
			currentPrice: 110,
			want:         false,
		},
		{
			name: "cross - should trigger",
			alert: &Alert{
				ID:        "5",
				Condition: ConditionCross,
				Value:     100,
			},
			currentPrice: 100,
			want:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := engine.shouldTrigger(tt.alert, tt.currentPrice); got != tt.want {
				t.Errorf("shouldTrigger() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{
			name:   "short string",
			s:      "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "exact length",
			s:      "hello",
			maxLen: 5,
			want:   "hello",
		},
		{
			name:   "long string",
			s:      "hello world",
			maxLen: 5,
			want:   "hello...",
		},
		{
			name:   "chinese string",
			s:      "你好世界",
			maxLen: 2,
			want:   "你好...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateString(tt.s, tt.maxLen); got != tt.want {
				t.Errorf("truncateString() = %v, want %v", got, tt.want)
			}
		})
	}
}
