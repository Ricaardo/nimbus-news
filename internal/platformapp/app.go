// Package platformapp owns construction and lifecycle of the news platform.
package platformapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/Ricaardo/nimbus-os/datasources/market"

	"github.com/Ricaardo/nimbus-os/news/internal/alert"
	"github.com/Ricaardo/nimbus-os/news/internal/api"
	"github.com/Ricaardo/nimbus-os/news/internal/bootstrap"
	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	_ "github.com/Ricaardo/nimbus-os/news/internal/channel/discord"
	_ "github.com/Ricaardo/nimbus-os/news/internal/channel/telegram"
	_ "github.com/Ricaardo/nimbus-os/news/internal/channel/wechat"
	_ "github.com/Ricaardo/nimbus-os/news/internal/channel/wxofficial"
	_ "github.com/Ricaardo/nimbus-os/news/internal/channel/wxpersonal"
	"github.com/Ricaardo/nimbus-os/news/internal/config"
	"github.com/Ricaardo/nimbus-os/news/internal/core"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/source"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

// Options contains the deployment locations for the news platform.
type Options struct {
	ConfigPath       string
	ListenAddr       string
	BootstrapClosing bool
}

type App struct {
	listener net.Listener
	server   *api.Server
	stores   *bootstrap.Stores
	marketDB *store.MarketDB
	router   *core.Router
	alert    *alert.Engine
	market   *market.CachedService
	channels *channel.Manager
	llm      llm.Provider
	options  Options

	runMu      sync.Mutex
	runStarted bool
	routerOn   bool
	alertOn    bool
	closeOnce  sync.Once
	closeErr   error
}

// New fully constructs the platform and binds its listener. A port conflict is
// returned synchronously and all resources acquired before it are released.
func New(options Options) (*App, error) {
	if strings.TrimSpace(options.ConfigPath) == "" {
		return nil, fmt.Errorf("platformapp: config path is required")
	}
	addr, err := normalizeListenAddr(options.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("platformapp: listen address: %w", err)
	}
	options.ListenAddr = addr

	cm, err := bootstrap.InitConfig(options.ConfigPath)
	if err != nil {
		return nil, err
	}
	cfg := cm.Get()

	stores, err := bootstrap.InitStoreContext(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("platformapp: init store: %w", err)
	}
	cleanup := func() { _ = stores.Close() }

	channels := bootstrap.InitChannels(cfg)
	rawLLM, enhancer := bootstrap.InitLLM(cfg)
	var llmProvider llm.Provider
	if rawLLM != nil {
		hotSwap := llm.NewHotSwapProvider(rawLLM)
		llmProvider = hotSwap
		source.SetGuanfuLLM(hotSwap)
	}
	marketService := bootstrap.InitMarket(cfg, llmProvider, stores.Quote)
	asyncHighScore := func(msg *model.Message) {
		if score, ok := msg.GetMetadata("ai_score"); ok {
			slog.Info("async eval high-score", "title", msg.Title, "score", score)
		}
	}
	filterChain, asyncEval := bootstrap.InitFilters(cfg, stores.Store, llmProvider, asyncHighScore)
	router, engine, err := bootstrap.InitRouter(cfg, channels, filterChain, asyncEval, stores.Store, stores.Digest, stores.News, stores.Quote, enhancer)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("platformapp: init router: %w", err)
	}
	if llmProvider != nil {
		engine.InjectLLM(llmProvider)
	}

	marketDB := bootstrap.InitMarketDB("data/market.db")
	alertEngine, err := bootstrap.InitAlert(cfg, stores.DB, marketService)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("platformapp: init alert: %w", err)
	}
	listener, err := net.Listen("tcp", options.ListenAddr)
	if err != nil {
		if marketDB != nil {
			_ = marketDB.Close()
		}
		cleanup()
		return nil, fmt.Errorf("platformapp: listen %q: %w", options.ListenAddr, err)
	}

	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	server := api.NewServer(cm, port)
	server.SetChannelManager(channels)
	server.SetNewsEngine(engine)

	if llmProvider != nil {
		cm.Subscribe(func(oldCfg, newCfg *config.PlatformConfig) {
			if oldCfg == nil || oldCfg.LLM == newCfg.LLM || newCfg.LLM.APIKey == "" {
				return
			}
			provider := llm.NewOpenAIProvider(llm.Config{
				Provider: newCfg.LLM.Provider, APIURL: newCfg.LLM.APIURL, APIKey: newCfg.LLM.APIKey,
				Model: newCfg.LLM.Model, HeavyModel: newCfg.LLM.HeavyModel, ThinkingMode: newCfg.LLM.ThinkingMode,
			})
			if hp, ok := llmProvider.(*llm.HotSwapProvider); ok {
				hp.Swap(provider)
			}
			if enhancer != nil {
				enhancer.SetEnhanceConfig(newCfg.LLM.Enhance.Enabled, newCfg.LLM.Enhance.MaxPerHour)
			}
		})
	}

	return &App{
		listener: listener, server: server, stores: stores, marketDB: marketDB,
		router: router, alert: alertEngine, market: marketService, channels: channels,
		llm: llmProvider, options: options,
	}, nil
}

func (a *App) Addr() string { return a.listener.Addr().String() }

// Run starts children and fails the whole application if the API listener
// fails. Cancellation drains resources in reverse construction order.
func (a *App) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("platformapp: run context is required")
	}
	a.runMu.Lock()
	if a.runStarted {
		a.runMu.Unlock()
		return fmt.Errorf("platformapp: app already run")
	}
	a.runStarted = true
	a.runMu.Unlock()

	if a.options.BootstrapClosing {
		source.BootstrapClosingScanDB(source.ClosingScanBootstrapOptions{})
	}
	a.routerOn = true
	if err := a.router.Start(ctx); err != nil {
		_ = a.Close()
		return fmt.Errorf("platformapp: start router: %w", err)
	}
	if a.alert != nil {
		if err := a.alert.Start(ctx); err != nil {
			_ = a.Close()
			return fmt.Errorf("platformapp: start alert: %w", err)
		}
		a.alertOn = true
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- a.server.Serve(a.listener) }()
	var err error
	select {
	case <-ctx.Done():
	case err = <-serveErr:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
	}
	return errors.Join(err, a.Close())
}

// Close is idempotent and releases resources in reverse order.
func (a *App) Close() error {
	a.closeOnce.Do(func() {
		var errs []error
		if a.server != nil {
			errs = append(errs, a.server.Stop())
		}
		if a.listener != nil {
			if err := a.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, err)
			}
		}
		if a.alertOn {
			a.alert.Stop()
		}
		if a.routerOn {
			a.router.Stop()
		}
		if a.marketDB != nil {
			errs = append(errs, a.marketDB.Close())
		}
		if a.stores != nil {
			errs = append(errs, a.stores.Close())
		}
		a.closeErr = errors.Join(errs...)
	})
	return a.closeErr
}

func normalizeListenAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("address is required")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("non-loopback or wildcard address %q is forbidden", addr)
	}
	return net.JoinHostPort(ip.String(), port), nil
}
