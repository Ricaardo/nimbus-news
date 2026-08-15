package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	bolt "go.etcd.io/bbolt"
)

const (
	digestItemStateIndexBucket                  = "digest_idx_item_state_v1"
	digestItemLeaseIndexBucket                  = "digest_idx_item_lease_v1"
	digestItemLeaseDueIndexBucket               = "digest_idx_item_lease_due_v1"
	digestItemExpiryIndexBucket                 = "digest_idx_item_expiry_v1"
	digestDeliveryStateIndexBucket              = "digest_idx_delivery_state_v1"
	digestDeliveryDeadlineIndexBucket           = "digest_idx_delivery_deadline_v1"
	digestDeliverySinkDueIndexBucket            = "digest_idx_delivery_sink_due_v1"
	digestItemTTLTerminalIndexBucket            = "digest_idx_item_ttl_terminal_v1"
	digestDeliveryDeadlineTerminalIndexBucket   = "digest_idx_delivery_deadline_terminal_v1"
	digestDeliveryMaxAttemptTerminalIndexBucket = "digest_idx_delivery_max_attempt_terminal_v1"
	digestCanonicalGenerationKey                = "canonical_generation"
	digestIndexedGenerationKey                  = "indexed_generation"
)

var digestIndexBuckets = []string{
	digestItemStateIndexBucket,
	digestItemLeaseIndexBucket,
	digestItemLeaseDueIndexBucket,
	digestItemExpiryIndexBucket,
	digestDeliveryStateIndexBucket,
	digestDeliveryDeadlineIndexBucket,
	digestDeliverySinkDueIndexBucket,
	digestItemTTLTerminalIndexBucket,
	digestDeliveryDeadlineTerminalIndexBucket,
	digestDeliveryMaxAttemptTerminalIndexBucket,
}

type digestIndexState struct {
	mu      sync.RWMutex
	enabled bool
}

func (s *DigestStore) updateDigest(fn func(*bolt.Tx) error) error {
	return s.db.Update(func(tx *bolt.Tx) (err error) {
		maintainMarker := s.indexesEnabled(tx)
		defer func() {
			if err == nil && maintainMarker {
				err = touchDigestIndexTx(tx)
			}
		}()
		return fn(tx)
	})
}

func (s *DigestStore) setIndexesEnabled(enabled bool) {
	s.index.mu.Lock()
	s.index.enabled = enabled
	s.index.mu.Unlock()
}

func (s *DigestStore) indexesEnabled(tx *bolt.Tx) bool {
	s.index.mu.RLock()
	enabled := s.index.enabled
	s.index.mu.RUnlock()
	if !enabled {
		return false
	}
	meta := tx.Bucket([]byte(digestIndexMetaBucket))
	if meta == nil || string(meta.Get([]byte("version"))) != digestIndexVersion {
		s.setIndexesEnabled(false)
		return false
	}
	canonicalGeneration, canonicalOK := readDigestGeneration(meta, digestCanonicalGenerationKey)
	indexedGeneration, indexedOK := readDigestGeneration(meta, digestIndexedGenerationKey)
	if !canonicalOK || !indexedOK || indexedGeneration != canonicalGeneration {
		s.setIndexesEnabled(false)
		return false
	}
	for _, name := range digestIndexBuckets {
		if tx.Bucket([]byte(name)) == nil {
			s.setIndexesEnabled(false)
			return false
		}
	}
	return true
}

