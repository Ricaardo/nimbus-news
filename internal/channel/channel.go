package channel

import (
	"context"
	"fmt"
	"sync"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// Mode 渠道模式
type Mode string

const (
	ModePush          Mode = "push"          // 仅推送
	ModeReceive       Mode = "receive"       // 仅接收
	ModeBidirectional Mode = "bidirectional" // 双向
)

// Channel 统一渠道接口（支持双向通信）
type Channel interface {
	// 基本信息
	Name() string
	Type() string
	Mode() Mode

	// 推送能力（主动发送）
	Send(ctx context.Context, msg *model.Message) error
	SendBatch(ctx context.Context, msgs []*model.Message) error

	// 接收能力（被动接收用户消息）
	// 设置消息处理回调，渠道收到消息后调用此回调
	SetMessageHandler(handler MessageHandler)

	// 回复能力（针对用户消息的回复）
	Reply(ctx context.Context, originalMsgID string, reply *model.Message) error

	// 生命周期
	Start(ctx context.Context) error
	Stop() error
}

// IdempotentSender is an optional channel capability. The key is stable across
// retries, but exactly-once delivery still depends on the downstream provider.
type IdempotentSender interface {
	SendIdempotent(ctx context.Context, msg *model.Message, key string) error
}

// MessageHandler 消息处理回调
type MessageHandler func(ctx context.Context, msg *model.Message)

// Config 渠道配置
type Config struct {
	Name    string                 `yaml:"name"`
	Type    string                 `yaml:"type"` // wechat/telegram/discord/rest
	Mode    Mode                   `yaml:"mode"` // push/receive/bidirectional
	Webhook string                 `yaml:"webhook,omitempty"`
	Options map[string]interface{} `yaml:"options,omitempty"`
}

// Factory 渠道工厂函数类型
type Factory func(cfg Config) (Channel, error)

// Registry 渠道类型注册表
type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

// 全局注册表
var registry = &Registry{
	factories: make(map[string]Factory),
}

// Register 注册渠道类型
func Register(channelType string, factory Factory) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.factories[channelType] = factory
}

// Create 根据配置创建渠道实例
func Create(cfg Config) (Channel, error) {
	registry.mu.RLock()
	factory, ok := registry.factories[cfg.Type]
	registry.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("unknown channel type: %s", cfg.Type)
	}
	return factory(cfg)
}

// ListTypes 列出所有已注册的渠道类型
func ListTypes() []string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()

	types := make([]string, 0, len(registry.factories))
	for t := range registry.factories {
		types = append(types, t)
	}
	return types
}

// BaseChannel 基础渠道实现，提供默认方法
type BaseChannel struct {
	name        string
	channelType string
	mode        Mode
	handler     MessageHandler
	mu          sync.RWMutex
}

// NewBaseChannel 创建基础渠道
func NewBaseChannel(name, channelType string, mode Mode) BaseChannel {
	return BaseChannel{
		name:        name,
		channelType: channelType,
		mode:        mode,
	}
}

func (b *BaseChannel) Name() string { return b.name }
func (b *BaseChannel) Type() string { return b.channelType }
func (b *BaseChannel) Mode() Mode   { return b.mode }

func (b *BaseChannel) SetMessageHandler(handler MessageHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handler = handler
}

func (b *BaseChannel) GetHandler() MessageHandler {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.handler
}

// SendBatch 默认批量发送实现（逐条发送）
func (b *BaseChannel) SendBatch(ctx context.Context, msgs []*model.Message, sendFn func(context.Context, *model.Message) error) error {
	for _, msg := range msgs {
		if err := sendFn(ctx, msg); err != nil {
			return err
		}
	}
	return nil
}

// CanSend 检查是否支持发送
func (b *BaseChannel) CanSend() bool {
	return b.mode == ModePush || b.mode == ModeBidirectional
}

// CanReceive 检查是否支持接收
func (b *BaseChannel) CanReceive() bool {
	return b.mode == ModeReceive || b.mode == ModeBidirectional
}
