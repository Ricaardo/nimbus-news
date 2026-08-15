package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/config"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/filter"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/metrics"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/source"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

// sourceStateBucket 记录定时源已执行过的时间槽，用于避免进程重启后重复补推
const sourceStateBucket = "source_state"

// newsSourceInfo 存储新闻源信息用于调度
type newsSourceInfo struct {
	name            string
	interval        int      // 间隔调度（秒），0表示使用定时调度
	schedule        []string // 定时调度（HH:MM格式）
	channels        []string
	aiEnhance       bool
	tradingDaysOnly bool
	deliveryMode    string
	briefingTarget  digest.Briefing
	routing         *config.SourceRoutingConfig
	ctx             context.Context
}

type sourceOperationGate struct {
	mu sync.RWMutex
}

// SourceSchedule 定时源调度信息（导出给 API）
type SourceSchedule struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Schedule []string `json:"schedule"`
}

// NewsEngine 新闻引擎
type NewsEngine struct {
	sources        map[string]source.Source
	sourceInfos    []newsSourceInfo
	fetchGates     sync.Map // name → *sync.Mutex,主动拉取并发门(防双跑 LLM/双租 digest 租约)
	filters        *filter.Chain
	store          store.Store
	digestStore    digest.Store
	newsStore      *store.NewsStore
	quoteStore     *store.QuoteStore
	enhancer       *llm.NewsEnhancer
	router         *Router
	mirrorChannels []string // 全量镜像渠道（追加到每个源的 channels）

	// 补推队列
	retryQueue *filter.RetryQueue

	// 健康监控
	healthMonitor *source.HealthMonitor

	// 异步 AI 评估
	asyncEval *filter.AsyncEvaluator

	// 热更新：每个源独立的 cancel，允许动态启停
	sourceCancels       map[string]context.CancelFunc
	sourceDones         map[string]chan struct{}
	sourceMu            sync.Mutex
	sourceGenerations   map[string]uint64
	sourceGates         map[string]*sourceOperationGate
	llmProvider         llm.Provider
	beforeSourceEffects func()

	running    bool
	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup

	// Direct delivery has a separate graceful-shutdown lifetime. Source and
	// recovery scheduling use ctx and are cancelled first; a send that already
	// started keeps this context until its Bolt checkpoint has completed.
	directMu          sync.Mutex
	directAccepting   bool
	directInitialized bool
	directCtx         context.Context
	directCancel      context.CancelFunc
	directWG          sync.WaitGroup
}

var (
	ErrSourceNotFound   = errors.New("source not found")
	ErrSourceConflict   = errors.New("source registry changed")
	ErrSourceValidation = errors.New("invalid source configuration")
	ErrEngineStopped    = errors.New("news engine stopped")
)

// PreparedSourceChange owns a fully constructed, dependency-injected source
// until it is either atomically activated or aborted.
type PreparedSourceChange struct {
	engine     *NewsEngine
	cfg        SourceConfig
	src        source.Source
	info       newsSourceInfo
	old        source.Source
	generation uint64
	existed    bool
	activated  bool
	gate       *sourceOperationGate
}

// NewsEngineConfig 新闻引擎配置
type NewsEngineConfig struct {
	Sources        []SourceConfig
	FilterChain    *filter.Chain
	Store          store.Store
	DigestStore    digest.Store
	NewsStore      *store.NewsStore
	QuoteStore     *store.QuoteStore
	Enhancer       *llm.NewsEnhancer
	AsyncEval      *filter.AsyncEvaluator // 异步 AI 评估器（可选）
	Router         *Router
	HealthConfig   source.HealthConfig
	MirrorChannels []string // 全量镜像渠道（追加到每个源的 channels）
}

// SourceConfig 源配置
type SourceConfig struct {
	Name            string
	Type            string
	URL             string
	Interval        int      // 间隔调度（秒）
	Schedule        []string // 定时调度（HH:MM格式）
	Channels        []string
	AIEnhance       bool
	TradingDaysOnly bool
	DeliveryMode    string
	BriefingTarget  string
	Enabled         *bool
	Options         map[string]interface{}
	Routing         *config.SourceRoutingConfig
}

// withMirror 在渠道列表后追加全量镜像渠道（去重）。用于把所有新闻同时镜像到
// 额外渠道，并享受与正式渠道相同的过滤（去重/降噪）。
func (e *NewsEngine) withMirror(channels []string) []string {
	if len(e.mirrorChannels) == 0 {
		return channels
	}
	out := append([]string(nil), channels...)
	for _, m := range e.mirrorChannels {
		seen := false
		for _, c := range out {
			if c == m {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, m)
		}
	}
	return out
}

// NewNewsEngine 创建新闻引擎
func NewNewsEngine(cfg NewsEngineConfig) (*NewsEngine, error) {
	for _, srcCfg := range cfg.Sources {
		if srcCfg.Routing != nil {
			if _, ok := cfg.DigestStore.(digest.RoutedStore); !ok {
				return nil, fmt.Errorf("create source %s failed: typed routing requires routed digest store", srcCfg.Name)
			}
		}
	}
	// 初始化健康监控器
	var healthMonitor *source.HealthMonitor
	if cfg.HealthConfig.Enabled {
		healthMonitor = source.NewHealthMonitor(cfg.HealthConfig)
	}

	e := &NewsEngine{
		sources:           make(map[string]source.Source),
		filters:           cfg.FilterChain,
		store:             cfg.Store,
		digestStore:       cfg.DigestStore,
		newsStore:         cfg.NewsStore,
		quoteStore:        cfg.QuoteStore,
		enhancer:          cfg.Enhancer,
		asyncEval:         cfg.AsyncEval,
		router:            cfg.Router,
		mirrorChannels:    cfg.MirrorChannels,
		retryQueue:        filter.NewRetryQueue(500, 10), // 最多500条，最多重试10次
		healthMonitor:     healthMonitor,
		sourceCancels:     make(map[string]context.CancelFunc),
		sourceDones:       make(map[string]chan struct{}),
		sourceGenerations: make(map[string]uint64),
		sourceGates:       make(map[string]*sourceOperationGate),
	}

	// 初始化源
	for _, srcCfg := range cfg.Sources {
		src, err := e.constructSource(srcCfg)
		if err != nil {
			for _, created := range e.sources {
				closeSource(created)
			}
			return nil, fmt.Errorf("create source %s failed: %w", srcCfg.Name, err)
		}

		e.sources[srcCfg.Name] = src
		e.sourceGenerations[srcCfg.Name] = 1
		e.sourceGates[srcCfg.Name] = &sourceOperationGate{}
		e.sourceInfos = append(e.sourceInfos, newsSourceInfo{
			name:            srcCfg.Name,
			interval:        srcCfg.Interval,
			schedule:        srcCfg.Schedule,
			channels:        srcCfg.Channels,
			aiEnhance:       srcCfg.AIEnhance,
			tradingDaysOnly: srcCfg.TradingDaysOnly,
			deliveryMode:    deliveryMode(srcCfg.DeliveryMode),
			briefingTarget:  digest.Briefing(srcCfg.BriefingTarget),
			routing:         srcCfg.Routing.DeepCopy(),
		})

		e.registerSourceHealth(srcCfg)
	}

	return e, nil
}

