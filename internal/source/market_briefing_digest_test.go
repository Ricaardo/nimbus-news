package source

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/datasources/nasdaq"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

type briefingDigestStore struct {
	target digest.Briefing
	lease  *digest.Lease
}

type topicBriefingDigestStore struct {
	*briefingDigestStore
	topicTarget digest.Briefing
	topicLimit  int
	topicLease  *digest.Lease
}

type fixtureNasdaqClient struct {
	events []nasdaq.Event
	err    error
	date   string
}

func (c *fixtureNasdaqClient) FetchCalendar(_ context.Context, date string) ([]nasdaq.Event, error) {
	c.date = date
	return c.events, c.err
}

func float64Pointer(value float64) *float64 { return &value }

func (*briefingDigestStore) Enqueue(context.Context, string, digest.Briefing, *model.Message) error {
	return nil
}
func (s *briefingDigestStore) Lease(_ context.Context, target digest.Briefing, _ int, _ time.Duration) (*digest.Lease, error) {
	s.target = target
	return s.lease, nil
}
func (*briefingDigestStore) Ack(context.Context, string) error { return nil }

func (s *topicBriefingDigestStore) LeaseTopics(_ context.Context, target digest.Briefing, limit int, _ time.Duration) (*digest.Lease, error) {
	s.topicTarget = target
	s.topicLimit = limit
	return s.topicLease, nil
}

func TestMarketBriefingAppendsTargetedDigestMetadata(t *testing.T) {
	ds := &briefingDigestStore{lease: &digest.Lease{
		ID:       "lease-1",
		Briefing: digest.USPreview,
		Items: []digest.Item{{
			ID: "item", Source: "wire",
			Message: &model.Message{Title: "Fed signals patience"},
		}},
	}}
	src := &MarketBriefingSource{briefingType: "us_preview", digestStore: ds}
	msg := &model.Message{Content: "base"}
	got, err := src.appendDigest(context.Background(), []*model.Message{msg})
	if err != nil {
		t.Fatal(err)
	}
	if ds.target != digest.USPreview {
		t.Fatalf("leased target=%s", ds.target)
	}
	if len(got) != 1 || got[0].GetStringMetadata("digest_lease_id") != "lease-1" {
		t.Fatalf("missing lease metadata: %+v", got)
	}
	if !strings.Contains(got[0].Content, "## 资讯摘要") || !strings.Contains(got[0].Content, "Fed signals patience") {
		t.Fatalf("digest not rendered: %s", got[0].Content)
	}
}

func TestMarketBriefingLeasesAndRendersTwelveTopics(t *testing.T) {
	topics := make([]digest.Topic, 0, digestBriefingItemLimit)
	for i := 0; i < digestBriefingItemLimit; i++ {
		topic := digest.Topic{
			Key: fmt.Sprintf("topic-%02d", i),
			Items: []digest.Item{{
				ID: fmt.Sprintf("item-%02d", i), Source: "wire-a",
				Message: &model.Message{Title: fmt.Sprintf("Topic %02d", i)},
			}},
		}
		if i == 0 {
			topic.Items = append(topic.Items, digest.Item{
				ID: "item-00-b", Source: "wire-b",
				Message: &model.Message{Title: "Duplicate member must not add a line"},
			})
		}
		topics = append(topics, topic)
	}
	ds := &topicBriefingDigestStore{
		briefingDigestStore: &briefingDigestStore{},
		topicLease: &digest.Lease{
			ID: "topic-lease", Briefing: digest.USPreview, Topics: topics,
		},
	}
	src := &MarketBriefingSource{briefingType: "us_preview", digestStore: ds}
	got, err := src.appendDigest(context.Background(), []*model.Message{{Content: "base"}})
	if err != nil {
		t.Fatal(err)
	}
	if ds.topicTarget != digest.USPreview || ds.topicLimit != digestBriefingItemLimit {
		t.Fatalf("topic lease target=%s limit=%d", ds.topicTarget, ds.topicLimit)
	}
	if ds.target != "" {
		t.Fatalf("legacy Lease called for TopicStore: target=%s", ds.target)
	}
	if strings.Count(got[0].Content, "\n- ") != digestBriefingItemLimit {
		t.Fatalf("rendered topic lines=%d, want %d:\n%s",
			strings.Count(got[0].Content, "\n- "), digestBriefingItemLimit, got[0].Content)
	}
	if strings.Contains(got[0].Content, "Duplicate member must not add a line") {
		t.Fatalf("multi-member topic rendered more than one line:\n%s", got[0].Content)
	}
	if !strings.Contains(got[0].Content, "wire-a、wire-b") {
		t.Fatalf("multi-member topic attribution missing:\n%s", got[0].Content)
	}
	if got[0].GetStringMetadata("digest_lease_id") != "topic-lease" {
		t.Fatalf("topic lease metadata missing: %+v", got[0])
	}
}

