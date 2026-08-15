package source

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// mockHealthNotifier 记录通知调用次数，用于测试持续不健康重新告警行为
type mockHealthNotifier struct {
	mu             sync.Mutex
	degradedCalls  int
	unhealthyCalls int
	recoveredCalls int
}

func (m *mockHealthNotifier) NotifyDegraded(source string, health *SourceHealth) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.degradedCalls++
}

func (m *mockHealthNotifier) NotifyUnhealthy(source string, health *SourceHealth) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unhealthyCalls++
}

func (m *mockHealthNotifier) NotifyRecovered(source string, health *SourceHealth) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recoveredCalls++
}

func (m *mockHealthNotifier) UnhealthyCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.unhealthyCalls
}

func (m *mockHealthNotifier) RecoveredCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recoveredCalls
}

func TestHealthMonitor_ScheduledSourceTransitions(t *testing.T) {
	monitor := NewHealthMonitor(HealthConfig{DegradedThreshold: 2, UnhealthyThreshold: 3})
	notifier := &mockHealthNotifier{}
	monitor.SetNotifier(notifier)
	monitor.RegisterScheduledSource("scheduled")

	health := monitor.GetHealth("scheduled")
	if health.Status != StatusAwaitingSchedule || health.TotalRequests != 0 {
		t.Fatalf("initial scheduled health=%+v", health)
	}

	monitor.RecordSuccess("scheduled", time.Millisecond, 1)
	health = monitor.GetHealth("scheduled")
	if health.Status != StatusHealthy || notifier.RecoveredCalls() != 0 {
		t.Fatalf("first success health=%+v recovered notifications=%d", health, notifier.RecoveredCalls())
	}

	monitor.RegisterScheduledSource("scheduled-failure")
	monitor.RecordFailure("scheduled-failure", errors.New("first scheduled failure"))
	health = monitor.GetHealth("scheduled-failure")
	if health.Status != StatusAwaitingSchedule || health.ConsecutiveFails != 1 {
		t.Fatalf("first failure health=%+v", health)
	}
	monitor.RecordFailure("scheduled-failure", errors.New("second scheduled failure"))
	health = monitor.GetHealth("scheduled-failure")
	if health.Status != StatusDegraded || health.ConsecutiveFails != 2 {
		t.Fatalf("degraded scheduled health=%+v", health)
	}
	monitor.RecordFailure("scheduled-failure", errors.New("third scheduled failure"))
	health = monitor.GetHealth("scheduled-failure")
	if health.Status != StatusUnhealthy || health.ConsecutiveFails != 3 {
		t.Fatalf("unhealthy scheduled health=%+v", health)
	}
	deadline := time.Now().Add(time.Second)
	for notifier.UnhealthyCalls() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if notifier.UnhealthyCalls() != 1 {
		t.Fatalf("unhealthy notifications=%d", notifier.UnhealthyCalls())
	}
}

func TestHealthMonitor_ResetAwaitingScheduledSource(t *testing.T) {
	monitor := NewHealthMonitor(HealthConfig{DegradedThreshold: 1, UnhealthyThreshold: 2})
	monitor.RegisterScheduledSource("scheduled")
	monitor.RecordFailure("scheduled", errors.New("failure"))
	if err := monitor.ResetSource("scheduled"); err != nil {
		t.Fatal(err)
	}
	health := monitor.GetHealth("scheduled")
	if health.Status != StatusAwaitingSchedule || health.TotalRequests != 0 || health.ConsecutiveFails != 0 {
		t.Fatalf("reset scheduled health=%+v", health)
	}
}

func TestHealthMonitor_SummaryExcludesAwaitingFromAverage(t *testing.T) {
	monitor := NewHealthMonitor(DefaultHealthConfig())
	monitor.RegisterSource("healthy")
	monitor.RecordSuccess("healthy", time.Millisecond, 1)
	monitor.RegisterSource("failed")
	monitor.RecordFailure("failed", errors.New("failure"))
	monitor.RegisterScheduledSource("scheduled")
	monitor.RegisterSource("disabled")
	if err := monitor.SetSourceStatus("disabled", StatusDisabled); err != nil {
		t.Fatal(err)
	}

	summary := monitor.GetSummary(true)
	if summary.Total != 4 || summary.Healthy != 2 || summary.Disabled != 1 || summary.AwaitingSchedule != 1 {
		t.Fatalf("summary counts=%+v", summary)
	}
	if got, want := summary.Healthy+summary.Degraded+summary.Unhealthy+summary.Disabled+summary.AwaitingSchedule, summary.Total; got != want {
		t.Fatalf("status buckets=%d total=%d", got, want)
	}
	if summary.AvgSuccess != 2.0/3.0 {
		t.Fatalf("average success rate=%v want=%v", summary.AvgSuccess, 2.0/3.0)
	}
}

