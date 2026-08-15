package channel

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// MessageFormatter 提供跨渠道的消息格式化工具
type MessageFormatter struct{}

// NewMessageFormatter 创建消息格式化器
func NewMessageFormatter() *MessageFormatter {
	return &MessageFormatter{}
}

// FormatOptions 格式化选项
type FormatOptions struct {
	MaxTitleLen   int
	MaxContentLen int
	EscapeHTML    bool
	StripMarkdown bool
	Platform      string // telegram/discord/wechat/feishu/wxofficial/wxpersonal
}

// DefaultOptions 返回默认选项
func DefaultOptions(platform string) FormatOptions {
	opts := FormatOptions{
		MaxTitleLen:   256,
		MaxContentLen: 2000,
		Platform:      platform,
	}

	switch platform {
	case "telegram":
		opts.EscapeHTML = true
		opts.MaxContentLen = 4096
	case "discord":
		opts.MaxTitleLen = 256
		opts.MaxContentLen = 4096
	case "feishu":
		opts.MaxContentLen = 4000
	case "wechat", "wxofficial":
		opts.MaxContentLen = 2048
	case "wxpersonal":
		opts.StripMarkdown = true
		opts.MaxContentLen = 2000
	}

	return opts
}

// Format 格式化消息，返回处理后的 title 和 content
func (f *MessageFormatter) Format(msg *model.Message, opts FormatOptions) (title, content string) {
	title = msg.Title
	content = msg.Content

	// 渠道差异化: wechat/wxofficial/wxpersonal 优先使用 ShortContent (若提供)
	switch opts.Platform {
	case "wechat", "wxofficial", "wxpersonal":
		if msg.ShortContent != "" {
			content = msg.ShortContent
		}
	}

	// 1. 清理特殊字符
	title = SanitizeText(title)
	content = SanitizeText(content)

	// 2. 智能处理标题和内容
	title, content = SmartTitleContent(title, content, opts.MaxTitleLen)

	// 3. 去重处理
	content = DeduplicateContent(title, content)

	// 4. 长度限制
	if opts.MaxTitleLen > 0 && len(title) > opts.MaxTitleLen {
		title = truncateText(title, opts.MaxTitleLen)
	}
	if opts.MaxContentLen > 0 && len(content) > opts.MaxContentLen {
		content = truncateText(content, opts.MaxContentLen)
	}

	// 5. 平台特定处理
	if opts.EscapeHTML {
		title = EscapeHTML(title)
		content = EscapeHTML(content)
	}
	if opts.StripMarkdown {
		title = StripMarkdown(title)
		content = StripMarkdown(content)
	}

	return title, content
}

// SmartTitleContent 智能处理标题和内容
// 处理以下情况：
// 1. 标题和内容重复 → 只保留 title，content 置空
// 2. 有 title 无 content → 只用 title
// 3. 无 title 有 content → 只用 content
// 4. title 和 content 都有且不重复 → 都保留
// 5. 金十格式【xxx】→ 提取方括号内容作为标题
func SmartTitleContent(title, content string, maxTitleLen int) (string, string) {
	if maxTitleLen <= 0 {
		maxTitleLen = 100
	}

	// 情况1：金十格式 - 内容以【xxx】开头，提取为标题
	if strings.HasPrefix(content, "【") {
		endIdx := strings.Index(content, "】")
		if endIdx > 0 && endIdx < 100 {
			bracketContent := content[len("【"):endIdx]
			// 如果标题和方括号内容相似，使用方括号内容作为标题
			if title == "" || strings.Contains(title, bracketContent) || strings.Contains(bracketContent, title) {
				newContent := strings.TrimSpace(content[endIdx+len("】"):])
				return bracketContent, newContent
			}
		}
	}

	// 情况2：标题和内容重复（完全相同或内容以标题开头）→ 只保留 title
	if title != "" && content != "" {
		normalizedTitle := normalizeForCompare(title)
		normalizedContent := normalizeForCompare(content)
		// 完全相同或内容以标题开头，说明信息重复
		if normalizedContent == normalizedTitle || strings.HasPrefix(normalizedContent, normalizedTitle) {
			return title, ""
		}
	}

	// 情况3：有 title 无 content → 只用 title
	if title != "" && content == "" {
		return title, ""
	}

	// 情况4：无 title 有 content → 只用 content
	if title == "" && content != "" {
		return "", content
	}

	// 情况5：title 和 content 都有且不重复 → 都保留
	return title, content
}

