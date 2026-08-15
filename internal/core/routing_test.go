package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/config"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/filter"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/source"
	newsstore "github.com/Ricaardo/nimbus-os/news/internal/store"
	bolt "go.etcd.io/bbolt"
)

type recoverySignalChannel struct {
	channel.BaseChannel
	sent chan struct{}
	once sync.Once
}

type contextHungChannel struct {
	channel.BaseChannel
	entered chan struct{}
	once    sync.Once
}

func (c *contextHungChannel) Send(ctx context.Context, _ *model.Message) error {
	c.once.Do(func() { close(c.entered) })
	<-ctx.Done()
	return ctx.Err()
}
func (*contextHungChannel) SendBatch(context.Context, []*model.Message) error   { return nil }
func (*contextHungChannel) Reply(context.Context, string, *model.Message) error { return nil }
func (*contextHungChannel) Start(context.Context) error                         { return nil }
func (*contextHungChannel) Stop() error                                         { return nil }

func (c *recoverySignalChannel) Send(context.Context, *model.Message) error {
	c.once.Do(func() { close(c.sent) })
	return nil
}
func (*recoverySignalChannel) SendBatch(context.Context, []*model.Message) error   { return nil }
func (*recoverySignalChannel) Reply(context.Context, string, *model.Message) error { return nil }
func (*recoverySignalChannel) Start(context.Context) error                         { return nil }
func (*recoverySignalChannel) Stop() error                                         { return nil }

type checkpointBlockingRoutedStore struct {
	digest.Store
	digest.RoutedStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *checkpointBlockingRoutedStore) CompleteDirectChannel(ctx context.Context, source, messageID, token, channel string) error {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.RoutedStore.CompleteDirectChannel(ctx, source, messageID, token, channel)
}

type typedProvider struct {
	response string
	err      error
	calls    int
}

func (p *typedProvider) Chat(context.Context, []llm.Message) (string, error) {
	p.calls++
	return p.response, p.err
}
func (p *typedProvider) ChatHeavy(ctx context.Context, messages []llm.Message) (string, error) {
	return p.Chat(ctx, messages)
}
func (*typedProvider) ChatWithTools(context.Context, []llm.Message, []map[string]interface{}) (*llm.Message, error) {
	return nil, nil
}
func (*typedProvider) Think(context.Context, []llm.Message, string) (string, string, error) {
	return "", "", nil
}

type typedRoutedStore struct {
	directAdmission digest.RouteAdmission
	directCalls     int
	directRequest   digest.RouteRequest
	digestCalls     int
	digestRequest   digest.RouteRequest
	digestMessage   *model.Message
	lookupAdmission digest.RouteAdmission
	completeCalls   int
	releaseCalls    int
	claimed         bool
	delivered       map[string]bool
}

