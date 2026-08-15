package filter

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-ego/gse"

	"github.com/Ricaardo/nimbus-os/news/internal/metrics"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

const unifiedDedupBucket = "unified_dedup"

// UnifiedDedupConfig 统一去重配置
type UnifiedDedupConfig struct {
	Enabled        bool       `yaml:"enabled"`
	TTL            int        `yaml:"ttl"`            // Stage 1+2 TTL (秒)
	DedupGroups    [][]string `yaml:"groups"`         // Stage 2 跨源去重组
	SkipSinks      []string   `yaml:"skip_sinks"`
	Semantic       SemanticSubConfig `yaml:"semantic"`
}

// SemanticSubConfig 语义去重子配置
type SemanticSubConfig struct {
	Enabled    bool    `yaml:"enabled"`
	Threshold  float64 `yaml:"threshold"`   // 相似度阈值 (0-1)
	TimeWindow int     `yaml:"time_window"` // 时间窗口（秒）
	CacheSize  int     `yaml:"cache_size"`  // 缓存容量
}

// UnifiedDedup 统一去重过滤器
// 三阶段级联: Exact ID → Content Hash → Semantic
type UnifiedDedup struct {
	st   store.Store
	ttl  time.Duration

	// Stage 2: cross-source groups
	dedupGroups  [][]string
	groupIndices map[string]int // source name → group index
	stage2Titles *stringLRU     // fingerprint key → title, block-log observability only (best-effort)

	// Stage 3: in-memory semantic cache
	seg          gse.Segmenter
	cache        *newsCache
	threshold    float64
	timeWindow   time.Duration
	titleCleanRE *regexp.Regexp // pre-compiled for contentFingerprint
	punctRE      *regexp.Regexp // pre-compiled for contentFingerprint

	skipSinks map[string]bool
	boltMu    sync.Mutex // protects stages 1+2 (BoltDB check-then-set)
	semMu     sync.Mutex // protects stage 3 (in-memory cache)
}

// NewUnifiedDedup 创建统一去重过滤器
func NewUnifiedDedup(s store.Store, cfg UnifiedDedupConfig) (*UnifiedDedup, error) {
	if cfg.TTL <= 0 {
		cfg.TTL = 86400
	}
	if cfg.Semantic.Threshold <= 0 {
		cfg.Semantic.Threshold = 0.6
	}
	if cfg.Semantic.TimeWindow <= 0 {
		cfg.Semantic.TimeWindow = 3600
	}
	if cfg.Semantic.CacheSize <= 0 {
		cfg.Semantic.CacheSize = 1000
	}

	u := &UnifiedDedup{
		st:           s,
		ttl:          time.Duration(cfg.TTL) * time.Second,
		dedupGroups:  cfg.DedupGroups,
		groupIndices: make(map[string]int),
		stage2Titles: newStringLRU(5000),
		threshold:    cfg.Semantic.Threshold,
		timeWindow:   time.Duration(cfg.Semantic.TimeWindow) * time.Second,
		skipSinks:    make(map[string]bool),
		titleCleanRE: regexp.MustCompile(`\s*[-|]\s*[A-Za-z]+\s*$`),
		punctRE:      regexp.MustCompile(`[^\p{L}\p{N}\s]`),
	}

	for _, sink := range cfg.SkipSinks {
		u.skipSinks[sink] = true
	}

	for i, group := range cfg.DedupGroups {
		for _, name := range group {
			u.groupIndices[name] = i
		}
	}

	if cfg.Semantic.Enabled {
		u.cache = newNewsCache(cfg.Semantic.CacheSize)
		_ = u.seg.LoadDict("zh") // load Chinese dict; non-fatal for English content
		_ = u.seg.LoadDict()     // also load default dict
	}

	return u, nil
}

func (u *UnifiedDedup) Name() string {
	return "unified_dedup"
}

// DigestSinkName 是 typed digest 路由使用的伪 sink(routing.go enqueueTypedDigest
// 引用此常量,避免字面量漂移)。
// digest lane 语义:跨源同题放行(多源角度进摘要,渲染时按 Topic 聚合),
// 因此跳过跨源内容指纹与语义去重,仅拦同源精确重复与同源同题。
const DigestSinkName = "__digest__"

