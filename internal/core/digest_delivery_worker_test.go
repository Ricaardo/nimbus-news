package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/metrics"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestDigestDeliveryWorkerPartialSuccessAndWakeRetry(t *testing.T) {
	store := &workerTestStore{due: []digest.Attempt{
		{DeliveryID: "delivery", Sink: "ok", Token: "ok-1", Number: 1, Message: &model.Message{ID: "stable"}},
		{DeliveryID: "delivery", Sink: "retry", Token: "retry-1", Number: 1, Message: &model.Message{ID: "stable"}},
	}}
	manager := channel.NewManager()
	okSink := &workerTestChannel{BaseChannel: channel.NewBaseChannel("ok", "test", channel.ModePush)}
	retrySink := &workerTestChannel{
		BaseChannel: channel.NewBaseChannel("retry", "test", channel.ModePush),
		results:     []error{errors.New("down"), nil},
	}
	manager.Add(okSink)
	manager.Add(retrySink)
	worker := newDigestDeliveryWorker(store, manager, digestDeliveryWorkerOptions{pollInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.Start(ctx)
	waitForWorkerCompletions(t, store, 2)

	store.mu.Lock()
	store.due = []digest.Attempt{
		{DeliveryID: "delivery", Sink: "retry", Token: "retry-2", Number: 2, Message: &model.Message{ID: "stable"}},
	}
	store.mu.Unlock()
	worker.Wake()
	waitForWorkerCompletions(t, store, 3)
	worker.Stop()

	okSink.mu.Lock()
	okSent := append([]*model.Message(nil), okSink.sent...)
	okSink.mu.Unlock()
	retrySink.mu.Lock()
	retrySent := append([]*model.Message(nil), retrySink.sent...)
	retrySink.mu.Unlock()
	if len(okSent) != 1 || len(retrySent) != 2 {
		t.Fatalf("sent ok=%d retry=%d", len(okSent), len(retrySent))
	}
	for _, sent := range append(okSent, retrySent...) {
		if sent.ID != "stable" {
			t.Fatalf("message ID changed: %q", sent.ID)
		}
	}
}

func TestDigestDeliveryWorkerStartupRecoveryExpiryAndShutdown(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	store := &workerTestStore{
		due:     []digest.Attempt{{DeliveryID: "recovered", Sink: "main", Token: "token", Number: 1, Message: &model.Message{ID: "stable"}}},
		expired: 2,
	}
	manager := channel.NewManager()
	sink := &workerTestChannel{
		BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush),
		block:       block,
		started:     started,
	}
	manager.Add(sink)
	worker := newDigestDeliveryWorker(store, manager, digestDeliveryWorkerOptions{pollInterval: time.Hour})
	worker.Start(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("startup recovery did not claim the durable attempt")
	}

	stopped := make(chan struct{})
	go func() {
		worker.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("worker shutdown did not cancel and wait for in-flight send")
	}
	waitForWorkerCompletions(t, store, 1)
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.completions[0].deliveryID != "recovered" || store.completions[0].token != "token" {
		t.Fatalf("attempt fencing fields changed: %+v", store.completions[0])
	}
	if !errors.Is(store.completions[0].err, context.Canceled) {
		t.Fatalf("shutdown completion error=%v", store.completions[0].err)
	}
	if store.expired != 0 {
		t.Fatalf("startup did not expire due deliveries: %d", store.expired)
	}
}

func TestDigestDeliveryWorkerItemExpiryDoesNotSetDeadlineHealth(t *testing.T) {
	store := &workerTestStore{
		expiredItems: 1,
		stats: digest.Stats{
			Items:      map[digest.State]int{digest.Expired: 1},
			Deliveries: map[digest.DeliveryState]int{},
		},
	}
	worker := newDigestDeliveryWorker(store, channel.NewManager(), digestDeliveryWorkerOptions{})
	worker.runCycle(context.Background())
	snapshot := worker.Snapshot()
	if got := snapshot.LastDeadlineExpiry; !got.IsZero() {
		t.Fatalf("item expiry marked delivery deadline health: %v", got)
	}
	if snapshot.LastItemExpiry.IsZero() {
		t.Fatal("item expiry did not update item terminal health")
	}
}

