package store

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

func TestNewsStore_Save(t *testing.T) {
	store := NewNewsStore(NewsStoreConfig{MaxItems: 100})
	ctx := context.Background()

	news := &model.News{
		ID:        "test-1",
		Title:     "Test News",
		Content:   "Test Content",
		FetchTime: time.Now(),
	}

	err := store.Save(ctx, news)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if store.Count() != 1 {
		t.Errorf("expected count 1, got %d", store.Count())
	}
}

func TestNewsStore_Save_Duplicate(t *testing.T) {
	store := NewNewsStore(NewsStoreConfig{MaxItems: 100})
	ctx := context.Background()

	news := &model.News{
		ID:        "test-1",
		Title:     "Test News",
		FetchTime: time.Now(),
	}

	store.Save(ctx, news)
	store.Save(ctx, news) // 重复保存

	if store.Count() != 1 {
		t.Errorf("expected count 1 after duplicate save, got %d", store.Count())
	}
}

func TestNewsStore_GetByID(t *testing.T) {
	store := NewNewsStore(NewsStoreConfig{MaxItems: 100})
	ctx := context.Background()

	news := &model.News{
		ID:        "test-1",
		Title:     "Test News",
		FetchTime: time.Now(),
	}

	store.Save(ctx, news)

	found := store.GetByID(ctx, "test-1")
	if found == nil {
		t.Error("expected to find news")
	}
	if found.Title != "Test News" {
		t.Errorf("expected title 'Test News', got '%s'", found.Title)
	}

	notFound := store.GetByID(ctx, "not-exists")
	if notFound != nil {
		t.Error("expected nil for non-existent ID")
	}
}

func TestNewsStore_GetRecent(t *testing.T) {
	store := NewNewsStore(NewsStoreConfig{MaxItems: 100})
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		store.Save(ctx, &model.News{
			ID:        string(rune('0' + i)),
			Title:     "News " + string(rune('0'+i)),
			FetchTime: time.Now(),
		})
	}

	recent := store.GetRecent(ctx, 3)
	if len(recent) != 3 {
		t.Errorf("expected 3 items, got %d", len(recent))
	}

	// 最新的应该在前面
	if recent[0].ID != "5" {
		t.Errorf("expected newest ID '5', got '%s'", recent[0].ID)
	}
}

func TestNewsStore_GetRecentOrdersByFetchTime(t *testing.T) {
	ctx := context.Background()
	store := NewNewsStore(NewsStoreConfig{MaxItems: 3})
	now := time.Now()
	for _, news := range []*model.News{
		{ID: "new", FetchTime: now.Add(2 * time.Minute)},
		{ID: "old", FetchTime: now},
		{ID: "middle", FetchTime: now.Add(time.Minute)},
	} {
		if err := store.Save(ctx, news); err != nil {
			t.Fatal(err)
		}
	}

	got := store.GetRecent(ctx, 3)
	if got[0].ID != "new" || got[1].ID != "middle" || got[2].ID != "old" {
		t.Fatalf("recent order=%v", []string{got[0].ID, got[1].ID, got[2].ID})
	}
}

func TestNewsStore_GetRecentUsesStableIDOrderForEqualFetchTime(t *testing.T) {
	ctx := context.Background()
	store := NewNewsStore(NewsStoreConfig{MaxItems: 3})
	fetchTime := time.Now()
	for _, id := range []string{"b", "c", "a"} {
		if err := store.Save(ctx, &model.News{ID: id, FetchTime: fetchTime}); err != nil {
			t.Fatal(err)
		}
	}
	assertNewsOrder(t, store.GetRecent(ctx, 3), "c", "b", "a")
}

func TestNewsStoreConstructorClampsUnsafeCapacity(t *testing.T) {
	for _, maxItems := range []int{0, -1} {
		store := NewNewsStore(NewsStoreConfig{MaxItems: maxItems})
		if store.maxItems != defaultNewsStoreMaxItems || cap(store.recent) != defaultNewsStoreMaxItems {
			t.Fatalf("max_items=%d produced max=%d capacity=%d", maxItems, store.maxItems, cap(store.recent))
		}
	}

	store := NewNewsStore(NewsStoreConfig{MaxItems: maxNewsStoreMaxItems + 1})
	if store.maxItems != maxNewsStoreMaxItems || cap(store.recent) != maxNewsStoreMaxItems {
		t.Fatalf("oversized config produced max=%d capacity=%d", store.maxItems, cap(store.recent))
	}
}

func TestNewsStoreSaveAtLargeCapacityReusesBoundedRecentView(t *testing.T) {
	ctx := context.Background()
	store := NewNewsStore(NewsStoreConfig{MaxItems: maxNewsStoreMaxItems, TTL: time.Hour})
	now := time.Now()
	for i := 0; i < maxNewsStoreMaxItems; i++ {
		if err := store.Save(ctx, &model.News{
			ID:        fmt.Sprintf("%05d", i),
			FetchTime: now.Add(time.Duration(i) * time.Nanosecond),
		}); err != nil {
			t.Fatal(err)
		}
	}
	backing := &store.recent[0]
	if err := store.Save(ctx, &model.News{
		ID:        "newest",
		FetchTime: now.Add(maxNewsStoreMaxItems * time.Nanosecond),
	}); err != nil {
		t.Fatal(err)
	}
	if &store.recent[0] != backing {
		t.Fatal("save at capacity replaced the recent view backing array")
	}
	if store.Count() != maxNewsStoreMaxItems || store.GetByID(ctx, "00000") != nil {
		t.Fatalf("capacity eviction failed: count=%d", store.Count())
	}
}

