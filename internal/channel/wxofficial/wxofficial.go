package wxofficial

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log/slog"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/model"

	"github.com/gin-gonic/gin"
)

func init() {
	channel.Register("wxofficial", NewWxOfficialChannel)
}

// WxOfficialChannel 微信公众号渠道（支持双向通信）
type WxOfficialChannel struct {
	channel.BaseChannel

	// 公众号配置
	appID          string
	appSecret      string
	token          string // 用于验证消息来源
	encodingAESKey string // 消息加解密密钥（可选）
	templateID     string // 模板消息ID（用于主动推送）

	// HTTP 服务
	port       int
	server     *http.Server
	router     *gin.Engine
	cancelFunc context.CancelFunc

	// Access Token 缓存
	accessToken   string
	tokenExpireAt time.Time
	tokenMu       sync.RWMutex

	// 订阅用户列表（用于群发）
	subscribers   []string
	subscribersMu sync.RWMutex

	mu sync.Mutex
}

// WxMessage 微信消息结构
type WxMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content,omitempty"`
	MsgId        int64    `xml:"MsgId,omitempty"`
	Event        string   `xml:"Event,omitempty"`
	EventKey     string   `xml:"EventKey,omitempty"`
	// 图片消息
	PicUrl  string `xml:"PicUrl,omitempty"`
	MediaId string `xml:"MediaId,omitempty"`
	// 语音消息
	Recognition string `xml:"Recognition,omitempty"`
}

// WxReplyMessage 微信回复消息结构
type WxReplyMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content,omitempty"`
}

// NewWxOfficialChannel 创建微信公众号渠道
func NewWxOfficialChannel(cfg channel.Config) (channel.Channel, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = channel.ModeBidirectional
	}

	ch := &WxOfficialChannel{
		BaseChannel: channel.NewBaseChannel(cfg.Name, "wxofficial", mode),
		port:        8080,
	}

	// 解析选项
	if cfg.Options != nil {
		if v, ok := cfg.Options["app_id"].(string); ok {
			ch.appID = v
		}
		if v, ok := cfg.Options["app_secret"].(string); ok {
			ch.appSecret = v
		}
		if v, ok := cfg.Options["token"].(string); ok {
			ch.token = v
		}
		if v, ok := cfg.Options["encoding_aes_key"].(string); ok {
			ch.encodingAESKey = v
		}
		if v, ok := cfg.Options["template_id"].(string); ok {
			ch.templateID = v
		}
		if p, ok := cfg.Options["port"].(float64); ok {
			ch.port = int(p)
		}
		if p, ok := cfg.Options["port"].(int); ok {
			ch.port = p
		}
	}

	gin.SetMode(gin.ReleaseMode)
	ch.router = gin.New()
	ch.router.Use(gin.Recovery())

	return ch, nil
}

// Start 启动渠道
func (w *WxOfficialChannel) Start(ctx context.Context) error {
	if !w.CanReceive() {
		slog.Info("WxOfficial channel", "w_name", w.Name())
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	w.cancelFunc = cancel

	w.setupRoutes()

	w.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", w.port),
		Handler: w.router,
	}

	go func() {
		slog.Info("WxOfficial channel", "w_name", w.Name(), "port", w.port)
		if err := w.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Warn("WxOfficial server error", "err", err)
		}
	}()

	return nil
}

// Stop 停止渠道
func (w *WxOfficialChannel) Stop() error {
	if w.cancelFunc != nil {
		w.cancelFunc()
	}
	if w.server != nil {
		return w.server.Shutdown(context.Background())
	}
	return nil
}

// Send 发送消息
// 支持三种模式:
// 1. 指定 UserID/ChatID: 发送客服消息（需用户48小时内互动过）
// 2. 配置 template_id: 使用模板消息群发给所有订阅用户
// 3. 都没有: 返回错误
func (w *WxOfficialChannel) Send(ctx context.Context, msg *model.Message) error {
	if !w.CanSend() {
		return fmt.Errorf("channel %s cannot send", w.Name())
	}

	// 获取目标用户 OpenID
	openID := msg.UserID
	if openID == "" {
		openID = msg.ChatID
	}

	// 如果指定了用户，发送客服消息
	if openID != "" {
		return w.sendCustomMessage(openID, w.buildContent(msg))
	}

	// 如果配置了模板ID，使用模板消息群发
	if w.templateID != "" {
		return w.sendTemplateMessageToAll(msg)
	}

	// 尝试群发给所有订阅用户（客服消息方式，仅48小时内活跃用户能收到）
	return w.broadcastToSubscribers(msg)
}