// FetchOnly 同步拉取指定源并返回消息(不推送)。主动拉取入口:
// 用户随时查看某个源当前会产出什么,不等定时调度。
// 跳过健康 ShouldSkip 与 generation fence——主动拉取即执行。
// ctx 用于调用方限时(如 API handler 90s);源内部有自身的超时控制。
func (e *NewsEngine) FetchOnly(ctx context.Context, name string) ([]*model.Message, error) {
	e.sourceMu.Lock()
	if !e.running {
		e.sourceMu.Unlock()
		return nil, ErrEngineStopped
	}
	src, ok := e.sources[name]
	if !ok {
		e.sourceMu.Unlock()
		return nil, fmt.Errorf("source not found: %s", name)
	}
	var selected newsSourceInfo
	for _, info := range e.sourceInfos {
		if info.name == name {
			selected = info
			break
		}
	}
	e.sourceMu.Unlock()
	if selected.name == "" {
		return nil, fmt.Errorf("source info not found: %s", name)
	}

	// 并发门:同一源同时只允许一个主动拉取(简报源含 LLM 调用与 digest 租约,
	// 双跑会双倍消耗并争用租约)。与调度链路的 sourceGates 相互独立。
	gate, _ := e.fetchGates.LoadOrStore(name, &sync.Mutex{})
	gate.(*sync.Mutex).Lock()
	defer gate.(*sync.Mutex).Unlock()

	startTime := time.Now()
	fetchCtx := ctx
	if fetchCtx == nil {
		fetchCtx = selected.ctx
	}
	if fetchCtx == nil {
		fetchCtx = e.ctx
	}
	if fetchCtx == nil {
		fetchCtx = context.Background()
	}
	var messages []*model.Message
	var err error
	if fetcher, ok := src.(source.ContextFetcher); ok {
		messages, err = fetcher.FetchContext(fetchCtx)
	} else {
		messages, err = src.Fetch()
	}
	latency := time.Since(startTime)

	if err != nil {
		if e.healthMonitor != nil {
			e.healthMonitor.RecordFailure(src.Name(), err)
		}
		metrics.SourceFetchTotal.WithLabelValues(src.Name(), "error").Inc()
		metrics.SourceFetchDuration.WithLabelValues(src.Name()).Observe(latency.Seconds())
		return nil, err
	}
	// 健康语义:有产出才算成功;0 条(304/空源)不更新健康——
	// 避免主动拉取掩盖真实故障(304 只代表内容未变,不代表源健康)。
	if len(messages) > 0 && e.healthMonitor != nil {
		e.healthMonitor.RecordSuccess(src.Name(), latency, len(messages))
	}
	metrics.SourceFetchTotal.WithLabelValues(src.Name(), "success").Inc()
	metrics.SourceFetchDuration.WithLabelValues(src.Name()).Observe(latency.Seconds())
	metrics.SourceMessagesTotal.WithLabelValues(src.Name()).Add(float64(len(messages)))
	return messages, nil
}

// TriggerSource 手动触发指定源（测试用）
func (e *NewsEngine) TriggerSource(name string) error {
	e.sourceMu.Lock()
	if !e.running {
		e.sourceMu.Unlock()
		return ErrEngineStopped
	}
	src, ok := e.sources[name]
	if !ok {
		e.sourceMu.Unlock()
		return fmt.Errorf("source not found: %s", name)
	}
	var selected newsSourceInfo
	for _, info := range e.sourceInfos {
		if info.name == name {
			selected = info
			break
		}
	}
	if selected.name == "" {
		e.sourceMu.Unlock()
		return fmt.Errorf("source info not found: %s", name)
	}
	selected.ctx = e.ctx
	generation := e.sourceGenerations[name]
	e.wg.Add(1)
	e.sourceMu.Unlock()
	go func() {
		defer e.wg.Done()
		e.fetchAndDispatchGeneration(src, selected, generation)
	}()
	return nil
}

// GetQuoteStore 获取行情存储
func (e *NewsEngine) GetQuoteStore() *store.QuoteStore {
	return e.quoteStore
}

// InjectLLM 向所有实现 LLMSettable 接口的源注入 LLM Provider
// 在 bootstrap 阶段由 InitLLM 调用, 聚合报告源通过此机制启用 AI 总结
func (e *NewsEngine) InjectLLM(provider llm.Provider) {
	if provider == nil {
		return
	}
	count := 0
	e.sourceMu.Lock()
	e.llmProvider = provider
	for name, src := range e.sources {
		if settable, ok := src.(source.LLMSettable); ok {
			settable.SetLLMProvider(provider)
			count++
			slog.Debug("llm injected", "source", name)
		}
	}
	e.sourceMu.Unlock()
	if count > 0 {
		slog.Info("llm injected to sources", "count", count)
	}
}

