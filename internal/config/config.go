package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// SourceConfig 源配置
type SourceConfig struct {
	Name            string                 `yaml:"name" json:"name"`
	Type            string                 `yaml:"type" json:"type"`
	URL             string                 `yaml:"url" json:"url"`
	Interval        int                    `yaml:"interval" json:"interval"`                     // 间隔调度（秒）
	Schedule        []string               `yaml:"schedule,omitempty" json:"schedule,omitempty"` // 定时调度（HH:MM格式）
	Sinks           []string               `yaml:"sinks" json:"sinks"`
	DeliveryMode    string                 `yaml:"delivery_mode,omitempty" json:"delivery_mode,omitempty"`
	BriefingTarget  string                 `yaml:"briefing_target,omitempty" json:"briefing_target,omitempty"`
	Routing         *SourceRoutingConfig   `yaml:"routing,omitempty" json:"routing,omitempty"`
	Enabled         *bool                  `yaml:"enabled,omitempty" json:"enabled,omitempty"`                     // 是否启用，默认 true
	TradingDaysOnly bool                   `yaml:"trading_days_only,omitempty" json:"trading_days_only,omitempty"` // 跳过非交易日
	Options         map[string]interface{} `yaml:"options" json:"options,omitempty"`
}

// SourceRoutingConfig defines score-aware routing for a source. A nil Routing
// on SourceConfig preserves the legacy delivery_mode behavior.
type SourceRoutingConfig struct {
	Critical         bool       `yaml:"critical,omitempty" json:"critical,omitempty"`
	CriticalKeywords []string   `yaml:"critical_keywords,omitempty" json:"critical_keywords,omitempty"`
	AIPolicy         string     `yaml:"ai_policy" json:"ai_policy"`
	OnAIFailure      string     `yaml:"on_ai_failure" json:"on_ai_failure"`
	Direct           *RouteBand `yaml:"direct,omitempty" json:"direct,omitempty"`
	Digest           *RouteBand `yaml:"digest,omitempty" json:"digest,omitempty"`
	Default          string     `yaml:"default" json:"default"`
}

// RouteBand selects a route and applies its quota and digest metadata.
type RouteBand struct {
	MinScore       *float64 `yaml:"min_score,omitempty" json:"min_score,omitempty"`
	MaxPerDay      int      `yaml:"max_per_day" json:"max_per_day"`
	Priority       int      `yaml:"priority,omitempty" json:"priority,omitempty"`
	BriefingTarget string   `yaml:"briefing_target,omitempty" json:"briefing_target,omitempty"`
}

const (
	AIPolicyBypass   = "bypass"
	AIPolicyRequired = "required"

	RouteDefaultDigest = "digest"
	RouteDefaultSilent = "silent"
	RouteDefaultDirect = "direct"
)

// DeepCopy returns a detached routing configuration for runtime consumers.
func (r *SourceRoutingConfig) DeepCopy() *SourceRoutingConfig {
	if r == nil {
		return nil
	}
	cloned := *r
	cloned.CriticalKeywords = append([]string(nil), r.CriticalKeywords...)
	cloned.Direct = r.Direct.deepCopy()
	cloned.Digest = r.Digest.deepCopy()
	return &cloned
}

func (b *RouteBand) deepCopy() *RouteBand {
	if b == nil {
		return nil
	}
	cloned := *b
	if b.MinScore != nil {
		score := *b.MinScore
		cloned.MinScore = &score
	}
	return &cloned
}

const (
	DeliveryDirect = "direct"
	DeliveryDigest = "digest"
	DeliverySilent = "silent"
)

// IsEnabled 检查数据源是否启用
func (s *SourceConfig) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// SinkConfig 目标配置
type SinkConfig struct {
	Name    string                 `yaml:"name"`
	Type    string                 `yaml:"type"`
	Webhook string                 `yaml:"webhook"`
	Options map[string]interface{} `yaml:"options"`
}

// Config 应用配置
type Config struct {
	Sources  []SourceConfig `yaml:"sources"`
	Sinks    []SinkConfig   `yaml:"sinks"`
	Filters  FiltersConfig  `yaml:"filters"`
	Store    StoreConfig    `yaml:"store"`
	Investor InvestorConfig `yaml:"investor"`
}

// FiltersConfig 过滤器配置
type FiltersConfig struct {
	Dedup     DedupConfig     `yaml:"dedup"`
	Ratelimit RatelimitConfig `yaml:"ratelimit"`
	Content   ContentConfig   `yaml:"content"`
	AIFilter  AIFilterConfig  `yaml:"ai_filter"`
}