func (s *DigestStore) rebuildDigestIndexes() error {
	err := s.db.Update(func(tx *bolt.Tx) error {
		for _, name := range digestIndexBuckets {
			if tx.Bucket([]byte(name)) != nil {
				if err := tx.DeleteBucket([]byte(name)); err != nil {
					return err
				}
			}
			if _, err := tx.CreateBucket([]byte(name)); err != nil {
				return err
			}
		}
		items := tx.Bucket([]byte(digestBucketV1))
		if err := items.ForEach(func(key, value []byte) error {
			var item digest.Item
			if err := json.Unmarshal(value, &item); err != nil {
				return fmt.Errorf("rebuild digest item index %q: %w", key, err)
			}
			return addItemIndexes(tx, item)
		}); err != nil {
			return err
		}
		deliveries := tx.Bucket([]byte(digestDeliveriesBucket))
		if err := deliveries.ForEach(func(key, value []byte) error {
			var delivery digest.Delivery
			if err := json.Unmarshal(value, &delivery); err != nil {
				return fmt.Errorf("rebuild digest delivery index %q: %w", key, err)
			}
			return addDeliveryIndexes(tx, delivery)
		}); err != nil {
			return err
		}
		return touchDigestIndexTx(tx)
	})
	s.setIndexesEnabled(err == nil)
	return err
}

func putDigestItem(tx *bolt.Tx, key []byte, item digest.Item) error {
	bucket := tx.Bucket([]byte(digestBucketV1))
	maintainIndexes := indexesUsableTx(tx)
	if maintainIndexes {
		if raw := bucket.Get(key); raw != nil {
			var old digest.Item
			if err := json.Unmarshal(raw, &old); err != nil {
				return err
			}
			if err := removeItemIndexes(tx, old); err != nil {
				return err
			}
		}
	}
	if err := putJSON(bucket, key, item); err != nil {
		return err
	}
	if err := incrementDigestGenerationTx(tx); err != nil {
		return err
	}
	if maintainIndexes {
		if err := addItemIndexes(tx, item); err != nil {
			return err
		}
		return touchDigestIndexTx(tx)
	}
	return nil
}

func putDigestDelivery(tx *bolt.Tx, key []byte, delivery digest.Delivery) error {
	bucket := tx.Bucket([]byte(digestDeliveriesBucket))
	maintainIndexes := indexesUsableTx(tx)
	if maintainIndexes {
		if raw := bucket.Get(key); raw != nil {
			var old digest.Delivery
			if err := json.Unmarshal(raw, &old); err != nil {
				return err
			}
			if err := removeDeliveryIndexes(tx, old); err != nil {
				return err
			}
		}
	}
	if err := putJSON(bucket, key, delivery); err != nil {
		return err
	}
	if err := incrementDigestGenerationTx(tx); err != nil {
		return err
	}
	if maintainIndexes {
		if err := addDeliveryIndexes(tx, delivery); err != nil {
			return err
		}
		return touchDigestIndexTx(tx)
	}
	return nil
}

func deleteDigestItem(tx *bolt.Tx, id string) error {
	bucket := tx.Bucket([]byte(digestBucketV1))
	raw := bucket.Get([]byte(id))
	if raw == nil {
		return nil
	}
	maintainIndexes := indexesUsableTx(tx)
	if maintainIndexes {
		var item digest.Item
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		if err := removeItemIndexes(tx, item); err != nil {
			return err
		}
	}
	if err := bucket.Delete([]byte(id)); err != nil {
		return err
	}
	if err := incrementDigestGenerationTx(tx); err != nil {
		return err
	}
	if maintainIndexes {
		return touchDigestIndexTx(tx)
	}
	return nil
}

func deleteDigestDelivery(tx *bolt.Tx, id string) error {
	bucket := tx.Bucket([]byte(digestDeliveriesBucket))
	raw := bucket.Get([]byte(id))
	if raw == nil {
		return nil
	}
	maintainIndexes := indexesUsableTx(tx)
	if maintainIndexes {
		var delivery digest.Delivery
		if err := json.Unmarshal(raw, &delivery); err != nil {
			return err
		}
		if err := removeDeliveryIndexes(tx, delivery); err != nil {
			return err
		}
	}
	if err := bucket.Delete([]byte(id)); err != nil {
		return err
	}
	if err := incrementDigestGenerationTx(tx); err != nil {
		return err
	}
	if maintainIndexes {
		return touchDigestIndexTx(tx)
	}
	return nil
}

