package filter

import (
	"sync"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

// fakeDedupStore 内存版 store.Store,满足 checkAndSet 的 Exists/Set 语义。
type fakeDedupStore struct {
	mu   sync.Mutex
	data map[string]time.Time
}

func newFakeDedupStore() *fakeDedupStore {
	return &fakeDedupStore{data: make(map[string]time.Time)}
}

func (f *fakeDedupStore) Exists(bucket, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := bucket + "\x00" + key
	exp, ok := f.data[k]
	if !ok {
		return false, nil
	}
	if time.Now().After(exp) {
		delete(f.data, k)
		return false, nil
	}
	return true, nil
}

func (f *fakeDedupStore) Set(bucket, key string, ttl time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[bucket+"\x00"+key] = time.Now().Add(ttl)
	return nil
}

func (f *fakeDedupStore) Delete(bucket, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, bucket+"\x00"+key)
	return nil
}

func (f *fakeDedupStore) Close() error { return nil }

func newTestDedup() (*UnifiedDedup, error) {
	return NewUnifiedDedup(newFakeDedupStore(), UnifiedDedupConfig{
		TTL: 3600,
		DedupGroups: [][]string{
			{"trump-rss", "bwe-tradfi", "kobeissi-letter", "bloomberg-markets"},
		},
		Semantic: SemanticSubConfig{
			Enabled:    true,
			Threshold:  0.75,
			TimeWindow: 5400,
			CacheSize:  100,
		},
	})
}

func newsMsg(source, title string) *model.Message {
	return &model.Message{
		Source:     source,
		SourceType: "rss",
		Title:      title,
		ID:         source + "-" + title, // 每源每标题唯一,避免 stage1 干扰
	}
}

// 同源同标题(stage1 精确):lane 内重复拦截(stage1 key 按 source+sink 隔离,
// 同一条消息只会路由到一个 lane,跨 lane 不互拦是设计)。
func TestDedupStage1ExactBlocksWithinLane(t *testing.T) {
	u, err := newTestDedup()
	if err != nil {
		t.Fatal(err)
	}
	m := newsMsg("bloomberg-markets", "伊朗局势最新进展")
	if u.ShouldFilter("bloomberg-markets", "wechat-main", m) {
		t.Fatal("first occurrence should pass")
	}
	if !u.ShouldFilter("bloomberg-markets", "wechat-main", m) {
		t.Fatal("duplicate ID should be blocked on direct lane")
	}
	// digest lane 独立计数:第一次出现通过,第二次拦
	if u.ShouldFilter("bloomberg-markets", DigestSinkName, m) {
		t.Fatal("digest lane first occurrence should pass")
	}
	if !u.ShouldFilter("bloomberg-markets", DigestSinkName, m) {
		t.Fatal("duplicate ID should be blocked on digest lane")
	}
}

// direct lane:跨源同题(同组不同源,同标题不同 ID)被 stage2 拦截。
func TestDedupDirectBlocksCrossSourceDuplicate(t *testing.T) {
	u, err := newTestDedup()
	if err != nil {
		t.Fatal(err)
	}
	if u.ShouldFilter("trump-rss", "wechat-main", newsMsg("trump-rss", "Trump says US to cancel Iran attack")) {
		t.Fatal("first should pass")
	}
	// 同组另一源,同标题(不同 ID)
	if !u.ShouldFilter("bwe-tradfi", "wechat-main", newsMsg("bwe-tradfi", "Trump says US to cancel Iran attack")) {
		t.Fatal("direct lane should block cross-source duplicate (group-wide stage2)")
	}
}

// digest lane:跨源同题放行(多源角度进摘要,Topic 聚合渲染)。
func TestDedupDigestAllowsCrossSourceDuplicate(t *testing.T) {
	u, err := newTestDedup()
	if err != nil {
		t.Fatal(err)
	}
	if u.ShouldFilter("trump-rss", DigestSinkName, newsMsg("trump-rss", "Trump cancels Iran attack")) {
		t.Fatal("first should pass")
	}
	// 同组另一源,同标题:digest lane 应放行
	if u.ShouldFilter("bwe-tradfi", DigestSinkName, newsMsg("bwe-tradfi", "Trump cancels Iran attack")) {
		t.Fatal("digest lane should allow cross-source duplicate for topic aggregation")
	}
	// 同源再发同题(同源同题 stage2 source-scoped):应拦
	if !u.ShouldFilter("trump-rss", DigestSinkName, newsMsg("trump-rss", "Trump cancels Iran attack")) {
		t.Fatal("digest lane should block same-source duplicate title")
	}
}

// digest lane 跳过语义去重:跨源相似标题(非完全同题)放行。
func TestDedupDigestSkipsSemantic(t *testing.T) {
	u, err := newTestDedup()
	if err != nil {
		t.Fatal(err)
	}
	if u.ShouldFilter("kobeissi-letter", DigestSinkName, newsMsg("kobeissi-letter", "BREAKING: Trump cancels US attack on Iran")) {
		t.Fatal("first should pass")
	}
	// trump-rss 的相似标题(同事件不同措辞):direct 会被 stage3 语义拦,digest 放行
	if u.ShouldFilter("trump-rss", DigestSinkName, newsMsg("trump-rss", "Trump says attack on Iran cancelled subject to deal")) {
		t.Fatal("digest lane should skip semantic dedup")
	}
}

// direct lane 语义去重保持:高度相似标题(仅个别词差异)跨源仍拦。
func TestDedupDirectKeepsSemantic(t *testing.T) {
	u, err := newTestDedup()
	if err != nil {
		t.Fatal(err)
	}
	if u.ShouldFilter("kobeissi-letter", "wechat-main", newsMsg("kobeissi-letter", "Trump cancels US attack on Iran")) {
		t.Fatal("first should pass")
	}
	// 同词不同序,相似度 1.0 > 阈值 0.75
	if !u.ShouldFilter("trump-rss", "wechat-main", newsMsg("trump-rss", "Trump cancels attack on Iran")) {
		t.Fatal("direct lane should keep semantic dedup")
	}
}

var _ store.Store = (*fakeDedupStore)(nil)