// AIFilterConfig AI 过滤器配置
type AIFilterConfig struct {
	Enabled            bool     `yaml:"enabled"`
	ThresholdScore     float64  `yaml:"threshold_score"` // 过滤阈值，低于此分数的新闻将被过滤 (0-10)
	MaxPerMinute       int      `yaml:"max_per_minute"`  // 每分钟最多处理数量
	GlobalMaxPerMinute int      `yaml:"global_max_per_minute"`
	BlockCategories    []string `yaml:"block_categories"` // 直接屏蔽的分类
	TargetSources      []string `yaml:"target_sources"`   // 需要 AI 过滤的源列表，为空表示所有源
}

// DedupSubSemanticConfig 语义去重子配置
type DedupSubSemanticConfig struct {
	Enabled    bool    `yaml:"enabled"`
	Threshold  float64 `yaml:"threshold"`
	TimeWindow int     `yaml:"time_window"`
	CacheSize  int     `yaml:"cache_size"`
}

// ContentConfig 内容过滤配置
type ContentConfig struct {
	Enabled          bool     `yaml:"enabled"`
	MinContentLength int      `yaml:"min_content_length"` // 最小内容长度
	BlockKeywords    []string `yaml:"block_keywords"`     // 屏蔽关键词（正则）
	StrictSources    []string `yaml:"strict_sources"`     // 严格过滤的源
}

// DedupConfig 统一去重配置
type DedupConfig struct {
	Enabled   bool                   `yaml:"enabled"`
	TTL       int                    `yaml:"ttl"`    // Stage 1+2 TTL (秒)
	Groups    [][]string             `yaml:"groups"` // Stage 2 跨源去重组
	SkipSinks []string               `yaml:"skip_sinks"`
	Semantic  DedupSubSemanticConfig `yaml:"semantic"` // Stage 3 语义去重
}

// RatelimitConfig 频控配置
type RatelimitConfig struct {
	Enabled bool            `yaml:"enabled"`
	Rules   []RatelimitRule `yaml:"rules"`
}

// RatelimitRule 频控规则
type RatelimitRule struct {
	Source       string `yaml:"source"`         // 源名称，* 表示所有
	Sink         string `yaml:"sink"`           // 目标名称，* 表示所有
	MaxPerMinute int    `yaml:"max_per_minute"` // 每分钟最大消息数
}

// StoreConfig 存储配置
type StoreConfig struct {
	Type string `yaml:"type"` // bolt/memory
	Path string `yaml:"path"` // 数据文件路径
}

// InvestorConfig AI投资人自动运行配置
type InvestorConfig struct {
	Enabled   bool                     `yaml:"enabled"`   // 是否启用
	Mode      string                   `yaml:"mode"`      // 运行模式: discovery(自主选标的)/portfolio(组合分析)/analyze(分析单个标的)
	Schedule  []string                 `yaml:"schedule"`  // 运行时间点 (HH:MM格式)
	Investors []InvestorScheduleConfig `yaml:"investors"` // 投资人配置列表
	Symbols   []string                 `yaml:"symbols"`   // 默认分析的标的列表
	Channels  []string                 `yaml:"channels"`  // 推送渠道
}

// InvestorScheduleConfig 单个投资人配置
type InvestorScheduleConfig struct {
	ID      string   `yaml:"id"`      // 投资人ID: buffett, soros, munger, dalio, lynch
	Symbols []string `yaml:"symbols"` // 该投资人分析的标的，为空则使用默认
	Mode    string   `yaml:"mode"`    // 该投资人的运行模式
}

// Load 从文件加载配置
func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()

	var cfg Config
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config file: %w", err)
	}

	// 设置默认值
	cfg.setDefaults()

	return &cfg, nil
}

// setDefaults 设置默认值
func (c *Config) setDefaults() {
	// 源默认抓取间隔
	for i := range c.Sources {
		normalizeSourceRouting(c.Sources[i].Routing)
		if c.Sources[i].Interval <= 0 {
			c.Sources[i].Interval = 120
		}
		if c.Sources[i].Routing == nil && c.Sources[i].DeliveryMode == "" {
			c.Sources[i].DeliveryMode = DeliveryDirect
		}
	}

	// 去重默认TTL
	if c.Filters.Dedup.TTL <= 0 {
		c.Filters.Dedup.TTL = 86400 // 24小时
	}

	// 存储默认配置
	if c.Store.Type == "" {
		c.Store.Type = "bolt"
	}
	if c.Store.Path == "" {
		c.Store.Path = "data/news.db"
	}
}

// GetSourceConfig 获取指定名称的源配置
func (c *Config) GetSourceConfig(name string) *SourceConfig {
	for i := range c.Sources {
		if c.Sources[i].Name == name {
			return &c.Sources[i]
		}
	}
	return nil
}

// GetSinkConfig 获取指定名称的目标配置
func (c *Config) GetSinkConfig(name string) *SinkConfig {
	for i := range c.Sinks {
		if c.Sinks[i].Name == name {
			return &c.Sinks[i]
		}
	}
	return nil
}

