package store

import (
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

func newTestNews(id string) *model.News {
	return &model.News{
		ID:        id,
		Title:     "Test News " + id,
		Content:   "Content " + id,
		FetchTime: time.Now(),
	}
}

func TestRingBuffer_Push(t *testing.T) {
	rb := NewRingBuffer(3)

	rb.Push(newTestNews("1"))
	rb.Push(newTestNews("2"))
	rb.Push(newTestNews("3"))

	if rb.Size() != 3 {
		t.Errorf("expected size 3, got %d", rb.Size())
	}

	// 超出容量
	rb.Push(newTestNews("4"))
	if rb.Size() != 3 {
		t.Errorf("expected size 3 after overflow, got %d", rb.Size())
	}
}

func TestRingBuffer_GetRecent(t *testing.T) {
	rb := NewRingBuffer(5)

	for i := 1; i <= 5; i++ {
		rb.Push(newTestNews(string(rune('0' + i))))
	}

	// 获取最近 3 条
	recent := rb.GetRecent(3)
	if len(recent) != 3 {
		t.Errorf("expected 3 items, got %d", len(recent))
	}

	// 最新的应该在前面
	if recent[0].ID != "5" {
		t.Errorf("expected newest item ID '5', got '%s'", recent[0].ID)
	}
	if recent[1].ID != "4" {
		t.Errorf("expected second item ID '4', got '%s'", recent[1].ID)
	}
	if recent[2].ID != "3" {
		t.Errorf("expected third item ID '3', got '%s'", recent[2].ID)
	}
}

func TestRingBuffer_GetRecent_MoreThanSize(t *testing.T) {
	rb := NewRingBuffer(10)

	rb.Push(newTestNews("1"))
	rb.Push(newTestNews("2"))

	// 请求超过实际大小
	recent := rb.GetRecent(100)
	if len(recent) != 2 {
		t.Errorf("expected 2 items, got %d", len(recent))
	}
}

func TestRingBuffer_GetAll(t *testing.T) {
	rb := NewRingBuffer(5)

	for i := 1; i <= 3; i++ {
		rb.Push(newTestNews(string(rune('0' + i))))
	}

	all := rb.GetAll()
	if len(all) != 3 {
		t.Errorf("expected 3 items, got %d", len(all))
	}

	// 验证顺序（最新在前）
	if all[0].ID != "3" || all[1].ID != "2" || all[2].ID != "1" {
		t.Error("items not in expected order")
	}
}

func TestRingBuffer_Overflow(t *testing.T) {
	rb := NewRingBuffer(3)

	// 插入超过容量的元素
	for i := 1; i <= 5; i++ {
		rb.Push(newTestNews(string(rune('0' + i))))
	}

	all := rb.GetAll()
	if len(all) != 3 {
		t.Errorf("expected 3 items after overflow, got %d", len(all))
	}

	// 应该只保留最新的 3 条
	ids := make(map[string]bool)
	for _, n := range all {
		ids[n.ID] = true
	}

	if !ids["5"] || !ids["4"] || !ids["3"] {
		t.Error("expected items 3, 4, 5 to be retained")
	}
	if ids["1"] || ids["2"] {
		t.Error("expected items 1, 2 to be evicted")
	}
}

func TestRingBuffer_ForEach(t *testing.T) {
	rb := NewRingBuffer(5)

	for i := 1; i <= 3; i++ {
		rb.Push(newTestNews(string(rune('0' + i))))
	}

	var visited []string
	rb.ForEach(func(n *model.News) bool {
		visited = append(visited, n.ID)
		return true
	})

	if len(visited) != 3 {
		t.Errorf("expected 3 items visited, got %d", len(visited))
	}

	// 验证顺序
	if visited[0] != "3" || visited[1] != "2" || visited[2] != "1" {
		t.Error("ForEach not in expected order")
	}
}

func TestRingBuffer_ForEach_EarlyStop(t *testing.T) {
	rb := NewRingBuffer(5)

	for i := 1; i <= 5; i++ {
		rb.Push(newTestNews(string(rune('0' + i))))
	}

	count := 0
	rb.ForEach(func(n *model.News) bool {
		count++
		return count < 3 // 只处理前 3 个
	})

	if count != 3 {
		t.Errorf("expected to visit 3 items, visited %d", count)
	}
}

func TestRingBuffer_Clear(t *testing.T) {
	rb := NewRingBuffer(5)

	for i := 1; i <= 3; i++ {
		rb.Push(newTestNews(string(rune('0' + i))))
	}

	rb.Clear()

	if rb.Size() != 0 {
		t.Errorf("expected size 0 after clear, got %d", rb.Size())
	}

	all := rb.GetAll()
	if len(all) != 0 {
		t.Errorf("expected no items after clear, got %d", len(all))
	}
}

func TestRingBuffer_Filter(t *testing.T) {
	rb := NewRingBuffer(10)

	rb.Push(&model.News{ID: "1", Source: "A"})
	rb.Push(&model.News{ID: "2", Source: "B"})
	rb.Push(&model.News{ID: "3", Source: "A"})
	rb.Push(&model.News{ID: "4", Source: "B"})

	filtered := rb.Filter(func(n *model.News) bool {
		return n.Source == "A"
	})

	if len(filtered) != 2 {
		t.Errorf("expected 2 filtered items, got %d", len(filtered))
	}
}

func BenchmarkRingBuffer_Push(b *testing.B) {
	rb := NewRingBuffer(1000)
	news := newTestNews("test")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rb.Push(news)
	}
}

func BenchmarkRingBuffer_GetRecent(b *testing.B) {
	rb := NewRingBuffer(1000)
	for i := 0; i < 1000; i++ {
		rb.Push(newTestNews(string(rune('0' + i))))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rb.GetRecent(100)
	}
}

// 对比旧的 slice prepend 方式
func BenchmarkSlicePrepend(b *testing.B) {
	var slice []*model.News
	news := newTestNews("test")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		slice = append([]*model.News{news}, slice...)
		if len(slice) > 1000 {
			slice = slice[:1000]
		}
	}
}
