package llm

import (
	"context"
	"reflect"
	"testing"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// ---------------------------------------------------------------------------
// parseTickerLine
// ---------------------------------------------------------------------------

func TestParseTickerLine_Dash(t *testing.T) {
	if got := parseTickerLine("-"); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestParseTickerLine_Empty(t *testing.T) {
	if got := parseTickerLine(""); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestParseTickerLine_Basic(t *testing.T) {
	got := parseTickerLine("PLTR, NVDA")
	want := []string{"PLTR", "NVDA"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseTickerLine_ChineseSeparators(t *testing.T) {
	got := parseTickerLine("AAPL，TSLA、MSFT")
	want := []string{"AAPL", "TSLA", "MSFT"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseTickerLine_Dedup(t *testing.T) {
	got := parseTickerLine("NVDA, NVDA, AAPL")
	want := []string{"NVDA", "AAPL"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseTickerLine_TruncatesAt5(t *testing.T) {
	got := parseTickerLine("A, B, C, D, E, F")
	if len(got) != 5 {
		t.Errorf("expected 5, got %d: %v", len(got), got)
	}
}

func TestParseTickerLine_FiltersChinese(t *testing.T) {
	// Chinese company names and lowercase must be dropped
	got := parseTickerLine("英伟达, nvda, NVDA")
	want := []string{"NVDA"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseTickerLine_FiltersSentence(t *testing.T) {
	// Sentences longer than 5 chars or with non-alpha chars are dropped by the regex
	got := parseTickerLine("TOOLONG, AAPL")
	want := []string{"AAPL"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// parseImpact
// ---------------------------------------------------------------------------

func TestParseImpact_Bullish(t *testing.T) {
	cases := []string{
		"利好 | 板块: 科技 | 标的: NVDA | 影响文字",
		"利好",
	}
	for _, c := range cases {
		if got := parseImpact(c); got != "利好" {
			t.Errorf("input=%q: got %q, want 利好", c, got)
		}
	}
}

func TestParseImpact_Bearish(t *testing.T) {
	if got := parseImpact("利空 | 板块: 能源 | ..."); got != "利空" {
		t.Errorf("got %q", got)
	}
}

func TestParseImpact_Neutral(t *testing.T) {
	if got := parseImpact("中性 | 板块: 消费 | ..."); got != "中性" {
		t.Errorf("got %q", got)
	}
}

func TestParseImpact_Garbage(t *testing.T) {
	if got := parseImpact("乱码随机文字"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// gateTickers
// ---------------------------------------------------------------------------

func TestGateTickers_EmptyInput(t *testing.T) {
	if got := gateTickers("title", "content", nil); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestGateTickers_FiltersMismatch(t *testing.T) {
	// NVDA not in text → removed; PLTR is → kept
	got := gateTickers("Palantir (PLTR) earnings beat", "Details here.", []string{"PLTR", "NVDA"})
	want := []string{"PLTR"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGateTickers_CaseInsensitive(t *testing.T) {
	// "nvidia nvda corp" contains lowercase "nvda", ticker "NVDA" must match
	got := gateTickers("nvidia nvda corp quarterly results", "", []string{"NVDA"})
	want := []string{"NVDA"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGateTickers_AllFiltered(t *testing.T) {
	// None of the tickers appear in text → nil
	got := gateTickers("market news", "no symbols here", []string{"PLTR", "NVDA"})
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// parseEnhanceResponse — full 5-segment
// ---------------------------------------------------------------------------

func TestParseEnhanceResponse_Full5Segments(t *testing.T) {
	resp := `【标题】帕兰提尔季报超预期
【正文】帕兰提尔科技公司第二季度营收超市场预期。
【评】利好 | 板块: 科技 | 标的: PLTR | 业绩超预期提振股价。
【代码】PLTR, NVDA
【相关】财经`

	res := parseEnhanceResponse(resp)
	if res.Title != "帕兰提尔季报超预期" {
		t.Errorf("Title: %q", res.Title)
	}
	if res.Content != "帕兰提尔科技公司第二季度营收超市场预期。" {
		t.Errorf("Content: %q", res.Content)
	}
	if res.Impact != "利好" {
		t.Errorf("Impact: %q", res.Impact)
	}
	if !reflect.DeepEqual(res.Tickers, []string{"PLTR", "NVDA"}) {
		t.Errorf("Tickers: %v", res.Tickers)
	}
	if res.NotFinance {
		t.Error("NotFinance should be false for 财经")
	}
}

func TestParseEnhanceResponse_NotFinance(t *testing.T) {
	resp := `【标题】某明星八卦消息
【正文】-
【评】中性 | 板块: 无 | 标的: 无 | 与市场无关。
【代码】-
【相关】非财经`

	res := parseEnhanceResponse(resp)
	if !res.NotFinance {
		t.Error("NotFinance should be true")
	}
	if res.Tickers != nil {
		t.Errorf("Tickers should be nil, got %v", res.Tickers)
	}
}

func TestParseEnhanceResponse_Backward3Segments(t *testing.T) {
	// Old 3-segment response: no 【代码】 or 【相关】 → Tickers nil, NotFinance false, Impact parsed from 【评】
	resp := `【标题】旧格式标题
【正文】旧格式正文内容。
【评】利空 | 板块: 能源 | 标的: XOM | 油价下跌影响。`

	res := parseEnhanceResponse(resp)
	if res.Tickers != nil {
		t.Errorf("expected nil Tickers, got %v", res.Tickers)
	}
	if res.NotFinance {
		t.Error("NotFinance should be false (fail-open)")
	}
	if res.Impact != "利空" {
		t.Errorf("Impact: %q", res.Impact)
	}
	if res.Title != "旧格式标题" {
		t.Errorf("Title: %q", res.Title)
	}
}

func TestParseEnhanceResponse_MissingRelevance(t *testing.T) {
	// 【相关】 absent → fail-open, NotFinance=false
	resp := `【标题】测试
【正文】-
【评】中性 | 板块: X | 标的: X | 测试。
【代码】-`

	res := parseEnhanceResponse(resp)
	if res.NotFinance {
		t.Error("missing 【相关】 should default to false (fail-open)")
	}
}

// ---------------------------------------------------------------------------
// Key mismatch-correction scenario (the main bug fix)
// ---------------------------------------------------------------------------

func TestGateTickers_MismatchCorrection(t *testing.T) {
	// parseEnhanceResponse extracts [PLTR, NVDA]; original text only contains PLTR
	// gateTickers should keep only PLTR
	title := "Palantir (PLTR) earnings beat expectations in Q2"
	content := "Palantir Technologies reported strong results."

	tickers := []string{"PLTR", "NVDA"}
	got := gateTickers(title, content, tickers)
	want := []string{"PLTR"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGateTickers_PureMacro(t *testing.T) {
	// 【代码】- → parseTickerLine returns nil → gateTickers returns nil
	got := gateTickers("Fed keeps rates unchanged", "FOMC meeting outcome.", parseTickerLine("-"))
	if got != nil {
		t.Errorf("expected nil for pure macro, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// gateTickers — word-boundary fix (Fix 2)
// ---------------------------------------------------------------------------

func TestGateTickers_SingleLetterNoFalseMatch(t *testing.T) {
	// "F" must NOT match "for the win" (substring without word boundary)
	got := gateTickers("for the win", "", []string{"F"})
	if got != nil {
		t.Errorf("expected nil, got %v (false positive: 'F' matched inside 'for')", got)
	}
}

func TestGateTickers_SingleLetterParenMatch(t *testing.T) {
	// "F" MUST match "Ford (F) shares" (word boundary via parentheses)
	got := gateTickers("Ford (F) shares", "", []string{"F"})
	want := []string{"F"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGateTickers_SubstringNoFalseMatch(t *testing.T) {
	// "ALL" must NOT match "really" (substring)
	got := gateTickers("I really like it", "", []string{"ALL"})
	if got != nil {
		t.Errorf("expected nil, got %v (false positive: 'ALL' matched inside 'really')", got)
	}
}

// ---------------------------------------------------------------------------
// fakeProvider — minimal stub for EnhanceBatch integration tests
// ---------------------------------------------------------------------------

// fakeProvider implements Provider; Chat returns chatResp, ChatHeavy returns heavyResp.
type fakeProvider struct {
	chatResp  string
	heavyResp string
}

func (f *fakeProvider) Chat(_ context.Context, _ []Message) (string, error) {
	return f.chatResp, nil
}

func (f *fakeProvider) ChatHeavy(_ context.Context, _ []Message) (string, error) {
	return f.heavyResp, nil
}

func (f *fakeProvider) ChatWithTools(_ context.Context, _ []Message, _ []map[string]interface{}) (*Message, error) {
	return nil, nil
}

func (f *fakeProvider) Think(_ context.Context, _ []Message, _ string) (string, string, error) {
	return "", "", nil
}

func newTestEnhancer(chatResp, heavyResp string) *NewsEnhancer {
	p := &fakeProvider{chatResp: chatResp, heavyResp: heavyResp}
	return NewNewsEnhancer(p, EnhancerConfig{Enabled: true})
}

// ---------------------------------------------------------------------------
// EnhanceBatch integration tests
// ---------------------------------------------------------------------------

// B1 regression: structured report Tags must NOT be cleared by EnhanceBatch.
func TestEnhanceBatch_B1_StructuredReportTagsPreserved(t *testing.T) {
	// assessReport (ChatHeavy) returns a non-empty comment so the goroutine proceeds.
	e := newTestEnhancer("", "市场情绪偏谨慎，关注支撑位。")
	msg := model.NewNewsMessage("观复BTC读盘", "比特币当前处于关键支撑位。")
	msg.SourceType = "guanfu"
	msg.Tags = []string{"crypto", "btc", "观复"}

	e.EnhanceBatch(context.Background(), []*model.Message{msg}, 1)

	want := []string{"crypto", "btc", "观复"}
	if !reflect.DeepEqual(msg.Tags, want) {
		t.Errorf("B1 regression: Tags after EnhanceBatch = %v, want %v", msg.Tags, want)
	}
}

// Non-structured path: Tags should be set to gated tickers from the LLM response.
func TestEnhanceBatch_NonStructured_TagsSetToGatedTickers(t *testing.T) {
	llmResp := `【标题】帕兰提尔季报超预期
【正文】帕兰提尔科技公司季报超预期。
【评】利好 | 板块: 科技 | 标的: PLTR | 业绩超预期。
【代码】PLTR
【相关】财经`
	e := newTestEnhancer(llmResp, "")
	msg := model.NewNewsMessage("Palantir (PLTR) earnings beat", "Palantir reported strong results.")
	msg.SourceType = "rss"

	e.EnhanceBatch(context.Background(), []*model.Message{msg}, 1)

	want := []string{"PLTR"}
	if !reflect.DeepEqual(msg.Tags, want) {
		t.Errorf("Tags = %v, want %v", msg.Tags, want)
	}
}

// NotFinance: LLM says 非财经 → drop=1, message otherwise untouched.
func TestEnhanceBatch_NotFinance_SetsDropFlag(t *testing.T) {
	llmResp := `【标题】某明星八卦
【正文】-
【评】中性 | 板块: 无 | 标的: 无 | 无关。
【代码】-
【相关】非财经`
	e := newTestEnhancer(llmResp, "")
	msg := model.NewNewsMessage("Celebrity gossip", "Star spotted at party.")
	msg.SourceType = "rss"

	e.EnhanceBatch(context.Background(), []*model.Message{msg}, 1)

	if msg.GetStringMetadata("drop") != "1" {
		t.Errorf("expected drop=1 for non-finance news, got %q", msg.GetStringMetadata("drop"))
	}
}

// Finance: LLM says 财经 → drop stays empty.
func TestEnhanceBatch_Finance_NoDrop(t *testing.T) {
	llmResp := `【标题】市场消息
【正文】市场上涨。
【评】利好 | 板块: 科技 | 标的: NVDA | 推动科技股上行。
【代码】NVDA
【相关】财经`
	e := newTestEnhancer(llmResp, "")
	msg := model.NewNewsMessage("NVIDIA (NVDA) surges", "NVDA reached a new high.")
	msg.SourceType = "rss"

	e.EnhanceBatch(context.Background(), []*model.Message{msg}, 1)

	if msg.GetStringMetadata("drop") != "" {
		t.Errorf("expected drop empty for finance news, got %q", msg.GetStringMetadata("drop"))
	}
}
