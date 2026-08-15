package source

import (
	"context"
	"fmt"
	"sync"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// Source 消息源接口
type Source interface {
	// Name 返回源的唯一名称
	Name() string
	// Type 返回源的类型标识 (rss/api 等)
	Type() string
	// Fetch 抓取消息列表
	Fetch() ([]*model.Message, error)
}

// ContextFetcher is an optional extension for sources that can cancel an
// in-flight fetch. Source remains unchanged for compatibility with existing
// implementations.
type ContextFetcher interface {
	FetchContext(context.Context) ([]*model.Message, error)
}

// Config 源配置
type Config struct {
	Name     string                 `yaml:"name"`
	Type     string                 `yaml:"type"`
	URL      string                 `yaml:"url"`
	Interval int                    `yaml:"interval"` // 抓取间隔（秒）
	Sinks    []string               `yaml:"sinks"`    // 推送到哪些目标
	Options  map[string]interface{} `yaml:"options"`  // 源特定配置
}

// Factory 源工厂函数类型
type Factory func(cfg Config) (Source, error)

// Registry 源类型注册表
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// 全局注册表
var registry = &Registry{
	factories: make(map[string]Factory),
}

// Register 注册源类型
func Register(sourceType string, factory Factory) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.factories[sourceType] = factory
}

// Create 根据配置创建源实例
func Create(cfg Config) (Source, error) {
	registry.mu.RLock()
	factory, ok := registry.factories[cfg.Type]
	registry.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown source type: %s", cfg.Type)
	}
	return factory(cfg)
}

// ListTypes 列出所有已注册的源类型
func ListTypes() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()

	types := make([]string, 0, len(registry.factories))
	for t := range registry.factories {
		types = append(types, t)
	}
	return types
}
