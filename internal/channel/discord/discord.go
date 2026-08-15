package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/model"

	"github.com/bwmarrin/discordgo"
)

func init() {
	channel.Register("discord", NewDiscordChannel)
}

// DiscordChannel Discord 渠道
type DiscordChannel struct {
	channel.BaseChannel
	webhookURL string
	threadID   string

	// 多 webhook 分散推送（轮询）以规避 Discord 单 webhook 频控
	webhookURLs  []string
	webhookIdx   uint64 // 轮询计数器（atomic）
	webhookPlain bool   // true = 普通文本频道，不发 thread_name
	httpClient   *http.Client

	// Bot 模式
	botToken  string
	channelID string
	session   *discordgo.Session

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Thread Pool
	threadPool  *ThreadPool
	threadDedup *ThreadDedup
	ratelimiter *DiscordRatelimiter

	// 配置选项
	threadPoolEnabled bool
	threadMappings    map[string][]string
	threadTTL         time.Duration
}

// NewDiscordChannel 创建 Discord 渠道
func NewDiscordChannel(cfg channel.Config) (channel.Channel, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = channel.ModePush
	}

	ch := &DiscordChannel{
		BaseChannel:       channel.NewBaseChannel(cfg.Name, "discord", mode),
		webhookURL:        cfg.Webhook,
		httpClient:        &http.Client{Timeout: 30 * time.Second},
		threadPoolEnabled: true,
		threadTTL:         24 * time.Hour,
		threadMappings:    make(map[string][]string),
	}

	if cfg.Options != nil {
		if id, ok := cfg.Options["thread_id"].(string); ok {
			ch.threadID = id
		}
		// 多 webhook 列表（分散推送 + 轮询规避频控）
		if list, ok := cfg.Options["webhooks"].([]interface{}); ok {
			for _, v := range list {
				if s, ok := v.(string); ok && s != "" {
					ch.webhookURLs = append(ch.webhookURLs, s)
				}
			}
		}
		// webhook_plain=true：普通文本频道，不创建 forum thread
		if plain, ok := cfg.Options["webhook_plain"].(bool); ok {
			ch.webhookPlain = plain
		}
		if token, ok := cfg.Options["bot_token"].(string); ok {
			ch.botToken = token
		}
		if channelID, ok := cfg.Options["channel_id"].(string); ok {
			ch.channelID = channelID
		}
		if enabled, ok := cfg.Options["thread_pool_enabled"].(bool); ok {
			ch.threadPoolEnabled = enabled
		}
		if mappings, ok := cfg.Options["thread_mappings"].(map[string]interface{}); ok {
			for topic, sources := range mappings {
				if srcs, ok := sources.([]interface{}); ok {
					var srcList []string
					for _, s := range srcs {
						if str, ok := s.(string); ok {
							srcList = append(srcList, str)
						}
					}
					ch.threadMappings[topic] = srcList
				}
			}
		}
		if ttl, ok := cfg.Options["thread_ttl"].(int); ok {
			ch.threadTTL = time.Duration(ttl) * time.Second
		}
	}

	// 双向模式需要 bot_token
	if mode == channel.ModeBidirectional && ch.botToken == "" {
		return nil, fmt.Errorf("discord bidirectional mode requires bot_token")
	}

	return ch, nil
}

// Start 启动渠道
func (d *DiscordChannel) Start(ctx context.Context) error {
	d.ctx, d.cancel = context.WithCancel(ctx)

	// 只要配了 bot_token 就启动 Bot session：
	// 推送模式也需要 session 才能发 forum 帖（sendViaBot/createForumThread 走 session 的 REST）。
	// 此前仅在双向(可接收)模式启动，导致 push 模式的 discord-push 配了 bot 却 session=nil 无法发送。
	if d.botToken != "" {
		if err := d.startBot(); err != nil {
			return fmt.Errorf("failed to start discord bot: %w", err)
		}
	}

	// 初始化 Thread Pool (Bot 模式下)
	if d.session != nil && d.channelID != "" && d.threadPoolEnabled {
		d.threadPool = NewThreadPool(d.session, d.channelID, d.threadTTL)
		d.threadPool.SetMappings(d.threadMappings)
		d.threadDedup = NewThreadDedup(60*time.Minute, 0.7)
		d.ratelimiter = NewDiscordRatelimiter(3, 5, 2) // 3 thread/15min, 5 msg/s, burst 2
		slog.Info("Discord: Thread Pool enabled with", "len_d_threadmappings", len(d.threadMappings))
	}

	slog.Info("Discord channel", "d_name", d.Name(), "mode", d.Mode())
	return nil
}