// ShouldFilter 三阶段级联去重检查
func (u *UnifiedDedup) ShouldFilter(sourceName, sinkName string, msg *model.Message) bool {
	if u.skipSinks[sinkName] {
		return false
	}

	// Stage 1: Exact ID dedup (per source+sink)
	if u.stage1Exact(sourceName, sinkName, msg) {
		metrics.FilteredTotal.WithLabelValues("unified_dedup", sourceName, sinkName).Inc()
		return true
	}

	// 结构化报告（简报/观复/宏观/脚本等）每次定时运行都带唯一 ID，
	// Stage1 已能拦住真正的重发；但其正文与前一场次高度相似，若过内容/语义去重
	// 会被误判为重复而漏推。这类消息只做 Stage1 精确去重。
	if model.IsStructuredReport(msg.SourceType) {
		return false
	}

	digestLane := sinkName == DigestSinkName

	// Stage 2: Content hash dedup (cross-source within groups)
	// digest lane 只做同源同题去重(source-scoped),跨源同题放行给 Topic 聚合。
	if u.stage2Content(sourceName, sinkName, msg, digestLane) {
		metrics.FilteredTotal.WithLabelValues("unified_dedup", sourceName, sinkName).Inc()
		return true
	}

	// Stage 3: Semantic dedup (Jaccard similarity)
	// digest lane 跳过:跨源同题(相似标题)是摘要的多源价值,不应被语义去重误杀。
	if !digestLane && u.cache != nil && u.stage3Semantic(sourceName, sinkName, msg) {
		metrics.FilteredTotal.WithLabelValues("unified_dedup", sourceName, sinkName).Inc()
		return true
	}

	return false
}

// -- Stage 1: Exact ID-based dedup --

func (u *UnifiedDedup) stage1Exact(sourceName, sinkName string, msg *model.Message) bool {
	var id string
	if msg.ID != "" {
		id = msg.ID
	} else if msg.Link != "" {
		id = msg.Link
	} else {
		id = msg.Title
	}
	if id == "" {
		return false
	}

	key := fmt.Sprintf("e|%s|%s|%s", sourceName, sinkName, id)
	return u.checkAndSet(key)
}

// -- Stage 2: Content-hash dedup (cross-source within dedup groups) --

func (u *UnifiedDedup) stage2Content(sourceName, sinkName string, msg *model.Message, perSource bool) bool {
	groupIdx, ok := u.groupIndices[sourceName]
	if !ok {
		return false
	}

	fp := u.contentFingerprint(msg.Title)
	if fp == "" {
		return false
	}

	var key string
	if perSource {
		// digest lane:同源同题去重(跨源放行)
		key = fmt.Sprintf("c|%d|%s|%s|%s", groupIdx, sourceName, sinkName, fp)
	} else {
		key = fmt.Sprintf("c|%d|%s|%s", groupIdx, sinkName, fp)
	}
	blocked := u.checkAndSet(key)
	if blocked {
		prevTitle, _ := u.stage2Titles.get(key)
		slog.Info("unified_dedup blocked", "stage", "stage2_content", "source", sourceName, "sink", sinkName,
			"per_source", perSource, "title", truncate(msg.Title, 40), "matched_title", truncate(prevTitle, 40))
	} else {
		u.stage2Titles.set(key, msg.Title)
	}
	return blocked
}

func (u *UnifiedDedup) contentFingerprint(title string) string {
	title = html.UnescapeString(title)
	title = u.titleCleanRE.ReplaceAllString(title, "")
	cleaned := u.punctRE.ReplaceAllString(title, "")
	cleaned = strings.ToLower(strings.TrimSpace(cleaned))
	if cleaned == "" {
		return ""
	}

	segments := u.seg.Cut(cleaned, true)
	var processed []string
	for _, word := range segments {
		word = strings.TrimSpace(word)
		if word == "" || len(word) < 2 || isStopWord(word) {
			continue
		}
		processed = append(processed, word)
	}
	if len(processed) == 0 {
		return ""
	}

	data := strings.Join(processed, " ")
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// -- Stage 3: Semantic Jaccard dedup --

func (u *UnifiedDedup) stage3Semantic(sourceName, sinkName string, msg *model.Message) bool {
	u.semMu.Lock()
	defer u.semMu.Unlock()

	text := msg.Title + " " + msg.Content
	keywords := u.extractKeywords(text)
	if len(keywords) == 0 {
		return false
	}

	// cache key 包含 sinkName，避免不同 sink 互相干扰
	cacheID := sinkName + "|" + msg.ID

	now := time.Now()
	var foundSimilar bool
	var matchedTitle string
	var matchedSim float64
	u.cache.forEach(func(item *cachedNews) bool {
		if now.Sub(item.time) > u.timeWindow {
			return true
		}
		// 只与同一 sink 的缓存比较
		if !strings.HasPrefix(item.id, sinkName+"|") {
			return true
		}
		sim := u.jaccard(keywords, item.keywords)
		if sim >= u.threshold {
			foundSimilar = true
			matchedTitle = item.title
			matchedSim = sim
			return false
		}
		return true
	})

	u.cache.add(&cachedNews{
		id:       cacheID,
		title:    msg.Title,
		keywords: keywords,
		time:     now,
	})

	if foundSimilar {
		slog.Info("unified_dedup blocked", "stage", "stage3_semantic", "source", sourceName, "sink", sinkName,
			"jaccard", matchedSim, "title", truncate(msg.Title, 40), "matched_title", truncate(matchedTitle, 40))
	}

	return foundSimilar
}

func (u *UnifiedDedup) extractKeywords(text string) map[string]int {
	keywords := make(map[string]int)

	segments := u.seg.Segment([]byte(text))
	for _, seg := range segments {
		word := seg.Token().Text()
		if len([]rune(word)) >= 2 && !isStopWord(word) {
			keywords[strings.ToLower(word)]++
		}
	}

	// fallback: space-split for English
	if len(keywords) < 5 {
		for _, word := range strings.Fields(text) {
			word = strings.ToLower(strings.Trim(word, ".,!?\"'()[]{}"))
			if len(word) >= 3 && !isStopWord(word) {
				keywords[word]++
			}
		}
	}

	return keywords
}

func (u *UnifiedDedup) jaccard(a, b map[string]int) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	union := make(map[string]bool)
	for word := range a {
		union[word] = true
		if _, ok := b[word]; ok {
			intersection++
		}
	}
	for word := range b {
		union[word] = true
	}
	if len(union) == 0 {
		return 0
	}
	return float64(intersection) / float64(len(union))
}

