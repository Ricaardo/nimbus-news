package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	bolt "go.etcd.io/bbolt"
)

const (
	digestRouteAdmissionsBucket = "digest_route_admissions_v1"
	digestRouteQuarantineBucket = "digest_route_quarantine_v1"
	digestRouteQuotaBucket      = "digest_route_quota_v1"
	digestMigrationsBucket      = "digest_migrations_v1"
	digestTopicsV4Migration     = "topics_v4"
	digestAdmissionRetention    = 7 * 24 * time.Hour
	digestQuarantineRetention   = 7 * 24 * time.Hour
	digestQuarantineMaxRecords  = 100
)

var digestShanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

type routeAdmissionRecord struct {
	Date         string                          `json:"date"`
	Source       string                          `json:"source"`
	MessageID    string                          `json:"message_id"`
	Lane         digest.RouteLane                `json:"lane"`
	Limit        int                             `json:"limit"`
	Critical     bool                            `json:"critical,omitempty"`
	CreatedAt    time.Time                       `json:"created_at"`
	CompletedAt  time.Time                       `json:"completed_at,omitempty"`
	Channels     []string                        `json:"channels,omitempty"`
	Message      *model.Message                  `json:"message,omitempty"`
	Delivered    map[string]bool                 `json:"delivered,omitempty"`
	ClaimToken   string                          `json:"claim_token,omitempty"`
	ClaimedUntil time.Time                       `json:"claimed_until,omitempty"`
	Failures     map[string]directChannelFailure `json:"failures,omitempty"`
}

type directChannelFailure struct {
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"next_attempt_at,omitempty"`
	Terminal      bool      `json:"terminal,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
}

type routeAdmissionQuarantineRecord struct {
	QuarantinedAt time.Time `json:"quarantined_at"`
	OriginalKey   []byte    `json:"original_key"`
	Raw           []byte    `json:"raw"`
	DecodeError   string    `json:"decode_error"`
}

func (s *DigestStore) LookupDirect(ctx context.Context, request digest.RouteRequest) (digest.RouteAdmission, error) {
	if err := contextError(ctx); err != nil {
		return digest.RouteAdmission{}, err
	}
	if strings.TrimSpace(request.Source) == "" || strings.TrimSpace(request.MessageID) == "" {
		return digest.RouteAdmission{}, fmt.Errorf("digest direct lookup: invalid request")
	}
	result := digest.RouteAdmission{Lane: digest.RouteDirect}
	err := s.db.View(func(tx *bolt.Tx) error {
		_, raw := findDirectRecord(tx.Bucket([]byte(digestRouteAdmissionsBucket)), request.Source, request.MessageID)
		if raw == nil {
			return nil
		}
		var existing routeAdmissionRecord
		if err := json.Unmarshal(raw, &existing); err != nil {
			return fmt.Errorf("decode route admission: %w", err)
		}
		result.Admitted = true
		result.Existing = true
		result.Lane = existing.Lane
		result.Limit = existing.Limit
		result.Completed = !existing.CompletedAt.IsZero()
		result.Channels = pendingDirectChannels(existing)
		return nil
	})
	return result, err
}

func (s *DigestStore) AdmitDirect(ctx context.Context, request digest.RouteRequest) (digest.RouteAdmission, error) {
	if request.Lane != digest.RouteDirect {
		return digest.RouteAdmission{}, fmt.Errorf("digest admit direct: lane must be direct")
	}
	return s.admitRoute(ctx, request, nil)
}

func (s *DigestStore) ClaimDirect(ctx context.Context, source, messageID string, lease time.Duration) (*digest.DirectClaim, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if lease <= 0 {
		return nil, fmt.Errorf("claim direct: lease must be positive")
	}
	var claim *digest.DirectClaim
	err := s.updateDigest(func(tx *bolt.Tx) error {
		var err error
		bucket := tx.Bucket([]byte(digestRouteAdmissionsBucket))
		key, _ := findDirectRecord(bucket, source, messageID)
		claim, err = s.claimDirectRecord(bucket, key, lease)
		return err
	})
	return claim, err
}

