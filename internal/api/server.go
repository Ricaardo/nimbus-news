package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/config"
	"github.com/Ricaardo/nimbus-os/news/internal/core"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/source"
)

// Server API 服务器
type Server struct {
	router           *gin.Engine
	server           *http.Server
	configManager    *config.ConfigManager
	channelManager   *channel.Manager
	newsEngine       *core.NewsEngine
	port             int
	sourceMutationMu sync.Mutex
	removeSource     func(string) error
}

const (
	digestDeliveryDegradedAge  = 12 * time.Hour
	digestDeliveryUnhealthyAge = 20 * time.Hour
	digestTerminalHealthWindow = 24 * time.Hour
)

// NewServer 创建 API 服务器
func NewServer(cfg *config.ConfigManager, port int) *Server {
	return newServer(cfg, port)
}

func newServer(cfg *config.ConfigManager, port int) *Server {
	gin.SetMode(gin.ReleaseMode)

	s := &Server{
		router:        gin.New(),
		configManager: cfg,
		port:          port,
	}

	s.router.Use(gin.Logger())
	s.router.Use(adminSecurity)
	s.router.Use(gin.Recovery())

	s.registerRoutes()

	return s
}

// SetChannelManager 设置渠道管理器
func (s *Server) SetChannelManager(m *channel.Manager) {
	s.channelManager = m
}

// SetNewsEngine 设置新闻引擎
func (s *Server) SetNewsEngine(e *core.NewsEngine) {
	s.newsEngine = e
}

// registerRoutes 注册所有路由
func (s *Server) registerRoutes() {
	// 公开端点（无需鉴权）
	s.router.GET("/api/health", s.healthCheck)
	s.router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	api := s.router.Group("/api")

	// 配置管理
	api.GET("/config", s.getConfig)
	api.PUT("/config", s.updateConfig)
	api.POST("/config/reload", s.reloadConfig)

	// LLM 管理
	api.GET("/llm", s.getLLMConfig)
	api.PUT("/llm", s.updateLLMConfig)
	api.POST("/llm/test", s.testLLMConnection)

	// 渠道管理
	api.GET("/channels", s.listChannels)
	api.POST("/channels", s.createChannel)
	api.GET("/channels/:name", s.getChannel)
	api.PUT("/channels/:name", s.updateChannel)
	api.POST("/channels/:name/toggle", s.toggleChannel)

	// 子系统配置
	api.GET("/config/filters", s.getFiltersConfig)
	api.PUT("/config/filters", s.updateFiltersConfig)
	api.GET("/config/alert", s.getAlertConfig)
	api.PUT("/config/alert", s.updateAlertConfig)
	api.GET("/config/health", s.getHealthConfig)
	api.PUT("/config/health", s.updateHealthConfig)

	// Discord 优化管理
	api.GET("/discord/status", s.getDiscordStatus)
	api.GET("/discord/threads", s.getDiscordThreads)
	api.POST("/discord/threads/cleanup", s.cleanupDiscordThreads)
	api.GET("/discord/dedup/stats", s.getDiscordDedupStats)

	// 数据源管理
	api.GET("/sources", s.listSources)
	api.GET("/schedule", s.getSchedule)
	api.POST("/sources", s.createSource)
	api.GET("/sources/:name", s.getSource)
	api.PUT("/sources/:name", s.updateSource)
	api.DELETE("/sources/:name", s.deleteSource)
	api.POST("/sources/:name/toggle", s.toggleSource)
	api.POST("/sources/:name/trigger", s.triggerSource)
	api.POST("/sources/:name/fetch", s.fetchSource)
	api.GET("/sources/types", s.getSourceTypes)

	// 数据源健康监控
	api.GET("/sources/health", s.getSourcesHealth)
	api.GET("/sources/:name/health", s.getSourceHealth)
	api.POST("/sources/:name/health/reset", s.resetSourceHealth)

	// API 密钥管理
	api.GET("/keys", s.listKeys)
	api.PUT("/keys", s.updateKey)
	api.GET("/macro/overview", s.getMacroOverview)
	api.GET("/macro/series", s.getMacroSeries)

	// 系统状态
	api.GET("/status", s.getStatus)

	// 调试：获取原始消息样例
	api.GET("/debug/messages", s.getDebugMessages)

	// 结构化查询：供下游(SDK bot / news-bridge)拉取任意/全部源的最近消息
	//   GET /api/news            → 全部源最近 limit 条
	//   GET /api/news?source=X   → 指定源最近 limit 条
	//   GET /api/news?limit=N    → 默认 50，最大 1000
	api.GET("/news", s.getNews)

	// 聚合报告历史
	api.GET("/reports", s.getReports)

	api.GET("/digest/stats", s.getDigestStats)
	api.GET("/digest/items", s.getDigestItems)
	api.GET("/digest/deliveries", s.getDigestDeliveries)
	api.POST("/digest/deliveries/:id/retry", s.retryDigestDelivery)
}

// Management endpoints have no stable authentication contract. Keep them local,
// allow trusted local browser origins, and preserve origin-less CLI access.
func adminSecurity(c *gin.Context) {
	admin := isAdminPath(c.Request.URL.Path)
	origin := c.GetHeader("Origin")
	if !admin {
		c.Header("Access-Control-Allow-Origin", "*")
		if c.Request.Method == http.MethodOptions {
			setCORSPreflightHeaders(c)
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
		return
	}

	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		host = c.Request.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		c.Header("Access-Control-Allow-Origin", "")
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "management API is local-only"})
		return
	}

	if origin != "" {
		if !trustedLocalOrigin(origin) {
			c.Header("Access-Control-Allow-Origin", "")
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "browser origin is not trusted"})
			return
		}
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Vary", "Origin")
	}
	if c.Request.Method == http.MethodOptions {
		setCORSPreflightHeaders(c)
		c.AbortWithStatus(http.StatusNoContent)
		return
	}

	writer := &sanitizingResponseWriter{ResponseWriter: c.Writer}
	c.Writer = writer
	defer writer.flush()
	c.Next()
}