// extractFirstSentence 提取第一句话作为标题
func extractFirstSentence(text string, maxLen int) (title, remaining string) {
	if text == "" {
		return "", ""
	}

	// 中文句号优先
	punctuations := []string{"。", "！", "？", ".", "!", "?", "；", ";", "——", "—"}

	bestIdx := -1
	for _, p := range punctuations {
		idx := strings.Index(text, p)
		if idx > 0 && idx < maxLen {
			if bestIdx == -1 || idx < bestIdx {
				bestIdx = idx + len(p)
			}
		}
	}

	// 如果找到合适的断句点
	if bestIdx > 10 && bestIdx <= maxLen {
		return strings.TrimSpace(text[:bestIdx]), strings.TrimSpace(text[bestIdx:])
	}

	// 没找到句号，尝试在逗号处截断
	commas := []string{"，", ",", "、"}
	for _, c := range commas {
		idx := strings.Index(text, c)
		if idx > 20 && idx < maxLen {
			return strings.TrimSpace(text[:idx]), strings.TrimSpace(text[idx+len(c):])
		}
	}

	// 强制截断
	if len(text) > maxLen {
		// 尝试在空格处截断
		truncated := text[:maxLen]
		lastSpace := strings.LastIndex(truncated, " ")
		if lastSpace > maxLen/2 {
			return strings.TrimSpace(text[:lastSpace]) + "...", strings.TrimSpace(text[lastSpace:])
		}
		return truncated + "...", strings.TrimSpace(text[maxLen:])
	}

	return text, ""
}

// SanitizeText 清理可能导致乱码的特殊字符
func SanitizeText(text string) string {
	if text == "" {
		return ""
	}

	// 使用 strings.Map 清理字符
	text = strings.Map(func(r rune) rune {
		switch r {
		// 零宽字符
		case '\u200B', '\u200C', '\u200D', '\uFEFF', '\u200E', '\u200F':
			return -1
		// 不间断空格 → 普通空格
		case '\u00A0':
			return ' '
		// 特殊引号 → 普通引号
		case '\u201C', '\u201D', '\u201E', '\u201F':
			return '"'
		case '\u2018', '\u2019', '\u201A', '\u201B':
			return '\''
		// 特殊破折号 → 普通破折号
		case '\u2013', '\u2014', '\u2015':
			return '-'
		// 特殊省略号 → 三个点
		case '\u2026':
			return '.'
		}

		// 移除控制字符（保留换行、制表符）
		if r < 32 && r != '\n' && r != '\t' && r != '\r' {
			return -1
		}

		// 移除私用区字符
		if r >= 0xE000 && r <= 0xF8FF {
			return -1
		}

		// 移除代理对字符
		if r >= 0xD800 && r <= 0xDFFF {
			return -1
		}

		return r
	}, text)

	// 清理多余的空白
	text = cleanWhitespace(text)

	return strings.TrimSpace(text)
}

