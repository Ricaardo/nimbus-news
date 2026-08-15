package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/core"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
	bolt "go.etcd.io/bbolt"
)

func TestDigestAdminValidationPrecedesAvailability(t *testing.T) {
	server := newConfigTestServer(t)
	tests := []struct {
		path string
	}{
		{"/api/digest/items?briefing=invalid"},
		{"/api/digest/items?state=invalid"},
		{"/api/digest/items?limit=0"},
		{"/api/digest/items?limit=1001"},
		{"/api/digest/deliveries?state=invalid"},
		{"/api/digest/deliveries?sink=unsafe%2Fsink"},
	}
	for _, tt := range tests {
		assertConfigStatus(t, server, http.MethodGet, tt.path, "", http.StatusBadRequest)
	}
}

func TestSanitizeDigestDeliveryRemovesClaimInternals(t *testing.T) {
	delivery := &digest.Delivery{Sinks: map[string]digest.SinkCheckpoint{
		"sink": {Sink: "sink", AttemptToken: "secret-token", ClaimedUntil: time.Now(), LastError: "secret"},
	}}
	sanitizeDigestDelivery(delivery)
	checkpoint := delivery.Sinks["sink"]
	if checkpoint.AttemptToken != "" || !checkpoint.ClaimedUntil.IsZero() || checkpoint.LastError != "" {
		t.Fatalf("claim internals leaked: %+v", checkpoint)
	}
}

func TestSanitizeDigestItemRemovesLeaseInternals(t *testing.T) {
	item := &digest.Item{LeaseID: "secret-lease", LeaseUntil: time.Now()}
	sanitizeDigestItem(item)
	if item.LeaseID != "" || !item.LeaseUntil.IsZero() {
		t.Fatalf("lease internals leaked: %+v", item)
	}
}

func TestSanitizeDigestMetadataRecursively(t *testing.T) {
	message := &model.Message{Metadata: map[string]interface{}{
		"safe": "visible",
		"nested": map[string]interface{}{
			"api_token": "hidden",
			"safe": []interface{}{
				map[string]interface{}{"password": "hidden", "value": "visible"},
				[]interface{}{map[string]string{"secret_key": "hidden", "label": "visible"}},
				[]map[string]interface{}{{"access_token": "hidden", "name": "visible"}},
			},
		},
	}}
	sanitizeDigestMessage(message)
	nested := message.Metadata["nested"].(map[string]interface{})
	if _, ok := nested["api_token"]; ok {
		t.Fatal("nested token was not redacted")
	}
	items := nested["safe"].([]interface{})
	first := items[0].(map[string]interface{})
	deep := items[1].([]interface{})[0].(map[string]string)
	typedSlice := items[2].([]map[string]interface{})[0]
	if _, ok := first["password"]; ok {
		t.Fatal("password in nested slice was not redacted")
	}
	if _, ok := deep["secret_key"]; ok {
		t.Fatal("key in deeply nested typed map was not redacted")
	}
	if _, ok := typedSlice["access_token"]; ok {
		t.Fatal("token in typed map slice was not redacted")
	}
	if message.Metadata["safe"] != "visible" || first["value"] != "visible" ||
		deep["label"] != "visible" || typedSlice["name"] != "visible" {
		t.Fatal("safe metadata was removed")
	}
}

func TestDigestAdminRejectsNonLoopbackButHealthRemainsPublic(t *testing.T) {
	server := newConfigTestServer(t)
	for _, path := range []string{"/api/digest/stats", "/api/digest/items"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "192.0.2.10:12345"
		rec := httptest.NewRecorder()
		server.GetRouter().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d want=%d", path, rec.Code, http.StatusForbidden)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.RemoteAddr = "192.0.2.10:12345"
	rec := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status=%d want=%d", rec.Code, http.StatusOK)
	}
}

func TestDigestAdminRejectsLoopbackBrowserOriginWithoutCORSAccess(t *testing.T) {
	server := newConfigTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/digest/stats", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	server.GetRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusForbidden)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("browser origin received CORS access: %q", got)
	}
}