func (s *DigestStore) ClaimPendingDirect(ctx context.Context, limit int, lease time.Duration) ([]digest.DirectClaim, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || lease <= 0 {
		return nil, fmt.Errorf("claim pending direct: invalid limit or lease")
	}
	claims := make([]digest.DirectClaim, 0, limit)
	err := s.updateDigest(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(digestRouteAdmissionsBucket))
		cursor := bucket.Cursor()
		for key, raw := cursor.First(); key != nil && len(claims) < limit; key, raw = cursor.Next() {
			var record routeAdmissionRecord
			if json.Unmarshal(raw, &record) != nil || record.Lane != digest.RouteDirect || record.Message == nil || !record.CompletedAt.IsZero() {
				continue
			}
			claim, err := s.claimDirectRecord(bucket, append([]byte(nil), key...), lease)
			if err != nil {
				return err
			}
			if claim != nil {
				claims = append(claims, *claim)
			}
		}
		return nil
	})
	return claims, err
}

func (s *DigestStore) claimDirectRecord(bucket *bolt.Bucket, key []byte, lease time.Duration) (*digest.DirectClaim, error) {
	raw := bucket.Get(key)
	if raw == nil {
		return nil, nil
	}
	var record routeAdmissionRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	if record.Lane != digest.RouteDirect || record.Message == nil || !record.CompletedAt.IsZero() || (!record.ClaimedUntil.IsZero() && record.ClaimedUntil.After(now)) {
		return nil, nil
	}
	channels := dueDirectChannels(record, now)
	if len(channels) == 0 {
		if directRecordTerminal(record) {
			record.CompletedAt = now
			return nil, putJSON(bucket, key, record)
		}
		return nil, nil
	}
	token, err := directClaimToken()
	if err != nil {
		return nil, err
	}
	record.ClaimToken = token
	record.ClaimedUntil = now.Add(lease)
	if err := putJSON(bucket, key, record); err != nil {
		return nil, err
	}
	message, err := cloneMessage(record.Message)
	if err != nil {
		return nil, err
	}
	return &digest.DirectClaim{Source: record.Source, MessageID: record.MessageID, Token: token, Message: message, Channels: channels}, nil
}

func (s *DigestStore) FailDirectChannel(ctx context.Context, source, messageID, token, channel, reason string, permanent bool) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	return s.updateDigest(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(digestRouteAdmissionsBucket))
		key, _ := findDirectRecord(bucket, source, messageID)
		raw := bucket.Get(key)
		if raw == nil {
			return fmt.Errorf("fail direct channel: admission not found")
		}
		var record routeAdmissionRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if record.ClaimToken != token || token == "" {
			return fmt.Errorf("fail direct channel: stale claim")
		}
		if record.Failures == nil {
			record.Failures = make(map[string]directChannelFailure)
		}
		failure := record.Failures[channel]
		failure.Attempts++
		failure.LastError = reason
		failure.Terminal = permanent || failure.Attempts >= 10
		if failure.Terminal {
			failure.NextAttemptAt = time.Time{}
		} else {
			backoff := 30 * time.Second * time.Duration(1<<min(failure.Attempts-1, 7))
			failure.NextAttemptAt = s.now().UTC().Add(backoff)
		}
		record.Failures[channel] = failure
		if directRecordTerminal(record) {
			record.CompletedAt = s.now().UTC()
			record.ClaimToken = ""
			record.ClaimedUntil = time.Time{}
		}
		return putJSON(bucket, key, record)
	})
}

func (s *DigestStore) CompleteDirectChannel(ctx context.Context, source, messageID, token, channel string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	return s.updateDigest(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(digestRouteAdmissionsBucket))
		key, _ := findDirectRecord(bucket, source, messageID)
		raw := bucket.Get(key)
		if raw == nil {
			return fmt.Errorf("complete direct channel: admission not found")
		}
		var record routeAdmissionRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if record.ClaimToken != token || token == "" {
			return fmt.Errorf("complete direct channel: stale claim")
		}
		known := false
		for _, configured := range record.Channels {
			if configured == channel {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("complete direct channel: unknown channel")
		}
		if record.Delivered == nil {
			record.Delivered = make(map[string]bool)
		}
		record.Delivered[channel] = true
		if len(pendingDirectChannels(record)) == 0 {
			record.CompletedAt = s.now().UTC()
			record.ClaimToken = ""
			record.ClaimedUntil = time.Time{}
		}
		return putJSON(bucket, key, record)
	})
}