func (*typedRoutedStore) Enqueue(context.Context, string, digest.Briefing, *model.Message) error {
	return nil
}
func (*typedRoutedStore) Lease(context.Context, digest.Briefing, int, time.Duration) (*digest.Lease, error) {
	return nil, nil
}
func (*typedRoutedStore) Ack(context.Context, string) error { return nil }
func (s *typedRoutedStore) LookupDirect(context.Context, digest.RouteRequest) (digest.RouteAdmission, error) {
	return s.lookupAdmission, nil
}
func (s *typedRoutedStore) AdmitDirect(_ context.Context, request digest.RouteRequest) (digest.RouteAdmission, error) {
	s.directCalls++
	s.directRequest = request
	if s.lookupAdmission.Existing {
		return s.lookupAdmission, nil
	}
	if s.directAdmission.Admitted {
		s.lookupAdmission = digest.RouteAdmission{Admitted: true, Existing: true, Lane: digest.RouteDirect, Channels: append([]string(nil), request.Channels...)}
		s.digestMessage = request.Payload
		s.delivered = make(map[string]bool)
	}
	return s.directAdmission, nil
}
func (s *typedRoutedStore) ClaimDirect(context.Context, string, string, time.Duration) (*digest.DirectClaim, error) {
	if !s.lookupAdmission.Existing || s.lookupAdmission.Completed || s.claimed {
		return nil, nil
	}
	s.claimed = true
	channels := make([]string, 0, len(s.lookupAdmission.Channels))
	for _, channel := range s.lookupAdmission.Channels {
		if !s.delivered[channel] {
			channels = append(channels, channel)
		}
	}
	return &digest.DirectClaim{Source: s.directRequest.Source, MessageID: s.directRequest.MessageID, Token: "token", Message: s.digestMessage, Channels: channels}, nil
}
func (s *typedRoutedStore) ClaimPendingDirect(ctx context.Context, _ int, lease time.Duration) ([]digest.DirectClaim, error) {
	claim, err := s.ClaimDirect(ctx, s.directRequest.Source, s.directRequest.MessageID, lease)
	if claim == nil || err != nil {
		return nil, err
	}
	return []digest.DirectClaim{*claim}, nil
}
func (s *typedRoutedStore) CompleteDirectChannel(_ context.Context, _, _, _, channel string) error {
	s.delivered[channel] = true
	return nil
}
func (s *typedRoutedStore) FailDirectChannel(_ context.Context, _, _, _, channel, _ string, permanent bool) error {
	if permanent {
		s.delivered[channel] = true
	}
	return nil
}
func (s *typedRoutedStore) ReleaseDirect(context.Context, string, string, string) error {
	s.releaseCalls++
	s.claimed = false
	complete := len(s.lookupAdmission.Channels) > 0
	for _, channel := range s.lookupAdmission.Channels {
		complete = complete && s.delivered[channel]
	}
	if complete {
		s.completeCalls++
		s.lookupAdmission.Completed = true
	}
	return nil
}
func (s *typedRoutedStore) EnqueueRoutedDigest(_ context.Context, request digest.RouteRequest, msg *model.Message) (digest.RouteAdmission, error) {
	s.digestCalls++
	s.digestRequest = request
	s.digestMessage = msg
	return digest.RouteAdmission{Admitted: true, Lane: digest.RouteDigest}, nil
}

type failingTypedChannel struct {
	channel.BaseChannel
	calls     int
	failCount int
}

func (c *failingTypedChannel) Send(context.Context, *model.Message) error {
	c.calls++
	if c.calls <= c.failCount {
		return errors.New("partial send")
	}
	return nil
}
func (*failingTypedChannel) SendBatch(context.Context, []*model.Message) error { return nil }
func (*failingTypedChannel) Reply(context.Context, string, *model.Message) error {
	return nil
}
func (*failingTypedChannel) Start(context.Context) error { return nil }
func (*failingTypedChannel) Stop() error                 { return nil }

func scorePointer(v float64) *float64 { return &v }

func typedRoutingPolicy() *config.SourceRoutingConfig {
	return &config.SourceRoutingConfig{
		AIPolicy:    config.AIPolicyRequired,
		OnAIFailure: config.RouteDefaultSilent,
		Default:     config.RouteDefaultSilent,
		Direct:      &config.RouteBand{MinScore: scorePointer(8), MaxPerDay: 5},
		Digest:      &config.RouteBand{MinScore: scorePointer(6), MaxPerDay: 10, Priority: 20, BriefingTarget: "us_preview"},
	}
}

func attachSuccessfulTypedRouter(engine *NewsEngine) {
	manager := channel.NewManager()
	manager.Add(&routingChannel{BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush)})
	engine.router = NewRouter(manager)
}

