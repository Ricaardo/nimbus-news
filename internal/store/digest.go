package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	bolt "go.etcd.io/bbolt"
)

const (
	digestBucketV1         = "digest_items_v1"
	digestDeliveriesBucket = "digest_deliveries_v1"
	digestSinkHealthBucket = "digest_sink_health_v1"
	digestIndexMetaBucket  = "digest_index_meta_v1"
	digestIndexVersion     = "4"
	digestLegacyDefaultTTL = 24 * time.Hour
	digestTTL36Migration   = "default_ttl_36h_migrated"
	// Daily briefings can run a few seconds after the enqueue timestamp on the
	// following day. Keep a full scheduling margin so items cannot expire in
	// the race between the 24-hour TTL and the next daily lease.
	digestDefaultTTL = 36 * time.Hour
	// Five-minute aging yields 432 points over the default TTL, exceeding the
	// full -100..100 priority spread so priority affects order without starvation.
	digestAgeStep        = 5 * time.Minute
	digestMaxAttempts    = 10
	digestMaxErrorLength = 512
)

type DigestStore struct {
	db              *bolt.DB
	sourcePriority  map[string]int
	mu              sync.Mutex
	now             func() time.Time
	activeMu        sync.RWMutex
	activeSinks     map[string]struct{}
	activeSinksSet  bool
	activeRevision  uint64
	statsMu         sync.RWMutex
	stats           digest.Stats
	statsGeneration uint64
	statsActiveRev  uint64
	statsValid      bool
	index           digestIndexState
}

// SetActiveSinks replaces the configured sink set used by Stats. Sinks still
// required by pending deliveries remain active until those deliveries finish.
func (s *DigestStore) SetActiveSinks(sinks []string) {
	active := make(map[string]struct{}, len(sinks))
	for _, sink := range sinks {
		if sink = strings.TrimSpace(sink); sink != "" {
			active[sink] = struct{}{}
		}
	}
	s.activeMu.Lock()
	unchanged := s.activeSinksSet && len(s.activeSinks) == len(active)
	if unchanged {
		for sink := range active {
			if _, ok := s.activeSinks[sink]; !ok {
				unchanged = false
				break
			}
		}
	}
	if unchanged {
		s.activeMu.Unlock()
		return
	}
	s.activeSinks = active
	s.activeSinksSet = true
	s.activeRevision++
	s.activeMu.Unlock()
	s.statsMu.Lock()
	s.statsValid = false
	s.statsMu.Unlock()
}

func (s *DigestStore) activeSinkSnapshot() (map[string]struct{}, bool, uint64) {
	s.activeMu.RLock()
	defer s.activeMu.RUnlock()
	active := make(map[string]struct{}, len(s.activeSinks))
	for sink := range s.activeSinks {
		active[sink] = struct{}{}
	}
	return active, s.activeSinksSet, s.activeRevision
}

func NewDigestStore(db *bolt.DB) (*DigestStore, error) {
	return NewDigestStoreWithPriorities(db, nil)
}

func NewDigestStoreWithPriorities(db *bolt.DB, priorities map[string]int) (*DigestStore, error) {
	if db == nil {
		return nil, fmt.Errorf("digest store: db is required")
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(digestBucketV1)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(digestDeliveriesBucket)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(digestSinkHealthBucket)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(digestRouteAdmissionsBucket)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(digestRouteQuarantineBucket)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(digestRouteQuotaBucket)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(digestMigrationsBucket)); err != nil {
			return err
		}
		meta, err := tx.CreateBucketIfNotExists([]byte(digestIndexMetaBucket))
		if err != nil {
			return err
		}
		if version := meta.Get([]byte("version")); version != nil && string(version) != digestIndexVersion {
			if err := tx.DeleteBucket([]byte(digestIndexMetaBucket)); err != nil {
				return err
			}
			meta, err = tx.CreateBucket([]byte(digestIndexMetaBucket))
			if err != nil {
				return err
			}
		}
		// This bucket only identifies the rebuildable derived index format.
		// Canonical item and delivery JSON remains the sole source of truth.
		if err := meta.Put([]byte("version"), []byte(digestIndexVersion)); err != nil {
			return err
		}
		if _, ok := readDigestGeneration(meta, digestCanonicalGenerationKey); !ok {
			if err := writeDigestGeneration(meta, digestCanonicalGenerationKey, 0); err != nil {
				return err
			}
		}
		if meta.Get([]byte(digestTTL36Migration)) == nil {
			items := tx.Bucket([]byte(digestBucketV1))
			updated := false
			if err := items.ForEach(func(key, value []byte) error {
				var item digest.Item
				if err := json.Unmarshal(value, &item); err != nil {
					return fmt.Errorf("migrate digest item %q TTL: %w", key, err)
				}
				if item.State != digest.Pending && item.State != digest.Leased {
					return nil
				}
				if !item.ExpiresAt.Equal(item.CreatedAt.Add(digestLegacyDefaultTTL)) {
					return nil
				}
				item.ExpiresAt = item.CreatedAt.Add(digestDefaultTTL)
				if err := putJSON(items, key, item); err != nil {
					return err
				}
				updated = true
				return nil
			}); err != nil {
				return err
			}
			if updated {
				if err := incrementDigestGenerationTx(tx); err != nil {
					return err
				}
			}
			if err := meta.Put([]byte(digestTTL36Migration), []byte("1")); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("digest store: create buckets: %w", err)
	}
	priorityCopy := make(map[string]int, len(priorities))
	for source, priority := range priorities {
		priorityCopy[source] = clampDigestPriority(priority)
	}
	store := &DigestStore{db: db, now: time.Now, sourcePriority: priorityCopy}
	_ = store.rebuildDigestIndexes()
	// A malformed canonical record must not make the platform unstartable. In
	// that case Stats keeps the established scan path and returns its error.
	_, _ = store.rebuildStatsIndex(context.Background())
	return store, nil
}

func (s *DigestStore) Enqueue(ctx context.Context, source string, briefing digest.Briefing, msg *model.Message) error {
	return s.EnqueueWithOptions(ctx, source, briefing, msg, s.sourcePriority[source], digestDefaultTTL)
}

func (s *DigestStore) EnqueueWithOptions(ctx context.Context, source string, briefing digest.Briefing, msg *model.Message, priority int, ttl time.Duration) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if source == "" || msg == nil || !briefing.Valid() || priority < -100 || priority > 100 || ttl <= 0 {
		return fmt.Errorf("digest enqueue: invalid source, briefing, message, priority, or ttl")
	}
	messageID := msg.ID
	if messageID == "" {
		sum := sha256.Sum256([]byte(msg.Title + "\x00" + msg.Content + "\x00" + msg.Link))
		messageID = hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256([]byte(source + "\x00" + messageID))
	id := hex.EncodeToString(sum[:])
	now := s.now().UTC()
	message, err := cloneMessage(msg)
	if err != nil {
		return fmt.Errorf("digest enqueue: %w", err)
	}
	item := digest.Item{
		ID: id, SchemaVersion: digest.SchemaVersion, Source: source, Briefing: briefing, Message: message,
		State: digest.Pending, Priority: priority, TopicKey: digest.TopicKey(briefing, message, now),
		CreatedAt: now, ExpiresAt: now.Add(ttl),
	}
	return s.updateDigest(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(digestBucketV1))
		if b.Get([]byte(id)) != nil {
			return nil
		}
		return putDigestItem(tx, []byte(id), item)
	})
}