func (s *DigestStore) ReleaseDirect(ctx context.Context, source, messageID, token string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	return s.updateDigest(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(digestRouteAdmissionsBucket))
		key, _ := findDirectRecord(bucket, source, messageID)
		raw := bucket.Get(key)
		if raw == nil {
			return nil
		}
		var record routeAdmissionRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if record.ClaimToken != token {
			return nil
		}
		record.ClaimToken = ""
		record.ClaimedUntil = time.Time{}
		return putJSON(bucket, key, record)
	})
}

func pendingDirectChannels(record routeAdmissionRecord) []string {
	pending := make([]string, 0, len(record.Channels))
	for _, channel := range record.Channels {
		if !record.Delivered[channel] && !record.Failures[channel].Terminal {
			pending = append(pending, channel)
		}
	}
	return pending
}

func dueDirectChannels(record routeAdmissionRecord, now time.Time) []string {
	pending := make([]string, 0, len(record.Channels))
	for _, channel := range record.Channels {
		failure := record.Failures[channel]
		if !record.Delivered[channel] && !failure.Terminal && (failure.NextAttemptAt.IsZero() || !failure.NextAttemptAt.After(now)) {
			pending = append(pending, channel)
		}
	}
	return pending
}

func directRecordTerminal(record routeAdmissionRecord) bool {
	for _, channel := range record.Channels {
		if !record.Delivered[channel] && !record.Failures[channel].Terminal {
			return false
		}
	}
	return len(record.Channels) > 0
}

func directClaimToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", raw[:]), nil
}

func (s *DigestStore) EnqueueRoutedDigest(ctx context.Context, request digest.RouteRequest, msg *model.Message) (digest.RouteAdmission, error) {
	if request.Lane != digest.RouteDigest || !request.Briefing.Valid() || msg == nil {
		return digest.RouteAdmission{}, fmt.Errorf("digest routed enqueue: invalid lane, briefing, or message")
	}
	return s.admitRoute(ctx, request, msg)
}

func (s *DigestStore) admitRoute(ctx context.Context, request digest.RouteRequest, msg *model.Message) (digest.RouteAdmission, error) {
	if err := contextError(ctx); err != nil {
		return digest.RouteAdmission{}, err
	}
	if strings.TrimSpace(request.Source) == "" || strings.TrimSpace(request.MessageID) == "" ||
		!request.Lane.Valid() || request.MaxPerDay < 0 || request.Priority < -100 || request.Priority > 100 {
		return digest.RouteAdmission{}, fmt.Errorf("digest route admission: invalid request")
	}
	now := s.now().UTC()
	date := now.In(digestShanghai).Format("2006-01-02")
	admissionKey := routeAdmissionKey(date, request.Source, request.MessageID)
	if request.Lane == digest.RouteDirect {
		admissionKey = directAdmissionKey(request.Source, request.MessageID)
	}
	quotaKey := routeQuotaKey(date, request.Source, request.Lane)
	result := digest.RouteAdmission{Lane: request.Lane, Limit: request.MaxPerDay}
	err := s.updateDigest(func(tx *bolt.Tx) error {
		admissions := tx.Bucket([]byte(digestRouteAdmissionsBucket))
		quotas := tx.Bucket([]byte(digestRouteQuotaBucket))
		existingKey := admissionKey
		raw := admissions.Get(admissionKey)
		if request.Lane == digest.RouteDirect {
			existingKey, raw = findDirectRecord(admissions, request.Source, request.MessageID)
			if raw == nil {
				existingKey, raw = findSameDayRouteRecord(admissions, date, request.Source, request.MessageID)
			}
		} else if request.Lane == digest.RouteDigest && raw == nil {
			// Source+MessageID is the stable article/output identity. Once that
			// identity has a direct admission, its durable record fences both lanes
			// on every later date, whether delivery is incomplete or completed.
			candidateKey, candidateRaw := findDirectRecord(admissions, request.Source, request.MessageID)
			if candidateRaw != nil {
				var direct routeAdmissionRecord
				if json.Unmarshal(candidateRaw, &direct) == nil && direct.Lane == digest.RouteDirect &&
					direct.Source == request.Source && direct.MessageID == request.MessageID {
					existingKey, raw = candidateKey, candidateRaw
				}
			}
			// Preserve same-day idempotency across lanes. A message already routed
			// to a digest today must not be re-routed direct if policy changes on a
			// later poll.
			if raw == nil {
				existingKey = routeAdmissionKey(date, request.Source, request.MessageID)
				raw = admissions.Get(existingKey)
			}
		}
		if raw != nil {
			var existing routeAdmissionRecord
			if err := json.Unmarshal(raw, &existing); err != nil {
				return fmt.Errorf("decode route admission: %w", err)
			}
			result.Admitted = true
			result.Existing = true
			result.Lane = existing.Lane
			result.Limit = existing.Limit
			result.Used = readRouteQuota(quotas.Get(routeQuotaKey(existing.Date, request.Source, existing.Lane)))
			result.Completed = !existing.CompletedAt.IsZero()
			result.Channels = pendingDirectChannels(existing)
			if request.Lane == digest.RouteDirect && existing.Message == nil && request.Payload != nil {
				cloned, err := cloneMessage(request.Payload)
				if err != nil {
					return err
				}
				existing.Message = cloned
				existing.Channels = append([]string(nil), request.Channels...)
				existing.Delivered = make(map[string]bool)
				if err := putJSON(admissions, existingKey, existing); err != nil {
					return err
				}
				result.Channels = append([]string(nil), existing.Channels...)
			}
			return nil
		}
		used := readRouteQuota(quotas.Get(quotaKey))
		result.Used = used
		if !request.Critical && request.MaxPerDay > 0 && used >= request.MaxPerDay {
			return nil
		}
		result.Admitted = true
		// Critical admissions retain their durable idempotency/audit record but
		// do not consume the ordinary daily lane quota.
		if !request.Critical {
			result.Used = used + 1
			if err := writeRouteQuota(quotas, quotaKey, result.Used); err != nil {
				return err
			}
		}
		record := routeAdmissionRecord{Date: date, Source: request.Source, MessageID: request.MessageID,
			Lane: request.Lane, Limit: request.MaxPerDay, Critical: request.Critical,
			CreatedAt: now, Channels: append([]string(nil), request.Channels...)}
		if request.Lane == digest.RouteDirect {
			cloned, err := cloneMessage(request.Payload)
			if err != nil {
				return err
			}
			record.Message = cloned
			record.Delivered = make(map[string]bool, len(record.Channels))
		}
		if err := putJSON(admissions, admissionKey, record); err != nil {
			return err
		}
		if msg != nil {
			priority := s.routedPriority(request)
			return s.putRoutedDigestItem(tx, request, msg, priority, now)
		}
		return nil
	})
	return result, err
}

