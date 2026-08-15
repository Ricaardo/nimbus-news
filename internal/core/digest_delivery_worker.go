package core

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/metrics"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

const (
	digestDeliveryPollInterval = 5 * time.Second
	digestDeliveryAttemptTTL   = 2 * time.Minute
	digestDeliverySendTimeout  = 45 * time.Second
	digestDeliveryBatchSize    = 20
	digestDeliveryConcurrency  = 4
)

type digestDeliveryStore interface {
	PrepareDelivery(context.Context, string, *model.Message, []string, time.Time) (*digest.Delivery, error)
	ClaimDueAttempts(context.Context, int, time.Duration) ([]digest.Attempt, error)
	CompleteAttempt(context.Context, string, string, string, error) error
	ExpireDue(context.Context) (digest.ExpiryResult, error)
	Stats(context.Context) (digest.Stats, error)
	ListDeliveries(context.Context, digest.DeliveryFilter) ([]digest.Delivery, error)
}

type digestActiveSinkStore interface {
	SetActiveSinks([]string)
}

type digestDeliveryWorkerOptions struct {
	pollInterval time.Duration
	attemptTTL   time.Duration
	sendTimeout  time.Duration
	batchSize    int
	concurrency  int
}

// DigestDeliverySnapshot is a low-cardinality operational view for future APIs.
type DigestDeliverySnapshot struct {
	Available            bool                          `json:"available"`
	Stats                digest.Stats                  `json:"stats"`
	LastError            string                        `json:"-"`
	LastSuccess          time.Time                     `json:"last_success,omitempty"`
	LastCycleSuccess     time.Time                     `json:"last_cycle_success,omitempty"`
	OldestPendingSince   time.Time                     `json:"oldest_pending_since,omitempty"`
	Sinks                map[string]DigestSinkSnapshot `json:"sinks,omitempty"`
	ConsecutiveFailures  int                           `json:"consecutive_failures"`
	LastDeadlineExpiry   time.Time                     `json:"last_deadline_expiry,omitempty"`
	LastItemExpiry       time.Time                     `json:"last_item_expiry,omitempty"`
	LastMaxAttemptExpiry time.Time                     `json:"last_max_attempt_expiry,omitempty"`
	HealthStatus         string                        `json:"status,omitempty"`
	HealthReason         string                        `json:"reason,omitempty"`
}