func TestDigestAdminUnavailable(t *testing.T) {
	server := newConfigTestServer(t)
	assertConfigStatus(t, server, http.MethodGet, "/api/digest/stats", "", http.StatusServiceUnavailable)
	assertConfigStatus(t, server, http.MethodGet, "/api/digest/items", "", http.StatusServiceUnavailable)
	assertConfigStatus(t, server, http.MethodGet, "/api/digest/deliveries", "", http.StatusServiceUnavailable)
	assertConfigStatus(t, server, http.MethodPost, "/api/digest/deliveries/id/retry", "", http.StatusServiceUnavailable)
}

func TestTriggerSourceMapsStoppedEngineToServiceUnavailable(t *testing.T) {
	engine, err := core.NewNewsEngine(core.NewsEngineConfig{})
	if err != nil {
		t.Fatal(err)
	}
	server := newConfigTestServer(t)
	server.SetNewsEngine(engine)

	assertConfigStatus(t, server, http.MethodPost, "/api/sources/existing/trigger", "", http.StatusServiceUnavailable)

	engine.Start(context.Background())
	defer engine.Stop()
	assertConfigStatus(t, server, http.MethodPost, "/api/sources/missing/trigger", "", http.StatusNotFound)
}

func TestDigestDeliveryHealthAgesIntakeItems(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		stats digest.Stats
	}{
		{name: "pending", stats: digest.Stats{OldestPendingItemAt: now.Add(-21 * time.Hour)}},
		{name: "leased", stats: digest.Stats{OldestLeasedItemAt: now.Add(-21 * time.Hour)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, httpStatus, _ := digestDeliveryHealth(core.DigestDeliverySnapshot{
				Available:          true,
				Stats:              tt.stats,
				OldestPendingSince: now.Add(-21 * time.Hour),
			}, now)
			if status != "degraded" || httpStatus != http.StatusOK {
				t.Fatalf("status=%q http=%d, want degraded/%d", status, httpStatus, http.StatusOK)
			}
		})
	}
}

func TestDigestDeliveryHealthAgesPreparedDelivery(t *testing.T) {
	now := time.Now()
	status, httpStatus, reason := digestDeliveryHealth(core.DigestDeliverySnapshot{
		Available: true,
		Stats: digest.Stats{
			OldestPendingDeliveryAt: now.Add(-21 * time.Hour),
		},
	}, now)
	if status != "unhealthy" || httpStatus != http.StatusServiceUnavailable || reason != "pending_backlog" {
		t.Fatalf("status=%q http=%d reason=%q", status, httpStatus, reason)
	}
}

func TestDigestDeliveryHealthDoesNotClassifyAggregatePendingAge(t *testing.T) {
	now := time.Now()
	status, httpStatus, reason := digestDeliveryHealth(core.DigestDeliverySnapshot{
		Available:          true,
		OldestPendingSince: now.Add(-21 * time.Hour),
	}, now)
	if status != "ok" || httpStatus != http.StatusOK || reason != "" {
		t.Fatalf("status=%q http=%d reason=%q", status, httpStatus, reason)
	}
}

func TestDigestDeliveryHealthUsesWorstRequiredSink(t *testing.T) {
	now := time.Now()
	snapshot := core.DigestDeliverySnapshot{
		Available: true,
		Sinks: map[string]core.DigestSinkSnapshot{
			"healthy": {LastSuccess: now},
			"stuck": {
				Pending:            1,
				OldestPendingSince: now.Add(-21 * time.Hour),
			},
		},
	}
	status, httpStatus, reason := digestDeliveryHealth(snapshot, now)
	if status != "unhealthy" || httpStatus != http.StatusServiceUnavailable || reason != "sink_pending_backlog" {
		t.Fatalf("status=%q http=%d reason=%q", status, httpStatus, reason)
	}
}