// DeduplicateContent 确保 content 不与 title 重复
func DeduplicateContent(title, content string) string {
	if title == "" || content == "" {
		return content
	}

	// 规范化用于比较
	normalizedTitle := normalizeForCompare(title)
	normalizedContent := normalizeForCompare(content)

	// 1. 完全相同
	if normalizedContent == normalizedTitle {
		return ""
	}

	// 2. content 以 title 开头（精确匹配）
	if strings.HasPrefix(content, title) {
		content = strings.TrimPrefix(content, title)
		content = strings.TrimLeft(content, "：:。.,，\n\r\t -—|")
		content = strings.TrimSpace(content)
	}

	// 3. content 以 title 开头（规范化比较）
	if strings.HasPrefix(normalizedContent, normalizedTitle) {
		// 找到原始 content 中对应的位置
		titleLen := len(title)
		if titleLen < len(content) {
			content = strings.TrimSpace(content[titleLen:])
			content = strings.TrimLeft(content, "：:。.,，\n\r\t -—|")
		}
	}

	// 4. title 包含在 content 开头（模糊匹配，处理标点差异）
	if len(title) > 10 {
		// 尝试匹配 title 的前80%
		partialTitle := normalizedTitle[:len(normalizedTitle)*8/10]
		if strings.HasPrefix(normalizedContent, partialTitle) {
			// 找到 title 结束位置
			idx := findTitleEndIndex(content, title)
			if idx > 0 && idx < len(content) {
				content = strings.TrimSpace(content[idx:])
				content = strings.TrimLeft(content, "：:。.,，\n\r\t -—|")
			}
		}
	}

	return content
}

// EscapeHTML 转义 HTML 特殊字符（用于 Telegram）
func EscapeHTML(text string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(text)
}

// StripMarkdown 移除 Markdown 格式（用于纯文本渠道）
func StripMarkdown(text string) string {
	// 移除常见 Markdown 标记
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "__", "")
	text = strings.ReplaceAll(text, "```", "")
	text = strings.ReplaceAll(text, "`", "")
	text = strings.ReplaceAll(text, "###", "")
	text = strings.ReplaceAll(text, "##", "")
	text = strings.ReplaceAll(text, "# ", "")
	text = strings.ReplaceAll(text, "_", "")
	text = strings.ReplaceAll(text, "~~", "")

	// 移除链接格式 [text](url) -> text
	linkRegex := regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	text = linkRegex.ReplaceAllString(text, "$1")

	// 移除图片格式 ![alt](url)
	imgRegex := regexp.MustCompile(`!\[[^\]]*\]\([^)]+\)`)
	text = imgRegex.ReplaceAllString(text, "")

	return strings.TrimSpace(text)
}

// EscapeMarkdown 转义 Markdown 特殊字符（用于 Discord）
func EscapeMarkdown(text string) string {
	// Discord Markdown 特殊字符
	specialChars := []string{"*", "_", "`", "~", "|", ">"}
	for _, char := range specialChars {
		text = strings.ReplaceAll(text, char, "\\"+char)
	}
	return text
}

// truncateText 截断文本，保留完整单词/句子
// 注意：maxLen 以字节计，但截断点对齐到 UTF-8 字符边界，避免把中文切成半个字导致乱码
func truncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}

	// 在字符边界回退到 <= maxLen-3 的位置（给 "..." 留 3 字节）
	cut := safeCutIndex(text, maxLen-3)
	truncated := text[:cut]

	// 尝试在句号/问号/感叹号处截断（保留标点本身，按其真实字节宽度对齐）
	lastPunct := strings.LastIndexAny(truncated, "。！？.!?")
	if lastPunct > maxLen/2 {
		_, punctSize := utf8.DecodeRuneInString(text[lastPunct:])
		return text[:lastPunct+punctSize]
	}

	// 尝试在空格处截断
	lastSpace := strings.LastIndex(truncated, " ")
	if lastSpace > maxLen/2 {
		return text[:lastSpace] + "..."
	}

	return truncated + "..."
}

// safeCutIndex 返回 <= maxBytes 且落在 UTF-8 字符边界上的字节下标
func safeCutIndex(s string, maxBytes int) int {
	if maxBytes <= 0 {
		return 0
	}
	if maxBytes >= len(s) {
		return len(s)
	}
	// 从 maxBytes 往回退，直到落在一个字符的起始字节上
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return maxBytes
}

// ClampRunes 将文本按字符边界裁到不超过 maxBytes 字节，超出则追加省略号
// 用于各渠道发送前的最终长度保护，避免超平台上限被拒发（= 静默漏推）
func ClampRunes(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := safeCutIndex(text, maxBytes-3)
	return text[:cut] + "..."
}

