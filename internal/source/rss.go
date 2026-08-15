package source

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html/charset"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
	xhtml "golang.org/x/net/html"
)

func init() {
	Register("rss", NewRSSSource)
}

const defaultRSSMaxResponseBytes int64 = 4 << 20

// RSSSource RSS 数据源
type RSSSource struct {
	name                string
	url                 string
	allowedSources      []string // 允许的来源域名（如 reuters.com, bloomberg.com）
	requireKeywords     []string // 必须包含的关键词（标题或内容需匹配其一）
	excludeKeywords     []string
	excludeLinkPatterns []string
	excludeVideoOnly    bool
	excludeReposts      bool
	fetchLinkedBody     bool
	linkedBodyAppend    bool // append linked body instead of replacing content
	linkedBodyHosts     map[string]struct{}
	linkedBodyMaxChars  int
	isGoogleNews        bool          // 是否是 Google News RSS
	maxAge              time.Duration // 新闻最大时效（默认24小时）
	maxResponseBytes    int64
	tags                []string // per-source category tags injected into every message
	httpClient          *http.Client

	// 条件请求(ETag/Last-Modified):上游未变化时 304 免下载。
	// 高频轮询源(300s×288轮/天)绝大多数轮次内容不变,可省 ~97% 流量与解析。
	condMu        sync.Mutex
	etag          string
	lastModified  string
	notModifiedSkips int64 // 可观测:304 跳过次数
}

// NewRSSSource 创建 RSS 源
func NewRSSSource(cfg Config) (Source, error) {
	source := &RSSSource{
		name:               cfg.Name,
		url:                cfg.URL,
		isGoogleNews:       strings.Contains(cfg.URL, "news.google.com"),
		maxAge:             24 * time.Hour, // 默认24小时
		maxResponseBytes:   defaultRSSMaxResponseBytes,
		linkedBodyHosts:    make(map[string]struct{}),
		linkedBodyMaxChars: 12000,
	}

	// 解析 allowed_sources 配置
	if allowedRaw, ok := cfg.Options["allowed_sources"]; ok {
		if allowedList, ok := allowedRaw.([]interface{}); ok {
			for _, s := range allowedList {
				if str, ok := s.(string); ok {
					source.allowedSources = append(source.allowedSources, strings.ToLower(str))
				}
			}
		}
	}

	// 解析 max_age 配置（单位：小时）
	if maxAgeRaw, ok := cfg.Options["max_age_hours"]; ok {
		switch v := maxAgeRaw.(type) {
		case int:
			source.maxAge = time.Duration(v) * time.Hour
		case float64:
			source.maxAge = time.Duration(v) * time.Hour
		}
	}

	// 解析 require_keywords 配置
	if keywordsRaw, ok := cfg.Options["require_keywords"]; ok {
		if keywordList, ok := keywordsRaw.([]interface{}); ok {
			for _, k := range keywordList {
				if str, ok := k.(string); ok {
					source.requireKeywords = append(source.requireKeywords, strings.ToLower(str))
				}
			}
		}
	}

	source.excludeVideoOnly = optionBool(cfg.Options, "exclude_video_only")
	source.excludeReposts = optionBool(cfg.Options, "exclude_reposts")
	source.fetchLinkedBody = optionBool(cfg.Options, "fetch_linked_body")
	source.linkedBodyAppend = optionBool(cfg.Options, "linked_body_append")
	source.excludeLinkPatterns = optionStrings(cfg.Options, "exclude_link_patterns")
	source.excludeKeywords = optionStrings(cfg.Options, "exclude_keywords")
	source.tags = optionTags(cfg.Options, "tags")
	for _, host := range optionStrings(cfg.Options, "linked_body_hosts") {
		source.linkedBodyHosts[host] = struct{}{}
	}
	if n := optionInt(cfg.Options, "linked_body_max_chars"); n > 0 {
		source.linkedBodyMaxChars = n
	}
	if n := optionInt(cfg.Options, "max_response_bytes"); n > 0 {
		source.maxResponseBytes = int64(n)
	}

	return source, nil
}