// SendBatch 批量发送
func (w *WxOfficialChannel) SendBatch(ctx context.Context, msgs []*model.Message) error {
	for i, msg := range msgs {
		if err := w.Send(ctx, msg); err != nil {
			return err
		}
		if i < len(msgs)-1 {
			time.Sleep(time.Second)
		}
	}
	return nil
}

// Reply 回复消息
func (w *WxOfficialChannel) Reply(ctx context.Context, originalMsgID string, reply *model.Message) error {
	// 公众号的回复通过被动回复机制处理
	// 这里使用客服消息作为补充回复
	return w.Send(ctx, reply)
}

// setupRoutes 设置路由
func (w *WxOfficialChannel) setupRoutes() {
	// 公众号回调接口
	w.router.GET("/wx/callback", w.handleVerify)
	w.router.POST("/wx/callback", w.handleMessage)

	// 健康检查
	w.router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "channel": "wxofficial"})
	})
}

// handleVerify 处理微信服务器验证
func (w *WxOfficialChannel) handleVerify(c *gin.Context) {
	signature := c.Query("signature")
	timestamp := c.Query("timestamp")
	nonce := c.Query("nonce")
	echostr := c.Query("echostr")

	if w.checkSignature(signature, timestamp, nonce) {
		c.String(http.StatusOK, echostr)
	} else {
		c.String(http.StatusForbidden, "Invalid signature")
	}
}

// handleMessage 处理用户消息
func (w *WxOfficialChannel) handleMessage(c *gin.Context) {
	// 验证签名
	signature := c.Query("signature")
	timestamp := c.Query("timestamp")
	nonce := c.Query("nonce")

	if !w.checkSignature(signature, timestamp, nonce) {
		c.String(http.StatusForbidden, "Invalid signature")
		return
	}

	// 解析消息
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.String(http.StatusBadRequest, "Failed to read body")
		return
	}

	var wxMsg WxMessage
	if err := xml.Unmarshal(body, &wxMsg); err != nil {
		c.String(http.StatusBadRequest, "Failed to parse message")
		return
	}

	// 处理消息
	handler := w.GetHandler()
	if handler == nil {
		// 没有处理器，返回默认回复
		w.replyText(c, &wxMsg, "服务暂时不可用，请稍后再试")
		return
	}

	// 根据消息类型处理
	var userText string
	switch wxMsg.MsgType {
	case "text":
		userText = wxMsg.Content
	case "voice":
		// 语音消息，使用语音识别结果
		userText = wxMsg.Recognition
		if userText == "" {
			w.replyText(c, &wxMsg, "抱歉，无法识别语音内容")
			return
		}
	case "event":
		// 事件消息
		switch wxMsg.Event {
		case "subscribe":
			w.addSubscriber(wxMsg.FromUserName)
			w.replyText(c, &wxMsg, "欢迎关注！发送任意消息与 AI 对话。")
			return
		case "unsubscribe":
			w.removeSubscriber(wxMsg.FromUserName)
		}
		c.String(http.StatusOK, "success")
		return
	default:
		w.replyText(c, &wxMsg, "暂不支持该消息类型，请发送文字消息")
		return
	}

	// 创建消息对象
	msg := model.NewChatMessage("wxofficial", wxMsg.FromUserName, wxMsg.FromUserName, userText)
	msg.ID = fmt.Sprintf("%d", wxMsg.MsgId)
	msg.SetMetadata("original_msg_id", msg.ID)
	msg.SetMetadata("to_user", wxMsg.ToUserName)

	// 创建响应通道
	respChan := make(chan string, 1)
	msg.SetMetadata("response_chan", respChan)

	// 异步处理（微信要求5秒内响应）
	go handler(c.Request.Context(), msg)

	// 等待响应（最多4秒）
	select {
	case response := <-respChan:
		w.replyText(c, &wxMsg, response)
	case <-time.After(4 * time.Second):
		// 超时，先返回空响应，后续通过客服消息发送
		c.String(http.StatusOK, "success")
		// 等待真正的响应并通过客服消息发送
		go func() {
			select {
			case response := <-respChan:
				w.sendCustomMessage(wxMsg.FromUserName, response)
			case <-time.After(60 * time.Second):
				// 60秒后放弃
			}
		}()
	}
}

// replyText 被动回复文本消息
func (w *WxOfficialChannel) replyText(c *gin.Context, wxMsg *WxMessage, content string) {
	reply := WxReplyMessage{
		ToUserName:   wxMsg.FromUserName,
		FromUserName: wxMsg.ToUserName,
		CreateTime:   time.Now().Unix(),
		MsgType:      "text",
		Content:      content,
	}

	data, _ := xml.Marshal(reply)
	c.Data(http.StatusOK, "application/xml", data)
}

