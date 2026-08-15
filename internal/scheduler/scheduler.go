package scheduler

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ScheduleType 调度类型
type ScheduleType string

const (
	// TypeInterval 间隔调度
	TypeInterval ScheduleType = "interval"
	// TypeCron 定时调度（每天固定时间）
	TypeCron ScheduleType = "cron"
)

// Schedule 调度配置
type Schedule struct {
	Type     ScheduleType // 调度类型
	Interval int          // 间隔秒数（TypeInterval）
	Times    []string     // 每天的时间点 "HH:MM"（TypeCron）
}

// Task 调度任务
type Task struct {
	Name     string
	Schedule Schedule
	Handler  func(ctx context.Context) error
}

// Scheduler 调度器
type Scheduler struct {
	tasks    map[string]*taskRunner
	mu       sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	running  bool
	location *time.Location // 时区
}

// NewSchedulerWithLocation 创建带时区的调度器
func NewSchedulerWithLocation(loc *time.Location) *Scheduler {
	return &Scheduler{
		tasks:    make(map[string]*taskRunner),
		location: loc,
	}
}

type taskRunner struct {
	task      *Task
	nextRun   time.Time
	lastRun   time.Time
	lastError error
}

// NewScheduler 创建调度器
func NewScheduler() *Scheduler {
	return &Scheduler{
		tasks: make(map[string]*taskRunner),
	}
}

// AddTask 添加任务
func (s *Scheduler) AddTask(task *Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tasks[task.Name]; exists {
		return fmt.Errorf("task %s already exists", task.Name)
	}

	runner := &taskRunner{
		task:    task,
		nextRun: s.calculateNextRun(task.Schedule),
	}
	s.tasks[task.Name] = runner

	fmt.Printf("Scheduler: added task %s, next run at %s\n", task.Name, runner.nextRun.Format("15:04:05"))
	return nil
}

// RemoveTask 移除任务
func (s *Scheduler) RemoveTask(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, name)
}

// Start 启动调度器
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.running = true
	s.mu.Unlock()

	s.wg.Add(1)
	go s.run()

	fmt.Println("Scheduler started")
}

// Stop 停止调度器
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	fmt.Println("Scheduler stopped")
}

// run 运行调度循环
func (s *Scheduler) run() {
	defer s.wg.Done()

	ticker := time.NewTicker(10 * time.Second) // 每10秒检查一次
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkAndRun()
		}
	}
}

// checkAndRun 检查并执行到期任务
func (s *Scheduler) checkAndRun() {
	s.mu.RLock()
	runners := make([]*taskRunner, 0, len(s.tasks))
	for _, r := range s.tasks {
		runners = append(runners, r)
	}
	s.mu.RUnlock()

	now := time.Now()
	for _, r := range runners {
		if now.After(r.nextRun) || now.Equal(r.nextRun) {
			s.executeTask(r)
		}
	}
}

// executeTask 执行任务
func (s *Scheduler) executeTask(r *taskRunner) {
	fmt.Printf("Scheduler: executing task %s\n", r.task.Name)

	// 更新下次运行时间
	s.mu.Lock()
	r.lastRun = time.Now()
	r.nextRun = s.calculateNextRun(r.task.Schedule)
	s.mu.Unlock()

	// 异步执行任务
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := r.task.Handler(s.ctx); err != nil {
			fmt.Printf("Scheduler: task %s failed: %v\n", r.task.Name, err)
			s.mu.Lock()
			r.lastError = err
			s.mu.Unlock()
		} else {
			fmt.Printf("Scheduler: task %s completed\n", r.task.Name)
		}
	}()
}

// calculateNextRun 计算下次运行时间
func (s *Scheduler) calculateNextRun(schedule Schedule) time.Time {
	loc := s.location
	if loc == nil {
		loc = time.Now().Location()
	}
	now := time.Now().In(loc)

	switch schedule.Type {
	case TypeInterval:
		return now.Add(time.Duration(schedule.Interval) * time.Second)

	case TypeCron:
		return s.nextCronTime(schedule.Times)

	default:
		return now.Add(time.Hour) // 默认1小时后
	}
}

// nextCronTime 计算下一个 cron 时间点
func (s *Scheduler) nextCronTime(times []string) time.Time {
	loc := s.location
	if loc == nil {
		loc = time.Now().Location()
	}
	now := time.Now().In(loc)

	// 解析所有时间点并排序
	var todayTimes []time.Time
	for _, t := range times {
		var hour, minute int
		if _, err := fmt.Sscanf(t, "%d:%d", &hour, &minute); err != nil {
			continue
		}
		scheduled := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
		todayTimes = append(todayTimes, scheduled)
	}

	sort.Slice(todayTimes, func(i, j int) bool {
		return todayTimes[i].Before(todayTimes[j])
	})

	// 找到今天下一个时间点
	for _, t := range todayTimes {
		if t.After(now) {
			return t
		}
	}

	// 没有找到，返回明天第一个时间点
	if len(todayTimes) > 0 {
		return todayTimes[0].Add(24 * time.Hour)
	}

	return now.Add(24 * time.Hour)
}

// GetStats 获取调度统计信息
func (s *Scheduler) GetStats() map[string]TaskStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := make(map[string]TaskStats)
	for name, r := range s.tasks {
		stats[name] = TaskStats{
			Name:      name,
			Type:      r.task.Schedule.Type,
			NextRun:   r.nextRun,
			LastRun:   r.lastRun,
			LastError: r.lastError,
		}
	}
	return stats
}

// TaskStats 任务统计
type TaskStats struct {
	Name      string
	Type      ScheduleType
	NextRun   time.Time
	LastRun   time.Time
	LastError error
}
