package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	bolt "go.etcd.io/bbolt"
)

func TestCleanupOnlyTouchesExpiryBuckets(t *testing.T) {
	s, err := NewBoltStore(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Set("dedup", "expired", -time.Hour); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"state": "pending"})
	if err := s.DB().Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("json_future_v1"))
		if err != nil {
			return err
		}
		return b.Put([]byte("item"), payload)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(); err != nil {
		t.Fatal(err)
	}
	exists, err := s.Exists("dedup", "expired")
	if err != nil || exists {
		t.Fatalf("expired key: exists=%v err=%v", exists, err)
	}
	if err := s.DB().View(func(tx *bolt.Tx) error {
		if got := tx.Bucket([]byte("json_future_v1")).Get([]byte("item")); string(got) != string(payload) {
			t.Fatalf("json payload changed: %s", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDigestStoreLeaseAckRestartAndExpiry(t *testing.T) {
	owner, err := NewBoltStore(filepath.Join(t.TempDir(), "digest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ds, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds.now = func() time.Time { return now }
	msg := &model.Message{ID: "m1", Title: "Alpha"}
	if err := ds.Enqueue(context.Background(), "rss", digest.PreMarket, msg); err != nil {
		t.Fatal(err)
	}
	if err := ds.Enqueue(context.Background(), "rss", digest.PreMarket, msg); err != nil {
		t.Fatal(err)
	}
	lease, err := ds.Lease(context.Background(), digest.PreMarket, 10, time.Minute)
	if err != nil || lease == nil || len(lease.Items) != 1 {
		t.Fatalf("lease=%+v err=%v", lease, err)
	}
	reopened, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = func() time.Time { return now.Add(30 * time.Second) }
	if got, err := reopened.Lease(context.Background(), digest.PreMarket, 10, time.Minute); err != nil || got != nil {
		t.Fatalf("active lease should not be reclaimed: lease=%+v err=%v", got, err)
	}
	reopened.now = func() time.Time { return now.Add(30 * time.Second) }
	if err := reopened.Ack(context.Background(), lease.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.Lease(context.Background(), digest.PreMarket, 10, time.Minute); err != nil || got != nil {
		t.Fatalf("acked item returned: lease=%+v err=%v", got, err)
	}

	msg2 := &model.Message{ID: "m2", Title: "Beta"}
	if err := reopened.Enqueue(context.Background(), "rss", digest.Closing, msg2); err != nil {
		t.Fatal(err)
	}
	first, err := reopened.Lease(context.Background(), digest.Closing, 10, time.Minute)
	if err != nil || first == nil {
		t.Fatalf("first lease=%+v err=%v", first, err)
	}
	reopened.now = func() time.Time { return now.Add(20 * time.Minute) }
	second, err := reopened.Lease(context.Background(), digest.Closing, 10, time.Minute)
	if err != nil || second == nil || second.ID == first.ID {
		t.Fatalf("expired lease not reclaimed: first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestDigestStoreConcurrentLeasesAcrossInstancesAreDisjoint(t *testing.T) {
	owner, err := NewBoltStore(filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	first, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		msg := &model.Message{ID: fmt.Sprintf("m-%02d", i), Title: "item"}
		if err := first.Enqueue(context.Background(), "wire", digest.USPreview, msg); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	results := make(chan *digest.Lease, 2)
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for _, ds := range []*DigestStore{first, second} {
		wait.Add(1)
		go func(ds *DigestStore) {
			defer wait.Done()
			<-start
			lease, err := ds.Lease(context.Background(), digest.USPreview, 10, time.Hour)
			results <- lease
			errors <- err
		}(ds)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]struct{})
	total := 0
	for lease := range results {
		if lease == nil {
			t.Fatal("concurrent lease unexpectedly empty")
		}
		for _, item := range lease.Items {
			if _, duplicate := seen[item.ID]; duplicate {
				t.Fatalf("item leased twice: %s", item.ID)
			}
			seen[item.ID] = struct{}{}
			total++
		}
	}
	if total != 20 {
		t.Fatalf("leased %d items, want 20", total)
	}
}

func TestDigestStoreStaleAckCannotAckReclaimedLease(t *testing.T) {
	owner, err := NewBoltStore(filepath.Join(t.TempDir(), "stale-ack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ds, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	ds.now = func() time.Time { return now }
	if err := ds.Enqueue(context.Background(), "wire", digest.Closing, &model.Message{ID: "m"}); err != nil {
		t.Fatal(err)
	}
	oldLease, err := ds.Lease(context.Background(), digest.Closing, 1, time.Minute)
	if err != nil || oldLease == nil {
		t.Fatalf("old lease=%+v err=%v", oldLease, err)
	}
	now = now.Add(2 * time.Minute)
	newLease, err := ds.Lease(context.Background(), digest.Closing, 1, time.Hour)
	if err != nil || newLease == nil {
		t.Fatalf("new lease=%+v err=%v", newLease, err)
	}
	if err := ds.Ack(context.Background(), oldLease.ID); err != nil {
		t.Fatal(err)
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(digestBucketV1)).ForEach(func(_, value []byte) error {
			var item digest.Item
			if err := json.Unmarshal(value, &item); err != nil {
				return err
			}
			if item.State != digest.Leased || item.LeaseID != newLease.ID {
				t.Fatalf("stale ack changed reclaimed item: %+v", item)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
}
