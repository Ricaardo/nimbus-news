package wxpersonal

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/eatmoreapple/openwechat"
	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

func init() {
	channel.Register("wxpersonal", NewWxPersonalChannel)
}

// 触发关键词 - 只有包含这些关键词的消息才会被处理
var triggerKeywords = []string{
	// 行情查询触发词
	"查", "价格", "行情", "多少钱", "报价", "走势",
	// 资产名称/代码
	"btc", "eth", "sol", "doge", "xrp", "ada", "bnb", "avax", "matic", "dot",
	"比特币", "以太坊", "狗狗币", "瑞波", "莱特币",
	"aapl", "tsla", "nvda", "googl", "msft", "meta", "amzn",
	"苹果", "特斯拉", "英伟达", "谷歌", "微软",
	"茅台", "腾讯", "阿里", "美团", "小米", "宁德",
	"黄金", "白银", "原油", "天然气",
	"标普", "纳指", "道指", "上证", "恒指", "创业板",
	// 功能触发词
	"分析", "rsi", "macd", "技术",
	"情绪", "恐惧", "贪婪", "fgi",
	"新闻", "资讯", "快讯",
	"帮助", "help",
	"指数", "大盘", "市场",
	// 测试
	"ping", "测试",
}

// 屏蔽词 - 包含这些词的消息不处理，也不回复
var blockKeywords = []string{
	// 政治敏感
	"习近平", "习主席", "总书记", "中共", "共产党", "国务院",
	"六四", "天安门", "64事件", "8964",
	"台独", "藏独", "疆独", "港独",
	"法轮功", "法轮大法",
	"民主运动", "民运", "颜色革命",
	"翻墙", "vpn", "科学上网",
	// 领导人姓名
	"胡锦涛", "江泽民", "温家宝", "李克强", "周永康", "薄熙来",
	// 敏感事件
	"新疆集中营", "再教育营",
	"香港国安法", "反送中", "雨伞运动",
	// 违法违规
	"赌博", "博彩", "六合彩", "彩票预测",
	"色情", "裸聊", "约炮",
	"毒品", "大麻", "冰毒",
	"枪支", "军火",
	// 诈骗相关
	"刷单", "兼职赚钱", "日赚", "躺赚",
	"内幕消息", "稳赚不赔", "保本保息",
	// 其他敏感
	"达赖喇嘛", "热比娅",
	"清零政策", "动态清零",
}

// 静默词 - 包含这些词的消息静默忽略（常见闲聊，不回复避免打扰）
var silentKeywords = []string{
	"早上好", "晚上好", "下午好", "你好", "hello", "hi",
	"谢谢", "感谢", "thanks", "thank you",
	"好的", "收到", "明白", "了解", "ok", "okay",
	"哈哈", "呵呵", "嘿嘿", "233", "666", "牛", "厉害",
	"再见", "拜拜", "bye", "晚安", "早安",
	"吃饭", "睡觉", "休息",
	"[图片]", "[表情]", "[语音]", "[视频]", "[文件]", "[链接]",
}

// WxPersonalChannel 个人微信渠道（基于 openwechat）
type WxPersonalChannel struct {
	channel.BaseChannel
	bot             *openwechat.Bot
	self            *openwechat.Self
	storagePath     string            // 登录凭证存储路径
	allowedUsers    []string          // 允许的用户列表（微信号或备注名），为空则允许所有
	allowedGroups   []string          // 允许的群聊列表（群名），为空则不接收群消息
	replyInterval   time.Duration     // 回复间隔（防止频繁回复）
	lastReplyTime   map[string]time.Time // 上次回复时间
	cancelFunc      context.CancelFunc
	wg              sync.WaitGroup
	mu              sync.RWMutex
}