// startBot 启动 Discord Bot
func (d *DiscordChannel) startBot() error {
	var err error
	d.session, err = discordgo.New("Bot " + d.botToken)
	if err != nil {
		return fmt.Errorf("failed to create discord session: %w", err)
	}

	// 设置 Intents
	d.session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent

	// 注册消息处理器
	d.session.AddHandler(d.handleMessage)

	// 连接到 Discord
	if err := d.session.Open(); err != nil {
		return fmt.Errorf("failed to open discord connection: %w", err)
	}

	slog.Info("Discord bot connected as", "d_session_state_user_username", d.session.State.User.Username, "d_session_state_user_discriminator", d.session.State.User.Discriminator)
	return nil
}

// handleMessage 处理收到的消息
func (d *DiscordChannel) handleMessage(s *discordgo.Session, m *discordgo.MessageCreate) {
	slog.Info("Discord: received message from", "m_author_username", m.Author.Username, "m_content", m.Content)

	// 忽略机器人自己的消息
	if m.Author.ID == s.State.User.ID {
		slog.Debug("Discord: ignoring own message")
		return
	}

	// 忽略其他机器人的消息
	if m.Author.Bot {
		slog.Debug("Discord: ignoring bot message")
		return
	}

	// 检查是否是私信（DM 绕过 channelID 限制）
	ch, err := s.Channel(m.ChannelID)
	if err != nil {
		slog.Warn("Discord: failed to get channel info", "err", err)
		return
	}
	isDM := ch.Type == discordgo.ChannelTypeDM || ch.Type == discordgo.ChannelTypeGroupDM

	// 检查是否需要限制到特定频道（非 DM 情况下）
	if !isDM && d.channelID != "" && m.ChannelID != d.channelID {
		slog.Info("Discord: ignoring message from channel", "m_channelid", m.ChannelID, "d_channelid", d.channelID)
		return
	}

	// 检查是否是 @机器人
	isMentioned := false
	for _, mention := range m.Mentions {
		if mention.ID == s.State.User.ID {
			isMentioned = true
			break
		}
	}

	// 如果不是私信也不是 @机器人，忽略
	if !isDM && !isMentioned {
		slog.Debug("Discord: ignoring message (not DM and not mentioned)")
		return
	}

	slog.Info("Discord: processing message (isDM=", "isdm", isDM, "ismentioned", isMentioned)

	// 清理消息内容（移除 @mention）
	content := m.Content
	if isMentioned {
		content = strings.TrimSpace(strings.Replace(content, fmt.Sprintf("<@%s>", s.State.User.ID), "", -1))
		content = strings.TrimSpace(strings.Replace(content, fmt.Sprintf("<@!%s>", s.State.User.ID), "", -1))
	}

	if content == "" {
		return
	}

	// 构建消息对象
	msg := &model.Message{
		ID:         m.ID,
		Type:       model.TypeChat,
		Content:    content,
		Source:     m.Author.Username,
		Platform:   d.Name(),
		CreateTime: m.Timestamp,
		Metadata: map[string]interface{}{
			"original_msg_id": m.ID,
			"channel_id":      m.ChannelID,
			"user_id":         m.Author.ID,
			"username":        m.Author.Username,
			"discriminator":   m.Author.Discriminator,
			"is_dm":           isDM,
		},
	}

	// 调用处理器
	handler := d.GetHandler()
	if handler != nil {
		slog.Info("Discord: calling handler for message", "msg_content", msg.Content)
		handler(d.ctx, msg)
	} else {
		slog.Debug("Discord: no handler configured!")
	}
}

// Stop 停止渠道
func (d *DiscordChannel) Stop() error {
	if d.cancel != nil {
		d.cancel()
	}

	if d.session != nil {
		d.session.Close()
	}

	d.wg.Wait()
	return nil
}

