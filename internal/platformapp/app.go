// Package platformapp owns construction and lifecycle of the news platform.
package platformapp

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	_ "github.com/Ricaardo/nimbus-os/news/internal/channel/discord"
	_ "github.com/Ricaardo/nimbus-os/news/internal/channel/feishu"
	_ "github.com/Ricaardo/nimbus-os/news/internal/channel/filefeed"
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

// TraderOptions preserves the legacy command's trading flags without moving
// the trader implementation into the reusable application package.
type TraderOptions struct {
	Mode     string
	Schedule string
	Capital  float64
}

type TraderStarter func(context.Context, TraderOptions, market.Service, llm.Provider, *channel.Manager)

// Options contains all candidate deployment locations. Shadow candidates must
// provide isolated store and feed paths and can only construct filefeed output.
type Options struct {
	ConfigPath       string
	ListenAddr       string
	StorePath        string
	CandidateRoot    string
	Shadow           bool
	ShadowFeedPath   string
	SignalHandler    http.Handler
	WarehouseStatus  http.Handler
	Trading          TraderOptions
	StartTrader      TraderStarter
	BootstrapClosing bool
}

type App struct {
	listener net.Listener
	server   *api.Server
	stores   *bootstrap.Stores
	bolt     boltSnapshotter
	marketDB *store.MarketDB
	router   *core.Router
	alert    *alert.Engine
	market   *market.CachedService
	channels *channel.Manager
	feed     feedSnapshotter
	llm      llm.Provider
	options  Options

	runMu      sync.Mutex
	runStarted bool
	routerOn   bool
	alertOn    bool
	closeOnce  sync.Once
	closeErr   error
}

type SnapshotResult = store.SnapshotResult

type boltSnapshotter interface {
	Snapshot(context.Context, io.Writer) (store.SnapshotResult, error)
}

type feedSnapshotter interface {
	Snapshot(context.Context, io.Writer) (store.SnapshotResult, error)
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
	if options.Shadow {
		root, err := candidate.NewRoot(options.CandidateRoot)
		if err != nil {
			return nil, fmt.Errorf("platformapp: %w", err)
		}
		options.StorePath, err = root.RequireFile("shadow store path", options.StorePath)
		if err != nil {
			return nil, fmt.Errorf("platformapp: %w", err)
		}
		options.ShadowFeedPath, err = root.RequireFile("shadow feed path", options.ShadowFeedPath)
		if err != nil {
			return nil, fmt.Errorf("platformapp: %w", err)
		}
		if options.Trading.Mode != "" && options.Trading.Mode != "off" {
			return nil, fmt.Errorf("platformapp: trading is forbidden in shadow mode")
		}
	}

	cm, err := bootstrap.InitConfig(options.ConfigPath)
	if err != nil {
		return nil, err
	}
	cfg := cm.Get()
	if options.StorePath != "" {
		cfg.Store.Path = options.StorePath
	}
	if options.Shadow {
		applyShadowConfig(cfg, options.ShadowFeedPath)
	}

	stores, err := bootstrap.InitStoreContext(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("platformapp: init store: %w", err)
	}
	cleanup := func() { _ = stores.Close() }
	boltOwner, ok := stores.Store.(boltSnapshotter)
	if !ok {
		cleanup()
		return nil, fmt.Errorf("platformapp: bolt snapshot owner unavailable")
	}

	channels := bootstrap.InitChannels(cfg)
	var feedOwner feedSnapshotter
	fileFeeds := channels.GetByType("filefeed")
	if len(fileFeeds) == 1 {
		feedOwner, _ = fileFeeds[0].(feedSnapshotter)
	}
	if options.Shadow && feedOwner == nil {
		cleanup()
		return nil, fmt.Errorf("platformapp: exactly one required v1 filefeed snapshot owner is required")
	}
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

	var marketDB *store.MarketDB
	if !options.Shadow {
		marketDB = bootstrap.InitMarketDB("data/market.db")
	}
	alertEngine, _ := bootstrap.InitAlert(cfg, stores.DB, marketService)
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
	var server *api.Server
	if options.Shadow {
		server = api.NewReadOnlyServer(cm, port)
	} else {
		server = api.NewServer(cm, port)
	}
	server.SetChannelManager(channels)
	server.SetNewsEngine(engine)
	if options.SignalHandler != nil {
		server.Mount("/api/signals", options.SignalHandler)
	}
	if options.WarehouseStatus != nil {
		server.Mount("/api/candidate/warehouse", options.WarehouseStatus)
	}

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
		listener: listener, server: server, stores: stores, bolt: boltOwner, marketDB: marketDB,
		router: router, alert: alertEngine, market: marketService, channels: channels, feed: feedOwner,
		llm: llmProvider, options: options,
	}, nil
}