func TestHealthMonitor_AllAwaitingSummaryHasZeroAverage(t *testing.T) {
	monitor := NewHealthMonitor(DefaultHealthConfig())
	monitor.RegisterScheduledSource("scheduled")
	if got := monitor.GetSummary(false).AvgSuccess; got != 0 {
		t.Fatalf("average success rate=%v want=0", got)
	}
}

func TestHealthMonitor_RecordSuccess(t *testing.T) {
	cfg := HealthConfig{
		DegradedThreshold:  3,
		UnhealthyThreshold: 5,
		RecoveryInterval:   5 * time.Minute,
	}
	monitor := NewHealthMonitor(cfg)
	monitor.RegisterSource("test-source")

	// 记录成功
	monitor.RecordSuccess("test-source", 100*time.Millisecond, 10)

	health := monitor.GetHealth("test-source")
	if health == nil {
		t.Fatal("expected health to be non-nil")
	}

	if health.Status != StatusHealthy {
		t.Errorf("expected status healthy, got %s", health.Status)
	}
	if health.TotalRequests != 1 {
		t.Errorf("expected 1 total request, got %d", health.TotalRequests)
	}
	if health.SuccessRate != 1.0 {
		t.Errorf("expected 100%% success rate, got %.2f", health.SuccessRate)
	}
	if health.LastFetchCount != 10 {
		t.Errorf("expected 10 fetch count, got %d", health.LastFetchCount)
	}
}

func TestHealthMonitor_RecordFailure_Degraded(t *testing.T) {
	cfg := HealthConfig{
		DegradedThreshold:  2,
		UnhealthyThreshold: 4,
		RecoveryInterval:   5 * time.Minute,
	}
	monitor := NewHealthMonitor(cfg)
	monitor.RegisterSource("test-source")

	// 记录失败直到降级
	testErr := errors.New("test error")
	monitor.RecordFailure("test-source", testErr)
	monitor.RecordFailure("test-source", testErr)

	health := monitor.GetHealth("test-source")
	if health.Status != StatusDegraded {
		t.Errorf("expected status degraded, got %s", health.Status)
	}
	if health.ConsecutiveFails != 2 {
		t.Errorf("expected 2 consecutive fails, got %d", health.ConsecutiveFails)
	}
}

func TestHealthMonitor_RecordFailure_Unhealthy(t *testing.T) {
	cfg := HealthConfig{
		DegradedThreshold:  2,
		UnhealthyThreshold: 4,
		RecoveryInterval:   5 * time.Minute,
	}
	monitor := NewHealthMonitor(cfg)
	monitor.RegisterSource("test-source")

	// 记录失败直到不健康
	testErr := errors.New("test error")
	for i := 0; i < 4; i++ {
		monitor.RecordFailure("test-source", testErr)
	}

	health := monitor.GetHealth("test-source")
	if health.Status != StatusUnhealthy {
		t.Errorf("expected status unhealthy, got %s", health.Status)
	}
	if health.ConsecutiveFails != 4 {
		t.Errorf("expected 4 consecutive fails, got %d", health.ConsecutiveFails)
	}
}

func TestHealthMonitor_Recovery(t *testing.T) {
	cfg := HealthConfig{
		DegradedThreshold:  2,
		UnhealthyThreshold: 4,
		RecoveryInterval:   5 * time.Minute,
	}
	monitor := NewHealthMonitor(cfg)
	monitor.RegisterSource("test-source")

	// 先变成不健康
	testErr := errors.New("test error")
	for i := 0; i < 4; i++ {
		monitor.RecordFailure("test-source", testErr)
	}

	health := monitor.GetHealth("test-source")
	if health.Status != StatusUnhealthy {
		t.Errorf("expected status unhealthy, got %s", health.Status)
	}

	// 记录成功，应该恢复
	monitor.RecordSuccess("test-source", 100*time.Millisecond, 5)

	health = monitor.GetHealth("test-source")
	if health.Status != StatusHealthy {
		t.Errorf("expected status healthy after recovery, got %s", health.Status)
	}
	if health.ConsecutiveFails != 0 {
		t.Errorf("expected 0 consecutive fails after recovery, got %d", health.ConsecutiveFails)
	}
}

