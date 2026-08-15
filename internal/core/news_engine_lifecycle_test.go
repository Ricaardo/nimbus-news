package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/source"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

type lifecycleTestSource struct {
	name     string
	provider llm.Provider
	closed   atomic.Bool
	fetch    func() ([]*model.Message, error)
}

type lifecycleContextSource struct {
	lifecycleTestSource
	fetchContext func(context.Context) ([]*model.Message, error)
}

func (s *lifecycleContextSource) FetchContext(ctx context.Context) ([]*model.Message, error) {
	return s.fetchContext(ctx)
}

func (s *lifecycleTestSource) Name() string { return s.name }
func (*lifecycleTestSource) Type() string   { return "lifecycle-test" }
func (s *lifecycleTestSource) Fetch() ([]*model.Message, error) {
	if s.fetch != nil {
		return s.fetch()
	}
	return nil, nil
}
func (s *lifecycleTestSource) Close() error { s.closed.Store(true); return nil }
func (s *lifecycleTestSource) SetLLMProvider(provider llm.Provider) {
	s.provider = provider
}

type lifecycleTestProvider struct{}

func (*lifecycleTestProvider) Chat(context.Context, []llm.Message) (string, error) {
	return "", nil
}
func (*lifecycleTestProvider) ChatHeavy(context.Context, []llm.Message) (string, error) {
	return "", nil
}
func (*lifecycleTestProvider) ChatWithTools(context.Context, []llm.Message, []map[string]interface{}) (*llm.Message, error) {
	return &llm.Message{}, nil
}
func (*lifecycleTestProvider) Think(context.Context, []llm.Message, string) (string, string, error) {
	return "", "", nil
}

type lifecycleCountingProvider struct {
	lifecycleTestProvider
	calls atomic.Int64
}

func (p *lifecycleCountingProvider) Chat(context.Context, []llm.Message) (string, error) {
	p.calls.Add(1)
	return "", nil
}

func (p *lifecycleCountingProvider) ChatHeavy(context.Context, []llm.Message) (string, error) {
	p.calls.Add(1)
	return "", nil
}

func TestTriggerSourceRequiresRunningEngine(t *testing.T) {
	engine := &NewsEngine{}
	if err := engine.TriggerSource("one"); !errors.Is(err, ErrEngineStopped) {
		t.Fatalf("TriggerSource error=%v, want ErrEngineStopped", err)
	}
}

