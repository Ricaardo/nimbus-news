package source

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// HealthMonitor 健康监控器
type HealthMonitor struct {
	mu       sync.RWMutex
	sources  map[string]*SourceHealth
	config   HealthConfig
	notifier HealthNotifier
}

// NewHealthMonitor 创建健康监控器
func NewHealthMonitor(cfg HealthConfig) *HealthMonitor {
	if cfg.DegradedThreshold == 0 {
		cfg.DegradedThreshold = 3
	}
	if cfg.UnhealthyThreshold == 0 {
		cfg.UnhealthyThreshold = 5
	}
	if cfg.RecoveryInterval == 0 {
		cfg.RecoveryInterval = 5 * time.Minute
	}
	if cfg.RenotifyInterval == 0 {
		cfg.RenotifyInterval = 6 * time.Hour
	}

	return &HealthMonitor{
		sources: make(map[string]*SourceHealth),
		config:  cfg,
	}
}

// SetNotifier 设置通知器
func (m *HealthMonitor) SetNotifier(notifier HealthNotifier) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notifier = notifier
}

// RegisterSource 注册数据源
func (m *HealthMonitor) RegisterSource(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if h, exists := m.sources[name]; exists {
		if h.scheduled {
			h.scheduled = false
			h.awaitingFirstSuccess = false
			if h.Status == StatusAwaitingSchedule {
				h.Status = StatusHealthy
			}
		}
		return
	}

	m.sources[name] = &SourceHealth{
		Name:        name,
		Status:      StatusHealthy,
		SuccessRate: 1.0, // 初始成功率100%
	}
}

// RegisterScheduledSource 注册尚未执行首次抓取的定时数据源。
func (m *HealthMonitor) RegisterScheduledSource(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if h, exists := m.sources[name]; exists {
		if h.scheduled {
			return
		}
		h.Status = StatusAwaitingSchedule
		h.scheduled = true
		h.awaitingFirstSuccess = true
		return
	}

	m.sources[name] = &SourceHealth{
		Name:                 name,
		Status:               StatusAwaitingSchedule,
		SuccessRate:          1.0,
		scheduled:            true,
		awaitingFirstSuccess: true,
	}
}

// RecordSuccess 记录成功
func (m *HealthMonitor) RecordSuccess(name string, latency time.Duration, fetchCount int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	h, ok := m.sources[name]
	if !ok {
		return
	}

	wasUnhealthy := h.Status == StatusUnhealthy || h.Status == StatusDegraded
	prevStatus := h.Status

	h.LastSuccess = time.Now()
	h.ConsecutiveFails = 0
	h.TotalRequests++
	h.LastCheckTime = time.Now()
	h.LastFetchCount = fetchCount
	h.awaitingFirstSuccess = false

	// 更新平均延迟（滑动平均）
	latencyMs := latency.Milliseconds()
	if h.AvgLatencyMs == 0 {
		h.AvgLatencyMs = latencyMs
	} else {
		h.AvgLatencyMs = (h.AvgLatencyMs*9 + latencyMs) / 10
	}

	// 更新成功率
	h.SuccessRate = float64(h.TotalRequests-h.TotalFailures) / float64(h.TotalRequests)

	// 状态恢复
	h.Status = StatusHealthy
	h.UnhealthySince = time.Time{}
	h.LastNotifiedAt = time.Time{}

	// 发送恢复通知
	if wasUnhealthy && m.notifier != nil {
		go m.notifier.NotifyRecovered(name, m.copyHealth(h))
		slog.Info("[HealthMonitor] Source", "name", name, "prevstatus", prevStatus)
	}
}

