package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	bolt "go.etcd.io/bbolt"
)

func TestDigestV1RecordNormalizesOnTouchAndAckedNeverReopens(t *testing.T) {
	owner := newDigestTestOwner(t, "v1.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	created := now.Add(-time.Hour)
	ds := mustDigestStore(t, owner.DB(), now)
	v1 := map[string]any{
		"id": "v1", "source": "rss", "briefing": digest.PreMarket,
		"message": map[string]any{"id": "m1", "content": "v1"},
		"state":   "acked", "created_at": created, "acknowledged_at": created.Add(time.Minute),
	}
	putDigestTestJSON(t, owner.DB(), digestBucketV1, "v1", v1)
	items, err := ds.ListItems(context.Background(), digest.ItemFilter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	item := items[0]
	if item.SchemaVersion != digest.SchemaVersion || item.Priority != 0 ||
		!item.ExpiresAt.Equal(created.Add(digestDefaultTTL)) || item.State != digest.Acked ||
		item.TerminalAt.IsZero() {
		t.Fatalf("v1 item not normalized conservatively: %+v", item)
	}
	if lease, err := ds.Lease(context.Background(), digest.PreMarket, 1, time.Minute); err != nil || lease != nil {
		t.Fatalf("acked v1 item reopened: lease=%+v err=%v", lease, err)
	}
}

func TestDigestStoreMigratesLegacyDefaultTTLOnce(t *testing.T) {
	owner := newDigestTestOwner(t, "ttl-migration.db")
	defer owner.Close()
	now := time.Date(2026, 8, 1, 5, 0, 0, 0, time.UTC)
	legacy := digest.Item{
		ID: "legacy-default-ttl", SchemaVersion: digest.SchemaVersion,
		Source: "bloomberg-markets", Briefing: digest.USPreview,
		Message: &model.Message{ID: "legacy-message", Title: "legacy"},
		State:   digest.Pending, CreatedAt: now, ExpiresAt: now.Add(digestLegacyDefaultTTL),
	}
	if err := owner.DB().Update(func(tx *bolt.Tx) error {
		items, err := tx.CreateBucketIfNotExists([]byte(digestBucketV1))
		if err != nil {
			return err
		}
		return putJSON(items, []byte(legacy.ID), legacy)
	}); err != nil {
		t.Fatal(err)
	}

	store, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListItems(context.Background(), digest.ItemFilter{State: digest.Pending})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].ExpiresAt.Equal(now.Add(digestDefaultTTL)) {
		t.Fatalf("legacy TTL was not migrated: %#v", items)
	}

	if err := store.EnqueueWithOptions(context.Background(), "custom", digest.USPreview,
		&model.Message{ID: "custom-ttl", Title: "custom"}, 0, digestLegacyDefaultTTL); err != nil {
		t.Fatal(err)
	}
	store, err = NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	items, err = store.ListItems(context.Background(), digest.ItemFilter{State: digest.Pending})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Source == "custom" && !item.ExpiresAt.Equal(item.CreatedAt.Add(digestLegacyDefaultTTL)) {
			t.Fatalf("explicit TTL was unexpectedly migrated: %#v", item)
		}
	}
}

func TestDigestLeasePriorityAgingAndExpiry(t *testing.T) {
	owner := newDigestTestOwner(t, "priority.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now.Add(-31*time.Hour))
	if err := ds.EnqueueWithOptions(context.Background(), "old", digest.Closing, &model.Message{ID: "old"}, -20, 72*time.Hour); err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return now }
	if err := ds.EnqueueWithOptions(context.Background(), "fresh", digest.Closing, &model.Message{ID: "fresh"}, 100, time.Hour); err != nil {
		t.Fatal(err)
	}
	lease, err := ds.Lease(context.Background(), digest.Closing, 1, time.Minute)
	if err != nil || lease == nil || len(lease.Items) != 1 {
		t.Fatalf("lease=%+v err=%v", lease, err)
	}
	if lease.Items[0].Source != "old" {
		t.Fatalf("aging did not prevent starvation: %+v", lease.Items[0])
	}

	if err := ds.EnqueueWithOptions(context.Background(), "expires", digest.USPreview, &model.Message{ID: "expires"}, 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return now.Add(2 * time.Minute) }
	if lease, err := ds.Lease(context.Background(), digest.USPreview, 1, time.Minute); err != nil || lease != nil {
		t.Fatalf("expired item leased: lease=%+v err=%v", lease, err)
	}
	expired, err := ds.ListItems(context.Background(), digest.ItemFilter{State: digest.Expired})
	if err != nil || len(expired) != 1 || expired[0].TerminalReason != "ttl" {
		t.Fatalf("expired=%+v err=%v", expired, err)
	}
}

