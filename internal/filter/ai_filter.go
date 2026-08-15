package filter

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	cache "github.com/Ricaardo/nimbus-os/datasources/cache"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// AIFilter AI 过滤器
// 使用 LLM 评估新闻的重要性和质量，过滤低价值新闻
type AIFilter struct {
	provider llm.Provider
	config   AIFilterConfig
	lru      *cache.LRUCache // 有界 LRU 缓存，容量 2000，TTL 1h

	// 每分钟 LLM 评估次数固定窗口限流（fail-open：超限直接放行）
	rlMu              sync.Mutex
	rlWindowStart     time.Time
	rlCount           int
	rlWarned          bool
	legacyWindowStart time.Time
	legacyCount       int
	typedLRU          *cache.LRUCache
	typedWindows      map[string]typedRateWindow
}

type typedRateWindow struct {
	started time.Time
	count   int
}

// AIFilterConfig AI 过滤器配置
type AIFilterConfig struct {
	Enabled            bool     `yaml:"enabled"`
	ThresholdScore     float64  `yaml:"threshold_score"`       // 过滤阈值，低于此分数的新闻将被过滤 (0-10)
	MaxPerMinute       int      `yaml:"max_per_minute"`        // 每分钟最多处理数量
	GlobalMaxPerMinute int      `yaml:"global_max_per_minute"` // 所有 typed/legacy 同步评分共享的硬上限
	BlockCategories    []string `yaml:"block_categories"`      // 直接屏蔽的分类
	TargetSources      []string `yaml:"target_sources"`        // 需要 AI 过滤的源列表，为空表示所有源
}

// filterResult 过滤结果
type filterResult struct {
	score      float64
	category   string
	reason     string
	shouldPass bool
	status     EvalStatus
}

// NewAIFilter 创建 AI 过滤器
func NewAIFilter(provider llm.Provider, cfg AIFilterConfig) *AIFilter {
	if cfg.ThresholdScore == 0 {
		cfg.ThresholdScore = 5.0
	}
	if cfg.MaxPerMinute == 0 {
		cfg.MaxPerMinute = 10
	}
	if cfg.GlobalMaxPerMinute <= 0 {
		cfg.GlobalMaxPerMinute = cfg.MaxPerMinute * 4
	}
	return &AIFilter{
		provider:     provider,
		config:       cfg,
		lru:          cache.NewLRUCache(2000, time.Hour),
		typedLRU:     cache.NewLRUCache(4000, 24*time.Hour),
		typedWindows: make(map[string]typedRateWindow),
	}
}

// Name 返回过滤器名称
func (f *AIFilter) Name() string { return "ai_filter" }

