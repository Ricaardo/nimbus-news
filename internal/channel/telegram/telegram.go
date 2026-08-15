package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// 确保 channel 包已导入（用于 formatter）
var _ = channel.DefaultFormatter

func init() {
	channel.Register("telegram", NewTelegramChannel)
}

// TelegramChannel Telegram 渠道
type TelegramChannel struct {
	channel.BaseChannel
	apiURL     string
	botToken   string
	chatID     string
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
	lastUpdate int64
	flushCh    chan struct{}
}

// NewTelegramChannel 创建 Telegram 渠道
func NewTelegramChannel(cfg channel.Config) (channel.Channel, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = channel.ModePush
	}

	ch := &TelegramChannel{
		BaseChannel: channel.NewBaseChannel(cfg.Name, "telegram", mode),
	}

	// 解析 webhook URL，提取 chat_id
	if cfg.Webhook != "" {
		u, err := url.Parse(cfg.Webhook)
		if err != nil {
			return nil, fmt.Errorf("invalid telegram webhook URL: %w", err)
		}

		ch.chatID = u.Query().Get("chat_id")
		if ch.chatID == "" {
			return nil, fmt.Errorf("telegram webhook URL missing chat_id parameter")
		}

		ch.apiURL = fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, u.Path)
	}

	// 从 options 获取配置
	if cfg.Options != nil {
		if v, ok := cfg.Options["bot_token"].(string); ok {
			ch.botToken = v
			ch.apiURL = fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", v)
		}
		if v, ok := cfg.Options["chat_id"].(string); ok {
			ch.chatID = v
		}
	}

	return ch, nil
}

// Start 启动渠道
func (t *TelegramChannel) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	t.cancelFunc = cancel

	// 启动合并发送协程
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.startFlusher(ctx)
	}()

	if !t.CanReceive() || t.botToken == "" {
		slog.Info("Telegram channel", "t_name", t.Name())
		return nil
	}

	// 启动轮询接收消息
	t.wg.Add(1)
	go t.pollUpdates(ctx)

	slog.Info("Telegram channel", "t_name", t.Name())
	return nil
}

// Stop 停止渠道
func (t *TelegramChannel) Stop() error {
	if t.cancelFunc != nil {
		t.cancelFunc()
	}
	t.wg.Wait()
	return nil
}

// pollUpdates 轮询获取消息更新
func (t *TelegramChannel) pollUpdates(ctx context.Context) {
	defer t.wg.Done()

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", t.botToken)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.fetchUpdates(ctx, apiURL)
		}
	}
}