func touchDigestIndexTx(tx *bolt.Tx) error {
	meta := tx.Bucket([]byte(digestIndexMetaBucket))
	if meta == nil {
		return nil
	}
	generation, ok := readDigestGeneration(meta, digestCanonicalGenerationKey)
	if !ok {
		return fmt.Errorf("digest canonical generation is missing or corrupt")
	}
	return writeDigestGeneration(meta, digestIndexedGenerationKey, generation)
}

func indexesUsableTx(tx *bolt.Tx) bool {
	meta := tx.Bucket([]byte(digestIndexMetaBucket))
	if meta == nil || string(meta.Get([]byte("version"))) != digestIndexVersion {
		return false
	}
	canonicalGeneration, canonicalOK := readDigestGeneration(meta, digestCanonicalGenerationKey)
	indexedGeneration, indexedOK := readDigestGeneration(meta, digestIndexedGenerationKey)
	if !canonicalOK || !indexedOK || canonicalGeneration != indexedGeneration {
		return false
	}
	for _, name := range digestIndexBuckets {
		if tx.Bucket([]byte(name)) == nil {
			return false
		}
	}
	return true
}

func digestGenerationTx(tx *bolt.Tx) (uint64, bool) {
	meta := tx.Bucket([]byte(digestIndexMetaBucket))
	if meta == nil || string(meta.Get([]byte("version"))) != digestIndexVersion {
		return 0, false
	}
	return readDigestGeneration(meta, digestCanonicalGenerationKey)
}

func incrementDigestGenerationTx(tx *bolt.Tx) error {
	meta := tx.Bucket([]byte(digestIndexMetaBucket))
	if meta == nil {
		return fmt.Errorf("digest index metadata bucket is missing")
	}
	generation, ok := readDigestGeneration(meta, digestCanonicalGenerationKey)
	if !ok {
		return fmt.Errorf("digest canonical generation is missing or corrupt")
	}
	return writeDigestGeneration(meta, digestCanonicalGenerationKey, generation+1)
}

func readDigestGeneration(meta *bolt.Bucket, key string) (uint64, bool) {
	raw := meta.Get([]byte(key))
	if len(raw) != 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(raw), true
}

func writeDigestGeneration(meta *bolt.Bucket, key string, generation uint64) error {
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, generation)
	return meta.Put([]byte(key), raw)
}

func addItemIndexes(tx *bolt.Tx, item digest.Item) error {
	if err := tx.Bucket([]byte(digestItemStateIndexBucket)).Put(
		itemStateKey(item.Briefing, item.State, item.CreatedAt, item.ID), []byte(item.ID)); err != nil {
		return err
	}
	if item.State == digest.Leased && item.LeaseID != "" {
		if err := tx.Bucket([]byte(digestItemLeaseIndexBucket)).Put(joinKey([]byte(item.LeaseID), []byte(item.ID)), []byte(item.ID)); err != nil {
			return err
		}
		if err := tx.Bucket([]byte(digestItemLeaseDueIndexBucket)).Put(timeIDKey(item.LeaseUntil, item.ID), []byte(item.ID)); err != nil {
			return err
		}
	}
	if (item.State == digest.Pending || item.State == digest.Leased) && !item.ExpiresAt.IsZero() {
		return tx.Bucket([]byte(digestItemExpiryIndexBucket)).Put(timeIDKey(item.ExpiresAt, item.ID), []byte(item.ID))
	}
	if item.State == digest.Expired && item.TerminalReason == "ttl" && !item.TerminalAt.IsZero() {
		return tx.Bucket([]byte(digestItemTTLTerminalIndexBucket)).Put(
			timeIDKey(item.TerminalAt, item.ID), []byte(item.ID))
	}
	return nil
}