func (s *DigestStore) putRoutedDigestItem(tx *bolt.Tx, request digest.RouteRequest, msg *model.Message, priority int, now time.Time) error {
	id := digestItemID(request.Source, request.MessageID)
	bucket := tx.Bucket([]byte(digestBucketV1))
	if bucket.Get([]byte(id)) != nil {
		return nil
	}
	message, err := cloneMessage(msg)
	if err != nil {
		return err
	}
	item := digest.Item{
		ID: id, SchemaVersion: digest.SchemaVersion, Source: request.Source, Briefing: request.Briefing,
		Message: message, State: digest.Pending, Priority: priority,
		TopicKey: digest.TopicKey(request.Briefing, message, now), CreatedAt: now, ExpiresAt: now.Add(digestDefaultTTL),
	}
	return putDigestItem(tx, []byte(id), item)
}

func digestItemID(source, messageID string) string {
	return digestHash(source + "\x00" + messageID)
}

func digestHash(value string) string {
	sum := sha256Sum([]byte(value))
	return fmt.Sprintf("%x", sum)
}

// Kept behind a helper to make conservation hashing and item identity use the
// same algorithm without exposing storage details.
func sha256Sum(value []byte) [32]byte {
	return sha256.Sum256(value)
}

func (s *DigestStore) routedPriority(request digest.RouteRequest) int {
	return clampDigestPriority(request.Priority)
}