func TestDigestLeaseLowPrioritySurvivesContinuousHighPriorityIngress(t *testing.T) {
	owner := newDigestTestOwner(t, "priority-starvation.db")
	defer owner.Close()
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), start)
	if err := ds.EnqueueWithOptions(
		context.Background(), "old-low", digest.Closing, &model.Message{ID: "old-low"},
		-100, digestDefaultTTL,
	); err != nil {
		t.Fatal(err)
	}

	for hour := 1; hour <= 16; hour++ {
		now := start.Add(time.Duration(hour) * time.Hour)
		ds.now = func() time.Time { return now }
		freshID := fmt.Sprintf("fresh-%02d", hour)
		if err := ds.EnqueueWithOptions(
			context.Background(), freshID, digest.Closing, &model.Message{ID: freshID},
			100, digestDefaultTTL,
		); err != nil {
			t.Fatal(err)
		}
		lease, err := ds.Lease(context.Background(), digest.Closing, 1, time.Minute)
		if err != nil || lease == nil || len(lease.Items) != 1 {
			t.Fatalf("hour=%d lease=%+v err=%v", hour, lease, err)
		}
		if lease.Items[0].Source != freshID {
			t.Fatalf("hour=%d old item won before aging crossed priority spread: source=%q", hour, lease.Items[0].Source)
		}
		if err := ds.Ack(context.Background(), lease.ID); err != nil {
			t.Fatal(err)
		}
	}

	// At 16h40, 200 five-minute age points exactly offset the -100..100
	// priority spread; deterministic oldest-first tie-breaking selects old-low.
	leasedOldAt := start.Add(16*time.Hour + 40*time.Minute)
	ds.now = func() time.Time { return leasedOldAt }
	if err := ds.EnqueueWithOptions(
		context.Background(), "fresh-boundary", digest.Closing, &model.Message{ID: "fresh-boundary"},
		100, digestDefaultTTL,
	); err != nil {
		t.Fatal(err)
	}
	lease, err := ds.Lease(context.Background(), digest.Closing, 1, time.Minute)
	if err != nil || lease == nil || len(lease.Items) != 1 {
		t.Fatalf("boundary lease=%+v err=%v", lease, err)
	}
	if lease.Items[0].Source != "old-low" {
		t.Fatalf("continuous high-priority ingress starved old item: source=%q", lease.Items[0].Source)
	}
	if !leasedOldAt.Before(start.Add(digestDefaultTTL)) {
		t.Fatalf("low-priority item leased too late: %v", leasedOldAt)
	}
}

func TestDigestCorruptRecordRollsBackLease(t *testing.T) {
	owner := newDigestTestOwner(t, "corrupt.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	if err := ds.Enqueue(context.Background(), "rss", digest.PreMarket, &model.Message{ID: "valid"}); err != nil {
		t.Fatal(err)
	}
	items, err := ds.ListItems(context.Background(), digest.ItemFilter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if err := owner.DB().Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket([]byte(digestBucketV1)).Put([]byte("corrupt"), []byte("{")); err != nil {
			return err
		}
		return incrementDigestGenerationTx(tx)
	}); err != nil {
		t.Fatal(err)
	}
	if lease, err := ds.Lease(context.Background(), digest.PreMarket, 10, time.Minute); err == nil || lease != nil {
		t.Fatalf("corrupt lease should fail atomically: lease=%+v err=%v", lease, err)
	}
	assertDigestItemState(t, owner.DB(), items[0].ID, digest.Pending)
}