func (s *DigestStore) Lease(ctx context.Context, briefing digest.Briefing, limit int, ttl time.Duration) (*digest.Lease, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if !briefing.Valid() || limit <= 0 || ttl <= 0 {
		return nil, fmt.Errorf("digest lease: invalid briefing, limit, or ttl")
	}
	now := s.now().UTC()
	leaseID, err := newDigestToken("lease")
	if err != nil {
		return nil, err
	}
	type candidate struct {
		key   []byte
		item  digest.Item
		score int64
	}
	var selected []candidate
	if err := s.updateDigest(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(digestBucketV1))
		var candidates []candidate
		touches := make(map[string]digest.Item)
		seen := make(map[string]struct{})
		process := func(key, value []byte) error {
			if err := contextError(ctx); err != nil {
				return err
			}
			item, changed, err := decodeDigestItem(value, now)
			if err != nil {
				return fmt.Errorf("decode digest item %q: %w", key, err)
			}
			if terminalizeExpiredItem(&item, now) {
				changed = true
			}
			if changed {
				touches[string(key)] = item
			}
			if item.Briefing != briefing || item.State == digest.Delivering || item.State == digest.Acked || item.State == digest.Expired {
				return nil
			}
			if item.State != digest.Pending && !(item.State == digest.Leased && !item.LeaseUntil.After(now)) {
				return nil
			}
			age := now.Sub(item.CreatedAt)
			if age < 0 {
				age = 0
			}
			candidates = append(candidates, candidate{
				key: append([]byte(nil), key...), item: item,
				score: int64(item.Priority) + int64(age/digestAgeStep),
			})
			return nil
		}
		useIndex := s.indexesEnabled(tx)
		var err error
		if useIndex {
			visitID := func(id []byte) error {
				if _, ok := seen[string(id)]; ok {
					return nil
				}
				raw := b.Get(id)
				if raw == nil {
					useIndex = false
					return nil
				}
				seen[string(id)] = struct{}{}
				return process(id, raw)
			}
			err = forIndexPrefix(tx.Bucket([]byte(digestItemStateIndexBucket)),
				itemStatePrefix(briefing, digest.Pending), visitID)
			if err == nil {
				err = forDueIndex(tx.Bucket([]byte(digestItemLeaseDueIndexBucket)), now, visitID)
			}
		}
		if err == nil && !useIndex {
			s.setIndexesEnabled(false)
			candidates = nil
			touches = make(map[string]digest.Item)
			err = b.ForEach(process)
		}
		if err != nil {
			return err
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].score != candidates[j].score {
				return candidates[i].score > candidates[j].score
			}
			if !candidates[i].item.CreatedAt.Equal(candidates[j].item.CreatedAt) {
				return candidates[i].item.CreatedAt.Before(candidates[j].item.CreatedAt)
			}
			return candidates[i].item.ID < candidates[j].item.ID
		})
		if len(candidates) > limit {
			candidates = candidates[:limit]
		}
		for i := range candidates {
			candidates[i].item.State = digest.Leased
			candidates[i].item.LeaseID = leaseID
			candidates[i].item.LeaseUntil = now.Add(ttl)
			touches[string(candidates[i].key)] = candidates[i].item
		}
		for key, item := range touches {
			if err := putDigestItem(tx, []byte(key), item); err != nil {
				return err
			}
		}
		selected = candidates
		return nil
	}); err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, nil
	}
	items := make([]digest.Item, len(selected))
	for i := range selected {
		items[i] = selected[i].item
	}
	return &digest.Lease{ID: leaseID, Briefing: briefing, Items: items}, nil
}