func numericScore(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		n, err := strconv.ParseFloat(string(v), 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func clampDigestPriority(priority int) int {
	if priority < -100 {
		return -100
	}
	if priority > 100 {
		return 100
	}
	return priority
}

func routeAdmissionKey(date, source, messageID string) []byte {
	return []byte(date + "\x00" + digestHash(source+"\x00"+messageID))
}

func directAdmissionKey(source, messageID string) []byte {
	return []byte("direct\x00" + digestHash(source+"\x00"+messageID))
}

func findDirectRecord(bucket *bolt.Bucket, source, messageID string) ([]byte, []byte) {
	stable := directAdmissionKey(source, messageID)
	if raw := bucket.Get(stable); raw != nil {
		return stable, raw
	}
	cursor := bucket.Cursor()
	for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
		var record routeAdmissionRecord
		if json.Unmarshal(raw, &record) == nil && record.Lane == digest.RouteDirect && record.Source == source && record.MessageID == messageID {
			return append([]byte(nil), key...), raw
		}
	}
	return stable, nil
}

func findSameDayRouteRecord(bucket *bolt.Bucket, date, source, messageID string) ([]byte, []byte) {
	cursor := bucket.Cursor()
	for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
		var record routeAdmissionRecord
		if json.Unmarshal(raw, &record) == nil && record.Date == date && record.Source == source && record.MessageID == messageID {
			return append([]byte(nil), key...), raw
		}
	}
	return directAdmissionKey(source, messageID), nil
}

func routeQuotaKey(date, source string, lane digest.RouteLane) []byte {
	return []byte(date + "\x00" + digestHash(source) + "\x00" + string(lane))
}

func readRouteQuota(raw []byte) int {
	if len(raw) != 8 {
		return 0
	}
	return int(binary.BigEndian.Uint64(raw))
}

func writeRouteQuota(bucket *bolt.Bucket, key []byte, value int) error {
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, uint64(value))
	return bucket.Put(key, raw)
}

func (s *DigestStore) LeaseTopics(ctx context.Context, briefing digest.Briefing, limit int, ttl time.Duration) (*digest.Lease, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if !briefing.Valid() || limit <= 0 || ttl <= 0 {
		return nil, fmt.Errorf("digest topic lease: invalid briefing, limit, or ttl")
	}
	if limit > 12 {
		limit = 12
	}
	now := s.now().UTC()
	leaseID, err := newDigestToken("lease")
	if err != nil {
		return nil, err
	}
	type topicCandidate struct {
		key      string
		priority int
		score    int64
		oldest   time.Time
		items    []digest.Item
		keys     [][]byte
		active   bool
	}
	var selected []*topicCandidate
	err = s.updateDigest(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(digestBucketV1))
		groups := make(map[string]*topicCandidate)
		touches := make(map[string]digest.Item)
		if err := bucket.ForEach(func(key, raw []byte) error {
			if err := contextError(ctx); err != nil {
				return err
			}
			var item digest.Item
			if err := json.Unmarshal(raw, &item); err != nil {
				return fmt.Errorf("decode digest item %q: %w", key, err)
			}
			topicKey := item.TopicKey
			if topicKey == "" {
				topicKey = digest.TopicKey(item.Briefing, item.Message, item.CreatedAt)
			}
			if terminalizeExpiredItem(&item, now) {
				touches[string(key)] = item
			}
			if item.Briefing != briefing || item.State == digest.Acked || item.State == digest.Expired {
				return nil
			}
			groupKey := digest.LeaseTopicKey(item.Briefing, item.Message, topicKey)
			group := groups[groupKey]
			if group == nil {
				group = &topicCandidate{key: groupKey, priority: -100, oldest: item.CreatedAt}
				groups[groupKey] = group
			}
			if item.State == digest.Delivering || (item.State == digest.Leased && item.LeaseUntil.After(now)) {
				group.active = true
				return nil
			}
			if item.State != digest.Pending && item.State != digest.Leased {
				return nil
			}
			age := now.Sub(item.CreatedAt)
			if age < 0 {
				age = 0
			}
			score := int64(item.Priority) + int64(age/digestAgeStep)
			if len(group.items) == 0 || score > group.score {
				group.score = score
			}
			if item.Priority > group.priority {
				group.priority = item.Priority
			}
			if item.CreatedAt.Before(group.oldest) {
				group.oldest = item.CreatedAt
			}
			group.items = append(group.items, item)
			group.keys = append(group.keys, append([]byte(nil), key...))
			return nil
		}); err != nil {
			return err
		}
		for _, group := range groups {
			if !group.active && len(group.items) > 0 {
				selected = append(selected, group)
			}
		}
		sort.Slice(selected, func(i, j int) bool {
			if selected[i].score != selected[j].score {
				return selected[i].score > selected[j].score
			}
			if !selected[i].oldest.Equal(selected[j].oldest) {
				return selected[i].oldest.Before(selected[j].oldest)
			}
			return selected[i].key < selected[j].key
		})
		if len(selected) > limit {
			selected = selected[:limit]
		}
		for _, group := range selected {
			for i := range group.items {
				group.items[i].State = digest.Leased
				group.items[i].LeaseID = leaseID
				group.items[i].LeaseUntil = now.Add(ttl)
				touches[string(group.keys[i])] = group.items[i]
			}
		}
		for key, item := range touches {
			if err := putDigestItem(tx, []byte(key), item); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, nil
	}
	lease := &digest.Lease{ID: leaseID, Briefing: briefing}
	for _, group := range selected {
		topic := digest.Topic{Key: group.key, Priority: group.priority, Items: append([]digest.Item(nil), group.items...)}
		lease.Topics = append(lease.Topics, topic)
		lease.Items = append(lease.Items, group.items...)
	}
	return lease, nil
}