type DigestSinkSnapshot struct {
	Pending             int       `json:"pending"`
	OldestPendingSince  time.Time `json:"oldest_pending_since,omitempty"`
	LastSuccess         time.Time `json:"last_success,omitempty"`
	LastFailure         time.Time `json:"last_failure,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
}

// digestDeliveryWorker owns durable per-sink delivery attempts.
type digestDeliveryWorker struct {
	store     digestDeliveryStore
	channels  *channel.Manager
	options   digestDeliveryWorkerOptions
	wake      chan struct{}
	sendSlots chan struct{}

	runMu   sync.Mutex
	running bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	snapshotMu sync.RWMutex
	snapshot   DigestDeliverySnapshot
}

func newDigestDeliveryWorker(store digestDeliveryStore, channels *channel.Manager, options digestDeliveryWorkerOptions) *digestDeliveryWorker {
	if options.pollInterval <= 0 {
		options.pollInterval = digestDeliveryPollInterval
	}
	if options.attemptTTL <= 0 {
		options.attemptTTL = digestDeliveryAttemptTTL
	}
	if options.batchSize <= 0 {
		options.batchSize = digestDeliveryBatchSize
	}
	if options.sendTimeout <= 0 {
		options.sendTimeout = digestDeliverySendTimeout
	}
	if options.concurrency <= 0 {
		options.concurrency = digestDeliveryConcurrency
	}
	return &digestDeliveryWorker{
		store: store, channels: channels, options: options, wake: make(chan struct{}, 1),
		sendSlots: make(chan struct{}, options.concurrency),
	}
}

func (w *digestDeliveryWorker) Start(parent context.Context) {
	w.runMu.Lock()
	defer w.runMu.Unlock()
	if w.running {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel
	w.running = true
	w.wg.Add(1)
	go w.loop(ctx)
}

func (w *digestDeliveryWorker) Stop() {
	w.runMu.Lock()
	if !w.running {
		w.runMu.Unlock()
		return
	}
	cancel := w.cancel
	w.running = false
	w.runMu.Unlock()
	cancel()
	w.wg.Wait()
}

func (w *digestDeliveryWorker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *digestDeliveryWorker) Snapshot() DigestDeliverySnapshot {
	w.snapshotMu.RLock()
	defer w.snapshotMu.RUnlock()
	snapshot := cloneDigestDeliverySnapshot(w.snapshot)
	snapshot.Available = true
	return snapshot
}

func (w *digestDeliveryWorker) loop(ctx context.Context) {
	defer w.wg.Done()
	ticker := time.NewTicker(w.options.pollInterval)
	defer ticker.Stop()

	w.runCycle(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runCycle(ctx)
		case <-w.wake:
			w.runCycle(ctx)
		}
	}
}

func (w *digestDeliveryWorker) runCycle(ctx context.Context) {
	expired, err := w.store.ExpireDue(ctx)
	if err != nil {
		w.recordError(err)
		return
	}
	if expired.Deliveries > 0 {
		metrics.DigestDeliveriesExpired.Add(float64(expired.Deliveries))
	}
	now := time.Now().UTC()
	if expired.Items > 0 {
		w.snapshotMu.Lock()
		w.snapshot.LastItemExpiry = now
		w.snapshotMu.Unlock()
	}
	if expired.DeadlineDeliveries > 0 {
		w.snapshotMu.Lock()
		w.snapshot.LastDeadlineExpiry = now
		w.snapshotMu.Unlock()
	}
	if expired.MaxAttemptDeliveries > 0 {
		w.snapshotMu.Lock()
		w.snapshot.LastMaxAttemptExpiry = now
		w.snapshotMu.Unlock()
	}

	attemptTTL := w.options.attemptTTL
	waves := (w.options.batchSize + w.options.concurrency - 1) / w.options.concurrency
	minAttemptTTL := time.Duration(waves)*w.options.sendTimeout + 10*time.Second
	if attemptTTL < minAttemptTTL {
		attemptTTL = minAttemptTTL
	}
	attempts, err := w.store.ClaimDueAttempts(ctx, w.options.batchSize, attemptTTL)
	if err != nil {
		w.recordError(err)
		return
	}
	results := make(chan error, len(attempts))
	var attemptsWG sync.WaitGroup
	for _, attempt := range attempts {
		attemptsWG.Add(1)
		go func(attempt digest.Attempt) {
			defer attemptsWG.Done()
			results <- w.deliverAttempt(ctx, attempt)
		}(attempt)
	}
	attemptsWG.Wait()
	close(results)

	successes := 0
	var attemptErrors []error
	for attemptErr := range results {
		if attemptErr == nil {
			successes++
		} else {
			attemptErrors = append(attemptErrors, attemptErr)
		}
	}
	if successes > 0 {
		w.snapshotMu.Lock()
		now := time.Now().UTC()
		w.snapshot.LastError = ""
		w.snapshot.ConsecutiveFailures = 0
		w.snapshot.LastSuccess = now
		w.snapshot.LastCycleSuccess = now
		w.snapshotMu.Unlock()
	}
	for _, attemptErr := range attemptErrors {
		w.recordError(attemptErr)
	}
	w.refreshStats(ctx)
}

func (w *digestDeliveryWorker) deliverAttempt(ctx context.Context, attempt digest.Attempt) error {
	if attempt.Number > 1 {
		metrics.DigestDeliveryRetries.Inc()
	}
	sendErr := w.sendBounded(ctx, attempt)
	result := "success"
	if sendErr != nil {
		result = "failure"
		metrics.DigestDeliveryFailures.Inc()
	}
	metrics.DigestDeliverySinkAttempts.WithLabelValues(metrics.DigestSinkLabel(attempt.Sink), result).Inc()

	// A process crash after SendTo succeeds but before CompleteAttempt commits can
	// cause the same sink to receive the stable message ID again (at-least-once).
	completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := w.store.CompleteAttempt(completeCtx, attempt.DeliveryID, attempt.Sink, attempt.Token, sendErr); err != nil {
		if errors.Is(err, digest.ErrDeliveryDeadline) {
			w.snapshotMu.Lock()
			w.snapshot.LastDeadlineExpiry = time.Now().UTC()
			w.snapshotMu.Unlock()
			metrics.DigestDeliveriesExpired.Inc()
		}
		return err
	}
	if sendErr != nil {
		return sendErr
	}
	return nil
}

func (w *digestDeliveryWorker) sendBounded(ctx context.Context, attempt digest.Attempt) error {
	select {
	case w.sendSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}

	sendCtx, cancel := context.WithTimeout(ctx, w.options.sendTimeout)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		defer func() { <-w.sendSlots }()
		key := "digest:" + attempt.DeliveryID + ":" + attempt.Sink
		result <- w.channels.SendToIdempotent(sendCtx, attempt.Message, attempt.Sink, key)
	}()
	select {
	case err := <-result:
		return err
	case <-sendCtx.Done():
		// A channel that ignores ctx can outlive this attempt, but it retains one
		// bounded slot so repeated polls cannot leak an unbounded number of sends.
		return sendCtx.Err()
	}
}

func (w *digestDeliveryWorker) refreshStats(ctx context.Context) bool {
	if store, ok := w.store.(digestActiveSinkStore); ok {
		channels := w.channels.SendChannels()
		sinks := make([]string, 0, len(channels))
		for _, ch := range channels {
			sinks = append(sinks, ch.Name())
		}
		store.SetActiveSinks(sinks)
	}
	stats, err := w.store.Stats(ctx)
	if err != nil {
		w.recordError(err)
		return false
	}
	for _, state := range []digest.State{digest.Pending, digest.Leased, digest.Delivering, digest.Acked, digest.Expired} {
		metrics.DigestItems.WithLabelValues(string(state)).Set(float64(stats.Items[state]))
	}
	for _, state := range []digest.DeliveryState{digest.DeliveryPending, digest.DeliveryDelivered, digest.DeliveryExpired} {
		metrics.DigestDeliveries.WithLabelValues(string(state)).Set(float64(stats.Deliveries[state]))
	}
	oldestPending := stats.OldestPendingItemAt
	for _, candidate := range []time.Time{
		stats.OldestLeasedItemAt, stats.OldestDeliveringItemAt, stats.OldestPendingDeliveryAt,
	} {
		if oldestPending.IsZero() || (!candidate.IsZero() && candidate.Before(oldestPending)) {
			oldestPending = candidate
		}
	}
	oldestAge := 0.0
	now := time.Now().UTC()
	if !oldestPending.IsZero() {
		oldestAge = now.Sub(oldestPending).Seconds()
		if oldestAge < 0 {
			oldestAge = 0
		}
	}
	metrics.DigestOldestPendingAge.Set(oldestAge)
	sinkMetrics := aggregateDigestSinkMetrics(stats.Sinks)
	for _, label := range []string{
		"feishu-bot", "wechat-main", "discord-webhook", "other",
	} {
		sinkStats := sinkMetrics[label]
		metrics.DigestSinkPending.WithLabelValues(label).Set(float64(sinkStats.Pending))
		age := 0.0
		if !sinkStats.OldestPendingAt.IsZero() {
			age = now.Sub(sinkStats.OldestPendingAt).Seconds()
			if age < 0 {
				age = 0
			}
		}
		metrics.DigestSinkOldestPendingAge.WithLabelValues(label).Set(age)
	}

	w.snapshotMu.Lock()
	w.snapshot.Stats = stats
	w.snapshot.OldestPendingSince = oldestPending
	w.snapshot.Sinks = make(map[string]DigestSinkSnapshot, len(stats.Sinks))
	for sink, sinkStats := range stats.Sinks {
		w.snapshot.Sinks[sink] = DigestSinkSnapshot{
			Pending: sinkStats.Pending, OldestPendingSince: sinkStats.OldestPendingAt,
			LastSuccess: sinkStats.LastDeliveredAt, LastFailure: sinkStats.LastFailedAt,
			ConsecutiveFailures: sinkStats.ConsecutiveFailures,
		}
	}
	w.snapshot.LastItemExpiry = laterTime(w.snapshot.LastItemExpiry, stats.LatestItemExpiryAt)
	w.snapshot.LastDeadlineExpiry = laterTime(w.snapshot.LastDeadlineExpiry, stats.LatestDeadlineExpiryAt)
	w.snapshot.LastMaxAttemptExpiry = laterTime(w.snapshot.LastMaxAttemptExpiry, stats.LatestMaxAttemptExpiryAt)
	w.snapshotMu.Unlock()
	return true
}

func aggregateDigestSinkMetrics(sinks map[string]digest.SinkStats) map[string]digest.SinkStats {
	aggregated := make(map[string]digest.SinkStats)
	for sink, stats := range sinks {
		label := metrics.DigestSinkLabel(sink)
		current := aggregated[label]
		current.Pending += stats.Pending
		current.OldestPendingAt = earlierDigestWorkerTime(current.OldestPendingAt, stats.OldestPendingAt)
		aggregated[label] = current
	}
	return aggregated
}

func earlierDigestWorkerTime(first, second time.Time) time.Time {
	if first.IsZero() || (!second.IsZero() && second.Before(first)) {
		return second
	}
	return first
}

func laterTime(first, second time.Time) time.Time {
	if second.After(first) {
		return second
	}
	return first
}

func (w *digestDeliveryWorker) recordError(err error) {
	slog.Error("digest delivery worker", "error", err)
	w.snapshotMu.Lock()
	w.snapshot.LastError = err.Error()
	w.snapshot.ConsecutiveFailures++
	w.snapshotMu.Unlock()
}

func cloneDigestDeliverySnapshot(snapshot DigestDeliverySnapshot) DigestDeliverySnapshot {
	out := snapshot
	out.Stats.Items = make(map[digest.State]int, len(snapshot.Stats.Items))
	for state, count := range snapshot.Stats.Items {
		out.Stats.Items[state] = count
	}
	out.Stats.Deliveries = make(map[digest.DeliveryState]int, len(snapshot.Stats.Deliveries))
	for state, count := range snapshot.Stats.Deliveries {
		out.Stats.Deliveries[state] = count
	}
	out.Stats.Sinks = make(map[string]digest.SinkStats, len(snapshot.Stats.Sinks))
	for sink, stats := range snapshot.Stats.Sinks {
		out.Stats.Sinks[sink] = stats
	}
	out.Sinks = make(map[string]DigestSinkSnapshot, len(snapshot.Sinks))
	for sink, stats := range snapshot.Sinks {
		out.Sinks[sink] = stats
	}
	return out
}
