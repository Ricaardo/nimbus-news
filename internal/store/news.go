package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
	bolt "go.etcd.io/bbolt"
)

// NewsStore 新闻存储（供 Agent 查询）。
// 最近视图始终按 FetchTime、ID 排序，使运行时与重启后的查询顺序一致。
type NewsStore struct {
	mu          sync.RWMutex
	persistMu   sync.Mutex
	recent      []*model.News          // 按 FetchTime、ID 升序，查询时反向读取
	newsMap     map[string]*model.News // ID 索引，用于快速查找
	maxItems    int
	ttl         time.Duration
	db          *bolt.DB
	cleanupDone chan struct{}
	writeStats  NewsStoreWriteStats
}

// NewsStoreWriteStats exposes durable write health without interrupting push
// delivery when the archive is temporarily unavailable.
type NewsStoreWriteStats struct {
	Persistent       bool      `json:"persistent"`
	WriteFailures    uint64    `json:"write_failures"`
	LastWriteError   string    `json:"last_write_error,omitempty"`
	LastWriteFailed  time.Time `json:"last_write_failed,omitempty"`
	LastWriteSuccess time.Time `json:"last_write_success,omitempty"`
}

// NewsStoreConfig 新闻存储配置
type NewsStoreConfig struct {
	MaxItems int           `yaml:"max_items"` // 最多保存条数
	TTL      time.Duration `yaml:"ttl"`       // 过期时间
}

const (
	defaultNewsStoreMaxItems = 1000
	maxNewsStoreMaxItems     = 10_000
)

// NewNewsStore 创建新闻存储
func NewNewsStore(cfg NewsStoreConfig) *NewsStore {
	return NewNewsStoreContext(context.Background(), cfg)
}

// NewNewsStoreContext ties periodic cleanup to the owning application.
func NewNewsStoreContext(ctx context.Context, cfg NewsStoreConfig) *NewsStore {
	ns := newNewsStore(cfg)
	ns.startCleanup(ctx)
	return ns
}

func newNewsStore(cfg NewsStoreConfig) *NewsStore {
	if cfg.MaxItems <= 0 {
		cfg.MaxItems = defaultNewsStoreMaxItems
	}
	if cfg.MaxItems > maxNewsStoreMaxItems {
		cfg.MaxItems = maxNewsStoreMaxItems
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 7 * 24 * time.Hour
	}

	ns := &NewsStore{
		recent:   make([]*model.News, 0, cfg.MaxItems),
		newsMap:  make(map[string]*model.News),
		maxItems: cfg.MaxItems,
		ttl:      cfg.TTL,
	}
	return ns
}

