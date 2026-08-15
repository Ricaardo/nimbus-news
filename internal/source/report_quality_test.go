package source

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Ricaardo/nimbus-os/news/internal/llm"
)

func TestFormatMacroValueUsesFREDSeriesUnits(t *testing.T) {
	tests := map[string]string{
		"WALCL":    "6.74万亿美元",
		"M2SL":     "21.50万亿美元",
		"CPIAUCSL": "323.1（指数）",
		"PCEPI":    "125.4（指数）",
		"T10Y2Y":   "0.42个百分点",
	}
	values := map[string]float64{
		"WALCL": 6_738_000, "M2SL": 21_500, "CPIAUCSL": 323.1,
		"PCEPI": 125.4, "T10Y2Y": 0.42,
	}
	for series, want := range tests {
		if got := formatMacroValue(series, values[series]); got != want {
			t.Errorf("formatMacroValue(%s) = %q, want %q", series, got, want)
		}
	}
}

func TestNewUSMacroPointSkipsMissingAndSameDateRevision(t *testing.T) {
	point, ok := newUSMacroPoint("CPIAUCSL", []fredObs{
		{Date: "2026-07-01", Value: "."},
		{Date: "2026-06-01", Value: "320"},
		{Date: "2026-06-01", Value: "319"},
		{Date: "2026-05-01", Value: "316"},
	})
	if !ok {
		t.Fatal("expected valid point")
	}
	if point.Date != "2026-06-01" || point.Value != 320 {
		t.Fatalf("latest valid point = %#v", point)
	}
	if !point.HasChange || point.ChangePct < 1.26 || point.ChangePct > 1.27 {
		t.Fatalf("change should compare against prior date, got %#v", point)
	}
}

func TestUSMacroRenderLabelsIndexAndPriorChange(t *testing.T) {
	s := &USMacroReportSource{}
	content := s.render([]*usMacroPoint{{
		SeriesID: "CPIAUCSL", NameCN: "CPI", Category: "通胀",
		Value: 323.1, ChangePct: 0.25, HasChange: true,
	}}, nil, nil, nil, nil, "", "")
	if !strings.Contains(content, "CPI 323.1（指数） (较前值 +0.25%)") {
		t.Fatalf("unexpected macro rendering:\n%s", content)
	}
	if strings.Contains(content, "同比") {
		t.Fatalf("index level must not be labeled year-over-year:\n%s", content)
	}
}

func TestClosingScanSummaryIsBoundedTypedOutput(t *testing.T) {
	fixture := `{
		"type":"closing_scan_report",
		"market_temp":{"zt_count":42,"dt_count":3,"today_break_ratio":0.125,"main_theme":["机器人","芯片"]},
		"candidates":[{"code":"600001","name":"示例股份","streak":3,"seal_amount":999999999,"score":88,
			"prediction":{"bucket":{"open_median":2.4,"win_rate":0.65,"n_samples":20},"confidence":"高"},
			"warnings":["高位波动","封单减弱"],"raw_secret":"must-not-leak"}]
	}`
	results := map[string]*ScriptResult{"closing_scan": {Parsed: json.RawMessage(fixture)}}
	var report closingScanReport
	if err := GetJSON(results, "closing_scan", &report); err != nil {
		t.Fatal(err)
	}
	want := "市场温度: 涨停 42 / 跌停 3 / 炸板率 12% / 主线 机器人、芯片\n" +
		"• [600001] 示例股份 3板 | 评分 88 | 置信度 高 | 分桶次日开盘中位数 +2.4%、胜率 65%（20样本） | 风险 高位波动；封单减弱"
	if got := formatClosingScanSummary(&report, 5); got != want {
		t.Fatalf("summary mismatch\ngot:  %s\nwant: %s", got, want)
	}

	content := (&MarketBriefingSource{}).renderClosing(
		&marketSentimentReport{}, &hotSectorEveningReport{}, &lhbEveningReport{},
		&capitalFlowEveningReport{}, &report, false, "")
	assertNoClosingRawJSON(t, content)
	if !strings.Contains(content, want) {
		t.Fatalf("rendered content missing typed summary:\n%s", content)
	}
}

func TestClosingNarrativePromptDoesNotContainRawJSON(t *testing.T) {
	provider := &captureLLMProvider{}
	s := &MarketBriefingSource{name: "closing-test", llmProvider: provider}
	report := &closingScanReport{
		MarketTemp: closingScanMarketTemp{LimitUpCount: 20, BreakRatio: 0.2},
		Candidates: []closingScanCandidate{{
			Code: "000001", Name: "平安银行", Streak: 1, Score: 70,
			Prediction: closingScanPrediction{Confidence: "中", KNN: &closingScanPredictionStats{
				OpenMedian: 1.2, WinRate: 0.55, Samples: 20,
			}},
			Warnings: []string{"样本有限"},
		}},
	}
	s.closingNarrative(context.Background(), &marketSentimentReport{}, &hotSectorEveningReport{},
		&lhbEveningReport{}, &capitalFlowEveningReport{}, report)
	assertNoClosingRawJSON(t, provider.prompt)
	if !strings.Contains(provider.prompt, "[000001] 平安银行 1板") {
		t.Fatalf("prompt missing typed candidate summary:\n%s", provider.prompt)
	}
}

func TestClosingRenderReportsParseFailureBriefly(t *testing.T) {
	content := (&MarketBriefingSource{}).renderClosing(
		&marketSentimentReport{}, &hotSectorEveningReport{}, &lhbEveningReport{},
		&capitalFlowEveningReport{}, nil, true, "")
	if !strings.Contains(content, "预测数据暂不可用") {
		t.Fatalf("missing unavailable notice:\n%s", content)
	}
}

func assertNoClosingRawJSON(t *testing.T, text string) {
	t.Helper()
	for _, forbidden := range []string{"seal_amount", "raw_secret", `"candidates"`, `{"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("output leaked raw JSON field %q:\n%s", forbidden, text)
		}
	}
}

type captureLLMProvider struct{ prompt string }

func (p *captureLLMProvider) Chat(context.Context, []llm.Message) (string, error) { return "", nil }
func (p *captureLLMProvider) ChatHeavy(context.Context, []llm.Message) (string, error) {
	return "", nil
}
func (p *captureLLMProvider) ChatWithTools(context.Context, []llm.Message, []map[string]interface{}) (*llm.Message, error) {
	return nil, nil
}
func (p *captureLLMProvider) Think(_ context.Context, messages []llm.Message, _ string) (string, string, error) {
	if len(messages) > 0 {
		p.prompt = messages[0].Content
	}
	return "ok", "", nil
}
