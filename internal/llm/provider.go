package llm

import (
	"context"
	"sync/atomic"
)

// Message LLM 消息
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall 工具调用
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Provider LLM 提供者接口
type Provider interface {
	// Chat 简单对话（non-thinking 模式，主模型，速度最快）
	Chat(ctx context.Context, messages []Message) (string, error)
	// ChatHeavy 用深度模型（heavy_model）做 non-thinking 快速调用
	ChatHeavy(ctx context.Context, messages []Message) (string, error)
	// ChatWithTools 带工具调用的对话
	ChatWithTools(ctx context.Context, messages []Message, tools []map[string]interface{}) (*Message, error)
	// Think 深度推理（thinking 模式），返回内容和推理过程
	Think(ctx context.Context, messages []Message, mode string) (content string, thinkingTrace string, err error)
}

// Config LLM 配置
type Config struct {
	Provider     string `yaml:"provider"`     // deepseek / openai
	APIURL       string `yaml:"api_url"`      // API 地址
	APIKey       string `yaml:"api_key"`      // API 密钥
	Model        string `yaml:"model"`        // 主模型 (v4-flash)，默认 deepseek-v4-flash
	HeavyModel   string `yaml:"heavy_model"`  // 深度推理模型 (v4-pro)，默认 deepseek-v4-pro
	ThinkingMode string `yaml:"thinking_mode"` // 默认推理模式: thinking / thinking_max
}

// HotSwapProvider 线程安全的热替换 LLM Provider 代理
// 所有业务组件持有此代理，Swap 时无需更新任何引用
type HotSwapProvider struct {
	current atomic.Value // holds Provider
}

// NewHotSwapProvider 创建热替换代理
func NewHotSwapProvider(p Provider) *HotSwapProvider {
	hp := &HotSwapProvider{}
	hp.current.Store(p)
	return hp
}

// Swap 原子替换底层 provider
func (p *HotSwapProvider) Swap(newProvider Provider) {
	p.current.Store(newProvider)
}

// Chat delegates to current provider
func (p *HotSwapProvider) Chat(ctx context.Context, messages []Message) (string, error) {
	return p.current.Load().(Provider).Chat(ctx, messages)
}

// ChatHeavy delegates to current provider
func (p *HotSwapProvider) ChatHeavy(ctx context.Context, messages []Message) (string, error) {
	return p.current.Load().(Provider).ChatHeavy(ctx, messages)
}

// ChatWithTools delegates to current provider
func (p *HotSwapProvider) ChatWithTools(ctx context.Context, messages []Message, tools []map[string]interface{}) (*Message, error) {
	return p.current.Load().(Provider).ChatWithTools(ctx, messages, tools)
}

// Think delegates to current provider
func (p *HotSwapProvider) Think(ctx context.Context, messages []Message, mode string) (string, string, error) {
	return p.current.Load().(Provider).Think(ctx, messages, mode)
}
