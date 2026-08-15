package alert

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/metrics"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// MarketDataProvider 市场数据提供者接口
type MarketDataProvider interface {
	GetPrice(ctx context.Context, symbol string) (float64, error)
}

// NotificationSender 通知发送接口
type NotificationSender interface {
	SendAlert(ctx context.Context, userID string, event *AlertEvent) error
}

// EngineConfig 告警引擎配置
type EngineConfig struct {
	// CheckInterval 价格检查间隔
	CheckInterval time.Duration `yaml:"check_interval"`
	// MaxAlertsPerUser 每用户最大告警数
	MaxAlertsPerUser int `yaml:"max_alerts_per_user"`
	// DefaultCooldown 默认冷却时间
	DefaultCooldown time.Duration `yaml:"default_cooldown"`
	// Enabled 是否启用
	Enabled bool `yaml:"enabled"`
}

// DefaultEngineConfig 默认配置
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		CheckInterval:    30 * time.Second,
		MaxAlertsPerUser: 50,
		DefaultCooldown:  5 * time.Minute,
		Enabled:          true,
	}
}

// Engine 告警引擎
type Engine struct {
	store          *Store
	marketProvider MarketDataProvider
	notifier       NotificationSender
	config         EngineConfig

	// 运行时状态
	alerts      map[string]*Alert // id -> alert
	userAlerts  map[string][]string // userID -> []alertID
	lastTrigger map[string]time.Time // alertID -> lastTriggerTime

	mu         sync.RWMutex
	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
}

// NewEngine 创建告警引擎
func NewEngine(store *Store, config EngineConfig) *Engine {
	return &Engine{
		store:       store,
		config:      config,
		alerts:      make(map[string]*Alert),
		userAlerts:  make(map[string][]string),
		lastTrigger: make(map[string]time.Time),
	}
}

// SetMarketProvider 设置市场数据提供者
func (e *Engine) SetMarketProvider(provider MarketDataProvider) {
	e.marketProvider = provider
}

// SetNotifier 设置通知发送者
func (e *Engine) SetNotifier(notifier NotificationSender) {
	e.notifier = notifier
}

// Start 启动告警引擎
func (e *Engine) Start(ctx context.Context) error {
	if !e.config.Enabled {
		fmt.Println("Alert engine is disabled")
		return nil
	}

	e.ctx, e.cancelFunc = context.WithCancel(ctx)

	// 从存储加载告警
	if err := e.loadAlerts(); err != nil {
		return fmt.Errorf("load alerts failed: %w", err)
	}

	// 启动价格监控协程
	e.wg.Add(1)
	go e.priceCheckLoop()

	fmt.Printf("Alert engine started with %d alerts\n", len(e.alerts))
	return nil
}

// Stop 停止告警引擎
func (e *Engine) Stop() {
	if e.cancelFunc != nil {
		e.cancelFunc()
	}
	e.wg.Wait()
	fmt.Println("Alert engine stopped")
}

// loadAlerts 从存储加载告警
func (e *Engine) loadAlerts() error {
	alerts, err := e.store.LoadAll(e.ctx)
	if err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	for _, alert := range alerts {
		// 检查是否过期
		if !alert.ExpiresAt.IsZero() && time.Now().After(alert.ExpiresAt) {
			// 删除过期告警
			e.store.Delete(e.ctx, alert.ID)
			continue
		}

		e.alerts[alert.ID] = alert
		e.userAlerts[alert.UserID] = append(e.userAlerts[alert.UserID], alert.ID)
	}

	// 更新指标
	metrics.ActiveAlertsGauge.Set(float64(len(e.alerts)))

	return nil
}

// priceCheckLoop 价格检查循环
func (e *Engine) priceCheckLoop() {
	defer e.wg.Done()

	ticker := time.NewTicker(e.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.checkPriceAlerts()
		}
	}
}

// checkPriceAlerts 检查价格告警
func (e *Engine) checkPriceAlerts() {
	if e.marketProvider == nil {
		return
	}

	e.mu.RLock()
	alerts := make([]*Alert, 0)
	for _, alert := range e.alerts {
		if alert.Type == AlertTypePrice || alert.Type == AlertTypeChange {
			alerts = append(alerts, alert)
		}
	}
	e.mu.RUnlock()

	// 按 symbol 分组以减少 API 调用
	symbolAlerts := make(map[string][]*Alert)
	for _, alert := range alerts {
		symbolAlerts[alert.Symbol] = append(symbolAlerts[alert.Symbol], alert)
	}

	for symbol, alertList := range symbolAlerts {
		price, err := e.marketProvider.GetPrice(e.ctx, symbol)
		if err != nil {
			continue
		}

		for _, alert := range alertList {
			if e.shouldTrigger(alert, price) {
				e.triggerAlert(alert, price)
			}
		}
	}
}

