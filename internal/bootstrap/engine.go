package bootstrap

import (
	"fmt"
	"log/slog"

	"github.com/Ricaardo/nimbus-os/datasources/market"
	"github.com/Ricaardo/nimbus-os/news/internal/alert"
	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/config"
	"github.com/Ricaardo/nimbus-os/news/internal/core"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/filter"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/source"
	"github.com/Ricaardo/nimbus-os/news/internal/store"

	bolt "go.etcd.io/bbolt"
)

// InitRouter creates the router and news engine.
func InitRouter(
	cfg *config.PlatformConfig,
	channels *channel.Manager,
	filterChain *filter.Chain,
	asyncEval *filter.AsyncEvaluator,
	st store.Store,
	digestStore digest.Store,
	newsStore *store.NewsStore,
	quoteStore *store.QuoteStore,
	enhancer *llm.NewsEnhancer,
) (*core.Router, *core.NewsEngine, error) {

	router := core.NewRouter(channels)
	router.SetDigestStore(digestStore)

	sourceConfigs := buildSourceConfigs(cfg)
	healthConfig := source.HealthConfig{
		Enabled:            cfg.SourceHealth.Enabled,
		DegradedThreshold:  cfg.SourceHealth.DegradedThreshold,
		UnhealthyThreshold: cfg.SourceHealth.UnhealthyThreshold,
		RecoveryInterval:   cfg.GetRecoveryInterval(),
		RenotifyInterval:   cfg.GetRenotifyInterval(),
		NotifyChannels:     cfg.SourceHealth.NotifyChannels,
	}

	engine, err := core.NewNewsEngine(core.NewsEngineConfig{
		Sources:        sourceConfigs,
		FilterChain:    filterChain,
		Store:          st,
		DigestStore:    digestStore,
		NewsStore:      newsStore,
		QuoteStore:     quoteStore,
		Enhancer:       enhancer,
		AsyncEval:      asyncEval,
		Router:         router,
		HealthConfig:   healthConfig,
		MirrorChannels: cfg.MirrorChannels,
	})
	if err != nil {
		return nil, nil, err
	}
	router.SetNewsEngine(engine)

	// Health notifier
	if healthConfig.Enabled && len(healthConfig.NotifyChannels) > 0 {
		notifier := source.NewDefaultHealthNotifier(router, healthConfig.NotifyChannels)
		engine.SetHealthNotifier(notifier)
		slog.Info("health monitor enabled", "notify_channels", healthConfig.NotifyChannels)
	} else if healthConfig.Enabled {
		slog.Info("health monitor enabled", "notify_channels", "none")
	}

	return router, engine, nil
}

// InitAlert initializes the alert engine.
func InitAlert(cfg *config.PlatformConfig, db *bolt.DB, marketService *market.CachedService) (*alert.Engine, error) {
	var alertEngine *alert.Engine
	if cfg.Alert.Enabled {
		alertStore, err := alert.NewStore(db)
		if err != nil {
			return nil, fmt.Errorf("init alert store: %w", err)
		}
		alertEngine = alert.NewEngine(alertStore, alert.EngineConfig{
			CheckInterval:    cfg.GetAlertCheckInterval(),
			MaxAlertsPerUser: cfg.Alert.MaxAlertsPerUser,
			DefaultCooldown:  cfg.GetAlertDefaultCooldown(),
			Enabled:          true,
		})
		alertEngine.SetMarketProvider(marketService)
		slog.Info("alert engine initialized")
	}
	return alertEngine, nil
}

func buildSourceConfigs(cfg *config.PlatformConfig) []core.SourceConfig {
	var configs []core.SourceConfig
	for _, srcCfg := range cfg.Sources {
		if !srcCfg.IsEnabled() {
			slog.Info("source skipped", "name", srcCfg.Name, "type", srcCfg.Type)
			continue
		}

		aiEnhance := false
		if srcCfg.Options != nil {
			if v, ok := srcCfg.Options["ai_enhance"].(bool); ok {
				aiEnhance = v
			}
		}

		var schedule []string
		if srcCfg.Options != nil {
			if s, ok := srcCfg.Options["schedule"].([]interface{}); ok {
				for _, v := range s {
					if str, ok := v.(string); ok {
						schedule = append(schedule, str)
					}
				}
			}
		}
		if len(srcCfg.Schedule) > 0 {
			schedule = srcCfg.Schedule
		}

		configs = append(configs, core.SourceConfig{
			Name:            srcCfg.Name,
			Type:            srcCfg.Type,
			URL:             srcCfg.URL,
			Interval:        srcCfg.Interval,
			Schedule:        schedule,
			Channels:        srcCfg.Sinks,
			AIEnhance:       aiEnhance,
			TradingDaysOnly: srcCfg.TradingDaysOnly,
			DeliveryMode:    srcCfg.DeliveryMode,
			BriefingTarget:  srcCfg.BriefingTarget,
			Routing:         srcCfg.Routing.DeepCopy(),
			Options:         srcCfg.Options,
		})
	}
	return configs
}