// Start 启动引擎
func (e *NewsEngine) Start(ctx context.Context) {
	e.sourceMu.Lock()
	defer e.sourceMu.Unlock()
	e.ctx, e.cancelFunc = context.WithCancel(ctx)
	e.directMu.Lock()
	e.directCtx, e.directCancel = context.WithCancel(context.WithoutCancel(ctx))
	e.directInitialized = true
	e.directAccepting = true
	e.directMu.Unlock()
	e.running = true
	e.sourceCancels = make(map[string]context.CancelFunc)
	e.sourceDones = make(map[string]chan struct{})

	slog.Info("newsengine starting")

	// 设置补推队列的分发函数
	e.retryQueue.SetDispatcher(func(ctx context.Context, msg *model.Message, sinks []string) error {
		if e.router != nil {
			return e.router.DispatchNews(ctx, msg, sinks)
		}
		return nil
	})

	// 为频控过滤器设置补推队列
	if e.filters != nil {
		for _, f := range e.filters.GetFilters() {
			if rf, ok := f.(*filter.RatelimitFilter); ok {
				rf.SetRetryQueue(e.retryQueue)
			}
		}
	}

	// 启动源调度（每个源使用独立 context，支持热启停）
	for _, info := range e.sourceInfos {
		src := e.sources[info.name]
		e.startSourceGoroutine(src, info, e.sourceGenerations[info.name])
	}

	// 启动补推处理协程
	e.wg.Add(1)
	go e.runRetryProcessor()
	if _, ok := e.digestStore.(digest.RoutedStore); ok {
		e.wg.Add(1)
		go e.runDirectRecovery()
	}

	// 启动异步 AI 评估器
	if e.asyncEval != nil {
		e.asyncEval.Start(e.ctx)
	}

	slog.Info("newsengine started", "sources", len(e.sources))
}

func (e *NewsEngine) runDirectRecovery() {
	defer e.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		e.recoverPendingDirect()
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (e *NewsEngine) recoverPendingDirect() {
	routedStore, ok := e.digestStore.(digest.RoutedStore)
	if !ok || e.ctx == nil {
		return
	}
	for processed := 0; processed < 100 && e.ctx.Err() == nil; processed++ {
		claims, err := routedStore.ClaimPendingDirect(e.ctx, 1, time.Minute)
		if err != nil {
			if e.ctx.Err() == nil {
				slog.Error("claim pending direct routes failed", "error", err)
			}
			return
		}
		if len(claims) == 0 {
			return
		}
		e.runDirectClaim(e.ctx, routedStore, claims[0])
	}
}

func (e *NewsEngine) runDirectClaim(fallback context.Context, routedStore digest.RoutedStore, claim digest.DirectClaim) bool {
	e.directMu.Lock()
	if e.directInitialized && !e.directAccepting {
		e.directMu.Unlock()
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := routedStore.ReleaseDirect(releaseCtx, claim.Source, claim.MessageID, claim.Token)
		cancel()
		if err != nil {
			slog.Error("release fenced direct claim failed", "source", claim.Source, "error", err)
		}
		return false
	}
	e.directWG.Add(1)
	ctx := e.directCtx
	if ctx == nil {
		ctx = fallback
	}
	e.directMu.Unlock()
	defer e.directWG.Done()
	return e.deliverDirectClaim(ctx, routedStore, claim)
}

// Stop 停止引擎
func (e *NewsEngine) Stop() {
	slog.Info("newsengine stopping")
	// Fence new direct sends before stopping source/recovery scheduling. Add and
	// Wait are serialized by directMu, so no direct work can join after the
	// graceful drain begins.
	e.directMu.Lock()
	e.directAccepting = false
	e.directMu.Unlock()

	e.sourceMu.Lock()
	e.running = false
	cancel := e.cancelFunc
	sources := make([]source.Source, 0, len(e.sources))
	for _, src := range e.sources {
		sources = append(sources, src)
	}
	e.sourceMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if e.asyncEval != nil {
		e.asyncEval.Stop()
	}

	directDone := make(chan struct{})
	go func() {
		e.directWG.Wait()
		close(directDone)
	}()
	select {
	case <-directDone:
	case <-time.After(500 * time.Millisecond):
		e.directMu.Lock()
		if e.directCancel != nil {
			e.directCancel()
		}
		e.directMu.Unlock()
		// All production Source and Channel implementations must honor their
		// context or internal timeout. Resource owners close channels and Bolt
		// immediately after Stop, so tracked work must drain before we return.
		<-directDone
	}
	e.directMu.Lock()
	if e.directCancel != nil {
		e.directCancel()
	}
	e.directCtx = nil
	e.directCancel = nil
	e.directMu.Unlock()

	// Direct recovery is part of the main group. Guarantee the drain before
	// closing sources so Router.Stop can safely close channels and storage next.
	e.wg.Wait()
	for _, src := range sources {
		closeSource(src)
	}

	slog.Info("newsengine stopped")
}

// runIntervalSourceWithCtx 运行间隔调度的源（使用独立 context）
func (e *NewsEngine) runIntervalSourceWithCtx(ctx context.Context, src source.Source, info newsSourceInfo, generation uint64, done chan struct{}) {
	defer e.wg.Done()
	defer close(done)

	interval := time.Duration(info.interval) * time.Second
	if interval <= 0 {
		interval = 120 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.Info("source scheduled", "name", info.name, "interval", interval)

	// 首次执行
	e.fetchAndDispatchGeneration(src, info, generation)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.fetchAndDispatchGeneration(src, info, generation)
		}
	}
}

// runScheduledSourceWithCtx 运行定时调度的源（使用独立 context）
func (e *NewsEngine) runScheduledSourceWithCtx(ctx context.Context, src source.Source, info newsSourceInfo, generation uint64, done chan struct{}) {
	defer e.wg.Done()
	defer close(done)

	slog.Info("source scheduled by time", "name", info.name, "schedule", info.schedule)

	// 跳过非交易日（仅对设置了 trading_days_only 的源生效）
	if info.tradingDaysOnly && isNonTradingDay(time.Now()) {
		slog.Debug("source skipped: non-trading day", "source", info.name)
		return
	}

	// 启动时做补推判断：仅当今天存在已过去且尚未记录运行过的时间槽时才补跑一次，
	// 避免进程重启导致同一时间槽被重复推送
	e.runScheduleCatchUpGeneration(src, info, generation)

	for {
		nextRun, slot := e.calculateNextScheduledTime(info.schedule)
		waitDuration := time.Until(nextRun)

		slog.Debug("source next run", "name", info.name, "next_run", nextRun.Format("15:04:05"), "in", waitDuration)

		select {
		case <-ctx.Done():
			return
		case <-time.After(waitDuration):
			// 过了午夜可能滚入非交易日，再次检查
			if info.tradingDaysOnly && isNonTradingDay(time.Now()) {
				slog.Debug("source skipped: non-trading day", "source", info.name)
				continue
			}
			if e.fetchAndDispatchGeneration(src, info, generation) {
				e.markScheduleRun(info.name, nextRun, slot)
			}
		}
	}
}

