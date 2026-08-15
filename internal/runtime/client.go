package runtime

import (
	"context"
	"encoding/json"
)

type AgentRunRequest struct {
	Prompt       string `json:"prompt"`
	Resume       string `json:"resume,omitempty"`
	Model        string `json:"model,omitempty"`
	Effort       string `json:"effort,omitempty"`
	BlockAccount bool   `json:"block_account,omitempty"`
}

type AgentUsage struct {
	Model           string  `json:"model"`
	CostUSD         float64 `json:"costUsd"`
	InputTokens     int64   `json:"inputTokens"`
	OutputTokens    int64   `json:"outputTokens"`
	CacheReadTokens int64   `json:"cacheReadTokens"`
}

type AgentRunResult struct {
	SessionID string      `json:"session_id,omitempty"`
	Text      string      `json:"text"`
	Usage     *AgentUsage `json:"usage,omitempty"`
}

// AgentRun performs exactly one attempt. In particular, child_crashed is
// returned to the caller and is never replayed by the supervisor.
func (s *Supervisor) AgentRun(ctx context.Context, request AgentRunRequest) (AgentRunResult, error) {
	var result AgentRunResult
	err := s.Call(ctx, "agent.run", request, &result)
	return result, err
}

type EmbedRequest struct {
	Texts []string `json:"texts"`
	Model string   `json:"model,omitempty"`
}

type EmbedResult struct {
	Model     string      `json:"model"`
	Dimension int         `json:"dimension"`
	Vectors   [][]float64 `json:"vectors"`
}

// Embed performs exactly one attempt. Callers own all retry policy.
func (s *Supervisor) Embed(ctx context.Context, request EmbedRequest) (EmbedResult, error) {
	var result EmbedResult
	err := s.Call(ctx, "embedding.embed", request, &result)
	return result, err
}

type TextDelta struct {
	RequestID json.RawMessage `json:"request_id"`
	Delta     string          `json:"delta"`
}