// NewWxPersonalChannel 创建个人微信渠道
func NewWxPersonalChannel(cfg channel.Config) (channel.Channel, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = channel.ModeBidirectional
	}

	ch := &WxPersonalChannel{
		BaseChannel:   channel.NewBaseChannel(cfg.Name, "wxpersonal", mode),
		storagePath:   "wxpersonal_storage.json",
		replyInterval: 3 * time.Second, // 默认3秒间隔
		lastReplyTime: make(map[string]time.Time),
	}

	// 解析配置
	if cfg.Options != nil {
		if v, ok := cfg.Options["storage_path"].(string); ok && v != "" {
			ch.storagePath = v
		}
		if v, ok := cfg.Options["allowed_users"].([]interface{}); ok {
			for _, u := range v {
				if s, ok := u.(string); ok {
					ch.allowedUsers = append(ch.allowedUsers, s)
				}
			}
		}
		if v, ok := cfg.Options["allowed_groups"].([]interface{}); ok {
			for _, g := range v {
				if s, ok := g.(string); ok {
					ch.allowedGroups = append(ch.allowedGroups, s)
				}
			}
		}
		if v, ok := cfg.Options["reply_interval"].(int); ok && v > 0 {
			ch.replyInterval = time.Duration(v) * time.Second
		}
	}

	return ch, nil
}

// Start 启动渠道
func (w *WxPersonalChannel) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	w.cancelFunc = cancel

	// 创建 bot
	w.bot = openwechat.DefaultBot(openwechat.Desktop)

	// 注册消息处理器
	w.bot.MessageHandler = w.handleMessage

	// 尝试热登录（使用保存的凭证）
	storage := openwechat.NewFileHotReloadStorage(w.storagePath)

	// 登录回调：显示二维码
	w.bot.UUIDCallback = func(uuid string) {
		qrcodeURL := fmt.Sprintf("https://login.weixin.qq.com/qrcode/%s", uuid)
		slog.Debug("\n========================================")
		slog.Debug("📱 微信扫码登录")
		slog.Debug("========================================")
		slog.Debug("请用微信扫描以下链接中的二维码：")
		slog.Info("", "qrcodeurl", qrcodeURL)
		slog.Debug("或者访问上述链接查看二维码")
		slog.Debug("========================================\n")
		openwechat.PrintlnQrcodeUrl(qrcodeURL)
	}

	// 登录扫码回调
	w.bot.ScanCallBack = func(body openwechat.CheckLoginResponse) {
		slog.Debug("✅ 扫码成功，请在手机上确认登录...")
	}

	// 登录成功回调
	w.bot.LoginCallBack = func(body openwechat.CheckLoginResponse) {
		slog.Debug("✅ 登录成功！")
	}

	// 启动登录
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()

		if err := w.bot.HotLogin(storage, openwechat.NewRetryLoginOption()); err != nil {
			slog.Info("热登录失败，尝试扫码登录", "err", err)
			if err := w.bot.Login(); err != nil {
				slog.Info("微信登录失败", "err", err)
				return
			}
		}

		self, err := w.bot.GetCurrentUser()
		if err != nil {
			slog.Info("获取当前用户失败", "err", err)
			return
		}
		w.mu.Lock()
		w.self = self
		w.mu.Unlock()

		slog.Info("✅ 微信渠道", "w_name", w.Name(), "nick_name", self.NickName)
		w.bot.Block()
	}()

	slog.Info("个人微信渠道", "w_name", w.Name())
	return nil
}

// Stop 停止渠道
func (w *WxPersonalChannel) Stop() error {
	if w.cancelFunc != nil {
		w.cancelFunc()
	}
	if w.bot != nil {
		w.bot.Logout()
	}
	w.wg.Wait()
	return nil
}