func optionBool(options map[string]interface{}, key string) bool {
	v, _ := options[key].(bool)
	return v
}

func optionStrings(options map[string]interface{}, key string) []string {
	var result []string
	switch values := options[key].(type) {
	case []interface{}:
		for _, value := range values {
			if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
				result = append(result, strings.ToLower(strings.TrimSpace(s)))
			}
		}
	case []string:
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				result = append(result, strings.ToLower(strings.TrimSpace(value)))
			}
		}
	}
	return result
}

// optionTags is like optionStrings but preserves original case (for display tags).
func optionTags(options map[string]interface{}, key string) []string {
	var result []string
	switch values := options[key].(type) {
	case []interface{}:
		for _, value := range values {
			if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
				result = append(result, strings.TrimSpace(s))
			}
		}
	case []string:
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				result = append(result, strings.TrimSpace(value))
			}
		}
	}
	return result
}

func optionInt(options map[string]interface{}, key string) int {
	switch v := options[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}

func (r *RSSSource) Name() string { return r.name }
func (r *RSSSource) Type() string { return "rss" }

// 来源关键字映射：将配置的标识符映射到可能出现的各种形式
var sourceKeywordMap = map[string][]string{
	"reuters.com":   {"reuters", "reuters.com"},
	"bloomberg.com": {"bloomberg", "bloomberg.com", "bloomberg news"},
	"x.com":         {"x.com", "twitter.com", "twitter", " x ", "@"},
}

// isSourceAllowed 检查来源是否在允许列表中
func (r *RSSSource) isSourceAllowed(sourceName, sourceURL, link string) bool {
	// 合并所有来源信息用于检查
	sourceToCheck := strings.ToLower(sourceName + " " + sourceURL + " " + link)

	for _, allowedSource := range r.allowedSources {
		// 首先尝试直接匹配
		if strings.Contains(sourceToCheck, allowedSource) {
			return true
		}

		// 然后尝试关键字映射匹配
		if keywords, ok := sourceKeywordMap[allowedSource]; ok {
			for _, keyword := range keywords {
				if strings.Contains(sourceToCheck, keyword) {
					return true
				}
			}
		}
	}

	return false
}

// matchesRequiredKeywords 检查内容是否包含任一必需关键词
func (r *RSSSource) matchesRequiredKeywords(title, content string) bool {
	if len(r.requireKeywords) == 0 {
		return true // 未配置关键词则全部通过
	}

	textToCheck := strings.ToLower(title + " " + content)
	for _, keyword := range r.requireKeywords {
		if strings.Contains(textToCheck, keyword) {
			return true
		}
	}
	return false
}

type rssMedia struct {
	URL    string
	Type   string
	Medium string
}

type rssItem struct {
	Title          string
	Link           string
	Description    string
	ContentEncoded string
	GUID           string
	PubDate        string
	Category       string
	Author         string
	Source         struct {
		Name string `xml:",chardata"`
		URL  string `xml:"url,attr"`
	}
	Enclosures      []rssMedia
	MediaContents   []rssMedia
	MediaThumbnails []rssMedia
}

func (item *rssItem) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if end, ok := token.(xml.EndElement); ok && end.Name == start.Name {
			return nil
		}
		element, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch element.Name.Local {
		case "title":
			if err := decoder.DecodeElement(&item.Title, &element); err != nil {
				return err
			}
		case "link":
			if err := decoder.DecodeElement(&item.Link, &element); err != nil {
				return err
			}
		case "description":
			if err := decoder.DecodeElement(&item.Description, &element); err != nil {
				return err
			}
		case "encoded":
			if err := decoder.DecodeElement(&item.ContentEncoded, &element); err != nil {
				return err
			}
		case "guid":
			if err := decoder.DecodeElement(&item.GUID, &element); err != nil {
				return err
			}
		case "pubDate":
			if err := decoder.DecodeElement(&item.PubDate, &element); err != nil {
				return err
			}
		case "category":
			if err := decoder.DecodeElement(&item.Category, &element); err != nil {
				return err
			}
		case "author":
			if err := decoder.DecodeElement(&item.Author, &element); err != nil {
				return err
			}
		case "source":
			if err := decoder.DecodeElement(&item.Source, &element); err != nil {
				return err
			}
		case "enclosure":
			item.Enclosures = append(item.Enclosures, mediaFromAttrs(element.Attr))
			if err := decoder.Skip(); err != nil {
				return err
			}
		case "content":
			item.MediaContents = append(item.MediaContents, mediaFromAttrs(element.Attr))
			if err := decoder.Skip(); err != nil {
				return err
			}
		case "thumbnail":
			item.MediaThumbnails = append(item.MediaThumbnails, mediaFromAttrs(element.Attr))
			if err := decoder.Skip(); err != nil {
				return err
			}
		default:
			if err := decoder.Skip(); err != nil {
				return err
			}
		}
	}
}