// checkSignature 验证签名
func (w *WxOfficialChannel) checkSignature(signature, timestamp, nonce string) bool {
	if w.token == "" {
		return true // 未配置token，跳过验证
	}

	strs := []string{w.token, timestamp, nonce}
	sort.Strings(strs)
	str := strings.Join(strs, "")

	h := sha1.New()
	h.Write([]byte(str))
	calculatedSignature := hex.EncodeToString(h.Sum(nil))

	return calculatedSignature == signature
}

// getAccessToken 获取 Access Token
func (w *WxOfficialChannel) getAccessToken() (string, error) {
	w.tokenMu.RLock()
	if w.accessToken != "" && time.Now().Before(w.tokenExpireAt) {
		token := w.accessToken
		w.tokenMu.RUnlock()
		return token, nil
	}
	w.tokenMu.RUnlock()

	w.tokenMu.Lock()
	defer w.tokenMu.Unlock()

	// 双重检查
	if w.accessToken != "" && time.Now().Before(w.tokenExpireAt) {
		return w.accessToken, nil
	}

	url := fmt.Sprintf("https://api.weixin.qq.com/cgi-bin/token?grant_type=client_credential&appid=%s&secret=%s",
		w.appID, w.appSecret)

	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if result.ErrCode != 0 {
		return "", fmt.Errorf("get access token failed: %s", result.ErrMsg)
	}

	w.accessToken = result.AccessToken
	w.tokenExpireAt = time.Now().Add(time.Duration(result.ExpiresIn-300) * time.Second) // 提前5分钟过期

	return w.accessToken, nil
}

// sendTemplateMessageToAll 使用模板消息群发给所有订阅用户
func (w *WxOfficialChannel) sendTemplateMessageToAll(msg *model.Message) error {
	subscribers := w.getSubscribers()
	if len(subscribers) == 0 {
		// 尝试刷新订阅者列表
		if err := w.refreshSubscribers(); err != nil {
			return fmt.Errorf("no subscribers and failed to refresh: %w", err)
		}
		subscribers = w.getSubscribers()
	}

	if len(subscribers) == 0 {
		return fmt.Errorf("no subscribers to send")
	}

	var lastErr error
	successCount := 0
	for _, openID := range subscribers {
		if err := w.sendTemplateMessage(openID, msg); err != nil {
			lastErr = err
			slog.Warn("Failed to send template message to", "openid", openID, "err", err)
		} else {
			successCount++
		}
	}

	slog.Info("Template message sent", "successcount", successCount, "len_subscribers", len(subscribers))
	return lastErr
}

// sendTemplateMessage 发送模板消息给单个用户
func (w *WxOfficialChannel) sendTemplateMessage(openID string, msg *model.Message) error {
	token, err := w.getAccessToken()
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://api.weixin.qq.com/cgi-bin/message/template/send?access_token=%s", token)

	// 构建模板数据
	title := msg.Title
	if title == "" {
		title = "新消息"
	}
	content := msg.Content
	if len(content) > 200 {
		content = content[:200] + "..."
	}

	payload := map[string]interface{}{
		"touser":      openID,
		"template_id": w.templateID,
		"url":         msg.Link, // 点击跳转链接
		"data": map[string]interface{}{
			"first": map[string]string{
				"value": title,
				"color": "#173177",
			},
			"keyword1": map[string]string{
				"value": content,
				"color": "#173177",
			},
			"keyword2": map[string]string{
				"value": msg.Source,
				"color": "#173177",
			},
			"remark": map[string]string{
				"value": msg.CreateTime.Format("2006-01-02 15:04"),
				"color": "#999999",
			},
		},
	}

	data, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if result.ErrCode != 0 {
		return fmt.Errorf("send template message failed: %s", result.ErrMsg)
	}

	return nil
}

// broadcastToSubscribers 使用客服消息群发（仅48小时内活跃用户能收到）
func (w *WxOfficialChannel) broadcastToSubscribers(msg *model.Message) error {
	subscribers := w.getSubscribers()
	if len(subscribers) == 0 {
		if err := w.refreshSubscribers(); err != nil {
			return fmt.Errorf("no subscribers and failed to refresh: %w", err)
		}
		subscribers = w.getSubscribers()
	}

	if len(subscribers) == 0 {
		return fmt.Errorf("no subscribers to send")
	}

	content := w.buildContent(msg)
	var lastErr error
	successCount := 0

	for _, openID := range subscribers {
		if err := w.sendCustomMessage(openID, content); err != nil {
			// 客服消息失败通常是因为用户超过48小时未互动，这是正常的
			lastErr = err
		} else {
			successCount++
		}
		time.Sleep(50 * time.Millisecond) // 避免请求过快
	}

	slog.Info("Broadcast sent", "successcount", successCount, "len_subscribers", len(subscribers))
	return lastErr
}