// shouldProcess 判断消息是否应该处理
func (w *WxPersonalChannel) shouldProcess(content string) (bool, string) {
	lower := strings.ToLower(content)

	// 1. 检查屏蔽词 - 包含则完全忽略
	for _, keyword := range blockKeywords {
		if strings.Contains(lower, strings.ToLower(keyword)) {
			return false, "blocked"
		}
	}

	// 2. 检查静默词 - 常见闲聊，静默忽略
	for _, keyword := range silentKeywords {
		if strings.Contains(lower, strings.ToLower(keyword)) {
			return false, "silent"
		}
	}

	// 3. 检查触发关键词 - 只有包含触发词才处理
	for _, keyword := range triggerKeywords {
		if strings.Contains(lower, strings.ToLower(keyword)) {
			return true, "trigger"
		}
	}

	// 4. 检查是否像标的代码（纯字母数字，2-10字符）
	cleanText := strings.TrimSpace(content)
	if len(cleanText) >= 2 && len(cleanText) <= 10 {
		if matched, _ := regexp.MatchString(`^[A-Za-z0-9\.\-\^]+$`, cleanText); matched {
			return true, "symbol"
		}
		// 纯中文2-4字可能是股票名
		if matched, _ := regexp.MatchString(`^[\p{Han}]{2,4}$`, cleanText); matched {
			return true, "symbol"
		}
	}

	// 5. 默认不处理
	return false, "unknown"
}

// checkRateLimit 检查回复频率限制
func (w *WxPersonalChannel) checkRateLimit(chatID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	lastTime, exists := w.lastReplyTime[chatID]
	if exists && time.Since(lastTime) < w.replyInterval {
		return false // 太频繁，不回复
	}
	w.lastReplyTime[chatID] = time.Now()
	return true
}

// handleMessage 处理收到的消息
func (w *WxPersonalChannel) handleMessage(msg *openwechat.Message) {
	handler := w.GetHandler()
	if handler == nil {
		return
	}

	// 只处理文本消息
	if !msg.IsText() {
		return
	}

	// 忽略自己发的消息
	if msg.IsSendBySelf() {
		return
	}

	// 获取发送者
	sender, err := msg.Sender()
	if err != nil {
		return
	}

	var chatID, userID, userName string
	isGroup := msg.IsSendByGroup()

	if isGroup {
		// 群消息
		group := sender
		chatID = group.UserName
		groupName := group.NickName

		// 检查是否在允许的群列表中
		if len(w.allowedGroups) > 0 {
			allowed := false
			for _, g := range w.allowedGroups {
				if g == groupName {
					allowed = true
					break
				}
			}
			if !allowed {
				return
			}
		} else {
			// 如果没配置允许的群，默认不处理群消息
			return
		}

		senderInGroup, err := msg.SenderInGroup()
		if err != nil {
			return
		}
		userID = senderInGroup.UserName
		userName = senderInGroup.NickName
	} else {
		// 私聊消息
		chatID = sender.UserName
		userID = sender.UserName
		userName = sender.NickName

		// 检查是否在允许的用户列表中
		if len(w.allowedUsers) > 0 {
			allowed := false
			for _, u := range w.allowedUsers {
				if u == sender.NickName || u == sender.Alias || u == sender.UserName {
					allowed = true
					break
				}
			}
			if !allowed {
				return
			}
		}
	}

	// 构建消息内容
	content := msg.Content

	// 群消息中需要 @机器人 才处理
	if isGroup {
		w.mu.RLock()
		self := w.self
		w.mu.RUnlock()
		if self != nil {
			atPrefix := "@" + self.NickName
			if strings.HasPrefix(content, atPrefix) {
				content = strings.TrimPrefix(content, atPrefix)
				content = strings.TrimSpace(content)
			} else {
				// 群消息如果没有 @机器人，不处理
				return
			}
		}
	}

	// 检查是否应该处理这条消息
	shouldHandle, reason := w.shouldProcess(content)
	if !shouldHandle {
		// 静默忽略，不回复任何内容
		slog.Info("[wxpersonal] ignored message", "reason", reason, "content", truncate(content, 50))
		return
	}

	// 检查回复频率
	if !w.checkRateLimit(chatID) {
		slog.Info("[wxpersonal] rate limited", "content", truncate(content, 50))
		return
	}

	slog.Info("[wxpersonal] processing message", "reason", reason, "content", truncate(content, 50))

	// 创建消息对象
	inMsg := model.NewChatMessage(
		w.Name(),
		chatID,
		userID,
		content,
	)
	inMsg.ID = msg.MsgId
	inMsg.SetMetadata("user_name", userName)
	inMsg.SetMetadata("is_group", isGroup)
	inMsg.SetMetadata("original_msg", msg)

	// 调用处理器
	handler(context.Background(), inMsg)
}