// Validate 验证配置
func (c *Config) Validate() error {
	// 验证源名称唯一性
	sourceNames := make(map[string]bool)
	for _, src := range c.Sources {
		if src.Name == "" {
			return fmt.Errorf("source name cannot be empty")
		}
		if sourceNames[src.Name] {
			return fmt.Errorf("duplicate source name: %s", src.Name)
		}
		sourceNames[src.Name] = true
		if err := validateDelivery(src); err != nil {
			return err
		}
	}

	// 验证目标名称唯一性
	sinkNames := make(map[string]bool)
	for _, s := range c.Sinks {
		if s.Name == "" {
			return fmt.Errorf("sink name cannot be empty")
		}
		if sinkNames[s.Name] {
			return fmt.Errorf("duplicate sink name: %s", s.Name)
		}
		sinkNames[s.Name] = true
	}

	// 验证源引用的目标存在
	for _, src := range c.Sources {
		for _, sinkName := range src.Sinks {
			if !sinkNames[sinkName] {
				return fmt.Errorf("source %s references unknown sink: %s", src.Name, sinkName)
			}
		}
	}

	return nil
}

func validateDelivery(src SourceConfig) error {
	if err := validateSourceOptions(src.Name, src.Options); err != nil {
		return err
	}
	if src.Routing != nil {
		if src.DeliveryMode != "" || src.BriefingTarget != "" {
			return fmt.Errorf("source %s: routing conflicts with legacy delivery_mode/briefing_target", src.Name)
		}
		return validateSourceRouting(src.Name, src.Routing)
	}
	switch src.DeliveryMode {
	case DeliveryDirect, DeliverySilent:
		if src.BriefingTarget != "" {
			return fmt.Errorf("source %s: briefing_target requires digest delivery", src.Name)
		}
	case DeliveryDigest:
		switch src.BriefingTarget {
		case "pre_market", "closing", "us_preview", "news_aggregate":
		default:
			return fmt.Errorf("source %s: invalid briefing_target %q", src.Name, src.BriefingTarget)
		}
	default:
		return fmt.Errorf("source %s: invalid delivery_mode %q", src.Name, src.DeliveryMode)
	}
	return nil
}