// Ack preserves the v1 all-or-nothing API. It only acknowledges the current
// lease fence; delivery workers should use the durable delivery APIs below.
func (s *DigestStore) Ack(ctx context.Context, leaseID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if leaseID == "" {
		return fmt.Errorf("digest ack: lease id is required")
	}
	now := s.now().UTC()
	return s.updateDigest(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(digestBucketV1))
		updates := make(map[string]digest.Item)
		if err := b.ForEach(func(key, value []byte) error {
			item, changed, err := decodeDigestItem(value, now)
			if err != nil {
				return err
			}
			if terminalizeExpiredItem(&item, now) {
				changed = true
			}
			if item.State != digest.Leased || item.LeaseID != leaseID {
				if changed {
					updates[string(key)] = item
				}
				return nil
			}
			markItemTerminal(&item, digest.Acked, now, "legacy_ack")
			updates[string(key)] = item
			return nil
		}); err != nil {
			return err
		}
		for key, item := range updates {
			if err := putDigestItem(tx, []byte(key), item); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *DigestStore) PrepareDelivery(ctx context.Context, leaseID string, message *model.Message, sinks []string, deadline time.Time) (*digest.Delivery, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	canonicalSinks, err := normalizeSinks(sinks)
	if leaseID == "" || message == nil || err != nil {
		return nil, fmt.Errorf("digest prepare delivery: invalid lease, message, or sinks")
	}
	payload, err := cloneMessage(message)
	if err != nil {
		return nil, fmt.Errorf("digest prepare delivery: %w", err)
	}
	hash, err := messageHash(payload)
	if err != nil {
		return nil, fmt.Errorf("digest prepare delivery: %w", err)
	}
	now := s.now().UTC()
	deadline = deadline.UTC()
	if deadline.IsZero() || !deadline.After(now) {
		return nil, fmt.Errorf("digest prepare delivery: deadline must be in the future")
	}
	deliveryID := leaseID
	var result digest.Delivery
	var prepareErr error
	err = s.updateDigest(func(tx *bolt.Tx) error {
		itemsBucket := tx.Bucket([]byte(digestBucketV1))
		deliveriesBucket := tx.Bucket([]byte(digestDeliveriesBucket))
		if raw := deliveriesBucket.Get([]byte(deliveryID)); raw != nil {
			var existing digest.Delivery
			if err := json.Unmarshal(raw, &existing); err != nil {
				return fmt.Errorf("decode digest delivery %q: %w", deliveryID, err)
			}
			if existing.LeaseID != leaseID || existing.PayloadHash != hash || !equalStrings(existing.RequiredSinks, canonicalSinks) {
				return fmt.Errorf("digest prepare delivery: idempotency mismatch")
			}
			result = existing
			return nil
		}
		var items []digest.Item
		var keys [][]byte
		touches := make(map[string]digest.Item)
		invalidLease := false
		process := func(key, value []byte) error {
			item, changed, err := decodeDigestItem(value, now)
			if err != nil {
				return err
			}
			matchesLease := item.State == digest.Leased && item.LeaseID == leaseID
			if terminalizeExpiredItem(&item, now) {
				changed = true
			}
			if changed {
				touches[string(key)] = item
			}
			if matchesLease && (item.State != digest.Leased || !item.LeaseUntil.After(now)) {
				invalidLease = true
			}
			if matchesLease && item.State == digest.Leased && item.LeaseUntil.After(now) {
				items = append(items, item)
				keys = append(keys, append([]byte(nil), key...))
			}
			return nil
		}
		useIndex := s.indexesEnabled(tx)
		var err error
		if useIndex {
			prefix := append([]byte(leaseID), 0)
			err = forIndexPrefix(tx.Bucket([]byte(digestItemLeaseIndexBucket)), prefix, func(id []byte) error {
				raw := itemsBucket.Get(id)
				if raw == nil {
					useIndex = false
					return nil
				}
				return process(id, raw)
			})
		}
		if err == nil && !useIndex {
			s.setIndexesEnabled(false)
			items = nil
			keys = nil
			touches = make(map[string]digest.Item)
			invalidLease = false
			err = itemsBucket.ForEach(process)
		}
		if err != nil {
			return err
		}
		for key, item := range touches {
			if err := putDigestItem(tx, []byte(key), item); err != nil {
				return err
			}
		}
		if invalidLease {
			prepareErr = fmt.Errorf("digest prepare delivery: lease is stale or contains expired items")
			return nil
		}
		if len(items) == 0 {
			return fmt.Errorf("digest prepare delivery: lease is missing, stale, or empty")
		}
		checkpoints := make(map[string]digest.SinkCheckpoint, len(canonicalSinks))
		for _, sink := range canonicalSinks {
			checkpoints[sink] = digest.SinkCheckpoint{Sink: sink, NextAttemptAt: now}
		}
		itemIDs := make([]string, len(items))
		for i := range items {
			itemIDs[i] = items[i].ID
		}
		sort.Strings(itemIDs)
		result = digest.Delivery{
			ID: deliveryID, SchemaVersion: digest.SchemaVersion, LeaseID: leaseID,
			Briefing: items[0].Briefing, ItemIDs: itemIDs, Message: payload, PayloadHash: hash,
			RequiredSinks: canonicalSinks, Sinks: checkpoints, State: digest.DeliveryPending,
			CreatedAt: now, UpdatedAt: now, Deadline: deadline,
		}
		if err := putDigestDelivery(tx, []byte(deliveryID), result); err != nil {
			return err
		}
		for i := range items {
			items[i].State = digest.Delivering
			items[i].DeliveryID = deliveryID
			items[i].LeaseUntil = time.Time{}
			if err := putDigestItem(tx, keys[i], items[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if prepareErr != nil {
		return nil, prepareErr
	}
	return cloneDelivery(&result)
}

func (s *DigestStore) ClaimDueAttempts(ctx context.Context, limit int, ttl time.Duration) ([]digest.Attempt, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || ttl <= 0 {
		return nil, fmt.Errorf("digest claim attempts: invalid limit or ttl")
	}
	now := s.now().UTC()
	var attempts []digest.Attempt
	err := s.updateDigest(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(digestDeliveriesBucket))
		type due struct {
			key      []byte
			delivery digest.Delivery
			sink     string
		}
		var candidates []due
		var expired []due
		expiredIDs := make(map[string]struct{})
		candidateIDs := make(map[string]struct{})
		staleIndex := false
		process := func(key []byte, sinkHint string) error {
			value := b.Get(key)
			if value == nil {
				staleIndex = true
				return nil
			}
			var delivery digest.Delivery
			if err := json.Unmarshal(value, &delivery); err != nil {
				return fmt.Errorf("decode digest delivery %q: %w", key, err)
			}
			if delivery.State != digest.DeliveryPending {
				return nil
			}
			if !delivery.Deadline.After(now) {
				if _, ok := expiredIDs[delivery.ID]; !ok {
					expiredIDs[delivery.ID] = struct{}{}
					expired = append(expired, due{key: append([]byte(nil), key...), delivery: delivery, sink: "deadline"})
				}
				return nil
			}
			sinks := delivery.RequiredSinks
			if sinkHint != "" {
				sinks = []string{sinkHint}
			}
			for _, sink := range sinks {
				checkpoint := delivery.Sinks[sink]
				if !checkpoint.Delivered && checkpoint.Attempts >= digestMaxAttempts && !checkpoint.ClaimedUntil.After(now) {
					if _, ok := expiredIDs[delivery.ID]; !ok {
						expiredIDs[delivery.ID] = struct{}{}
						expired = append(expired, due{key: append([]byte(nil), key...), delivery: delivery, sink: "max_attempts"})
					}
					return nil
				}
			}
			for _, sink := range sinks {
				checkpoint, ok := delivery.Sinks[sink]
				if !ok {
					return fmt.Errorf("digest delivery %q: missing sink checkpoint %q", delivery.ID, sink)
				}
				if checkpoint.Delivered || checkpoint.NextAttemptAt.After(now) || checkpoint.ClaimedUntil.After(now) {
					continue
				}
				candidateID := delivery.ID + "\x00" + sink
				if _, ok := candidateIDs[candidateID]; !ok {
					candidateIDs[candidateID] = struct{}{}
					candidates = append(candidates, due{append([]byte(nil), key...), delivery, sink})
				}
			}
			return nil
		}
		useIndex := s.indexesEnabled(tx)
		var err error
		if useIndex {
			err = forDueIndex(tx.Bucket([]byte(digestDeliveryDeadlineIndexBucket)), now, func(id []byte) error {
				return process(id, "")
			})
			if err == nil {
				err = forDueIndex(tx.Bucket([]byte(digestDeliverySinkDueIndexBucket)), now, func(value []byte) error {
					id, sink, ok := splitPair(value)
					if !ok {
						staleIndex = true
						return nil
					}
					return process([]byte(id), sink)
				})
			}
		}
		if staleIndex {
			useIndex = false
		}
		if err == nil && !useIndex {
			s.setIndexesEnabled(false)
			candidates = nil
			expired = nil
			expiredIDs = make(map[string]struct{})
			candidateIDs = make(map[string]struct{})
			err = b.ForEach(func(key, _ []byte) error { return process(key, "") })
		}
		if err != nil {
			return err
		}
		for i := range expired {
			if err := expireDelivery(tx, expired[i].key, &expired[i].delivery, now, expired[i].sink); err != nil {
				return err
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			left := candidates[i].delivery.Sinks[candidates[i].sink].NextAttemptAt
			right := candidates[j].delivery.Sinks[candidates[j].sink].NextAttemptAt
			if !left.Equal(right) {
				return left.Before(right)
			}
			if candidates[i].delivery.CreatedAt != candidates[j].delivery.CreatedAt {
				return candidates[i].delivery.CreatedAt.Before(candidates[j].delivery.CreatedAt)
			}
			if candidates[i].delivery.ID != candidates[j].delivery.ID {
				return candidates[i].delivery.ID < candidates[j].delivery.ID
			}
			return candidates[i].sink < candidates[j].sink
		})
		claimed := make(map[string]digest.Delivery)
		for _, candidate := range candidates {
			if len(attempts) >= limit {
				break
			}
			delivery, ok := claimed[candidate.delivery.ID]
			if !ok {
				delivery = candidate.delivery
			}
			checkpoint := delivery.Sinks[candidate.sink]
			if checkpoint.Delivered || checkpoint.ClaimedUntil.After(now) {
				continue
			}
			token, err := newDigestToken("attempt")
			if err != nil {
				return err
			}
			checkpoint.Attempts++
			checkpoint.AttemptToken = token
			checkpoint.ClaimedUntil = now.Add(ttl)
			delivery.Sinks[candidate.sink] = checkpoint
			delivery.UpdatedAt = now
			claimed[delivery.ID] = delivery
			message, err := cloneMessage(delivery.Message)
			if err != nil {
				return err
			}
			attempts = append(attempts, digest.Attempt{
				DeliveryID: delivery.ID, Sink: candidate.sink, Token: token,
				Number: checkpoint.Attempts, ClaimedUntil: checkpoint.ClaimedUntil, Message: message,
			})
		}
		for id, delivery := range claimed {
			if err := putDigestDelivery(tx, []byte(id), delivery); err != nil {
				return err
			}
		}
		return nil
	})
	return attempts, err
}

func (s *DigestStore) CompleteAttempt(ctx context.Context, deliveryID, sink, attemptToken string, deliveryErr error) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if deliveryID == "" || sink == "" || attemptToken == "" {
		return fmt.Errorf("digest complete attempt: delivery, sink, and token are required")
	}
	now := s.now().UTC()
	var completionErr error
	err := s.updateDigest(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(digestDeliveriesBucket))
		raw := b.Get([]byte(deliveryID))
		if raw == nil {
			return fmt.Errorf("digest complete attempt: delivery not found")
		}
		var delivery digest.Delivery
		if err := json.Unmarshal(raw, &delivery); err != nil {
			return err
		}
		if delivery.State != digest.DeliveryPending {
			return fmt.Errorf("digest complete attempt: delivery is terminal")
		}
		checkpoint, ok := delivery.Sinks[sink]
		if !ok || checkpoint.AttemptToken != attemptToken {
			return fmt.Errorf("digest complete attempt: stale attempt token")
		}
		if deliveryErr != nil && !delivery.Deadline.After(now) {
			completionErr = fmt.Errorf("digest complete attempt: %w", digest.ErrDeliveryDeadline)
			return expireDelivery(tx, []byte(deliveryID), &delivery, now, "deadline")
		}
		checkpoint.AttemptToken = ""
		checkpoint.ClaimedUntil = time.Time{}
		checkpoint.Outcomes = append(checkpoint.Outcomes, digest.SinkAttemptOutcome{
			At: now, Order: uint64(tx.ID()), Success: deliveryErr == nil,
		})
		sinkHealth, err := sinkHealthTx(tx, sink)
		if err != nil {
			return err
		}
		if deliveryErr == nil {
			sinkHealth.LastDeliveredAt = now
			sinkHealth.ConsecutiveFailures = 0
		} else {
			sinkHealth.LastFailedAt = now
			sinkHealth.ConsecutiveFailures++
		}
		if err := putJSON(tx.Bucket([]byte(digestSinkHealthBucket)), []byte(sink), sinkHealth); err != nil {
			return err
		}
		if deliveryErr == nil {
			checkpoint.Delivered = true
			checkpoint.DeliveredAt = now
			checkpoint.LastFailedAt = time.Time{}
			checkpoint.LastError = ""
			checkpoint.NextAttemptAt = time.Time{}
		} else {
			checkpoint.FailedAttempts++
			checkpoint.LastError = boundedError(deliveryErr)
			checkpoint.LastFailedAt = now
			if checkpoint.Attempts >= digestMaxAttempts || !delivery.Deadline.After(now) {
				delivery.Sinks[sink] = checkpoint
				return expireDelivery(tx, []byte(deliveryID), &delivery, now, failureReason(checkpoint))
			}
			checkpoint.NextAttemptAt = now.Add(digestBackoff(delivery.ID, sink, checkpoint.Attempts))
		}
		delivery.Sinks[sink] = checkpoint
		delivery.UpdatedAt = now
		if allSinksDelivered(delivery) {
			delivery.State = digest.DeliveryDelivered
			delivery.TerminalAt = now
			delivery.TerminalReason = "delivered"
			if err := updateDeliveryItems(tx, delivery, func(item *digest.Item) {
				markItemTerminal(item, digest.Acked, now, "delivered")
			}); err != nil {
				return err
			}
		}
		return putDigestDelivery(tx, []byte(deliveryID), delivery)
	})
	if err != nil {
		return err
	}
	return completionErr
}

func (s *DigestStore) RetryDelivery(ctx context.Context, deliveryID string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if deliveryID == "" {
		return fmt.Errorf("digest retry delivery: delivery id is required")
	}
	now := s.now().UTC()
	err := s.updateDigest(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(digestDeliveriesBucket))
		raw := b.Get([]byte(deliveryID))
		if raw == nil {
			return fmt.Errorf("digest retry delivery: %w", digest.ErrDeliveryNotFound)
		}
		var delivery digest.Delivery
		if err := json.Unmarshal(raw, &delivery); err != nil {
			return err
		}
		if delivery.State != digest.DeliveryPending {
			return fmt.Errorf("digest retry delivery: %w", digest.ErrDeliveryTerminal)
		}
		retryable := false
		active := false
		for sink, checkpoint := range delivery.Sinks {
			if checkpoint.Delivered {
				continue
			}
			if checkpoint.AttemptToken != "" && checkpoint.ClaimedUntil.After(now) {
				active = true
				continue
			}
			if checkpoint.AttemptToken == "" && checkpoint.LastError == "" {
				continue
			}
			checkpoint.AttemptToken = ""
			checkpoint.ClaimedUntil = time.Time{}
			checkpoint.NextAttemptAt = now
			delivery.Sinks[sink] = checkpoint
			retryable = true
		}
		if !retryable {
			if active {
				return fmt.Errorf("digest retry delivery: %w", digest.ErrDeliveryClaimActive)
			}
			return fmt.Errorf("digest retry delivery: %w", digest.ErrDeliveryNotRetryable)
		}
		delivery.UpdatedAt = now
		return putDigestDelivery(tx, []byte(deliveryID), delivery)
	})
	return err
}

