package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPlatformDeliveryDefaultsAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	if err := os.WriteFile(path, []byte(`
sources:
  - name: legacy
    type: rss
    sinks: [main]
  - name: batched
    type: rss
    delivery_mode: digest
    briefing_target: closing
    sinks: [main]
`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPlatform(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources[0].DeliveryMode != DeliveryDirect {
		t.Fatalf("legacy mode=%q", cfg.Sources[0].DeliveryMode)
	}
	if cfg.Sources[1].DeliveryMode != DeliveryDigest || cfg.Sources[1].BriefingTarget != "closing" {
		t.Fatalf("digest source=%+v", cfg.Sources[1])
	}

	if err := os.WriteFile(path, []byte(`
sources:
  - name: broken
    type: rss
    delivery_mode: digest
`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPlatform(path); err == nil {
		t.Fatal("digest source without target should fail validation")
	}
}

func TestConfigManagerUpdateDefaultsAndRejectsInvalidDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	if err := os.WriteFile(path, []byte("sources: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &PlatformConfig{Sources: []SourceConfig{{Name: "legacy", Type: "rss"}}}
	if err := manager.Update(legacy); err != nil {
		t.Fatal(err)
	}
	if manager.Get().Sources[0].DeliveryMode != DeliveryDirect {
		t.Fatalf("whole-config update did not default direct: %+v", manager.Get().Sources[0])
	}
	if legacy.Sources[0].DeliveryMode != "" {
		t.Fatalf("whole-config update mutated caller-owned config: %+v", legacy.Sources[0])
	}

	invalid := &PlatformConfig{Sources: []SourceConfig{{
		Name: "bad", Type: "rss", DeliveryMode: DeliveryDigest,
	}}}
	err = manager.Update(invalid)
	if err == nil || !IsValidationError(err) {
		t.Fatalf("invalid update error=%v", err)
	}
	if manager.Get().Sources[0].Name != "legacy" {
		t.Fatalf("invalid update replaced active config: %+v", manager.Get().Sources)
	}
}

func TestTypedSourceRoutingValidationAndLegacyCompatibility(t *testing.T) {
	score := 8.5
	valid := SourceConfig{Name: "typed", Type: "rss", Routing: &SourceRoutingConfig{
		AIPolicy: AIPolicyRequired, OnAIFailure: RouteDefaultSilent, Default: RouteDefaultSilent,
		Direct: &RouteBand{MinScore: &score, MaxPerDay: 4, Priority: 80},
	}}
	if _, err := NormalizeSourceConfig(valid); err != nil {
		t.Fatalf("valid routing: %v", err)
	}
	if valid.DeliveryMode != "" {
		t.Fatalf("typed routing unexpectedly defaulted legacy delivery: %+v", valid)
	}

	tests := []struct {
		name string
		src  SourceConfig
	}{
		{name: "legacy conflict", src: SourceConfig{Name: "bad", DeliveryMode: DeliveryDirect, Routing: valid.Routing}},
		{name: "failure direct", src: SourceConfig{Name: "bad", Routing: &SourceRoutingConfig{AIPolicy: AIPolicyRequired, OnAIFailure: DeliveryDirect, Default: RouteDefaultSilent, Direct: valid.Routing.Direct}}},
		{name: "non critical bypass direct", src: SourceConfig{Name: "bad", Routing: &SourceRoutingConfig{AIPolicy: AIPolicyBypass, OnAIFailure: RouteDefaultSilent, Default: RouteDefaultSilent, Direct: &RouteBand{MaxPerDay: 1}}}},
		{name: "required score", src: SourceConfig{Name: "bad", Routing: &SourceRoutingConfig{AIPolicy: AIPolicyRequired, OnAIFailure: RouteDefaultSilent, Default: RouteDefaultSilent, Direct: &RouteBand{MaxPerDay: 1}}}},
		{name: "score range", src: SourceConfig{Name: "bad", Routing: &SourceRoutingConfig{AIPolicy: AIPolicyRequired, OnAIFailure: RouteDefaultSilent, Default: RouteDefaultSilent, Direct: &RouteBand{MinScore: float64Ptr(11)}}}},
		{name: "negative quota", src: SourceConfig{Name: "bad", Routing: &SourceRoutingConfig{AIPolicy: AIPolicyRequired, OnAIFailure: RouteDefaultSilent, Default: RouteDefaultSilent, Direct: &RouteBand{MinScore: &score, MaxPerDay: -1}}}},
		{name: "priority range", src: SourceConfig{Name: "bad", Routing: &SourceRoutingConfig{AIPolicy: AIPolicyRequired, OnAIFailure: RouteDefaultSilent, Default: RouteDefaultSilent, Direct: &RouteBand{MinScore: &score, Priority: 101}}}},
		{name: "direct briefing", src: SourceConfig{Name: "bad", Routing: &SourceRoutingConfig{AIPolicy: AIPolicyRequired, OnAIFailure: RouteDefaultSilent, Default: RouteDefaultSilent, Direct: &RouteBand{MinScore: &score, BriefingTarget: "us_preview"}}}},
		{name: "digest briefing", src: SourceConfig{Name: "bad", Routing: &SourceRoutingConfig{AIPolicy: AIPolicyRequired, OnAIFailure: RouteDefaultDigest, Default: RouteDefaultDigest, Digest: &RouteBand{MinScore: &score, BriefingTarget: "later"}}}},
		{name: "no bands digest", src: SourceConfig{Name: "bad", Routing: &SourceRoutingConfig{AIPolicy: AIPolicyRequired, OnAIFailure: RouteDefaultSilent, Default: RouteDefaultDigest}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeSourceConfig(test.src); err == nil {
				t.Fatal("invalid routing was accepted")
			}
		})
	}

	silent := SourceConfig{Name: "energy", Routing: &SourceRoutingConfig{
		AIPolicy: AIPolicyRequired, OnAIFailure: RouteDefaultSilent, Default: RouteDefaultSilent,
	}}
	if _, err := NormalizeSourceConfig(silent); err != nil {
		t.Fatalf("silent-only routing: %v", err)
	}
}

func TestSourceRoutingDeepCopyDetachesScores(t *testing.T) {
	score := 7.0
	routing := &SourceRoutingConfig{
		AIPolicy: AIPolicyRequired, OnAIFailure: RouteDefaultSilent, Default: RouteDefaultSilent,
		CriticalKeywords: []string{"rate decision"},
		Digest:           &RouteBand{MinScore: &score, MaxPerDay: 3, BriefingTarget: "us_preview"},
	}
	cloned := routing.DeepCopy()
	*cloned.Digest.MinScore = 9
	cloned.Digest.MaxPerDay = 1
	cloned.CriticalKeywords[0] = "changed"
	if *routing.Digest.MinScore != 7 || routing.Digest.MaxPerDay != 3 {
		t.Fatalf("deep copy mutated original: %+v", routing.Digest)
	}
	if routing.CriticalKeywords[0] != "rate decision" {
		t.Fatalf("deep copy mutated original keywords: %v", routing.CriticalKeywords)
	}
}

func TestSourceRoutingCriticalKeywordNormalizationAndBounds(t *testing.T) {
	score := 8.0
	src := SourceConfig{Name: "events", Routing: &SourceRoutingConfig{
		CriticalKeywords: []string{"  sanctions  ", "SANCTIONS", "战争"},
		AIPolicy:         AIPolicyRequired,
		OnAIFailure:      RouteDefaultSilent,
		Default:          RouteDefaultSilent,
		Direct:           &RouteBand{MinScore: &score, MaxPerDay: 1},
	}}
	normalized, err := NormalizeSourceConfig(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := normalized.Routing.CriticalKeywords; len(got) != 2 || got[0] != "sanctions" || got[1] != "战争" {
		t.Fatalf("normalized keywords=%v", got)
	}
	if src.Routing.CriticalKeywords[0] != "  sanctions  " || len(src.Routing.CriticalKeywords) != 3 {
		t.Fatalf("normalization mutated caller: %v", src.Routing.CriticalKeywords)
	}

	tooMany := make([]string, 65)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("keyword-%d", i)
	}
	for _, keywords := range [][]string{{""}, {strings.Repeat("长", 129)}, tooMany} {
		routing := src.Routing.DeepCopy()
		routing.CriticalKeywords = keywords
		if err := ValidateSourceRouting("events", routing); err == nil {
			t.Fatalf("invalid critical keywords accepted: count=%d", len(keywords))
		}
	}
}

func TestSourceOptionsRejectRedactionSentinelsAndCredentialArguments(t *testing.T) {
	tests := []SourceConfig{
		{Name: "masked", Type: "rss", Options: map[string]interface{}{"api_key": "***"}},
		{Name: "command-flag", Type: "script", Options: map[string]interface{}{"command": "python3 report.py --api-key value"}},
		{Name: "command-assignment", Type: "script", Options: map[string]interface{}{"command": "python3 report.py --token=value"}},
		{Name: "args", Type: "script", Options: map[string]interface{}{"command": "python3 report.py", "args": []interface{}{"--secret", "value"}}},
	}
	for _, src := range tests {
		t.Run(src.Name, func(t *testing.T) {
			if _, err := NormalizeSourceConfig(src); err == nil || !IsValidationError(err) {
				t.Fatalf("error=%v, want validation error", err)
			}
		})
	}

	safe := SourceConfig{Name: "safe", Type: "script", Options: map[string]interface{}{
		"command": "python3 scripts/blockbeats_daily_reports.py --mode evening",
	}}
	if _, err := NormalizeSourceConfig(safe); err != nil {
		t.Fatalf("safe environment-backed command rejected: %v", err)
	}
}

func TestProductionTypedRoutingUsesNarrowCriticalPolicies(t *testing.T) {
	cfg, err := LoadPlatform(filepath.Join("..", "..", "config.platform.yaml.example"))
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]struct{}{
		"war": {}, "战争": {}, "actual": {}, "expected": {}, "forecast": {},
		"consensus": {}, "实际值": {}, "预期值": {},
	}
	externalRouted := 0
	sources := make(map[string]*SourceConfig, len(cfg.Sources))
	for i := range cfg.Sources {
		src := &cfg.Sources[i]
		sources[src.Name] = src
		if src.Routing == nil {
			continue
		}
		externalRouted++
		for _, keyword := range src.Routing.CriticalKeywords {
			if _, blocked := forbidden[strings.ToLower(keyword)]; blocked {
				t.Fatalf("source %s retained generic critical keyword %q", src.Name, keyword)
			}
		}
	}
	if externalRouted != 10 {
		t.Fatalf("typed external sources=%d, want 10 (8 RSS + fed-speeches + eia-energy)", externalRouted)
	}

	assertSourcePolicy(t, sources, "trump-rss", 300, nil, nil,
		false, AIPolicyRequired, RouteDefaultSilent, RouteDefaultSilent,
		&routeBandExpectation{minScore: float64Ptr(8.5), maxPerDay: 4, priority: 80},
		&routeBandExpectation{minScore: float64Ptr(6.5), maxPerDay: 2, priority: 40, briefingTarget: "news_aggregate"})
	assertSourcePolicy(t, sources, "bwe-tradfi", 60, nil, nil,
		false, AIPolicyRequired, RouteDefaultSilent, RouteDefaultSilent,
		&routeBandExpectation{minScore: float64Ptr(8.5), maxPerDay: 8, priority: 70},
		&routeBandExpectation{minScore: float64Ptr(7), maxPerDay: 3, priority: 30, briefingTarget: "news_aggregate"})
	assertSourcePolicy(t, sources, "bloomberg-markets", 300, nil, nil,
		false, AIPolicyRequired, RouteDefaultSilent, RouteDefaultSilent, nil,
		&routeBandExpectation{minScore: float64Ptr(8.5), maxPerDay: 3, priority: 50, briefingTarget: "news_aggregate"})
	assertSourcePolicy(t, sources, "bloomberg-economics", 300, nil, nil,
		false, AIPolicyRequired, RouteDefaultSilent, RouteDefaultSilent, nil,
		&routeBandExpectation{minScore: float64Ptr(8.5), maxPerDay: 2, priority: 50, briefingTarget: "news_aggregate"})
	assertSourcePolicy(t, sources, "bloomberg-politics", 300, nil, nil,
		false, AIPolicyRequired, RouteDefaultSilent, RouteDefaultSilent,
		&routeBandExpectation{minScore: float64Ptr(8.5), maxPerDay: 3, priority: 60},
		&routeBandExpectation{minScore: float64Ptr(8.5), maxPerDay: 2, priority: 40, briefingTarget: "news_aggregate"})
	assertSourcePolicy(t, sources, "forexlive-breaking", 120, nil, nil,
		false, AIPolicyRequired, RouteDefaultSilent, RouteDefaultSilent,
		&routeBandExpectation{minScore: float64Ptr(8), maxPerDay: 8, priority: 70},
		&routeBandExpectation{minScore: float64Ptr(6.5), maxPerDay: 3, priority: 30, briefingTarget: "news_aggregate"})
	// These policies are deliberately unchanged by the source tuning above.
	assertSourcePolicy(t, sources, "kobeissi-letter", 300, nil, nil,
		false, AIPolicyRequired, RouteDefaultSilent, RouteDefaultSilent,
		&routeBandExpectation{minScore: float64Ptr(9), maxPerDay: 2, priority: 60},
		&routeBandExpectation{minScore: float64Ptr(7), maxPerDay: 3, priority: 40, briefingTarget: "news_aggregate"})
	assertSourcePolicy(t, sources, "fed-press", 900, nil, nil,
		true, AIPolicyBypass, RouteDefaultSilent, RouteDefaultSilent,
		&routeBandExpectation{maxPerDay: 0, priority: 100}, nil)
}

type routeBandExpectation struct {
	minScore       *float64
	maxPerDay      int
	priority       int
	briefingTarget string
}

func assertSourcePolicy(t *testing.T, sources map[string]*SourceConfig, name string, interval int, schedule []string, enabled *bool, critical bool, aiPolicy, onAIFailure, defaultRoute string, direct, digest *routeBandExpectation) {
	t.Helper()
	src := sources[name]
	if src == nil {
		t.Fatalf("source %q is missing", name)
	}
	if src.Interval != interval || !reflect.DeepEqual(src.Schedule, schedule) || !reflect.DeepEqual(src.Enabled, enabled) || src.TradingDaysOnly {
		t.Fatalf("source %s runtime flags interval=%d schedule=%v enabled=%v trading_days_only=%v", name, src.Interval, src.Schedule, src.IsEnabled(), src.TradingDaysOnly)
	}
	if src.Routing == nil || src.Routing.Critical != critical || src.Routing.AIPolicy != aiPolicy || src.Routing.OnAIFailure != onAIFailure || src.Routing.Default != defaultRoute {
		t.Fatalf("source %s routing=%+v", name, src.Routing)
	}
	assertRouteBand(t, name+" direct", src.Routing.Direct, direct)
	assertRouteBand(t, name+" digest", src.Routing.Digest, digest)
}

func assertRouteBand(t *testing.T, name string, got *RouteBand, want *routeBandExpectation) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("%s=%+v, want nil", name, got)
		}
		return
	}
	if got == nil || !reflect.DeepEqual(got.MinScore, want.minScore) || got.MaxPerDay != want.maxPerDay ||
		got.Priority != want.priority || got.BriefingTarget != want.briefingTarget {
		t.Fatalf("%s=%+v, want %+v", name, got, want)
	}
}

func float64Ptr(value float64) *float64 { return &value }