func TestMarketBriefingEmptyDigestLeavesBaseUntouched(t *testing.T) {
	ds := &briefingDigestStore{}
	src := &MarketBriefingSource{briefingType: "closing", digestStore: ds}
	msg := &model.Message{Content: "base"}
	got, err := src.appendDigest(context.Background(), []*model.Message{msg})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "base" || got[0].GetStringMetadata("digest_lease_id") != "" {
		t.Fatalf("empty digest changed briefing: %+v", got)
	}
}

func TestUSPreviewNasdaqCalendarPartialResultsAndCaps(t *testing.T) {
	client := &fixtureNasdaqClient{err: errors.New("dividends endpoint unavailable")}
	for i := 0; i < 8; i++ {
		client.events = append(client.events, nasdaq.Event{
			Type: nasdaq.EventEarnings, Symbol: "US:E" + string(rune('A'+i)),
			Session: nasdaq.SessionAfterMarket, EPS: float64Pointer(float64(i)),
			Estimated:  true,
			Provenance: "Nasdaq earnings calendar dates are algorithmic estimates and are not company-confirmed.",
		})
	}
	client.events = append(client.events,
		nasdaq.Event{Type: nasdaq.EventDividend, Symbol: "US:DIV", Amount: float64Pointer(0.25)},
		nasdaq.Event{Type: nasdaq.EventDividend, Symbol: "US:ZERO", Amount: float64Pointer(0)},
		nasdaq.Event{Type: nasdaq.EventSplit, Symbol: "US:SPLT", SplitRatio: &nasdaq.SplitRatio{Numerator: 3, Denominator: 2}},
		nasdaq.Event{Type: nasdaq.EventSplit, Symbol: "US:NOOP", SplitRatio: &nasdaq.SplitRatio{Numerator: 1, Denominator: 1}},
	)
	src := &MarketBriefingSource{
		name: "us-preview", briefingType: "us_preview",
		nasdaqEnabled: true, nasdaqClient: client,
	}
	now := time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)
	got := src.fetchNasdaqCalendar(context.Background(), now)
	if client.date != "2026-07-30" {
		t.Fatalf("request date=%s", client.date)
	}
	if !strings.Contains(got, "美股事件日历") ||
		!strings.Contains(got, "Estimated · Nasdaq earnings calendar dates are algorithmic estimates and are not company-confirmed.") {
		t.Fatalf("missing estimated provenance: %s", got)
	}
	if strings.Count(got, "EPS预期") != 6 {
		t.Fatalf("earnings cap not applied: %s", got)
	}
	if !strings.Contains(got, "除息: DIV $0.25") || strings.Contains(got, "ZERO") {
		t.Fatalf("dividend significance filtering failed: %s", got)
	}
	if !strings.Contains(got, "拆股: SPLT 3:2") || strings.Contains(got, "NOOP") {
		t.Fatalf("split significance filtering failed: %s", got)
	}
}

func TestUSPreviewNasdaqCalendarGatingAndEmptyOmission(t *testing.T) {
	client := &fixtureNasdaqClient{}
	src := &MarketBriefingSource{
		briefingType: "closing", nasdaqEnabled: true, nasdaqClient: client,
	}
	if got := src.fetchNasdaqCalendar(context.Background(), time.Now()); got != "" || client.date != "" {
		t.Fatalf("non-US briefing fetched Nasdaq calendar: got=%q date=%q", got, client.date)
	}
	src.briefingType = "us_preview"
	if got := src.fetchNasdaqCalendar(context.Background(), time.Now()); got != "" {
		t.Fatalf("empty calendar rendered section: %q", got)
	}
}

func TestUSPreviewMarksNasdaqCachedFallback(t *testing.T) {
	cachedAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	got := renderNasdaqCalendar([]nasdaq.Event{{
		Type:       nasdaq.EventEarnings,
		Symbol:     "US:AAPL",
		Estimated:  true,
		Provenance: "estimated dates",
		DataStatus: nasdaq.EndpointCachedFallback,
		CachedAt:   &cachedAt,
	}})
	if !strings.Contains(got, "Cached fallback") ||
		!strings.Contains(got, "同一交易日缓存") ||
		!strings.Contains(got, "Estimated · estimated dates") {
		t.Fatalf("cached fallback provenance missing: %s", got)
	}
}