func TestTypedRouteDecisionBoundariesAndFailures(t *testing.T) {
	tests := []struct {
		name        string
		response    string
		providerErr error
		mutate      func(*config.SourceRoutingConfig, *model.Message)
		want        digest.RouteLane
		wantStatus  filter.EvalStatus
		wantCalls   int
	}{
		{name: "direct boundary", response: "8|重要|直接影响", want: digest.RouteDirect, wantStatus: filter.EvalSuccess, wantCalls: 1},
		{name: "digest boundary", response: "6|一般|摘要价值", want: digest.RouteDigest, wantStatus: filter.EvalSuccess, wantCalls: 1},
		{name: "below bands", response: "5.9|一般|低影响", want: digest.RouteSilent, wantStatus: filter.EvalSuccess, wantCalls: 1},
		{name: "provider failure silent", providerErr: errors.New("offline"), want: digest.RouteSilent, wantStatus: filter.EvalProviderError, wantCalls: 1},
		{name: "parse failure digest", response: "eleven|重要|错误", mutate: func(r *config.SourceRoutingConfig, _ *model.Message) { r.OnAIFailure = config.RouteDefaultDigest }, want: digest.RouteDigest, wantStatus: filter.EvalParseError, wantCalls: 1},
		{name: "keyword critical", response: "1|一般|不应调用", mutate: func(r *config.SourceRoutingConfig, m *model.Message) {
			r.CriticalKeywords = []string{"紧 急"}
			m.Content = "  紧   急  制裁 "
		}, want: digest.RouteDirect, wantCalls: 0},
		{name: "source critical", response: "1|一般|不应调用", mutate: func(r *config.SourceRoutingConfig, _ *model.Message) { r.Critical = true }, want: digest.RouteDirect, wantCalls: 0},
		{name: "out of range never direct", response: "11|重要|越界", want: digest.RouteSilent, wantStatus: filter.EvalParseError, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &typedProvider{response: tt.response, err: tt.providerErr}
			chain := filter.NewChain(filter.NewAIFilter(provider, filter.AIFilterConfig{MaxPerMinute: 100}))
			engine := &NewsEngine{filters: chain}
			routing := typedRoutingPolicy()
			msg := &model.Message{ID: tt.name, Title: "headline", Content: "content", Metadata: map[string]interface{}{}}
			if tt.mutate != nil {
				tt.mutate(routing, msg)
			}
			got := engine.decideTypedRoute(context.Background(), "feed", routing, msg)
			if got.lane != tt.want || got.eval.Status != tt.wantStatus || provider.calls != tt.wantCalls {
				t.Fatalf("lane=%q status=%q calls=%d, want %q %q %d", got.lane, got.eval.Status, provider.calls, tt.want, tt.wantStatus, tt.wantCalls)
			}
		})
	}
}

func TestCriticalKeywordUsesTokenBoundaries(t *testing.T) {
	for _, text := range []string{"award winner", "toward growth", "weather forecast"} {
		if matchesCriticalKeyword([]string{"war"}, &model.Message{Title: text}) {
			t.Fatalf("war matched inside %q", text)
		}
	}
	for _, text := range []string{"war begins", "risk-of-war", "WAR escalation"} {
		if !matchesCriticalKeyword([]string{"war"}, &model.Message{Title: text}) {
			t.Fatalf("war did not match token in %q", text)
		}
	}
	if !matchesCriticalKeyword([]string{"紧急制裁"}, &model.Message{Content: "当局宣布紧急制裁措施"}) {
		t.Fatal("CJK phrase should retain substring matching")
	}
}