func mediaFromAttrs(attrs []xml.Attr) rssMedia {
	var media rssMedia
	for _, attr := range attrs {
		switch attr.Name.Local {
		case "url":
			media.URL = strings.TrimSpace(attr.Value)
		case "type":
			media.Type = strings.ToLower(attr.Value)
		case "medium":
			media.Medium = strings.ToLower(attr.Value)
		}
	}
	return media
}

// Fetch 抓取 RSS 数据
func (r *RSSSource) Fetch() ([]*model.Message, error) {
	req, err := http.NewRequest("GET", r.url, nil)
	if err != nil {
		return nil, fmt.Errorf("RSS request creation failed: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	// 条件请求:带上上次响应的 ETag/Last-Modified,内容未变时上游返回 304 免下载。
	r.condMu.Lock()
	if r.etag != "" {
		req.Header.Set("If-None-Match", r.etag)
	}
	if r.lastModified != "" {
		req.Header.Set("If-Modified-Since", r.lastModified)
	}
	r.condMu.Unlock()

	client := r.httpClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("RSS request failed: %w", err)
	}
	defer resp.Body.Close()

	// 304 Not Modified:内容未变化,无新条目。
	if resp.StatusCode == http.StatusNotModified {
		r.condMu.Lock()
		r.notModifiedSkips++
		skips := r.notModifiedSkips
		r.condMu.Unlock()
		// Info 级别:304 是常态路径(如 fed-press 周末 96 轮/天有 95 轮 304),
		// Debug 级别会让优化不可观测。
		slog.Info("rss 304 not modified", "source", r.name, "skips", skips)
		return nil, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("RSS request returned HTTP %d", resp.StatusCode)
	}

	// 记录验证器供下一轮条件请求
	r.condMu.Lock()
	if et := resp.Header.Get("ETag"); et != "" {
		r.etag = et
	}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		r.lastModified = lm
	}
	r.condMu.Unlock()

	maxResponseBytes := r.maxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultRSSMaxResponseBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read RSS response failed: %w", err)
	}
	if int64(len(body)) > maxResponseBytes {
		return nil, fmt.Errorf("RSS response exceeds %d byte limit", maxResponseBytes)
	}

	var rss struct {
		Items []rssItem `xml:"channel>item"`
	}
	// CharsetReader:部分源声明非 UTF-8 编码(EIA 为 ISO-8859-1),
	// encoding/xml 裸 Unmarshal 会因 CharsetReader=nil 直接报错。
	decoder := xml.NewDecoder(bytes.NewReader(body))
	decoder.CharsetReader = charset.NewReaderLabel
	if err := decoder.Decode(&rss); err != nil {
		return nil, fmt.Errorf("parse RSS data failed: %w", err)
	}

	var messages []*model.Message
	acceptedCount := 0
	for _, item := range rss.Items {
		if acceptedCount >= 10 {
			break
		}

		pubTime, err := parseRSSTime(item.PubDate)
		hasPubTime := err == nil && !pubTime.IsZero()
		if !hasPubTime {
			// 解析失败时使用当前时间
			pubTime = time.Now()
		}

		// 仅当有有效发布时间时才进行时间过滤，避免误杀
		// 使用 UTC 时间比较，避免时区问题（中国 UTC+8，美国新闻通常是 UTC/GMT）
		age := time.Now().UTC().Sub(pubTime.UTC())
		if hasPubTime && age > r.maxAge {
			slog.Debug("rss fetched", "source", r.name, "age_hours", age.Hours(), "max_age_hours", r.maxAge.Hours(), "title", item.Title)
			continue
		}

		// 处理标题和链接
		title := strings.TrimSpace(item.Title)

		// 过滤掉"No Title"或包含"[No Title]"的条目
		if strings.Contains(title, "[No Title]") || strings.Contains(strings.ToLower(title), "no title") {
			slog.Debug("rss fetched", "source", r.name, "title", title)
			continue
		}

		link := item.Link
		if r.matchesExcludedLink(link) {
			continue
		}

		// Google News: 提取原始来源信息
		sourceName := item.Source.Name
		sourceURL := item.Source.URL

		// 来源过滤（仅当配置了 allowed_sources 时生效）
		if len(r.allowedSources) > 0 {
			allowed := r.isSourceAllowed(sourceName, sourceURL, link)
			if !allowed {
				slog.Debug("rss fetched", "source", r.name, "sourcename", sourceName, "title", title)
				continue
			}
		}

		// BWE 源特殊处理
		if strings.Contains(r.url, "bwe-ws.com") || strings.Contains(r.url, "ch2rss.fflow.net") {
			title, link = extractBWEContent(title, link)
		}

		// 处理 content，清理并去重
		cleanTitle := htmlToPlainText(title)
		rawContent := item.Description
		if strings.TrimSpace(item.ContentEncoded) != "" {
			rawContent = item.ContentEncoded
		}
		cleanContent := htmlToPlainText(rawContent)
		// Google News 的 description 通常包含来源信息，提取有用内容
		if r.isGoogleNews {
			cleanContent = extractGoogleNewsContent(cleanContent, cleanTitle)
		}

		imageURLs, videoURL := extractRSSMedia(item)
		if r.excludeVideoOnly && videoURL != "" && !hasMeaningfulBody(cleanContent) {
			continue
		}
		if r.excludeReposts && isRepost(cleanTitle, cleanContent) {
			continue
		}
		if r.matchesExcludedKeywords(cleanTitle, cleanContent) {
			continue
		}

		// 关键词过滤（仅当配置了 require_keywords 时生效）
		if !r.matchesRequiredKeywords(cleanTitle, cleanContent) {
			slog.Debug("rss fetched", "source", r.name, "cleantitle", cleanTitle)
			continue
		}

		if r.fetchLinkedBody {
			if linkedBody, err := r.fetchLinkedArticleBody(link); err == nil && linkedBody != "" {
				if r.linkedBodyAppend && cleanContent != "" { cleanContent = cleanContent + "\n\n── AI 分析 ──\n" + linkedBody } else { cleanContent = linkedBody }
			} else if err != nil {
				slog.Debug("rss linked body fallback", "source", r.name, "link", link, "error", err)
			}
		}

		imageURL := ""
		if len(imageURLs) > 0 {
			imageURL = imageURLs[0]
		}

		// ID 回退链：GUID -> Link -> Title+发布时间
		// 部分源（ch2rss.fflow.net、rss-public.bwe-ws.com）不提供 <guid>，
		// 若直接对空字符串哈希会导致所有条目落到同一 ID，被去重误杀
		idBasis := item.GUID
		if idBasis == "" {
			idBasis = link
		}
		if idBasis == "" {
			idBasis = item.Title + item.PubDate
		}

		msg := &model.Message{
			Type:       model.TypeNews,
			ID:         hashString(idBasis),
			Title:      cleanTitle,
			Content:    cleanContent,
			Link:       link,
			ImageURL:   imageURL,
			ImageURLs:  imageURLs,
			VideoURL:   videoURL,
			CreateTime: pubTime,
			FetchTime:  time.Now(),
			Source:     r.name,
			SourceType: "rss",
				Tags:       r.tags,
		}

		// 确定来源显示名称（优先使用 RSS source 字段）
		displaySource := determineDisplaySource(r.url, item.Author)
		if sourceName != "" {
			displaySource = sourceName
		}
		msg.SetMetadata("display_source", displaySource)
		if sourceURL != "" {
			msg.SetMetadata("source_url", sourceURL)
		}

		messages = append(messages, msg)
		acceptedCount++
	}

	slog.Info("rss fetched", "source", r.name, "len_messages", len(messages))
	return messages, nil
}