// Send 发送消息
func (d *DiscordChannel) Send(ctx context.Context, msg *model.Message) error {
	if !d.CanSend() {
		return fmt.Errorf("channel %s cannot send", d.Name())
	}

	// 优先使用 Bot API 发送
	if d.session != nil && d.channelID != "" {
		return d.sendViaBot(ctx, msg)
	}

	// 回退到 Webhook（单 webhook 或多 webhook 轮询）
	if d.webhookURL != "" || len(d.webhookURLs) > 0 {
		return d.sendViaWebhook(ctx, msg)
	}

	return fmt.Errorf("no webhook or bot configured for channel %s", d.Name())
}

// sendViaBot 通过 Bot API 发送消息
func (d *DiscordChannel) sendViaBot(ctx context.Context, msg *model.Message) error {
	embed := d.buildDiscordEmbed(msg)

	if d.session != nil && d.channelID != "" {
		// 使用 Thread Pool 模式
		if d.threadPool != nil && d.threadDedup != nil {
			return d.sendViaThreadPool(ctx, msg, embed)
		}

		// 每条消息创建独立帖子（论坛 feed 可见）
		threadName := msg.Title
		if threadName == "" {
			threadName = msg.Source
		}
		if len(threadName) > 100 {
			threadName = threadName[:97] + "..."
		}
		return d.createForumThreadWithRetry(ctx, d.channelID, threadName, embed)
	}

	_, err := d.session.ChannelMessageSendEmbed(d.channelID, embed)
	return err
}

// sendViaThreadPool 通过 Thread Pool 发送消息
func (d *DiscordChannel) sendViaThreadPool(ctx context.Context, msg *model.Message, embed *discordgo.MessageEmbed) error {
	sourceName := msg.Source

	// 检查 Thread 内去重
	threadID, err := d.threadPool.GetOrCreateThread(sourceName)
	if err != nil {
		return fmt.Errorf("get/create thread failed: %w", err)
	}

	// Thread 内去重检查
	if !d.threadDedup.ShouldSend(sourceName, threadID, msg.ID, msg.Title, msg.Content) {
		slog.Debug("Discord: filtered duplicate in thread", "source", sourceName, "title", msg.Title[:min(30, len(msg.Title))])
		return nil
	}

	// 检查 rate limit
	if !d.ratelimiter.AllowMessage() {
		slog.Debug("Discord: rate limited, message queued")
		return fmt.Errorf("rate limit exceeded")
	}

	// 向 Thread 追加消息
	if err := d.threadPool.AppendMessage(threadID, embed); err != nil {
		// 如果是 429 错误，启用退避重试
		if strings.Contains(err.Error(), "429") {
			return d.retryAppendWithBackoff(ctx, sourceName, threadID, embed)
		}
		return fmt.Errorf("append message failed: %w", err)
	}

	d.threadPool.UpdateLastActivity(sourceName)
	slog.Info("Discord: sent to thread pool", "sourcename", sourceName)
	return nil
}

// retryAppendWithBackoff 指数退避重试
func (d *DiscordChannel) retryAppendWithBackoff(ctx context.Context, sourceName, threadID string, embed *discordgo.MessageEmbed) error {
	maxRetries := 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		delay := d.ratelimiter.NextRetryDelay(threadID, attempt)
		slog.Info("Discord: retrying after", "delay", delay, "attempt_1", attempt+1, "maxretries", maxRetries)
		if err := waitForContext(ctx, delay); err != nil {
			return err
		}

		if err := d.threadPool.AppendMessage(threadID, embed); err != nil {
			if attempt == maxRetries-1 {
				return fmt.Errorf("retry exhausted: %w", err)
			}
			continue
		}
		return nil
	}
	return fmt.Errorf("retry exhausted")
}

