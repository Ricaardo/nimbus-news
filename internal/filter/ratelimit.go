package filter

import (
	"fmt"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"sync"
	"time"
)

// RatelimitRule 频控规则
type RatelimitRule struct {
	Source       string // 源名称，* 表示所有
	Sink         string // 目标名称，* 表示所有
	MaxPerMinute int    // 每分钟最大消息数
}

// RatelimitFilter 频控过滤器
type RatelimitFilter struct {
	rules      []RatelimitRule
	counters   map[string]*counter // key: source|sink
	mu         sync.Mutex
	retryQueue *RetryQueue     // 补推队列
	stats      *RatelimitStats // 统计信息
}

type counter struct {
	count     int
	resetTime time.Time
}

// RatelimitStats 频控统计
type RatelimitStats struct {
	mu             sync.RWMutex
	TotalFiltered  int64            // 总拦截数
	TotalPassed    int64            // 总通过数
	FilteredByRule map[string]int64 // 按规则统计拦截数
	FilteredBySink map[string]int64 // 按目标统计拦截数
	RecentFiltered []FilteredRecord // 最近拦截记录
}

// FilteredRecord 拦截记录
type FilteredRecord struct {
	Time       time.Time
	SourceName string
	SinkName   string
	MessageID  string
	Rule       string
}

// NewRatelimitFilter 创建频控过滤器
func NewRatelimitFilter(rules []RatelimitRule) *RatelimitFilter {
	return &RatelimitFilter{
		rules:    rules,
		counters: make(map[string]*counter),
		stats: &RatelimitStats{
			FilteredByRule: make(map[string]int64),
			FilteredBySink: make(map[string]int64),
			RecentFiltered: make([]FilteredRecord, 0, 100),
		},
	}
}

// SetRetryQueue 设置补推队列
func (f *RatelimitFilter) SetRetryQueue(queue *RetryQueue) {
	f.retryQueue = queue
}

// Name 返回过滤器名称
func (f *RatelimitFilter) Name() string {
	return "ratelimit"
}

// ShouldFilter 检查是否应该限流
func (f *RatelimitFilter) ShouldFilter(sourceName, sinkName string, msg *model.Message) bool {
	if msg.GetStringMetadata("typed_routing_critical") == "1" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	// 查找匹配的规则
	rule := f.findRule(sourceName, sinkName)
	if rule == nil {
		f.stats.recordPassed()
		return false // 无匹配规则，不限流
	}

	// 获取或创建计数器
	key := fmt.Sprintf("%s|%s", sourceName, sinkName)
	c := f.counters[key]
	now := time.Now()

	if c == nil || now.After(c.resetTime) {
		// 创建新计数器或重置
		c = &counter{
			count:     0,
			resetTime: now.Add(time.Minute),
		}
		f.counters[key] = c
	}

	// 检查是否超限
	if c.count >= rule.MaxPerMinute {
		ruleKey := fmt.Sprintf("%s->%s(%d/min)", rule.Source, rule.Sink, rule.MaxPerMinute)
		fmt.Printf("ratelimit: %s -> %s exceeded %d/min\n", sourceName, sinkName, rule.MaxPerMinute)

		// 记录统计
		f.stats.recordFiltered(sourceName, sinkName, msg.ID, ruleKey)

		// 加入补推队列
		if f.retryQueue != nil {
			f.retryQueue.Enqueue(msg, sourceName, sinkName, "ratelimit exceeded")
		}

		return true
	}

	// 增加计数
	c.count++
	f.stats.recordPassed()
	return false
}

// ShouldFilterWithoutQueue 检查是否应该限流（不入队，用于重试检查）
func (f *RatelimitFilter) ShouldFilterWithoutQueue(sourceName, sinkName string, msg *model.Message) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	rule := f.findRule(sourceName, sinkName)
	if rule == nil {
		return false
	}

	key := fmt.Sprintf("%s|%s", sourceName, sinkName)
	c := f.counters[key]
	now := time.Now()

	if c == nil || now.After(c.resetTime) {
		return false // 计数器已重置，可以发送
	}

	return c.count >= rule.MaxPerMinute
}

// findRule 查找匹配的规则
func (f *RatelimitFilter) findRule(sourceName, sinkName string) *RatelimitRule {
	// 优先精确匹配
	for i := range f.rules {
		r := &f.rules[i]
		if r.Source == sourceName && r.Sink == sinkName {
			return r
		}
	}

	// 源精确 + 目标通配
	for i := range f.rules {
		r := &f.rules[i]
		if r.Source == sourceName && r.Sink == "*" {
			return r
		}
	}

	// 源通配 + 目标精确
	for i := range f.rules {
		r := &f.rules[i]
		if r.Source == "*" && r.Sink == sinkName {
			return r
		}
	}

	// 双通配
	for i := range f.rules {
		r := &f.rules[i]
		if r.Source == "*" && r.Sink == "*" {
			return r
		}
	}

	return nil
}

// Reset 重置所有计数器
func (f *RatelimitFilter) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters = make(map[string]*counter)
}

// GetStats 获取当前统计信息
func (f *RatelimitFilter) GetStats() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()

	stats := make(map[string]int)
	for k, c := range f.counters {
		if time.Now().Before(c.resetTime) {
			stats[k] = c.count
		}
	}
	return stats
}

// GetRatelimitStats 获取频控统计
func (f *RatelimitFilter) GetRatelimitStats() *RatelimitStats {
	return f.stats
}

// GetRetryQueue 获取补推队列
func (f *RatelimitFilter) GetRetryQueue() *RetryQueue {
	return f.retryQueue
}

// recordPassed 记录通过
func (s *RatelimitStats) recordPassed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalPassed++
}

// recordFiltered 记录拦截
func (s *RatelimitStats) recordFiltered(sourceName, sinkName, msgID, rule string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.TotalFiltered++
	s.FilteredByRule[rule]++
	s.FilteredBySink[sinkName]++

	// 保留最近100条记录
	record := FilteredRecord{
		Time:       time.Now(),
		SourceName: sourceName,
		SinkName:   sinkName,
		MessageID:  msgID,
		Rule:       rule,
	}
	s.RecentFiltered = append(s.RecentFiltered, record)
	if len(s.RecentFiltered) > 100 {
		s.RecentFiltered = s.RecentFiltered[1:]
	}
}

// GetSummary 获取统计摘要
func (s *RatelimitStats) GetSummary() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]interface{}{
		"total_filtered":   s.TotalFiltered,
		"total_passed":     s.TotalPassed,
		"filtered_by_rule": s.FilteredByRule,
		"filtered_by_sink": s.FilteredBySink,
		"recent_count":     len(s.RecentFiltered),
	}
}

// GetRecentFiltered 获取最近拦截记录
func (s *RatelimitStats) GetRecentFiltered(limit int) []FilteredRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.RecentFiltered) {
		limit = len(s.RecentFiltered)
	}

	// 返回最近的记录
	start := len(s.RecentFiltered) - limit
	result := make([]FilteredRecord, limit)
	copy(result, s.RecentFiltered[start:])
	return result
}
