package model

import "time"

// MessageType 消息类型
type MessageType string

const (
	TypeNews     MessageType = "news"     // 新闻推送
	TypeChat     MessageType = "chat"     // 用户对话
	TypeQuote    MessageType = "quote"    // 行情推送
	TypeAnalysis MessageType = "analysis" // 分析报告
)

// Message 统一消息模型
// 同时支持新闻推送和用户交互场景
type Message struct {
	ID   string      `json:"id"`
	Type MessageType `json:"type"`

	// 通用字段
	Title        string `json:"title,omitempty"`
	Content      string `json:"content"`
	ShortContent string `json:"short_content,omitempty"` // 摘要版 (wechat 优先, discord 用 Content)

	// 新闻相关
	Source     string   `json:"source,omitempty"`      // 来源名称
	SourceType string   `json:"source_type,omitempty"` // 来源类型 (rss/api 等)
	Link       string   `json:"link,omitempty"`
	ImageURL   string   `json:"image_url,omitempty"`
	ImageURLs  []string `json:"image_urls,omitempty"`
	VideoURL   string   `json:"video_url,omitempty"`
	Tags       []string `json:"tags,omitempty"`

	// 对话相关
	Platform    string `json:"platform,omitempty"`     // feishu/wechat/telegram
	ChatID      string `json:"chat_id,omitempty"`      // 会话ID
	ChatType    string `json:"chat_type,omitempty"`    // private/group
	UserID      string `json:"user_id,omitempty"`      // 发送者ID
	Username    string `json:"username,omitempty"`     // 发送者名称
	ReplyTo     string `json:"reply_to,omitempty"`     // 回复的消息ID
	IsMentioned bool   `json:"is_mentioned,omitempty"` // 是否被@

	// 时间
	CreateTime time.Time `json:"create_time"`
	FetchTime  time.Time `json:"fetch_time,omitempty"`

	// 扩展
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// NewNewsMessage 创建新闻消息
func NewNewsMessage(title, content string) *Message {
	return &Message{
		Type:      TypeNews,
		Title:     title,
		Content:   content,
		FetchTime: time.Now(),
		Metadata:  make(map[string]interface{}),
	}
}

// NewChatMessage 创建聊天消息
func NewChatMessage(platform, chatID, userID, text string) *Message {
	return &Message{
		Type:       TypeChat,
		Content:    text,
		Platform:   platform,
		ChatID:     chatID,
		UserID:     userID,
		CreateTime: time.Now(),
		Metadata:   make(map[string]interface{}),
	}
}

// SetMetadata 设置元数据
func (m *Message) SetMetadata(key string, value interface{}) {
	if m.Metadata == nil {
		m.Metadata = make(map[string]interface{})
	}
	m.Metadata[key] = value
}

// GetMetadata 获取元数据
func (m *Message) GetMetadata(key string) (interface{}, bool) {
	if m.Metadata == nil {
		return nil, false
	}
	v, ok := m.Metadata[key]
	return v, ok
}

// GetStringMetadata 获取字符串类型的元数据
func (m *Message) GetStringMetadata(key string) string {
	v, ok := m.GetMetadata(key)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// IsNews 判断是否为新闻类消息
func (m *Message) IsNews() bool {
	return m.Type == TypeNews || m.Type == TypeQuote || m.Type == TypeAnalysis
}

// IsChat 判断是否为聊天类消息
func (m *Message) IsChat() bool {
	return m.Type == TypeChat
}

// IsStructuredReport 判断是否为「自成体系的结构化报告」——简报 /
// 观复 / 宏观报告，以及所有 push_output 脚本源(13F/VIX/FedWatch/超级投资者/全球宏观)。
// 这类消息已中文化、带唯一 ID、正文自带排版，全链路据此统一处理：
//   - 渠道输出：企业微信走 markdown(4096)、个人微信走 priority=now(不并入 digest)
//   - AI 增强：跳过翻译+简评(已自成体系)
//   - 去重：只做精确 ID 去重，不过内容/语义去重(各场次正文高度相似会被误删)
//
// ⚠️ 单一事实来源：channel/llm/filter 等各处务必调用本函数，勿再各自维护副本(历史上
// 因复制多份清单、漏更新 script 导致脚本报告在微信端被当普通新闻截断/并入 digest)。
func IsStructuredReport(sourceType string) bool {
	switch sourceType {
	// 仅保留实际注册的源类型(morning-scan/limit-up-ladder 等已移除的
	// 历史类型分支于 2026-08-02 清理,防残留类型误走结构化路径)。
	case "market-briefing", "us-macro-report", "guanfu", "script":
		return true
	}
	return false
}

// AIComment 获取AI点评（如果有）
func (m *Message) AIComment() string {
	return m.GetStringMetadata("ai_comment")
}

// SetAIComment 设置AI点评
func (m *Message) SetAIComment(comment string) {
	m.SetMetadata("ai_comment", comment)
}