func TestMarketBriefingDigestOverflowRemainsPending(t *testing.T) {
	owner, err := store.NewBoltStore(filepath.Join(t.TempDir(), "digest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ds, err := store.NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < digestBriefingItemLimit+3; i++ {
		msg := &model.Message{ID: fmt.Sprintf("item-%02d", i), Title: fmt.Sprintf("News %02d", i)}
		if err := ds.Enqueue(context.Background(), "wire", digest.USPreview, msg); err != nil {
			t.Fatal(err)
		}
	}
	src := &MarketBriefingSource{briefingType: "us_preview", digestStore: ds}
	base := &model.Message{Content: "base"}
	got, err := src.appendDigest(context.Background(), []*model.Message{base})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].GetStringMetadata("digest_lease_id") == "" {
		t.Fatal("first digest lease metadata missing")
	}
	next, err := ds.Lease(context.Background(), digest.USPreview, digestBriefingItemLimit, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || len(next.Items) != 3 {
		t.Fatalf("overflow lease=%+v, want 3 pending items", next)
	}
}

// TestAggregateLeasesNewsAggregateBucket 验证聚合源:
// 租 news_aggregate 桶 + 打 digest_lease_id(走 durable digest 投递)。
// 脚本目录设为不存在路径 → blockbeats 快速失败,摘要仍正常产出。
func TestAggregateLeasesNewsAggregateBucket(t *testing.T) {
	ds := &topicBriefingDigestStore{topicLease: &digest.Lease{
		ID: "lease-agg-1",
		Topics: []digest.Topic{{
			Key:      "topic-1",
			Priority: 10,
			Items: []digest.Item{{
				Source:  "bloomberg-markets",
				Message: &model.Message{Title: "美联储官员讲话摘要"},
			}},
		}},
	}}
	src := &MarketBriefingSource{
		name:           "news-aggregate-evening",
		briefingType:   "aggregate",
		blockbeatsMode: "evening",
		pythonCmd:      "python3",
		scriptsDir:     "/nonexistent-scripts", // 脚本快速失败,验证摘要独立产出
		digestStore:    ds,
	}
	messages, err := src.Fetch()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	msg := messages[0]
	if ds.topicTarget != digest.NewsAggregate {
		t.Fatalf("leased bucket = %q, want %q", ds.topicTarget, digest.NewsAggregate)
	}
	if msg.GetStringMetadata("digest_lease_id") != "lease-agg-1" {
		t.Fatalf("digest_lease_id = %q", msg.GetStringMetadata("digest_lease_id"))
	}
	if !strings.Contains(msg.Content, "美联储官员讲话摘要") {
		t.Fatalf("digest summary missing from content: %s", msg.Content)
	}
	if strings.Contains(msg.Content, "BlockBeats") || strings.Contains(msg.Content, "BTC") {
		t.Fatalf("script failure content leaked into message: %s", msg.Content[:min(len(msg.Content), 200)])
	}
}

// TestAggregateEmptyEverythingReturnsNoMessages 聚合源无摘要且脚本失败 → 不产出。
func TestAggregateEmptyEverythingReturnsNoMessages(t *testing.T) {
	src := &MarketBriefingSource{
		name:           "news-aggregate-morning",
		briefingType:   "aggregate",
		blockbeatsMode: "morning",
		pythonCmd:      "python3",
		scriptsDir:     "/nonexistent-scripts",
		digestStore:    &topicBriefingDigestStore{}, // 空租约
	}
	messages, err := src.Fetch()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("messages = %d, want 0", len(messages))
	}
}

// TestPreMarketSessionMapping verifies the four trading sessions map correctly
// for Asia/Shanghai timezone boundary hours.
func TestPreMarketSessionMapping(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	cases := []struct {
		hour, min int
		want      string
	}{
		{9, 15, "pre_market"},
		{9, 30, "pre_market"},
		{10, 0, "morning_close"},
		{11, 35, "morning_close"},
		{11, 59, "morning_close"},
		{12, 0, "afternoon_open"},
		{13, 5, "afternoon_open"},
		{13, 59, "afternoon_open"},
		{14, 0, "afternoon_close"},
		{15, 0, "afternoon_close"},
		{15, 30, "afternoon_close"},
	}
	for _, c := range cases {
		ts := time.Date(2026, 8, 3, c.hour, c.min, 0, 0, shanghai)
		got := preMarketSession(ts)
		if got != c.want {
			t.Errorf("preMarketSession(%02d:%02d Asia/Shanghai) = %q, want %q", c.hour, c.min, got, c.want)
		}
	}
}
