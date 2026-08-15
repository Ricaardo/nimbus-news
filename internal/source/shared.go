package source

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// seenCache 源级别已发送消息去重缓存
type seenCache struct {
	mu      sync.RWMutex
	items   map[string]time.Time
	maxSize int
	ttl     time.Duration
}

func newSeenCache(maxSize int, ttl time.Duration) *seenCache {
	return &seenCache{
		items:   make(map[string]time.Time),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

func (c *seenCache) seen(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if len(c.items) > c.maxSize/2 {
		for k, t := range c.items {
			if now.Sub(t) > c.ttl {
				delete(c.items, k)
			}
		}
	}

	if _, exists := c.items[id]; exists {
		return true
	}
	c.items[id] = now
	return false
}

// hashFinancialNews 统一的 URL 哈希 ID 生成
func hashFinancialNews(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("fn_%x", h[:8])
}

// extractRealURL 从 Google News 跳转链接提取原始 URL
func extractRealURL(googleURL string) string {
	if !strings.Contains(googleURL, "news.google.com") {
		return googleURL
	}
	parsed, err := url.Parse(googleURL)
	if err != nil {
		return googleURL
	}
	if realURL := parsed.Query().Get("url"); realURL != "" {
		return realURL
	}
	return googleURL
}

// getStringOption 从配置 map 安全读取字符串
func getStringOption(m map[string]interface{}, key, defaultVal string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return defaultVal
}

// isSimilarText 判断 content 是否与 title 高度相似（用于去重展示）
func isSimilarText(title, content string) bool {
	if title == "" || content == "" {
		return false
	}
	normTitle := strings.ToLower(strings.TrimSpace(title))
	normContent := strings.ToLower(strings.TrimSpace(content))

	if normTitle == normContent {
		return true
	}
	if strings.HasPrefix(normContent, normTitle) {
		return true
	}
	if strings.HasPrefix(normTitle, normContent) {
		return true
	}

	re := regexp.MustCompile(`[^\p{L}\p{N}\s]`)
	cleanTitle := strings.Join(strings.Fields(re.ReplaceAllString(normTitle, "")), " ")
	cleanContent := strings.Join(strings.Fields(re.ReplaceAllString(normContent, "")), " ")
	if cleanTitle == cleanContent {
		return true
	}
	if strings.HasPrefix(cleanContent, cleanTitle) || strings.HasPrefix(cleanTitle, cleanContent) {
		return true
	}
	return false
}

// sortByTime 按时间倒序排序消息列表
func sortByTime(messages []*model.Message) {
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].CreateTime.After(messages[j].CreateTime)
	})
}