func removeItemIndexes(tx *bolt.Tx, item digest.Item) error {
	if err := tx.Bucket([]byte(digestItemStateIndexBucket)).Delete(
		itemStateKey(item.Briefing, item.State, item.CreatedAt, item.ID)); err != nil {
		return err
	}
	if item.State == digest.Leased && item.LeaseID != "" {
		if err := tx.Bucket([]byte(digestItemLeaseIndexBucket)).Delete(joinKey([]byte(item.LeaseID), []byte(item.ID))); err != nil {
			return err
		}
		if err := tx.Bucket([]byte(digestItemLeaseDueIndexBucket)).Delete(timeIDKey(item.LeaseUntil, item.ID)); err != nil {
			return err
		}
	}
	if (item.State == digest.Pending || item.State == digest.Leased) && !item.ExpiresAt.IsZero() {
		return tx.Bucket([]byte(digestItemExpiryIndexBucket)).Delete(timeIDKey(item.ExpiresAt, item.ID))
	}
	if item.State == digest.Expired && item.TerminalReason == "ttl" && !item.TerminalAt.IsZero() {
		return tx.Bucket([]byte(digestItemTTLTerminalIndexBucket)).Delete(
			timeIDKey(item.TerminalAt, item.ID))
	}
	return nil
}

func addDeliveryIndexes(tx *bolt.Tx, delivery digest.Delivery) error {
	if err := tx.Bucket([]byte(digestDeliveryStateIndexBucket)).Put(
		deliveryStateKey(delivery.State, delivery.CreatedAt, delivery.ID), []byte(delivery.ID)); err != nil {
		return err
	}
	if delivery.State != digest.DeliveryPending {
		if delivery.State == digest.DeliveryExpired && !delivery.TerminalAt.IsZero() {
			switch delivery.TerminalReason {
			case "deadline":
				return tx.Bucket([]byte(digestDeliveryDeadlineTerminalIndexBucket)).Put(
					timeIDKey(delivery.TerminalAt, delivery.ID), []byte(delivery.ID))
			case "max_attempts":
				return tx.Bucket([]byte(digestDeliveryMaxAttemptTerminalIndexBucket)).Put(
					timeIDKey(delivery.TerminalAt, delivery.ID), []byte(delivery.ID))
			}
		}
		return nil
	}
	if err := tx.Bucket([]byte(digestDeliveryDeadlineIndexBucket)).Put(
		timeIDKey(delivery.Deadline, delivery.ID), []byte(delivery.ID)); err != nil {
		return err
	}
	for _, sink := range delivery.RequiredSinks {
		checkpoint := delivery.Sinks[sink]
		if checkpoint.Delivered {
			continue
		}
		due := checkpoint.NextAttemptAt
		if checkpoint.ClaimedUntil.After(due) {
			due = checkpoint.ClaimedUntil
		}
		if err := tx.Bucket([]byte(digestDeliverySinkDueIndexBucket)).Put(
			sinkDueKey(due, delivery.ID, sink), joinKey([]byte(delivery.ID), []byte(sink))); err != nil {
			return err
		}
	}
	return nil
}

func removeDeliveryIndexes(tx *bolt.Tx, delivery digest.Delivery) error {
	if err := tx.Bucket([]byte(digestDeliveryStateIndexBucket)).Delete(
		deliveryStateKey(delivery.State, delivery.CreatedAt, delivery.ID)); err != nil {
		return err
	}
	if delivery.State != digest.DeliveryPending {
		if delivery.State == digest.DeliveryExpired && !delivery.TerminalAt.IsZero() {
			switch delivery.TerminalReason {
			case "deadline":
				return tx.Bucket([]byte(digestDeliveryDeadlineTerminalIndexBucket)).Delete(
					timeIDKey(delivery.TerminalAt, delivery.ID))
			case "max_attempts":
				return tx.Bucket([]byte(digestDeliveryMaxAttemptTerminalIndexBucket)).Delete(
					timeIDKey(delivery.TerminalAt, delivery.ID))
			}
		}
		return nil
	}
	if err := tx.Bucket([]byte(digestDeliveryDeadlineIndexBucket)).Delete(
		timeIDKey(delivery.Deadline, delivery.ID)); err != nil {
		return err
	}
	for _, sink := range delivery.RequiredSinks {
		checkpoint := delivery.Sinks[sink]
		if checkpoint.Delivered {
			continue
		}
		due := checkpoint.NextAttemptAt
		if checkpoint.ClaimedUntil.After(due) {
			due = checkpoint.ClaimedUntil
		}
		if err := tx.Bucket([]byte(digestDeliverySinkDueIndexBucket)).Delete(
			sinkDueKey(due, delivery.ID, sink)); err != nil {
			return err
		}
	}
	return nil
}