// Save 保存新闻。
func (s *NewsStore) Save(ctx context.Context, news *model.News) error {
	if news == nil || news.ID == "" {
		return fmt.Errorf("news store: news and id are required")
	}
	if s.db != nil {
		s.persistMu.Lock()
		defer s.persistMu.Unlock()
		retained, deleted, err := s.savePersistent(ctx, news)
		if err != nil {
			s.recordWriteFailure(err)
			return err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.writeStats.LastWriteSuccess = time.Now()
		s.removeIDsUnsafe(deleted)
		if !retained {
			return nil
		}
		if _, exists := s.newsMap[news.ID]; exists {
			return nil
		}
		s.insertNewsUnsafe(news)
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.newsMap[news.ID]; exists {
		return nil
	}

	s.removeExpiredUnsafe(time.Now().Add(-s.ttl))
	s.insertNewsUnsafe(news)

	return nil
}

func newsLess(a, b *model.News) bool {
	if a.FetchTime.Equal(b.FetchTime) {
		return a.ID < b.ID
	}
	return a.FetchTime.Before(b.FetchTime)
}

// insertNewsUnsafe maintains the bounded recent view with one binary search
// and at most maxItems element moves. The caller must hold s.mu.
func (s *NewsStore) insertNewsUnsafe(news *model.News) bool {
	if !news.FetchTime.After(time.Now().Add(-s.ttl)) {
		return false
	}
	if len(s.recent) == s.maxItems {
		if !newsLess(s.recent[0], news) {
			return false
		}
		index := sort.Search(len(s.recent), func(i int) bool {
			return !newsLess(s.recent[i], news)
		})
		delete(s.newsMap, s.recent[0].ID)
		copy(s.recent[:index-1], s.recent[1:index])
		s.recent[index-1] = news
		s.newsMap[news.ID] = news
		return true
	}

	index := sort.Search(len(s.recent), func(i int) bool {
		return !newsLess(s.recent[i], news)
	})
	s.recent = append(s.recent, nil)
	copy(s.recent[index+1:], s.recent[index:])
	s.recent[index] = news
	s.newsMap[news.ID] = news
	return true
}

func (s *NewsStore) removeExpiredUnsafe(cutoff time.Time) {
	firstRetained := sort.Search(len(s.recent), func(i int) bool {
		return s.recent[i].FetchTime.After(cutoff)
	})
	for _, news := range s.recent[:firstRetained] {
		delete(s.newsMap, news.ID)
	}
	if firstRetained > 0 {
		copy(s.recent, s.recent[firstRetained:])
		clear(s.recent[len(s.recent)-firstRetained:])
		s.recent = s.recent[:len(s.recent)-firstRetained]
	}
}

func (s *NewsStore) recordWriteFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeStats.WriteFailures++
	s.writeStats.LastWriteError = err.Error()
	s.writeStats.LastWriteFailed = time.Now()
}

// SaveBatch 批量保存新闻
func (s *NewsStore) SaveBatch(ctx context.Context, newsList []*model.News) error {
	for _, news := range newsList {
		if err := s.Save(ctx, news); err != nil {
			return err
		}
	}
	return nil
}

// GetByID 根据 ID 获取新闻 - O(1)
func (s *NewsStore) GetByID(ctx context.Context, id string) *model.News {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.newsMap[id]
}

// GetRecent 获取最近的新闻 - O(n)
func (s *NewsStore) GetRecent(ctx context.Context, count int) []*model.News {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if count > len(s.recent) {
		count = len(s.recent)
	}
	if count <= 0 {
		return nil
	}
	result := make([]*model.News, count)
	for i := 0; i < count; i++ {
		result[i] = s.recent[len(s.recent)-1-i]
	}
	return result
}

// Search 搜索新闻
func (s *NewsStore) Search(ctx context.Context, query string, limit int) []*model.News {
	s.mu.RLock()
	defer s.mu.RUnlock()

	query = strings.ToLower(query)
	var result []*model.News

	for i := len(s.recent) - 1; i >= 0; i-- {
		news := s.recent[i]
		if news == nil {
			continue
		}
		if len(result) >= limit {
			break
		}

		// 简单的关键词匹配
		matched := strings.Contains(strings.ToLower(news.Title), query) ||
			strings.Contains(strings.ToLower(news.Content), query) ||
			strings.Contains(strings.ToLower(news.Source), query)

		if !matched {
			// 检查标签
			for _, tag := range news.Tags {
				if strings.Contains(strings.ToLower(tag), query) {
					matched = true
					break
				}
			}
		}

		if matched {
			result = append(result, news)
		}
	}

	return result
}

// GetBySource 按来源获取新闻
func (s *NewsStore) GetBySource(ctx context.Context, source string, limit int) []*model.News {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*model.News
	for i := len(s.recent) - 1; i >= 0; i-- {
		news := s.recent[i]
		if news == nil {
			continue
		}
		if len(result) >= limit {
			break
		}
		if news.Source == source {
			result = append(result, news)
		}
	}

	return result
}

// Count 返回当前存储的新闻数量
func (s *NewsStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.recent)
}

// WriteStats returns a copy of durable write health.
func (s *NewsStore) WriteStats() NewsStoreWriteStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats := s.writeStats
	stats.Persistent = s.db != nil
	return stats
}

// cleanupLoop 清理过期数据
func (s *NewsStore) cleanupLoop(ctx context.Context) {
	defer close(s.cleanupDone)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.cleanup(); err != nil {
				slog.Warn("news store cleanup failed", "error", err)
			}
		}
	}
}

func (s *NewsStore) startCleanup(ctx context.Context) {
	s.cleanupDone = make(chan struct{})
	go s.cleanupLoop(ctx)
}

// WaitCleanup waits until the owning context has stopped background cleanup.
func (s *NewsStore) WaitCleanup() {
	if s == nil || s.cleanupDone == nil {
		return
	}
	<-s.cleanupDone
}

func (s *NewsStore) cleanup() error {
	if s.db != nil {
		s.persistMu.Lock()
		defer s.persistMu.Unlock()
		deleted, err := s.purgePersistent(context.Background())
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.removeIDsUnsafe(deleted)
		s.mu.Unlock()
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.removeExpiredUnsafe(time.Now().Add(-s.ttl))
	return nil
}

func (s *NewsStore) removeIDsUnsafe(ids map[string]struct{}) {
	if len(ids) == 0 {
		return
	}
	write := 0
	for _, news := range s.recent {
		if _, remove := ids[news.ID]; remove {
			delete(s.newsMap, news.ID)
			continue
		}
		s.recent[write] = news
		write++
	}
	clear(s.recent[write:])
	s.recent = s.recent[:write]
}

// ToJSON 导出为 JSON（调试用）
func (s *NewsStore) ToJSON() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]*model.News, len(s.recent))
	for i := range s.recent {
		items[i] = s.recent[len(s.recent)-1-i]
	}
	return json.Marshal(items)
}