// isNonTradingDay 判断是否为非交易日（简化：仅判周末）
func isNonTradingDay(t time.Time) bool {
	wd := t.Weekday()
	return wd == time.Saturday || wd == time.Sunday
}

// runScheduleCatchUp 启动时的补推逻辑。若无可用 store（如测试环境），退化为旧行为：
// 启动时总是抓取一次。否则只在今天存在已过去且尚未标记运行过的最近时间槽时补跑一次。
func (e *NewsEngine) runScheduleCatchUp(src source.Source, info newsSourceInfo) {
	e.runScheduleCatchUpGeneration(src, info, 0)
}

func (e *NewsEngine) runScheduleCatchUpGeneration(src source.Source, info newsSourceInfo, generation uint64) {
	if e.store == nil {
		slog.Info("source initial fetch", "name", info.name)
		e.fetchAndDispatchGeneration(src, info, generation)
		return
	}

	slot, has := e.mostRecentPastSlot(info.schedule)
	if !has {
		slog.Info("schedule catch-up skipped", "name", info.name)
		return
	}

	dateStr := time.Now().Format("2006-01-02")
	key := e.scheduleRunKey(info.name, dateStr, slot)
	already, err := e.store.Exists(sourceStateBucket, key)
	if err != nil {
		slog.Warn("schedule catch-up check failed", "name", info.name, "error", err)
	}
	if already {
		slog.Info("schedule catch-up skipped", "name", info.name)
		return
	}

	slog.Info("schedule catch-up run", "name", info.name, "slot", slot)
	if e.fetchAndDispatchGeneration(src, info, generation) {
		if err := e.store.Set(sourceStateBucket, key, 24*time.Hour); err != nil {
			slog.Warn("mark schedule run failed", "name", info.name, "error", err)
		}
	}
}

// scheduleRunKey 生成定时源某个日期、某个时间槽的存储键
func (e *NewsEngine) scheduleRunKey(sourceName, date, slot string) string {
	return fmt.Sprintf("s|%s|%s|%s", sourceName, date, slot)
}

// markScheduleRun 标记某个定时源的时间槽已完成一次运行（成功抓取，即便0条消息也算）
func (e *NewsEngine) markScheduleRun(sourceName string, at time.Time, slot string) {
	if e.store == nil || slot == "" {
		return
	}
	key := e.scheduleRunKey(sourceName, at.Format("2006-01-02"), slot)
	if err := e.store.Set(sourceStateBucket, key, 24*time.Hour); err != nil {
		slog.Warn("mark schedule run failed", "name", sourceName, "error", err)
	}
}

// mostRecentPastSlot 返回今天已过去的时间槽中最近的一个（HH:MM 格式）
func (e *NewsEngine) mostRecentPastSlot(schedule []string) (string, bool) {
	return mostRecentPastSlotAt(schedule, time.Now())
}

// mostRecentPastSlotAt 是 mostRecentPastSlot 的可测试版本，接受 now 作为参数
func mostRecentPastSlotAt(schedule []string, now time.Time) (string, bool) {
	loc := now.Location()

	var slot string
	var slotTime time.Time
	found := false
	for _, t := range schedule {
		var hour, minute int
		if _, err := fmt.Sscanf(t, "%d:%d", &hour, &minute); err != nil {
			continue
		}
		scheduled := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
		if scheduled.After(now) {
			continue
		}
		if !found || scheduled.After(slotTime) {
			slotTime = scheduled
			slot = fmt.Sprintf("%02d:%02d", hour, minute)
			found = true
		}
	}
	return slot, found
}

// calculateNextScheduledTime 计算下一个定时执行时间及其对应的时间槽（HH:MM）
func (e *NewsEngine) calculateNextScheduledTime(schedule []string) (time.Time, string) {
	return calculateNextScheduledTimeAt(schedule, time.Now())
}

// calculateNextScheduledTimeAt 是 calculateNextScheduledTime 的可测试版本，接受 now 作为参数
func calculateNextScheduledTimeAt(schedule []string, now time.Time) (time.Time, string) {
	loc := now.Location()

	type slotEntry struct {
		t    time.Time
		slot string
	}

	var todaySlots []slotEntry
	for _, t := range schedule {
		var hour, minute int
		if _, err := fmt.Sscanf(t, "%d:%d", &hour, &minute); err != nil {
			continue
		}
		scheduled := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
		todaySlots = append(todaySlots, slotEntry{t: scheduled, slot: fmt.Sprintf("%02d:%02d", hour, minute)})
	}

	sort.Slice(todaySlots, func(i, j int) bool {
		return todaySlots[i].t.Before(todaySlots[j].t)
	})

	// 找到今天下一个时间点
	for _, s := range todaySlots {
		if s.t.After(now) {
			return s.t, s.slot
		}
	}

	// 没有找到，返回明天第一个时间点
	if len(todaySlots) > 0 {
		return todaySlots[0].t.Add(24 * time.Hour), todaySlots[0].slot
	}

	return now.Add(24 * time.Hour), ""
}

// runRetryProcessor 运行补推处理器
func (e *NewsEngine) runRetryProcessor() {
	defer e.wg.Done()

	ticker := time.NewTicker(30 * time.Second) // 每30秒检查一次
	defer ticker.Stop()

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.processRetryQueue()
		}
	}
}

// processRetryQueue 处理补推队列
func (e *NewsEngine) processRetryQueue() {
	if e.retryQueue == nil || e.filters == nil {
		return
	}

	// 找到频控过滤器
	var ratelimitFilter *filter.RatelimitFilter
	for _, f := range e.filters.GetFilters() {
		if rf, ok := f.(*filter.RatelimitFilter); ok {
			ratelimitFilter = rf
			break
		}
	}

	if ratelimitFilter == nil {
		return
	}

	// 使用不入队的检查方法
	e.retryQueue.ProcessRetries(e.ctx, func(source, sink string, msg *model.Message) bool {
		return ratelimitFilter.ShouldFilterWithoutQueue(source, sink, msg)
	})
}