func (r *RSSSource) matchesExcludedLink(link string) bool {
	lower := strings.ToLower(link)
	for _, pattern := range r.excludeLinkPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func (r *RSSSource) matchesExcludedKeywords(title, content string) bool {
	text := strings.ToLower(title + " " + content)
	for _, keyword := range r.excludeKeywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

var repostPrefix = regexp.MustCompile(`(?i)^\s*(?:rt\s+@\w+\s*:|repost(?:ed)?\s+(?:from|by)\b|shared\s+from\s+@\w+)`)

func isRepost(title, content string) bool {
	return repostPrefix.MatchString(title) || repostPrefix.MatchString(content)
}

func hasMeaningfulBody(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	lower := strings.ToLower(content)
	if lower == "video" || lower == "watch video" || lower == "watch now" {
		return false
	}
	withoutURLs := regexp.MustCompile(`https?://\S+`).ReplaceAllString(content, "")
	return len([]rune(strings.TrimSpace(withoutURLs))) >= 12
}

func extractRSSMedia(item rssItem) ([]string, string) {
	var images []string
	videoURL := ""
	seen := make(map[string]struct{})
	addImage := func(media rssMedia, enclosure bool) {
		if media.URL == "" || !isImageMedia(media, enclosure) {
			return
		}
		if _, ok := seen[media.URL]; ok {
			return
		}
		seen[media.URL] = struct{}{}
		images = append(images, media.URL)
	}
	addVideo := func(media rssMedia) {
		if videoURL == "" && media.URL != "" && isVideoMedia(media) {
			videoURL = media.URL
		}
	}
	for _, media := range item.Enclosures {
		addImage(media, true)
		addVideo(media)
	}
	for _, media := range item.MediaContents {
		addImage(media, false)
		addVideo(media)
	}
	for _, media := range item.MediaThumbnails {
		addImage(media, false)
	}
	return images, videoURL
}

func isImageMedia(media rssMedia, enclosure bool) bool {
	if strings.HasPrefix(media.Type, "audio/") || strings.HasPrefix(media.Type, "video/") ||
		media.Medium == "audio" || media.Medium == "video" {
		return false
	}
	if strings.HasPrefix(media.Type, "image/") || media.Medium == "image" {
		return true
	}
	ext := strings.ToLower(filepath.Ext(mediaURLPath(media.URL)))
	switch ext {
	case ".mp3", ".m4a", ".aac", ".wav", ".ogg", ".opus", ".mp4", ".mov", ".webm", ".m3u8":
		return false
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif":
		return true
	}
	return enclosure && media.Type == "" && media.Medium == ""
}

func isVideoMedia(media rssMedia) bool {
	if strings.HasPrefix(media.Type, "video/") || media.Medium == "video" {
		return true
	}
	switch strings.ToLower(filepath.Ext(mediaURLPath(media.URL))) {
	case ".mp4", ".mov", ".webm", ".m3u8":
		return true
	default:
		return false
	}
}

func mediaURLPath(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return parsed.Path
}

func (r *RSSSource) fetchLinkedArticleBody(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "https" || !r.isLinkedBodyHostAllowed(parsed.Hostname()) {
		return "", fmt.Errorf("linked body URL is not allowlisted")
	}

	baseClient := r.httpClient
	client := &http.Client{Timeout: 5 * time.Second}
	if baseClient != nil {
		client.Transport = baseClient.Transport
		client.Jar = baseClient.Jar
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || !r.isLinkedBodyHostAllowed(req.URL.Hostname()) {
			return fmt.Errorf("linked body redirect left allowlist")
		}
		if len(via) >= 5 {
			return fmt.Errorf("too many linked body redirects")
		}
		return nil
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("linked body returned HTTP %d", resp.StatusCode)
	}

	const maxBodyBytes = 512 << 10
	doc, err := xhtml.Parse(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return "", err
	}
	body := extractOfficialArticleText(doc)
	if body == "" {
		return "", fmt.Errorf("official article body not found")
	}
	return truncateRunes(body, r.linkedBodyMaxChars), nil
}

func (r *RSSSource) isLinkedBodyHostAllowed(host string) bool {
	_, ok := r.linkedBodyHosts[strings.ToLower(host)]
	return ok
}

func extractOfficialArticleText(doc *xhtml.Node) string {
	var best string
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && hasHTMLAttr(node, "id", "article") {
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if isFedArticleBodyColumn(child) {
					text := htmlNodeText(child)
					if len([]rune(text)) > len([]rune(best)) {
						best = text
					}
				}
			}
		}
		if node.Type == xhtml.ElementNode && isArticleContainer(node) {
			text := htmlNodeText(node)
			if len([]rune(text)) > len([]rune(best)) {
				best = text
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	return best
}

func isArticleContainer(node *xhtml.Node) bool {
	if node.Data == "article" {
		return true
	}
	for _, attr := range node.Attr {
		value := strings.ToLower(attr.Val)
		if attr.Key == "id" && (value == "article-content" || value == "content") {
			return true
		}
		if attr.Key == "class" && (strings.Contains(value, "article-content") || strings.Contains(value, "article__content")) {
			return true
		}
	}
	return false
}

func hasHTMLAttr(node *xhtml.Node, key, value string) bool {
	for _, attr := range node.Attr {
		if attr.Key == key && strings.EqualFold(attr.Val, value) {
			return true
		}
	}
	return false
}

func isFedArticleBodyColumn(node *xhtml.Node) bool {
	if node.Type != xhtml.ElementNode || node.Data != "div" {
		return false
	}
	for _, attr := range node.Attr {
		if attr.Key != "class" {
			continue
		}
		class := strings.ToLower(attr.Val)
		return strings.Contains(class, "col-xs-12") &&
			strings.Contains(class, "col-sm-8") &&
			strings.Contains(class, "col-md-8") &&
			!strings.Contains(class, "heading")
	}
	return false
}

func htmlNodeText(root *xhtml.Node) string {
	var builder strings.Builder
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode {
			switch node.Data {
			case "script", "style", "nav", "footer", "aside":
				return
			case "br", "p", "div", "li", "h1", "h2", "h3", "h4":
				builder.WriteByte(' ')
			}
		}
		if node.Type == xhtml.TextNode {
			builder.WriteString(node.Data)
			builder.WriteByte(' ')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return collapseWhitespace(html.UnescapeString(builder.String()))
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if max <= 0 || len(runes) <= max {
		return value
	}
	return strings.TrimSpace(string(runes[:max]))
}

// extractBWEContent 处理 BWE 源内容
func extractBWEContent(title, link string) (string, string) {
	sourceSuffixRe := regexp.MustCompile(`\s*source:\s*(https?://[^\s<]+)\s*$`)
	reBr := regexp.MustCompile(`(?i)<br\s*/?>|\r\n|\r|\n`)

	cleaned := reBr.ReplaceAllString(title, "\n")
	reAutoMatch := regexp.MustCompile(`(?i)\(Auto match could be wrong, 自动匹配可能不准确\)`)
	cleaned = reAutoMatch.ReplaceAllString(cleaned, "")

	matches := sourceSuffixRe.FindStringSubmatch(cleaned)
	if len(matches) == 2 {
		link = matches[1]
		cleaned = strings.TrimSpace(sourceSuffixRe.ReplaceAllString(cleaned, ""))
	}

	lines := strings.Split(cleaned, "\n")
	var resultLines []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			resultLines = append(resultLines, trimmed)
		}
	}
	return strings.Join(resultLines, "\n"), link
}

// determineDisplaySource 确定显示来源
func determineDisplaySource(url, author string) string {
	switch {
	case strings.Contains(url, "panewslab.com"):
		return "PANews"
	case strings.Contains(url, "trumpstruth.org"):
		return "Trump Truth Social"
	case strings.Contains(url, "trump.fm"):
		return "Trump真实社交"
	case strings.Contains(url, "rss-public.bwe-ws.com"):
		return "BWEnews"
	case strings.Contains(url, "federalreserve.gov"):
		return "Federal Reserve"
	case strings.Contains(url, "dj.com"):
		return "WSJ"
	case strings.Contains(url, "rsshub.app/twitter"):
		return fmt.Sprintf("Twitter - %s", author)
	case strings.Contains(url, "WatcherGuru"):
		return "WatcherGuru"
	case strings.Contains(url, "BWEtradfi"):
		return "BWE TradFi"
	case strings.Contains(url, "TheKobeissiLetter"):
		return "The Kobeissi Letter"
	case strings.Contains(url, "news.google.com"):
		return "Google News"
	default:
		return "RSS"
	}
}

// extractGoogleNewsContent 提取 Google News RSS 中的有用内容
// Google News 的 description 通常格式为: "<a href=...>标题</a>&nbsp;&nbsp;<font...>来源名称</font>"
// 我们需要提取其中的摘要部分（如果有）
func extractGoogleNewsContent(content, title string) string {
	if content == "" {
		return ""
	}

	// 去除 HTML 标签后检查
	cleaned := strings.TrimSpace(content)

	// 如果内容与标题相同或相似，返回空
	if cleaned == title || strings.HasPrefix(cleaned, title) {
		return ""
	}

	// Google News description 格式通常是: "摘要内容... - 来源名称"
	// 尝试提取摘要（去掉末尾的来源标注）
	if idx := strings.LastIndex(cleaned, " - "); idx > 0 {
		potentialSummary := strings.TrimSpace(cleaned[:idx])
		// 如果摘要与标题不同，返回摘要
		if potentialSummary != title && !strings.HasPrefix(potentialSummary, title) && len(potentialSummary) > 20 {
			return potentialSummary
		}
	}

	// 如果内容足够长且与标题不同，返回完整内容
	if len(cleaned) > 50 && !strings.Contains(title, cleaned) && !strings.Contains(cleaned, title) {
		return cleaned
	}

	return ""
}

// htmlToPlainText 将 HTML 转换为纯文本
func htmlToPlainText(s string) string {
	// Preserve word boundaries created by common block/line-break tags before
	// removing markup. Otherwise "one</p><p>two" becomes "onetwo".
	boundaries := regexp.MustCompile(`(?i)</?(?:br|p|div|li|h[1-6]|tr|td|blockquote)\b[^>]*>`)
	s = boundaries.ReplaceAllString(s, " ")
	tags := regexp.MustCompile(`<[^>]*>`)
	s = tags.ReplaceAllString(s, " ")
	return collapseWhitespace(html.UnescapeString(s))
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// parseRSSTime 解析多种常见的RSS时间格式
func parseRSSTime(dateStr string) (time.Time, error) {
	// 常见的RSS时间格式
	formats := []string{
		time.RFC1123Z,                    // "Mon, 02 Jan 2006 15:04:05 -0700"
		time.RFC1123,                     // "Mon, 02 Jan 2006 15:04:05 MST"
		time.RFC822Z,                     // "02 Jan 06 15:04 -0700"
		time.RFC822,                      // "02 Jan 06 15:04 MST"
		time.RFC3339,                     // "2006-01-02T15:04:05Z07:00"
		"2006-01-02T15:04:05Z",           // ISO 8601 UTC
		"2006-01-02T15:04:05-07:00",      // ISO 8601 with timezone
		"2006-01-02 15:04:05",            // 常见日期时间格式
		"Mon, 2 Jan 2006 15:04:05 -0700", // RFC1123Z 变体(单数日期)
		"Mon, 2 Jan 2006 15:04:05 MST",   // RFC1123 变体(单数日期)
	}

	dateStr = strings.TrimSpace(dateStr)
	for _, format := range formats {
		if t, err := time.Parse(format, dateStr); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("unable to parse date: %s", dateStr)
}

// hashString 生成字符串的哈希值
func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:8]) // 使用前8字节作为ID
}