func setCORSPreflightHeaders(c *gin.Context) {
	c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization")
	c.Header("Access-Control-Max-Age", strconv.Itoa(int((12 * time.Hour).Seconds())))
}

func isAdminPath(path string) bool {
	for _, prefix := range []string{
		"/api/config", "/api/llm", "/api/channels", "/api/sources",
		"/api/keys", "/api/discord", "/api/digest", "/api/debug",
		"/api/status", "/api/schedule",
	} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func trustedLocalOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type sanitizingResponseWriter struct {
	gin.ResponseWriter
	body   bytes.Buffer
	status int
}

func (w *sanitizingResponseWriter) WriteHeader(statusCode int) {
	if w.status == 0 {
		w.status = statusCode
	}
}

func (w *sanitizingResponseWriter) WriteHeaderNow() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
}

func (w *sanitizingResponseWriter) Write(data []byte) (int, error) {
	w.WriteHeaderNow()
	return w.body.Write(data)
}

func (w *sanitizingResponseWriter) WriteString(data string) (int, error) {
	w.WriteHeaderNow()
	return w.body.WriteString(data)
}

func (w *sanitizingResponseWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *sanitizingResponseWriter) Size() int {
	return w.body.Len()
}

func (w *sanitizingResponseWriter) Written() bool {
	return w.status != 0
}

func (w *sanitizingResponseWriter) flush() {
	body := w.body.Bytes()
	if strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		var value interface{}
		if json.Unmarshal(body, &value) == nil {
			redactResponseValue(value)
			if sanitized, err := json.Marshal(value); err == nil {
				body = sanitized
			}
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.ResponseWriter.WriteHeader(w.Status())
	_, _ = w.ResponseWriter.Write(body)
}

func redactResponseValue(value interface{}) {
	switch current := value.(type) {
	case map[string]interface{}:
		for key, nested := range current {
			if sensitiveResponseKey(key) {
				current[key] = "***"
				continue
			}
			redactResponseValue(nested)
		}
	case []interface{}:
		for _, nested := range current {
			redactResponseValue(nested)
		}
	}
}

func sensitiveResponseKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(lower, "secret") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "password") ||
		strings.Contains(lower, "authorization") ||
		lower == "webhook" ||
		lower == "webhooks" ||
		strings.HasPrefix(lower, "webhook_") ||
		strings.HasSuffix(lower, "_webhook") ||
		strings.Contains(lower, "api_key") ||
		strings.Contains(lower, "apikey") ||
		lower == "key" ||
		strings.HasSuffix(lower, "_key") ||
		strings.HasSuffix(lower, "-key")
}

// getNews 供下游拉取存储中的消息(全部源或按源过滤)。store 捕获所有源(与 sink 无关)。
func (s *Server) getNews(c *gin.Context) {
	if s.newsEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "news engine not available"})
		return
	}
	newsStore := s.newsEngine.GetNewsStore()
	if newsStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "news store not available"})
		return
	}

	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}

	source := c.Query("source")
	var messages []*model.News
	if source != "" {
		messages = newsStore.GetBySource(c.Request.Context(), source, limit)
	} else {
		messages = newsStore.GetRecent(c.Request.Context(), limit)
	}

	c.JSON(http.StatusOK, gin.H{
		"count":    len(messages),
		"source":   source,
		"messages": messages,
	})
}

// Start 启动 API 服务器
func (s *Server) Start() error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return err
	}

	go func() {
		fmt.Printf("API server started on port %d\n", s.port)
		if err := s.Serve(listener); err != nil && err != http.ErrServerClosed && !errors.Is(err, net.ErrClosed) {
			fmt.Printf("API server error: %v\n", err)
		}
	}()

	return nil
}

// Serve serves the API on a listener already bound by the application owner.
// Binding before bootstrap completes makes port conflicts a startup failure.
func (s *Server) Serve(listener net.Listener) error {
	if listener == nil {
		return fmt.Errorf("api: listener is required")
	}
	s.server = &http.Server{Addr: listener.Addr().String(), Handler: s.router}
	return s.server.Serve(listener)
}

// Mount exposes an additional handler below prefix on the same listener.
func (s *Server) Mount(prefix string, handler http.Handler) {
	s.router.Any(prefix+"/*path", gin.WrapH(http.StripPrefix(prefix, handler)))
}

// Stop 停止 API 服务器
func (s *Server) Stop() error {
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(ctx)
	}
	return nil
}

// GetRouter 获取 gin 路由器（用于共享端口）
func (s *Server) GetRouter() *gin.Engine {
	return s.router
}

// === 处理器实现 ===

func (s *Server) healthCheck(c *gin.Context) {
	status := "ok"
	httpStatus := http.StatusOK
	var delivery any = gin.H{"available": false}
	if s.newsEngine != nil {
		snapshot := s.newsEngine.GetDigestDeliverySnapshot()
		status, httpStatus, snapshot.HealthReason = digestDeliveryHealth(snapshot, time.Now())
		snapshot.HealthStatus = status
		delivery = snapshot
	}
	c.JSON(httpStatus, gin.H{
		"status":          status,
		"timestamp":       time.Now().Format(time.RFC3339),
		"digest_delivery": delivery,
	})
}

