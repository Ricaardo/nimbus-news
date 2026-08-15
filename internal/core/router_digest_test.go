package core

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

type workerTestStore struct {
	mu                 sync.Mutex
	prepared           []*digest.Delivery
	due                []digest.Attempt
	completions        []workerTestCompletion
	expired            int
	expiredItems       int
	expiredDeadline    int
	expiredMaxAttempts int
	stats              digest.Stats
}

type workerTestCompletion struct {
	deliveryID string
	sink       string
	token      string
	err        error
}

func (*workerTestStore) Enqueue(context.Context, string, digest.Briefing, *model.Message) error {
	return nil
}
func (*workerTestStore) Lease(context.Context, digest.Briefing, int, time.Duration) (*digest.Lease, error) {
	return nil, nil
}
func (*workerTestStore) Ack(context.Context, string) error { return nil }
func (s *workerTestStore) PrepareDelivery(_ context.Context, leaseID string, message *model.Message, sinks []string, deadline time.Time) (*digest.Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	required := append([]string(nil), sinks...)
	sort.Strings(required)
	delivery := &digest.Delivery{
		ID: leaseID, LeaseID: leaseID, Message: message, RequiredSinks: required,
		State: digest.DeliveryPending, CreatedAt: time.Now(), Deadline: deadline,
	}
	s.prepared = append(s.prepared, delivery)
	return delivery, nil
}
func (s *workerTestStore) ClaimDueAttempts(context.Context, int, time.Duration) ([]digest.Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempts := append([]digest.Attempt(nil), s.due...)
	s.due = nil
	return attempts, nil
}
func (s *workerTestStore) CompleteAttempt(_ context.Context, deliveryID, sink, token string, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completions = append(s.completions, workerTestCompletion{deliveryID, sink, token, err})
	return nil
}
func (s *workerTestStore) ExpireDue(context.Context) (digest.ExpiryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	expired := s.expired
	expiredItems := s.expiredItems
	s.expired = 0
	s.expiredItems = 0
	deadline := s.expiredDeadline
	maxAttempts := s.expiredMaxAttempts
	s.expiredDeadline = 0
	s.expiredMaxAttempts = 0
	return digest.ExpiryResult{
		Items: expiredItems, Deliveries: expired,
		DeadlineDeliveries: deadline, MaxAttemptDeliveries: maxAttempts,
	}, nil
}
func (s *workerTestStore) Stats(context.Context) (digest.Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats, nil
}
func (*workerTestStore) ListDeliveries(context.Context, digest.DeliveryFilter) ([]digest.Delivery, error) {
	return nil, nil
}

type workerTestChannel struct {
	channel.BaseChannel
	mu            sync.Mutex
	sent          []*model.Message
	results       []error
	block         <-chan struct{}
	started       chan<- struct{}
	ignoreContext bool
	delay         time.Duration
}

type idempotentWorkerTestChannel struct {
	*workerTestChannel
	key string
}

func (c *idempotentWorkerTestChannel) SendIdempotent(ctx context.Context, message *model.Message, key string) error {
	c.key = key
	return c.Send(ctx, message)
}

func (c *workerTestChannel) Send(ctx context.Context, message *model.Message) error {
	if c.started != nil {
		select {
		case c.started <- struct{}{}:
		default:
		}
	}
	if c.block != nil {
		if c.ignoreContext {
			<-c.block
		} else {
			select {
			case <-c.block:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if c.delay > 0 {
		timer := time.NewTimer(c.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, message)
	if len(c.results) == 0 {
		return nil
	}
	result := c.results[0]
	c.results = c.results[1:]
	return result
}
func (c *workerTestChannel) SendBatch(context.Context, []*model.Message) error { return nil }
func (c *workerTestChannel) Reply(context.Context, string, *model.Message) error {
	return nil
}
func (*workerTestChannel) Start(context.Context) error { return nil }
func (*workerTestChannel) Stop() error                 { return nil }

func TestDigestBriefingPreparesBeforeWorkerSend(t *testing.T) {
	manager := channel.NewManager()
	sink := &workerTestChannel{BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush)}
	manager.Add(sink)
	digests := &workerTestStore{}
	router := NewRouter(manager)
	router.SetDigestStore(digests)
	msg := &model.Message{ID: "stable-message", Content: "rendered"}
	msg.SetMetadata("digest_lease_id", "lease-1")

	if err := router.DispatchNews(context.Background(), msg, []string{"z", "main"}); err != nil {
		t.Fatal(err)
	}
	if len(sink.sent) != 0 {
		t.Fatal("digest was sent synchronously before durable preparation")
	}
	if len(digests.prepared) != 1 {
		t.Fatalf("prepared=%d", len(digests.prepared))
	}
	prepared := digests.prepared[0]
	if prepared.Message.ID != msg.ID || prepared.Message.Content != msg.Content {
		t.Fatalf("prepared payload changed: %+v", prepared.Message)
	}
	if got := prepared.RequiredSinks; len(got) != 2 || got[0] != "main" || got[1] != "z" {
		t.Fatalf("required sinks not canonical: %v", got)
	}
	if remaining := time.Until(prepared.Deadline); remaining < 23*time.Hour || remaining > 24*time.Hour+time.Minute {
		t.Fatalf("unexpected deadline: %s", remaining)
	}
}

func TestDigestDeliveryWorkerUsesStableOptionalIdempotencyKey(t *testing.T) {
	manager := channel.NewManager()
	sink := &idempotentWorkerTestChannel{workerTestChannel: &workerTestChannel{
		BaseChannel: channel.NewBaseChannel("main", "test", channel.ModePush),
	}}
	manager.Add(sink)
	store := &workerTestStore{due: []digest.Attempt{{
		DeliveryID: "delivery-1", Sink: "main", Token: "token-1", Number: 2,
		Message: &model.Message{ID: "payload"},
	}}}
	worker := newDigestDeliveryWorker(store, manager, digestDeliveryWorkerOptions{})

	if err := worker.deliverAttempt(context.Background(), store.due[0]); err != nil {
		t.Fatal(err)
	}
	if sink.key != "digest:delivery-1:main" {
		t.Fatalf("idempotency key=%q", sink.key)
	}
}

func TestAggregateDigestSinkMetricsSumsUnknownSinks(t *testing.T) {
	older := time.Date(2026, 7, 30, 7, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	aggregated := aggregateDigestSinkMetrics(map[string]digest.SinkStats{
		"unknown-one": {Pending: 2, OldestPendingAt: newer},
		"unknown-two": {Pending: 3, OldestPendingAt: older},
		"wechat-main": {Pending: 4, OldestPendingAt: newer},
	})
	if got := aggregated["other"]; got.Pending != 5 || !got.OldestPendingAt.Equal(older) {
		t.Fatalf("other metrics=%+v", got)
	}
	if got := aggregated["wechat-main"]; got.Pending != 4 || !got.OldestPendingAt.Equal(newer) {
		t.Fatalf("known metrics=%+v", got)
	}
}