func TestDigestDeliveryWorkerMaxAttemptExpiryDoesNotSetDeadlineHealth(t *testing.T) {
	store := &workerTestStore{
		expired:            1,
		expiredMaxAttempts: 1,
		stats: digest.Stats{
			Items:      map[digest.State]int{digest.Expired: 1},
			Deliveries: map[digest.DeliveryState]int{digest.DeliveryExpired: 1},
		},
	}
	worker := newDigestDeliveryWorker(store, channel.NewManager(), digestDeliveryWorkerOptions{})
	worker.runCycle(context.Background())
	snapshot := worker.Snapshot()
	if got := snapshot.LastDeadlineExpiry; !got.IsZero() {
		t.Fatalf("max-attempt terminal marked deadline health: %v", got)
	}
	if snapshot.LastMaxAttemptExpiry.IsZero() {
		t.Fatal("max-attempt terminal did not update terminal health")
	}
}

func TestDigestDeliveryWorkerDeadlineExpirySetsDeadlineHealth(t *testing.T) {
	store := &workerTestStore{
		expired:         1,
		expiredDeadline: 1,
		stats: digest.Stats{
			Items:      map[digest.State]int{digest.Expired: 1},
			Deliveries: map[digest.DeliveryState]int{digest.DeliveryExpired: 1},
		},
	}
	worker := newDigestDeliveryWorker(store, channel.NewManager(), digestDeliveryWorkerOptions{})
	worker.runCycle(context.Background())
	if got := worker.Snapshot().LastDeadlineExpiry; got.IsZero() {
		t.Fatal("deadline terminal did not update deadline health")
	}
}

func TestDigestDeliveryWorkerTracksOldestPendingItemAge(t *testing.T) {
	oldest := time.Now().UTC().Add(-25 * time.Hour)
	store := &workerTestStore{stats: digest.Stats{
		Items:               map[digest.State]int{digest.Pending: 10},
		Deliveries:          map[digest.DeliveryState]int{},
		OldestPendingItemAt: oldest,
	}}
	worker := newDigestDeliveryWorker(store, channel.NewManager(), digestDeliveryWorkerOptions{})
	worker.runCycle(context.Background())
	snapshot := worker.Snapshot()
	if !snapshot.OldestPendingSince.Equal(oldest) {
		t.Fatalf("oldest pending snapshot=%v, want %v", snapshot.OldestPendingSince, oldest)
	}
	age := testutil.ToFloat64(metrics.DigestOldestPendingAge)
	if age < (25*time.Hour-time.Minute).Seconds() || age > (25*time.Hour+time.Minute).Seconds() {
		t.Fatalf("oldest pending metric=%f seconds", age)
	}
}

func TestDigestDeliveryWorkerEmptyPollsPreserveFailureStreak(t *testing.T) {
	store := &workerTestStore{}
	manager := channel.NewManager()
	sink := &workerTestChannel{
		BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush),
		results:     []error{errors.New("down-1"), errors.New("down-2"), errors.New("down-3")},
	}
	manager.Add(sink)
	worker := newDigestDeliveryWorker(store, manager, digestDeliveryWorkerOptions{sendTimeout: time.Second})
	for attempt := 1; attempt <= 3; attempt++ {
		store.mu.Lock()
		store.due = []digest.Attempt{{
			DeliveryID: "delivery", Sink: "main", Token: "token",
			Number: attempt, Message: &model.Message{ID: "stable"},
		}}
		store.mu.Unlock()
		worker.runCycle(context.Background())
		worker.runCycle(context.Background()) // no-work poll must not imply recovery
	}
	if got := worker.Snapshot().ConsecutiveFailures; got != 3 {
		t.Fatalf("failure streak after empty polls=%d, want 3", got)
	}
}