func TestTriggerSourceTracksAndCancelsContextFetch(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	src := &lifecycleContextSource{
		lifecycleTestSource: lifecycleTestSource{name: "one"},
		fetchContext: func(ctx context.Context) ([]*model.Message, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	engine := &NewsEngine{
		sources: map[string]source.Source{"one": src}, sourceInfos: []newsSourceInfo{{name: "one"}},
		sourceGenerations: map[string]uint64{"one": 1}, running: true, ctx: ctx, cancelFunc: cancel,
	}
	if err := engine.TriggerSource("one"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("triggered context fetch did not start")
	}
	engine.Stop()
	select {
	case <-canceled:
	default:
		t.Fatal("Stop returned before the triggered context fetch observed cancellation")
	}
	if !src.closed.Load() {
		t.Fatal("source was not closed after its triggered fetch drained")
	}
}

func TestTriggerSourcePassesGenerationFence(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	src := &lifecycleTestSource{name: "one", fetch: func() ([]*model.Message, error) {
		close(started)
		<-release
		return []*model.Message{{ID: "late", Title: "late"}}, nil
	}}
	sink := &routingChannel{BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush)}
	manager := channel.NewManager()
	manager.Add(sink)
	engine := &NewsEngine{
		sources:           map[string]source.Source{"one": src},
		sourceInfos:       []newsSourceInfo{{name: "one", channels: []string{"main"}, deliveryMode: "direct"}},
		sourceGenerations: map[string]uint64{"one": 1}, running: true, ctx: context.Background(), router: NewRouter(manager),
	}
	if err := engine.TriggerSource("one"); err != nil {
		t.Fatal(err)
	}
	<-started
	engine.sourceMu.Lock()
	engine.sourceGenerations["one"] = 2
	engine.sourceMu.Unlock()
	close(release)
	done := make(chan struct{})
	go func() {
		engine.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("triggered fetch was not tracked")
	}
	if sink.sent != 0 {
		t.Fatalf("late triggered generation dispatched %d messages", sink.sent)
	}
}

func TestPreparedSourceChangeRetainsLLMAndAbortCloses(t *testing.T) {
	var created []*lifecycleTestSource
	source.Register("lifecycle-test-retained", func(cfg source.Config) (source.Source, error) {
		src := &lifecycleTestSource{name: cfg.Name}
		created = append(created, src)
		return src, nil
	})
	engine, err := NewNewsEngine(NewsEngineConfig{Sources: []SourceConfig{{Name: "one", Type: "lifecycle-test-retained"}}})
	if err != nil {
		t.Fatal(err)
	}
	provider := &lifecycleTestProvider{}
	engine.InjectLLM(provider)

	change, err := engine.PrepareSourceChange(SourceConfig{Name: "one", Type: "lifecycle-test-retained"})
	if err != nil {
		t.Fatal(err)
	}
	if created[1].provider != provider {
		t.Fatal("prepared source did not retain injected LLM provider")
	}
	change.Abort()
	if !created[1].closed.Load() || created[0].closed.Load() {
		t.Fatalf("abort closed states: new=%v old=%v", created[1].closed.Load(), created[0].closed.Load())
	}
}

func TestPreparedSourceChangeFencesLateOldFetch(t *testing.T) {
	release := make(chan struct{})
	old := &lifecycleTestSource{name: "one", fetch: func() ([]*model.Message, error) {
		<-release
		return []*model.Message{{ID: "late", Title: "late"}}, nil
	}}
	source.Register("lifecycle-test-fence", func(cfg source.Config) (source.Source, error) {
		return &lifecycleTestSource{name: cfg.Name}, nil
	})
	engine := &NewsEngine{
		sources:           map[string]source.Source{"one": old},
		sourceInfos:       []newsSourceInfo{{name: "one"}},
		sourceCancels:     make(map[string]context.CancelFunc),
		sourceGenerations: map[string]uint64{"one": 1},
		ctx:               context.Background(),
	}
	result := make(chan bool, 1)
	go func() {
		result <- engine.fetchAndDispatchGeneration(old, newsSourceInfo{name: "one"}, 1)
	}()
	change, err := engine.PrepareSourceChange(SourceConfig{Name: "one", Type: "lifecycle-test-fence"})
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Activate(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if <-result {
		t.Fatal("late fetch from replaced source was accepted")
	}
}

func TestPreparedSourceChangeFencesLateOldErrorFromReplacementHealth(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	old := &lifecycleTestSource{name: "one", fetch: func() ([]*model.Message, error) {
		started <- struct{}{}
		<-release
		return nil, errors.New("late old failure")
	}}
	source.Register("lifecycle-test-error-fence", func(cfg source.Config) (source.Source, error) {
		return &lifecycleTestSource{name: cfg.Name}, nil
	})
	monitor := source.NewHealthMonitor(source.DefaultHealthConfig())
	monitor.RegisterSource("one")
	engine := &NewsEngine{
		sources:           map[string]source.Source{"one": old},
		sourceInfos:       []newsSourceInfo{{name: "one"}},
		sourceCancels:     make(map[string]context.CancelFunc),
		sourceGenerations: map[string]uint64{"one": 1},
		healthMonitor:     monitor,
		ctx:               context.Background(),
	}
	result := make(chan bool, 1)
	go func() {
		result <- engine.fetchAndDispatchGeneration(old, newsSourceInfo{name: "one"}, 1)
	}()
	<-started
	change, err := engine.PrepareSourceChange(SourceConfig{Name: "one", Type: "lifecycle-test-error-fence"})
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Activate(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if <-result {
		t.Fatal("late error from replaced source was accepted")
	}
	health := monitor.GetHealth("one")
	if health == nil || health.Status != source.StatusHealthy || health.ConsecutiveFails != 0 || health.TotalFailures != 0 {
		t.Fatalf("late old error poisoned replacement health: %+v", health)
	}
}

func TestPreparedSourceChangeFencesAllPostFetchEffects(t *testing.T) {
	paused := make(chan struct{}, 1)
	release := make(chan struct{})
	source.Register("lifecycle-test-effects-fence", func(cfg source.Config) (source.Source, error) {
		return &lifecycleTestSource{name: cfg.Name}, nil
	})
	old := &lifecycleTestSource{name: "one", fetch: func() ([]*model.Message, error) {
		return []*model.Message{{ID: "stale", Title: "stale", Content: "stale"}}, nil
	}}
	ch := &routingChannel{BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush)}
	manager := channel.NewManager()
	manager.Add(ch)
	storeCtx, cancelStore := context.WithCancel(context.Background())
	defer cancelStore()
	newsStore := store.NewNewsStoreContext(storeCtx, store.NewsStoreConfig{})
	digestStore := &routingDigestStore{}
	provider := &lifecycleCountingProvider{}
	monitor := source.NewHealthMonitor(source.DefaultHealthConfig())
	monitor.RegisterSource("one")
	engine := &NewsEngine{
		sources:           map[string]source.Source{"one": old},
		sourceInfos:       []newsSourceInfo{{name: "one"}},
		sourceCancels:     make(map[string]context.CancelFunc),
		sourceGenerations: map[string]uint64{"one": 1},
		newsStore:         newsStore,
		digestStore:       digestStore,
		enhancer:          llm.NewNewsEnhancer(provider, llm.EnhancerConfig{Enabled: true}),
		router:            NewRouter(manager),
		healthMonitor:     monitor,
		ctx:               context.Background(),
		beforeSourceEffects: func() {
			paused <- struct{}{}
			<-release
		},
	}
	result := make(chan bool, 1)
	go func() {
		result <- engine.fetchAndDispatchGeneration(old, newsSourceInfo{
			name: "one", channels: []string{"main"}, deliveryMode: "direct",
		}, 1)
	}()
	<-paused
	change, err := engine.PrepareSourceChange(SourceConfig{Name: "one", Type: "lifecycle-test-effects-fence"})
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Activate(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if <-result {
		t.Fatal("stale post-fetch run was accepted")
	}
	health := monitor.GetHealth("one")
	if newsStore.GetByID(context.Background(), "stale") != nil || ch.sent != 0 ||
		digestStore.enqueued != 0 || provider.calls.Load() != 0 ||
		health.TotalRequests != 0 || health.TotalFailures != 0 {
		t.Fatalf("stale effects persisted: news=%v sent=%d enqueued=%d enhanced=%d health=%+v",
			newsStore.GetByID(context.Background(), "stale"), ch.sent, digestStore.enqueued, provider.calls.Load(), health)
	}
}

func TestNewsPersistenceFailureDoesNotBlockRealtimeDispatch(t *testing.T) {
	storeCtx, cancelStore := context.WithCancel(context.Background())
	owner, err := store.NewBoltStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	newsStore, err := store.NewPersistentNewsStoreContext(storeCtx, owner.DB(), store.NewsStoreConfig{
		MaxItems: 10,
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelStore()
	newsStore.WaitCleanup()
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}

	ch := &routingChannel{BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush)}
	manager := channel.NewManager()
	manager.Add(ch)
	engine := &NewsEngine{
		newsStore: newsStore,
		router:    NewRouter(manager),
		ctx:       context.Background(),
	}
	src := &lifecycleTestSource{name: "one", fetch: func() ([]*model.Message, error) {
		return []*model.Message{{
			ID: "live", Title: "live", FetchTime: time.Now(),
		}}, nil
	}}
	if !engine.fetchAndDispatch(src, newsSourceInfo{
		name: "one", channels: []string{"main"}, deliveryMode: "direct",
	}) {
		t.Fatal("fetch failed because persistence was unavailable")
	}
	if ch.sent != 1 {
		t.Fatalf("realtime dispatch stopped after persistence failure: sent=%d", ch.sent)
	}
	if newsStore.GetByID(context.Background(), "live") != nil {
		t.Fatal("failed persistent write leaked into memory")
	}
}

func TestPreparedSourceChangeWaitsForOldFetchBeforeClose(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var created []*lifecycleTestSource
	source.Register("lifecycle-test-close-wait", func(cfg source.Config) (source.Source, error) {
		src := &lifecycleTestSource{name: cfg.Name}
		if len(created) == 0 {
			src.fetch = func() ([]*model.Message, error) {
				started <- struct{}{}
				<-release
				return nil, nil
			}
		}
		created = append(created, src)
		return src, nil
	})
	engine, err := NewNewsEngine(NewsEngineConfig{Sources: []SourceConfig{{
		Name: "one", Type: "lifecycle-test-close-wait", Interval: 3600,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	engine.Start(context.Background())
	<-started
	change, err := engine.PrepareSourceChange(SourceConfig{Name: "one", Type: "lifecycle-test-close-wait", Interval: 3600})
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Activate(); err != nil {
		t.Fatal(err)
	}
	if created[0].closed.Load() {
		t.Fatal("old source closed while Fetch was still running")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for !created[0].closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !created[0].closed.Load() {
		t.Fatal("old source was not closed after Fetch exited")
	}
	engine.Stop()
}

func TestPreparedDisabledSourceRemovesRuntimeWithoutConstruction(t *testing.T) {
	var created int
	source.Register("lifecycle-test-disabled", func(cfg source.Config) (source.Source, error) {
		created++
		return &lifecycleTestSource{name: cfg.Name}, nil
	})
	engine, err := NewNewsEngine(NewsEngineConfig{Sources: []SourceConfig{{Name: "one", Type: "lifecycle-test-disabled"}}})
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	change, err := engine.PrepareSourceChange(SourceConfig{Name: "one", Type: "lifecycle-test-disabled", Enabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Activate(); err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("disabled target constructed a source: created=%d", created)
	}
	if _, ok := engine.sources["one"]; ok {
		t.Fatal("disabled target remained in runtime registry")
	}
}

func TestNewsEngineRegistersIntervalAndScheduledHealth(t *testing.T) {
	source.Register("lifecycle-test-health-registration", func(cfg source.Config) (source.Source, error) {
		return &lifecycleTestSource{name: cfg.Name}, nil
	})
	engine, err := NewNewsEngine(NewsEngineConfig{
		Sources: []SourceConfig{
			{Name: "interval", Type: "lifecycle-test-health-registration", Interval: 60},
			{Name: "scheduled", Type: "lifecycle-test-health-registration", Schedule: []string{"23:59"}},
		},
		HealthConfig: source.HealthConfig{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := engine.healthMonitor.GetHealth("interval").Status; got != source.StatusHealthy {
		t.Fatalf("interval status=%s", got)
	}
	if got := engine.healthMonitor.GetHealth("scheduled").Status; got != source.StatusAwaitingSchedule {
		t.Fatalf("scheduled status=%s", got)
	}
}

func TestPreparedSourceChangeReclassifiesScheduleHealth(t *testing.T) {
	source.Register("lifecycle-test-health-reconfigure", func(cfg source.Config) (source.Source, error) {
		return &lifecycleTestSource{name: cfg.Name}, nil
	})
	engine, err := NewNewsEngine(NewsEngineConfig{
		Sources:      []SourceConfig{{Name: "one", Type: "lifecycle-test-health-reconfigure", Interval: 60}},
		HealthConfig: source.HealthConfig{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	change, err := engine.PrepareSourceChange(SourceConfig{
		Name: "one", Type: "lifecycle-test-health-reconfigure", Schedule: []string{"23:59"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Activate(); err != nil {
		t.Fatal(err)
	}
	if got := engine.healthMonitor.GetHealth("one").Status; got != source.StatusAwaitingSchedule {
		t.Fatalf("scheduled status=%s", got)
	}
	engine.healthMonitor.RecordSuccess("one", time.Millisecond, 1)

	change, err = engine.PrepareSourceChange(SourceConfig{
		Name: "one", Type: "lifecycle-test-health-reconfigure", Schedule: []string{"22:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Activate(); err != nil {
		t.Fatal(err)
	}
	if got := engine.healthMonitor.GetHealth("one").Status; got != source.StatusHealthy {
		t.Fatalf("measured scheduled status after reconfigure=%s", got)
	}

	change, err = engine.PrepareSourceChange(SourceConfig{
		Name: "one", Type: "lifecycle-test-health-reconfigure", Interval: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := change.Activate(); err != nil {
		t.Fatal(err)
	}
	if got := engine.healthMonitor.GetHealth("one").Status; got != source.StatusHealthy {
		t.Fatalf("interval status after reconfigure=%s", got)
	}
}

func TestSourceOperationGateIsScopedAndStopCancelsBlockedEffect(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	createdA := 0
	source.Register("lifecycle-test-gated-a", func(cfg source.Config) (source.Source, error) {
		createdA++
		src := &lifecycleTestSource{name: cfg.Name}
		id := fmt.Sprintf("generation-%d", createdA)
		src.fetch = func() ([]*model.Message, error) {
			return []*model.Message{{ID: id, Title: id}}, nil
		}
		return src, nil
	})
	source.Register("lifecycle-test-gated-b", func(cfg source.Config) (source.Source, error) {
		return &lifecycleTestSource{name: cfg.Name}, nil
	})
	manager := channel.NewManager()
	sink := &workerTestChannel{
		BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush),
		block:       block,
		started:     started,
	}
	manager.Add(sink)
	engine, err := NewNewsEngine(NewsEngineConfig{
		Sources: []SourceConfig{{
			Name: "a", Type: "lifecycle-test-gated-a", Interval: 3600, Channels: []string{"main"},
		}},
		Router: NewRouter(manager),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.Start(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("source a did not enter blocked external effect")
	}

	unrelatedDone := make(chan error, 1)
	go func() {
		unrelatedDone <- engine.AddSource(SourceConfig{Name: "b", Type: "lifecycle-test-gated-b", Interval: 3600})
	}()
	select {
	case err := <-unrelatedDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated source mutation waited on source a gate")
	}

	change, err := engine.PrepareSourceChange(SourceConfig{
		Name: "a", Type: "lifecycle-test-gated-a", Interval: 3600, Channels: []string{"main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	activated := make(chan error, 1)
	go func() { activated <- change.Activate() }()
	select {
	case err := <-activated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("activation did not cancel the blocked source effect")
	}
	sink.mu.Lock()
	sent := len(sink.sent)
	sink.mu.Unlock()
	if sent != 0 {
		t.Fatalf("stale blocked source emitted after activation: sent=%d", sent)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("replacement source did not enter blocked external effect")
	}

	stopped := make(chan struct{})
	go func() {
		engine.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop was not bounded after cancelling source contexts")
	}
}

// FetchOnly 返回源消息,有产出时记录健康成功。
func TestFetchOnlyReturnsMessagesAndRecordsSuccess(t *testing.T) {
	src := &lifecycleTestSource{name: "one", fetch: func() ([]*model.Message, error) {
		return []*model.Message{{ID: "a", Title: "A", Content: "content"}}, nil
	}}
	hm := source.NewHealthMonitor(source.HealthConfig{DegradedThreshold: 3, UnhealthyThreshold: 5})
	hm.RegisterSource("one")
	engine := &NewsEngine{
		sources: map[string]source.Source{"one": src},
		sourceInfos: []newsSourceInfo{{name: "one"}},
		running:     true, ctx: context.Background(),
		healthMonitor: hm,
	}
	msgs, err := engine.FetchOnly(context.Background(), "one")
	if err != nil {
		t.Fatalf("FetchOnly: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Title != "A" {
		t.Fatalf("messages = %+v", msgs)
	}
	if got := hm.GetHealth("one"); got == nil || got.Status != source.StatusHealthy || got.ConsecutiveFails != 0 {
		t.Fatalf("health after success = %+v, want healthy/0 fails", got)
	}
}

// FetchOnly 空产出(304/空源)不触碰健康:不重置失败计数,避免掩盖真实故障。
func TestFetchOnlyEmptyDoesNotTouchHealth(t *testing.T) {
	src := &lifecycleTestSource{name: "one", fetch: func() ([]*model.Message, error) {
		return nil, nil // 模拟 304 空返回
	}}
	hm := source.NewHealthMonitor(source.HealthConfig{DegradedThreshold: 3, UnhealthyThreshold: 5})
	hm.RegisterSource("one")
	// 制造连续失败,健康进入异常
	for i := 0; i < 5; i++ {
		hm.RecordFailure("one", fmt.Errorf("fail %d", i))
	}
	if got := hm.GetHealth("one"); got.Status != source.StatusUnhealthy {
		t.Fatalf("precondition: status = %s, want unhealthy", got.Status)
	}
	engine := &NewsEngine{
		sources: map[string]source.Source{"one": src},
		sourceInfos: []newsSourceInfo{{name: "one"}},
		running:     true, ctx: context.Background(),
		healthMonitor: hm,
	}
	msgs, err := engine.FetchOnly(context.Background(), "one")
	if err != nil || len(msgs) != 0 {
		t.Fatalf("FetchOnly = %v, %v; want empty, nil", msgs, err)
	}
	// 空产出不得重置失败计数/状态
	if got := hm.GetHealth("one"); got.Status != source.StatusUnhealthy || got.ConsecutiveFails != 5 {
		t.Fatalf("health after empty fetch = status %s fails %d, want unhealthy/5 unchanged", got.Status, got.ConsecutiveFails)
	}
}

// FetchOnly 失败记录健康失败并返回错误。
func TestFetchOnlyFailureRecordsHealth(t *testing.T) {
	fetchErr := fmt.Errorf("boom")
	src := &lifecycleTestSource{name: "one", fetch: func() ([]*model.Message, error) {
		return nil, fetchErr
	}}
	hm := source.NewHealthMonitor(source.HealthConfig{DegradedThreshold: 3, UnhealthyThreshold: 5})
	hm.RegisterSource("one")
	engine := &NewsEngine{
		sources: map[string]source.Source{"one": src},
		sourceInfos: []newsSourceInfo{{name: "one"}},
		running:     true, ctx: context.Background(),
		healthMonitor: hm,
	}
	if _, err := engine.FetchOnly(context.Background(), "one"); !errors.Is(err, fetchErr) {
		t.Fatalf("err = %v, want %v", err, fetchErr)
	}
	if got := hm.GetHealth("one"); got == nil || got.ConsecutiveFails != 1 {
		t.Fatalf("health after failure = %+v, want 1 fail", got)
	}
}

// FetchOnly 并发门:同一源并发拉取被串行化(最大并发度 1)。
func TestFetchOnlyConcurrentGate(t *testing.T) {
	var active, maxActive atomic.Int64
	src := &lifecycleTestSource{name: "one", fetch: func() ([]*model.Message, error) {
		a := active.Add(1)
		for {
			cur := maxActive.Load()
			if a <= cur || maxActive.CompareAndSwap(cur, a) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		active.Add(-1)
		return []*model.Message{{ID: "a", Title: "A"}}, nil
	}}
	engine := &NewsEngine{
		sources: map[string]source.Source{"one": src},
		sourceInfos: []newsSourceInfo{{name: "one"}},
		running:     true, ctx: context.Background(),
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = engine.FetchOnly(context.Background(), "one")
		}()
	}
	wg.Wait()
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent fetches = %d, want 1", got)
	}
}

// FetchOnly 未知源与引擎停止时报错。
func TestFetchOnlyErrors(t *testing.T) {
	engine := &NewsEngine{}
	if _, err := engine.FetchOnly(context.Background(), "one"); !errors.Is(err, ErrEngineStopped) {
		t.Fatalf("stopped engine err = %v, want ErrEngineStopped", err)
	}
	engine = &NewsEngine{sources: map[string]source.Source{}, running: true, ctx: context.Background()}
	if _, err := engine.FetchOnly(context.Background(), "nope"); err == nil {
		t.Fatal("missing source should error")
	}
}