func TestDigestDeliveryHealthRecentTerminalLossWindow(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name       string
		update     func(*core.DigestDeliverySnapshot, time.Time)
		reason     string
		wantStatus string
		wantHTTP   int
	}{
		{name: "item", reason: "item_expired", wantStatus: "degraded", wantHTTP: http.StatusOK, update: func(s *core.DigestDeliverySnapshot, at time.Time) { s.LastItemExpiry = at }},
		{name: "deadline", reason: "delivery_deadline_expired", wantStatus: "unhealthy", wantHTTP: http.StatusServiceUnavailable, update: func(s *core.DigestDeliverySnapshot, at time.Time) { s.LastDeadlineExpiry = at }},
		{name: "max attempts", reason: "delivery_max_attempts", wantStatus: "unhealthy", wantHTTP: http.StatusServiceUnavailable, update: func(s *core.DigestDeliverySnapshot, at time.Time) { s.LastMaxAttemptExpiry = at }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := core.DigestDeliverySnapshot{Available: true}
			terminalAt := now.Add(-time.Hour)
			tt.update(&snapshot, terminalAt)
			status, httpStatus, reason := digestDeliveryHealth(snapshot, now)
			if status != tt.wantStatus || httpStatus != tt.wantHTTP || reason != tt.reason {
				t.Fatalf("recent status=%q http=%d reason=%q", status, httpStatus, reason)
			}
			status, httpStatus, reason = digestDeliveryHealth(snapshot, terminalAt.Add(digestTerminalHealthWindow))
			if status != tt.wantStatus || httpStatus != tt.wantHTTP || reason != tt.reason {
				t.Fatalf("boundary status=%q http=%d reason=%q", status, httpStatus, reason)
			}
			status, httpStatus, reason = digestDeliveryHealth(snapshot, terminalAt.Add(digestTerminalHealthWindow+time.Nanosecond))
			if status != "ok" || httpStatus != http.StatusOK || reason != "" {
				t.Fatalf("recovered status=%q http=%d reason=%q", status, httpStatus, reason)
			}
		})
	}
}