func validateSourceOptions(source string, options map[string]interface{}) error {
	var walk func(string, interface{}) error
	walk = func(key string, value interface{}) error {
		lower := strings.ToLower(strings.TrimSpace(key))
		if isSensitiveOptionKey(lower) {
			if text, ok := value.(string); ok && strings.TrimSpace(text) == "***" {
				return fmt.Errorf("source %s: redacted value for option %q cannot be persisted", source, key)
			}
		}
		if lower == "command" || lower == "args" {
			if commandValueContainsCredentialArg(value) {
				return fmt.Errorf("source %s: option %q cannot contain credential arguments", source, key)
			}
		}
		switch typed := value.(type) {
		case map[string]interface{}:
			for nestedKey, nested := range typed {
				if err := walk(nestedKey, nested); err != nil {
					return err
				}
			}
		case []interface{}:
			for _, nested := range typed {
				if err := walk(key, nested); err != nil {
					return err
				}
			}
		case []string:
			for _, nested := range typed {
				if err := walk(key, nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for key, value := range options {
		if err := walk(key, value); err != nil {
			return err
		}
	}
	return nil
}

func isSensitiveOptionKey(lower string) bool {
	return strings.Contains(lower, "secret") || strings.Contains(lower, "token") ||
		strings.Contains(lower, "password") || strings.Contains(lower, "authorization") ||
		lower == "webhook" || lower == "webhooks" || strings.HasPrefix(lower, "webhook_") ||
		strings.HasSuffix(lower, "_webhook") || strings.Contains(lower, "api_key") ||
		strings.Contains(lower, "apikey") || lower == "key" || strings.HasSuffix(lower, "_key") ||
		strings.HasSuffix(lower, "-key")
}

func commandValueContainsCredentialArg(value interface{}) bool {
	var values []string
	switch typed := value.(type) {
	case string:
		values = []string{typed}
	case []string:
		values = typed
	case []interface{}:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	}
	for _, value := range values {
		for _, token := range strings.Fields(strings.ToLower(value)) {
			name := strings.TrimLeft(strings.SplitN(token, "=", 2)[0], "-")
			name = strings.ReplaceAll(name, "-", "_")
			if name == "api_key" || name == "apikey" || name == "access_token" ||
				name == "token" || name == "secret" || name == "password" || name == "webhook" {
				return true
			}
			if strings.Contains(token, "?api_key=") || strings.Contains(token, "&api_key=") ||
				strings.Contains(token, "?token=") || strings.Contains(token, "&token=") {
				return true
			}
		}
	}
	return false
}

// ValidateSourceRouting validates a typed routing policy for API and runtime callers.
func ValidateSourceRouting(source string, routing *SourceRoutingConfig) error {
	return validateSourceRouting(source, routing)
}

func validateSourceRouting(source string, routing *SourceRoutingConfig) error {
	if routing == nil {
		return nil
	}
	if routing.AIPolicy != AIPolicyBypass && routing.AIPolicy != AIPolicyRequired {
		return fmt.Errorf("source %s: invalid routing ai_policy %q", source, routing.AIPolicy)
	}
	if len(routing.CriticalKeywords) > 64 {
		return fmt.Errorf("source %s: routing critical_keywords cannot exceed 64 entries", source)
	}
	for _, keyword := range routing.CriticalKeywords {
		if keyword == "" {
			return fmt.Errorf("source %s: routing critical_keywords cannot contain empty values", source)
		}
		if utf8.RuneCountInString(keyword) > 128 {
			return fmt.Errorf("source %s: routing critical keyword cannot exceed 128 characters", source)
		}
	}
	if routing.Direct != nil && !routing.Critical && routing.AIPolicy != AIPolicyRequired &&
		routing.Default != RouteDefaultDirect {
		return fmt.Errorf("source %s: non-critical direct routing requires ai_policy required", source)
	}
	if routing.OnAIFailure != RouteDefaultDigest && routing.OnAIFailure != RouteDefaultSilent {
		return fmt.Errorf("source %s: invalid routing on_ai_failure %q", source, routing.OnAIFailure)
	}
	if routing.Default != RouteDefaultDigest && routing.Default != RouteDefaultSilent && routing.Default != RouteDefaultDirect {
		return fmt.Errorf("source %s: invalid routing default %q", source, routing.Default)
	}
	if routing.Direct == nil && routing.Digest == nil &&
		(routing.Default != RouteDefaultSilent || routing.OnAIFailure != RouteDefaultSilent) {
		return fmt.Errorf("source %s: routing without bands must default and fail silently", source)
	}
	if err := validateRouteBand(source, "direct", routing.Direct, false, routing.AIPolicy == AIPolicyRequired); err != nil {
		return err
	}
	if err := validateRouteBand(source, "digest", routing.Digest, true, routing.AIPolicy == AIPolicyRequired); err != nil {
		return err
	}
	if routing.Default == RouteDefaultDigest && routing.Digest == nil {
		return fmt.Errorf("source %s: routing default digest requires a digest band", source)
	}
	if routing.Default == RouteDefaultDirect && routing.Direct == nil {
		return fmt.Errorf("source %s: routing default direct requires a direct band", source)
	}
	if routing.OnAIFailure == RouteDefaultDigest && routing.Digest == nil {
		return fmt.Errorf("source %s: routing on_ai_failure digest requires a digest band", source)
	}
	return nil
}

func normalizeSourceRouting(routing *SourceRoutingConfig) {
	if routing == nil || len(routing.CriticalKeywords) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(routing.CriticalKeywords))
	keywords := routing.CriticalKeywords[:0]
	for _, keyword := range routing.CriticalKeywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}
		key := strings.ToLower(keyword)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keywords = append(keywords, keyword)
	}
	routing.CriticalKeywords = keywords
}

func validateRouteBand(source, name string, band *RouteBand, digest, scoreRequired bool) error {
	if band == nil {
		return nil
	}
	if band.MinScore != nil && (*band.MinScore < 0 || *band.MinScore > 10) {
		return fmt.Errorf("source %s: routing %s min_score must be between 0 and 10", source, name)
	}
	if scoreRequired && band.MinScore == nil {
		return fmt.Errorf("source %s: routing %s min_score is required by ai_policy", source, name)
	}
	if band.MaxPerDay < 0 {
		return fmt.Errorf("source %s: routing %s max_per_day cannot be negative", source, name)
	}
	if band.Priority < -100 || band.Priority > 100 {
		return fmt.Errorf("source %s: routing %s priority must be between -100 and 100", source, name)
	}
	if !digest {
		if band.BriefingTarget != "" {
			return fmt.Errorf("source %s: routing direct briefing_target is not allowed", source)
		}
		return nil
	}
	switch band.BriefingTarget {
	case "pre_market", "closing", "us_preview", "news_aggregate":
		return nil
	default:
		return fmt.Errorf("source %s: invalid routing digest briefing_target %q", source, band.BriefingTarget)
	}
}

// Print 打印配置信息
func (c *Config) Print() {
	slog.Info("loaded config", "sources", len(c.Sources), "sinks", len(c.Sinks))
	for _, src := range c.Sources {
		slog.Debug("source config", "name", src.Name, "type", src.Type, "interval", src.Interval, "sinks", src.Sinks)
	}
	for _, s := range c.Sinks {
		slog.Debug("sink config", "name", s.Name, "type", s.Type)
	}
}