func TestNewsStore_Search(t *testing.T) {
	store := NewNewsStore(NewsStoreConfig{MaxItems: 100})
	ctx := context.Background()

	store.Save(ctx, &model.News{
		ID:        "1",
		Title:     "Bitcoin price rises",
		FetchTime: time.Now(),
	})
	store.Save(ctx, &model.News{
		ID:        "2",
		Title:     "Ethereum update",
		FetchTime: time.Now(),
	})
	store.Save(ctx, &model.News{
		ID:        "3",
		Title:     "Bitcoin mining",
		FetchTime: time.Now(),
	})

	results := store.Search(ctx, "bitcoin", 10)
	if len(results) != 2 {
		t.Errorf("expected 2 results for 'bitcoin', got %d", len(results))
	}

	results = store.Search(ctx, "ethereum", 10)
	if len(results) != 1 {
		t.Errorf("expected 1 result for 'ethereum', got %d", len(results))
	}
}

func TestNewsStore_GetBySource(t *testing.T) {
	store := NewNewsStore(NewsStoreConfig{MaxItems: 100})
	ctx := context.Background()

	store.Save(ctx, &model.News{
		ID:        "1",
		Source:    "Reuters",
		FetchTime: time.Now(),
	})
	store.Save(ctx, &model.News{
		ID:        "2",
		Source:    "Bloomberg",
		FetchTime: time.Now(),
	})
	store.Save(ctx, &model.News{
		ID:        "3",
		Source:    "Reuters",
		FetchTime: time.Now(),
	})

	results := store.GetBySource(ctx, "Reuters", 10)
	if len(results) != 2 {
		t.Errorf("expected 2 results for 'Reuters', got %d", len(results))
	}
}

func TestNewsStore_MaxItems(t *testing.T) {
	store := NewNewsStore(NewsStoreConfig{MaxItems: 3})
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		store.Save(ctx, &model.News{
			ID:        string(rune('0' + i)),
			FetchTime: time.Now(),
		})
	}

	if store.Count() != 3 {
		t.Errorf("expected count 3 (maxItems), got %d", store.Count())
	}

	// 最旧的应该被删除
	if store.GetByID(ctx, "1") != nil {
		t.Error("expected item 1 to be evicted")
	}
	if store.GetByID(ctx, "2") != nil {
		t.Error("expected item 2 to be evicted")
	}

	// 最新的应该保留
	if store.GetByID(ctx, "5") == nil {
		t.Error("expected item 5 to exist")
	}
}

func TestNewsStore_SaveBatch(t *testing.T) {
	store := NewNewsStore(NewsStoreConfig{MaxItems: 100})
	ctx := context.Background()

	newsList := []*model.News{
		{ID: "1", FetchTime: time.Now()},
		{ID: "2", FetchTime: time.Now()},
		{ID: "3", FetchTime: time.Now()},
	}

	err := store.SaveBatch(ctx, newsList)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if store.Count() != 3 {
		t.Errorf("expected count 3, got %d", store.Count())
	}
}

func BenchmarkNewsStore_Save(b *testing.B) {
	store := NewNewsStore(NewsStoreConfig{MaxItems: 1000})
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Save(ctx, &model.News{
			ID:        string(rune(i % 10000)),
			FetchTime: time.Now(),
		})
	}
}

func BenchmarkNewsStore_GetRecent(b *testing.B) {
	store := NewNewsStore(NewsStoreConfig{MaxItems: 1000})
	ctx := context.Background()

	for i := 0; i < 1000; i++ {
		store.Save(ctx, &model.News{
			ID:        string(rune(i)),
			FetchTime: time.Now(),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.GetRecent(ctx, 100)
	}
}

func BenchmarkNewsStore_SaveAtCapacity(b *testing.B) {
	for _, maxItems := range []int{1000, 10_000} {
		b.Run(strconv.Itoa(maxItems), func(b *testing.B) {
			store := NewNewsStore(NewsStoreConfig{MaxItems: maxItems, TTL: time.Hour})
			ctx := context.Background()
			now := time.Now()
			for i := 0; i < maxItems; i++ {
				_ = store.Save(ctx, &model.News{
					ID:        fmt.Sprintf("seed-%05d", i),
					FetchTime: now.Add(time.Duration(i) * time.Nanosecond),
				})
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = store.Save(ctx, &model.News{
					ID:        fmt.Sprintf("save-%09d", i),
					FetchTime: now.Add(time.Duration(maxItems+i) * time.Nanosecond),
				})
			}
		})
	}
}