func TestHealthMonitor_ShouldSkip(t *testing.T) {
	cfg := HealthConfig{
		DegradedThreshold:  2,
		UnhealthyThreshold: 3,
		RecoveryInterval:   1 * time.Second, // 短间隔用于测试
	}
	monitor := NewHealthMonitor(cfg)
	monitor.RegisterSource("test-source")

	// 健康源不应该跳过
	if monitor.ShouldSkip("test-source") {
		t.Error("healthy source should not be skipped")
	}

	// 变成不健康
	testErr := errors.New("test error")
	for i := 0; i < 3; i++ {
		monitor.RecordFailure("test-source", testErr)
	}

	// 不健康源在恢复间隔内应该跳过
	if !monitor.ShouldSkip("test-source") {
		t.Error("unhealthy source should be skipped within recovery interval")
	}

	// 等待恢复间隔
	time.Sleep(1100 * time.Millisecond)

	// 恢复间隔后不应该跳过
	if monitor.ShouldSkip("test-source") {
		t.Error("unhealthy source should not be skipped after recovery interval")
	}
}

func TestHealthMonitor_GetSummary(t *testing.T) {
	cfg := HealthConfig{
		DegradedThreshold:  2,
		UnhealthyThreshold: 4,
	}
	monitor := NewHealthMonitor(cfg)
	monitor.RegisterSource("source1")
	monitor.RegisterSource("source2")
	monitor.RegisterSource("source3")

	// source1 保持健康
	monitor.RecordSuccess("source1", 100*time.Millisecond, 5)

	// source2 变成降级
	testErr := errors.New("test error")
	monitor.RecordFailure("source2", testErr)
	monitor.RecordFailure("source2", testErr)

	// source3 变成不健康
	for i := 0; i < 4; i++ {
		monitor.RecordFailure("source3", testErr)
	}

	summary := monitor.GetSummary(false)
	if summary.Total != 3 {
		t.Errorf("expected 3 total, got %d", summary.Total)
	}
	if summary.Healthy != 1 {
		t.Errorf("expected 1 healthy, got %d", summary.Healthy)
	}
	if summary.Degraded != 1 {
		t.Errorf("expected 1 degraded, got %d", summary.Degraded)
	}
	if summary.Unhealthy != 1 {
		t.Errorf("expected 1 unhealthy, got %d", summary.Unhealthy)
	}
}

func TestHealthMonitor_ResetSource(t *testing.T) {
	cfg := HealthConfig{
		DegradedThreshold:  2,
		UnhealthyThreshold: 4,
	}
	monitor := NewHealthMonitor(cfg)
	monitor.RegisterSource("test-source")

	// 变成不健康
	testErr := errors.New("test error")
	for i := 0; i < 4; i++ {
		monitor.RecordFailure("test-source", testErr)
	}

	health := monitor.GetHealth("test-source")
	if health.Status != StatusUnhealthy {
		t.Errorf("expected status unhealthy, got %s", health.Status)
	}

	// 重置
	if err := monitor.ResetSource("test-source"); err != nil {
		t.Fatalf("reset failed: %v", err)
	}

	health = monitor.GetHealth("test-source")
	if health.Status != StatusHealthy {
		t.Errorf("expected status healthy after reset, got %s", health.Status)
	}
	if health.ConsecutiveFails != 0 {
		t.Errorf("expected 0 consecutive fails after reset, got %d", health.ConsecutiveFails)
	}
	if health.TotalRequests != 0 {
		t.Errorf("expected 0 total requests after reset, got %d", health.TotalRequests)
	}
}