func TestTypedBlockedCategoryIsAlwaysSilent(t *testing.T) {
	provider := &typedProvider{response: "10|无价值|高分但无交易价值"}
	engine := &NewsEngine{filters: filter.NewChain(filter.NewAIFilter(provider, filter.AIFilterConfig{
		MaxPerMinute: 100, GlobalMaxPerMinute: 100, BlockCategories: []string{"无价值", "无关"},
	}))}
	decision := engine.decideTypedRoute(context.Background(), "feed", typedRoutingPolicy(),
		&model.Message{ID: "blocked", Title: "headline"})
	if decision.lane != digest.RouteSilent || decision.eval.Status != filter.EvalSuccess || !decision.eval.Blocked {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestTypedBypassDefaultDirectRemainsQuotaBounded(t *testing.T) {
	routing := &config.SourceRoutingConfig{
		AIPolicy: config.AIPolicyBypass, OnAIFailure: config.RouteDefaultSilent,
		Default: config.RouteDefaultDirect, Direct: &config.RouteBand{MaxPerDay: 1, Priority: 20},
	}
	decision := (&NewsEngine{}).decideTypedRoute(context.Background(), "scheduled", routing, &model.Message{ID: "one"})
	if decision.lane != digest.RouteDirect || decision.critical || decision.band == nil || decision.band.MaxPerDay != 1 {
		t.Fatalf("decision=%+v", decision)
	}
}

func TestTypedDirectQuotaDowngradesAndPriorityIsBounded(t *testing.T) {
	provider := &typedProvider{response: "10|重要|高影响"}
	store := &typedRoutedStore{directAdmission: digest.RouteAdmission{Admitted: false, Lane: digest.RouteDirect}}
	engine := &NewsEngine{
		filters:     filter.NewChain(filter.NewAIFilter(provider, filter.AIFilterConfig{MaxPerMinute: 100})),
		digestStore: store,
	}
	attachSuccessfulTypedRouter(engine)
	routing := typedRoutingPolicy()
	routing.Digest.Priority = 99
	msg := &model.Message{ID: "quota", Title: "headline", Content: "content", Metadata: map[string]interface{}{}}
	if !engine.routeTypedMessages(context.Background(), "feed", newsSourceInfo{routing: routing, channels: []string{"main"}}, []*model.Message{msg}) {
		t.Fatal("route failed")
	}
	if store.directCalls != 1 || store.digestCalls != 1 || store.digestRequest.Priority != 100 {
		t.Fatalf("direct=%d digest=%d priority=%d", store.directCalls, store.digestCalls, store.digestRequest.Priority)
	}
	if got := msg.GetStringMetadata("routing_lane"); got != string(digest.RouteDigest) {
		t.Fatalf("lane metadata=%q", got)
	}
}

func TestTypedCriticalBypassesAIAndQuotaLimitButRecordsAdmission(t *testing.T) {
	provider := &typedProvider{response: "1|一般|不应调用"}
	store := &typedRoutedStore{directAdmission: digest.RouteAdmission{Admitted: true, Lane: digest.RouteDirect}}
	engine := &NewsEngine{
		filters:     filter.NewChain(filter.NewAIFilter(provider, filter.AIFilterConfig{MaxPerMinute: 100})),
		digestStore: store,
	}
	attachSuccessfulTypedRouter(engine)
	routing := typedRoutingPolicy()
	routing.Critical = true
	msg := &model.Message{ID: "critical", Title: "headline", Content: "content", Metadata: map[string]interface{}{}}
	if !engine.routeTypedMessages(context.Background(), "feed", newsSourceInfo{routing: routing, channels: []string{"main"}}, []*model.Message{msg}) {
		t.Fatal("route failed")
	}
	if provider.calls != 0 || store.directCalls != 1 || store.digestCalls != 0 || !store.directRequest.Critical {
		t.Fatalf("ai=%d direct=%d digest=%d", provider.calls, store.directCalls, store.digestCalls)
	}
}

func TestTypedDirectPartialFailureRetriesWithoutFallbackOrRecharge(t *testing.T) {
	provider := &typedProvider{response: "9|重要|高影响"}
	store := &typedRoutedStore{directAdmission: digest.RouteAdmission{Admitted: true, Lane: digest.RouteDirect}}
	manager := channel.NewManager()
	succeeded := &routingChannel{BaseChannel: channel.NewBaseChannel("good", "test", channel.ModePush)}
	failed := &failingTypedChannel{BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush), failCount: 1}
	manager.Add(succeeded)
	manager.Add(failed)
	engine := &NewsEngine{
		filters:     filter.NewChain(filter.NewAIFilter(provider, filter.AIFilterConfig{MaxPerMinute: 100})),
		digestStore: store,
		router:      NewRouter(manager),
	}
	msg := &model.Message{ID: "partial", Title: "headline", Content: "content", Metadata: map[string]interface{}{}}
	info := newsSourceInfo{routing: typedRoutingPolicy(), channels: []string{"good", "main"}}
	if !engine.routeTypedMessages(context.Background(), "feed", info, []*model.Message{msg}) {
		t.Fatal("route failed")
	}
	if store.directCalls != 1 || store.digestCalls != 0 || failed.calls != 1 || succeeded.sent != 1 {
		t.Fatalf("direct=%d digest=%d failed=%d succeeded=%d", store.directCalls, store.digestCalls, failed.calls, succeeded.sent)
	}
	// The existing, incomplete admission retries without charging quota again.
	if !engine.routeTypedMessages(context.Background(), "feed", info, []*model.Message{msg}) {
		t.Fatal("retry route failed")
	}
	if store.directCalls != 2 || store.completeCalls != 1 || failed.calls != 2 || succeeded.sent != 1 {
		t.Fatalf("after retry direct=%d complete=%d failed=%d succeeded=%d", store.directCalls, store.completeCalls, failed.calls, succeeded.sent)
	}
	// A completed admission suppresses later polls.
	if !engine.routeTypedMessages(context.Background(), "feed", info, []*model.Message{msg}) {
		t.Fatal("completed route failed")
	}
	if store.directCalls != 3 || failed.calls != 2 || succeeded.sent != 1 {
		t.Fatalf("completed admission resent: failed=%d succeeded=%d", failed.calls, succeeded.sent)
	}
}

func TestTypedRoutingKeepsRawArchivePreEvaluation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	archive := newsstore.NewNewsStoreContext(ctx, newsstore.NewsStoreConfig{MaxItems: 10})
	t.Cleanup(func() {
		cancel()
		archive.WaitCleanup()
	})
	provider := &typedProvider{response: "7|一般|摘要价值"}
	routed := &typedRoutedStore{}
	engine := &NewsEngine{
		ctx:         context.Background(),
		filters:     filter.NewChain(filter.NewAIFilter(provider, filter.AIFilterConfig{MaxPerMinute: 100})),
		digestStore: routed,
		newsStore:   archive,
	}
	src := &routingSource{name: "feed", msg: &model.Message{ID: "raw", Title: "headline", Content: "content", FetchTime: time.Now(), Metadata: map[string]interface{}{"original": "yes"}}}
	if !engine.fetchAndDispatch(src, newsSourceInfo{routing: typedRoutingPolicy()}) {
		t.Fatal("fetch failed")
	}
	raw := archive.GetByID(context.Background(), "raw")
	if raw == nil || raw.Metadata["ai_eval_status"] != nil {
		t.Fatalf("raw archive contains routing metadata: %#v", raw)
	}
	if routed.digestMessage == nil || routed.digestMessage.GetStringMetadata("ai_eval_status") != string(filter.EvalSuccess) {
		t.Fatalf("downstream status missing: %#v", routed.digestMessage)
	}
}