// normalizeForCompare 规范化文本用于比较
func normalizeForCompare(text string) string {
	// 转小写
	text = strings.ToLower(text)
	// 移除所有空白
	text = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
	// 移除常见标点
	text = strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return -1
		}
		return r
	}, text)
	return text
}

// findTitleEndIndex 找到 title 在 content 中的结束位置
func findTitleEndIndex(content, title string) int {
	// 简单实现：返回 title 长度
	// 可以优化为更智能的匹配
	if len(title) >= len(content) {
		return 0
	}
	return len(title)
}

// cleanWhitespace 清理多余空白
func cleanWhitespace(text string) string {
	// 合并连续空格
	spaceRegex := regexp.MustCompile(`[ \t]+`)
	text = spaceRegex.ReplaceAllString(text, " ")

	// 合并连续换行（最多保留2个）
	newlineRegex := regexp.MustCompile(`\n{3,}`)
	text = newlineRegex.ReplaceAllString(text, "\n\n")

	return text
}

// GetSourceIcon 根据来源类型返回图标
func GetSourceIcon(sourceType, source string) string {
	switch sourceType {
	case "rss":
		return "📰"
	default:
		lowerSource := strings.ToLower(source)
		if strings.Contains(lowerSource, "trump") {
			return "🇺🇸"
		}
		if strings.Contains(lowerSource, "crypto") || strings.Contains(lowerSource, "coin") {
			return "🪙"
		}
		if strings.Contains(lowerSource, "news") {
			return "📰"
		}
		return "📢"
	}
}

// LinkFormat 链接格式类型
type LinkFormat string

const (
	LinkFormatHTML     LinkFormat = "html"     // <a href="url">text</a> - Telegram
	LinkFormatMarkdown LinkFormat = "markdown" // [text](url) - Discord
	LinkFormatLarkMD   LinkFormat = "lark_md"  // [text](url) - 飞书
	LinkFormatPlain    LinkFormat = "plain"    // text: url - 纯文本
)

// FormatLink 格式化链接
func FormatLink(url, text string, format LinkFormat) string {
	if url == "" {
		return ""
	}
	if text == "" {
		text = "链接"
	}

	switch format {
	case LinkFormatHTML:
		return fmt.Sprintf("<a href=\"%s\">%s</a>", url, EscapeHTML(text))
	case LinkFormatMarkdown, LinkFormatLarkMD:
		return fmt.Sprintf("[%s](%s)", text, url)
	case LinkFormatPlain:
		return fmt.Sprintf("%s: %s", text, url)
	default:
		return url
	}
}

// FormatLinks 格式化多个链接
func FormatLinks(links map[string]string, format LinkFormat, separator string) string {
	if len(links) == 0 {
		return ""
	}
	if separator == "" {
		separator = " | "
	}

	var parts []string
	// 按固定顺序处理链接
	order := []string{"详情", "相关", "原文", "查看"}
	for _, name := range order {
		if url, ok := links[name]; ok && url != "" {
			parts = append(parts, FormatLink(url, name, format))
		}
	}
	// 处理其他链接
	for name, url := range links {
		found := false
		for _, o := range order {
			if o == name {
				found = true
				break
			}
		}
		if !found && url != "" {
			parts = append(parts, FormatLink(url, name, format))
		}
	}

	return strings.Join(parts, separator)
}

// GetLinkFormat 根据平台获取链接格式
func GetLinkFormat(platform string) LinkFormat {
	switch platform {
	case "telegram":
		return LinkFormatHTML
	case "discord":
		return LinkFormatMarkdown
	case "feishu":
		return LinkFormatLarkMD
	case "wechat", "wxofficial", "wxpersonal":
		return LinkFormatPlain
	default:
		return LinkFormatPlain
	}
}

// ShortenURL 缩短 URL 显示（用于纯文本渠道）
func ShortenURL(url string) string {
	if len(url) <= 50 {
		return url
	}
	// 提取域名
	if idx := strings.Index(url, "://"); idx > 0 {
		rest := url[idx+3:]
		if slashIdx := strings.Index(rest, "/"); slashIdx > 0 {
			domain := rest[:slashIdx]
			return domain + "/..."
		}
	}
	return url[:47] + "..."
}

