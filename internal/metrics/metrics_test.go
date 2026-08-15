package metrics

import "testing"

func TestDigestSinkLabelIsBounded(t *testing.T) {
	if got := DigestSinkLabel("wechat-main"); got != "wechat-main" {
		t.Fatalf("known sink label=%q", got)
	}
	if got := DigestSinkLabel("delivery-id-from-untrusted-input"); got != "other" {
		t.Fatalf("unknown sink label=%q", got)
	}
}

func TestHealthStatusValue(t *testing.T) {
	tests := map[string]float64{
		"healthy":           0,
		"degraded":          1,
		"unhealthy":         2,
		"disabled":          3,
		"awaiting_schedule": 4,
		"unknown":           -1,
	}
	for status, want := range tests {
		if got := HealthStatusValue(status); got != want {
			t.Errorf("HealthStatusValue(%q)=%v want=%v", status, got, want)
		}
	}
}