func digestDeliveryHealth(snapshot core.DigestDeliverySnapshot, now time.Time) (string, int, string) {
	if recentDigestTerminal(snapshot.LastDeadlineExpiry, now) {
		return "unhealthy", http.StatusServiceUnavailable, "delivery_deadline_expired"
	}
	if recentDigestTerminal(snapshot.LastMaxAttemptExpiry, now) {
		return "unhealthy", http.StatusServiceUnavailable, "delivery_max_attempts"
	}
	for _, sink := range snapshot.Sinks {
		if !sink.OldestPendingSince.IsZero() &&
			now.Sub(sink.OldestPendingSince) > digestDeliveryUnhealthyAge {
			return "unhealthy", http.StatusServiceUnavailable, "sink_pending_backlog"
		}
	}
	if snapshot.Available && !snapshot.Stats.OldestPendingDeliveryAt.IsZero() &&
		now.Sub(snapshot.Stats.OldestPendingDeliveryAt) > digestDeliveryUnhealthyAge {
		return "unhealthy", http.StatusServiceUnavailable, "pending_backlog"
	}
	if recentDigestTerminal(snapshot.LastItemExpiry, now) {
		return "degraded", http.StatusOK, "item_expired"
	}
	if snapshot.Available && (digestIntakeOlderThan(snapshot.Stats, now, digestDeliveryDegradedAge) ||
		snapshot.ConsecutiveFailures >= 3) {
		return "degraded", http.StatusOK, "pending_backlog"
	}
	for _, sink := range snapshot.Sinks {
		if (!sink.OldestPendingSince.IsZero() &&
			now.Sub(sink.OldestPendingSince) > digestDeliveryDegradedAge) ||
			sink.ConsecutiveFailures >= 3 {
			return "degraded", http.StatusOK, "sink_pending_backlog"
		}
	}
	return "ok", http.StatusOK, ""
}

func digestIntakeOlderThan(stats digest.Stats, now time.Time, age time.Duration) bool {
	for _, at := range []time.Time{stats.OldestPendingItemAt, stats.OldestLeasedItemAt} {
		if !at.IsZero() && now.Sub(at) > age {
			return true
		}
	}
	return false
}

func recentDigestTerminal(at, now time.Time) bool {
	return !at.IsZero() && !at.After(now) && now.Sub(at) <= digestTerminalHealthWindow
}

func (s *Server) digestAdmin(c *gin.Context) digest.AdminStore {
	if s.newsEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "digest admin unavailable"})
		return nil
	}
	admin := s.newsEngine.GetDigestAdminStore()
	if admin == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "digest admin unavailable"})
	}
	return admin
}

func digestLimit(c *gin.Context) (int, bool) {
	raw := c.Query("limit")
	if raw == "" {
		return 100, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 1000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 1000"})
		return 0, false
	}
	return limit, true
}

func (s *Server) getDigestStats(c *gin.Context) {
	admin := s.digestAdmin(c)
	if admin == nil {
		return
	}
	stats, err := admin.Stats(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "digest stats failed"})
		return
	}
	c.JSON(http.StatusOK, stats)
}

func (s *Server) getDigestItems(c *gin.Context) {
	briefing := digest.Briefing(c.Query("briefing"))
	if briefing != "" && !briefing.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid briefing"})
		return
	}
	state := digest.State(c.Query("state"))
	if state != "" && !state.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid item state"})
		return
	}
	limit, ok := digestLimit(c)
	if !ok {
		return
	}
	admin := s.digestAdmin(c)
	if admin == nil {
		return
	}
	items, err := admin.ListItems(c.Request.Context(), digest.ItemFilter{Briefing: briefing, State: state, Limit: limit})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "digest items failed"})
		return
	}
	for i := range items {
		sanitizeDigestItem(&items[i])
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func sanitizeDigestItem(item *digest.Item) {
	if item == nil {
		return
	}
	item.LeaseID = ""
	item.LeaseUntil = time.Time{}
	sanitizeDigestMessage(item.Message)
}

func (s *Server) getDigestDeliveries(c *gin.Context) {
	state := digest.DeliveryState(c.Query("state"))
	if state != "" && !state.Valid() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid delivery state"})
		return
	}
	sink := c.Query("sink")
	if sink != "" && !safeDigestIdentifier(sink) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid sink"})
		return
	}
	limit, ok := digestLimit(c)
	if !ok {
		return
	}
	admin := s.digestAdmin(c)
	if admin == nil {
		return
	}
	deliveries, err := admin.ListDeliveries(c.Request.Context(), digest.DeliveryFilter{State: state, Sink: sink, Limit: limit})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "digest deliveries failed"})
		return
	}
	for i := range deliveries {
		sanitizeDigestDelivery(&deliveries[i])
	}
	c.JSON(http.StatusOK, gin.H{"deliveries": deliveries})
}

func sanitizeDigestDelivery(delivery *digest.Delivery) {
	if delivery == nil {
		return
	}
	sanitizeDigestMessage(delivery.Message)
	for name, checkpoint := range delivery.Sinks {
		checkpoint.LastError = ""
		checkpoint.AttemptToken = ""
		checkpoint.ClaimedUntil = time.Time{}
		delivery.Sinks[name] = checkpoint
	}
}

func (s *Server) retryDigestDelivery(c *gin.Context) {
	id := c.Param("id")
	if !safeDigestIdentifier(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid delivery id"})
		return
	}
	admin := s.digestAdmin(c)
	if admin == nil {
		return
	}
	err := admin.RetryDelivery(c.Request.Context(), id)
	switch {
	case errors.Is(err, digest.ErrDeliveryNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "delivery not found"})
	case errors.Is(err, digest.ErrDeliveryTerminal):
		c.JSON(http.StatusConflict, gin.H{"error": "delivery is terminal"})
	case errors.Is(err, digest.ErrDeliveryClaimActive), errors.Is(err, digest.ErrDeliveryNotRetryable):
		c.JSON(http.StatusConflict, gin.H{"error": "delivery has nothing retryable"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "retry failed"})
	default:
		s.newsEngine.WakeDigestDelivery()
		c.JSON(http.StatusAccepted, gin.H{"status": "accepted"})
	}
}

func safeDigestIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') &&
			r != '-' && r != '_' && r != '.' && r != ':' {
			return false
		}
	}
	return true
}

func sanitizeDigestMessage(message *model.Message) {
	if message == nil {
		return
	}
	redactSensitiveMap(message.Metadata)
}

func redactSensitiveMap(values map[string]interface{}) {
	for key := range values {
		if sensitiveDigestKey(key) {
			delete(values, key)
			continue
		}
		switch nested := values[key].(type) {
		case map[string]interface{}:
			redactSensitiveMap(nested)
		case map[string]string:
			redactSensitiveStringMap(nested)
		case []interface{}:
			redactSensitiveSlice(nested)
		case []map[string]interface{}:
			for _, item := range nested {
				redactSensitiveMap(item)
			}
		case []map[string]string:
			for _, item := range nested {
				redactSensitiveStringMap(item)
			}
		}
	}
}