// fetchAndDispatch 抓取并分发消息。返回值表示本次是否完成了一次有效抓取
// （Fetch 未报错即视为完成，即便返回0条消息；不健康跳过或 Fetch 出错均为 false）。
func (e *NewsEngine) fetchAndDispatch(src source.Source, info newsSourceInfo) bool {
	return e.fetchAndDispatchGeneration(src, info, 0)
}

func (e *NewsEngine) fetchAndDispatchGeneration(src source.Source, info newsSourceInfo, generation uint64) bool {
	// 检查是否应该跳过（不健康源的探活间隔）
	if e.healthMonitor != nil && e.healthMonitor.ShouldSkip(src.Name()) {
		slog.Warn("skipping unhealthy source", "name", src.Name())
		return false
	}

	slog.Debug("fetching from source", "name", src.Name(), "time", time.Now().Format("15:04:05"))

	// 记录开始时间
	startTime := time.Now()

	// 抓取消息
	fetchCtx := info.ctx
	if fetchCtx == nil {
		fetchCtx = e.ctx
	}
	if fetchCtx == nil {
		fetchCtx = context.Background()
	}
	var messages []*model.Message
	var err error
	if fetcher, ok := src.(source.ContextFetcher); ok {
		messages, err = fetcher.FetchContext(fetchCtx)
	} else {
		messages, err = src.Fetch()
	}

	// 计算延迟
	latency := time.Since(startTime)
	if generation != 0 && !e.isCurrentSource(info.name, src, generation) {
		slog.Debug("discarding stale source fetch", "source", info.name)
		return false
	}
	if e.beforeSourceEffects != nil {
		e.beforeSourceEffects()
	}
	releaseEffects, current := e.beginSourceEffects(info.name, src, generation)
	if !current {
		slog.Debug("discarding stale source effects", "source", info.name)
		return false
	}
	defer releaseEffects()
	effectCtx := info.ctx
	if effectCtx == nil {
		effectCtx = e.ctx
	}
	if effectCtx == nil {
		effectCtx = context.Background()
	}

	if err != nil {
		slog.Error("fetch error", "source", src.Name(), "error", err)
		// 记录失败
		if e.healthMonitor != nil {
			e.healthMonitor.RecordFailure(src.Name(), err)
		}
		// 记录指标
		metrics.SourceFetchTotal.WithLabelValues(src.Name(), "error").Inc()
		metrics.SourceFetchDuration.WithLabelValues(src.Name()).Observe(latency.Seconds())
		return false
	}

	digestBriefing := len(messages) > 0 && messages[0].GetStringMetadata("digest_lease_id") != ""
	// A leased briefing is only a successful source run after its rendered
	// payload has been durably prepared for delivery.
	if !digestBriefing {
		if e.healthMonitor != nil {
			e.healthMonitor.RecordSuccess(src.Name(), latency, len(messages))
		}
		metrics.SourceFetchTotal.WithLabelValues(src.Name(), "success").Inc()
	}
	metrics.SourceFetchDuration.WithLabelValues(src.Name()).Observe(latency.Seconds())
	metrics.SourceMessagesTotal.WithLabelValues(src.Name()).Add(float64(len(messages)))

	if len(messages) == 0 {
		slog.Debug("no messages from source", "name", src.Name())
		return true
	}

	slog.Info("fetched messages", "count", len(messages), "source", src.Name(), "latency", latency)

	// 转换为新的消息格式
	var allMessages []*model.Message
	for _, oldMsg := range messages {
		msg := &model.Message{
			ID:         oldMsg.ID,
			Type:       model.TypeNews,
			Title:      oldMsg.Title,
			Content:    oldMsg.Content,
			Source:     oldMsg.Source,
			SourceType: oldMsg.SourceType,
			Link:       oldMsg.Link,
			ImageURL:   oldMsg.ImageURL,
			ImageURLs:  oldMsg.ImageURLs,
			VideoURL:   oldMsg.VideoURL,
			Tags:       oldMsg.Tags,
			CreateTime: oldMsg.CreateTime,
			FetchTime:  oldMsg.FetchTime,
			Metadata:   oldMsg.Metadata,
		}
		allMessages = append(allMessages, msg)
	}

	// 先存储所有原始消息（供调试和 Agent 查询）
	if e.newsStore != nil {
		for _, msg := range allMessages {
			news := model.NewsFromMessage(msg)
			if err := e.newsStore.Save(effectCtx, news); err != nil {
				stats := e.newsStore.WriteStats()
				slog.Warn("news persistence failed",
					"source", src.Name(),
					"news_id", news.ID,
					"write_failures", stats.WriteFailures,
					"last_write_failed", stats.LastWriteFailed,
					"error", err,
				)
				continue
			}
			metrics.NewsStoreSize.Set(float64(e.newsStore.Count()))
		}
	}

	if info.deliveryMode == "silent" {
		return true
	}

	// A briefing containing a leased digest has already passed filtering and
	// enhancement when its items were enqueued.
	if allMessages[0].GetStringMetadata("digest_lease_id") != "" {
		if e.router == nil {
			err := fmt.Errorf("digest delivery router unavailable")
			slog.Error("digest delivery preparation failed", "error", err)
			if e.healthMonitor != nil {
				e.healthMonitor.RecordFailure(src.Name(), err)
			}
			metrics.SourceFetchTotal.WithLabelValues(src.Name(), "error").Inc()
			return false
		}
		for _, msg := range allMessages {
			if err := e.router.DispatchNews(effectCtx, msg, info.channels); err != nil {
				slog.Error("digest delivery preparation failed", "error", err)
				if e.healthMonitor != nil {
					e.healthMonitor.RecordFailure(src.Name(), err)
				}
				metrics.SourceFetchTotal.WithLabelValues(src.Name(), "error").Inc()
				return false
			}
		}
		if e.healthMonitor != nil {
			e.healthMonitor.RecordSuccess(src.Name(), latency, len(messages))
		}
		metrics.SourceFetchTotal.WithLabelValues(src.Name(), "success").Inc()
		return true
	}

	if info.routing != nil {
		// NewsStore is the pre-evaluation raw archive. Detach the runtime map so
		// routing metadata does not mutate the in-memory archive entry by alias.
		for _, msg := range allMessages {
			msg.Metadata = cloneMessageMetadata(msg.Metadata)
		}
		return e.routeTypedMessages(effectCtx, src.Name(), info, allMessages)
	}

	if info.deliveryMode == "digest" {
		var accepted []*model.Message
		for i, msg := range allMessages {
			shouldFilter := e.filters != nil && e.filters.ShouldFilter(src.Name(), filter.DigestSinkName, messages[i])
			if !shouldFilter {
				accepted = append(accepted, msg)
			}
		}
		if e.enhancer != nil {
			e.enhancer.EnhanceBatch(effectCtx, accepted, 5)
		}
		for _, msg := range accepted {
			if msg.GetStringMetadata("drop") == "1" {
				continue
			}
			if e.digestStore == nil {
				slog.Error("digest enqueue failed", "source", src.Name(), "error", "digest store unavailable")
				return false
			}
			if err := e.digestStore.Enqueue(effectCtx, src.Name(), info.briefingTarget, msg); err != nil {
				slog.Error("digest enqueue failed", "source", src.Name(), "error", err)
				return false
			}
		}
		return true
	}

	// 应用过滤器
	var filtered []*model.Message
	for i, msg := range allMessages {
		oldMsg := messages[i]
		channelsForMsg := e.withMirror(info.channels)
		passedChannels := make([]string, 0, len(channelsForMsg))
		for _, chName := range channelsForMsg {
			shouldFilter := e.filters != nil && e.filters.ShouldFilter(src.Name(), chName, oldMsg)
			slog.Debug("filter check", "source", src.Name(), "channel", chName, "should_filter", shouldFilter, "title", msg.Title[:min(30, len(msg.Title))])
			if !shouldFilter {
				passedChannels = append(passedChannels, chName)
			}
		}

		if len(passedChannels) > 0 {
			msg.SetMetadata("target_channels", passedChannels)
			filtered = append(filtered, msg)
		}
	}

	slog.Info("after filtering", "count", len(filtered))

	// AI 增强（并发处理 + 缓存）
	if e.enhancer != nil {
		e.enhancer.EnhanceBatch(effectCtx, filtered, 5) // 并发数 5
	}

	// 分发到渠道（只发送到通过过滤的渠道）
	if e.router != nil {
		for _, msg := range filtered {
			// 跳过被 AI 增强器标记为非财经噪音的消息（全渠道丢弃）
			if msg.GetStringMetadata("drop") == "1" {
				continue
			}
			// 使用过滤后的目标渠道列表
			targetChannels := info.channels
			if channels, ok := msg.GetMetadata("target_channels"); ok {
				if ch, ok := channels.([]string); ok {
					targetChannels = ch
				}
			}
			if err := e.router.DispatchNews(effectCtx, msg, targetChannels); err != nil {
				slog.Error("dispatch error", "error", err)
			}

			// 异步 AI 评估（不阻塞推送）
			if e.asyncEval != nil {
				e.asyncEval.Submit(msg)
			}

			time.Sleep(150 * time.Millisecond)
		}
	}

	return true
}