func TestDigestDeliveryPreparePartialSuccessRetryAndStaleFence(t *testing.T) {
	owner := newDigestTestOwner(t, "delivery.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	if err := ds.Enqueue(context.Background(), "rss", digest.PreMarket, &model.Message{ID: "item"}); err != nil {
		t.Fatal(err)
	}
	lease, err := ds.Lease(context.Background(), digest.PreMarket, 1, time.Hour)
	if err != nil || lease == nil {
		t.Fatalf("lease=%+v err=%v", lease, err)
	}
	payload := &model.Message{ID: "payload", Content: "exact", Metadata: map[string]any{"nested": map[string]any{"value": "original"}}}
	prepared, err := ds.PrepareDelivery(context.Background(), lease.ID, payload, []string{"wechat", "discord", "wechat"}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	payload.Content = "mutated"
	payload.Metadata["nested"].(map[string]any)["value"] = "mutated"
	again, err := ds.PrepareDelivery(context.Background(), lease.ID, prepared.Message, []string{"discord", "wechat"}, now.Add(2*time.Hour))
	if err != nil || again.ID != prepared.ID {
		t.Fatalf("idempotent prepare=%+v err=%v", again, err)
	}
	if _, err := ds.PrepareDelivery(context.Background(), lease.ID, &model.Message{ID: "different"}, []string{"discord", "wechat"}, now.Add(time.Hour)); err == nil {
		t.Fatal("prepare accepted payload hash mismatch")
	}

	attempts, err := ds.ClaimDueAttempts(context.Background(), 2, time.Minute)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	bySink := make(map[string]digest.Attempt)
	for _, attempt := range attempts {
		bySink[attempt.Sink] = attempt
		if attempt.Message.Content != "exact" {
			t.Fatalf("persisted payload mutated: %+v", attempt.Message)
		}
	}
	if err := ds.CompleteAttempt(context.Background(), prepared.ID, "discord", "stale", nil); err == nil {
		t.Fatal("stale attempt token accepted")
	}
	if err := ds.CompleteAttempt(context.Background(), prepared.ID, "discord", bySink["discord"].Token, nil); err != nil {
		t.Fatal(err)
	}
	if err := ds.CompleteAttempt(context.Background(), prepared.ID, "wechat", bySink["wechat"].Token, errors.New("temporary")); err != nil {
		t.Fatal(err)
	}
	if err := ds.RetryDelivery(context.Background(), prepared.ID); err != nil {
		t.Fatal(err)
	}
	retry, err := ds.ClaimDueAttempts(context.Background(), 2, time.Minute)
	if err != nil || len(retry) != 1 || retry[0].Sink != "wechat" {
		t.Fatalf("retry should claim failed sink only: %+v err=%v", retry, err)
	}
	if err := ds.CompleteAttempt(context.Background(), prepared.ID, "wechat", retry[0].Token, nil); err != nil {
		t.Fatal(err)
	}
	deliveries, err := ds.ListDeliveries(context.Background(), digest.DeliveryFilter{})
	if err != nil || len(deliveries) != 1 || deliveries[0].State != digest.DeliveryDelivered {
		t.Fatalf("deliveries=%+v err=%v", deliveries, err)
	}
	assertDigestItemState(t, owner.DB(), lease.Items[0].ID, digest.Acked)
}

func TestDigestDeliveryDeadlineMaxAttemptsAndTerminalPurge(t *testing.T) {
	owner := newDigestTestOwner(t, "terminal.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	first := prepareDigestTestDelivery(t, ds, "deadline", now.Add(time.Minute))
	ds.now = func() time.Time { return now.Add(2 * time.Minute) }
	count, err := ds.ExpireDue(context.Background())
	if err != nil || count.Deliveries != 1 || count.DeadlineDeliveries != 1 || count.MaxAttemptDeliveries != 0 {
		t.Fatalf("expire count=%+v err=%v", count, err)
	}
	assertDigestItemState(t, owner.DB(), first.ItemIDs[0], digest.Expired)

	ds.now = func() time.Time { return now }
	second := prepareDigestTestDelivery(t, ds, "attempts", now.Add(time.Hour))
	if err := owner.DB().Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(digestDeliveriesBucket))
		var delivery digest.Delivery
		if err := json.Unmarshal(b.Get([]byte(second.ID)), &delivery); err != nil {
			return err
		}
		checkpoint := delivery.Sinks["sink"]
		checkpoint.Attempts = digestMaxAttempts
		delivery.Sinks["sink"] = checkpoint
		if err := putJSON(b, []byte(delivery.ID), delivery); err != nil {
			return err
		}
		return incrementDigestGenerationTx(tx)
	}); err != nil {
		t.Fatal(err)
	}
	count, err = ds.ExpireDue(context.Background())
	if err != nil || count.Deliveries != 1 || count.DeadlineDeliveries != 0 || count.MaxAttemptDeliveries != 1 {
		t.Fatalf("max-attempt expiry count=%+v err=%v", count, err)
	}

	third := prepareDigestTestDelivery(t, ds, "active", now.Add(time.Hour))
	if err := ds.Purge(context.Background(), now.Add(3*time.Minute), 0); err != nil {
		t.Fatal(err)
	}
	deliveries, err := ds.ListDeliveries(context.Background(), digest.DeliveryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].ID != third.ID {
		t.Fatalf("purge removed active or retained old terminal delivery: %+v", deliveries)
	}
}

