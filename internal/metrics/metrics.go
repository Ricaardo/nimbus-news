package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// === 数据源指标 ===

	// SourceFetchTotal 源抓取总数
	SourceFetchTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "source",
			Name:      "fetch_total",
			Help:      "Total number of source fetches",
		},
		[]string{"source", "status"}, // status: success, error
	)

	// SourceFetchDuration 源抓取延迟
	SourceFetchDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "news",
			Subsystem: "source",
			Name:      "fetch_duration_seconds",
			Help:      "Duration of source fetches in seconds",
			Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
		},
		[]string{"source"},
	)

	// SourceMessagesTotal 源消息数量
	SourceMessagesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "source",
			Name:      "messages_total",
			Help:      "Total number of messages fetched from sources",
		},
		[]string{"source"},
	)

	// SourceHealthStatus 源健康状态
	SourceHealthStatus = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "source",
			Name:      "health_status",
			Help:      "Health status of source (0=healthy, 1=degraded, 2=unhealthy, 3=disabled)",
		},
		[]string{"source"},
	)

	// === 过滤器指标 ===

	// FilteredTotal 被过滤的消息总数
	FilteredTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "filter",
			Name:      "filtered_total",
			Help:      "Total number of filtered messages",
		},
		[]string{"filter", "source", "sink"},
	)

	// FilterPassedTotal 通过过滤的消息总数
	FilterPassedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "filter",
			Name:      "passed_total",
			Help:      "Total number of messages passed through filters",
		},
		[]string{"source", "sink"},
	)

	// === 推送指标 ===

	// PushTotal 推送总数
	PushTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "push",
			Name:      "total",
			Help:      "Total number of push attempts",
		},
		[]string{"channel", "status"}, // status: success, error
	)

	// PushDuration 推送延迟
	PushDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "news",
			Subsystem: "push",
			Name:      "duration_seconds",
			Help:      "Duration of message pushes in seconds",
			Buckets:   []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5},
		},
		[]string{"channel"},
	)

	// === LLM 指标 ===

	// LLMRequestTotal LLM 请求总数
	LLMRequestTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "llm",
			Name:      "request_total",
			Help:      "Total number of LLM API requests",
		},
		[]string{"model", "type", "status"}, // type: chat, tools, reason; status: success, error
	)

	// LLMRequestDuration LLM 请求延迟
	LLMRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "news",
			Subsystem: "llm",
			Name:      "request_duration_seconds",
			Help:      "Duration of LLM API requests in seconds",
			Buckets:   []float64{0.5, 1, 2, 5, 10, 30, 60, 120},
		},
		[]string{"model", "type"},
	)

	// LLMTokensTotal LLM Token 使用量
	LLMTokensTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "llm",
			Name:      "tokens_total",
			Help:      "Total number of LLM tokens used",
		},
		[]string{"model", "type"}, // type: prompt, completion
	)

	// AIEvalDuration AI 评分延迟分布
	AIEvalDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "news",
			Subsystem: "ai",
			Name:      "eval_duration_seconds",
			Help:      "Duration of AI evaluation calls in seconds",
			Buckets:   []float64{0.5, 1, 2, 5, 10, 30, 60},
		},
		[]string{"model"},
	)

	// === 存储指标 ===

	// NewsStoreSize 新闻存储大小
	NewsStoreSize = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "store",
			Name:      "news_size",
			Help:      "Number of news items in the store",
		},
	)

	// RetryQueueSize 补推队列大小
	RetryQueueSize = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "store",
			Name:      "retry_queue_size",
			Help:      "Number of items in the retry queue",
		},
	)

	DigestItems = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "digest",
			Name:      "items",
			Help:      "Number of digest items by durable state",
		},
		[]string{"state"},
	)

	DigestDeliveries = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "digest",
			Name:      "deliveries",
			Help:      "Number of digest deliveries by durable state",
		},
		[]string{"state"},
	)

	DigestOldestPendingAge = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "digest",
			Name:      "oldest_pending_age_seconds",
			Help:      "Age of the oldest pending digest item or delivery",
		},
	)

	DigestSinkPending = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "digest",
			Name:      "sink_pending",
			Help:      "Pending digest deliveries by bounded required sink",
		},
		[]string{"sink"},
	)

	DigestSinkOldestPendingAge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "digest",
			Name:      "sink_oldest_pending_age_seconds",
			Help:      "Age of the oldest pending digest delivery by bounded required sink",
		},
		[]string{"sink"},
	)

	DigestDeliverySinkAttempts = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "digest",
			Name:      "delivery_sink_attempts_total",
			Help:      "Digest delivery attempts by bounded sink name and result",
		},
		[]string{"sink", "result"},
	)

	DigestDeliveryFailures = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "digest",
			Name:      "delivery_failures_total",
			Help:      "Failed digest sink delivery attempts",
		},
	)

	DigestDeliveryRetries = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "digest",
			Name:      "delivery_retries_total",
			Help:      "Retried digest sink delivery attempts",
		},
	)

	DigestDeliveriesExpired = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "digest",
			Name:      "deliveries_expired_total",
			Help:      "Digest deliveries expired by the worker",
		},
	)

	// SessionCount 会话数量
	SessionCount = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "store",
			Name:      "session_count",
			Help:      "Number of active sessions",
		},
	)

	// === Agent 指标 ===

	// AgentRequestTotal Agent 请求总数
	AgentRequestTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "agent",
			Name:      "request_total",
			Help:      "Total number of agent requests",
		},
		[]string{"handler", "status"}, // handler: quote, news, analysis, etc.; status: success, error, fallback
	)

	// AgentRequestDuration Agent 请求延迟
	AgentRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "news",
			Subsystem: "agent",
			Name:      "request_duration_seconds",
			Help:      "Duration of agent request processing in seconds",
			Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 30},
		},
		[]string{"handler"},
	)

	// === 告警指标 ===

	// AlertTriggeredTotal 告警触发总数
	AlertTriggeredTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "alert",
			Name:      "triggered_total",
			Help:      "Total number of triggered alerts",
		},
		[]string{"type"}, // type: price, change, news
	)

	// ActiveAlertsGauge 活跃告警数量
	ActiveAlertsGauge = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "alert",
			Name:      "active_count",
			Help:      "Number of active alerts",
		},
	)

	// === 系统指标 ===

	// ChannelStatusGauge 渠道状态
	ChannelStatusGauge = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "channel",
			Name:      "status",
			Help:      "Status of channels (1=running, 0=stopped)",
		},
		[]string{"channel", "type"},
	)

	// UptimeGauge 运行时长
	UptimeGauge = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "system",
			Name:      "uptime_seconds",
			Help:      "System uptime in seconds",
		},
	)

	// === Discord 优化指标 ===

	// DiscordThreadPoolThreads Discord Thread Pool 中活跃线程数
	DiscordThreadPoolThreads = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "news",
			Subsystem: "discord",
			Name:      "threadpool_threads",
			Help:      "Number of active threads in Discord thread pool",
		},
		[]string{"source"},
	)

	// DiscordThreadDedupFiltered Discord Thread 内去重过滤数
	DiscordThreadDedupFiltered = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "discord",
			Name:      "thread_dedup_filtered_total",
			Help:      "Total number of messages filtered by thread dedup",
		},
		[]string{"source", "thread"},
	)

	// DiscordRatelimitBlocked Discord 频率限制拦截数
	DiscordRatelimitBlocked = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "discord",
			Name:      "ratelimit_blocked_total",
			Help:      "Total number of messages blocked by rate limiter",
		},
		[]string{"type"}, // type: thread, message
	)

	// DiscordRetryTotal Discord 重试总数
	DiscordRetryTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "discord",
			Name:      "retry_total",
			Help:      "Total number of Discord API retries",
		},
		[]string{"status"}, // status: success, failed
	)

	// === 聚合报告 / LLM 指标 ===

	// ReportScriptTotal 报告源里子脚本执行次数
	ReportScriptTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "report",
			Name:      "script_total",
			Help:      "Total number of python script executions in report sources",
		},
		[]string{"source", "script", "status"}, // status: success, error, timeout
	)

	// ReportScriptDuration 子脚本延迟
	ReportScriptDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "news",
			Subsystem: "report",
			Name:      "script_duration_seconds",
			Help:      "Duration of report source sub-scripts",
			Buckets:   []float64{1, 5, 10, 30, 60, 120, 180, 300},
		},
		[]string{"source", "script"},
	)

	// LLMSummarizeTotal LLM 摘要调用统计
	LLMSummarizeTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "news",
			Subsystem: "llm",
			Name:      "summarize_total",
			Help:      "Total number of LLM summarize calls in report sources",
		},
		[]string{"source", "result"}, // result: success, error, empty
	)

	// LLMSummarizeDuration LLM 调用耗时
	LLMSummarizeDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "news",
			Subsystem: "llm",
			Name:      "summarize_duration_seconds",
			Help:      "Duration of LLM summarize calls",
			Buckets:   []float64{1, 5, 10, 15, 20, 30, 45, 60},
		},
		[]string{"source"},
	)
)

// DigestSinkLabel bounds sink-cardinality even if configuration is malformed.
func DigestSinkLabel(sink string) string {
	switch sink {
	case "feishu-bot", "wechat-main", "discord-webhook":
		return sink
	default:
		return "other"
	}
}

// HealthStatusValue 将健康状态转换为数值
func HealthStatusValue(status string) float64 {
	switch status {
	case "healthy":
		return 0
	case "degraded":
		return 1
	case "unhealthy":
		return 2
	case "disabled":
		return 3
	case "awaiting_schedule":
		return 4
	default:
		return -1
	}
}