func redactSensitiveSlice(values []interface{}) {
	for _, value := range values {
		switch nested := value.(type) {
		case map[string]interface{}:
			redactSensitiveMap(nested)
		case map[string]string:
			redactSensitiveStringMap(nested)
		case []interface{}:
			redactSensitiveSlice(nested)
		case []map[string]interface{}:
			for _, item := range nested {
				redactSensitiveMap(item)
			}
		case []map[string]string:
			for _, item := range nested {
				redactSensitiveStringMap(item)
			}
		}
	}
}

func redactSensitiveStringMap(values map[string]string) {
	for key := range values {
		if sensitiveDigestKey(key) {
			delete(values, key)
		}
	}
}

func sensitiveDigestKey(key string) bool {
	lower := strings.ToLower(key)
	return strings.Contains(lower, "secret") || strings.Contains(lower, "token") ||
		strings.Contains(lower, "password") || strings.Contains(lower, "key") ||
		strings.Contains(lower, "webhook") || strings.Contains(lower, "authorization")
}

func (s *Server) getConfig(c *gin.Context) {
	cfg := s.configManager.Get()
	c.JSON(http.StatusOK, gin.H{
		"config":       maskSensitive(cfg),
		"lastModified": s.configManager.LastModified(),
	})
}

func (s *Server) updateConfig(c *gin.Context) {
	s.sourceMutationMu.Lock()
	defer s.sourceMutationMu.Unlock()
	var cfg config.PlatformConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := config.NormalizePlatformConfig(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	current := s.configManager.Get()
	if current != nil && !reflect.DeepEqual(current.Sources, cfg.Sources) {
		c.JSON(http.StatusConflict, gin.H{"code": "restart_required", "error": "source changes require source endpoints"})
		return
	}

	if err := s.configManager.Update(&cfg); err != nil {
		status := http.StatusInternalServerError
		if config.IsValidationError(err) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      "config updated",
		"lastModified": s.configManager.LastModified(),
	})
}

func (s *Server) reloadConfig(c *gin.Context) {
	s.sourceMutationMu.Lock()
	defer s.sourceMutationMu.Unlock()
	preview, err := s.configManager.PreviewReload()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	current, revision, err := s.configManager.Snapshot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !reflect.DeepEqual(current.Sources, preview.Sources) {
		c.JSON(http.StatusConflict, gin.H{"code": "restart_required", "error": "source changes require restart"})
		return
	}
	if err := s.configManager.CommitPreview(preview, revision); err != nil {
		c.JSON(configErrorStatus(err), gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      "config reloaded",
		"lastModified": s.configManager.LastModified(),
	})
}

func (s *Server) listChannels(c *gin.Context) {
	cfg := s.configManager.Get()

	channels := make([]gin.H, 0, len(cfg.Channels))
	for _, ch := range cfg.Channels {
		status := "unknown"
		if s.channelManager != nil {
			if _, exists := s.channelManager.Get(ch.Name); exists {
				status = "running"
			} else if !ch.IsEnabled() {
				status = "disabled"
			} else {
				status = "stopped"
			}
		}

		channels = append(channels, gin.H{
			"name":    ch.Name,
			"type":    ch.Type,
			"mode":    ch.Mode,
			"enabled": ch.IsEnabled(),
			"status":  status,
		})
	}

	c.JSON(http.StatusOK, gin.H{"channels": channels})
}

func (s *Server) getChannel(c *gin.Context) {
	name := c.Param("name")
	chCfg, found := s.configManager.GetChannelConfig(name)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"channel": chCfg})
}