// createForumThreadWithRetry 创建 Forum 线程（带速率限制重试）
func (d *DiscordChannel) createForumThreadWithRetry(ctx context.Context, channelID, threadName string, embed *discordgo.MessageEmbed) error {
	maxRetries := 5
	baseDelay := time.Second * 12 // Discord rate limit window 通常 12-15 秒

	for i := 0; i < maxRetries; i++ {
		_, err := d.session.ForumThreadStartEmbed(channelID, threadName, 1440, embed)
		if err == nil {
			if i > 0 {
				slog.Info("Discord: ForumThread created after", "i", i)
			}
			return nil
		}

		// 检查是否是速率限制错误
		errStr := err.Error()
		if strings.Contains(errStr, "429") || strings.Contains(errStr, "rate limit") {
			delay := baseDelay
			// 尝试解析 retry_after
			if strings.Contains(errStr, "retry_after") {
				delay = time.Second * 15
			}
			slog.Info("Discord: rate limited, retrying i", "delay", delay, "i_1", i+1, "maxretries", maxRetries)
			if err := waitForContext(ctx, delay); err != nil {
				return err
			}
			continue
		}

		// 其他错误直接返回
		slog.Warn("Discord: ForumThreadStartEmbed error", "err", err)
		return err
	}

	return fmt.Errorf("discord rate limit exceeded after %d retries", maxRetries)
}

// sendViaWebhook 通过 Webhook 发送消息
func (d *DiscordChannel) sendViaWebhook(ctx context.Context, msg *model.Message) error {
	embed := embedToMap(d.buildDiscordEmbed(msg))
	payload := map[string]interface{}{
		"embeds": []map[string]interface{}{embed},
	}

	// 仅论坛频道才需 thread_name（每条建独立帖）；
	// webhook_plain=true 的普通文本频道不能带 thread_name，否则 Discord 报错
	if !d.webhookPlain && d.threadID == "" {
		threadName := msg.Title
		if threadName == "" {
			threadName = msg.Source
		}
		if len(threadName) > 100 {
			threadName = threadName[:97] + "..."
		}
		payload["thread_name"] = threadName
	}

	return d.sendRequest(ctx, d.nextWebhook(), payload)
}

// nextWebhook 轮询选取下一个 webhook，将推送分散到多个 webhook 以规避单点频控
func (d *DiscordChannel) nextWebhook() string {
	if len(d.webhookURLs) == 0 {
		return d.webhookURL
	}
	if len(d.webhookURLs) == 1 {
		return d.webhookURLs[0]
	}
	i := atomic.AddUint64(&d.webhookIdx, 1)
	return d.webhookURLs[(i-1)%uint64(len(d.webhookURLs))]
}

// SendBatch 批量发送
func (d *DiscordChannel) SendBatch(ctx context.Context, msgs []*model.Message) error {
	for i, msg := range msgs {
		if err := d.Send(ctx, msg); err != nil {
			return err
		}
		if i < len(msgs)-1 {
			if err := waitForContext(ctx, time.Second); err != nil {
				return err
			}
		}
	}
	return nil
}

// Reply 回复消息
func (d *DiscordChannel) Reply(ctx context.Context, originalMsgID string, reply *model.Message) error {
	// 获取原消息的频道 ID
	channelID := d.channelID
	if cid := reply.GetStringMetadata("channel_id"); cid != "" {
		channelID = cid
	}

	// 使用 Bot API 回复
	if d.session != nil && channelID != "" {
		content := reply.Content

		// 如果内容过长，截断
		if len(content) > 2000 {
			content = content[:1997] + "..."
		}

		_, err := d.session.ChannelMessageSendReply(channelID, content, &discordgo.MessageReference{
			MessageID: originalMsgID,
			ChannelID: channelID,
		})
		return err
	}

	// 回退到普通发送
	return d.Send(ctx, reply)
}

// sourceColor 按源类型返回 Discord embed 颜色和 emoji
// 仅保留实际注册的源类型(历史类型分支 2026-08-02 清理)。
func sourceMeta(sourceType string) (color int, emoji string) {
	switch sourceType {
	case "market-briefing":
		return 0x2ECC71, "🌅" // 绿色 — 简报(盘前/收盘/美盘前瞻)
	case "guanfu":
		return 0x9B59B6, "🔭" // 紫色 — BTC 观复
	case "us-macro-report":
		return 0x3498DB, "🌎" // 蓝色 — 宏观
	case "rss":
		return 0x5865F2, "📰" // 靛蓝 — 资讯
	case "script":
		return 0x95A5A6, "" // 脚本报告正文首行自带 emoji，避免双 emoji
	default:
		return 0x95A5A6, "📌" // 灰色 — 其他
	}
}

