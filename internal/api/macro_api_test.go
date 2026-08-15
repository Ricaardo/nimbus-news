package api

import (
	"testing"

	dsfred "github.com/Ricaardo/nimbus-os/datasources/fred"
)

// TestParseFloatValue FRED 观测值解析边界:缺失/点/科学计数/负数。
func TestParseFloatValue(t *testing.T) {
	cases := map[string]struct {
		in      string
		want    float64
		wantErr bool
	}{
		"normal":     {"3.63", 3.63, false},
		"negative":   {"-77585.0", -77585, false},
		"dot":        {".", 0, true},
		"empty":      {"", 0, true},
		"scientific": {"1.5e3", 1500, false},
	}
	for name, c := range cases {
		got, err := parseFloatValue(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%s: expected error, got %v", name, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Fatalf("%s: got %v, %v; want %v", name, got, err, c.want)
		}
	}
}

// TestObservationsToPoints 反转逻辑:最新在前 → 时间正序,并跳过缺失值。
func TestObservationsToPoints(t *testing.T) {
	obs := []dsfred.Observation{
		{Date: "2026-07-01", Value: "4.8"},
		{Date: "2026-06-01", Value: "."}, // 缺失值必须跳过
		{Date: "2026-05-01", Value: "4.5"},
	}
	pts := observationsToPoints(obs, true)
	if len(pts) != 2 {
		t.Fatalf("points=%d, want 2 (missing skipped)", len(pts))
	}
	if pts[0].Date != "2026-05-01" || pts[0].Value != 4.5 {
		t.Fatalf("first=%+v, want 2026-05-01/4.5", pts[0])
	}
	if pts[1].Date != "2026-07-01" || pts[1].Value != 4.8 {
		t.Fatalf("last=%+v, want 2026-07-01/4.8", pts[1])
	}

	// 已是正序时保持
	pts = observationsToPoints(obs, false)
	if pts[0].Date != "2026-07-01" {
		t.Fatalf("descToAsc=false must keep order, got %+v", pts[0])
	}
	if len(pts) != 2 {
		t.Fatalf("missing must still be skipped, got %d", len(pts))
	}
}

// TestMacroOverviewSeriesNoDuplicates 概览序列清单无重复(28 项并发拉取的顺序稳定前提)。
func TestMacroOverviewSeriesNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, sid := range macroOverviewSeries {
		if seen[sid] {
			t.Fatalf("duplicate series in overview: %s", sid)
		}
		seen[sid] = true
	}
	if len(macroOverviewSeries) < 20 {
		t.Fatalf("overview series=%d, want >=20", len(macroOverviewSeries))
	}
	// 特殊计算序列必须在清单中
	for _, sid := range []string{"BUFFETT_INDEX", "LABOR_GAP"} {
		if !seen[sid] {
			t.Fatalf("computed series %s missing from overview", sid)
		}
	}
}