func (s *Server) updateChannel(c *gin.Context) {
	name := c.Param("name")

	var chCfg config.ChannelConfig
	if err := c.ShouldBindJSON(&chCfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 确保名称一致
	chCfg.Name = name

	if err := s.configManager.UpdateChannel(name, chCfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 通知渠道管理器更新
	if s.channelManager != nil {
		if chCfg.IsEnabled() {
			s.channelManager.EnableChannel(name)
		} else {
			s.channelManager.DisableChannel(name)
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "channel updated"})
}

func (s *Server) toggleChannel(c *gin.Context) {
	name := c.Param("name")

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.configManager.ToggleChannel(name, req.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 通知渠道管理器
	if s.channelManager != nil {
		if req.Enabled {
			s.channelManager.EnableChannel(name)
		} else {
			s.channelManager.DisableChannel(name)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("channel %s %s", name, map[bool]string{true: "enabled", false: "disabled"}[req.Enabled]),
	})
}

func (s *Server) listSources(c *gin.Context) {
	cfg := s.configManager.Get()

	sources := make([]gin.H, 0, len(cfg.Sources))
	for _, src := range cfg.Sources {
		status := "unknown"
		if s.newsEngine != nil {
			if s.newsEngine.IsSourceRunning(src.Name) {
				status = "running"
			} else if !src.IsEnabled() {
				status = "disabled"
			} else {
				status = "stopped"
			}
		}

		sources = append(sources, gin.H{
			"name":              src.Name,
			"type":              src.Type,
			"url":               src.URL,
			"interval":          src.Interval,
			"schedule":          src.Schedule,
			"enabled":           src.IsEnabled(),
			"trading_days_only": src.TradingDaysOnly,
			"status":            status,
			"sinks":             src.Sinks,
			"delivery_mode":     src.DeliveryMode,
			"briefing_target":   src.BriefingTarget,
			"routing":           src.Routing,
			"options":           src.Options,
		})
	}

	c.JSON(http.StatusOK, gin.H{"sources": sources})
}

func (s *Server) getSource(c *gin.Context) {
	name := c.Param("name")
	srcCfg, found := s.configManager.GetSourceConfig(name)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"source": srcCfg})
}

func configErrorStatus(err error) int {
	switch {
	case config.IsValidationError(err):
		return http.StatusBadRequest
	case config.IsNotFoundError(err):
		return http.StatusNotFound
	case config.IsConflictError(err):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) updateSource(c *gin.Context) {
	s.sourceMutationMu.Lock()
	defer s.sourceMutationMu.Unlock()
	name := c.Param("name")

	var srcCfg config.SourceConfig
	if err := c.ShouldBindJSON(&srcCfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 确保名称一致
	srcCfg.Name = name
	old, found := s.configManager.GetSourceConfig(name)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		return
	}
	if srcCfg.Enabled == nil {
		srcCfg.Enabled = old.Enabled
	}
	preserveRedactedSourceSecrets(&srcCfg, old)
	normalized, err := config.NormalizeSourceConfig(srcCfg)
	if err != nil {
		c.JSON(configErrorStatus(err), gin.H{"error": err.Error()})
		return
	}
	var prepared *core.PreparedSourceChange
	if s.newsEngine != nil {
		prepared, err = s.newsEngine.PrepareSourceChange(configToCoreSourceConfig(normalized))
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, core.ErrSourceValidation) {
				status = http.StatusBadRequest
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		defer prepared.Abort()
	}

	var activate func() error
	if prepared != nil {
		activate = prepared.Activate
	}
	if err := s.configManager.UpdateSourceWithRuntime(name, normalized, activate); err != nil {
		var runtimeErr *config.SourceRuntimeMutationError
		if errors.As(err, &runtimeErr) {
			if runtimeErr.RollbackErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"code":  "restart_required",
					"error": err.Error(),
				})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(configErrorStatus(err), gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "source updated"})
}

func preserveRedactedSourceSecrets(incoming *config.SourceConfig, existing *config.SourceConfig) {
	if incoming == nil || existing == nil {
		return
	}
	incoming.Options = preserveRedactedOptionMap(incoming.Options, existing.Options)
}

func preserveRedactedOptionMap(incoming, existing map[string]interface{}) map[string]interface{} {
	if incoming == nil {
		incoming = make(map[string]interface{})
	}
	for key, oldValue := range existing {
		newValue, present := incoming[key]
		if sensitiveResponseKey(key) && (!present || newValue == "***") {
			incoming[key] = oldValue
			continue
		}
		if !present {
			continue
		}
		oldMap, oldOK := oldValue.(map[string]interface{})
		newMap, newOK := newValue.(map[string]interface{})
		if oldOK && newOK {
			incoming[key] = preserveRedactedOptionMap(newMap, oldMap)
		}
	}
	return incoming
}

func (s *Server) toggleSource(c *gin.Context) {
	s.sourceMutationMu.Lock()
	defer s.sourceMutationMu.Unlock()
	name := c.Param("name")

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	old, found := s.configManager.GetSourceConfig(name)
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		return
	}
	target := *old
	target.Enabled = &req.Enabled
	var prepared *core.PreparedSourceChange
	if s.newsEngine != nil {
		var err error
		prepared, err = s.newsEngine.PrepareSourceChange(configToCoreSourceConfig(target))
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, core.ErrSourceValidation) {
				status = http.StatusBadRequest
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		defer prepared.Abort()
	}
	var activate func() error
	if prepared != nil {
		activate = prepared.Activate
	}
	if err := s.configManager.UpdateSourceWithRuntime(name, target, activate); err != nil {
		var runtimeErr *config.SourceRuntimeMutationError
		if errors.As(err, &runtimeErr) {
			if runtimeErr.RollbackErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"code":  "restart_required",
					"error": err.Error(),
				})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(configErrorStatus(err), gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("source %s %s", name, map[bool]string{true: "enabled", false: "disabled"}[req.Enabled]),
	})
}

func (s *Server) getStatus(c *gin.Context) {
	cfg := s.configManager.Get()

	status := gin.H{
		"server": gin.H{
			"name": cfg.Server.Name,
			"port": cfg.Server.Port,
		},
		"channels": len(cfg.Channels),
		"sources":  len(cfg.Sources),
		"config": gin.H{
			"path":         s.configManager.GetPath(),
			"lastModified": s.configManager.LastModified(),
		},
	}

	// 添加运行时统计
	if s.channelManager != nil {
		enabledChannels := 0
		for _, ch := range cfg.Channels {
			if ch.IsEnabled() {
				enabledChannels++
			}
		}
		status["enabledChannels"] = enabledChannels
	}

	if s.newsEngine != nil {
		enabledSources := 0
		for _, src := range cfg.Sources {
			if src.IsEnabled() {
				enabledSources++
			}
		}
		status["enabledSources"] = enabledSources
	}

	c.JSON(http.StatusOK, status)
}

// getDebugMessages 获取原始消息样例（调试用）
func (s *Server) getDebugMessages(c *gin.Context) {
	if s.newsEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "news engine not available"})
		return
	}

	// 从 NewsStore 获取最近的消息
	newsStore := s.newsEngine.GetNewsStore()
	if newsStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "news store not available"})
		return
	}

	// 获取最近的消息（News 结构已有 JSON tag）
	messages := newsStore.GetRecent(c.Request.Context(), 50)

	c.JSON(http.StatusOK, gin.H{
		"count":    len(messages),
		"messages": messages,
	})
}

// === 数据源健康监控 API ===

// getSourcesHealth 获取所有数据源的健康状态
func (s *Server) getSourcesHealth(c *gin.Context) {
	if s.newsEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "news engine not available"})
		return
	}

	healthMonitor := s.newsEngine.GetHealthMonitor()
	if healthMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "health monitor not enabled"})
		return
	}

	// 获取是否包含详情
	includeDetails := c.Query("details") == "true"

	summary := healthMonitor.GetSummary(includeDetails)

	c.JSON(http.StatusOK, gin.H{
		"summary": gin.H{
			"total":             summary.Total,
			"healthy":           summary.Healthy,
			"degraded":          summary.Degraded,
			"unhealthy":         summary.Unhealthy,
			"disabled":          summary.Disabled,
			"awaiting_schedule": summary.AwaitingSchedule,
			"avg_success_rate":  summary.AvgSuccess,
		},
		"sources": summary.Sources,
	})
}

