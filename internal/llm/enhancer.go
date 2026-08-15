package llm

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// NewsEnhancer 新闻增强器（AI 点评）
type NewsEnhancer struct {
	provider Provider
	config   EnhancerConfig

	// 缓存：相似内容复用结果
	cache       map[string]cacheEntry
	cacheMu     sync.RWMutex
	cacheMaxAge time.Duration
}

// EnhanceResult 增强结果：译文（替换原标题/正文）+ 简评
type EnhanceResult struct {
	Title      string   // 中文标题译文
	Content    string   // 中文正文译文
	Comment    string   // 简评（利好/利空 + 板块/标的 + 影响）
	Tickers    []string // 原文实际出现的美股代码（gated 后）
	Impact     string   // 利好/利空/中性
	NotFinance bool     // 非财经噪音（true=丢弃）
}

// IsEmpty 判断结果是否为空（仅看 Title/Content/Comment，不变）
func (r EnhanceResult) IsEmpty() bool {
	return r.Title == "" && r.Content == "" && r.Comment == ""
}

// cacheEntry 缓存条目
type cacheEntry struct {
	result    EnhanceResult
	createdAt time.Time
}

// EnhancerConfig 增强器配置
type EnhancerConfig struct {
	Enabled    bool `yaml:"enabled"`
	MaxPerHour int  `yaml:"max_per_hour"` // 保留配置兼容性，但不再使用
}

// NewNewsEnhancer 创建新闻增强器
func NewNewsEnhancer(provider Provider, cfg EnhancerConfig) *NewsEnhancer {
	return &NewsEnhancer{
		provider:    provider,
		config:      cfg,
		cache:       make(map[string]cacheEntry),
		cacheMaxAge: 6 * time.Hour, // 缓存6小时
	}
}

// SetEnhanceConfig 热更新增强器配置
func (e *NewsEnhancer) SetEnhanceConfig(enabled bool, maxPerHour int) {
	e.config.Enabled = enabled
	if maxPerHour > 0 {
		e.config.MaxPerHour = maxPerHour
	}
}

// EnhanceBatch 批量并发增强新闻
func (e *NewsEnhancer) EnhanceBatch(ctx context.Context, newsList []*model.Message, concurrency int) {
	if !e.config.Enabled || len(newsList) == 0 {
		return
	}

	if concurrency <= 0 {
		concurrency = 5 // 默认并发数
	}

	// 清理过期缓存
	e.cleanExpiredCache()

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency) // 并发控制

	for _, news := range newsList {
		wg.Add(1)
		go func(msg *model.Message) {
			defer wg.Done()
			sem <- struct{}{}        // 获取信号量
			defer func() { <-sem }() // 释放信号量

			res, err := e.Enhance(ctx, msg)
			if err != nil || res.IsEmpty() {
				return
			}
			// 1) 相关性丢弃（fail-open：仅显式非财经才丢）
			if res.NotFinance {
				msg.SetMetadata("drop", "1")
				return
			}
			// 2) ticker Gate —— 在覆盖 msg.Title 之前用英文原文做匹配
			gated := gateTickers(msg.Title, msg.Content, res.Tickers)
			// 3) 译文替换（原有逻辑）
			// 模型按格式返回了译文（标题非空即视为已翻译）：译文为准，替换原英文标题与正文。
			// 此时正文译文为空（模型判定正文与标题重复/无正文）应清掉英文原文，避免中标题+英正文。
			// 原文已先存入 NewsStore，不丢。
			if res.Title != "" {
				msg.Title = res.Title
				msg.Content = res.Content
			}
			if res.Comment != "" {
				msg.SetAIComment(res.Comment)
			}
			// 4) 结构化字段写回（非结构化路径才覆盖 Tags；结构化报告保留原有 Tags）
			if !model.IsStructuredReport(msg.SourceType) {
				msg.Tags = gated
			}
			if res.Impact != "" {
				msg.SetMetadata("economic_impact", res.Impact)
			}
		}(news)
	}

	wg.Wait()
}

