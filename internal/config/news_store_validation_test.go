package config

import "testing"

func TestNormalizePlatformConfigNewsStoreDefaultsAndBounds(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		var cfg PlatformConfig
		if err := NormalizePlatformConfig(&cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.Store.News.MaxItems != defaultNewsStoreMaxItems {
			t.Fatalf("max_items=%d want=%d", cfg.Store.News.MaxItems, defaultNewsStoreMaxItems)
		}
		if cfg.Store.News.TTL != defaultNewsStoreTTL {
			t.Fatalf("ttl=%d want=%d", cfg.Store.News.TTL, defaultNewsStoreTTL)
		}
	})

	for _, tt := range []struct {
		name     string
		maxItems int
		ttl      int
		wantErr  bool
	}{
		{name: "minimum", maxItems: 1, ttl: 1},
		{name: "maximum", maxItems: maxNewsStoreMaxItems, ttl: maxNewsStoreTTL},
		{name: "negative max items", maxItems: -1, ttl: 1, wantErr: true},
		{name: "excessive max items", maxItems: maxNewsStoreMaxItems + 1, ttl: 1, wantErr: true},
		{name: "negative ttl", maxItems: 1, ttl: -1, wantErr: true},
		{name: "excessive ttl", maxItems: 1, ttl: maxNewsStoreTTL + 1, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := PlatformConfig{
				Store: PlatformStoreConfig{
					News: NewsStoreConfig{MaxItems: tt.maxItems, TTL: tt.ttl},
				},
			}
			err := NormalizePlatformConfig(&cfg)
			if tt.wantErr && !IsValidationError(err) {
				t.Fatalf("err=%v want validation error", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}