// shouldTrigger 判断是否应该触发告警
func (e *Engine) shouldTrigger(alert *Alert, currentPrice float64) bool {
	// 检查冷却时间
	e.mu.RLock()
	lastTrigger, exists := e.lastTrigger[alert.ID]
	e.mu.RUnlock()

	cooldown := alert.Cooldown
	if cooldown == 0 {
		cooldown = e.config.DefaultCooldown
	}

	if exists && time.Since(lastTrigger) < cooldown {
		return false
	}

	// 检查条件
	switch alert.Condition {
	case ConditionAbove:
		return currentPrice > alert.Value
	case ConditionBelow:
		return currentPrice < alert.Value
	case ConditionCross:
		// 需要历史价格判断穿越，这里简化为等于
		return currentPrice >= alert.Value*0.99 && currentPrice <= alert.Value*1.01
	case ConditionChangeUp:
		// 需要基准价格，暂时跳过
		return false
	case ConditionChangeDown:
		// 需要基准价格，暂时跳过
		return false
	}

	return false
}

// triggerAlert 触发告警
func (e *Engine) triggerAlert(alert *Alert, currentPrice float64) {
	event := &AlertEvent{
		AlertID:      alert.ID,
		UserID:       alert.UserID,
		Type:         alert.Type,
		Symbol:       alert.Symbol,
		TriggeredAt:  time.Now(),
		CurrentValue: currentPrice,
		TargetValue:  alert.Value,
		Message:      e.formatAlertMessage(alert, currentPrice),
	}

	// 发送通知
	if e.notifier != nil {
		if err := e.notifier.SendAlert(e.ctx, alert.UserID, event); err != nil {
			fmt.Printf("Send alert notification failed: %v\n", err)
		}
	}

	// 记录触发时间
	e.mu.Lock()
	e.lastTrigger[alert.ID] = time.Now()
	e.mu.Unlock()

	// 记录指标
	metrics.AlertTriggeredTotal.WithLabelValues(string(alert.Type)).Inc()

	// 一次性告警，删除
	if alert.OneTime {
		e.RemoveAlert(e.ctx, alert.ID)
	}

	fmt.Printf("Alert triggered: %s for %s at %.2f\n", alert.ID, alert.Symbol, currentPrice)
}

// formatAlertMessage 格式化告警消息
func (e *Engine) formatAlertMessage(alert *Alert, currentPrice float64) string {
	var conditionStr string
	switch alert.Condition {
	case ConditionAbove:
		conditionStr = "突破"
	case ConditionBelow:
		conditionStr = "跌破"
	case ConditionCross:
		conditionStr = "触及"
	case ConditionChangeUp:
		conditionStr = "涨幅达到"
	case ConditionChangeDown:
		conditionStr = "跌幅达到"
	}

	return fmt.Sprintf("🔔 %s %s %.2f\n当前价格: %.2f\n目标价格: %.2f",
		alert.Symbol, conditionStr, alert.Value, currentPrice, alert.Value)
}

// === 告警管理 API ===

// CreateAlert 创建告警
func (e *Engine) CreateAlert(ctx context.Context, alert *Alert) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// 检查用户告警数量限制
	if len(e.userAlerts[alert.UserID]) >= e.config.MaxAlertsPerUser {
		return fmt.Errorf("maximum alerts per user reached (%d)", e.config.MaxAlertsPerUser)
	}

	// 生成 ID
	if alert.ID == "" {
		alert.ID = fmt.Sprintf("alert_%d", time.Now().UnixNano())
	}
	alert.CreatedAt = time.Now()

	// 设置默认冷却时间
	if alert.Cooldown == 0 {
		alert.Cooldown = e.config.DefaultCooldown
	}

	// 保存到存储
	if err := e.store.Save(ctx, alert); err != nil {
		return err
	}

	// 添加到内存
	e.alerts[alert.ID] = alert
	e.userAlerts[alert.UserID] = append(e.userAlerts[alert.UserID], alert.ID)

	// 更新指标
	metrics.ActiveAlertsGauge.Set(float64(len(e.alerts)))

	return nil
}