// getSourceHealth 获取单个数据源的健康状态
func (s *Server) getSourceHealth(c *gin.Context) {
	name := c.Param("name")

	if s.newsEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "news engine not available"})
		return
	}

	healthMonitor := s.newsEngine.GetHealthMonitor()
	if healthMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "health monitor not enabled"})
		return
	}

	health := healthMonitor.GetHealth(name)
	if health == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "source not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"health": health})
}

// resetSourceHealth 重置数据源健康统计
func (s *Server) resetSourceHealth(c *gin.Context) {
	name := c.Param("name")

	if s.newsEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "news engine not available"})
		return
	}

	healthMonitor := s.newsEngine.GetHealthMonitor()
	if healthMonitor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "health monitor not enabled"})
		return
	}

	if err := healthMonitor.ResetSource(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("health stats for source %s reset", name)})
}

// === Discord Admin API ===

func (s *Server) getDiscordStatus(c *gin.Context) {
	if s.channelManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "channel manager not available"})
		return
	}

	ch, ok := s.channelManager.Get("discord-push")
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "discord-push channel not found"})
		return
	}

	type discordChannel interface {
		GetThreadPoolStatus() map[string]interface{}
	}

	resp := map[string]interface{}{
		"channel": ch.Name(),
		"type":    ch.Type(),
	}

	if dc, ok := ch.(discordChannel); ok {
		resp["threadPool"] = dc.GetThreadPoolStatus()
	}

	c.JSON(http.StatusOK, resp)
}

func (s *Server) getDiscordThreads(c *gin.Context) {
	if s.channelManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "channel manager not available"})
		return
	}

	ch, ok := s.channelManager.Get("discord-push")
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "discord-push channel not found"})
		return
	}

	type discordThreadGetter interface {
		GetAllThreads() map[string]interface{}
	}

	if dt, ok := ch.(discordThreadGetter); ok {
		c.JSON(http.StatusOK, gin.H{"threads": dt.GetAllThreads()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"threads": []interface{}{}})
}

func (s *Server) cleanupDiscordThreads(c *gin.Context) {
	if s.channelManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "channel manager not available"})
		return
	}

	ch, ok := s.channelManager.Get("discord-push")
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "discord-push channel not found"})
		return
	}

	type discordThreadCleaner interface {
		CleanupInactive() int
	}

	if dc, ok := ch.(discordThreadCleaner); ok {
		count := dc.CleanupInactive()
		c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("cleaned %d inactive threads", count)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "cleanup not supported"})
}

func (s *Server) getDiscordDedupStats(c *gin.Context) {
	if s.channelManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "channel manager not available"})
		return
	}

	ch, ok := s.channelManager.Get("discord-push")
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "discord-push channel not found"})
		return
	}

	type discordDedupStats interface {
		GetDedupStats() map[string]interface{}
	}

	if dd, ok := ch.(discordDedupStats); ok {
		c.JSON(http.StatusOK, dd.GetDedupStats())
		return
	}

	c.JSON(http.StatusOK, gin.H{"stats": "not available"})
}

// === 新增数据源 API ===

// createSource 新增数据源
func (s *Server) createSource(c *gin.Context) {
	s.sourceMutationMu.Lock()
	defer s.sourceMutationMu.Unlock()
	var srcCfg config.SourceConfig
	if err := c.ShouldBindJSON(&srcCfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if srcCfg.Name == "" || srcCfg.Type == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and type are required"})
		return
	}
	normalized, err := config.NormalizeSourceConfig(srcCfg)
	if err != nil {
		c.JSON(configErrorStatus(err), gin.H{"error": err.Error()})
		return
	}
	if _, exists := s.configManager.GetSourceConfig(normalized.Name); exists {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("source %s already exists", normalized.Name)})
		return
	}
	var prepared *core.PreparedSourceChange
	if s.newsEngine != nil {
		prepared, err = s.newsEngine.PrepareSourceChange(configToCoreSourceConfig(normalized))
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, core.ErrSourceValidation) {
				status = http.StatusBadRequest
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		defer prepared.Abort()
	}

	// 追加到配置
	if err := s.configManager.AddSource(normalized); err != nil {
		c.JSON(configErrorStatus(err), gin.H{"error": err.Error()})
		return
	}

	// 热加载到引擎
	if s.newsEngine != nil {
		if err := prepared.Activate(); err != nil {
			if rollbackErr := s.configManager.RemoveSource(normalized.Name); rollbackErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"code":  "restart_required",
					"error": errors.Join(err, fmt.Errorf("config rollback failed: %w", rollbackErr)).Error(),
				})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusCreated, gin.H{"message": "source created", "name": srcCfg.Name})
}

// deleteSource 删除数据源
func (s *Server) deleteSource(c *gin.Context) {
	s.sourceMutationMu.Lock()
	defer s.sourceMutationMu.Unlock()
	name := c.Param("name")
	remove := s.removeSource
	if remove == nil && s.newsEngine != nil {
		remove = s.newsEngine.RemoveSource
	}
	if err := s.configManager.RemoveSourceWithRuntime(name, remove); err != nil {
		var runtimeErr *config.SourceRuntimeMutationError
		if errors.As(err, &runtimeErr) {
			if runtimeErr.RollbackErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"code":  "restart_required",
					"error": err.Error(),
				})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(configErrorStatus(err), gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("source %s deleted", name)})
}

// getSourceTypes 获取已注册的 source type 列表
func (s *Server) getSourceTypes(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"types": source.ListTypes()})
}

// getSchedule 返回所有定时调度源的调度信息
func (s *Server) getSchedule(c *gin.Context) {
	if s.newsEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "news engine not available"})
		return
	}

	schedule := s.newsEngine.GetSchedule()
	c.JSON(http.StatusOK, gin.H{"sources": schedule})
}

