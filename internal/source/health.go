package source

import (
	"time"
)

// HealthStatus 健康状态枚举
type HealthStatus string

const (
	StatusHealthy          HealthStatus = "healthy"           // 正常
	StatusDegraded         HealthStatus = "degraded"          // 降级（偶发失败）
	StatusUnhealthy        HealthStatus = "unhealthy"         // 不健康（连续失败）
	StatusDisabled         HealthStatus = "disabled"          // 已禁用
	StatusAwaitingSchedule HealthStatus = "awaiting_schedule" // 等待首次定时调度
)

// SourceHealth 数据源健康状态
type SourceHealth struct {
	Name                 string       `json:"name"`
	Status               HealthStatus `json:"status"`
	LastSuccess          time.Time    `json:"last_success,omitempty"`
	LastFailure          time.Time    `json:"last_failure,omitempty"`
	LastError            string       `json:"last_error,omitempty"`
	ConsecutiveFails     int          `json:"consecutive_fails"`
	TotalRequests        int64        `json:"total_requests"`
	TotalFailures        int64        `json:"total_failures"`
	SuccessRate          float64      `json:"success_rate"`
	AvgLatencyMs         int64        `json:"avg_latency_ms"`
	LastCheckTime        time.Time    `json:"last_check_time,omitempty"`
	LastFetchCount       int          `json:"last_fetch_count"`           // 上次抓取的消息数量
	UnhealthySince       time.Time    `json:"unhealthy_since,omitempty"`  // 进入不健康状态的时间
	LastNotifiedAt       time.Time    `json:"last_notified_at,omitempty"` // 最后一次发送告警的时间
	scheduled            bool
	awaitingFirstSuccess bool
}

// HealthConfig 健康检查配置
type HealthConfig struct {
	Enabled            bool          `yaml:"enabled"`
	DegradedThreshold  int           `yaml:"degraded_threshold"`  // 降级阈值（连续失败次数）
	UnhealthyThreshold int           `yaml:"unhealthy_threshold"` // 不健康阈值
	RecoveryInterval   time.Duration `yaml:"recovery_interval"`   // 恢复探测间隔
	RenotifyInterval   time.Duration `yaml:"renotify_interval"`   // 持续不健康时的重新告警间隔
	NotifyChannels     []string      `yaml:"notify_channels"`     // 告警通知渠道
}

// DefaultHealthConfig 默认健康配置
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		Enabled:            true,
		DegradedThreshold:  3,
		UnhealthyThreshold: 5,
		RecoveryInterval:   5 * time.Minute,
		RenotifyInterval:   6 * time.Hour,
	}
}

// HealthNotifier 健康告警通知接口
type HealthNotifier interface {
	NotifyDegraded(source string, health *SourceHealth)
	NotifyUnhealthy(source string, health *SourceHealth)
	NotifyRecovered(source string, health *SourceHealth)
}

// HealthSummary 健康状态汇总
type HealthSummary struct {
	Total            int             `json:"total"`
	Healthy          int             `json:"healthy"`
	Degraded         int             `json:"degraded"`
	Unhealthy        int             `json:"unhealthy"`
	Disabled         int             `json:"disabled"`
	AwaitingSchedule int             `json:"awaiting_schedule"`
	AvgSuccess       float64         `json:"avg_success_rate"`
	Sources          []*SourceHealth `json:"sources,omitempty"`
}

// CalculateSummary 计算健康汇总
func CalculateSummary(sources map[string]*SourceHealth, includeDetails bool) *HealthSummary {
	summary := &HealthSummary{}
	totalSuccessRate := 0.0

	for _, h := range sources {
		summary.Total++
		switch h.Status {
		case StatusHealthy:
			summary.Healthy++
		case StatusDegraded:
			summary.Degraded++
		case StatusUnhealthy:
			summary.Unhealthy++
		case StatusDisabled:
			summary.Disabled++
		case StatusAwaitingSchedule:
			summary.AwaitingSchedule++
			if includeDetails {
				copyHealth := *h
				summary.Sources = append(summary.Sources, &copyHealth)
			}
			continue
		}
		totalSuccessRate += h.SuccessRate

		if includeDetails {
			copyHealth := *h
			summary.Sources = append(summary.Sources, &copyHealth)
		}
	}

	measuredSources := summary.Total - summary.AwaitingSchedule
	if measuredSources > 0 {
		summary.AvgSuccess = totalSuccessRate / float64(measuredSources)
	}

	return summary
}
