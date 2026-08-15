package store

import (
	"sync"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// RingBuffer 环形缓冲区，用于高效存储固定大小的新闻列表
// 插入操作为 O(1)，获取最近 N 条为 O(N)
type RingBuffer struct {
	items    []*model.News
	capacity int
	head     int  // 下一个写入位置
	size     int  // 当前元素数量
	mu       sync.RWMutex
}

// NewRingBuffer 创建环形缓冲区
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1000
	}
	return &RingBuffer{
		items:    make([]*model.News, capacity),
		capacity: capacity,
	}
}

// Push 添加元素到缓冲区 - O(1)
// 新元素总是添加到 head 位置，head 向前移动
func (r *RingBuffer) Push(item *model.News) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 将元素放在当前 head 位置
	r.items[r.head] = item

	// 移动 head 到下一个位置
	r.head = (r.head + 1) % r.capacity

	// 更新大小
	if r.size < r.capacity {
		r.size++
	}
}

// GetRecent 获取最近 n 条记录 - O(n)
// 按时间倒序返回（最新的在前）
func (r *RingBuffer) GetRecent(n int) []*model.News {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if n > r.size {
		n = r.size
	}
	if n <= 0 {
		return nil
	}

	result := make([]*model.News, n)
	for i := 0; i < n; i++ {
		// 从 head-1 开始向后读取（head 指向下一个写入位置，head-1 是最新的）
		idx := (r.head - 1 - i + r.capacity) % r.capacity
		result[i] = r.items[idx]
	}
	return result
}

// GetAll 获取所有记录（按时间倒序）
func (r *RingBuffer) GetAll() []*model.News {
	return r.GetRecent(r.size)
}

// Size 返回当前元素数量
func (r *RingBuffer) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.size
}

// Capacity 返回容量
func (r *RingBuffer) Capacity() int {
	return r.capacity
}

// ForEach 遍历所有元素（按时间倒序，最新的在前）
func (r *RingBuffer) ForEach(fn func(*model.News) bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for i := 0; i < r.size; i++ {
		idx := (r.head - 1 - i + r.capacity) % r.capacity
		if !fn(r.items[idx]) {
			break
		}
	}
}

// Filter 过滤元素
func (r *RingBuffer) Filter(fn func(*model.News) bool) []*model.News {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*model.News
	for i := 0; i < r.size; i++ {
		idx := (r.head - 1 - i + r.capacity) % r.capacity
		if fn(r.items[idx]) {
			result = append(result, r.items[idx])
		}
	}
	return result
}

// Clear 清空缓冲区
func (r *RingBuffer) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := 0; i < r.capacity; i++ {
		r.items[i] = nil
	}
	r.head = 0
	r.size = 0
}

// RemoveExpired 移除过期元素
// 由于环形缓冲区的特性，我们不能真正删除中间的元素
// 这个方法会将过期元素标记为 nil，在 GetRecent 时跳过
func (r *RingBuffer) RemoveExpired(isExpired func(*model.News) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	removed := 0
	for i := 0; i < r.size; i++ {
		idx := (r.head - 1 - i + r.capacity) % r.capacity
		if r.items[idx] != nil && isExpired(r.items[idx]) {
			r.items[idx] = nil
			removed++
		}
	}
	return removed
}

// Compact 压缩缓冲区，移除 nil 元素
// 注意：这个操作是 O(n) 的，应该在低负载时调用
func (r *RingBuffer) Compact() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 收集非 nil 元素
	var valid []*model.News
	for i := 0; i < r.size; i++ {
		idx := (r.head - 1 - i + r.capacity) % r.capacity
		if r.items[idx] != nil {
			valid = append(valid, r.items[idx])
		}
	}

	// 重置缓冲区
	for i := 0; i < r.capacity; i++ {
		r.items[i] = nil
	}

	// 重新插入（注意顺序，valid 是从新到旧，需要反向插入）
	r.head = 0
	r.size = 0
	for i := len(valid) - 1; i >= 0; i-- {
		r.items[r.head] = valid[i]
		r.head = (r.head + 1) % r.capacity
		r.size++
	}
}
