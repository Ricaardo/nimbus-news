package wechat

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

func init() {
	channel.Register("wechat", NewWechatChannel)
}

// WechatChannel 企业微信渠道
type WechatChannel struct {
	channel.BaseChannel
	webhook    string
	httpClient *http.Client
}

// NewWechatChannel 创建企业微信渠道
func NewWechatChannel(cfg channel.Config) (channel.Channel, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = channel.ModePush
	}

	return &WechatChannel{
		BaseChannel: channel.NewBaseChannel(cfg.Name, "wechat", mode),
		webhook:     cfg.Webhook,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Start 启动渠道
func (w *WechatChannel) Start(ctx context.Context) error {
	slog.Info("Wechat channel", "w_name", w.Name())
	return nil
}

// Stop 停止渠道
func (w *WechatChannel) Stop() error {
	return nil
}

// Send 发送消息
func (w *WechatChannel) Send(ctx context.Context, msg *model.Message) error {
	if !w.CanSend() || w.webhook == "" {
		return fmt.Errorf("channel %s cannot send or no webhook configured", w.Name())
	}

	// 结构化报告类消息用 markdown，普通新闻用 text；超限时分段发送。
	if model.IsStructuredReport(msg.SourceType) {
		for _, part := range splitWechatText(w.buildMarkdownContent(msg), 4096) {
			payload := map[string]interface{}{"msgtype": "markdown", "markdown": map[string]string{"content": part}}
			if err := w.sendRequest(ctx, payload); err != nil {
				return err
			}
		}
	} else {
		for _, part := range splitWechatText(w.buildContent(msg), 2000) {
			payload := map[string]interface{}{"msgtype": "text", "text": map[string]string{"content": part}}
			if err := w.sendRequest(ctx, payload); err != nil {
				return err
			}
		}
	}

	// 发送主图及附加图片。
	imageURLs := append([]string{}, msg.ImageURL)
	imageURLs = append(imageURLs, msg.ImageURLs...)
	seenImages := map[string]bool{}
	for _, imageURL := range imageURLs {
		if imageURL == "" || seenImages[imageURL] {
			continue
		}
		seenImages[imageURL] = true
		if err := w.sendImageMessage(ctx, imageURL); err != nil {
			slog.Warn("wechat image send failed", "url", imageURL, "error", err)
		}
	}
	return nil
}

// sendImageMessage 下载图片并作为 image 消息发送(企微限制 2MB)。
// 安全护栏:仅 HTTPS 且禁止私网/保留 IP(防未来调用方传入外部可控 URL 形成 SSRF)。
func (w *WechatChannel) sendImageMessage(ctx context.Context, url string) error {
	if err := validateImageURL(url); err != nil {
		return err
	}
	data, err := w.downloadImage(ctx, url)
	if err != nil {
		return err
	}
	payload := map[string]interface{}{
		"msgtype": "image",
		"image": map[string]string{
			"base64": buildImageBase64(data),
			"md5":    buildImageMD5(data),
		},
	}
	return w.sendRequest(ctx, payload)
}

// downloadImage 拉取图片正文(2MB 上限)。
func (w *WechatChannel) downloadImage(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image fetch %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, fmt.Errorf("image too large: %d bytes", len(data))
	}
	return data, nil
}

// validateImageURL 仅允许 HTTPS 且非私网/保留地址的图片 URL。
func validateImageURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid image url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("image url must be https, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("image url missing host")
	}
	// 域名直连走 DNS,兜底拦截 IP 字面量的私网/保留段
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("image url blocked: private/reserved address %s", host)
		}
	}
	return nil
}

// buildImageBase64 企微 image 消息要求纯 base64(无 data: 前缀)。
func buildImageBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

// buildImageMD5 企微 image 消息 md5(小写 hex)。
func buildImageMD5(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

// buildMarkdownContent 构建企业微信 markdown 格式（结构化报告）
func (w *WechatChannel) buildMarkdownContent(msg *model.Message) string {
	fm := channel.BuildFormattedMessage(msg, channel.FormatOptions{
		MaxContentLen: 0,
		Platform:      "wechat",
	})

	emoji := channel.SourceEmojiForType(msg.SourceType)
	var sb strings.Builder

	if fm.Title != "" {
		sb.WriteString(fmt.Sprintf("## %s %s\n", emoji, fm.Title))
	}

	if fm.Content != "" {
		sb.WriteString(fm.Content)
		sb.WriteString("\n")
	}

	if fm.AIComment != "" {
		sb.WriteString(fmt.Sprintf("\n> 🤖 %s\n", fm.AIComment))
	}

	sb.WriteString(fmt.Sprintf("\n<font color=\"comment\">%s %s · %s</font>", emoji, fm.DisplaySource, fm.Timestamp))

	return sb.String()
}

// SendBatch 批量发送
func splitWechatText(text string, maxBytes int) []string {
	var parts []string
	for len(text) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		if idx := strings.LastIndex(text[:cut], "\n"); idx > maxBytes/2 {
			cut = idx
		}
		parts = append(parts, text[:cut])
		text = strings.TrimLeft(text[cut:], "\n")
	}
	if text != "" {
		parts = append(parts, text)
	}
	return parts
}

func (w *WechatChannel) SendBatch(ctx context.Context, msgs []*model.Message) error {
	for i, msg := range msgs {
		if err := w.Send(ctx, msg); err != nil {
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

// Reply 回复消息（企业微信 Webhook 不支持回复）
func (w *WechatChannel) Reply(ctx context.Context, originalMsgID string, reply *model.Message) error {
	return w.Send(ctx, reply)
}

func (w *WechatChannel) buildContent(msg *model.Message) string {
	fm := channel.BuildFormattedMessage(msg, channel.FormatOptions{
		MaxTitleLen:   0,
		MaxContentLen: 0,
		Platform:      "wechat",
	})

	emoji := channel.SourceEmojiForType(msg.SourceType)
	var sb strings.Builder

	if fm.Title != "" {
		sb.WriteString(fmt.Sprintf("%s %s\n", emoji, fm.Title))
		sb.WriteString("━━━━━━━━━━━━━━\n\n")
	}

	if fm.Content != "" {
		sb.WriteString(fm.Content)
		sb.WriteString("\n")
	}

	if fm.AIComment != "" {
		sb.WriteString(fmt.Sprintf("\n🤖 %s\n", fm.AIComment))
	}

	if len(fm.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("\n🏷 %s\n", strings.Join(fm.Tags, " ")))
	}

	if fm.EconomicImpact != "" {
		sb.WriteString(fmt.Sprintf("\n%s %s\n", fm.EconomicIcon, fm.EconomicImpact))
	}

	sb.WriteString(fmt.Sprintf("\n\n%s %s | %s", emoji, fm.DisplaySource, fm.Timestamp))

	return sb.String()
}

func (w *WechatChannel) sendRequest(ctx context.Context, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.webhook, bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wechat response status: %d", resp.StatusCode)
	}
	return nil
}