func TestDigestHealthWithRealStorePendingAndExpiry(t *testing.T) {
	owner, err := store.NewBoltStore(filepath.Join(t.TempDir(), "digest-health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	digests, err := store.NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := digests.Enqueue(context.Background(), "health", digest.Closing, &model.Message{ID: "health"}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	setPendingDigestItemTimes(t, owner.DB(), now.Add(-13*time.Hour), now.Add(11*time.Hour))
	router := core.NewRouter(channel.NewManager())
	router.SetDigestStore(digests)
	engine, err := core.NewNewsEngine(core.NewsEngineConfig{Router: router, DigestStore: digests})
	if err != nil {
		t.Fatal(err)
	}
	router.SetNewsEngine(engine)
	server := newConfigTestServer(t)
	server.SetNewsEngine(engine)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := router.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer router.Stop()

	waitForDigestHealth(t, server, "degraded", http.StatusOK, "pending_backlog")

	setPendingDigestItemTimes(t, owner.DB(), now.Add(-21*time.Hour), now.Add(3*time.Hour))
	engine.WakeDigestDelivery()
	waitForDigestHealth(t, server, "degraded", http.StatusOK, "pending_backlog")

	setPendingDigestItemTimes(t, owner.DB(), now.Add(-25*time.Hour), now.Add(-time.Minute))
	engine.WakeDigestDelivery()
	waitForDigestHealth(t, server, "degraded", http.StatusOK, "item_expired")

	var snapshot core.DigestDeliverySnapshot
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot = engine.GetDigestDeliverySnapshot()
		if snapshot.OldestPendingSince.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !snapshot.OldestPendingSince.IsZero() {
		t.Fatalf("expired item remained in pending snapshot: %v", snapshot.OldestPendingSince)
	}

	if err := digests.Enqueue(context.Background(), "delivery-health", digest.Closing,
		&model.Message{ID: "delivery-health"}); err != nil {
		t.Fatal(err)
	}
	lease, err := digests.Lease(context.Background(), digest.Closing, 1, time.Minute)
	if err != nil || lease == nil {
		t.Fatalf("lease=%+v err=%v", lease, err)
	}
	if _, err := digests.PrepareDelivery(context.Background(), lease.ID, &model.Message{ID: "delivery-health"},
		[]string{"sink"}, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	setDigestDeliveryAtMaxAttempts(t, owner.DB())
	engine.WakeDigestDelivery()
	waitForDigestHealth(t, server, "unhealthy", http.StatusServiceUnavailable, "delivery_max_attempts")

	snapshot = engine.GetDigestDeliverySnapshot()
	status, httpStatus, reason := digestDeliveryHealth(snapshot, time.Now().Add(25*time.Hour))
	if status != "ok" || httpStatus != http.StatusOK || reason != "" {
		t.Fatalf("expired item health did not recover: status=%q http=%d reason=%q", status, httpStatus, reason)
	}
}

func TestDigestHealthReconstructsTerminalLossAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "digest-restart-health.db")
	owner, err := store.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	digests, err := store.NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := digests.Enqueue(context.Background(), "item-expiry", digest.Closing,
		&model.Message{ID: "item-expiry"}); err != nil {
		t.Fatal(err)
	}
	setPendingDigestItemTimes(t, owner.DB(), time.Now().Add(-25*time.Hour), time.Now().Add(-time.Minute))
	if result, err := digests.ExpireDue(context.Background()); err != nil || result.Items != 1 {
		t.Fatalf("item expiry=%+v err=%v", result, err)
	}

	prepareHealthDelivery(t, digests, "deadline")
	setPendingDigestDeliveryDeadline(t, owner.DB(), time.Now().Add(-time.Minute))
	if result, err := digests.ExpireDue(context.Background()); err != nil || result.DeadlineDeliveries != 1 {
		t.Fatalf("deadline expiry=%+v err=%v", result, err)
	}

	prepareHealthDelivery(t, digests, "max-attempts")
	setDigestDeliveryAtMaxAttempts(t, owner.DB())
	if result, err := digests.ExpireDue(context.Background()); err != nil || result.MaxAttemptDeliveries != 1 {
		t.Fatalf("max-attempt expiry=%+v err=%v", result, err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedDigests, err := store.NewDigestStore(reopened.DB())
	if err != nil {
		t.Fatal(err)
	}
	router := core.NewRouter(channel.NewManager())
	router.SetDigestStore(reopenedDigests)
	engine, err := core.NewNewsEngine(core.NewsEngineConfig{Router: router, DigestStore: reopenedDigests})
	if err != nil {
		t.Fatal(err)
	}
	router.SetNewsEngine(engine)
	server := newConfigTestServer(t)
	server.SetNewsEngine(engine)
	if err := router.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer router.Stop()

	waitForDigestHealth(t, server, "unhealthy", http.StatusServiceUnavailable, "delivery_deadline_expired")
	var snapshot core.DigestDeliverySnapshot
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot = engine.GetDigestDeliverySnapshot()
		if !snapshot.LastItemExpiry.IsZero() && !snapshot.LastDeadlineExpiry.IsZero() &&
			!snapshot.LastMaxAttemptExpiry.IsZero() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot.LastItemExpiry.IsZero() || snapshot.LastDeadlineExpiry.IsZero() ||
		snapshot.LastMaxAttemptExpiry.IsZero() {
		t.Fatalf("durable terminal timestamps not reconstructed: %+v", snapshot)
	}

	now := time.Now()
	for _, tt := range []struct {
		name   string
		at     time.Time
		reason string
	}{
		{name: "item", at: snapshot.LastItemExpiry, reason: "item_expired"},
		{name: "deadline", at: snapshot.LastDeadlineExpiry, reason: "delivery_deadline_expired"},
		{name: "max attempts", at: snapshot.LastMaxAttemptExpiry, reason: "delivery_max_attempts"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			isolated := core.DigestDeliverySnapshot{Available: true}
			switch tt.reason {
			case "item_expired":
				isolated.LastItemExpiry = tt.at
			case "delivery_deadline_expired":
				isolated.LastDeadlineExpiry = tt.at
			case "delivery_max_attempts":
				isolated.LastMaxAttemptExpiry = tt.at
			}
			status, httpStatus, reason := digestDeliveryHealth(isolated, now)
			wantStatus := "unhealthy"
			wantHTTP := http.StatusServiceUnavailable
			if tt.reason == "item_expired" {
				wantStatus = "degraded"
				wantHTTP = http.StatusOK
			}
			if status != wantStatus || httpStatus != wantHTTP || reason != tt.reason {
				t.Fatalf("recent terminal status=%q http=%d reason=%q", status, httpStatus, reason)
			}
			status, httpStatus, reason = digestDeliveryHealth(isolated, tt.at.Add(digestTerminalHealthWindow+time.Nanosecond))
			if status != "ok" || httpStatus != http.StatusOK || reason != "" {
				t.Fatalf("old terminal remained sticky: status=%q http=%d reason=%q", status, httpStatus, reason)
			}
		})
	}
}

func prepareHealthDelivery(t *testing.T, digests *store.DigestStore, id string) {
	t.Helper()
	if err := digests.Enqueue(context.Background(), id, digest.Closing, &model.Message{ID: id}); err != nil {
		t.Fatal(err)
	}
	lease, err := digests.Lease(context.Background(), digest.Closing, 1, time.Minute)
	if err != nil || lease == nil {
		t.Fatalf("lease=%+v err=%v", lease, err)
	}
	if _, err := digests.PrepareDelivery(context.Background(), lease.ID, &model.Message{ID: "payload-" + id},
		[]string{"sink"}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func setPendingDigestItemTimes(t *testing.T, db *bolt.DB, createdAt, expiresAt time.Time) {
	t.Helper()
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("digest_items_v1"))
		if err := bucket.ForEach(func(key, value []byte) error {
			var item digest.Item
			if err := json.Unmarshal(value, &item); err != nil {
				return err
			}
			if item.State != digest.Pending {
				return nil
			}
			item.CreatedAt = createdAt
			item.ExpiresAt = expiresAt
			raw, err := json.Marshal(item)
			if err != nil {
				return err
			}
			return bucket.Put(key, raw)
		}); err != nil {
			return err
		}
		return bumpDigestGeneration(tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func setDigestDeliveryAtMaxAttempts(t *testing.T, db *bolt.DB) {
	t.Helper()
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("digest_deliveries_v1"))
		if err := bucket.ForEach(func(key, value []byte) error {
			var delivery digest.Delivery
			if err := json.Unmarshal(value, &delivery); err != nil {
				return err
			}
			if delivery.State != digest.DeliveryPending {
				return nil
			}
			for sink, checkpoint := range delivery.Sinks {
				checkpoint.Attempts = 10
				checkpoint.ClaimedUntil = time.Time{}
				delivery.Sinks[sink] = checkpoint
			}
			raw, err := json.Marshal(delivery)
			if err != nil {
				return err
			}
			return bucket.Put(key, raw)
		}); err != nil {
			return err
		}
		return bumpDigestGeneration(tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func setPendingDigestDeliveryDeadline(t *testing.T, db *bolt.DB, deadline time.Time) {
	t.Helper()
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("digest_deliveries_v1"))
		if err := bucket.ForEach(func(key, value []byte) error {
			var delivery digest.Delivery
			if err := json.Unmarshal(value, &delivery); err != nil {
				return err
			}
			if delivery.State != digest.DeliveryPending {
				return nil
			}
			delivery.Deadline = deadline
			raw, err := json.Marshal(delivery)
			if err != nil {
				return err
			}
			return bucket.Put(key, raw)
		}); err != nil {
			return err
		}
		return bumpDigestGeneration(tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func bumpDigestGeneration(tx *bolt.Tx) error {
	meta := tx.Bucket([]byte("digest_index_meta_v1"))
	if meta == nil {
		return fmt.Errorf("digest metadata bucket missing")
	}
	raw := meta.Get([]byte("canonical_generation"))
	if len(raw) != 8 {
		return fmt.Errorf("digest canonical generation is invalid: %x", raw)
	}
	generation := binary.BigEndian.Uint64(raw)
	next := make([]byte, 8)
	binary.BigEndian.PutUint64(next, generation+1)
	return meta.Put([]byte("canonical_generation"), next)
}

func waitForDigestHealth(t *testing.T, server *Server, wantStatus string, wantHTTP int, wantReason string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		rec := httptest.NewRecorder()
		server.GetRouter().ServeHTTP(rec, req)
		var payload struct {
			Status         string `json:"status"`
			DigestDelivery struct {
				Reason string `json:"reason"`
			} `json:"digest_delivery"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err == nil &&
			rec.Code == wantHTTP && payload.Status == wantStatus &&
			payload.DigestDelivery.Reason == wantReason {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("digest health did not reach status=%q http=%d reason=%q", wantStatus, wantHTTP, wantReason)
}
