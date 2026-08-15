package filter

import (
	"log/slog"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// Filter 过滤器接口
type Filter interface {
	// Name 返回过滤器名称
	Name() string
	// ShouldFilter 判断消息是否应该被过滤（返回 true 表示过滤掉，不发送）
	ShouldFilter(sourceName, sinkName string, msg *model.Message) bool
}

// Chain 过滤器链
type Chain struct {
	filters []Filter
}

// NewChain 创建过滤器链
func NewChain(filters ...Filter) *Chain {
	return &Chain{filters: filters}
}

// Add 添加过滤器
func (c *Chain) Add(f Filter) {
	c.filters = append(c.filters, f)
}

// ShouldFilter 检查消息是否应该被过滤
func (c *Chain) ShouldFilter(sourceName, sinkName string, msg *model.Message) bool {
	for _, f := range c.filters {
		if f.ShouldFilter(sourceName, sinkName, msg) {
			slog.Debug("chain filtered", "title", msg.Title[:min(20, len(msg.Title))], "filter", f.Name(), "src", sourceName, "sink", sinkName)
			return true
		}
	}
	return false
}

// FilterMessages 过滤消息列表，返回未被过滤的消息
func (c *Chain) FilterMessages(sourceName, sinkName string, msgs []*model.Message) []*model.Message {
	var result []*model.Message
	for _, msg := range msgs {
		if !c.ShouldFilter(sourceName, sinkName, msg) {
			result = append(result, msg)
		}
	}
	return result
}

// GetFilters 获取所有过滤器
func (c *Chain) GetFilters() []Filter {
	return c.filters
}