// Enhance 增强新闻（支持缓存）
//   - 结构化报告（宏观/观复/A股扫描等，已是中文）→ deepseek-v4-pro 快速解读影响（仅追加点评，不动正文）
//   - 普通外文新闻 → deepseek-v4-flash 翻译原文（替换标题/正文）+ 简评
func (e *NewsEnhancer) Enhance(ctx context.Context, news *model.Message) (EnhanceResult, error) {
	if !e.config.Enabled {
		return EnhanceResult{}, nil
	}

	// 生成缓存 key（基于标题和内容的 hash）
	cacheKey := e.generateCacheKey(news)

	// 检查缓存
	if res, ok := e.getFromCache(cacheKey); ok {
		return res, nil
	}

	var res EnhanceResult
	var err error
	if model.IsStructuredReport(news.SourceType) {
		res, err = e.assessReport(ctx, news)
	} else {
		res, err = e.translateAndComment(ctx, news)
	}
	if err != nil {
		return EnhanceResult{}, err
	}

	// 存入缓存
	e.saveToCache(cacheKey, res)

	return res, nil
}

// translateAndComment 普通新闻：flash 翻译原文 + 简评（译文替换标题/正文）
func (e *NewsEnhancer) translateAndComment(ctx context.Context, news *model.Message) (EnhanceResult, error) {
	// 五段标签输出：标题译文 / 正文译文 / 简评 / 代码 / 相关性。译文用于替换原英文标题与正文。
	prompt := fmt.Sprintf(`你是专业财经编辑。把下面这条新闻翻译成通顺中文，并给出简评。严格按以下五行标签格式输出，不要输出任何额外内容：
【标题】<标题中文翻译；本就是中文则原样>
【正文】<正文中文翻译，完整；无正文或与标题重复则输出 ->
【评】利好/利空/中性 | 板块: X | 标的: X | <1-2 句影响>
【代码】<原文中实际出现的美股代码，大写，逗号分隔，最多5个；必须是原文里出现的代码，不要写公司中文名、不要臆造；无则输出 ->
【相关】财经/非财经

标题: %s
内容: %s`, news.Title, news.Content)

	// 实时新闻：限时 20s，避免单条翻译卡住整批推送
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	resp, err := e.provider.Chat(ctx, []Message{{Role: "user", Content: prompt}})
	if err != nil {
		return EnhanceResult{}, err
	}

	res := parseEnhanceResponse(resp)
	// 解析全空（模型未按格式返回）时，把整段输出兜底当作简评，避免丢失信息
	if res.IsEmpty() && strings.TrimSpace(resp) != "" {
		res.Comment = strings.TrimSpace(resp)
	}
	return res, nil
}

// assessReport 结构化报告：deepseek-v4-pro 快速解读影响（只产出点评，不翻译/不改正文）
func (e *NewsEnhancer) assessReport(ctx context.Context, news *model.Message) (EnhanceResult, error) {
	prompt := fmt.Sprintf(`以下是已生成的中文财经报告（如宏观数据/观复 BTC 读盘/A股扫描等）。用 2-4 句给出快速解读，聚焦：核心信号、对市场或相关资产的影响、需要注意的点。直接给结论，不要复述原文，中文，简洁。

标题: %s
内容: %s`, news.Title, news.Content)

	// 报告解读：pro 非 thinking，限时 45s（报告低频、非实时，可宽松些）
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	// 用 pro 模型（heavy_model = deepseek-v4-pro）做快速解读：non-thinking，要判断力不要延迟
	content, err := e.provider.ChatHeavy(ctx, []Message{{Role: "user", Content: prompt}})
	if err != nil {
		return EnhanceResult{}, err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return EnhanceResult{}, nil
	}
	// 仅作为解读点评追加，不替换报告标题/正文；NotFinance 显式 false 确保结构化报告永不被丢
	return EnhanceResult{Comment: content, NotFinance: false}, nil
}