func (a *App) Addr() string { return a.listener.Addr().String() }

// SnapshotBolt delegates to the owner of the currently open Bolt database.
func (a *App) SnapshotBolt(ctx context.Context, w io.Writer) (SnapshotResult, error) {
	if a == nil || a.bolt == nil {
		return SnapshotResult{}, fmt.Errorf("platformapp: bolt snapshot owner unavailable")
	}
	return a.bolt.Snapshot(ctx, w)
}

// SnapshotFeed delegates to the required v1 filefeed owner retained at
// construction time, so runtime channel disablement cannot remove the seam.
func (a *App) SnapshotFeed(ctx context.Context, w io.Writer) (SnapshotResult, error) {
	if a == nil || a.feed == nil {
		return SnapshotResult{}, fmt.Errorf("platformapp: v1 filefeed snapshot owner unavailable")
	}
	return a.feed.Snapshot(ctx, w)
}

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

	if a.options.BootstrapClosing && !a.options.Shadow {
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
	if a.options.StartTrader != nil && a.options.Trading.Mode != "" && a.options.Trading.Mode != "off" {
		a.options.StartTrader(ctx, a.options.Trading, a.market, a.llm, a.channels)
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

func applyShadowConfig(cfg *config.PlatformConfig, feedPath string) {
	disabled := false
	enabled := true
	foundFileFeed := false
	for i := range cfg.Channels {
		ch := &cfg.Channels[i]
		if ch.Type != "filefeed" {
			ch.Enabled = &disabled
			continue
		}
		if foundFileFeed {
			ch.Enabled = &disabled
			continue
		}
		foundFileFeed = true
		ch.Enabled = &enabled
		ch.Mode = "push"
		ch.Webhook = ""
		ch.Options = map[string]interface{}{"path": feedPath, "v2_path": feedPath + ".v2", "max_lines": 2000}
	}
	if !foundFileFeed {
		cfg.Channels = append(cfg.Channels, config.ChannelConfig{
			Name: "candidate-filefeed", Type: "filefeed", Mode: "push", Enabled: &enabled,
			Options: map[string]interface{}{"path": feedPath, "v2_path": feedPath + ".v2", "max_lines": 2000},
		})
	}
	filefeedNames := make(map[string]struct{})
	for i := range cfg.Channels {
		if cfg.Channels[i].Type == "filefeed" && cfg.Channels[i].IsEnabled() {
			filefeedNames[cfg.Channels[i].Name] = struct{}{}
		}
	}
	for i := range cfg.Sources {
		var sinks []string
		for _, sink := range cfg.Sources[i].Sinks {
			if _, ok := filefeedNames[sink]; ok {
				sinks = append(sinks, sink)
			}
		}
		if len(sinks) == 0 {
			for name := range filefeedNames {
				sinks = append(sinks, name)
				break
			}
		}
		cfg.Sources[i].Sinks = sinks
	}
	cfg.MirrorChannels = nil
	cfg.SourceHealth.NotifyChannels = nil
	cfg.Alert.Enabled = false
	cfg.LLM.APIKey = ""
	cfg.LLM.Enhance.Enabled = false
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
