package discord

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// ThreadInfo Thread 信息
type ThreadInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	LastMsgAt time.Time `json:"last_msg_at"`
	Source    string    `json:"source"` // 对应的源
}

// ThreadPool Thread 池管理
type ThreadPool struct {
	// sourceName -> ThreadInfo
	threads map[string]*ThreadInfo

	// 活跃的 thread IDs (用于清理)
	activeIDs map[string]bool

	session   *discordgo.Session
	channelID string
	threadTTL time.Duration
	mappings  map[string][]string // topic -> sources 映射

	mu sync.RWMutex
}

// NewThreadPool 创建 Thread Pool
func NewThreadPool(session *discordgo.Session, channelID string, threadTTL time.Duration) *ThreadPool {
	return &ThreadPool{
		threads:   make(map[string]*ThreadInfo),
		activeIDs: make(map[string]bool),
		session:   session,
		channelID: channelID,
		threadTTL: threadTTL,
		mappings:  make(map[string][]string),
	}
}

// SetMappings 设置源 -> Thread 映射
func (p *ThreadPool) SetMappings(mappings map[string][]string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 反转映射: source -> topic
	for topic, sources := range mappings {
		for _, src := range sources {
			// 存储 topic 信息
			if _, ok := p.threads[src]; !ok {
				p.threads[src] = &ThreadInfo{Name: topic, Source: src}
			}
		}
	}
	p.mappings = mappings
}

// GetOrCreateThread 获取或创建指定源的 Thread
func (p *ThreadPool) GetOrCreateThread(sourceName string) (string, error) {
	p.mu.RLock()
	info, exists := p.threads[sourceName]
	p.mu.RUnlock()

	if exists && info.ID != "" {
		// 检查是否过期
		if time.Since(info.LastMsgAt) < p.threadTTL {
			return info.ID, nil
		}
		// TTL 过期，标记清理
		p.mu.Lock()
		if info.ID != "" {
			p.activeIDs[info.ID] = false // 标记为不活跃
		}
		p.mu.Unlock()
	}

	// 需要创建新 Thread
	threadName := p.getThreadName(sourceName)

	thread, err := p.createForumThread(threadName)
	if err != nil {
		return "", err
	}

	p.mu.Lock()
	p.threads[sourceName] = &ThreadInfo{
		ID:        thread.ID,
		Name:      threadName,
		CreatedAt: time.Now(),
		LastMsgAt: time.Now(),
		Source:    sourceName,
	}
	p.activeIDs[thread.ID] = true
	p.mu.Unlock()

	slog.Info("Discord: created new thread", "threadname", threadName, "sourcename", sourceName)
	return thread.ID, nil
}

// createForumThread 创建 Forum Thread
func (p *ThreadPool) createForumThread(name string) (*discordgo.Channel, error) {
	// 使用 ForumThreadStartComplex 创建带初始消息的 thread
	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("📌 %s", name),
		Description: "此 Thread 用于聚合此来源的新闻更新",
		Color:       3447003,
		Timestamp:   time.Now().Format(time.RFC3339),
		Footer: &discordgo.MessageEmbedFooter{
			Text: "News Fetcher",
		},
	}

	thread, err := p.session.ForumThreadStartEmbed(p.channelID, name, 1440, embed)
	if err != nil {
		return nil, fmt.Errorf("create forum thread failed: %w", err)
	}

	return thread, nil
}

// getThreadName 获取源的 Thread 名称
func (p *ThreadPool) getThreadName(sourceName string) string {
	topicNames := map[string]string{
		// Trump 相关
		"trump-rss": "🐘 Trump News",

		// 财经新闻
		"bwe-tradfi":     "💰 财经新闻",
		"kobeissi-letter": "📰 Kobeissi Letter",

		// 美股 / 宏观
		"us-macro-report": "🌎 美国宏观",

		"guanfu-score":   "🔭 观复盘面",
	}

	if name, ok := topicNames[sourceName]; ok {
		return name
	}

	return fmt.Sprintf("📰 %s", sourceName)
}

// AppendMessage 向 Thread 追加消息
func (p *ThreadPool) AppendMessage(threadID string, embed *discordgo.MessageEmbed) error {
	_, err := p.session.ChannelMessageSendEmbed(threadID, embed)
	if err != nil {
		return err
	}

	// 更新 LastMsgAt
	p.mu.Lock()
	for _, info := range p.threads {
		if info.ID == threadID {
			info.LastMsgAt = time.Now()
			break
		}
	}
	p.mu.Unlock()

	return nil
}

// UpdateLastActivity 更新 Thread 最后活跃时间
func (p *ThreadPool) UpdateLastActivity(sourceName string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if info, ok := p.threads[sourceName]; ok {
		info.LastMsgAt = time.Now()
	}
}

// GetThreadInfo 获取 Thread 信息
func (p *ThreadPool) GetThreadInfo(sourceName string) (*ThreadInfo, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	info, ok := p.threads[sourceName]
	return info, ok
}

// GetAllThreads 获取所有 Thread 信息
func (p *ThreadPool) GetAllThreads() map[string]*ThreadInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make(map[string]*ThreadInfo)
	for k, v := range p.threads {
		result[k] = v
	}
	return result
}

// CleanupInactive 清理不活跃的 Thread
func (p *ThreadPool) CleanupInactive() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	count := 0
	for id, active := range p.activeIDs {
		if !active {
			delete(p.activeIDs, id)
			count++
		}
	}
	return count
}

// SourceMapping 返回源到 topic 的映射
func (p *ThreadPool) SourceMapping() map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make(map[string]string)
	for src, info := range p.threads {
		result[src] = info.Name
	}
	return result
}