func TestTypedRoutingRequiresRoutedStoreAndDeepCopiesLifecycleConfig(t *testing.T) {
	routing := typedRoutingPolicy()
	if _, err := NewNewsEngine(NewsEngineConfig{Sources: []SourceConfig{{Name: "typed", Routing: routing}}}); err == nil {
		t.Fatal("expected missing routed store error")
	}
	info := sourceInfoFromConfig(SourceConfig{Name: "typed", Routing: routing})
	*routing.Direct.MinScore = 1
	routing.CriticalKeywords = append(routing.CriticalKeywords, "changed")
	if *info.routing.Direct.MinScore != 8 || len(info.routing.CriticalKeywords) != 0 {
		t.Fatal("runtime routing aliases mutable config")
	}
}

func TestDirectRecoveryDeliversPersistedPayloadWithoutUpstreamRefetch(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "direct-recovery.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ds, err := newsstore.NewDigestStore(db)
	if err != nil {
		t.Fatal(err)
	}
	request := digest.RouteRequest{Source: "rss", MessageID: "persisted", Lane: digest.RouteDirect, MaxPerDay: 5,
		Channels: []string{"main"}, Payload: &model.Message{ID: "persisted", Title: "recover me", Metadata: map[string]interface{}{"ai_eval_status": "success"}}}
	if admission, err := ds.AdmitDirect(context.Background(), request); err != nil || !admission.Admitted {
		t.Fatalf("admission=%+v err=%v", admission, err)
	}
	restarted, err := newsstore.NewDigestStore(db)
	if err != nil {
		t.Fatal(err)
	}
	manager := channel.NewManager()
	recovered := &recoverySignalChannel{BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush), sent: make(chan struct{})}
	manager.Add(recovered)
	blocking := &checkpointBlockingRoutedStore{Store: restarted, RoutedStore: restarted, entered: make(chan struct{}), release: make(chan struct{})}
	engine := &NewsEngine{digestStore: blocking, router: NewRouter(manager), retryQueue: filter.NewRetryQueue(10, 1), sources: map[string]source.Source{}, sourceGenerations: map[string]uint64{}, sourceGates: map[string]*sourceOperationGate{}}
	engine.Start(context.Background())
	select {
	case <-recovered.sent:
	case <-time.After(time.Second):
		t.Fatal("recovery did not send persisted payload")
	}
	stopped := make(chan struct{})
	go func() {
		engine.Stop()
		close(stopped)
	}()
	select {
	case <-blocking.entered:
	case <-time.After(time.Second):
		t.Fatal("checkpoint did not start after send")
	}
	select {
	case <-stopped:
		t.Fatal("Stop returned before in-flight direct checkpoint")
	case <-time.After(20 * time.Millisecond):
	}
	close(blocking.release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish after direct checkpoint")
	}
	state, err := restarted.LookupDirect(context.Background(), request)
	if err != nil || !state.Completed {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestDirectWorkIsRejectedAfterStop(t *testing.T) {
	store := &typedRoutedStore{}
	manager := channel.NewManager()
	sink := &routingChannel{BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush)}
	manager.Add(sink)
	engine := &NewsEngine{
		digestStore: store, router: NewRouter(manager), retryQueue: filter.NewRetryQueue(10, 1),
		sources: map[string]source.Source{}, sourceGenerations: map[string]uint64{}, sourceGates: map[string]*sourceOperationGate{},
	}
	engine.Start(context.Background())
	engine.Stop()
	claim := digest.DirectClaim{Source: "source", MessageID: "after-stop", Token: "token",
		Message: &model.Message{ID: "after-stop"}, Channels: []string{"main"}}
	store.claimed = true
	if engine.runDirectClaim(context.Background(), store, claim) || sink.sent != 0 || store.claimed || store.releaseCalls != 1 {
		t.Fatalf("post-stop direct work was accepted or retained: sent=%d claimed=%v releases=%d", sink.sent, store.claimed, store.releaseCalls)
	}
}

