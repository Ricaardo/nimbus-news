package filter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// fakeProvider is a minimal llm.Provider test double with a scripted Chat response.
type fakeProvider struct {
	resp  string
	err   error
	calls int32
}

func (p *fakeProvider) Chat(ctx context.Context, messages []llm.Message) (string, error) {
	atomic.AddInt32(&p.calls, 1)
	if p.err != nil {
		return "", p.err
	}
	return p.resp, nil
}

func (p *fakeProvider) ChatHeavy(ctx context.Context, messages []llm.Message) (string, error) {
	return p.Chat(ctx, messages)
}

func (p *fakeProvider) ChatWithTools(ctx context.Context, messages []llm.Message, tools []map[string]interface{}) (*llm.Message, error) {
	return nil, nil
}

func (p *fakeProvider) Think(ctx context.Context, messages []llm.Message, mode string) (string, string, error) {
	return "", "", nil
}

func (p *fakeProvider) callCount() int {
	return int(atomic.LoadInt32(&p.calls))
}

func newTestMsg(title string) *model.Message {
	return model.NewNewsMessage(title, title+" 正文内容")
}

func TestAIFilter_ShouldFilter(t *testing.T) {
	cases := []struct {
		name        string
		resp        string
		err         error
		wantBlocked bool
	}{
		{
			name:        "block on category 无价值",
			resp:        "3|无价值|无实质内容",
			wantBlocked: true,
		},
		{
			name:        "block on score below threshold",
			resp:        "4|一般|背景信息不足",
			wantBlocked: true,
		},
		{
			name:        "pass on llm error",
			err:         errors.New("network error"),
			wantBlocked: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fakeProvider{resp: tc.resp, err: tc.err}
			f := NewAIFilter(provider, AIFilterConfig{
				Enabled:         true,
				ThresholdScore:  5,
				MaxPerMinute:    100,
				BlockCategories: []string{"无价值", "无关"},
				TargetSources:   []string{"src-a"},
			})
			msg := newTestMsg(tc.name)
			blocked := f.ShouldFilter("src-a", "sink1", msg)
			if blocked != tc.wantBlocked {
				t.Errorf("ShouldFilter() = %v, want %v", blocked, tc.wantBlocked)
			}
		})
	}
}

func TestAIFilter_MaxPerMinuteCap(t *testing.T) {
	provider := &fakeProvider{resp: "8|重要|影响科技行业"}
	f := NewAIFilter(provider, AIFilterConfig{
		Enabled:         true,
		ThresholdScore:  5,
		MaxPerMinute:    1,
		BlockCategories: []string{"无价值", "无关"},
		TargetSources:   []string{"src-a"},
	})

	msg1 := newTestMsg("first-news")
	f.ShouldFilter("src-a", "sink1", msg1)
	if provider.callCount() != 1 {
		t.Fatalf("expected 1 provider call, got %d", provider.callCount())
	}

	// 不同内容 -> 缓存未命中 -> 触发限流
	msg2 := newTestMsg("second-news")
	blocked := f.ShouldFilter("src-a", "sink1", msg2)
	if blocked {
		t.Errorf("expected rate-limited message to pass through, got blocked")
	}
	if provider.callCount() != 1 {
		t.Errorf("expected provider not called again when rate limited, got %d calls", provider.callCount())
	}
	reason, _ := msg2.GetMetadata("ai_reason")
	if reason != "评分限流放行" {
		t.Errorf("expected rate-limit reason, got %v", reason)
	}
}

func TestAIFilter_CacheDedup(t *testing.T) {
	provider := &fakeProvider{resp: "8|重要|影响科技行业"}
	f := NewAIFilter(provider, AIFilterConfig{
		Enabled:         true,
		ThresholdScore:  5,
		MaxPerMinute:    100,
		BlockCategories: []string{"无价值", "无关"},
		TargetSources:   []string{"src-a"},
	})

	msg := newTestMsg("same-news")
	f.ShouldFilter("src-a", "sink1", msg)
	f.ShouldFilter("src-a", "sink2", msg)

	if provider.callCount() != 1 {
		t.Errorf("expected 1 provider call across 2 sinks (LRU cache hit), got %d", provider.callCount())
	}
}

func TestParseStrictEvalResponse(t *testing.T) {
	tests := []struct {
		name string
		resp string
		want EvalStatus
	}{
		{name: "valid lower bound", resp: "0|一般|低影响", want: EvalSuccess},
		{name: "valid upper bound", resp: "10|重要|高影响", want: EvalSuccess},
		{name: "missing fields", resp: "8|重要", want: EvalParseError},
		{name: "trailing junk score", resp: "8x|重要|高影响", want: EvalParseError},
		{name: "nan", resp: "NaN|重要|高影响", want: EvalParseError},
		{name: "infinity", resp: "+Inf|重要|高影响", want: EvalParseError},
		{name: "below range", resp: "-0.1|重要|高影响", want: EvalParseError},
		{name: "above range", resp: "10.1|重要|高影响", want: EvalParseError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseStrictEvalResponse(tt.resp, 7.5); got.Status != tt.want {
				t.Fatalf("status=%q, want %q", got.Status, tt.want)
			}
		})
	}
}