// buildDiscordEmbed 构建 discordgo.MessageEmbed（Bot 和 Webhook 共用）
func (d *DiscordChannel) buildDiscordEmbed(msg *model.Message) *discordgo.MessageEmbed {
	fm := channel.BuildFormattedMessage(msg, channel.FormatOptions{
		MaxTitleLen:   256,
		MaxContentLen: 4096,
		Platform:      "discord",
	})

	color, emoji := sourceMeta(msg.SourceType)
	// 防御：Discord 颜色必须在 0..0xFFFFFF，越界会导致整条 embed 被拒(400)
	if color < 0 || color > 0xFFFFFF {
		color = 0x95A5A6
	}

	embed := &discordgo.MessageEmbed{
		Color:     color,
		Timestamp: msg.CreateTime.Format(time.RFC3339),
	}

	var longTitleOverflow string
	prefix := ""
	if emoji != "" {
		prefix = emoji + " "
	}
	if fm.Title != "" {
		embed.Title = prefix + fm.Title
	} else {
		embed.Title = prefix + fm.DisplaySource
	}
	// Discord embed 标题上限 256 字符；长标题(如 trump.fm 整条推文)会被 Discord 拒收(400)。
	// 标题裁到 256(字节级，保证 ≤256 码点)，完整原文移到正文(上限 4096)避免丢内容。
	if len(embed.Title) > 256 {
		if fm.Title != "" && fm.Content == "" {
			longTitleOverflow = fm.Title // 完整原文放进 description
		}
		embed.Title = channel.ClampRunes(embed.Title, 256)
	}

	// 组装 description
	var descParts []string
	if longTitleOverflow != "" {
		descParts = append(descParts, longTitleOverflow)
	} else if fm.Content != "" {
		descParts = append(descParts, fm.Content)
	}

	if fm.AIComment != "" {
		descParts = append(descParts, "🤖 **AI 点评:** "+fm.AIComment)
	}
	if len(fm.Tags) > 0 {
		descParts = append(descParts, "🏷 **相关:** "+strings.Join(fm.Tags, ", "))
	}
	if fm.EconomicImpact != "" {
		descParts = append(descParts, fmt.Sprintf("%s **影响:** %s", fm.EconomicIcon, fm.EconomicImpact))
	}

	// 确保 embed 至少有 title 或 description
	if len(descParts) == 0 && fm.Title == "" {
		descParts = append(descParts, "(无内容)")
	}

	if len(descParts) > 0 {
		embed.Description = channel.ClampRunes(strings.Join(descParts, "\n\n"), 4096)
	}

	if fm.PrimaryLink != "" {
		embed.URL = fm.PrimaryLink
	}

	// 图片
	if fm.ImageURL != "" {
		embed.Image = &discordgo.MessageEmbedImage{URL: fm.ImageURL}
	}

	// 额外链接作为 fields
	var fields []*discordgo.MessageEmbedField
	for name, url := range fm.ExtraLinks {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   name,
			Value:  fmt.Sprintf("[点击查看](%s)", url),
			Inline: true,
		})
	}
	if len(fields) > 0 {
		embed.Fields = fields
	}

	var footerText string
	if msg.SourceType != "" {
		if emoji != "" {
			footerText = fmt.Sprintf("%s %s | %s", emoji, msg.SourceType, fm.DisplaySource)
		} else {
			footerText = fmt.Sprintf("%s | %s", msg.SourceType, fm.DisplaySource)
		}
	} else {
		footerText = fmt.Sprintf("来源: %s", fm.DisplaySource)
	}
	embed.Footer = &discordgo.MessageEmbedFooter{
		Text: footerText,
	}

	return embed
}