func (s *DigestStore) ExpireDue(ctx context.Context) (digest.ExpiryResult, error) {
	if err := contextError(ctx); err != nil {
		return digest.ExpiryResult{}, err
	}
	now := s.now().UTC()
	var result digest.ExpiryResult
	err := s.updateDigest(func(tx *bolt.Tx) error {
		itemsBucket := tx.Bucket([]byte(digestBucketV1))
		deliveriesBucket := tx.Bucket([]byte(digestDeliveriesBucket))
		itemUpdates := make(map[string]digest.Item)
		processItem := func(key, value []byte) error {
			item, changed, err := decodeDigestItem(value, now)
			if err != nil {
				return err
			}
			if terminalizeExpiredItem(&item, now) {
				changed = true
				result.Items++
			}
			if changed {
				itemUpdates[string(key)] = item
			}
			return nil
		}
		useIndex := s.indexesEnabled(tx)
		staleIndex := false
		var err error
		if useIndex {
			err = forDueIndex(tx.Bucket([]byte(digestItemExpiryIndexBucket)), now, func(id []byte) error {
				raw := itemsBucket.Get(id)
				if raw == nil {
					staleIndex = true
					return nil
				}
				return processItem(id, raw)
			})
		}
		if staleIndex {
			useIndex = false
		}
		if err == nil && !useIndex {
			s.setIndexesEnabled(false)
			itemUpdates = make(map[string]digest.Item)
			result.Items = 0
			err = itemsBucket.ForEach(processItem)
		}
		if err != nil {
			return err
		}
		for key, item := range itemUpdates {
			if err := putDigestItem(tx, []byte(key), item); err != nil {
				return err
			}
		}
		type expiry struct {
			key      []byte
			delivery digest.Delivery
			reason   string
		}
		var expiries []expiry
		expiryIDs := make(map[string]struct{})
		processDelivery := func(key []byte, sinkHint string) error {
			value := deliveriesBucket.Get(key)
			if value == nil {
				staleIndex = true
				return nil
			}
			var delivery digest.Delivery
			if err := json.Unmarshal(value, &delivery); err != nil {
				return err
			}
			if delivery.State == digest.DeliveryPending && !delivery.Deadline.After(now) {
				if _, ok := expiryIDs[delivery.ID]; !ok {
					expiryIDs[delivery.ID] = struct{}{}
					result.Deliveries++
					result.DeadlineDeliveries++
					expiries = append(expiries, expiry{append([]byte(nil), key...), delivery, "deadline"})
				}
				return nil
			}
			if delivery.State == digest.DeliveryPending {
				sinks := delivery.RequiredSinks
				if sinkHint != "" {
					sinks = []string{sinkHint}
				}
				for _, sink := range sinks {
					checkpoint, ok := delivery.Sinks[sink]
					if !ok {
						return fmt.Errorf("digest delivery %q: missing sink checkpoint %q", delivery.ID, sink)
					}
					if !checkpoint.Delivered && checkpoint.Attempts >= digestMaxAttempts && !checkpoint.ClaimedUntil.After(now) {
						if _, ok := expiryIDs[delivery.ID]; !ok {
							expiryIDs[delivery.ID] = struct{}{}
							result.Deliveries++
							result.MaxAttemptDeliveries++
							expiries = append(expiries, expiry{append([]byte(nil), key...), delivery, "max_attempts"})
						}
						return nil
					}
				}
			}
			return nil
		}
		if useIndex {
			err = forDueIndex(tx.Bucket([]byte(digestDeliveryDeadlineIndexBucket)), now, func(id []byte) error {
				return processDelivery(id, "")
			})
			if err == nil {
				err = forDueIndex(tx.Bucket([]byte(digestDeliverySinkDueIndexBucket)), now, func(value []byte) error {
					id, sink, ok := splitPair(value)
					if !ok {
						staleIndex = true
						return nil
					}
					return processDelivery([]byte(id), sink)
				})
			}
		}
		if staleIndex {
			useIndex = false
		}
		if err == nil && !useIndex {
			s.setIndexesEnabled(false)
			expiries = nil
			expiryIDs = make(map[string]struct{})
			result.Deliveries = 0
			result.DeadlineDeliveries = 0
			result.MaxAttemptDeliveries = 0
			err = deliveriesBucket.ForEach(func(key, _ []byte) error { return processDelivery(key, "") })
		}
		if err != nil {
			return err
		}
		for i := range expiries {
			if err := expireDelivery(tx, expiries[i].key, &expiries[i].delivery, now, expiries[i].reason); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

func (s *DigestStore) ListItems(ctx context.Context, filter digest.ItemFilter) ([]digest.Item, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	var items []digest.Item
	err := s.updateDigest(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(digestBucketV1))
		updates := make(map[string]digest.Item)
		if err := b.ForEach(func(key, value []byte) error {
			item, changed, err := decodeDigestItem(value, now)
			if err != nil {
				return fmt.Errorf("decode digest item %q: %w", key, err)
			}
			if changed {
				updates[string(key)] = item
			}
			if (filter.Briefing == "" || item.Briefing == filter.Briefing) &&
				(filter.State == "" || item.State == filter.State) {
				items = append(items, item)
			}
			return nil
		}); err != nil {
			return err
		}
		for key, item := range updates {
			if err := putDigestItem(tx, []byte(key), item); err != nil {
				return err
			}
		}
		return nil
	})
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, err
}

func (s *DigestStore) ListDeliveries(ctx context.Context, filter digest.DeliveryFilter) ([]digest.Delivery, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	var deliveries []digest.Delivery
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(digestDeliveriesBucket)).ForEach(func(key, value []byte) error {
			var delivery digest.Delivery
			if err := json.Unmarshal(value, &delivery); err != nil {
				return fmt.Errorf("decode digest delivery %q: %w", key, err)
			}
			if filter.State != "" && delivery.State != filter.State {
				return nil
			}
			if filter.Sink != "" {
				if _, ok := delivery.Sinks[filter.Sink]; !ok {
					return nil
				}
			}
			deliveries = append(deliveries, delivery)
			return nil
		})
	})
	sort.Slice(deliveries, func(i, j int) bool {
		if deliveries[i].CreatedAt.Equal(deliveries[j].CreatedAt) {
			return deliveries[i].ID < deliveries[j].ID
		}
		return deliveries[i].CreatedAt.After(deliveries[j].CreatedAt)
	})
	if filter.Limit > 0 && len(deliveries) > filter.Limit {
		deliveries = deliveries[:filter.Limit]
	}
	return deliveries, err
}