// Send 发送消息（用于主动推送，个人微信场景较少使用）
func (w *WxPersonalChannel) Send(ctx context.Context, msg *model.Message) error {
	return fmt.Errorf("wxpersonal channel does not support broadcast send, use Reply instead")
}

// SendBatch 批量发送
func (w *WxPersonalChannel) SendBatch(ctx context.Context, msgs []*model.Message) error {
	return fmt.Errorf("wxpersonal channel does not support batch send")
}

// Reply 回复消息
func (w *WxPersonalChannel) Reply(ctx context.Context, originalMsgID string, reply *model.Message) error {
	originalMsg, ok := reply.Metadata["original_msg"].(*openwechat.Message)
	if !ok {
		return fmt.Errorf("original message not found in metadata")
	}

	content := w.formatPlainText(reply.Content)

	// 限制回复长度，避免过长消息
	if len(content) > 2000 {
		content = content[:2000] + "\n...(内容过长已截断)"
	}

	_, err := originalMsg.ReplyText(content)
	if err != nil {
		return fmt.Errorf("reply failed: %w", err)
	}

	return nil
}

// formatPlainText 将内容转为微信纯文本格式
func (w *WxPersonalChannel) formatPlainText(content string) string {
	// 使用统一工具清理和移除 Markdown
	content = channel.SanitizeText(content)
	content = channel.StripMarkdown(content)
	return strings.TrimSpace(content)
}

// SendToUser 发送消息给指定用户
func (w *WxPersonalChannel) SendToUser(ctx context.Context, nickName string, content string) error {
	w.mu.RLock()
	self := w.self
	w.mu.RUnlock()

	if self == nil {
		return fmt.Errorf("not logged in")
	}

	friends, err := self.Friends()
	if err != nil {
		return fmt.Errorf("get friends failed: %w", err)
	}

	friend := friends.SearchByNickName(1, nickName)
	if friend == nil || len(friend) == 0 {
		return fmt.Errorf("user not found: %s", nickName)
	}

	_, err = self.SendTextToFriend(friend[0], content)
	return err
}

// SendToGroup 发送消息到指定群
func (w *WxPersonalChannel) SendToGroup(ctx context.Context, groupName string, content string) error {
	w.mu.RLock()
	self := w.self
	w.mu.RUnlock()

	if self == nil {
		return fmt.Errorf("not logged in")
	}

	groups, err := self.Groups()
	if err != nil {
		return fmt.Errorf("get groups failed: %w", err)
	}

	group := groups.SearchByNickName(1, groupName)
	if group == nil || len(group) == 0 {
		return fmt.Errorf("group not found: %s", groupName)
	}

	_, err = self.SendTextToGroup(group[0], content)
	return err
}

// IsLoggedIn 检查是否已登录
func (w *WxPersonalChannel) IsLoggedIn() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.self != nil
}

// GetLoginQRCode 获取登录二维码（用于 Web 显示）
func (w *WxPersonalChannel) GetLoginQRCode() (string, error) {
	if w.bot == nil {
		return "", fmt.Errorf("bot not initialized")
	}
	return "", fmt.Errorf("use terminal to scan QR code")
}

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// 确保 storage 文件目录存在
func ensureDir(path string) error {
	dir := path[:strings.LastIndex(path, "/")]
	if dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0755)
}