func TestDigestDeliveryWorkerBoundsContextIgnoringSinkAndIsolatesAttempts(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	store := &workerTestStore{due: []digest.Attempt{
		{DeliveryID: "stalled", Sink: "stalled", Token: "stalled-token", Number: 1, Message: &model.Message{ID: "stable"}},
		{DeliveryID: "healthy", Sink: "healthy", Token: "healthy-token", Number: 1, Message: &model.Message{ID: "stable"}},
	}}
	manager := channel.NewManager()
	manager.Add(&workerTestChannel{
		BaseChannel:   channel.NewBaseChannel("stalled", "test", channel.ModePush),
		block:         block,
		started:       started,
		ignoreContext: true,
	})
	healthy := &workerTestChannel{BaseChannel: channel.NewBaseChannel("healthy", "test", channel.ModePush)}
	manager.Add(healthy)
	worker := newDigestDeliveryWorker(store, manager, digestDeliveryWorkerOptions{
		pollInterval: time.Hour, sendTimeout: 25 * time.Millisecond, concurrency: 2,
	})
	worker.Start(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stalled sink did not start")
	}
	waitForWorkerCompletions(t, store, 2)

	stopped := make(chan struct{})
	go func() {
		worker.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("context-ignoring sink made worker shutdown unbounded")
	}
	healthy.mu.Lock()
	healthySent := len(healthy.sent)
	healthy.mu.Unlock()
	if healthySent != 1 {
		t.Fatalf("healthy sink was blocked by stalled sink: sent=%d", healthySent)
	}
	if slots := len(worker.sendSlots); slots != 1 {
		t.Fatalf("stalled send slots=%d, want one bounded retained slot", slots)
	}
	store.mu.Lock()
	store.due = []digest.Attempt{{
		DeliveryID: "stalled-2", Sink: "stalled", Token: "stalled-token-2",
		Number: 1, Message: &model.Message{ID: "stable"},
	}}
	store.mu.Unlock()
	worker.runCycle(context.Background())
	if slots := len(worker.sendSlots); slots != 2 {
		t.Fatalf("second stalled send slots=%d, want concurrency cap 2", slots)
	}
	store.mu.Lock()
	store.due = []digest.Attempt{{
		DeliveryID: "stalled-3", Sink: "stalled", Token: "stalled-token-3",
		Number: 1, Message: &model.Message{ID: "stable"},
	}}
	store.mu.Unlock()
	blockedCtx, cancelBlocked := context.WithTimeout(context.Background(), 50*time.Millisecond)
	worker.runCycle(blockedCtx)
	cancelBlocked()
	if slots := len(worker.sendSlots); slots != 2 {
		t.Fatalf("send goroutines exceeded concurrency cap: slots=%d", slots)
	}
	close(block)
	deadline := time.Now().Add(time.Second)
	for len(worker.sendSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if slots := len(worker.sendSlots); slots != 0 {
		t.Fatalf("stalled send did not release after test cleanup: slots=%d", slots)
	}
}

func TestDigestDeliveryWorkerQueueWaitDoesNotConsumeSendTimeout(t *testing.T) {
	const attemptsCount = 6
	attempts := make([]digest.Attempt, 0, attemptsCount)
	for i := 0; i < attemptsCount; i++ {
		attempts = append(attempts, digest.Attempt{
			DeliveryID: fmt.Sprintf("delivery-%d", i),
			Sink:       "delayed",
			Token:      fmt.Sprintf("token-%d", i),
			Number:     1,
			Message:    &model.Message{ID: fmt.Sprintf("message-%d", i)},
		})
	}
	store := &workerTestStore{due: attempts}
	manager := channel.NewManager()
	sink := &queueTimeoutProbeChannel{
		BaseChannel: channel.NewBaseChannel("delayed", "test", channel.ModePush),
		started:     make(chan int, attemptsCount),
	}
	manager.Add(sink)
	worker := newDigestDeliveryWorker(store, manager, digestDeliveryWorkerOptions{
		sendTimeout: 20 * time.Millisecond,
		attemptTTL:  time.Second,
		batchSize:   attemptsCount,
		concurrency: 2,
	})

	done := make(chan struct{})
	go func() {
		worker.runCycle(context.Background())
		close(done)
	}()
	deadlock := time.NewTimer(2 * time.Second)
	defer deadlock.Stop()
	seen := make(map[int]struct{}, attemptsCount)
	for len(seen) < attemptsCount {
		select {
		case call := <-sink.started:
			seen[call] = struct{}{}
		case <-deadlock.C:
			t.Fatalf("timed out waiting for queued sends: started=%v", seen)
		}
	}
	select {
	case <-done:
	case <-deadlock.C:
		t.Fatal("worker did not finish after all send events")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.completions) != attemptsCount {
		t.Fatalf("completions=%d, want %d", len(store.completions), attemptsCount)
	}
	failures := 0
	for _, completion := range store.completions {
		if completion.err != nil {
			failures++
		}
	}
	if failures != 2 {
		t.Fatalf("completion failures=%d, want only the two context-driven probe sends", failures)
	}
}

type queueTimeoutProbeChannel struct {
	channel.BaseChannel
	mu      sync.Mutex
	calls   int
	started chan int
}

func (c *queueTimeoutProbeChannel) Send(ctx context.Context, _ *model.Message) error {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	c.started <- call
	if call <= 2 {
		<-ctx.Done()
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (*queueTimeoutProbeChannel) SendBatch(context.Context, []*model.Message) error { return nil }
func (*queueTimeoutProbeChannel) Reply(context.Context, string, *model.Message) error {
	return nil
}
func (*queueTimeoutProbeChannel) Start(context.Context) error { return nil }
func (*queueTimeoutProbeChannel) Stop() error                 { return nil }

func waitForWorkerCompletions(t *testing.T, store *workerTestStore, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		got := len(store.completions)
		store.mu.Unlock()
		if got >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d completions", count)
}
