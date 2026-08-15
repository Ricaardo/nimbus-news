package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/model"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

func init() {
	channel.Register("feishu", NewFeishuChannel)
}

// FeishuChannel 飞书渠道（支持双向通信）
type FeishuChannel struct {
	channel.BaseChannel
	webhook           string
	appID             string
	appSecret         string
	encryptKey        string
	verificationToken string
	client            *lark.Client
	webhookClient     *http.Client
	wsClient          *larkws.Client
	cancelFunc        context.CancelFunc
	mu                sync.Mutex
}

// NewFeishuChannel 创建飞书渠道
func NewFeishuChannel(cfg channel.Config) (channel.Channel, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = channel.ModePush
	}

	ch := &FeishuChannel{
		BaseChannel:   channel.NewBaseChannel(cfg.Name, "feishu", mode),
		webhook:       cfg.Webhook,
		webhookClient: &http.Client{Timeout: 30 * time.Second},
	}

	// 解析选项
	if cfg.Options != nil {
		if v, ok := cfg.Options["app_id"].(string); ok {
			ch.appID = v
		}
		if v, ok := cfg.Options["app_secret"].(string); ok {
			ch.appSecret = v
		}
		if v, ok := cfg.Options["encrypt_key"].(string); ok {
			ch.encryptKey = v
		}
		if v, ok := cfg.Options["verification_token"].(string); ok {
			ch.verificationToken = v
		}
	}

	// 如果需要接收消息，创建飞书客户端
	if ch.CanReceive() && ch.appID != "" && ch.appSecret != "" {
		ch.client = lark.NewClient(ch.appID, ch.appSecret,
			lark.WithLogReqAtDebug(true),
			lark.WithLogLevel(larkcore.LogLevelInfo),
		)
	}

	return ch, nil
}

// Start 启动渠道
func (f *FeishuChannel) Start(ctx context.Context) error {
	if !f.CanReceive() || f.client == nil {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	f.cancelFunc = cancel

	eventHandler := larkevent.NewEventDispatcher(f.verificationToken, f.encryptKey).
		OnP2MessageReceiveV1(f.handleMessage).
		OnP2MessageReadV1(func(ctx context.Context, event *larkim.P2MessageReadV1) error {
			return nil
		})

	f.wsClient = larkws.NewClient(f.appID, f.appSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)

	go func() {
		if err := f.wsClient.Start(ctx); err != nil {
			slog.Warn("Feishu WebSocket error", "err", err)
		}
	}()

	slog.Info("Feishu channel", "f_name", f.Name())
	return nil
}

// Stop 停止渠道
func (f *FeishuChannel) Stop() error {
	if f.cancelFunc != nil {
		f.cancelFunc()
	}
	return nil
}

// Send 发送消息
func (f *FeishuChannel) Send(ctx context.Context, msg *model.Message) error {
	if !f.CanSend() || f.webhook == "" {
		return fmt.Errorf("channel %s cannot send or no webhook configured", f.Name())
	}

	card := f.buildCard(msg)
	payload := map[string]interface{}{
		"msg_type": "interactive",
		"card":     card,
	}

	return f.sendRequest(ctx, payload)
}

// SendBatch 批量发送
func (f *FeishuChannel) SendBatch(ctx context.Context, msgs []*model.Message) error {
	for i, msg := range msgs {
		if err := f.Send(ctx, msg); err != nil {
			return err
		}
		if i < len(msgs)-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	return nil
}

// Reply 回复消息
func (f *FeishuChannel) Reply(ctx context.Context, originalMsgID string, reply *model.Message) error {
	if f.client == nil {
		return fmt.Errorf("feishu client not initialized")
	}

	cardContent := map[string]interface{}{
		"config": map[string]interface{}{
			"wide_screen_mode": true,
		},
		"header": map[string]interface{}{
			"template": "blue",
			"title": map[string]interface{}{
				"content": "Investor AI",
				"tag":     "plain_text",
			},
		},
		"elements": []map[string]interface{}{
			{
				"tag":     "markdown",
				"content": reply.Content,
			},
			{
				"tag": "note",
				"elements": []map[string]interface{}{
					{
						"tag":     "plain_text",
						"content": "⚠️ 投资有风险，决策需谨慎 | Powered by Investor",
					},
				},
			},
		},
	}

	cardBytes, err := json.Marshal(cardContent)
	if err != nil {
		return err
	}
	content := string(cardBytes)

	resp, err := f.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
		MessageId(originalMsgID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType(larkim.MsgTypeInteractive).
			Content(content).
			Build()).
		Build())

	if err != nil {
		return err
	}

	if !resp.Success() {
		return fmt.Errorf("feishu reply failed: code=%d, msg=%s", resp.Code, resp.Msg)
	}

	return nil
}

// handleMessage 处理接收的消息
func (f *FeishuChannel) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	handler := f.GetHandler()
	if handler == nil {
		return nil
	}

	content := event.Event.Message.Content
	msgID := event.Event.Message.MessageId
	chatID := event.Event.Message.ChatId
	senderID := event.Event.Sender.SenderId.OpenId

	var contentMap map[string]string
	if err := json.Unmarshal([]byte(*content), &contentMap); err != nil {
		return nil
	}
	text := contentMap["text"]

	msg := model.NewChatMessage("feishu", *chatID, *senderID, text)
	msg.ID = *msgID
	msg.SetMetadata("original_msg_id", *msgID)

	handler(ctx, msg)
	return nil
}