// embedToMap 将 discordgo.MessageEmbed 转换为 Webhook map 格式
func embedToMap(e *discordgo.MessageEmbed) map[string]interface{} {
	m := map[string]interface{}{
		"color": e.Color,
	}
	if e.Title != "" {
		m["title"] = e.Title
	}
	if e.Description != "" {
		m["description"] = e.Description
	}
	if e.URL != "" {
		m["url"] = e.URL
	}
	if e.Timestamp != "" {
		m["timestamp"] = e.Timestamp
	}
	if e.Footer != nil {
		m["footer"] = map[string]interface{}{"text": e.Footer.Text}
	}
	if e.Image != nil {
		if imageURL, err := url.Parse(e.Image.URL); err == nil &&
			(imageURL.Scheme == "http" || imageURL.Scheme == "https") && imageURL.Host != "" {
			m["image"] = map[string]interface{}{"url": e.Image.URL}
		}
	}
	if len(e.Fields) > 0 {
		var fields []map[string]interface{}
		for _, f := range e.Fields {
			fields = append(fields, map[string]interface{}{
				"name":   f.Name,
				"value":  f.Value,
				"inline": f.Inline,
			})
		}
		m["fields"] = fields
	}
	return m
}

func (d *DiscordChannel) sendRequest(ctx context.Context, url string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	if d.threadID != "" {
		if strings.Contains(url, "?") {
			url += "&thread_id=" + d.threadID
		} else {
			url += "?thread_id=" + d.threadID
		}
	}

	// 最多重试 3 次（处理 rate limit）
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(data))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := d.httpClient.Do(req)
		if err != nil {
			return err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
			return nil
		}

		// 处理 rate limit (429)
		if resp.StatusCode == http.StatusTooManyRequests {
			// 解析 retry_after
			var rateLimitResp struct {
				RetryAfter float64 `json:"retry_after"`
			}
			if json.Unmarshal(body, &rateLimitResp) == nil && rateLimitResp.RetryAfter > 0 {
				sleepDuration := time.Duration(rateLimitResp.RetryAfter*1000) * time.Millisecond
				if sleepDuration > 5*time.Second {
					sleepDuration = 5 * time.Second
				}
				if err := waitForContext(ctx, sleepDuration); err != nil {
					return err
				}
				continue
			}
			if err := waitForContext(ctx, 500*time.Millisecond); err != nil {
				return err
			}
			continue
		}

		return fmt.Errorf("discord response status: %d, body: %s", resp.StatusCode, string(body))
	}

	return fmt.Errorf("discord rate limit exceeded after %d retries", maxRetries)
}

func waitForContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// GetThreadPoolStatus 获取 Thread Pool 状态
func (d *DiscordChannel) GetThreadPoolStatus() map[string]interface{} {
	if d.threadPool == nil {
		return map[string]interface{}{"enabled": false}
	}

	threads := d.threadPool.GetAllThreads()
	activeCount := 0
	now := time.Now()
	for _, t := range threads {
		if now.Sub(t.LastMsgAt) < d.threadTTL {
			activeCount++
		}
	}

	return map[string]interface{}{
		"enabled":       true,
		"totalThreads":  len(threads),
		"activeThreads": activeCount,
		"ttl":           d.threadTTL.String(),
		"mappings":      d.threadMappings,
	}
}

// GetAllThreads 获取所有 Thread 信息 (用于 API)
func (d *DiscordChannel) GetAllThreads() map[string]interface{} {
	if d.threadPool == nil {
		return map[string]interface{}{}
	}

	threads := d.threadPool.GetAllThreads()
	result := make(map[string]interface{})
	for src, info := range threads {
		result[src] = map[string]string{
			"id":         info.ID,
			"name":       info.Name,
			"last_msg":   info.LastMsgAt.Format(time.RFC3339),
			"created_at": info.CreatedAt.Format(time.RFC3339),
		}
	}
	return result
}

// CleanupInactive 清理不活跃的 Thread
func (d *DiscordChannel) CleanupInactive() int {
	if d.threadPool == nil {
		return 0
	}
	return d.threadPool.CleanupInactive()
}

// GetDedupStats 获取去重统计
func (d *DiscordChannel) GetDedupStats() map[string]interface{} {
	if d.threadDedup == nil {
		return map[string]interface{}{"enabled": false}
	}

	return map[string]interface{}{
		"enabled":   true,
		"window":    "60 minutes",
		"threshold": 0.7,
	}
}