// getSubscribers 获取订阅者列表
func (w *WxOfficialChannel) getSubscribers() []string {
	w.subscribersMu.RLock()
	defer w.subscribersMu.RUnlock()
	return w.subscribers
}

// refreshSubscribers 刷新订阅者列表
func (w *WxOfficialChannel) refreshSubscribers() error {
	token, err := w.getAccessToken()
	if err != nil {
		return err
	}

	var allOpenIDs []string
	nextOpenID := ""

	for {
		url := fmt.Sprintf("https://api.weixin.qq.com/cgi-bin/user/get?access_token=%s&next_openid=%s", token, nextOpenID)
		resp, err := http.Get(url)
		if err != nil {
			return err
		}

		var result struct {
			Total int `json:"total"`
			Count int `json:"count"`
			Data  struct {
				OpenID []string `json:"openid"`
			} `json:"data"`
			NextOpenID string `json:"next_openid"`
			ErrCode    int    `json:"errcode"`
			ErrMsg     string `json:"errmsg"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return err
		}
		resp.Body.Close()

		if result.ErrCode != 0 {
			return fmt.Errorf("get user list failed: %s", result.ErrMsg)
		}

		allOpenIDs = append(allOpenIDs, result.Data.OpenID...)

		if result.Count < 10000 || result.NextOpenID == "" {
			break
		}
		nextOpenID = result.NextOpenID
	}

	w.subscribersMu.Lock()
	w.subscribers = allOpenIDs
	w.subscribersMu.Unlock()

	slog.Info("Refreshed subscribers", "len_allopenids", len(allOpenIDs))
	return nil
}

// addSubscriber 添加订阅者（关注时调用）
func (w *WxOfficialChannel) addSubscriber(openID string) {
	w.subscribersMu.Lock()
	defer w.subscribersMu.Unlock()

	// 检查是否已存在
	for _, id := range w.subscribers {
		if id == openID {
			return
		}
	}
	w.subscribers = append(w.subscribers, openID)
}

// removeSubscriber 移除订阅者（取消关注时调用）
func (w *WxOfficialChannel) removeSubscriber(openID string) {
	w.subscribersMu.Lock()
	defer w.subscribersMu.Unlock()

	for i, id := range w.subscribers {
		if id == openID {
			w.subscribers = append(w.subscribers[:i], w.subscribers[i+1:]...)
			return
		}
	}
}

// sendCustomMessage 发送客服消息
func (w *WxOfficialChannel) sendCustomMessage(openID, content string) error {
	token, err := w.getAccessToken()
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://api.weixin.qq.com/cgi-bin/message/custom/send?access_token=%s", token)

	payload := map[string]interface{}{
		"touser":  openID,
		"msgtype": "text",
		"text": map[string]string{
			"content": content,
		},
	}

	data, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if result.ErrCode != 0 {
		return fmt.Errorf("send custom message failed: %s", result.ErrMsg)
	}

	return nil
}

// buildContent 构建消息内容
func (w *WxOfficialChannel) buildContent(msg *model.Message) string {
	fm := channel.BuildFormattedMessage(msg, channel.FormatOptions{
		MaxTitleLen:   100,
		MaxContentLen: 2000,
		Platform:      "wxofficial",
	})

	var sb strings.Builder

	if fm.Title != "" {
		sb.WriteString(fmt.Sprintf("%s %s\n\n", fm.Icon, fm.Title))
	}

	if fm.Content != "" {
		sb.WriteString(fm.Content)
		sb.WriteString("\n\n")
	}

	if fm.AIComment != "" {
		sb.WriteString(fmt.Sprintf("🤖 AI点评: %s\n\n", fm.AIComment))
	}

	if len(fm.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("💰 相关: %s\n\n", strings.Join(fm.Tags, " ")))
	}

	if fm.EconomicImpact != "" {
		sb.WriteString(fmt.Sprintf("%s %s\n\n", fm.EconomicIcon, fm.EconomicImpact))
	}

	if fm.PrimaryLink != "" {
		sb.WriteString(fmt.Sprintf("🔗 %s\n", fm.PrimaryLink))
	}

	if fm.DisplaySource != "" {
		sb.WriteString(fmt.Sprintf("\n📌 %s | %s", fm.DisplaySource, fm.Timestamp))
	}

	return sb.String()
}

// GetRouter 获取 gin 路由器（用于与其他渠道共享端口）
func (w *WxOfficialChannel) GetRouter() *gin.Engine {
	return w.router
}