// parseEnhanceResponse 解析 deepseek 返回的【标题】/【正文】/【评】/【代码】/【相关】五段标签
func parseEnhanceResponse(resp string) EnhanceResult {
	const (
		tagTitle     = "【标题】"
		tagContent   = "【正文】"
		tagComment   = "【评】"
		tagTickers   = "【代码】"
		tagRelevance = "【相关】"
	)
	// 按标签出现顺序切片，取每个标签到下一个标签之间的文本
	type seg struct {
		tag string
		idx int
	}
	order := []seg{
		{tagTitle, strings.Index(resp, tagTitle)},
		{tagContent, strings.Index(resp, tagContent)},
		{tagComment, strings.Index(resp, tagComment)},
		{tagTickers, strings.Index(resp, tagTickers)},
		{tagRelevance, strings.Index(resp, tagRelevance)},
	}
	// 仅保留实际出现的标签，并按位置排序
	var present []seg
	for _, s := range order {
		if s.idx >= 0 {
			present = append(present, s)
		}
	}
	sort.Slice(present, func(i, j int) bool { return present[i].idx < present[j].idx })

	var res EnhanceResult
	for i, s := range present {
		start := s.idx + len(s.tag)
		end := len(resp)
		if i+1 < len(present) {
			end = present[i+1].idx
		}
		val := strings.TrimSpace(resp[start:end])
		switch s.tag {
		case tagTitle:
			res.Title = val
		case tagContent:
			if val != "-" && val != "" {
				res.Content = val
			}
		case tagComment:
			res.Comment = val
			res.Impact = parseImpact(val)
		case tagTickers:
			res.Tickers = parseTickerLine(val)
		case tagRelevance:
			// 必须先判"非财经"再判"财经"，因为"非财经"含"财经"
			res.NotFinance = strings.Contains(val, "非财经")
		}
	}
	return res
}

// parseTickerLine 解析【代码】行，返回去重、过滤后最多5个有效美股代码
func parseTickerLine(s string) []string {
	s = strings.TrimSpace(s)
	if s == "-" || s == "" {
		return nil
	}
	// 支持中英文逗号、顿号分割
	replacer := strings.NewReplacer("，", ",", "、", ",")
	s = replacer.Replace(s)
	parts := strings.Split(s, ",")

	tickerRe := regexp.MustCompile(`^[A-Z]{1,5}$`)
	seen := make(map[string]bool)
	var result []string
	for _, p := range parts {
		t := strings.ToUpper(strings.TrimSpace(p))
		if t == "" || t == "-" {
			continue
		}
		if !tickerRe.MatchString(t) {
			continue
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		result = append(result, t)
		if len(result) == 5 {
			break
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// parseImpact 从【评】的值提取利好/利空/中性枚举
func parseImpact(commentVal string) string {
	// 取第一个 | 之前的片段
	part := commentVal
	if idx := strings.Index(commentVal, "|"); idx >= 0 {
		part = commentVal[:idx]
	}
	part = strings.TrimSpace(part)
	// 按优先级匹配（均用 Contains 以兼容前缀或嵌入句子）
	switch {
	case strings.Contains(part, "利好"):
		return "利好"
	case strings.Contains(part, "利空"):
		return "利空"
	case strings.Contains(part, "中性"):
		return "中性"
	default:
		return ""
	}
}

// gateTickers 保留在 title+content 原文中实际出现的 ticker（词边界匹配，大小写不敏感）
// 使用词边界正则避免单字母 ticker（如 "F"）误匹配普通英文词（如 "for"）。
func gateTickers(title, content string, tickers []string) []string {
	if len(tickers) == 0 {
		return nil
	}
	orig := title + " " + content
	var result []string
	for _, t := range tickers {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(t) + `\b`)
		if re.MatchString(orig) {
			result = append(result, t)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// generateCacheKey 生成缓存 key
func (e *NewsEnhancer) generateCacheKey(news *model.Message) string {
	// 使用标题 + 内容前200字符生成 hash
	content := news.Title
	if len(news.Content) > 200 {
		content += news.Content[:200]
	} else {
		content += news.Content
	}
	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}

// getFromCache 从缓存获取
func (e *NewsEnhancer) getFromCache(key string) (EnhanceResult, bool) {
	e.cacheMu.RLock()
	defer e.cacheMu.RUnlock()

	entry, ok := e.cache[key]
	if !ok {
		return EnhanceResult{}, false
	}

	// 检查是否过期
	if time.Since(entry.createdAt) > e.cacheMaxAge {
		return EnhanceResult{}, false
	}

	return entry.result, true
}

// saveToCache 保存到缓存
func (e *NewsEnhancer) saveToCache(key string, result EnhanceResult) {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()

	e.cache[key] = cacheEntry{
		result:    result,
		createdAt: time.Now(),
	}
}

// cleanExpiredCache 清理过期缓存
func (e *NewsEnhancer) cleanExpiredCache() {
	e.cacheMu.Lock()
	defer e.cacheMu.Unlock()

	now := time.Now()
	for key, entry := range e.cache {
		if now.Sub(entry.createdAt) > e.cacheMaxAge {
			delete(e.cache, key)
		}
	}
}
