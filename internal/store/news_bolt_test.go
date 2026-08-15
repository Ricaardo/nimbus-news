package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
	bolt "go.etcd.io/bbolt"
)

func TestPersistentNewsStoreRestoresAndRebuildsIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	ctx, cancel := context.WithCancel(context.Background())
	owner, err := NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := NewPersistentNewsStoreContext(ctx, owner.DB(), NewsStoreConfig{MaxItems: 3, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, id := range []string{"old", "middle", "new"} {
		if err := ns.Save(ctx, &model.News{
			ID: id, Source: "Reuters", FetchTime: now.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Secondary indexes are disposable: startup reconstructs them from the
	// canonical bucket.
	if err := owner.DB().Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(newsTimeBucketV1); err != nil {
			return err
		}
		return tx.DeleteBucket(newsSourceBucketV1)
	}); err != nil {
		t.Fatal(err)
	}
	cancel()
	ns.WaitCleanup()
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	reopened, err := NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := NewPersistentNewsStoreContext(ctx2, reopened.DB(), NewsStoreConfig{MaxItems: 3, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Count() != 3 || restored.GetRecent(ctx2, 3)[0].ID != "new" {
		t.Fatalf("restored news=%v", restored.GetRecent(ctx2, 3))
	}
	if err := reopened.DB().View(func(tx *bolt.Tx) error {
		if tx.Bucket(newsTimeBucketV1).Stats().KeyN != 3 || tx.Bucket(newsSourceBucketV1).Stats().KeyN != 3 {
			t.Fatalf("indexes not rebuilt")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentNewsStoreRuntimeAndRestartUseFetchTimeOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	ctx, cancel := context.WithCancel(context.Background())
	owner, err := NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := NewPersistentNewsStoreContext(ctx, owner.DB(), NewsStoreConfig{MaxItems: 3, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, news := range []*model.News{
		{ID: "new", FetchTime: now.Add(2 * time.Minute)},
		{ID: "old", FetchTime: now},
		{ID: "middle", FetchTime: now.Add(time.Minute)},
	} {
		if err := ns.Save(ctx, news); err != nil {
			t.Fatal(err)
		}
	}
	assertNewsOrder(t, ns.GetRecent(ctx, 3), "new", "middle", "old")

	cancel()
	ns.WaitCleanup()
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	reopened, err := NewBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := NewPersistentNewsStoreContext(ctx2, reopened.DB(), NewsStoreConfig{MaxItems: 3, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	assertNewsOrder(t, restored.GetRecent(ctx2, 3), "new", "middle", "old")
}

func TestPersistentNewsStoreRejectsUnsafeCapacity(t *testing.T) {
	ctx := context.Background()
	owner, err := NewBoltStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	for _, maxItems := range []int{0, -1, maxNewsStoreMaxItems + 1} {
		if _, err := NewPersistentNewsStoreContext(ctx, owner.DB(), NewsStoreConfig{
			MaxItems: maxItems,
			TTL:      time.Hour,
		}); err == nil {
			t.Fatalf("max_items=%d: expected error", maxItems)
		}
	}
}

func TestPersistentNewsStoreMaintainsIncrementalItemCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner, err := NewBoltStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ns, err := NewPersistentNewsStoreContext(ctx, owner.DB(), NewsStoreConfig{
		MaxItems: 1000,
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 1001; i++ {
		if err := ns.Save(ctx, &model.News{
			ID:        strconv.Itoa(i),
			FetchTime: now.Add(time.Duration(i) * time.Nanosecond),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		count, err := getNewsItemCount(tx)
		if err != nil {
			return err
		}
		if count != 1000 {
			t.Fatalf("metadata count=%d want=1000", count)
		}
		if got := tx.Bucket(newsItemsBucketV1).Stats().KeyN; got != count {
			t.Fatalf("canonical count=%d metadata=%d", got, count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentNewsStoreTTLAndCapacityApplyToCanonicalRecords(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner, err := NewBoltStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ns, err := NewPersistentNewsStoreContext(ctx, owner.DB(), NewsStoreConfig{MaxItems: 2, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := ns.Save(ctx, &model.News{ID: "expired", FetchTime: now.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"one", "two", "three"} {
		if err := ns.Save(ctx, &model.News{ID: id, FetchTime: now.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if ns.Count() != 2 || ns.GetByID(ctx, "expired") != nil || ns.GetByID(ctx, "one") != nil {
		t.Fatalf("memory retained wrong records: count=%d expired=%v one=%v records=%s",
			ns.Count(), ns.GetByID(ctx, "expired") != nil, ns.GetByID(ctx, "one") != nil, mustNewsJSON(t, ns))
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		items := tx.Bucket(newsItemsBucketV1)
		if items.Stats().KeyN != 2 || items.Get([]byte("two")) == nil || items.Get([]byte("three")) == nil {
			t.Fatalf("canonical records do not enforce ttl/capacity")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentNewsStoreSaveFailureDoesNotMutateMemory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	owner, err := NewBoltStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	ns, err := NewPersistentNewsStoreContext(ctx, owner.DB(), NewsStoreConfig{MaxItems: 10, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := ns.Save(ctx, &model.News{ID: "committed", FetchTime: time.Now()}); err != nil {
		t.Fatal(err)
	}
	cancel()
	ns.WaitCleanup()
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ns.Save(context.Background(), &model.News{ID: "failed", FetchTime: time.Now()}); err == nil {
		t.Fatal("expected closed database write to fail")
	}
	stats := ns.WriteStats()
	if !stats.Persistent || stats.WriteFailures != 1 || stats.LastWriteError == "" ||
		stats.LastWriteFailed.IsZero() || stats.LastWriteSuccess.IsZero() {
		t.Fatalf("write stats=%+v", stats)
	}
	if ns.Count() != 1 || ns.GetByID(context.Background(), "failed") != nil {
		t.Fatalf("failed write mutated memory: %s", mustNewsJSON(t, ns))
	}
}

func TestPersistentNewsStoreConcurrentCapacityStaysConsistent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner, err := NewBoltStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ns, err := NewPersistentNewsStoreContext(ctx, owner.DB(), NewsStoreConfig{MaxItems: 20, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ns.Save(ctx, &model.News{
				ID:        strconv.Itoa(i),
				FetchTime: now.Add(time.Duration(i) * time.Nanosecond),
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ns.Count() != 20 {
		t.Fatalf("memory count=%d", ns.Count())
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		items := tx.Bucket(newsItemsBucketV1)
		if items.Stats().KeyN != 20 {
			t.Fatalf("canonical count=%d", items.Stats().KeyN)
		}
		for _, news := range ns.GetRecent(ctx, 20) {
			if items.Get([]byte(news.ID)) == nil {
				t.Fatalf("memory-only record %q", news.ID)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentNewsStoreIncludedInOwnerSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner, err := NewBoltStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ns, err := NewPersistentNewsStoreContext(ctx, owner.DB(), NewsStoreConfig{MaxItems: 10, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := ns.Save(ctx, &model.News{ID: "snapshotted", Source: "Fed", FetchTime: time.Now()}); err != nil {
		t.Fatal(err)
	}

	var snapshot bytes.Buffer
	if _, err := owner.Snapshot(ctx, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.db")
	if err := os.WriteFile(snapshotPath, snapshot.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	copyOwner, err := NewBoltStore(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	defer copyOwner.Close()
	copyCtx, copyCancel := context.WithCancel(context.Background())
	defer copyCancel()
	restored, err := NewPersistentNewsStoreContext(copyCtx, copyOwner.DB(), NewsStoreConfig{MaxItems: 10, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if restored.GetByID(copyCtx, "snapshotted") == nil {
		t.Fatal("snapshot did not include canonical news")
	}
}

func mustNewsJSON(t *testing.T, ns *NewsStore) string {
	t.Helper()
	raw, err := ns.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertNewsOrder(t *testing.T, got []*model.News, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("news count=%d want=%d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("news[%d]=%q want=%q", i, got[i].ID, id)
		}
	}
}