func (f *FeishuChannel) buildCard(msg *model.Message) map[string]interface{} {
	fm := channel.BuildFormattedMessage(msg, channel.FormatOptions{
		MaxTitleLen:   100,
		MaxContentLen: 4000,
		Platform:      "feishu",
	})

	headerTitle := fm.Title
	if headerTitle == "" {
		headerTitle = "新闻"
	}

	var elements []map[string]interface{}

	if fm.Content != "" {
		elements = append(elements, map[string]interface{}{
			"tag": "div",
			"text": map[string]interface{}{
				"tag":     "lark_md",
				"content": fmt.Sprintf("<font color=\"#1e80ff\">%s</font>", fm.Content),
			},
		})
	}

	// 飞书图片元素只接受已上传至飞书后的 image_key。公网 URL
	// 不是 image_key，直接使用会导致整张卡片被飞书拒绝。
	if imageKey := feishuImageKey(fm.Raw, fm.ImageURL); imageKey != "" {
		elements = append(elements, map[string]interface{}{
			"tag":     "img",
			"img_key": imageKey,
			"alt": map[string]interface{}{
				"tag":     "lark_md",
				"content": "",
			},
		})
	} else if imageURL := safeExternalImageURL(fm.ImageURL); imageURL != "" {
		elements = append(elements, map[string]interface{}{
			"tag": "div",
			"text": map[string]interface{}{
				"tag":     "plain_text",
				"content": "图片: " + imageURL,
			},
		})
	}

	if fm.AIComment != "" {
		elements = append(elements, map[string]interface{}{
			"tag": "div",
			"text": map[string]interface{}{
				"tag":     "lark_md",
				"content": fmt.Sprintf("🤖 **AI点评:** %s", fm.AIComment),
			},
		})
	}

	if len(fm.Tags) > 0 {
		elements = append(elements, map[string]interface{}{
			"tag": "div",
			"text": map[string]interface{}{
				"tag":     "lark_md",
				"content": fmt.Sprintf("<font color=\"#626f86\">相关代币:</font>\n%s", strings.Join(fm.Tags, "\n")),
			},
		})
	}

	if fm.EconomicImpact != "" {
		elements = append(elements, map[string]interface{}{
			"tag": "div",
			"text": map[string]interface{}{
				"tag":     "lark_md",
				"content": fmt.Sprintf("%s 经济影响:\n%s", fm.EconomicIcon, fm.EconomicImpact),
			},
		})
	}

	// 链接区域
	links := make(map[string]string)
	for k, v := range fm.ExtraLinks {
		links[k] = v
	}
	if fm.PrimaryLink != "" {
		links["原文"] = fm.PrimaryLink
	}
	if len(links) > 0 {
		linkText := channel.FormatLinks(links, channel.LinkFormatLarkMD, " | ")
		elements = append(elements, map[string]interface{}{
			"tag": "div",
			"text": map[string]interface{}{
				"tag":     "lark_md",
				"content": "🔗 " + linkText,
			},
		})
	}

	if fm.DisplaySource != "" {
		elements = append(elements, map[string]interface{}{
			"tag": "note",
			"elements": []map[string]interface{}{
				{"tag": "plain_text", "content": fmt.Sprintf("来源: %s · %s", fm.DisplaySource, fm.Raw.CreateTime.Format("2006-01-02 15:04:05"))},
			},
		})
	}

	return map[string]interface{}{
		"header": map[string]interface{}{
			"title": map[string]interface{}{
				"tag":     "lark_md",
				"content": fmt.Sprintf("<font color=\"#1890ff\">%s</font>", headerTitle),
			},
			"template": "blue",
		},
		"elements": elements,
	}
}

func feishuImageKey(msg *model.Message, fallback string) string {
	if msg != nil {
		if key := msg.GetStringMetadata("feishu_image_key"); validFeishuImageKey(key) {
			return key
		}
	}
	if validFeishuImageKey(fallback) {
		return fallback
	}
	return ""
}

func validFeishuImageKey(value string) bool {
	if !strings.HasPrefix(value, "img_") || len(value) <= len("img_") || len(value) > 512 {
		return false
	}
	for _, r := range value {
		isAlphaNumeric := r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9'
		if !isAlphaNumeric && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func safeExternalImageURL(value string) string {
	if len(value) == 0 || len(value) > 2048 {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	return parsed.String()
}

func (f *FeishuChannel) sendRequest(ctx context.Context, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.webhook, bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.webhookClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("feishu response status: %d", resp.StatusCode)
	}
	return nil
}