func itemStatePrefix(briefing digest.Briefing, state digest.State) []byte {
	return joinKey([]byte(briefing), []byte(state), nil)
}

func itemStateKey(briefing digest.Briefing, state digest.State, created time.Time, id string) []byte {
	return append(itemStatePrefix(briefing, state), timeIDKey(created, id)...)
}

func deliveryStateKey(state digest.DeliveryState, created time.Time, id string) []byte {
	return joinKey([]byte(state), timeIDKey(created, id))
}

func timeIDKey(at time.Time, id string) []byte {
	key := make([]byte, 8, 9+len(id))
	binary.BigEndian.PutUint64(key, uint64(at.UnixNano())^(uint64(1)<<63))
	key = append(key, 0)
	return append(key, id...)
}

func sinkDueKey(at time.Time, deliveryID, sink string) []byte {
	return joinKey(timeIDKey(at, deliveryID), []byte(sink))
}

func dueEndKey(now time.Time) []byte {
	key := timeIDKey(now, "\xff")
	return append(key, 0xff)
}

func joinKey(parts ...[]byte) []byte {
	return bytes.Join(parts, []byte{0})
}

func digestStatsFromIndexesTx(
	ctx context.Context,
	tx *bolt.Tx,
	activeSinks map[string]struct{},
	activeSinksSet bool,
) (digest.Stats, error) {
	stats := digest.Stats{
		Items:      make(map[digest.State]int),
		Deliveries: make(map[digest.DeliveryState]int),
		Sinks:      make(map[string]digest.SinkStats),
	}
	itemStates := tx.Bucket([]byte(digestItemStateIndexBucket))
	if err := itemStates.ForEach(func(key, _ []byte) error {
		if err := contextError(ctx); err != nil {
			return err
		}
		first := bytes.IndexByte(key, 0)
		if first < 0 {
			return fmt.Errorf("malformed digest item state index key")
		}
		secondOffset := bytes.IndexByte(key[first+1:], 0)
		if secondOffset < 0 {
			return fmt.Errorf("malformed digest item state index key")
		}
		second := first + 1 + secondOffset
		state := digest.State(key[first+1 : second])
		createdAt, ok := digestIndexTime(key[second+1:])
		if !ok || !state.Valid() {
			return fmt.Errorf("malformed digest item state index key")
		}
		stats.Items[state]++
		switch state {
		case digest.Pending:
			stats.OldestPendingItemAt = earlierTime(stats.OldestPendingItemAt, createdAt)
		case digest.Leased:
			stats.OldestLeasedItemAt = earlierTime(stats.OldestLeasedItemAt, createdAt)
		case digest.Delivering:
			stats.OldestDeliveringItemAt = earlierTime(stats.OldestDeliveringItemAt, createdAt)
		}
		return nil
	}); err != nil {
		return digest.Stats{}, err
	}

	deliveriesBucket := tx.Bucket([]byte(digestDeliveriesBucket))
	stateIndex := tx.Bucket([]byte(digestDeliveryStateIndexBucket))
	pendingSinks := make(map[string]struct{})
	for _, state := range []digest.DeliveryState{
		digest.DeliveryPending, digest.DeliveryDelivered, digest.DeliveryExpired,
	} {
		prefix := joinKey([]byte(state), nil)
		cursor := stateIndex.Cursor()
		for key, id := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, id = cursor.Next() {
			if err := contextError(ctx); err != nil {
				return digest.Stats{}, err
			}
			stats.Deliveries[state]++
			createdAt, ok := digestIndexTime(key[len(prefix):])
			if !ok {
				return digest.Stats{}, fmt.Errorf("malformed digest delivery state index key")
			}
			if state != digest.DeliveryPending {
				continue
			}
			stats.OldestPendingDeliveryAt = earlierTime(stats.OldestPendingDeliveryAt, createdAt)
			raw := deliveriesBucket.Get(id)
			if raw == nil {
				return digest.Stats{}, fmt.Errorf("digest delivery index references missing delivery %q", id)
			}
			var delivery digest.Delivery
			if err := json.Unmarshal(raw, &delivery); err != nil {
				return digest.Stats{}, fmt.Errorf("decode indexed digest delivery %q: %w", id, err)
			}
			for _, sink := range delivery.RequiredSinks {
				checkpoint, ok := delivery.Sinks[sink]
				if !ok {
					return digest.Stats{}, fmt.Errorf("digest delivery %q: missing sink checkpoint %q", delivery.ID, sink)
				}
				if checkpoint.Delivered {
					continue
				}
				pendingSinks[sink] = struct{}{}
				sinkStats := stats.Sinks[sink]
				sinkStats.Pending++
				sinkStats.OldestPendingAt = earlierTime(sinkStats.OldestPendingAt, delivery.CreatedAt)
				stats.Sinks[sink] = sinkStats
			}
		}
	}

	stats.LatestItemExpiryAt = latestDigestIndexTime(tx.Bucket([]byte(digestItemTTLTerminalIndexBucket)))
	stats.LatestDeadlineExpiryAt = latestDigestIndexTime(tx.Bucket([]byte(digestDeliveryDeadlineTerminalIndexBucket)))
	stats.LatestMaxAttemptExpiryAt = latestDigestIndexTime(tx.Bucket([]byte(digestDeliveryMaxAttemptTerminalIndexBucket)))

	for sink := range activeSinks {
		if _, exists := stats.Sinks[sink]; !exists {
			stats.Sinks[sink] = digest.SinkStats{}
		}
	}
	healthBucket := tx.Bucket([]byte(digestSinkHealthBucket))
	if err := healthBucket.ForEach(func(key, value []byte) error {
		sink := string(key)
		if activeSinksSet {
			if _, active := activeSinks[sink]; !active {
				if _, pending := pendingSinks[sink]; !pending {
					return nil
				}
			}
		}
		var health digest.SinkStats
		if err := json.Unmarshal(value, &health); err != nil {
			return fmt.Errorf("decode digest sink health %q: %w", key, err)
		}
		sinkStats := stats.Sinks[sink]
		sinkStats.LastDeliveredAt = health.LastDeliveredAt
		sinkStats.LastFailedAt = health.LastFailedAt
		sinkStats.ConsecutiveFailures = health.ConsecutiveFailures
		stats.Sinks[sink] = sinkStats
		return nil
	}); err != nil {
		return digest.Stats{}, err
	}
	return stats, nil
}

