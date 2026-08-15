package filter

import (
	"fmt"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"regexp"
	"strings"
)

// ContentFilter 内容过滤器
// 过滤无实质内容的消息（纯视频、图片、转发等）
type ContentFilter struct {
	// 最小内容长度
	minContentLength int
	// 需要过滤的关键词（正则）
	blockPatterns []*regexp.Regexp
	// 需要过滤的源（应用更严格的过滤）
	strictSources map[string]bool
}

// ContentFilterConfig 内容过滤器配置
type ContentFilterConfig struct {
	MinContentLength int      `yaml:"min_content_length"` // 最小内容长度
	BlockKeywords    []string `yaml:"block_keywords"`     // 屏蔽关键词
	StrictSources    []string `yaml:"strict_sources"`     // 严格过滤的源
}

// NewContentFilter 创建内容过滤器
func NewContentFilter(cfg ContentFilterConfig) *ContentFilter {
	f := &ContentFilter{
		minContentLength: cfg.MinContentLength,
		strictSources:    make(map[string]bool),
	}

	if f.minContentLength <= 0 {
		f.minContentLength = 10 // 默认最小10个字符
	}

	// 编译屏蔽关键词正则
	defaultPatterns := []string{
		`(?i)^https?://`,                          // 纯链接
		`(?i)^(rt|retweet)\s*[@:]`,                // 纯转发
		`(?i)^video\s*:`,                          // 视频标记
		`(?i)\[video\]`,                           // [video] 标签
		`(?i)\[image\]`,                           // [image] 标签
		`(?i)^pic\.twitter\.com`,                  // 纯图片链接
		`(?i)^(🎥|📹|🎬|📷|📸|🖼️)\s*$`,             // 纯媒体 emoji
		`(?i)^watch\s*:?\s*https?://`,             // Watch: URL
		`(?i)^live\s*:?\s*https?://`,              // Live: URL
		`(?i)^🔴\s*(live|breaking)\s*$`,           // 纯直播标记
	}

	// 合并自定义关键词
	allPatterns := append(defaultPatterns, cfg.BlockKeywords...)
	for _, pattern := range allPatterns {
		if re, err := regexp.Compile(pattern); err == nil {
			f.blockPatterns = append(f.blockPatterns, re)
		}
	}

	// 设置严格过滤源
	for _, src := range cfg.StrictSources {
		f.strictSources[src] = true
	}

	return f
}

// Name 返回过滤器名称
func (f *ContentFilter) Name() string {
	return "content"
}

// ShouldFilter 判断消息是否应该被过滤
func (f *ContentFilter) ShouldFilter(sourceName, sinkName string, msg *model.Message) bool {
	// 获取要检查的文本
	text := strings.TrimSpace(msg.Title)
	if text == "" {
		text = strings.TrimSpace(msg.Content)
	}

	// 清理 HTML 标签
	text = stripHTML(text)
	text = strings.TrimSpace(text)

	// 检查是否是严格过滤的源
	isStrict := f.strictSources[sourceName]

	// 1. 检查内容长度
	if len(text) < f.minContentLength {
		if isStrict {
			fmt.Printf("ContentFilter: filtered [%s] - too short (%d chars): %s\n",
				sourceName, len(text), truncate(text, 50))
			return true
		}
	}

	// 2. 检查屏蔽关键词
	for _, re := range f.blockPatterns {
		if re.MatchString(text) {
			fmt.Printf("ContentFilter: filtered [%s] - matched pattern: %s\n",
				sourceName, truncate(text, 50))
			return true
		}
	}

	// 3. 严格源的额外检查
	if isStrict {
		// 过滤纯 emoji 内容
		if isOnlyEmoji(text) {
			fmt.Printf("ContentFilter: filtered [%s] - only emoji: %s\n",
				sourceName, truncate(text, 50))
			return true
		}

		// 过滤纯数字/符号
		if isOnlyNumbersOrSymbols(text) {
			fmt.Printf("ContentFilter: filtered [%s] - only numbers/symbols: %s\n",
				sourceName, truncate(text, 50))
			return true
		}
	}

	return false
}

// stripHTML 移除 HTML 标签
func stripHTML(s string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	return re.ReplaceAllString(s, "")
}

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// isOnlyEmoji 检查是否只包含 emoji
func isOnlyEmoji(s string) bool {
	// 移除空格后检查
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return true
	}

	// emoji 范围的简化检查
	for _, r := range s {
		// 如果包含普通字母或数字，则不是纯 emoji
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			(r >= 0x4E00 && r <= 0x9FFF) { // 中文
			return false
		}
	}
	return true
}

// isOnlyNumbersOrSymbols 检查是否只包含数字和符号
func isOnlyNumbersOrSymbols(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}

	for _, r := range s {
		// 如果包含字母或中文，则不是纯数字/符号
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= 0x4E00 && r <= 0x9FFF) {
			return false
		}
	}
	return true
}