func (s *DigestStore) Stats(ctx context.Context) (digest.Stats, error) {
	var generation uint64
	var generationValid bool
	if err := s.db.View(func(tx *bolt.Tx) error {
		generation, generationValid = digestGenerationTx(tx)
		return nil
	}); err != nil {
		return digest.Stats{}, err
	}
	_, _, activeRevision := s.activeSinkSnapshot()
	s.statsMu.RLock()
	if generationValid && s.statsValid && s.statsGeneration == generation && s.statsActiveRev == activeRevision {
		stats := cloneDigestStats(s.stats)
		s.statsMu.RUnlock()
		return stats, nil
	}
	s.statsMu.RUnlock()
	return s.rebuildStatsIndex(ctx)
}

func (s *DigestStore) rebuildStatsIndex(ctx context.Context) (digest.Stats, error) {
	activeSinks, activeSinksSet, activeRevision := s.activeSinkSnapshot()
	var indexedStats digest.Stats
	var indexedGeneration uint64
	var indexedGenerationValid bool
	var indexed bool
	indexedErr := s.db.View(func(tx *bolt.Tx) error {
		if !s.indexesEnabled(tx) {
			return nil
		}
		indexed = true
		indexedGeneration, indexedGenerationValid = digestGenerationTx(tx)
		var err error
		indexedStats, err = digestStatsFromIndexesTx(ctx, tx, activeSinks, activeSinksSet)
		return err
	})
	if indexed && indexedErr == nil {
		s.statsMu.Lock()
		s.stats = cloneDigestStats(indexedStats)
		s.statsGeneration = indexedGeneration
		s.statsActiveRev = activeRevision
		s.statsValid = indexedGenerationValid
		s.statsMu.Unlock()
		return indexedStats, nil
	}
	if indexedErr != nil {
		// Derived indexes are rebuildable. Preserve the canonical scan fallback
		// for external mutation and older/corrupt index layouts.
		s.setIndexesEnabled(false)
	}

	stats := digest.Stats{
		Items: make(map[digest.State]int), Deliveries: make(map[digest.DeliveryState]int),
		Sinks: make(map[string]digest.SinkStats),
	}
	type sinkOutcome struct {
		at         time.Time
		order      uint64
		deliveryID string
		index      int
		success    bool
		failures   int
	}
	outcomes := make(map[string][]sinkOutcome)
	canonicalHealth := make(map[string]digest.SinkStats)
	pendingSinks := make(map[string]struct{})
	var generation uint64
	var generationValid bool
	err := s.db.View(func(tx *bolt.Tx) error {
		generation, generationValid = digestGenerationTx(tx)
		itemsBucket := tx.Bucket([]byte(digestBucketV1))
		if err := itemsBucket.ForEach(func(key, value []byte) error {
			if err := contextError(ctx); err != nil {
				return err
			}
			item, _, err := decodeDigestItem(value, s.now().UTC())
			if err != nil {
				return fmt.Errorf("decode digest item %q: %w", key, err)
			}
			stats.Items[item.State]++
			switch item.State {
			case digest.Pending:
				stats.OldestPendingItemAt = earlierTime(stats.OldestPendingItemAt, item.CreatedAt)
			case digest.Leased:
				stats.OldestLeasedItemAt = earlierTime(stats.OldestLeasedItemAt, item.CreatedAt)
			case digest.Delivering:
				stats.OldestDeliveringItemAt = earlierTime(stats.OldestDeliveringItemAt, item.CreatedAt)
			}
			if item.State == digest.Expired && item.TerminalReason == "ttl" {
				stats.LatestItemExpiryAt = laterDigestTime(stats.LatestItemExpiryAt, item.TerminalAt)
			}
			return nil
		}); err != nil {
			return err
		}
		if err := tx.Bucket([]byte(digestDeliveriesBucket)).ForEach(func(key, value []byte) error {
			if err := contextError(ctx); err != nil {
				return err
			}
			var delivery digest.Delivery
			if err := json.Unmarshal(value, &delivery); err != nil {
				return fmt.Errorf("decode digest delivery %q: %w", key, err)
			}
			stats.Deliveries[delivery.State]++
			if delivery.State == digest.DeliveryPending {
				stats.OldestPendingDeliveryAt = earlierTime(stats.OldestPendingDeliveryAt, delivery.CreatedAt)
			}
			if delivery.State == digest.DeliveryExpired {
				switch delivery.TerminalReason {
				case "deadline":
					stats.LatestDeadlineExpiryAt = laterDigestTime(stats.LatestDeadlineExpiryAt, delivery.TerminalAt)
				case "max_attempts":
					stats.LatestMaxAttemptExpiryAt = laterDigestTime(stats.LatestMaxAttemptExpiryAt, delivery.TerminalAt)
				}
			}
			for _, sink := range delivery.RequiredSinks {
				checkpoint := delivery.Sinks[sink]
				sinkStats := stats.Sinks[sink]
				if delivery.State == digest.DeliveryPending && !checkpoint.Delivered {
					sinkStats.Pending++
					sinkStats.OldestPendingAt = earlierTime(sinkStats.OldestPendingAt, delivery.CreatedAt)
					pendingSinks[sink] = struct{}{}
				}
				sinkStats.LastDeliveredAt = laterDigestTime(sinkStats.LastDeliveredAt, checkpoint.DeliveredAt)
				failedAt := checkpoint.LastFailedAt
				if failedAt.IsZero() && checkpoint.LastError != "" {
					failedAt = delivery.UpdatedAt
				}
				sinkStats.LastFailedAt = laterDigestTime(sinkStats.LastFailedAt, failedAt)
				if len(checkpoint.Outcomes) > 0 {
					for index, outcome := range checkpoint.Outcomes {
						outcomes[sink] = append(outcomes[sink], sinkOutcome{
							at: outcome.At, order: outcome.Order, deliveryID: delivery.ID,
							index: index, success: outcome.Success, failures: 1,
						})
						if outcome.Success {
							sinkStats.LastDeliveredAt = laterDigestTime(sinkStats.LastDeliveredAt, outcome.At)
						} else {
							sinkStats.LastFailedAt = laterDigestTime(sinkStats.LastFailedAt, outcome.At)
						}
					}
				} else {
					if !checkpoint.DeliveredAt.IsZero() {
						outcomes[sink] = append(outcomes[sink], sinkOutcome{
							at: checkpoint.DeliveredAt, deliveryID: delivery.ID, success: true,
						})
					}
					if !failedAt.IsZero() {
						failures := checkpoint.FailedAttempts
						if failures == 0 {
							// Backward compatibility for checkpoints written before
							// failed_attempts was persisted separately from claims.
							failures = checkpoint.Attempts
						}
						outcomes[sink] = append(outcomes[sink], sinkOutcome{
							at: failedAt, deliveryID: delivery.ID, failures: failures,
						})
					}
				}
				stats.Sinks[sink] = sinkStats
			}
			return nil
		}); err != nil {
			return err
		}
		return tx.Bucket([]byte(digestSinkHealthBucket)).ForEach(func(key, value []byte) error {
			var health digest.SinkStats
			if err := json.Unmarshal(value, &health); err != nil {
				return fmt.Errorf("decode digest sink health %q: %w", key, err)
			}
			canonicalHealth[string(key)] = health
			return nil
		})
	})
	if err != nil {
		return digest.Stats{}, err
	}
	for sink, events := range outcomes {
		sort.Slice(events, func(i, j int) bool {
			if !events[i].at.Equal(events[j].at) {
				return events[i].at.Before(events[j].at)
			}
			if events[i].order != events[j].order {
				return events[i].order < events[j].order
			}
			if events[i].deliveryID != events[j].deliveryID {
				return events[i].deliveryID < events[j].deliveryID
			}
			return events[i].index < events[j].index
		})
		sinkStats := stats.Sinks[sink]
		for _, event := range events {
			if event.success {
				sinkStats.ConsecutiveFailures = 0
			} else {
				sinkStats.ConsecutiveFailures += event.failures
			}
		}
		stats.Sinks[sink] = sinkStats
	}
	for sink, health := range canonicalHealth {
		if activeSinksSet {
			if _, active := activeSinks[sink]; !active {
				if _, pending := pendingSinks[sink]; !pending {
					continue
				}
			}
		}
		sinkStats := stats.Sinks[sink]
		sinkStats.LastDeliveredAt = health.LastDeliveredAt
		sinkStats.LastFailedAt = health.LastFailedAt
		sinkStats.ConsecutiveFailures = health.ConsecutiveFailures
		stats.Sinks[sink] = sinkStats
	}
	if activeSinksSet {
		for sink := range stats.Sinks {
			if _, active := activeSinks[sink]; active {
				continue
			}
			if _, pending := pendingSinks[sink]; !pending {
				delete(stats.Sinks, sink)
			}
		}
	}
	s.statsMu.Lock()
	s.stats = cloneDigestStats(stats)
	s.statsGeneration = generation
	s.statsActiveRev = activeRevision
	s.statsValid = generationValid
	s.statsMu.Unlock()
	return stats, nil
}