func TestAIFilterEvaluateTypedStatuses(t *testing.T) {
	provider := &fakeProvider{err: errors.New("offline")}
	f := NewAIFilter(provider, AIFilterConfig{MaxPerMinute: 1})
	if got := f.EvaluateTyped(context.Background(), "source-a", newTestMsg("provider")); got.Status != EvalProviderError {
		t.Fatalf("provider status=%q", got.Status)
	}
	if got := f.EvaluateTyped(context.Background(), "source-a", newTestMsg("limited")); got.Status != EvalRateLimited {
		t.Fatalf("rate limit status=%q", got.Status)
	}
	var unavailable *AIFilter
	if got := unavailable.EvaluateTyped(context.Background(), "source-a", newTestMsg("unavailable")); got.Status != EvalUnavailable {
		t.Fatalf("unavailable status=%q", got.Status)
	}
}

func TestAIFilterEvaluateTypedCachesPerSourceAndLimitsFairly(t *testing.T) {
	provider := &fakeProvider{resp: "8|重要|影响市场"}
	f := NewAIFilter(provider, AIFilterConfig{MaxPerMinute: 1, GlobalMaxPerMinute: 3})
	item := &model.Message{ID: "stable-rss-guid", Title: "same", Content: "same"}
	if got := f.EvaluateTyped(context.Background(), "source-a", item); got.Status != EvalSuccess {
		t.Fatalf("first status=%q", got.Status)
	}
	if got := f.EvaluateTyped(context.Background(), "source-a", item); got.Status != EvalSuccess {
		t.Fatalf("cached status=%q", got.Status)
	}
	if provider.callCount() != 1 {
		t.Fatalf("same source duplicate used %d provider calls", provider.callCount())
	}
	if got := f.EvaluateTyped(context.Background(), "source-b", item); got.Status != EvalSuccess {
		t.Fatalf("second source status=%q", got.Status)
	}
	if provider.callCount() != 2 {
		t.Fatalf("source-specific cache/fairness calls=%d", provider.callCount())
	}
	if got := f.EvaluateTyped(context.Background(), "source-a", &model.Message{ID: "new"}); got.Status != EvalRateLimited {
		t.Fatalf("source-a limit status=%q", got.Status)
	}
	if got := f.EvaluateTyped(context.Background(), "source-c", &model.Message{ID: "third"}); got.Status != EvalSuccess {
		t.Fatalf("source-c fairness status=%q", got.Status)
	}
	if got := f.EvaluateTyped(context.Background(), "source-d", &model.Message{ID: "global"}); got.Status != EvalRateLimited {
		t.Fatalf("global limit status=%q", got.Status)
	}
	if provider.callCount() != 3 {
		t.Fatalf("hard global provider calls=%d", provider.callCount())
	}
}

func TestAIFilterEvaluateTypedBlocksCategoriesAndCachesOnlySuccess(t *testing.T) {
	blockedProvider := &fakeProvider{resp: "10|无价值|无交易价值"}
	blockedFilter := NewAIFilter(blockedProvider, AIFilterConfig{
		MaxPerMinute: 10, GlobalMaxPerMinute: 10, BlockCategories: []string{"无价值", "无关"},
	})
	message := &model.Message{ID: "blocked"}
	for i := 0; i < 2; i++ {
		got := blockedFilter.EvaluateTyped(context.Background(), "source", message)
		if got.Status != EvalSuccess || !got.Blocked || got.Passed {
			t.Fatalf("blocked result=%+v", got)
		}
	}
	if blockedProvider.callCount() != 1 || blockedFilter.typedLRU.Len() != 1 {
		t.Fatalf("blocked success cache calls=%d len=%d", blockedProvider.callCount(), blockedFilter.typedLRU.Len())
	}

	for _, test := range []struct {
		name     string
		provider *fakeProvider
	}{
		{name: "provider", provider: &fakeProvider{err: errors.New("offline")}},
		{name: "parse", provider: &fakeProvider{resp: "not-a-score"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			filter := NewAIFilter(test.provider, AIFilterConfig{MaxPerMinute: 10, GlobalMaxPerMinute: 10})
			for i := 0; i < 2; i++ {
				if got := filter.EvaluateTyped(context.Background(), "source", &model.Message{ID: "same"}); got.Status == EvalSuccess {
					t.Fatalf("unexpected success: %+v", got)
				}
			}
			if test.provider.callCount() != 2 || filter.typedLRU.Len() != 0 {
				t.Fatalf("failure was cached: calls=%d len=%d", test.provider.callCount(), filter.typedLRU.Len())
			}
		})
	}
}

func TestAIFilterEvaluateTypedGlobalBudgetIsAtomic(t *testing.T) {
	provider := &fakeProvider{resp: "8|重要|影响市场"}
	filter := NewAIFilter(provider, AIFilterConfig{MaxPerMinute: 10, GlobalMaxPerMinute: 5})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			filter.EvaluateTyped(context.Background(), fmt.Sprintf("source-%d", i), &model.Message{ID: "unique"})
		}(i)
	}
	wg.Wait()
	if provider.callCount() != 5 {
		t.Fatalf("provider calls=%d, want hard global budget 5", provider.callCount())
	}
}