func TestDigestStatsIncludeActiveItemAgesAndPerSinkHealth(t *testing.T) {
	owner := newDigestTestOwner(t, "stats-health.db")
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	if err := ds.Enqueue(context.Background(), "leased", digest.Closing, &model.Message{ID: "leased"}); err != nil {
		t.Fatal(err)
	}
	lease, err := ds.Lease(context.Background(), digest.Closing, 1, time.Hour)
	if err != nil || lease == nil {
		t.Fatalf("lease=%v err=%v", lease, err)
	}
	delivery, err := ds.PrepareDelivery(context.Background(), lease.ID, &model.Message{ID: "payload"},
		[]string{"good", "bad"}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := ds.ClaimDueAttempts(context.Background(), 2, time.Minute)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("attempts=%d err=%v", len(attempts), err)
	}
	for _, attempt := range attempts {
		var deliveryErr error
		if attempt.Sink == "bad" {
			deliveryErr = errors.New("downstream unavailable")
		}
		if err := ds.CompleteAttempt(context.Background(), delivery.ID, attempt.Sink, attempt.Token, deliveryErr); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := ds.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Items[digest.Delivering] != 1 || !stats.OldestDeliveringItemAt.Equal(now) {
		t.Fatalf("delivering stats=%+v", stats)
	}
	if stats.Sinks["bad"].Pending != 1 || stats.Sinks["bad"].LastFailedAt.IsZero() {
		t.Fatalf("bad sink stats=%+v", stats.Sinks["bad"])
	}
	if stats.Sinks["bad"].ConsecutiveFailures != 1 {
		t.Fatalf("bad sink consecutive failures=%d", stats.Sinks["bad"].ConsecutiveFailures)
	}
	if stats.Sinks["good"].Pending != 0 || stats.Sinks["good"].LastDeliveredAt.IsZero() {
		t.Fatalf("good sink stats=%+v", stats.Sinks["good"])
	}
}

func TestDigestRetiredSinkHealthIsFilteredAndPurgedAfterRetention(t *testing.T) {
	owner := newDigestTestOwner(t, "retired-sink-health.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	ds.SetActiveSinks([]string{"old-sink"})

	oldDelivery := prepareDigestTestDelivery(t, ds, "old", now.Add(time.Hour))
	attempts, err := ds.ClaimDueAttempts(context.Background(), 1, time.Minute)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("old attempts=%d err=%v", len(attempts), err)
	}
	if err := ds.CompleteAttempt(context.Background(), oldDelivery.ID, "sink", attempts[0].Token, errors.New("retired")); err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err := ds.ExpireDue(context.Background()); err != nil {
		t.Fatal(err)
	}

	ds.SetActiveSinks([]string{"new-sink"})
	stats, err := ds.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := stats.Sinks["sink"]; exists {
		t.Fatalf("retired sink still affects health: %+v", stats.Sinks)
	}
	if _, exists := stats.Sinks["new-sink"]; !exists {
		t.Fatalf("current sink missing from health: %+v", stats.Sinks)
	}

	ds.now = func() time.Time { return now.Add(10 * 24 * time.Hour) }
	if err := ds.Purge(context.Background(), now.Add(3*24*time.Hour), 0); err != nil {
		t.Fatal(err)
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(digestSinkHealthBucket))
		if got := bucket.Stats().KeyN; got != 0 {
			t.Fatalf("retired sink health bucket size=%d want=0", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkDigestStatsIndexedRebuild(b *testing.B) {
	path := filepath.Join(b.TempDir(), "digest-stats-benchmark.db")
	owner, err := NewBoltStore(path)
	if err != nil {
		b.Fatal(err)
	}
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds, err := NewDigestStore(owner.DB())
	if err != nil {
		b.Fatal(err)
	}
	ds.now = func() time.Time { return now }
	for i := 0; i < 1000; i++ {
		if err := ds.Enqueue(context.Background(), "benchmark", digest.Closing,
			&model.Message{ID: fmt.Sprintf("item-%04d", i)}); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ds.statsMu.Lock()
		ds.statsValid = false
		ds.statsMu.Unlock()
		if _, err := ds.Stats(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDigestSinkConsecutiveFailuresFollowInterleavedSameTimestampOutcomesAfterRestart(t *testing.T) {
	owner := newDigestTestOwner(t, "sink-failure-sequence.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)

	complete := func(delivery *digest.Delivery, deliveryErr error) {
		t.Helper()
		attempts, err := ds.ClaimDueAttempts(context.Background(), 1, time.Minute)
		if err != nil || len(attempts) != 1 || attempts[0].DeliveryID != delivery.ID {
			t.Fatalf("delivery=%q attempts=%+v err=%v", delivery.ID, attempts, err)
		}
		if err := ds.CompleteAttempt(
			context.Background(), delivery.ID, attempts[0].Sink, attempts[0].Token, deliveryErr,
		); err != nil {
			t.Fatal(err)
		}
	}
	assertFailures := func(store *DigestStore, want int) {
		t.Helper()
		stats, err := store.Stats(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got := stats.Sinks["sink"].ConsecutiveFailures; got != want {
			t.Fatalf("consecutive failures=%d want=%d stats=%+v", got, want, stats.Sinks["sink"])
		}
	}

	failing := prepareDigestTestDelivery(t, ds, "failure", now.Add(time.Hour))
	complete(failing, errors.New("failure-1"))
	assertFailures(ds, 1)

	success := prepareDigestTestDelivery(t, ds, "success", now.Add(time.Hour))
	complete(success, nil)
	assertFailures(ds, 0)

	if err := ds.updateDigest(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte(digestDeliveriesBucket)).Get([]byte(failing.ID))
		var delivery digest.Delivery
		if err := json.Unmarshal(raw, &delivery); err != nil {
			return err
		}
		checkpoint := delivery.Sinks["sink"]
		checkpoint.NextAttemptAt = now
		delivery.Sinks["sink"] = checkpoint
		return putDigestDelivery(tx, []byte(delivery.ID), delivery)
	}); err != nil {
		t.Fatal(err)
	}
	complete(failing, errors.New("failure-2"))
	assertFailures(ds, 1)

	restarted, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return now }
	assertFailures(restarted, 1)

	deliveries, err := restarted.ListDeliveries(context.Background(), digest.DeliveryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var outcomeOrders []uint64
	for _, delivery := range deliveries {
		for _, outcome := range delivery.Sinks["sink"].Outcomes {
			if !outcome.At.Equal(now) {
				t.Fatalf("outcome timestamp=%v want=%v", outcome.At, now)
			}
			if outcome.Order == 0 {
				t.Fatal("outcome completion order was not persisted")
			}
			outcomeOrders = append(outcomeOrders, outcome.Order)
		}
	}
	if len(outcomeOrders) != 3 {
		t.Fatalf("persisted outcomes=%v, want three", outcomeOrders)
	}
	sortedOrders := append([]uint64(nil), outcomeOrders...)
	sort.Slice(sortedOrders, func(i, j int) bool { return sortedOrders[i] < sortedOrders[j] })
	for i := 1; i < len(sortedOrders); i++ {
		if sortedOrders[i] == sortedOrders[i-1] {
			t.Fatalf("outcome completion orders are not unique: %v", sortedOrders)
		}
	}
	if err := restarted.Purge(context.Background(), now.Add(time.Hour), 0); err != nil {
		t.Fatal(err)
	}
	assertFailures(restarted, 1)
	afterPurge, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	afterPurge.now = func() time.Time { return now }
	assertFailures(afterPurge, 1)
}

func TestDigestLegacySinkHealthMigrationIsDeterministicAndCanonicalAfterFirstCompletion(t *testing.T) {
	owner := newDigestTestOwner(t, "legacy-sink-health.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	mustDigestStore(t, owner.DB(), now)
	deadline := now.Add(time.Hour)

	// Legacy checkpoints have no outcome sequence. When timestamps tie their
	// true completion order is unrecoverable, so migration deterministically
	// falls back to delivery ID order: this failure precedes the success below.
	putDigestTestJSON(t, owner.DB(), digestDeliveriesBucket, "a-legacy-failure", digest.Delivery{
		ID: "a-legacy-failure", SchemaVersion: 2, RequiredSinks: []string{"sink"},
		Sinks: map[string]digest.SinkCheckpoint{"sink": {
			Sink: "sink", Attempts: 2, FailedAttempts: 2, LastFailedAt: now,
			LastError: "legacy failure", NextAttemptAt: deadline,
		}},
		State: digest.DeliveryPending, CreatedAt: now, UpdatedAt: now, Deadline: deadline,
	})
	putDigestTestJSON(t, owner.DB(), digestDeliveriesBucket, "b-legacy-success", digest.Delivery{
		ID: "b-legacy-success", SchemaVersion: 2, RequiredSinks: []string{"sink"},
		Sinks: map[string]digest.SinkCheckpoint{"sink": {
			Sink: "sink", Delivered: true, DeliveredAt: now,
		}},
		State: digest.DeliveryDelivered, CreatedAt: now, UpdatedAt: now,
		Deadline: deadline, TerminalAt: now, TerminalReason: "delivered",
	})
	putDigestTestJSON(t, owner.DB(), digestDeliveriesBucket, "c-current", digest.Delivery{
		ID: "c-current", SchemaVersion: digest.SchemaVersion, RequiredSinks: []string{"sink"},
		Sinks: map[string]digest.SinkCheckpoint{"sink": {
			Sink: "sink", Attempts: 1, AttemptToken: "failure-token",
			ClaimedUntil: deadline,
		}},
		State: digest.DeliveryPending, CreatedAt: now, UpdatedAt: now, Deadline: deadline,
	})
	if err := owner.DB().Update(func(tx *bolt.Tx) error {
		return tx.DeleteBucket([]byte(digestSinkHealthBucket))
	}); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return now }
	stats, err := restarted.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Sinks["sink"].ConsecutiveFailures; got != 0 {
		t.Fatalf("legacy startup failure streak=%d, want deterministic best-effort 0", got)
	}

	if err := restarted.CompleteAttempt(
		context.Background(), "c-current", "sink", "failure-token", errors.New("new failure"),
	); err != nil {
		t.Fatal(err)
	}
	stats, err = restarted.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Sinks["sink"].ConsecutiveFailures; got != 1 {
		t.Fatalf("first canonical failure streak=%d, want 1 without legacy double-count", got)
	}

	if err := restarted.updateDigest(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte(digestDeliveriesBucket)).Get([]byte("c-current"))
		var delivery digest.Delivery
		if err := json.Unmarshal(raw, &delivery); err != nil {
			return err
		}
		checkpoint := delivery.Sinks["sink"]
		checkpoint.Attempts++
		checkpoint.AttemptToken = "success-token"
		checkpoint.ClaimedUntil = deadline
		delivery.Sinks["sink"] = checkpoint
		return putDigestDelivery(tx, []byte(delivery.ID), delivery)
	}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.CompleteAttempt(
		context.Background(), "c-current", "sink", "success-token", nil,
	); err != nil {
		t.Fatal(err)
	}
	stats, err = restarted.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Sinks["sink"].ConsecutiveFailures; got != 0 {
		t.Fatalf("canonical success did not clear failure streak: %d", got)
	}

	putDigestTestJSON(t, owner.DB(), digestDeliveriesBucket, "d-current", digest.Delivery{
		ID: "d-current", SchemaVersion: digest.SchemaVersion, RequiredSinks: []string{"sink"},
		Sinks: map[string]digest.SinkCheckpoint{"sink": {
			Sink: "sink", Attempts: 1, AttemptToken: "final-failure-token",
			ClaimedUntil: deadline,
		}},
		State: digest.DeliveryPending, CreatedAt: now, UpdatedAt: now, Deadline: deadline,
	})
	if err := restarted.CompleteAttempt(
		context.Background(), "d-current", "sink", "final-failure-token", errors.New("final failure"),
	); err != nil {
		t.Fatal(err)
	}
	afterCompletionRestart, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	afterCompletionRestart.now = func() time.Time { return now }
	stats, err = afterCompletionRestart.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.Sinks["sink"].ConsecutiveFailures; got != 1 {
		t.Fatalf("canonical failure streak after restart=%d, want 1", got)
	}
}

func TestDigestGenerationIgnoresPersistentNewsWrites(t *testing.T) {
	owner := newDigestTestOwner(t, "shared-store-generation.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	if err := ds.Enqueue(context.Background(), "digest", digest.Closing, &model.Message{ID: "digest"}); err != nil {
		t.Fatal(err)
	}
	first, err := ds.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	generation := ds.statsGeneration

	ctx, cancel := context.WithCancel(context.Background())
	newsStore, err := NewPersistentNewsStoreContext(ctx, owner.DB(), NewsStoreConfig{
		MaxItems: 10,
		TTL:      time.Hour,
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() {
		cancel()
		newsStore.WaitCleanup()
	}()
	if err := newsStore.Save(context.Background(), &model.News{
		ID: "news", Title: "unrelated", Source: "test", FetchTime: now,
	}); err != nil {
		t.Fatal(err)
	}

	if err := owner.DB().View(func(tx *bolt.Tx) error {
		if !ds.indexesEnabled(tx) {
			t.Fatal("persistent news write disabled digest indexes")
		}
		current, ok := digestGenerationTx(tx)
		if !ok || current != generation {
			t.Fatalf("digest generation=%d ok=%t want=%d", current, ok, generation)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second, err := ds.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ds.statsValid || ds.statsGeneration != generation || !reflect.DeepEqual(first, second) {
		t.Fatalf("stats cache invalidated by news write: first=%+v second=%+v generation=%d valid=%t",
			first, second, ds.statsGeneration, ds.statsValid)
	}
}

func TestDigestStatsIndexDetectsExternalMutationAndRebuildsVersion(t *testing.T) {
	owner := newDigestTestOwner(t, "stats-index.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	if err := ds.Enqueue(context.Background(), "one", digest.Closing, &model.Message{ID: "one"}); err != nil {
		t.Fatal(err)
	}
	first, err := ds.Stats(context.Background())
	if err != nil || first.Items[digest.Pending] != 1 {
		t.Fatalf("first stats=%+v err=%v", first, err)
	}
	items, err := ds.ListItems(context.Background(), digest.ItemFilter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	items[0].State = digest.Acked
	items[0].TerminalAt = now
	putDigestTestJSON(t, owner.DB(), digestBucketV1, items[0].ID, items[0])
	second, err := ds.Stats(context.Background())
	if err != nil || second.Items[digest.Acked] != 1 || second.Items[digest.Pending] != 0 {
		t.Fatalf("rebuilt stats=%+v err=%v", second, err)
	}

	if err := owner.DB().Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(digestIndexMetaBucket)).Put([]byte("version"), []byte("obsolete"))
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		if got := string(tx.Bucket([]byte(digestIndexMetaBucket)).Get([]byte("version"))); got != digestIndexVersion {
			t.Fatalf("index version=%q", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restarted, err := reopened.Stats(context.Background())
	if err != nil || !reflect.DeepEqual(second, restarted) {
		t.Fatalf("restart parity stats=%+v want=%+v err=%v", restarted, second, err)
	}
}

func TestDigestHotPathIndexesMatchScanFallback(t *testing.T) {
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	type outcome struct {
		leasedIDs []string
		sinks     []string
		expiry    digest.ExpiryResult
		stats     digest.Stats
	}
	run := func(t *testing.T, indexed bool) outcome {
		t.Helper()
		owner := newDigestTestOwner(t, fmt.Sprintf("parity-%t.db", indexed))
		defer owner.Close()
		ds := mustDigestStore(t, owner.DB(), now.Add(-13*time.Hour))
		for _, input := range []struct {
			source   string
			priority int
		}{
			{source: "old-low", priority: -20},
			{source: "old-high", priority: 80},
		} {
			if err := ds.EnqueueWithOptions(context.Background(), input.source, digest.Closing,
				&model.Message{ID: input.source}, input.priority, 48*time.Hour); err != nil {
				t.Fatal(err)
			}
		}
		ds.now = func() time.Time { return now }
		if err := ds.EnqueueWithOptions(context.Background(), "fresh", digest.Closing,
			&model.Message{ID: "fresh"}, 100, 48*time.Hour); err != nil {
			t.Fatal(err)
		}
		if !indexed {
			ds.setIndexesEnabled(false)
		}
		lease, err := ds.Lease(context.Background(), digest.Closing, 2, time.Minute)
		if err != nil || lease == nil {
			t.Fatalf("lease=%+v err=%v", lease, err)
		}
		ids := []string{lease.Items[0].ID, lease.Items[1].ID}
		delivery, err := ds.PrepareDelivery(context.Background(), lease.ID, &model.Message{ID: "payload"},
			[]string{"b", "a"}, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		attempts, err := ds.ClaimDueAttempts(context.Background(), 10, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		sinks := make([]string, len(attempts))
		for i, attempt := range attempts {
			sinks[i] = attempt.Sink
			if err := ds.CompleteAttempt(context.Background(), delivery.ID, attempt.Sink, attempt.Token, nil); err != nil {
				t.Fatal(err)
			}
		}
		deadlineLease, err := ds.Lease(context.Background(), digest.Closing, 1, time.Minute)
		if err != nil || deadlineLease == nil {
			t.Fatalf("deadline lease=%+v err=%v", deadlineLease, err)
		}
		if _, err := ds.PrepareDelivery(context.Background(), deadlineLease.ID, &model.Message{ID: "deadline"},
			[]string{"a"}, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := ds.EnqueueWithOptions(context.Background(), "ttl", digest.USPreview,
			&model.Message{ID: "ttl"}, 0, time.Minute); err != nil {
			t.Fatal(err)
		}
		ds.now = func() time.Time { return now.Add(2 * time.Hour) }
		expiry, err := ds.ExpireDue(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(ids)
		sort.Strings(sinks)
		stats, err := ds.Stats(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if indexed {
			if err := owner.DB().View(func(tx *bolt.Tx) error {
				if !ds.indexesEnabled(tx) {
					t.Fatal("indexes unexpectedly disabled")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		return outcome{leasedIDs: ids, sinks: sinks, expiry: expiry, stats: stats}
	}

	indexed := run(t, true)
	scanned := run(t, false)
	if !reflect.DeepEqual(indexed, scanned) {
		t.Fatalf("indexed outcome=%+v scan outcome=%+v", indexed, scanned)
	}
}

func TestDigestMissingIndexBucketRebuildsOnRestart(t *testing.T) {
	owner := newDigestTestOwner(t, "missing-index.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	if err := ds.Enqueue(context.Background(), "one", digest.PreMarket, &model.Message{ID: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := owner.DB().Update(func(tx *bolt.Tx) error {
		return tx.DeleteBucket([]byte(digestItemStateIndexBucket))
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = func() time.Time { return now }
	lease, err := reopened.Lease(context.Background(), digest.PreMarket, 1, time.Minute)
	if err != nil || lease == nil || len(lease.Items) != 1 {
		t.Fatalf("rebuilt lease=%+v err=%v", lease, err)
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		for _, bucket := range digestIndexBuckets {
			if tx.Bucket([]byte(bucket)) == nil {
				t.Fatalf("missing rebuilt bucket %q", bucket)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDigestAttemptCompletionAcceptsCurrentTokenAfterClaimTTL(t *testing.T) {
	owner := newDigestTestOwner(t, "slow-attempt.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	delivery := prepareDigestTestDelivery(t, ds, "slow", now.Add(time.Hour))
	attempts, err := ds.ClaimDueAttempts(context.Background(), 1, time.Minute)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	ds.now = func() time.Time { return now.Add(2 * time.Minute) }
	if err := ds.CompleteAttempt(context.Background(), delivery.ID, attempts[0].Sink, attempts[0].Token, nil); err != nil {
		t.Fatalf("current token rejected after claim TTL: %v", err)
	}
}

func TestDigestRetryRejectsActiveAndReleasesExpiredCrashClaim(t *testing.T) {
	owner := newDigestTestOwner(t, "retry-claim.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	delivery := prepareDigestTestDelivery(t, ds, "crash", now.Add(time.Hour))
	if _, err := ds.ClaimDueAttempts(context.Background(), 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := ds.RetryDelivery(context.Background(), delivery.ID); !errors.Is(err, digest.ErrDeliveryClaimActive) {
		t.Fatalf("active claim retry err=%v", err)
	}
	ds.now = func() time.Time { return now.Add(2 * time.Minute) }
	if err := ds.RetryDelivery(context.Background(), delivery.ID); err != nil {
		t.Fatalf("expired crash claim was not released: %v", err)
	}
	retry, err := ds.ClaimDueAttempts(context.Background(), 1, time.Minute)
	if err != nil || len(retry) != 1 {
		t.Fatalf("retry attempts=%+v err=%v", retry, err)
	}

	fresh := prepareDigestTestDelivery(t, ds, "fresh-noop", now.Add(time.Hour))
	if err := ds.RetryDelivery(context.Background(), fresh.ID); !errors.Is(err, digest.ErrDeliveryNotRetryable) {
		t.Fatalf("fresh delivery retry err=%v", err)
	}
}

func TestDigestExpirySeparatesItemsFromDeliveries(t *testing.T) {
	owner := newDigestTestOwner(t, "item-expiry.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	if err := ds.EnqueueWithOptions(context.Background(), "item", digest.Closing, &model.Message{ID: "item"}, 0, time.Minute); err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return now.Add(2 * time.Minute) }
	result, err := ds.ExpireDue(context.Background())
	if err != nil || result.Items != 1 || result.Deliveries != 0 ||
		result.DeadlineDeliveries != 0 || result.MaxAttemptDeliveries != 0 {
		t.Fatalf("expiry result=%+v err=%v", result, err)
	}
}

func TestDigestStatsExposeOldestPendingTimestamps(t *testing.T) {
	owner := newDigestTestOwner(t, "pending-stats.db")
	defer owner.Close()
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	if err := ds.Enqueue(context.Background(), "old", digest.Closing, &model.Message{ID: "old"}); err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return now.Add(time.Hour) }
	if err := ds.Enqueue(context.Background(), "new", digest.Closing, &model.Message{ID: "new"}); err != nil {
		t.Fatal(err)
	}
	stats, err := ds.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.OldestPendingItemAt.Equal(now) {
		t.Fatalf("oldest pending item=%v, want %v", stats.OldestPendingItemAt, now)
	}
	if !stats.OldestPendingDeliveryAt.IsZero() {
		t.Fatalf("unexpected pending delivery timestamp: %v", stats.OldestPendingDeliveryAt)
	}
}

func prepareDigestTestDelivery(t *testing.T, ds *DigestStore, id string, deadline time.Time) *digest.Delivery {
	t.Helper()
	if err := ds.Enqueue(context.Background(), id, digest.USPreview, &model.Message{ID: id}); err != nil {
		t.Fatal(err)
	}
	lease, err := ds.Lease(context.Background(), digest.USPreview, 1, time.Minute)
	if err != nil || lease == nil {
		t.Fatalf("lease=%+v err=%v", lease, err)
	}
	delivery, err := ds.PrepareDelivery(context.Background(), lease.ID, &model.Message{ID: "payload-" + id}, []string{"sink"}, deadline)
	if err != nil {
		t.Fatal(err)
	}
	return delivery
}

func newDigestTestOwner(t *testing.T, name string) *BoltStore {
	t.Helper()
	owner, err := NewBoltStore(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func mustDigestStore(t *testing.T, db *bolt.DB, now time.Time) *DigestStore {
	t.Helper()
	ds, err := NewDigestStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return now }
	return ds
}

func putDigestTestJSON(t *testing.T, db *bolt.DB, bucket, key string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket([]byte(bucket)).Put([]byte(key), raw); err != nil {
			return err
		}
		if bucket == digestBucketV1 || bucket == digestDeliveriesBucket {
			return incrementDigestGenerationTx(tx)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertDigestItemState(t *testing.T, db *bolt.DB, id string, state digest.State) {
	t.Helper()
	if err := db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte(digestBucketV1)).Get([]byte(id))
		if raw == nil {
			t.Fatalf("digest item %q not found", id)
		}
		var item digest.Item
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		if item.State != state {
			t.Fatalf("item %q state=%q want=%q: %+v", id, item.State, state, item)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