// -- BoltDB helpers --

func (u *UnifiedDedup) checkAndSet(key string) bool {
	if u.st == nil {
		return false
	}
	u.boltMu.Lock()
	defer u.boltMu.Unlock()

	exists, err := u.st.Exists(unifiedDedupBucket, key)
	if err != nil {
		slog.Warn("unified_dedup check error", "error", err)
		return false
	}
	if exists {
		return true
	}
	if err := u.st.Set(unifiedDedupBucket, key, u.ttl); err != nil {
		slog.Warn("unified_dedup store error", "error", err)
	}
	return false
}

// -- LRU cache (moved from semantic_dedup.go) --

type newsCache struct {
	capacity int
	items    map[string]*list.Element
	order    *list.List
}

func newNewsCache(capacity int) *newsCache {
	return &newsCache{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

func (c *newsCache) add(news *cachedNews) {
	if elem, ok := c.items[news.id]; ok {
		c.order.MoveToFront(elem)
		elem.Value = news
		return
	}
	if c.order.Len() >= c.capacity {
		if oldest := c.order.Back(); oldest != nil {
			c.order.Remove(oldest)
			delete(c.items, oldest.Value.(*cachedNews).id)
		}
	}
	elem := c.order.PushFront(news)
	c.items[news.id] = elem
}

func (c *newsCache) forEach(fn func(*cachedNews) bool) {
	for e := c.order.Front(); e != nil; e = e.Next() {
		if !fn(e.Value.(*cachedNews)) {
			break
		}
	}
}

type cachedNews struct {
	id       string
	title    string
	keywords map[string]int
	time     time.Time
}

// -- Title LRU (stage2 block-log observability only, best-effort) --

type stringLRU struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	order    *list.List
}

type stringLRUEntry struct {
	key   string
	value string
}

func newStringLRU(capacity int) *stringLRU {
	return &stringLRU{
		capacity: capacity,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

func (c *stringLRU) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		return elem.Value.(*stringLRUEntry).value, true
	}
	return "", false
}

func (c *stringLRU) set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[key]; ok {
		c.order.MoveToFront(elem)
		elem.Value.(*stringLRUEntry).value = value
		return
	}
	if c.order.Len() >= c.capacity {
		if oldest := c.order.Back(); oldest != nil {
			c.order.Remove(oldest)
			delete(c.items, oldest.Value.(*stringLRUEntry).key)
		}
	}
	elem := c.order.PushFront(&stringLRUEntry{key: key, value: value})
	c.items[key] = elem
}

// -- Shared stop words --

var stopWords = map[string]bool{
	"的": true, "是": true, "在": true, "了": true, "和": true,
	"与": true, "对": true, "为": true, "将": true, "也": true,
	"有": true, "这": true, "个": true, "他": true, "她": true,
	"我": true, "你": true, "它": true, "们": true, "上": true,
	"下": true, "中": true, "来": true, "去": true, "到": true,
	"从": true, "以": true, "被": true, "把": true, "让": true,
	"给": true, "向": true, "等": true, "但": true, "而": true,
	"或": true, "如": true, "若": true, "因": true, "所": true,
	"其": true, "此": true, "那": true, "什么": true, "怎么": true,
	"没有": true, "可以": true, "这个": true, "那个": true, "还是": true,
	"the": true, "a": true, "an": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "must": true, "shall": true, "can": true,
	"this": true, "that": true, "these": true, "those": true, "it": true,
	"its": true, "of": true, "in": true, "to": true, "for": true,
	"with": true, "on": true, "at": true, "by": true, "from": true,
	"up": true, "about": true, "into": true, "over": true, "after": true,
	"and": true, "but": true, "or": true, "as": true, "if": true,
	"when": true, "than": true, "because": true, "while": true, "where": true,
	"how": true, "all": true, "each": true, "every": true, "both": true,
	"few": true, "more": true, "most": true, "other": true, "some": true,
	"such": true, "no": true, "nor": true, "not": true, "only": true,
	"own": true, "same": true, "so": true, "too": true, "very": true,
	"just": true, "also": true, "now": true, "here": true, "there": true,
	"trump": true, "musk": true, "elon": true, "donald": true,
}

func isStopWord(word string) bool {
	return stopWords[strings.ToLower(word)]
}
