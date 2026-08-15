package filter

import (
	"context"
	"fmt"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"sync"
	"time"
)

// RetryItem 重试项
type RetryItem struct {
	Message    *model.Message
	SourceName string
	SinkName   string
	QueuedAt   time.Time
	RetryCount int
	LastError  string
}

// RetryQueue 补推队列
type RetryQueue struct {
	items      []*RetryItem
	mu         sync.RWMutex
	maxItems   int
	maxRetries int
	dispatcher func(ctx context.Context, msg *model.Message, sinks []string) error
}

// NewRetryQueue 创建补推队列
func NewRetryQueue(maxItems, maxRetries int) *RetryQueue {
	return &RetryQueue{
		items:      make([]*RetryItem, 0),
		maxItems:   maxItems,
		maxRetries: maxRetries,
	}
}

// SetDispatcher 设置分发函数
func (q *RetryQueue) SetDispatcher(fn func(ctx context.Context, msg *model.Message, sinks []string) error) {
	q.dispatcher = fn
}

// Enqueue 入队
func (q *RetryQueue) Enqueue(msg *model.Message, sourceName, sinkName string, reason string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// 检查是否已存在相同消息
	for _, item := range q.items {
		if item.Message.ID == msg.ID && item.SinkName == sinkName {
			return // 已存在，不重复入队
		}
	}

	// 检查队列容量
	if len(q.items) >= q.maxItems {
		// 移除最旧的项
		q.items = q.items[1:]
	}

	item := &RetryItem{
		Message:    msg,
		SourceName: sourceName,
		SinkName:   sinkName,
		QueuedAt:   time.Now(),
		RetryCount: 0,
		LastError:  reason,
	}
	q.items = append(q.items, item)

	fmt.Printf("RetryQueue: enqueued %s -> %s (reason: %s)\n", msg.ID, sinkName, reason)
}

// ProcessRetries 处理重试队列
func (q *RetryQueue) ProcessRetries(ctx context.Context, shouldFilter func(source, sink string, msg *model.Message) bool) {
	q.mu.Lock()
	pendingItems := make([]*RetryItem, len(q.items))
	copy(pendingItems, q.items)
	q.mu.Unlock()

	if len(pendingItems) == 0 {
		return
	}

	fmt.Printf("RetryQueue: processing %d pending items\n", len(pendingItems))

	var remaining []*RetryItem
	for _, item := range pendingItems {
		// 检查是否还需要限流
		if shouldFilter(item.SourceName, item.SinkName, item.Message) {
			item.RetryCount++
			if item.RetryCount < q.maxRetries {
				remaining = append(remaining, item)
			} else {
				fmt.Printf("RetryQueue: dropped %s -> %s after %d retries\n",
					item.Message.ID, item.SinkName, item.RetryCount)
			}
			continue
		}

		// 可以发送了
		if q.dispatcher != nil {
			if err := q.dispatcher(ctx, item.Message, []string{item.SinkName}); err != nil {
				fmt.Printf("RetryQueue: dispatch failed %s -> %s: %v\n",
					item.Message.ID, item.SinkName, err)
				item.RetryCount++
				item.LastError = err.Error()
				if item.RetryCount < q.maxRetries {
					remaining = append(remaining, item)
				}
			} else {
				fmt.Printf("RetryQueue: successfully dispatched %s -> %s\n",
					item.Message.ID, item.SinkName)
			}
		}
	}

	q.mu.Lock()
	q.items = remaining
	q.mu.Unlock()
}

// GetStats 获取队列统计
func (q *RetryQueue) GetStats() RetryQueueStats {
	q.mu.RLock()
	defer q.mu.RUnlock()

	stats := RetryQueueStats{
		PendingCount: len(q.items),
		BySink:       make(map[string]int),
		BySource:     make(map[string]int),
	}

	for _, item := range q.items {
		stats.BySink[item.SinkName]++
		stats.BySource[item.SourceName]++
	}

	return stats
}

// GetPendingItems 获取待处理项（供监控使用）
func (q *RetryQueue) GetPendingItems() []RetryItem {
	q.mu.RLock()
	defer q.mu.RUnlock()

	result := make([]RetryItem, len(q.items))
	for i, item := range q.items {
		result[i] = *item
	}
	return result
}

// Clear 清空队列
func (q *RetryQueue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = make([]*RetryItem, 0)
}

// RetryQueueStats 队列统计
type RetryQueueStats struct {
	PendingCount int
	BySink       map[string]int
	BySource     map[string]int
}
