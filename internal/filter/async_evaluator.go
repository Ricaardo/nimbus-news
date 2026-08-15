package filter

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// EvalResult AI 评估结果
type EvalResult struct {
	Status   EvalStatus
	Score    float64
	Category string
	Reason   string
	Passed   bool
	Blocked  bool
}

type EvalStatus string

const (
	EvalSuccess       EvalStatus = "success"
	EvalProviderError EvalStatus = "provider_error"
	EvalParseError    EvalStatus = "parse_error"
	EvalRateLimited   EvalStatus = "rate_limited"
	EvalUnavailable   EvalStatus = "unavailable"
)

// AsyncEvaluator 异步 AI 新闻评估器
// 从滤链中拆出，不阻塞主推送路径
type AsyncEvaluator struct {
	provider llm.Provider
	config   AsyncEvalConfig

	queue       chan *EvalTask
	onHighScore func(msg *model.Message)

	wg      sync.WaitGroup
	running bool
	mu      sync.Mutex
}

// AsyncEvalConfig 异步评估配置
type AsyncEvalConfig struct {
	Enabled         bool     `yaml:"enabled"`
	ThresholdScore  float64  `yaml:"threshold_score"` // 高分阈值（≥此分触发 onHighScore）
	Workers         int      `yaml:"workers"`         // 并发 worker 数
	QueueSize       int      `yaml:"queue_size"`      // 队列容量
	Timeout         int      `yaml:"timeout_seconds"` // 单次评估超时（秒）
	BlockCategories []string `yaml:"block_categories"`
	TargetSources   []string `yaml:"target_sources"`
}

// EvalTask 评估任务
type EvalTask struct {
	Msg       *model.Message
	CreatedAt time.Time
}

// NewAsyncEvaluator 创建异步评估器
func NewAsyncEvaluator(provider llm.Provider, cfg AsyncEvalConfig, onHighScore func(msg *model.Message)) *AsyncEvaluator {
	if cfg.ThresholdScore == 0 {
		cfg.ThresholdScore = 6.0
	}
	if cfg.Workers == 0 {
		cfg.Workers = 10
	}
	if cfg.QueueSize == 0 {
		cfg.QueueSize = 1000
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10
	}

	return &AsyncEvaluator{
		provider:    provider,
		config:      cfg,
		queue:       make(chan *EvalTask, cfg.QueueSize),
		onHighScore: onHighScore,
	}
}

// Start 启动 worker 协程
func (e *AsyncEvaluator) Start(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return
	}
	e.running = true

	for i := 0; i < e.config.Workers; i++ {
		e.wg.Add(1)
		go e.worker(ctx, i)
	}
	fmt.Printf("AsyncEvaluator: started %d workers, threshold=%.1f\n", e.config.Workers, e.config.ThresholdScore)
}

// Stop 停止（等待队列清空）
func (e *AsyncEvaluator) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return
	}
	close(e.queue)
	e.wg.Wait()
	e.running = false
	fmt.Println("AsyncEvaluator: stopped")
}

// Submit 提交评估任务。队列满时非阻塞丢弃
func (e *AsyncEvaluator) Submit(msg *model.Message) {
	if !e.config.Enabled {
		return
	}
	if len(e.config.TargetSources) > 0 {
		found := false
		for _, s := range e.config.TargetSources {
			if s == msg.Source {
				found = true
				break
			}
		}
		if !found {
			return
		}
	}

	// 阻塞式 ai_filter 已评过分,复用其结果,避免双份 LLM 花费。
	if v, ok := msg.GetMetadata("ai_score"); ok {
		if score, ok := v.(float64); ok && score >= e.config.ThresholdScore && e.onHighScore != nil {
			e.onHighScore(msg)
		}
		return
	}

	select {
	case e.queue <- &EvalTask{Msg: msg, CreatedAt: time.Now()}:
	default:
		// 队列满时丢弃（不阻塞主链路）
	}
}

// GetStats 获取队列状态
func (e *AsyncEvaluator) Stats() map[string]interface{} {
	return map[string]interface{}{
		"queue_length": len(e.queue),
		"queue_cap":    cap(e.queue),
		"workers":      e.config.Workers,
		"enabled":      e.config.Enabled,
		"threshold":    e.config.ThresholdScore,
	}
}

func (e *AsyncEvaluator) worker(ctx context.Context, id int) {
	defer e.wg.Done()

	for task := range e.queue {
		e.process(ctx, task)
	}
}

func (e *AsyncEvaluator) process(ctx context.Context, task *EvalTask) {
	evalCtx, cancel := context.WithTimeout(ctx, time.Duration(e.config.Timeout)*time.Second)
	defer cancel()

	prompt := newsEvalPrompt(task.Msg.Title, task.Msg.Content, task.Msg.Source)

	messages := []llm.Message{
		{Role: "user", Content: prompt},
	}

	resp, err := e.provider.Chat(evalCtx, messages)
	if err != nil {
		task.Msg.SetMetadata("ai_eval_status", string(EvalProviderError))
		return // 静默失败，不影响主流程
	}

	result := parseEvalResponse(resp, e.config.ThresholdScore)

	// 写入消息元数据
	task.Msg.SetMetadata("ai_score", result.Score)
	task.Msg.SetMetadata("ai_category", result.Category)
	task.Msg.SetMetadata("ai_reason", result.Reason)
	task.Msg.SetMetadata("ai_eval_status", string(result.Status))

	// 高分回调
	if result.Passed && result.Score >= e.config.ThresholdScore && e.onHighScore != nil {
		e.onHighScore(task.Msg)
	}
}

// 同 ai_filter.go 的解析逻辑
func parseEvalResponse(resp string, threshold float64) EvalResult {
	return parseStrictEvalResponse(resp, threshold)
}

func splitPipe(s string) []string {
	var parts []string
	start := 0
	count := 0
	for i, c := range s {
		if c == '|' {
			parts = append(parts, s[start:i])
			start = i + 1
			count++
			if count == 2 {
				parts = append(parts, s[start:])
				return parts
			}
		}
	}
	if len(parts) < 3 {
		parts = append(parts, s[start:])
	}
	return parts
}