// maskSensitive 脱敏配置中的敏感字段
func maskSensitive(cfg *config.PlatformConfig) *config.PlatformConfig {
	if cfg == nil {
		return nil
	}
	cp := *cfg

	// 脱敏 LLM API key
	if cp.LLM.APIKey != "" {
		cp.LLM.APIKey = maskString(cp.LLM.APIKey)
	}

	// 脱敏渠道中的敏感字段
	for i := range cp.Channels {
		if cp.Channels[i].Webhook != "" {
			cp.Channels[i].Webhook = maskString(cp.Channels[i].Webhook)
		}
		if cp.Channels[i].Options != nil {
			for _, key := range []string{"app_secret", "encrypt_key", "verification_token", "bot_token", "app_id"} {
				if v, ok := cp.Channels[i].Options[key].(string); ok && v != "" {
					cp.Channels[i].Options[key] = maskString(v)
				}
			}
		}
	}

	// 脱敏数据源中 options 包含的 api_key
	for i := range cp.Sources {
		if cp.Sources[i].Options != nil {
			if v, ok := cp.Sources[i].Options["api_key"].(string); ok && v != "" {
				cp.Sources[i].Options["api_key"] = maskString(v)
			}
		}
	}

	return &cp
}

func maskString(s string) string {
	if s == "" {
		return ""
	}
	// Do not expose prefixes or suffixes: values may come from expanded
	// environment variables and even partial disclosure is unnecessary here.
	return "***"
}

// getLLMConfig 获取 LLM 配置（脱敏处理后返回）
func (s *Server) getLLMConfig(c *gin.Context) {
	cfg := s.configManager.Get()
	cp := *cfg
	cp.LLM.APIKey = maskString(cp.LLM.APIKey)
	c.JSON(http.StatusOK, gin.H{"llm": cp.LLM})
}

// updateLLMConfig 更新 LLM 配置
func (s *Server) updateLLMConfig(c *gin.Context) {
	var llmCfg config.LLMConfig
	if err := c.ShouldBindJSON(&llmCfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.configManager.UpdateLLM(llmCfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "LLM config updated"})
}