func TestHealthMonitor_Renotify_AfterInterval(t *testing.T) {
	cfg := HealthConfig{
		DegradedThreshold:  2,
		UnhealthyThreshold: 3,
		RecoveryInterval:   5 * time.Minute,
		RenotifyInterval:   30 * time.Millisecond,
	}
	monitor := NewHealthMonitor(cfg)
	monitor.RegisterSource("test-source")

	notifier := &mockHealthNotifier{}
	monitor.SetNotifier(notifier)

	testErr := errors.New("test error")

	// 连续失败直到不健康，触发首次告警
	for i := 0; i < 3; i++ {
		monitor.RecordFailure("test-source", testErr)
	}
	time.Sleep(50 * time.Millisecond)
	if got := notifier.UnhealthyCalls(); got != 1 {
		t.Fatalf("expected 1 unhealthy notification after first transition, got %d", got)
	}

	// 等待超过重新告警间隔后再次失败，应重新告警
	time.Sleep(50 * time.Millisecond)
	monitor.RecordFailure("test-source", testErr)
	time.Sleep(50 * time.Millisecond)
	if got := notifier.UnhealthyCalls(); got != 2 {
		t.Fatalf("expected 2 unhealthy notifications after renotify interval elapsed, got %d", got)
	}

	health := monitor.GetHealth("test-source")
	if health.UnhealthySince.IsZero() {
		t.Error("expected UnhealthySince to be set while unhealthy")
	}
}

func TestHealthMonitor_Renotify_WithinInterval(t *testing.T) {
	cfg := HealthConfig{
		DegradedThreshold:  2,
		UnhealthyThreshold: 3,
		RecoveryInterval:   5 * time.Minute,
		RenotifyInterval:   5 * time.Minute,
	}
	monitor := NewHealthMonitor(cfg)
	monitor.RegisterSource("test-source")

	notifier := &mockHealthNotifier{}
	monitor.SetNotifier(notifier)

	testErr := errors.New("test error")

	// 连续失败直到不健康，触发首次告警
	for i := 0; i < 3; i++ {
		monitor.RecordFailure("test-source", testErr)
	}
	time.Sleep(50 * time.Millisecond)
	if got := notifier.UnhealthyCalls(); got != 1 {
		t.Fatalf("expected 1 unhealthy notification after first transition, got %d", got)
	}

	// 仍在重新告警间隔内继续失败，不应再次告警
	monitor.RecordFailure("test-source", testErr)
	time.Sleep(50 * time.Millisecond)
	if got := notifier.UnhealthyCalls(); got != 1 {
		t.Fatalf("expected notification count to stay at 1 within renotify interval, got %d", got)
	}
}

func TestHealthMonitor_Recovery_ClearsUnhealthyTimestamps(t *testing.T) {
	cfg := HealthConfig{
		DegradedThreshold:  2,
		UnhealthyThreshold: 3,
		RecoveryInterval:   5 * time.Minute,
		RenotifyInterval:   5 * time.Minute,
	}
	monitor := NewHealthMonitor(cfg)
	monitor.RegisterSource("test-source")

	notifier := &mockHealthNotifier{}
	monitor.SetNotifier(notifier)

	testErr := errors.New("test error")
	for i := 0; i < 3; i++ {
		monitor.RecordFailure("test-source", testErr)
	}

	health := monitor.GetHealth("test-source")
	if health.UnhealthySince.IsZero() {
		t.Fatal("expected UnhealthySince to be set once unhealthy")
	}
	if health.LastNotifiedAt.IsZero() {
		t.Fatal("expected LastNotifiedAt to be set once unhealthy")
	}

	monitor.RecordSuccess("test-source", 100*time.Millisecond, 5)

	health = monitor.GetHealth("test-source")
	if !health.UnhealthySince.IsZero() {
		t.Error("expected UnhealthySince to be cleared after recovery")
	}
	if !health.LastNotifiedAt.IsZero() {
		t.Error("expected LastNotifiedAt to be cleared after recovery")
	}
}

func TestHealthMonitor_GetUnhealthySources(t *testing.T) {
	cfg := HealthConfig{
		DegradedThreshold:  2,
		UnhealthyThreshold: 3,
	}
	monitor := NewHealthMonitor(cfg)
	monitor.RegisterSource("healthy")
	monitor.RegisterSource("unhealthy1")
	monitor.RegisterSource("unhealthy2")

	monitor.RecordSuccess("healthy", 100*time.Millisecond, 5)

	testErr := errors.New("test error")
	for i := 0; i < 3; i++ {
		monitor.RecordFailure("unhealthy1", testErr)
		monitor.RecordFailure("unhealthy2", testErr)
	}

	unhealthy := monitor.GetUnhealthySources()
	if len(unhealthy) != 2 {
		t.Errorf("expected 2 unhealthy sources, got %d", len(unhealthy))
	}
}