// GetNewsStore 获取新闻存储
func (e *NewsEngine) GetNewsStore() *store.NewsStore {
	return e.newsStore
}

// GetHealthMonitor 获取健康监控器
func (e *NewsEngine) GetHealthMonitor() *source.HealthMonitor {
	return e.healthMonitor
}

// SetHealthNotifier 设置健康通知器
func (e *NewsEngine) SetHealthNotifier(notifier source.HealthNotifier) {
	if e.healthMonitor != nil {
		e.healthMonitor.SetNotifier(notifier)
	}
}

// GetRetryQueueStats 获取补推队列统计
func (e *NewsEngine) GetRetryQueueStats() filter.RetryQueueStats {
	if e.retryQueue != nil {
		return e.retryQueue.GetStats()
	}
	return filter.RetryQueueStats{}
}

// GetDigestDeliverySnapshot exposes delivery health separately from fetch health.
func (e *NewsEngine) GetDigestDeliverySnapshot() DigestDeliverySnapshot {
	if e.router == nil {
		return DigestDeliverySnapshot{}
	}
	return e.router.DigestDeliverySnapshot()
}

func (e *NewsEngine) GetDigestAdminStore() digest.AdminStore {
	if e.router == nil {
		return nil
	}
	return e.router.DigestAdminStore()
}

func (e *NewsEngine) WakeDigestDelivery() {
	if e.router != nil {
		e.router.WakeDigestDelivery()
	}
}

// GetRatelimitStats 获取频控统计
func (e *NewsEngine) GetRatelimitStats() map[string]interface{} {
	if e.filters == nil {
		return nil
	}

	for _, f := range e.filters.GetFilters() {
		if rf, ok := f.(*filter.RatelimitFilter); ok {
			return rf.GetRatelimitStats().GetSummary()
		}
	}
	return nil
}

// GetSchedule 返回所有定时调度源的调度信息
func (e *NewsEngine) GetSchedule() []SourceSchedule {
	e.sourceMu.Lock()
	defer e.sourceMu.Unlock()
	var result []SourceSchedule
	for _, info := range e.sourceInfos {
		if len(info.schedule) == 0 {
			continue
		}
		src, ok := e.sources[info.name]
		if !ok {
			continue
		}
		result = append(result, SourceSchedule{
			Name:     info.name,
			Type:     src.Type(),
			Schedule: info.schedule,
		})
	}
	return result
}

// === 热更新支持 ===

// IsSourceRunning 检查数据源是否在运行
func (e *NewsEngine) IsSourceRunning(name string) bool {
	e.sourceMu.Lock()
	defer e.sourceMu.Unlock()
	_, running := e.sourceCancels[name]
	return running
}

// EnableSource 启用数据源（恢复 goroutine）
func (e *NewsEngine) EnableSource(name string) error {
	e.sourceMu.Lock()
	defer e.sourceMu.Unlock()

	src, exists := e.sources[name]
	if !exists {
		return fmt.Errorf("source %s not found", name)
	}

	if _, running := e.sourceCancels[name]; running {
		return nil
	}

	// 找到对应的 info
	for _, info := range e.sourceInfos {
		if info.name == name {
			e.startSourceGoroutine(src, info, e.sourceGenerations[name])
			slog.Info("source enabled", "name", name)
			return nil
		}
	}
	return fmt.Errorf("source info not found for %s", name)
}