// testLLMConnection 测试 LLM 连接
func (s *Server) testLLMConnection(c *gin.Context) {
	var req struct {
		Provider string `json:"provider"`
		APIURL   string `json:"api_url"`
		APIKey   string `json:"api_key"`
		Model    string `json:"model"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.APIURL == "" || req.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "api_url and api_key are required"})
		return
	}

	llmCfg := llm.Config{
		Provider: req.Provider,
		APIURL:   req.APIURL,
		APIKey:   req.APIKey,
		Model:    req.Model,
	}
	provider := llm.NewOpenAIProvider(llmCfg)
	ctx := c.Request.Context()

	msg, err := provider.ChatWithTools(ctx, []llm.Message{
		{Role: "user", Content: "Hi"},
	}, nil)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true, "response": msg.Content})
}

// === API 密钥管理 ===

type keyEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Value    string `json:"value"`
	Enabled  bool   `json:"enabled"`
}

// listKeys 聚合返回所有外部 API 密钥
func (s *Server) listKeys(c *gin.Context) {
	cfg := s.configManager.Get()
	keys := make([]keyEntry, 0)

	// LLM API key
	if cfg.LLM.APIKey != "" || cfg.LLM.APIURL != "" {
		keys = append(keys, keyEntry{
			ID:       "llm:api_key",
			Name:     "LLM API Key",
			Category: "LLM",
			Value:    maskString(cfg.LLM.APIKey),
			Enabled:  true,
		})
	}

	// Source API keys
	for _, src := range cfg.Sources {
		if v, ok := src.Options["api_key"].(string); ok && v != "" {
			keys = append(keys, keyEntry{
				ID:       fmt.Sprintf("source:%s:api_key", src.Name),
				Name:     fmt.Sprintf("%s API Key", src.Name),
				Category: "数据源",
				Value:    maskString(v),
				Enabled:  src.IsEnabled(),
			})
		}
	}

	// Channel webhooks and secret options
	sensitiveOpts := []string{"bot_token", "app_id", "app_secret", "encrypt_key", "verification_token"}
	for _, ch := range cfg.Channels {
		if ch.Webhook != "" {
			keys = append(keys, keyEntry{
				ID:       fmt.Sprintf("channel:%s:webhook", ch.Name),
				Name:     fmt.Sprintf("%s Webhook", ch.Name),
				Category: "渠道",
				Value:    maskString(ch.Webhook),
				Enabled:  ch.IsEnabled(),
			})
		}
		for _, key := range sensitiveOpts {
			if v, ok := ch.Options[key].(string); ok && v != "" {
				keys = append(keys, keyEntry{
					ID:       fmt.Sprintf("channel:%s:%s", ch.Name, key),
					Name:     fmt.Sprintf("%s %s", ch.Name, key),
					Category: "渠道",
					Value:    maskString(v),
					Enabled:  ch.IsEnabled(),
				})
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"keys": keys})
}

// updateKey 更新单个 API 密钥（id 通过请求体传入，避免 URL 路径中的冒号冲突）
func (s *Server) updateKey(c *gin.Context) {
	var req struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	if req.Value == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "value is required"})
		return
	}

	// 检测是否为脱敏值（未修改），拒绝更新
	if req.Value == maskString(req.Value) && len(req.Value) > 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "value appears to be masked, please provide the full key"})
		return
	}

	if err := s.configManager.UpdateKey(req.ID, req.Value); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "key updated"})
}

// === 渠道创建 ===

// createChannel 新增渠道
func (s *Server) createChannel(c *gin.Context) {
	var chCfg config.ChannelConfig
	if err := c.ShouldBindJSON(&chCfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if chCfg.Name == "" || chCfg.Type == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and type are required"})
		return
	}

	if err := s.configManager.AddChannel(chCfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 通知渠道管理器
	if s.channelManager != nil && chCfg.IsEnabled() {
		s.channelManager.EnableChannel(chCfg.Name)
	}

	c.JSON(http.StatusCreated, gin.H{"message": "channel created", "name": chCfg.Name})
}

// === 子系统配置 API ===

func (s *Server) getFiltersConfig(c *gin.Context) {
	cfg := s.configManager.Get()
	c.JSON(http.StatusOK, gin.H{"filters": cfg.Filters})
}

func (s *Server) updateFiltersConfig(c *gin.Context) {
	var filters config.FiltersConfig
	if err := c.ShouldBindJSON(&filters); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.configManager.UpdateFilters(filters); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "filters config updated"})
}

func (s *Server) getAlertConfig(c *gin.Context) {
	cfg := s.configManager.Get()
	c.JSON(http.StatusOK, gin.H{"alert": cfg.Alert})
}

func (s *Server) updateAlertConfig(c *gin.Context) {
	var alert config.AlertConfig
	if err := c.ShouldBindJSON(&alert); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.configManager.UpdateAlert(alert); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "alert config updated"})
}

func (s *Server) getHealthConfig(c *gin.Context) {
	cfg := s.configManager.Get()
	c.JSON(http.StatusOK, gin.H{"health": cfg.SourceHealth})
}

func (s *Server) updateHealthConfig(c *gin.Context) {
	var health config.SourceHealthConfig
	if err := c.ShouldBindJSON(&health); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := s.configManager.UpdateSourceHealth(health); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "health config updated"})
}

// configToCoreSourceConfig 将 API config 转为 engine config
func configToCoreSourceConfig(cfg config.SourceConfig) core.SourceConfig {
	return core.SourceConfig{
		Name:            cfg.Name,
		Type:            cfg.Type,
		URL:             cfg.URL,
		Interval:        cfg.Interval,
		Schedule:        cfg.Schedule,
		Channels:        cfg.Sinks,
		TradingDaysOnly: cfg.TradingDaysOnly,
		DeliveryMode:    cfg.DeliveryMode,
		BriefingTarget:  cfg.BriefingTarget,
		Routing:         cfg.Routing.DeepCopy(),
		Enabled:         cfg.Enabled,
		Options:         cfg.Options,
	}
}

// getReports 返回最近 N 条聚合报告
// GET /api/reports?type=us-macro-report&limit=7
// type 为空时返回所有报告类型
func (s *Server) getReports(c *gin.Context) {
	if s.newsEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "news engine not available"})
		return
	}
	newsStore := s.newsEngine.GetNewsStore()
	if newsStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "news store not available"})
		return
	}

	reportType := c.Query("type")
	limit := 20
	if l, err := strconv.Atoi(c.DefaultQuery("limit", "20")); err == nil && l > 0 && l <= 200 {
		limit = l
	}

	// 实际注册的报告源(2026-08-02:blockbeats-evening 并入 news-aggregate-*)
	reportSources := map[string]bool{
		"us-macro-report":       true,
		"guanfu-score":          true,
		"pre-market-briefing":   true,
		"closing-briefing":      true,
		"us-preview":            true,
		"news-aggregate-morning": true,
		"news-aggregate-evening": true,
	}

	// 如果指定了 type, 直接 GetBySource
	var results []map[string]interface{}
	if reportType != "" {
		if !reportSources[reportType] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid type, must be one of report sources"})
			return
		}
		list := newsStore.GetBySource(c.Request.Context(), reportType, limit)
		for _, n := range list {
			results = append(results, map[string]interface{}{
				"id":          n.ID,
				"source":      n.Source,
				"title":       n.Title,
				"content":     n.Content,
				"tags":        n.Tags,
				"create_time": n.CreateTime,
			})
		}
	} else {
		// 扫所有报告源, 按时间合并排序
		recent := newsStore.GetRecent(c.Request.Context(), 200)
		for _, n := range recent {
			if !reportSources[n.Source] {
				continue
			}
			results = append(results, map[string]interface{}{
				"id":          n.ID,
				"source":      n.Source,
				"title":       n.Title,
				"content":     n.Content,
				"tags":        n.Tags,
				"create_time": n.CreateTime,
			})
			if len(results) >= limit {
				break
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"count":   len(results),
		"reports": results,
	})
}

func (s *Server) triggerSource(c *gin.Context) {
	name := c.Param("name")
	if err := s.newsEngine.TriggerSource(name); err != nil {
		status := http.StatusNotFound
		if errors.Is(err, core.ErrEngineStopped) {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"triggered": name})
}

// fetchSource 主动拉取源(同步,不推送),返回拉到的消息供查看。
// 区别于 trigger(异步推送):fetch 只执行源 Fetch 并回传内容。
func (s *Server) fetchSource(c *gin.Context) {
	name := c.Param("name")
	if s.newsEngine == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "news engine not available"})
		return
	}
	// 慢源(简报类含 LLM 调用,us-preview 实测 ~150s)同步阻塞。
	// 兜底必须 > 源内最长超时(5min),否则每次拉取都会被截断——
	// 此处只防 handler 无限挂起(ctx 泄漏),正常超时由源内 4-5min 超时承担。
	fetchCtx, cancel := context.WithTimeout(c.Request.Context(), 330*time.Second)
	defer cancel()

	start := time.Now()
	messages, err := s.newsEngine.FetchOnly(fetchCtx, name)
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, core.ErrEngineStopped) {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	type messageView struct {
		Title          string `json:"title"`
		Content        string `json:"content"`
		ContentPreview bool   `json:"content_preview"`
		Link           string `json:"link"`
		Source         string `json:"source"`
		Type           string `json:"type"`
	}
	// 正文预览截断:链接正文源(fetchLinkedBody)单条可达数 KB,限制响应体积。
	const contentPreviewLimit = 4000
	views := make([]messageView, 0, len(messages))
	for _, m := range messages {
		view := messageView{
			Title:  m.Title,
			Content: m.Content,
			Link:    m.Link,
			Source:  m.Source,
			Type:    string(m.SourceType),
		}
		if runes := []rune(view.Content); len(runes) > contentPreviewLimit {
			view.Content = string(runes[:contentPreviewLimit]) + "…"
			view.ContentPreview = true
		}
		views = append(views, view)
	}
	c.JSON(http.StatusOK, gin.H{
		"source":     name,
		"count":      len(views),
		"latency_ms": time.Since(start).Milliseconds(),
		"messages":   views,
	})
}