// fetchUpdates 获取并处理更新
func (t *TelegramChannel) fetchUpdates(ctx context.Context, apiURL string) {
	handler := t.GetHandler()
	if handler == nil {
		return
	}

	payload := map[string]interface{}{
		"offset":  t.lastUpdate + 1,
		"timeout": 1,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(data))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	var result struct {
		OK     bool `json:"ok"`
		Result []struct {
			UpdateID int64 `json:"update_id"`
			Message  *struct {
				MessageID int64 `json:"message_id"`
				Chat      struct {
					ID int64 `json:"id"`
				} `json:"chat"`
				From struct {
					ID       int64  `json:"id"`
					Username string `json:"username"`
				} `json:"from"`
				Text string `json:"text"`
			} `json:"message"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return
	}

	if !result.OK {
		return
	}

	for _, update := range result.Result {
		t.lastUpdate = update.UpdateID
		if update.Message == nil || update.Message.Text == "" {
			continue
		}

		// 过滤群消息：只处理配置的 chat_id 或私聊
		chatIDStr := strconv.FormatInt(update.Message.Chat.ID, 10)
		if t.chatID != "" && chatIDStr != t.chatID && update.Message.Chat.ID > 0 {
			// 如果配置了 chat_id 且不匹配且是群聊，跳过
			// 但如果是私聊 (chat.id == from.id)，允许
			if update.Message.Chat.ID != update.Message.From.ID {
				continue
			}
		}

		msg := model.NewChatMessage(
			t.Name(),
			chatIDStr,
			strconv.FormatInt(update.Message.From.ID, 10),
			update.Message.Text,
		)
		msg.ID = strconv.FormatInt(update.Message.MessageID, 10)
		msg.SetMetadata("original_msg_id", msg.ID)
		msg.SetMetadata("chat_id", chatIDStr) // 保存实际 chat_id 用于回复

		handler(ctx, msg)
	}
}

// Send 直接发送单条消息（带 429 重试）
func (t *TelegramChannel) Send(ctx context.Context, msg *model.Message) error {
	if !t.CanSend() || t.apiURL == "" {
		return fmt.Errorf("channel %s cannot send or no API URL configured", t.Name())
	}

	text := t.buildMessage(msg)
	payload := map[string]interface{}{
		"chat_id":                  t.chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}

	for attempt := 0; attempt < 3; attempt++ {
		err := t.sendRequest(payload)
		if err == nil {
			return nil
		}
		if strings.Contains(err.Error(), "429") {
			var retrySec int
			fmt.Sscanf(err.Error(), "%*sretry_after %d", &retrySec)
			wait := time.Duration(retrySec) * time.Second
			if wait <= 0 {
				wait = 10 * time.Second
			}
			slog.Info("TG rate limited, waiting", "wait", wait)
			time.Sleep(wait)
			continue
		}
		return err
	}
	return fmt.Errorf("TG send failed after retries")
}

// startFlusher 空实现，保留接口兼容
func (t *TelegramChannel) startFlusher(ctx context.Context) {
	t.flushCh = make(chan struct{}, 1)
	<-ctx.Done()
}

// SendBatch 批量发送
func (t *TelegramChannel) SendBatch(ctx context.Context, msgs []*model.Message) error {
	for _, msg := range msgs {
		if err := t.Send(ctx, msg); err != nil {
			return err
		}
	}
	return nil
}

// Reply 回复消息
func (t *TelegramChannel) Reply(ctx context.Context, originalMsgID string, reply *model.Message) error {
	if t.botToken == "" {
		return fmt.Errorf("telegram bot token not configured")
	}

	// 从 reply 消息的 metadata 获取实际的 chat_id
	chatID := t.chatID
	if replyToChatID := reply.GetStringMetadata("chat_id"); replyToChatID != "" {
		chatID = replyToChatID
	}

	text := reply.Content
	payload := map[string]interface{}{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"reply_to_message_id":      originalMsgID,
		"disable_web_page_preview": true,
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)
	return t.sendRequestTo(apiURL, payload)
}

func (t *TelegramChannel) sendRequestTo(apiURL string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram response status: %d, body: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (t *TelegramChannel) buildMessage(msg *model.Message) string {
	// TG 单条上限 4096 字符；留出标题/AI 点评/标签/页脚的余量。
	// 之前固定 500 会把观复读盘、A 股扫描、宏观报告等长正文截断。
	fm := channel.BuildFormattedMessage(msg, channel.FormatOptions{
		MaxTitleLen:   256,
		MaxContentLen: 3200,
		EscapeHTML:    false,
		Platform:      "telegram",
	})

	sourceEmoji := channel.SourceEmojiForType(msg.SourceType)
	_ = sourceEmoji
	var sb strings.Builder

	sb.WriteString(fm.Icon)
	sb.WriteString(" <b>")
	sb.WriteString(escapeHTML(fm.Title))
	sb.WriteString("</b>\n")
	sb.WriteString("━━━━━━━━━━━━━━\n")

	if fm.Content != "" {
		sb.WriteString("\n")
		sb.WriteString(escapeHTML(fm.Content))
		sb.WriteString("\n")
	}

	if fm.AIComment != "" {
		sb.WriteString("\n🤖 <b>AI 点评</b>\n")
		sb.WriteString(escapeHTML(fm.AIComment))
		sb.WriteString("\n")
	}

	if len(fm.Tags) > 0 {
		sb.WriteString("\n🏷 ")
		var tagStrs []string
		for _, tag := range fm.Tags {
			tagStrs = append(tagStrs, "<code>"+escapeHTML(tag)+"</code>")
		}
		sb.WriteString(strings.Join(tagStrs, " "))
		sb.WriteString("\n")
	}

	if fm.EconomicImpact != "" {
		sb.WriteString("\n")
		sb.WriteString(fm.EconomicIcon)
		sb.WriteString(" <b>影响:</b> ")
		sb.WriteString(escapeHTML(fm.EconomicImpact))
		sb.WriteString("\n")
	}

	// 最终保护：TG 单条上限 4096 字符，超出会被拒发（= 静默漏推）
	return channel.ClampRunes(sb.String(), 4096)
}

func (t *TelegramChannel) sendRequest(payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := http.Post(t.apiURL, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram response status: %d, body: %s", resp.StatusCode, string(body))
	}
	return nil
}

func escapeHTML(text string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(text)
}