func digestIndexTime(key []byte) (time.Time, bool) {
	if len(key) < 8 {
		return time.Time{}, false
	}
	nanos := int64(binary.BigEndian.Uint64(key[:8]) ^ (uint64(1) << 63))
	return time.Unix(0, nanos).UTC(), true
}

func latestDigestIndexTime(bucket *bolt.Bucket) time.Time {
	if bucket == nil {
		return time.Time{}
	}
	key, _ := bucket.Cursor().Last()
	at, _ := digestIndexTime(key)
	return at
}

func splitPair(value []byte) (string, string, bool) {
	parts := bytes.SplitN(value, []byte{0}, 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return string(parts[0]), string(parts[1]), true
}

func forIndexPrefix(bucket *bolt.Bucket, prefix []byte, visit func([]byte) error) error {
	cursor := bucket.Cursor()
	for key, value := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, value = cursor.Next() {
		if err := visit(value); err != nil {
			return err
		}
	}
	return nil
}

func forDueIndex(bucket *bolt.Bucket, now time.Time, visit func([]byte) error) error {
	end := dueEndKey(now)
	cursor := bucket.Cursor()
	for key, value := cursor.First(); key != nil && bytes.Compare(key, end) <= 0; key, value = cursor.Next() {
		if err := visit(value); err != nil {
			return err
		}
	}
	return nil
}