// RemoveAlert 删除告警
func (e *Engine) RemoveAlert(ctx context.Context, alertID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	alert, exists := e.alerts[alertID]
	if !exists {
		return fmt.Errorf("alert not found: %s", alertID)
	}

	// 从存储删除
	if err := e.store.Delete(ctx, alertID); err != nil {
		return err
	}

	// 从内存删除
	delete(e.alerts, alertID)
	delete(e.lastTrigger, alertID)

	// 从用户列表删除
	userAlerts := e.userAlerts[alert.UserID]
	for i, id := range userAlerts {
		if id == alertID {
			e.userAlerts[alert.UserID] = append(userAlerts[:i], userAlerts[i+1:]...)
			break
		}
	}

	// 更新指标
	metrics.ActiveAlertsGauge.Set(float64(len(e.alerts)))

	return nil
}

// GetUserAlerts 获取用户所有告警
func (e *Engine) GetUserAlerts(ctx context.Context, userID string) []*Alert {
	e.mu.RLock()
	defer e.mu.RUnlock()

	alertIDs := e.userAlerts[userID]
	alerts := make([]*Alert, 0, len(alertIDs))

	for _, id := range alertIDs {
		if alert, exists := e.alerts[id]; exists {
			alerts = append(alerts, alert)
		}
	}

	return alerts
}

// GetAlert 获取单个告警
func (e *Engine) GetAlert(ctx context.Context, alertID string) (*Alert, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	alert, exists := e.alerts[alertID]
	if !exists {
		return nil, fmt.Errorf("alert not found: %s", alertID)
	}

	return alert, nil
}

// GetAlertCount 获取告警总数
func (e *Engine) GetAlertCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.alerts)
}

// === 新闻告警处理 ===

// CheckNewsAlert 检查新闻告警
// 当有新新闻时调用此方法
func (e *Engine) CheckNewsAlert(ctx context.Context, news *model.News) {
	e.mu.RLock()
	var newsAlerts []*Alert
	for _, alert := range e.alerts {
		if alert.Type == AlertTypeNews {
			newsAlerts = append(newsAlerts, alert)
		}
	}
	e.mu.RUnlock()

	for _, alert := range newsAlerts {
		if e.matchNewsAlert(alert, news) {
			e.triggerNewsAlert(ctx, alert, news)
		}
	}
}

// matchNewsAlert 匹配新闻告警
func (e *Engine) matchNewsAlert(alert *Alert, news *model.News) bool {
	// 检查冷却时间
	e.mu.RLock()
	lastTrigger, exists := e.lastTrigger[alert.ID]
	e.mu.RUnlock()

	cooldown := alert.Cooldown
	if cooldown == 0 {
		cooldown = e.config.DefaultCooldown
	}

	if exists && time.Since(lastTrigger) < cooldown {
		return false
	}

	// 检查关键词
	content := strings.ToLower(news.Title + " " + news.Content)
	for _, keyword := range alert.Keywords {
		if strings.Contains(content, strings.ToLower(keyword)) {
			return true
		}
	}

	// 检查 symbol
	if alert.Symbol != "" {
		if strings.Contains(strings.ToLower(news.Title), strings.ToLower(alert.Symbol)) {
			return true
		}
	}

	return false
}

// triggerNewsAlert 触发新闻告警
func (e *Engine) triggerNewsAlert(ctx context.Context, alert *Alert, news *model.News) {
	event := &AlertEvent{
		AlertID:     alert.ID,
		UserID:      alert.UserID,
		Type:        AlertTypeNews,
		Symbol:      alert.Symbol,
		TriggeredAt: time.Now(),
		Message:     e.formatNewsAlertMessage(alert, news),
	}

	// 发送通知
	if e.notifier != nil {
		if err := e.notifier.SendAlert(ctx, alert.UserID, event); err != nil {
			fmt.Printf("Send news alert notification failed: %v\n", err)
		}
	}

	// 记录触发时间
	e.mu.Lock()
	e.lastTrigger[alert.ID] = time.Now()
	e.mu.Unlock()

	// 记录指标
	metrics.AlertTriggeredTotal.WithLabelValues(string(AlertTypeNews)).Inc()

	// 一次性告警，删除
	if alert.OneTime {
		e.RemoveAlert(ctx, alert.ID)
	}

	fmt.Printf("News alert triggered: %s for news: %s\n", alert.ID, news.Title)
}

// formatNewsAlertMessage 格式化新闻告警消息
func (e *Engine) formatNewsAlertMessage(alert *Alert, news *model.News) string {
	keywordStr := ""
	if len(alert.Keywords) > 0 {
		keywordStr = fmt.Sprintf("\n关键词: %s", strings.Join(alert.Keywords, ", "))
	}

	return fmt.Sprintf("📰 新闻告警%s\n\n%s\n\n%s\n\n🔗 %s",
		keywordStr, news.Title, truncateString(news.Content, 200), news.Link)
}

// truncateString 截断字符串
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