// DisableSource 禁用数据源（停止 goroutine）
func (e *NewsEngine) DisableSource(name string) error {
	e.sourceMu.Lock()
	defer e.sourceMu.Unlock()

	cancel, running := e.sourceCancels[name]
	if !running {
		return nil
	}

	cancel()
	delete(e.sourceCancels, name)
	slog.Info("source disabled", "name", name)
	return nil
}

// AddSource 热添加新数据源
func (e *NewsEngine) AddSource(cfg SourceConfig) error {
	change, err := e.PrepareSourceChange(cfg)
	if err != nil {
		return err
	}
	if change.existed {
		change.Abort()
		return fmt.Errorf("%w: source %s already exists", ErrSourceConflict, cfg.Name)
	}
	return change.Activate()
}

func (e *NewsEngine) constructSource(cfg SourceConfig) (source.Source, error) {
	return e.constructSourceWithProvider(cfg, e.llmProvider)
}

func (e *NewsEngine) constructSourceWithProvider(cfg SourceConfig, provider llm.Provider) (source.Source, error) {
	src, err := source.Create(source.Config{
		Name:     cfg.Name,
		Type:     cfg.Type,
		URL:      cfg.URL,
		Interval: cfg.Interval,
		Sinks:    cfg.Channels,
		Options:  cfg.Options,
	})
	if err != nil {
		return nil, err
	}
	if e.newsStore != nil {
		if ns, ok := src.(interface{ SetNewsStore(*store.NewsStore) }); ok {
			ns.SetNewsStore(e.newsStore)
		}
	}
	if e.digestStore != nil {
		if ds, ok := src.(interface{ SetDigestStore(digest.Store) }); ok {
			ds.SetDigestStore(e.digestStore)
		}
	}
	if provider != nil {
		if settable, ok := src.(source.LLMSettable); ok {
			settable.SetLLMProvider(provider)
		}
	}
	return src, nil
}

func sourceInfoFromConfig(cfg SourceConfig) newsSourceInfo {
	return newsSourceInfo{
		name:            cfg.Name,
		interval:        cfg.Interval,
		schedule:        cfg.Schedule,
		channels:        cfg.Channels,
		aiEnhance:       cfg.AIEnhance,
		tradingDaysOnly: cfg.TradingDaysOnly,
		deliveryMode:    deliveryMode(cfg.DeliveryMode),
		briefingTarget:  digest.Briefing(cfg.BriefingTarget),
		routing:         cfg.Routing.DeepCopy(),
	}
}

// PrepareSourceChange constructs and injects a source without mutating runtime state.
func (e *NewsEngine) PrepareSourceChange(cfg SourceConfig) (*PreparedSourceChange, error) {
	if cfg.Name == "" || (sourceConfigEnabled(cfg) && !knownSourceType(cfg.Type)) {
		return nil, fmt.Errorf("%w: source name or type", ErrSourceValidation)
	}
	if cfg.Routing != nil {
		if _, ok := e.digestStore.(digest.RoutedStore); !ok {
			return nil, fmt.Errorf("%w: typed routing requires routed digest store", ErrSourceValidation)
		}
	}
	var src source.Source
	var err error
	if sourceConfigEnabled(cfg) {
		e.sourceMu.Lock()
		provider := e.llmProvider
		e.sourceMu.Unlock()
		src, err = e.constructSourceWithProvider(cfg, provider)
		if err != nil {
			return nil, fmt.Errorf("create source %s failed: %w", cfg.Name, err)
		}
	}
	e.sourceMu.Lock()
	if e.sourceGates == nil {
		e.sourceGates = make(map[string]*sourceOperationGate)
	}
	gate := e.sourceGates[cfg.Name]
	if gate == nil {
		gate = &sourceOperationGate{}
		if e.sources[cfg.Name] != nil {
			e.sourceGates[cfg.Name] = gate
		}
	}
	change := &PreparedSourceChange{
		engine: e, cfg: cfg, src: src, info: sourceInfoFromConfig(cfg),
		old: e.sources[cfg.Name], generation: e.sourceGenerations[cfg.Name],
		gate: gate,
	}
	change.existed = change.old != nil
	e.sourceMu.Unlock()
	return change, nil
}

func knownSourceType(sourceType string) bool {
	for _, registered := range source.ListTypes() {
		if registered == sourceType {
			return true
		}
	}
	return false
}

func (c *PreparedSourceChange) Activate() error {
	if c == nil || c.engine == nil || (sourceConfigEnabled(c.cfg) && c.src == nil) || c.activated {
		return fmt.Errorf("%w: invalid prepared source change", ErrSourceConflict)
	}
	e := c.engine
	e.sourceMu.Lock()
	current, exists := e.sources[c.cfg.Name]
	if exists != c.existed || current != c.old || e.sourceGenerations[c.cfg.Name] != c.generation {
		e.sourceMu.Unlock()
		return fmt.Errorf("%w: source %s", ErrSourceConflict, c.cfg.Name)
	}
	oldCancel := e.sourceCancels[c.cfg.Name]
	e.sourceMu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}

	c.gate.mu.Lock()
	e.sourceMu.Lock()
	current, exists = e.sources[c.cfg.Name]
	if exists != c.existed || current != c.old || e.sourceGenerations[c.cfg.Name] != c.generation {
		e.sourceMu.Unlock()
		c.gate.mu.Unlock()
		return fmt.Errorf("%w: source %s", ErrSourceConflict, c.cfg.Name)
	}
	oldCancel, oldRunning := e.sourceCancels[c.cfg.Name]
	oldDone := e.sourceDones[c.cfg.Name]
	e.sourceGenerations[c.cfg.Name] = c.generation + 1
	e.sourceGates[c.cfg.Name] = c.gate
	if sourceConfigEnabled(c.cfg) {
		e.sources[c.cfg.Name] = c.src
	} else {
		delete(e.sources, c.cfg.Name)
	}
	replaced := false
	for i := range e.sourceInfos {
		if e.sourceInfos[i].name == c.cfg.Name {
			if sourceConfigEnabled(c.cfg) {
				e.sourceInfos[i] = c.info
			} else {
				e.sourceInfos = append(e.sourceInfos[:i], e.sourceInfos[i+1:]...)
			}
			replaced = true
			break
		}
	}
	if !replaced && sourceConfigEnabled(c.cfg) {
		e.sourceInfos = append(e.sourceInfos, c.info)
	}
	if oldRunning {
		delete(e.sourceCancels, c.cfg.Name)
		delete(e.sourceDones, c.cfg.Name)
	}
	e.registerSourceHealth(c.cfg)
	if sourceConfigEnabled(c.cfg) && e.running && (!c.existed || oldRunning) {
		e.startSourceGoroutine(c.src, c.info, e.sourceGenerations[c.cfg.Name])
	}
	c.activated = true
	e.sourceMu.Unlock()
	c.gate.mu.Unlock()
	closeSourceAfter(c.old, oldDone)
	slog.Info("source activated", "name", c.cfg.Name)
	return nil
}

