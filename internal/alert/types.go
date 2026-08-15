package alert

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// AlertType 告警类型
type AlertType string

const (
	AlertTypePrice  AlertType = "price"  // 价格告警
	AlertTypeChange AlertType = "change" // 涨跌幅告警
	AlertTypeNews   AlertType = "news"   // 新闻关键词告警
)

// AlertCondition 告警条件
type AlertCondition string

const (
	ConditionAbove      AlertCondition = "above"       // 高于
	ConditionBelow      AlertCondition = "below"       // 低于
	ConditionCross      AlertCondition = "cross"       // 触及（穿越）
	ConditionChangeUp   AlertCondition = "change_up"   // 涨幅
	ConditionChangeDown AlertCondition = "change_down" // 跌幅
)

// Alert 告警规则
type Alert struct {
	ID            string         `json:"id"`
	UserID        string         `json:"user_id"`        // 用户ID（platform:chat_id）
	Type          AlertType      `json:"type"`           // 告警类型
	Symbol        string         `json:"symbol"`         // 标的代码（价格/涨跌告警用）
	Condition     AlertCondition `json:"condition"`      // 条件
	Value         float64        `json:"value"`          // 阈值
	Keywords      []string       `json:"keywords"`       // 关键词（新闻告警用）
	Message       string         `json:"message"`        // 自定义消息
	Enabled       bool           `json:"enabled"`        // 是否启用
	OneTime       bool           `json:"one_time"`       // 触发后自动删除
	Cooldown      time.Duration  `json:"cooldown"`       // 冷却时间（防止重复触发）
	LastTriggered time.Time      `json:"last_triggered"` // 上次触发时间
	CreatedAt     time.Time      `json:"created_at"`     // 创建时间
	ExpiresAt     time.Time      `json:"expires_at"`     // 过期时间（零值表示不过期）
}

// IsExpired 检查告警是否已过期
func (a *Alert) IsExpired() bool {
	if a.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(a.ExpiresAt)
}

// InCooldown 检查是否在冷却期
func (a *Alert) InCooldown() bool {
	if a.Cooldown == 0 {
		return false
	}
	return time.Since(a.LastTriggered) < a.Cooldown
}

// AlertEvent 告警触发事件
type AlertEvent struct {
	AlertID      string    `json:"alert_id"`
	UserID       string    `json:"user_id"`
	Type         AlertType `json:"type"`
	Symbol       string    `json:"symbol"`
	TriggeredAt  time.Time `json:"triggered_at"`
	CurrentValue float64   `json:"current_value"` // 当前值（价格/涨跌幅）
	TargetValue  float64   `json:"target_value"`  // 目标值
	Message      string    `json:"message"`       // 格式化的消息
}

// AlertNotifier 告警通知接口
type AlertNotifier interface {
	Notify(userID string, event *AlertEvent) error
}

// generateAlertID 生成告警ID
func generateAlertID() string {
	bytes := make([]byte, 8)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

// ConditionText 条件文本
func ConditionText(cond AlertCondition) string {
	switch cond {
	case ConditionAbove:
		return "高于"
	case ConditionBelow:
		return "低于"
	case ConditionCross:
		return "触及"
	case ConditionChangeUp:
		return "涨幅达"
	case ConditionChangeDown:
		return "跌幅达"
	default:
		return string(cond)
	}
}

// AlertTypeText 类型文本
func AlertTypeText(t AlertType) string {
	switch t {
	case AlertTypePrice:
		return "价格"
	case AlertTypeChange:
		return "涨跌幅"
	case AlertTypeNews:
		return "新闻"
	default:
		return string(t)
	}
}
