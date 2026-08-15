package source

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/metrics"
)

// LLMSettable 可接受 LLM Provider 注入的源接口
// 报告型源实现此接口, bootstrap 阶段由 InitLLM 统一注入
type LLMSettable interface {
	SetLLMProvider(llm.Provider)
}

// LLMSummarize 通用 LLM 摘要调用: prompt 驱动, 超时保护, 失败返回空串不报错
// 调用方根据返回值判断: 非空则附加到报告末尾, 空串则跳过 AI 段落
func LLMSummarize(ctx context.Context, provider llm.Provider, prompt string, mode string, timeout time.Duration) string {
	return LLMSummarizeWithSource(ctx, provider, "", prompt, mode, timeout)
}

// LLMSummarizeWithSource 带 source label 的版本, 供 metrics 使用
func LLMSummarizeWithSource(ctx context.Context, provider llm.Provider, sourceName, prompt, mode string, timeout time.Duration) string {
	if provider == nil || strings.TrimSpace(prompt) == "" {
		return ""
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	subCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	messages := []llm.Message{{Role: "user", Content: prompt}}

	start := time.Now()
	var (
		content string
		err     error
	)
	if mode == "" {
		content, err = provider.Chat(subCtx, messages)
	} else {
		content, _, err = provider.Think(subCtx, messages, mode)
	}
	latency := time.Since(start)

	// metrics
	if sourceName != "" {
		metrics.LLMSummarizeDuration.WithLabelValues(sourceName).Observe(latency.Seconds())
	}
	result := "success"
	if err != nil {
		result = "error"
		slog.Warn("llm summarize failed", "source", sourceName, "error", err, "len_prompt", len(prompt))
		if sourceName != "" {
			metrics.LLMSummarizeTotal.WithLabelValues(sourceName, result).Inc()
		}
		return ""
	}

	cleaned := strings.TrimSpace(content)
	if cleaned == "" {
		result = "empty"
	}
	if sourceName != "" {
		metrics.LLMSummarizeTotal.WithLabelValues(sourceName, result).Inc()
	}
	return cleaned
}

// FormatLLMSection 把 AI 内容包装成统一样式, 便于各报告嵌入
func FormatLLMSection(title, content string) string {
	if content == "" {
		return ""
	}
	return fmt.Sprintf("── %s ──\n%s\n", title, content)
}