func (c *PreparedSourceChange) Abort() {
	if c == nil || c.activated || c.src == nil {
		return
	}
	closeSource(c.src)
	c.src = nil
}

func closeSource(src source.Source) {
	if closer, ok := src.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func closeSourceAfter(src source.Source, done <-chan struct{}) {
	if src == nil {
		return
	}
	if done == nil {
		closeSource(src)
		return
	}
	go func() {
		<-done
		closeSource(src)
	}()
}

func sourceConfigEnabled(cfg SourceConfig) bool {
	return cfg.Enabled == nil || *cfg.Enabled
}

func (e *NewsEngine) registerSourceHealth(cfg SourceConfig) {
	if e.healthMonitor == nil {
		return
	}
	if !sourceConfigEnabled(cfg) {
		e.healthMonitor.RegisterSource(cfg.Name)
		_ = e.healthMonitor.SetSourceStatus(cfg.Name, source.StatusDisabled)
		return
	}
	if len(cfg.Schedule) > 0 {
		e.healthMonitor.RegisterScheduledSource(cfg.Name)
		return
	}
	e.healthMonitor.RegisterSource(cfg.Name)
	health := e.healthMonitor.GetHealth(cfg.Name)
	if health != nil && (health.Status == source.StatusAwaitingSchedule || health.Status == source.StatusDisabled) {
		_ = e.healthMonitor.SetSourceStatus(cfg.Name, source.StatusHealthy)
	}
}

func (e *NewsEngine) isCurrentSource(name string, src source.Source, generation uint64) bool {
	e.sourceMu.Lock()
	defer e.sourceMu.Unlock()
	return e.sources[name] == src && e.sourceGenerations[name] == generation
}

func (e *NewsEngine) beginSourceEffects(name string, src source.Source, generation uint64) (func(), bool) {
	if generation == 0 {
		return func() {}, true
	}
	e.sourceMu.Lock()
	gate := e.sourceGates[name]
	if gate == nil {
		if e.sourceGates == nil {
			e.sourceGates = make(map[string]*sourceOperationGate)
		}
		gate = &sourceOperationGate{}
		e.sourceGates[name] = gate
	}
	e.sourceMu.Unlock()

	gate.mu.RLock()
	e.sourceMu.Lock()
	current := e.sources[name] == src && e.sourceGenerations[name] == generation
	e.sourceMu.Unlock()
	if !current {
		gate.mu.RUnlock()
		return func() {}, false
	}
	return gate.mu.RUnlock, true
}

func deliveryMode(mode string) string {
	if mode == "" {
		return "direct"
	}
	return mode
}

// RemoveSource 热移除数据源
func (e *NewsEngine) RemoveSource(name string) error {
	e.sourceMu.Lock()
	old := e.sources[name]
	generation := e.sourceGenerations[name]
	gate := e.sourceGates[name]
	if gate == nil {
		gate = &sourceOperationGate{}
	}
	cancel := e.sourceCancels[name]
	e.sourceMu.Unlock()
	if cancel != nil {
		cancel()
	}

	gate.mu.Lock()
	e.sourceMu.Lock()
	if e.sources[name] != old || e.sourceGenerations[name] != generation {
		e.sourceMu.Unlock()
		gate.mu.Unlock()
		return fmt.Errorf("%w: source %s", ErrSourceConflict, name)
	}
	delete(e.sourceCancels, name)
	done := e.sourceDones[name]
	delete(e.sourceDones, name)
	e.sourceGenerations[name]++
	if e.sourceGates == nil {
		e.sourceGates = make(map[string]*sourceOperationGate)
	}
	e.sourceGates[name] = gate
	delete(e.sources, name)

	// 从 sourceInfos 移除
	for i, info := range e.sourceInfos {
		if info.name == name {
			e.sourceInfos = append(e.sourceInfos[:i], e.sourceInfos[i+1:]...)
			break
		}
	}
	e.sourceMu.Unlock()
	gate.mu.Unlock()
	closeSourceAfter(old, done)

	slog.Info("source removed", "name", name)
	return nil
}

// ReloadSource 热重载数据源（停止旧→创建新→启动）
func (e *NewsEngine) ReloadSource(cfg SourceConfig) error {
	change, err := e.PrepareSourceChange(cfg)
	if err != nil {
		return err
	}
	if !change.existed {
		change.Abort()
		return fmt.Errorf("%w: source %s", ErrSourceNotFound, cfg.Name)
	}
	return change.Activate()
}

// startSourceGoroutine 为单个源启动调度 goroutine（需持有 sourceMu）
func (e *NewsEngine) startSourceGoroutine(src source.Source, info newsSourceInfo, generation uint64) {
	srcCtx, cancel := context.WithCancel(e.ctx)
	info.ctx = srcCtx
	if e.sourceDones == nil {
		e.sourceDones = make(map[string]chan struct{})
	}
	e.sourceCancels[info.name] = cancel
	done := make(chan struct{})
	e.sourceDones[info.name] = done

	e.wg.Add(1)
	if len(info.schedule) > 0 {
		go e.runScheduledSourceWithCtx(srcCtx, src, info, generation, done)
	} else {
		go e.runIntervalSourceWithCtx(srcCtx, src, info, generation, done)
	}
}