// PurgeRouteAdmissions bounds canonical admission history while retaining at
// least three days for retries. Retention above seven days is capped.
func (s *DigestStore) PurgeRouteAdmissions(ctx context.Context, retention time.Duration) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if retention < 3*24*time.Hour {
		retention = 3 * 24 * time.Hour
	}
	if retention > digestAdmissionRetention {
		retention = digestAdmissionRetention
	}
	cutoff := s.now().In(digestShanghai).Add(-retention).Format("2006-01-02")
	cutoffTime := s.now().UTC().Add(-retention)
	return s.updateDigest(func(tx *bolt.Tx) error {
		admissions := tx.Bucket([]byte(digestRouteAdmissionsBucket))
		quarantine := tx.Bucket([]byte(digestRouteQuarantineBucket))
		cursor := admissions.Cursor()
		for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
			var record routeAdmissionRecord
			if decodeErr := json.Unmarshal(raw, &record); decodeErr != nil {
				audit := routeAdmissionQuarantineRecord{
					QuarantinedAt: s.now().UTC(), OriginalKey: append([]byte(nil), key...),
					Raw: append([]byte(nil), raw...), DecodeError: decodeErr.Error(),
				}
				if err := putJSON(quarantine, routeQuarantineKey(audit.QuarantinedAt, key, raw), audit); err != nil {
					return err
				}
				if err := cursor.Delete(); err != nil {
					return err
				}
				continue
			}
			remove := record.Date < cutoff
			if record.Lane == digest.RouteDirect {
				if record.CompletedAt.IsZero() {
					continue
				}
				remove = record.CreatedAt.Before(cutoffTime)
				if record.CreatedAt.IsZero() {
					remove = record.Date < cutoff
				}
			}
			if remove {
				if err := cursor.Delete(); err != nil {
					return err
				}
			}
		}
		if err := pruneRouteQuarantine(quarantine, s.now().UTC()); err != nil {
			return err
		}
		quotas := tx.Bucket([]byte(digestRouteQuotaBucket))
		cursor = quotas.Cursor()
		for key, _ := cursor.First(); key != nil; key, _ = cursor.Next() {
			date, _, _ := strings.Cut(string(key), "\x00")
			if date >= cutoff {
				break
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
		}
		return nil
	})
}

func routeQuarantineKey(at time.Time, originalKey, raw []byte) []byte {
	key := make([]byte, 8+sha256.Size)
	binary.BigEndian.PutUint64(key[:8], uint64(at.UnixNano()))
	hash := sha256.New()
	_, _ = hash.Write(originalKey)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(raw)
	copy(key[8:], hash.Sum(nil))
	return key
}

func pruneRouteQuarantine(bucket *bolt.Bucket, now time.Time) error {
	cutoff := now.Add(-digestQuarantineRetention).UnixNano()
	cursor := bucket.Cursor()
	for key, _ := cursor.First(); key != nil; key, _ = cursor.Next() {
		if len(key) < 8 || int64(binary.BigEndian.Uint64(key[:8])) < cutoff {
			if err := cursor.Delete(); err != nil {
				return err
			}
			continue
		}
		break
	}
	count := 0
	if err := bucket.ForEach(func(_, _ []byte) error {
		count++
		return nil
	}); err != nil {
		return err
	}
	for excess := count - digestQuarantineMaxRecords; excess > 0; excess-- {
		key, _ := cursor.First()
		if key == nil {
			break
		}
		if err := cursor.Delete(); err != nil {
			return err
		}
	}
	return nil
}