// ShouldFilter 判断消息是否应该被过滤
func (f *AIFilter) ShouldFilter(sourceName, sinkName string, msg *model.Message) bool {
	if !f.config.Enabled {
		return false
	}
	if msg.GetStringMetadata("typed_routing") == "1" {
		return false
	}

	if len(f.config.TargetSources) > 0 {
		found := false
		for _, src := range f.config.TargetSources {
			if src == sourceName {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	cacheKey := f.generateKey(msg)

	var result filterResult
	fresh := false
	if v, ok := f.lru.Get(cacheKey); ok {
		result = v.(filterResult)
	} else {
		fresh = true
		if f.allowEval() {
			result = f.evaluate(msg)
			f.lru.Set(cacheKey, result)
		} else {
			// 限流放行的结果不缓存，避免窗口重置后仍被误判为已评估
			result = filterResult{score: f.config.ThresholdScore, category: "一般", reason: "评分限流放行", shouldPass: true, status: EvalRateLimited}
		}
	}

	msg.SetMetadata("ai_score", result.score)
	msg.SetMetadata("ai_category", result.category)
	msg.SetMetadata("ai_reason", result.reason)
	msg.SetMetadata("ai_eval_status", string(result.status))

	// 仅在本次为新鲜评估（缓存未命中）时记录，避免同一消息因多个 sink 调用重复打日志
	if !result.shouldPass && fresh {
		slog.Info("ai_filter blocked", "source", sourceName, "score", result.score,
			"reason", result.reason, "title", truncate(msg.Title, 50))
	}
	return !result.shouldPass
}

// EvaluateTyped evaluates one typed-routing message. Unlike ShouldFilter,
// every non-success status is explicit so callers can apply a fail-closed
// routing policy instead of the legacy filter's fail-open behavior.
func (f *AIFilter) EvaluateTyped(ctx context.Context, sourceName string, msg *model.Message) EvalResult {
	if f == nil || f.provider == nil {
		return EvalResult{Status: EvalUnavailable, Reason: "AI评估器不可用"}
	}
	cacheKey := f.generateTypedKey(sourceName, msg)
	if cached, ok := f.typedLRU.Get(cacheKey); ok {
		return cached.(EvalResult)
	}
	if !f.allowTypedEval(sourceName) {
		return EvalResult{Status: EvalRateLimited, Reason: "AI评分限流"}
	}
	evalCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := f.provider.Chat(evalCtx, []llm.Message{{Role: "user", Content: newsEvalPrompt(msg.Title, msg.Content, msg.Source)}})
	if err != nil {
		return EvalResult{Status: EvalProviderError, Reason: "AI评估失败"}
	}
	result := parseStrictEvalResponse(resp, f.config.ThresholdScore)
	if result.Status == EvalSuccess {
		result.Blocked = f.blockedCategory(result.Category)
		if result.Blocked {
			result.Passed = false
		}
		f.typedLRU.Set(cacheKey, result)
	}
	return result
}

func (f *AIFilter) allowTypedEval(sourceName string) bool {
	f.rlMu.Lock()
	defer f.rlMu.Unlock()
	now := time.Now()
	window := f.typedWindows[sourceName]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = typedRateWindow{started: now}
	}
	if window.count >= f.config.MaxPerMinute {
		return false
	}
	if now.Sub(f.rlWindowStart) >= time.Minute {
		f.rlWindowStart = now
		f.rlCount = 0
		f.rlWarned = false
	}
	if f.rlCount >= f.config.GlobalMaxPerMinute {
		return false
	}
	window.count++
	f.typedWindows[sourceName] = window
	f.rlCount++
	return true
}

func (f *AIFilter) generateTypedKey(sourceName string, msg *model.Message) string {
	identity := msg.ID
	if identity == "" {
		identity = msg.Title + "\x00" + msg.Content
	}
	hash := md5.Sum([]byte(sourceName + "\x00" + identity))
	return hex.EncodeToString(hash[:])
}

// allowEval 每分钟 LLM 评估次数固定窗口限流；超限时放行（fail-open），
// 每个窗口只 slog.Warn 一次，避免日志刷屏。
func (f *AIFilter) allowEval() bool {
	f.rlMu.Lock()
	defer f.rlMu.Unlock()

	now := time.Now()
	if now.Sub(f.legacyWindowStart) >= time.Minute {
		f.legacyWindowStart = now
		f.legacyCount = 0
	}
	if now.Sub(f.rlWindowStart) >= time.Minute {
		f.rlWindowStart = now
		f.rlCount = 0
		f.rlWarned = false
	}
	if f.legacyCount >= f.config.MaxPerMinute || f.rlCount >= f.config.GlobalMaxPerMinute {
		if !f.rlWarned {
			slog.Warn("ai_filter rate limit reached, passing through without evaluation", "max_per_minute", f.config.GlobalMaxPerMinute)
			f.rlWarned = true
		}
		return false
	}
	f.legacyCount++
	f.rlCount++
	return true
}

func (f *AIFilter) blockedCategory(category string) bool {
	for _, block := range f.config.BlockCategories {
		if block != "" && strings.Contains(category, block) {
			return true
		}
	}
	return false
}

// evaluate 使用 AI 评估新闻
func (f *AIFilter) evaluate(msg *model.Message) filterResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	prompt := newsEvalPrompt(msg.Title, msg.Content, msg.Source)

	resp, err := f.provider.Chat(ctx, []llm.Message{{Role: "user", Content: prompt}})
	if err != nil {
		slog.Warn("ai_filter eval failed, passing through", "error", err)
		return filterResult{score: f.config.ThresholdScore, category: "一般", reason: "AI评估失败", shouldPass: true, status: EvalProviderError}
	}
	return f.parseResponse(resp)
}

// newsEvalPrompt 构造 AI 评估提示词
// 阻塞式 ai_filter（ShouldFilter/evaluate）与异步 AsyncEvaluator（process）共用同一份评分标准，
// 避免两条评估链路的分数标准漂移。
func newsEvalPrompt(title, content, source string) string {
	return fmt.Sprintf(`定位: 你在为交易者过滤实时资讯，评估的是【市场影响/交易信息价值】，不是文风、来源正式性或表述是否夸张。

高分锚点 (8-10分): 央行决议/利率与流动性变化、重要经济数据及 vs 预期偏差、政要或监管的政策表态(关税/制裁/监管，即使是社交媒体言论或转述，只要可能影响市场定价)、重大地缘冲突升级、大型企业重大事件(财报暴雷/并购/事故)。
6-7分: 有交易参考价值的行业/个股实质进展、机构重要观点。
4-5分: 一般资讯，有背景但缺乏可交易性。
0-3分: 纯指数涨跌播报(无背景无增量)、榜单/荐股/标题党、广告与栏目预告、旧闻复述、与金融市场无关内容。

明确一句: 政治人物的政策威胁言论按其潜在市场影响评分，不要因"非官方来源"或"言论夸张"降分。

标题: %s
内容: %s
来源: %s

返回格式: 分数|分类|原因（一行，无其他内容）
分数: 0-10 (10=极重要)
分类: 非常重要/重要/一般/无关/无价值
原因: ≤20字

示例: 8|重要|影响科技行业
示例: 2|无价值|无实质内容`, title, content, source)
}

// parseResponse 解析 AI 响应
func (f *AIFilter) parseResponse(resp string) filterResult {
	result := parseStrictEvalResponse(resp, f.config.ThresholdScore)
	if result.Status != EvalSuccess {
		return filterResult{score: f.config.ThresholdScore, category: "一般", reason: result.Reason, shouldPass: true, status: result.Status}
	}

	for _, block := range f.config.BlockCategories {
		if strings.Contains(result.Category, block) {
			return filterResult{score: result.Score, category: result.Category, reason: result.Reason, shouldPass: false, status: EvalSuccess}
		}
	}
	return filterResult{score: result.Score, category: result.Category, reason: result.Reason, shouldPass: result.Score >= f.config.ThresholdScore, status: EvalSuccess}
}

func parseStrictEvalResponse(resp string, threshold float64) EvalResult {
	parts := splitPipe(resp)
	if len(parts) != 3 {
		return EvalResult{Status: EvalParseError, Reason: "解析失败"}
	}
	score, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil || math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 10 {
		return EvalResult{Status: EvalParseError, Reason: "评分解析失败"}
	}
	category := strings.TrimSpace(parts[1])
	reason := strings.TrimSpace(parts[2])
	if category == "" || reason == "" {
		return EvalResult{Status: EvalParseError, Reason: "解析失败"}
	}
	return EvalResult{Status: EvalSuccess, Score: score, Category: category, Reason: reason, Passed: score >= threshold}
}

func (f *AIFilter) generateKey(msg *model.Message) string {
	content := msg.Title
	if len(msg.Content) > 100 {
		content += msg.Content[:100]
	} else {
		content += msg.Content
	}
	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}

// GetStats 获取过滤统计
func (f *AIFilter) GetStats() map[string]interface{} {
	stats := f.lru.Stats()
	return map[string]interface{}{
		"cache_size": stats.Size,
		"hits":       stats.Hits,
		"misses":     stats.Misses,
		"hit_rate":   stats.HitRate,
		"enabled":    f.config.Enabled,
		"threshold":  f.config.ThresholdScore,
	}
}