func sinkHealthTx(tx *bolt.Tx, sink string) (digest.SinkStats, error) {
	bucket := tx.Bucket([]byte(digestSinkHealthBucket))
	if raw := bucket.Get([]byte(sink)); raw != nil {
		var health digest.SinkStats
		if err := json.Unmarshal(raw, &health); err != nil {
			return digest.SinkStats{}, fmt.Errorf("decode digest sink health %q: %w", sink, err)
		}
		return health, nil
	}

	type outcome struct {
		at         time.Time
		order      uint64
		deliveryID string
		index      int
		success    bool
		failures   int
	}
	var outcomes []outcome
	var health digest.SinkStats
	err := tx.Bucket([]byte(digestDeliveriesBucket)).ForEach(func(_, value []byte) error {
		var delivery digest.Delivery
		if err := json.Unmarshal(value, &delivery); err != nil {
			return err
		}
		checkpoint, ok := delivery.Sinks[sink]
		if !ok {
			return nil
		}
		health.LastDeliveredAt = laterDigestTime(health.LastDeliveredAt, checkpoint.DeliveredAt)
		health.LastFailedAt = laterDigestTime(health.LastFailedAt, checkpoint.LastFailedAt)
		if len(checkpoint.Outcomes) > 0 {
			for index, event := range checkpoint.Outcomes {
				outcomes = append(outcomes, outcome{
					at: event.At, order: event.Order, deliveryID: delivery.ID,
					index: index, success: event.Success, failures: 1,
				})
			}
			return nil
		}
		if !checkpoint.DeliveredAt.IsZero() {
			outcomes = append(outcomes, outcome{
				at: checkpoint.DeliveredAt, deliveryID: delivery.ID, success: true,
			})
		}
		if !checkpoint.LastFailedAt.IsZero() {
			failures := checkpoint.FailedAttempts
			if failures == 0 {
				failures = checkpoint.Attempts
			}
			outcomes = append(outcomes, outcome{
				at: checkpoint.LastFailedAt, deliveryID: delivery.ID, failures: failures,
			})
		}
		return nil
	})
	if err != nil {
		return digest.SinkStats{}, fmt.Errorf("rebuild digest sink health %q: %w", sink, err)
	}
	sort.Slice(outcomes, func(i, j int) bool {
		if !outcomes[i].at.Equal(outcomes[j].at) {
			return outcomes[i].at.Before(outcomes[j].at)
		}
		if outcomes[i].order != outcomes[j].order {
			return outcomes[i].order < outcomes[j].order
		}
		if outcomes[i].deliveryID != outcomes[j].deliveryID {
			return outcomes[i].deliveryID < outcomes[j].deliveryID
		}
		return outcomes[i].index < outcomes[j].index
	})
	for _, event := range outcomes {
		if event.success {
			health.ConsecutiveFailures = 0
		} else {
			health.ConsecutiveFailures += event.failures
		}
	}
	return health, nil
}