func TestDirectRecoveryPoisonAdmissionsDoNotStarveHealthyTail(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "direct-fairness.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ds, err := newsstore.NewDigestStore(db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("poison-%02d", i)
		_, err := ds.AdmitDirect(context.Background(), digest.RouteRequest{Source: "rss", MessageID: id, Lane: digest.RouteDirect,
			Channels: []string{"removed"}, Payload: &model.Message{ID: id}})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = ds.AdmitDirect(context.Background(), digest.RouteRequest{Source: "rss", MessageID: "healthy-tail", Lane: digest.RouteDirect,
		Channels: []string{"main"}, Payload: &model.Message{ID: "healthy-tail"}})
	if err != nil {
		t.Fatal(err)
	}
	manager := channel.NewManager()
	healthy := &routingChannel{BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush)}
	manager.Add(healthy)
	engine := &NewsEngine{ctx: context.Background(), digestStore: ds, router: NewRouter(manager)}
	engine.recoverPendingDirect()
	if healthy.sent != 1 {
		t.Fatalf("healthy tail sends=%d", healthy.sent)
	}
	state, err := ds.LookupDirect(context.Background(), digest.RouteRequest{Source: "rss", MessageID: "poison-00"})
	if err != nil || !state.Completed {
		t.Fatalf("poison terminal state=%+v err=%v", state, err)
	}
}

func TestNewsEngineStopCancelsHungDirectDispatchWithinBound(t *testing.T) {
	db, err := bolt.Open(filepath.Join(t.TempDir(), "direct-stop.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ds, err := newsstore.NewDigestStore(db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ds.AdmitDirect(context.Background(), digest.RouteRequest{Source: "rss", MessageID: "hung", Lane: digest.RouteDirect,
		Channels: []string{"hung"}, Payload: &model.Message{ID: "hung"}})
	if err != nil {
		t.Fatal(err)
	}
	manager := channel.NewManager()
	hung := &contextHungChannel{BaseChannel: channel.NewBaseChannel("hung", "test", channel.ModePush), entered: make(chan struct{})}
	manager.Add(hung)
	engine := &NewsEngine{digestStore: ds, router: NewRouter(manager), retryQueue: filter.NewRetryQueue(10, 1), sources: map[string]source.Source{}, sourceGenerations: map[string]uint64{}, sourceGates: map[string]*sourceOperationGate{}}
	engine.Start(context.Background())
	select {
	case <-hung.entered:
	case <-time.After(time.Second):
		t.Fatal("hung send did not start")
	}
	started := time.Now()
	engine.Stop()
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Stop took %v", elapsed)
	}
}