// RecordFailure 记录失败
func (m *HealthMonitor) RecordFailure(name string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	h, ok := m.sources[name]
	if !ok {
		return
	}

	h.LastFailure = time.Now()
	h.LastError = err.Error()
	h.ConsecutiveFails++
	h.TotalRequests++
	h.TotalFailures++
	h.LastCheckTime = time.Now()

	// 更新成功率
	h.SuccessRate = float64(h.TotalRequests-h.TotalFailures) / float64(h.TotalRequests)

	// 状态转换
	prevStatus := h.Status

	if h.ConsecutiveFails >= m.config.UnhealthyThreshold {
		h.Status = StatusUnhealthy
		if prevStatus != StatusUnhealthy {
			h.UnhealthySince = time.Now()
			if m.notifier != nil {
				h.LastNotifiedAt = time.Now()
				go m.notifier.NotifyUnhealthy(name, m.copyHealth(h))
				slog.Warn("[HealthMonitor] Source", "name", name, "h_consecutivefails", h.ConsecutiveFails)
			}
		} else if m.config.RenotifyInterval > 0 && time.Since(h.LastNotifiedAt) >= m.config.RenotifyInterval {
			// 持续不健康，超过重新告警间隔则再次告警
			if m.notifier != nil {
				h.LastNotifiedAt = time.Now()
				go m.notifier.NotifyUnhealthy(name, m.copyHealth(h))
				slog.Warn("[HealthMonitor] Source still unhealthy", "name", name, "unhealthy_since", h.UnhealthySince, "h_consecutivefails", h.ConsecutiveFails)
			}
		}
	} else if h.ConsecutiveFails >= m.config.DegradedThreshold {
		h.Status = StatusDegraded
		if (prevStatus == StatusHealthy || prevStatus == StatusAwaitingSchedule) && m.notifier != nil {
			go m.notifier.NotifyDegraded(name, m.copyHealth(h))
			slog.Warn("[HealthMonitor] Source", "name", name, "h_consecutivefails", h.ConsecutiveFails)
		}
	}
}

// ShouldSkip 判断是否应该跳过（用于不健康源的探活）
func (m *HealthMonitor) ShouldSkip(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	h, ok := m.sources[name]
	if !ok {
		return false
	}

	if h.Status != StatusUnhealthy {
		return false
	}

	// 不健康的源按恢复间隔探活
	return time.Since(h.LastCheckTime) < m.config.RecoveryInterval
}

// GetHealth 获取健康状态
func (m *HealthMonitor) GetHealth(name string) *SourceHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if h, ok := m.sources[name]; ok {
		return m.copyHealth(h)
	}
	return nil
}

// GetAllHealth 获取所有源的健康状态
func (m *HealthMonitor) GetAllHealth() map[string]*SourceHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*SourceHealth)
	for k, v := range m.sources {
		result[k] = m.copyHealth(v)
	}
	return result
}

// GetSummary 获取健康汇总
func (m *HealthMonitor) GetSummary(includeDetails bool) *HealthSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return CalculateSummary(m.sources, includeDetails)
}

// GetUnhealthySources 获取不健康的源
func (m *HealthMonitor) GetUnhealthySources() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []string
	for name, h := range m.sources {
		if h.Status == StatusUnhealthy {
			result = append(result, name)
		}
	}
	return result
}

// GetDegradedSources 获取降级的源
func (m *HealthMonitor) GetDegradedSources() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []string
	for name, h := range m.sources {
		if h.Status == StatusDegraded {
			result = append(result, name)
		}
	}
	return result
}

// SetSourceStatus 手动设置源状态（用于启用/禁用）
func (m *HealthMonitor) SetSourceStatus(name string, status HealthStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	h, ok := m.sources[name]
	if !ok {
		return fmt.Errorf("source %s not found", name)
	}

	h.Status = status
	if status == StatusHealthy || status == StatusDisabled {
		h.awaitingFirstSuccess = false
	}
	return nil
}

// ResetSource 重置源统计
func (m *HealthMonitor) ResetSource(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	h, ok := m.sources[name]
	if !ok {
		return fmt.Errorf("source %s not found", name)
	}

	if h.awaitingFirstSuccess {
		h.Status = StatusAwaitingSchedule
	} else {
		h.Status = StatusHealthy
	}
	h.ConsecutiveFails = 0
	h.TotalRequests = 0
	h.TotalFailures = 0
	h.SuccessRate = 1.0
	h.AvgLatencyMs = 0
	h.LastError = ""

	return nil
}

// copyHealth 复制健康状态（避免数据竞争）
func (m *HealthMonitor) copyHealth(h *SourceHealth) *SourceHealth {
	copy := *h
	return &copy
}

// Config 获取配置
func (m *HealthMonitor) Config() HealthConfig {
	return m.config
}
