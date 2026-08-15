package rest

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/model"

	"github.com/gin-gonic/gin"
)

func init() {
	channel.Register("rest", NewRestChannel)
}

// RestChannel REST API 渠道
type RestChannel struct {
	channel.BaseChannel
	port       int
	server     *http.Server
	router     *gin.Engine
	cancelFunc context.CancelFunc
	mu         sync.Mutex
}

// NewRestChannel 创建 REST API 渠道
func NewRestChannel(cfg channel.Config) (channel.Channel, error) {
	mode := cfg.Mode
	if mode == "" {
		mode = channel.ModeReceive
	}

	port := 8080
	if cfg.Options != nil {
		if p, ok := cfg.Options["port"].(float64); ok {
			port = int(p)
		}
		if p, ok := cfg.Options["port"].(int); ok {
			port = p
		}
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	return &RestChannel{
		BaseChannel: channel.NewBaseChannel(cfg.Name, "rest", mode),
		port:        port,
		router:      router,
	}, nil
}

// Start 启动渠道
func (r *RestChannel) Start(ctx context.Context) error {
	if !r.CanReceive() {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	r.cancelFunc = cancel

	r.setupRoutes()

	r.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", r.port),
		Handler: r.router,
	}

	go func() {
		slog.Info("REST channel", "r_name", r.Name(), "port", r.port)
		if err := r.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Warn("REST server error", "err", err)
		}
	}()

	return nil
}

// Stop 停止渠道
func (r *RestChannel) Stop() error {
	if r.cancelFunc != nil {
		r.cancelFunc()
	}
	if r.server != nil {
		return r.server.Shutdown(context.Background())
	}
	return nil
}

// Send REST 渠道不支持主动发送
func (r *RestChannel) Send(ctx context.Context, msg *model.Message) error {
	return fmt.Errorf("REST channel does not support sending")
}

// SendBatch REST 渠道不支持主动发送
func (r *RestChannel) SendBatch(ctx context.Context, msgs []*model.Message) error {
	return fmt.Errorf("REST channel does not support sending")
}

// Reply REST 渠道的回复通过 HTTP 响应返回
func (r *RestChannel) Reply(ctx context.Context, originalMsgID string, reply *model.Message) error {
	return nil
}

func (r *RestChannel) setupRoutes() {
	// 健康检查
	r.router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 聊天接口
	r.router.POST("/chat", func(c *gin.Context) {
		var req struct {
			Text   string `json:"text" binding:"required"`
			ChatID string `json:"chat_id"`
			UserID string `json:"user_id"`
		}

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		handler := r.GetHandler()
		if handler == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no message handler"})
			return
		}

		msg := model.NewChatMessage("rest", req.ChatID, req.UserID, req.Text)

		// 创建响应通道
		respChan := make(chan string, 1)
		msg.SetMetadata("response_chan", respChan)

		// 调用处理器
		handler(c.Request.Context(), msg)

		// 等待响应
		select {
		case response := <-respChan:
			c.JSON(http.StatusOK, gin.H{"response": response})
		case <-c.Request.Context().Done():
			c.JSON(http.StatusRequestTimeout, gin.H{"error": "timeout"})
		}
	})
}

// GetRouter 获取 gin 路由器（用于自定义路由）
func (r *RestChannel) GetRouter() *gin.Engine {
	return r.router
}
