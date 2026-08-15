package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/go-resty/resty/v2"
)

// OpenAIProvider OpenAI 兼容的 LLM 提供者（DeepSeek v4）
type OpenAIProvider struct {
	client *resty.Client
	config Config
}

type openAIRequest struct {
	Model    string                   `json:"model"`
	Messages []Message                `json:"messages"`
	Tools    []map[string]interface{} `json:"tools,omitempty"`
	Thinking *ThinkingParam           `json:"thinking,omitempty"`
	Stream   bool                     `json:"stream"`
}

// ThinkingParam DeepSeek v4 thinking 参数
type ThinkingParam struct {
	Type string `json:"type"` // "enabled" | "disabled"
}

type openAIResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

// NewOpenAIProvider 创建 OpenAI 提供者
// 设置 HTTP 超时，避免 LLM 偶发挂起阻塞新闻推送（EnhanceBatch 是同步的）。
// 60s 作为硬上限可覆盖 pro thinking；普通 flash 调用 1-3s 返回，正常情况下不受影响。
func NewOpenAIProvider(cfg Config) *OpenAIProvider {
	return &OpenAIProvider{
		client: resty.New().SetTimeout(60 * time.Second),
		config: cfg,
	}
}

func (p *OpenAIProvider) Chat(ctx context.Context, messages []Message) (string, error) {
	respMsg, err := p.ChatWithTools(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return respMsg.Content, nil
}

// modelOrDefault 返回配置的主模型，缺省时按 provider 给默认值
func (p *OpenAIProvider) modelOrDefault() string {
	if p.config.Model != "" {
		return p.config.Model
	}
	if p.config.Provider == "deepseek" {
		return "deepseek-v4-flash"
	}
	return "gpt-3.5-turbo"
}

// heavyModelOrDefault 返回配置的深度模型，缺省 deepseek-v4-pro
func (p *OpenAIProvider) heavyModelOrDefault() string {
	if p.config.HeavyModel != "" {
		return p.config.HeavyModel
	}
	return "deepseek-v4-pro"
}

func (p *OpenAIProvider) ChatWithTools(ctx context.Context, messages []Message, tools []map[string]interface{}) (*Message, error) {
	return p.chat(ctx, p.modelOrDefault(), messages, tools)
}

// ChatHeavy 用深度模型（heavy_model = deepseek-v4-pro）做非 thinking 的快速调用
// 用于结构化报告的「快速解读影响」——要 pro 的判断力但不要 thinking 的延迟。
func (p *OpenAIProvider) ChatHeavy(ctx context.Context, messages []Message) (string, error) {
	respMsg, err := p.chat(ctx, p.heavyModelOrDefault(), messages, nil)
	if err != nil {
		return "", err
	}
	return respMsg.Content, nil
}

// chat 统一的非 thinking 对话调用
func (p *OpenAIProvider) chat(ctx context.Context, model string, messages []Message, tools []map[string]interface{}) (*Message, error) {
	reqBody := openAIRequest{
		Model:    model,
		Messages: messages,
		Tools:    tools,
		Thinking: &ThinkingParam{Type: "disabled"}, // non-thinking: 最快速度
	}

	var respBody openAIResponse

	resp, err := p.client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+p.config.APIKey).
		SetHeader("Content-Type", "application/json").
		SetBody(reqBody).
		SetResult(&respBody).
		Post(p.config.APIURL + "/chat/completions")

	if err != nil {
		return nil, err
	}

	if resp.IsError() {
		return nil, fmt.Errorf("LLM API error: %s", resp.String())
	}

	if len(respBody.Choices) == 0 {
		return nil, fmt.Errorf("empty response from LLM")
	}

	return &respBody.Choices[0].Message, nil
}

// Think 深度推理（使用 v4 thinking 模式）
// mode: "thinking" / "thinking_max" / ""（空则走默认配置）
// 返回: content(最终回答), thinkingTrace(推理过程), error
func (p *OpenAIProvider) Think(ctx context.Context, messages []Message, mode string) (string, string, error) {
	model := p.heavyModelOrDefault()
	if mode == "" {
		mode = p.config.ThinkingMode
		if mode == "" {
			mode = "thinking"
		}
	}

	reqBody := openAIRequest{
		Model:    model,
		Messages: messages,
		Thinking: &ThinkingParam{Type: "enabled"},
	}

	var respBody struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}

	req := p.client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+p.config.APIKey).
		SetHeader("Content-Type", "application/json").
		SetBody(reqBody).
		SetResult(&respBody)

	// thinking_max: 加 reasoning_effort 顶层参数
	if mode == "thinking_max" {
		body := map[string]interface{}{
			"model":            model,
			"messages":         messages,
			"thinking":         map[string]string{"type": "enabled"},
			"reasoning_effort": "max",
		}
		req.SetBody(body)
	}

	resp, err := req.Post(p.config.APIURL + "/chat/completions")

	if err != nil {
		return "", "", err
	}

	if resp.IsError() {
		return "", "", fmt.Errorf("Think API error: %s", resp.String())
	}

	if len(respBody.Choices) == 0 {
		return "", "", fmt.Errorf("empty response from Think")
	}

	content := respBody.Choices[0].Message.Content
	reasoning := respBody.Choices[0].Message.ReasoningContent

	return content, reasoning, nil
}
