package model

import "time"

// News 新闻模型（用于存储和查询）
type News struct {
	ID         string                 `json:"id"`
	Title      string                 `json:"title"`
	Content    string                 `json:"content"`
	Source     string                 `json:"source"`
	SourceType string                 `json:"source_type"`
	Link       string                 `json:"link,omitempty"`
	ImageURL   string                 `json:"image_url,omitempty"`
	ImageURLs  []string               `json:"image_urls,omitempty"`
	VideoURL   string                 `json:"video_url,omitempty"`
	Tags       []string               `json:"tags,omitempty"`
	AIComment  string                 `json:"ai_comment,omitempty"`
	CreateTime time.Time              `json:"create_time"`
	FetchTime  time.Time              `json:"fetch_time"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// ToMessage 转换为统一消息
func (n *News) ToMessage() *Message {
	msg := &Message{
		ID:         n.ID,
		Type:       TypeNews,
		Title:      n.Title,
		Content:    n.Content,
		Source:     n.Source,
		SourceType: n.SourceType,
		Link:       n.Link,
		ImageURL:   n.ImageURL,
		ImageURLs:  n.ImageURLs,
		VideoURL:   n.VideoURL,
		Tags:       n.Tags,
		CreateTime: n.CreateTime,
		FetchTime:  n.FetchTime,
		Metadata:   n.Metadata,
	}
	if n.AIComment != "" {
		msg.SetAIComment(n.AIComment)
	}
	return msg
}

// NewsFromMessage 从统一消息转换
func NewsFromMessage(msg *Message) *News {
	return &News{
		ID:         msg.ID,
		Title:      msg.Title,
		Content:    msg.Content,
		Source:     msg.Source,
		SourceType: msg.SourceType,
		Link:       msg.Link,
		ImageURL:   msg.ImageURL,
		ImageURLs:  msg.ImageURLs,
		VideoURL:   msg.VideoURL,
		Tags:       msg.Tags,
		AIComment:  msg.AIComment(),
		CreateTime: msg.CreateTime,
		FetchTime:  msg.FetchTime,
		Metadata:   msg.Metadata,
	}
}
