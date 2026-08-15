package source

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
)

// ChannelDispatcher 渠道分发接口
type ChannelDispatcher interface {
	DispatchNews(ctx context.Context, msg *model.Message, channels []string) error
}

// DefaultHealthNotifier 默认健康通知器
type DefaultHealthNotifier struct {
	dispatcher ChannelDispatcher
	channels   []string
}

// NewDefaultHealthNotifier 创建默认健康通知器
func NewDefaultHealthNotifier(dispatcher ChannelDispatcher, channels []string) *DefaultHealthNotifier {
	return &DefaultHealthNotifier{
		dispatcher: dispatcher,
		channels:   channels,
	}
}

// NotifyDegraded 通知降级
func (n *DefaultHealthNotifier) NotifyDegraded(source string, health *SourceHealth) {
	if n.dispatcher == nil || len(n.channels) == 0 {
		slog.Error("health alert dropped: no dispatcher/channels", "source", source)
		return
	}

	msg := &model.Message{
		Type:       model.TypeNews,
		Title:      fmt.Sprintf("⚠️ 数据源降级: %s", source),
		Content:    n.formatHealthMessage(health, "degraded"),
		Source:     "system",
		SourceType: "health-monitor",
		CreateTime: time.Now(),
		FetchTime:  time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := n.dispatcher.DispatchNews(ctx, msg, n.channels); err != nil {
		slog.Error("health alert dispatch failed", "source", source, "err", err)
	}
}

// NotifyUnhealthy 通知不健康
func (n *DefaultHealthNotifier) NotifyUnhealthy(source string, health *SourceHealth) {
	if n.dispatcher == nil || len(n.channels) == 0 {
		slog.Error("health alert dropped: no dispatcher/channels", "source", source)
		return
	}

	msg := &model.Message{
		Type:       model.TypeNews,
		Title:      fmt.Sprintf("🔴 数据源故障: %s", source),
		Content:    n.formatHealthMessage(health, "unhealthy"),
		Source:     "system",
		SourceType: "health-monitor",
		CreateTime: time.Now(),
		FetchTime:  time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := n.dispatcher.DispatchNews(ctx, msg, n.channels); err != nil {
		slog.Error("health alert dispatch failed", "source", source, "err", err)
	}
}

// NotifyRecovered 通知恢复
func (n *DefaultHealthNotifier) NotifyRecovered(source string, health *SourceHealth) {
	if n.dispatcher == nil || len(n.channels) == 0 {
		slog.Error("health alert dropped: no dispatcher/channels", "source", source)
		return
	}

	msg := &model.Message{
		Type:       model.TypeNews,
		Title:      fmt.Sprintf("✅ 数据源恢复: %s", source),
		Content:    n.formatHealthMessage(health, "recovered"),
		Source:     "system",
		SourceType: "health-monitor",
		CreateTime: time.Now(),
		FetchTime:  time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := n.dispatcher.DispatchNews(ctx, msg, n.channels); err != nil {
		slog.Error("health alert dispatch failed", "source", source, "err", err)
	}
}

func (n *DefaultHealthNotifier) formatHealthMessage(health *SourceHealth, eventType string) string {
	switch eventType {
	case "degraded":
		return fmt.Sprintf(`数据源 %s 出现异常

状态: 降级
连续失败: %d 次
成功率: %.1f%%
最后错误: %s
检查时间: %s`,
			health.Name,
			health.ConsecutiveFails,
			health.SuccessRate*100,
			health.LastError,
			health.LastCheckTime.Format("15:04:05"))

	case "unhealthy":
		return fmt.Sprintf(`数据源 %s 已停止工作

状态: 不健康
连续失败: %d 次
成功率: %.1f%%
已持续: %s，累计失败: %d 次
最后错误: %s
检查时间: %s

系统将每 5 分钟自动探活一次`,
			health.Name,
			health.ConsecutiveFails,
			health.SuccessRate*100,
			formatDurationCN(time.Since(health.UnhealthySince)),
			health.TotalFailures,
			health.LastError,
			health.LastCheckTime.Format("15:04:05"))

	case "recovered":
		return fmt.Sprintf(`数据源 %s 已恢复正常

状态: 健康
成功率: %.1f%%
平均延迟: %d ms
恢复时间: %s`,
			health.Name,
			health.SuccessRate*100,
			health.AvgLatencyMs,
			health.LastSuccess.Format("15:04:05"))

	default:
		return fmt.Sprintf("数据源 %s 状态变更", health.Name)
	}
}

// formatDurationCN 将时长格式化为中文可读形式
func formatDurationCN(d time.Duration) string {
	if d < time.Minute {
		return "不到1分钟"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d天%d小时", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d小时%d分钟", hours, minutes)
	default:
		return fmt.Sprintf("%d分钟", minutes)
	}
}

// NoopHealthNotifier 空通知器（用于测试或禁用通知）
type NoopHealthNotifier struct{}

func (n *NoopHealthNotifier) NotifyDegraded(source string, health *SourceHealth)  {}
func (n *NoopHealthNotifier) NotifyUnhealthy(source string, health *SourceHealth) {}
func (n *NoopHealthNotifier) NotifyRecovered(source string, health *SourceHealth) {}