// DefaultFormatter 全局默认格式化器
var DefaultFormatter = NewMessageFormatter()

// ─── FormattedMessage: 统一消息结构 ───

// FormattedMessage 预提取、预处理后的消息结构
// 各渠道从此结构渲染，而非直接操作 model.Message
type FormattedMessage struct {
	// 核心内容（已清理、去重、截断）
	Title   string
	Content string

	// 来源展示
	Icon          string // emoji
	DisplaySource string
	Timestamp     string // 格式化后的时间

	// 附加信息
	AIComment      string
	Tags           []string
	EconomicImpact string // 原始文本
	EconomicIcon   string // 📈/📉/📊

	// 媒体
	ImageURL  string   // 主图片链接
	ImageURLs []string // 全部图片链接
	VideoURL  string   // 视频链接

	// 链接
	PrimaryLink string            // msg.Link
	ExtraLinks  map[string]string // detail_link, related_link 等

	// 原始消息引用（渠道特殊逻辑兜底用）
	Raw *model.Message
}

// BuildFormattedMessage 从原始消息构建统一格式化结构
func BuildFormattedMessage(msg *model.Message, opts FormatOptions) *FormattedMessage {
	title, content := DefaultFormatter.Format(msg, opts)

	fm := &FormattedMessage{
		Title:   title,
		Content: content,
		Icon:    GetSourceIcon(msg.SourceType, msg.Source),
		Raw:     msg,
	}

	// 来源
	fm.DisplaySource = msg.GetStringMetadata("display_source")
	if fm.DisplaySource == "" {
		fm.DisplaySource = msg.Source
	}
	fm.Timestamp = msg.CreateTime.Format("01-02 15:04")

	// AI 点评
	fm.AIComment = msg.AIComment()

	// 图片
	fm.ImageURL = msg.ImageURL
	fm.ImageURLs = msg.ImageURLs
	fm.VideoURL = msg.VideoURL
	if fm.ImageURL == "" && len(fm.ImageURLs) > 0 {
		fm.ImageURL = fm.ImageURLs[0]
	}

	// 标签
	fm.Tags = msg.Tags

	// 经济影响
	if impact := msg.GetStringMetadata("economic_impact"); impact != "" {
		fm.EconomicImpact = impact
		fm.EconomicIcon = "📊"
		if strings.Contains(impact, "利好") || strings.Contains(impact, "positive") {
			fm.EconomicIcon = "📈"
		} else if strings.Contains(impact, "利空") || strings.Contains(impact, "negative") {
			fm.EconomicIcon = "📉"
		}
	}

	// 链接
	fm.PrimaryLink = msg.Link
	fm.ExtraLinks = make(map[string]string)
	if v := msg.GetStringMetadata("detail_link"); v != "" {
		fm.ExtraLinks["详情"] = v
	}
	if v := msg.GetStringMetadata("related_link"); v != "" {
		fm.ExtraLinks["相关"] = v
	}

	return fm
}

// GetProbBar 生成概率进度条（通用）
func GetProbBar(prob float64) string {
	filled := int(prob / 10)
	if filled > 10 {
		filled = 10
	}
	var sb strings.Builder
	for i := 0; i < filled; i++ {
		sb.WriteString("▓")
	}
	for i := filled; i < 10; i++ {
		sb.WriteString("░")
	}
	return sb.String()
}

// SourceEmojiForType 返回来源类型对应的 emoji
// 仅保留实际注册的源类型(morning-scan/limit-up-ladder 等历史类型
// 分支于 2026-08-02 清理)。
func SourceEmojiForType(sourceType string) string {
	switch sourceType {
	case "market-briefing":
		return "🌅"
	case "guanfu":
		return "🔭"
	case "rss":
		return "📰"
	case "us-macro-report":
		return "🌎"
	case "script":
		return "" // 脚本报告(VIX/FedWatch 等)正文首行自带 emoji，避免双 emoji
	default:
		return "📌"
	}
}