func cloneDigestStats(stats digest.Stats) digest.Stats {
	out := stats
	out.Items = make(map[digest.State]int, len(stats.Items))
	for state, count := range stats.Items {
		out.Items[state] = count
	}
	out.Deliveries = make(map[digest.DeliveryState]int, len(stats.Deliveries))
	for state, count := range stats.Deliveries {
		out.Deliveries[state] = count
	}
	out.Sinks = make(map[string]digest.SinkStats, len(stats.Sinks))
	for sink, sinkStats := range stats.Sinks {
		out.Sinks[sink] = sinkStats
	}
	return out
}

func earlierTime(first, second time.Time) time.Time {
	if first.IsZero() || (!second.IsZero() && second.Before(first)) {
		return second
	}
	return first
}

func laterDigestTime(first, second time.Time) time.Time {
	if second.After(first) {
		return second
	}
	return first
}

// Purge removes only terminal items and deliveries older than before. maxAcked
// remains for backward compatibility and bounds retained terminal item records.
func (s *DigestStore) Purge(ctx context.Context, before time.Time, maxAcked int) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	healthChanged := false
	err := s.updateDigest(func(tx *bolt.Tx) error {
		itemsBucket := tx.Bucket([]byte(digestBucketV1))
		deliveriesBucket := tx.Bucket([]byte(digestDeliveriesBucket))
		var terminal []digest.Item
		if err := itemsBucket.ForEach(func(_, value []byte) error {
			item, _, err := decodeDigestItem(value, s.now().UTC())
			if err != nil {
				return err
			}
			if item.State == digest.Acked || item.State == digest.Expired {
				terminal = append(terminal, item)
			}
			return nil
		}); err != nil {
			return err
		}
		sort.Slice(terminal, func(i, j int) bool {
			if terminal[i].TerminalAt.Equal(terminal[j].TerminalAt) {
				return terminal[i].ID < terminal[j].ID
			}
			return terminal[i].TerminalAt.Before(terminal[j].TerminalAt)
		})
		remove := make(map[string]struct{})
		for _, item := range terminal {
			if !item.TerminalAt.IsZero() && !item.TerminalAt.After(before) {
				remove[item.ID] = struct{}{}
			}
		}
		// maxAcked is retained in the signature for source compatibility. It
		// never permits deleting a terminal record newer than the retention
		// cutoff; doing so would make delivery inspection unsafe.
		_ = maxAcked
		for id := range remove {
			if err := deleteDigestItem(tx, id); err != nil {
				return err
			}
		}
		var deliveryKeys [][]byte
		if err := deliveriesBucket.ForEach(func(key, value []byte) error {
			var delivery digest.Delivery
			if err := json.Unmarshal(value, &delivery); err != nil {
				return err
			}
			if (delivery.State == digest.DeliveryDelivered || delivery.State == digest.DeliveryExpired) &&
				!delivery.TerminalAt.IsZero() && !delivery.TerminalAt.After(before) {
				deliveryKeys = append(deliveryKeys, append([]byte(nil), key...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, key := range deliveryKeys {
			if err := deleteDigestDelivery(tx, string(key)); err != nil {
				return err
			}
		}
		activeSinks, _, _ := s.activeSinkSnapshot()
		if err := deliveriesBucket.ForEach(func(_, value []byte) error {
			var delivery digest.Delivery
			if err := json.Unmarshal(value, &delivery); err != nil {
				return err
			}
			for _, sink := range delivery.RequiredSinks {
				activeSinks[sink] = struct{}{}
			}
			return nil
		}); err != nil {
			return err
		}
		healthBucket := tx.Bucket([]byte(digestSinkHealthBucket))
		var staleHealth [][]byte
		if err := healthBucket.ForEach(func(key, value []byte) error {
			if _, active := activeSinks[string(key)]; active {
				return nil
			}
			var health digest.SinkStats
			if err := json.Unmarshal(value, &health); err != nil {
				return fmt.Errorf("decode digest sink health %q: %w", key, err)
			}
			lastEvent := laterDigestTime(health.LastDeliveredAt, health.LastFailedAt)
			if lastEvent.IsZero() || !lastEvent.After(before) {
				staleHealth = append(staleHealth, append([]byte(nil), key...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, key := range staleHealth {
			if err := healthBucket.Delete(key); err != nil {
				return err
			}
			healthChanged = true
		}
		return nil
	})
	if err == nil && healthChanged {
		s.statsMu.Lock()
		s.statsValid = false
		s.statsMu.Unlock()
	}
	return err
}

func decodeDigestItem(raw []byte, now time.Time) (digest.Item, bool, error) {
	var item digest.Item
	if err := json.Unmarshal(raw, &item); err != nil {
		return item, false, err
	}
	changed := item.SchemaVersion < digest.SchemaVersion
	if item.TopicKey == "" && item.Briefing.Valid() {
		item.TopicKey = digest.TopicKey(item.Briefing, item.Message, item.CreatedAt)
		changed = true
	}
	if item.Priority < -100 {
		item.Priority = -100
		changed = true
	} else if item.Priority > 100 {
		item.Priority = 100
		changed = true
	}
	if item.CreatedAt.IsZero() {
		if item.ExpiresAt.IsZero() {
			item.ExpiresAt = now.Add(digestDefaultTTL)
			changed = true
		}
	} else if item.ExpiresAt.IsZero() {
		item.ExpiresAt = item.CreatedAt.Add(digestDefaultTTL)
		changed = true
	}
	if item.State == digest.Acked {
		if item.TerminalAt.IsZero() {
			item.TerminalAt = item.AcknowledgedAt
			if item.TerminalAt.IsZero() {
				item.TerminalAt = now
			}
			changed = true
		}
		if item.TerminalReason == "" {
			item.TerminalReason = "legacy_ack"
			changed = true
		}
	}
	if item.SchemaVersion != digest.SchemaVersion {
		item.SchemaVersion = digest.SchemaVersion
		changed = true
	}
	return item, changed, nil
}

func terminalizeExpiredItem(item *digest.Item, now time.Time) bool {
	if item.State != digest.Pending && item.State != digest.Leased {
		return false
	}
	if item.ExpiresAt.IsZero() || item.ExpiresAt.After(now) {
		return false
	}
	markItemTerminal(item, digest.Expired, now, "ttl")
	return true
}

func markItemTerminal(item *digest.Item, state digest.State, now time.Time, reason string) {
	item.State = state
	item.TerminalAt = now
	item.TerminalReason = reason
	item.LeaseID = ""
	item.LeaseUntil = time.Time{}
	if state == digest.Acked {
		item.AcknowledgedAt = now
	}
}

func updateDeliveryItems(tx *bolt.Tx, delivery digest.Delivery, update func(*digest.Item)) error {
	bucket := tx.Bucket([]byte(digestBucketV1))
	for _, id := range delivery.ItemIDs {
		raw := bucket.Get([]byte(id))
		if raw == nil {
			return fmt.Errorf("digest delivery %q: item %q not found", delivery.ID, id)
		}
		item, _, err := decodeDigestItem(raw, delivery.UpdatedAt)
		if err != nil {
			return err
		}
		if item.DeliveryID != delivery.ID || item.State != digest.Delivering {
			return fmt.Errorf("digest delivery %q: item %q fence mismatch", delivery.ID, id)
		}
		update(&item)
		if err := putDigestItem(tx, []byte(id), item); err != nil {
			return err
		}
	}
	return nil
}

func expireDelivery(tx *bolt.Tx, key []byte, delivery *digest.Delivery, now time.Time, reason string) error {
	delivery.State = digest.DeliveryExpired
	delivery.UpdatedAt = now
	delivery.TerminalAt = now
	delivery.TerminalReason = reason
	if err := updateDeliveryItems(tx, *delivery, func(item *digest.Item) {
		markItemTerminal(item, digest.Expired, now, reason)
	}); err != nil {
		return err
	}
	return putDigestDelivery(tx, key, *delivery)
}

func allSinksDelivered(delivery digest.Delivery) bool {
	for _, sink := range delivery.RequiredSinks {
		if !delivery.Sinks[sink].Delivered {
			return false
		}
	}
	return true
}

func failureReason(checkpoint digest.SinkCheckpoint) string {
	if checkpoint.Attempts >= digestMaxAttempts {
		return "max_attempts"
	}
	return "deadline"
}

func digestBackoff(deliveryID, sink string, attempts int) time.Duration {
	base := 30 * time.Second
	for i := 1; i < attempts && base < 30*time.Minute; i++ {
		base *= 2
	}
	if base > 30*time.Minute {
		base = 30 * time.Minute
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", deliveryID, sink, attempts)))
	// Deterministic 80%-120% jitter, with the documented 30s/30m bounds.
	percent := 80 + int(sum[0])%41
	delay := time.Duration(int64(base) * int64(percent) / 100)
	if delay < 30*time.Second {
		return 30 * time.Second
	}
	if delay > 30*time.Minute {
		return 30 * time.Minute
	}
	return delay
}

func boundedError(err error) string {
	value := err.Error()
	runes := []rune(value)
	if len(runes) > digestMaxErrorLength {
		value = string(runes[:digestMaxErrorLength])
	}
	return value
}

func normalizeSinks(sinks []string) ([]string, error) {
	set := make(map[string]struct{}, len(sinks))
	for _, sink := range sinks {
		sink = strings.TrimSpace(sink)
		if sink == "" {
			return nil, fmt.Errorf("empty sink")
		}
		set[sink] = struct{}{}
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("no sinks")
	}
	result := make([]string, 0, len(set))
	for sink := range set {
		result = append(result, sink)
	}
	sort.Strings(result)
	return result, nil
}

func messageHash(message *model.Message) (string, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func cloneMessage(message *model.Message) (*model.Message, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	var clone model.Message
	if err := json.Unmarshal(raw, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func cloneDelivery(delivery *digest.Delivery) (*digest.Delivery, error) {
	raw, err := json.Marshal(delivery)
	if err != nil {
		return nil, err
	}
	var clone digest.Delivery
	if err := json.Unmarshal(raw, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func putJSON(bucket *bolt.Bucket, key []byte, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bucket.Put(key, raw)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("digest store: context is required")
	}
	return ctx.Err()
}

func newDigestToken(kind string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("digest %s token: %w", kind, err)
	}
	return hex.EncodeToString(value[:]), nil
}
