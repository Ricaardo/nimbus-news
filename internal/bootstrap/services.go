package bootstrap

import (
	"context"
	"log/slog"
	"time"

	"github.com/Ricaardo/nimbus-os/datasources/market"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/config"
	"github.com/Ricaardo/nimbus-os/news/internal/llm"
	"github.com/Ricaardo/nimbus-os/news/internal/source"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

// InitChannels creates and returns the channel manager with all configured channels.
func InitChannels(cfg *config.PlatformConfig) *channel.Manager {
	mgr := channel.NewManager()
	for _, chCfg := range cfg.Channels {
		if !chCfg.IsEnabled() {
			slog.Info("channel skipped", "name", chCfg.Name, "type", chCfg.Type)
			continue
		}
		ch, err := channel.Create(channel.Config{
			Name:    chCfg.Name,
			Type:    chCfg.Type,
			Mode:    channel.Mode(chCfg.Mode),
			Webhook: chCfg.Webhook,
			Options: chCfg.Options,
		})
		if err != nil {
			slog.Error("failed to create channel", "channel", chCfg.Name, "error", err)
			continue
		}
		mgr.Add(ch)
		slog.Info("channel added", "name", chCfg.Name, "type", chCfg.Type)
	}
	return mgr
}

// InitLLM initializes the LLM provider and enhancer.
func InitLLM(cfg *config.PlatformConfig) (llm.Provider, *llm.NewsEnhancer) {
	if cfg.LLM.APIKey == "" {
		return nil, nil
	}

	provider := llm.NewOpenAIProvider(llm.Config{
		Provider:     cfg.LLM.Provider,
		APIURL:       cfg.LLM.APIURL,
		APIKey:       cfg.LLM.APIKey,
		Model:        cfg.LLM.Model,
		HeavyModel:   cfg.LLM.HeavyModel,
		ThinkingMode: cfg.LLM.ThinkingMode,
	})

	enhancer := llm.NewNewsEnhancer(provider, llm.EnhancerConfig{
		Enabled:    cfg.LLM.Enhance.Enabled,
		MaxPerHour: cfg.LLM.Enhance.MaxPerHour,
	})

	slog.Info("LLM initialized", "provider", cfg.LLM.Provider,
		"model", cfg.LLM.ModelName(), "heavy", cfg.LLM.ReasonerModel(),
		"thinking", cfg.LLM.ThinkingMode)

	source.SetGuanfuLLM(provider)
	slog.Info("Guanfu LLM configured")

	return provider, enhancer
}

// llmChatAdapter bridges the platform's llm.Provider onto the market
// package's minimal LLMProvider seam (message types differ, semantics don't).
type llmChatAdapter struct{ p llm.Provider }

func (a llmChatAdapter) Chat(ctx context.Context, messages []market.LLMMessage) (string, error) {
	msgs := make([]llm.Message, len(messages))
	for i, m := range messages {
		msgs[i] = llm.Message{Role: m.Role, Content: m.Content}
	}
	return a.p.Chat(ctx, msgs)
}

// InitMarket initializes the market data service.
func InitMarket(cfg *config.PlatformConfig, llmProvider llm.Provider, quoteStore *store.QuoteStore) *market.CachedService {
	var base *market.MarketService
	if llmProvider != nil {
		base = market.NewMarketServiceWithLLM(llmChatAdapter{p: llmProvider})
		slog.Info("market service initialized", "provider", "LLM")
	} else {
		base = market.NewMarketService()
		slog.Info("market service initialized", "provider", "none")
	}

	if cfg.Market.SmartCache.Enabled {
		smartCfg := &market.SmartCacheConfig{
			TradingTTL:    time.Duration(cfg.Market.SmartCache.TradingTTL) * time.Second,
			ClosedTTL:     time.Duration(cfg.Market.SmartCache.ClosedTTL) * time.Second,
			PreMarketTTL:  time.Duration(cfg.Market.SmartCache.PreMarketTTL) * time.Second,
			AfterHoursTTL: time.Duration(cfg.Market.SmartCache.AfterHoursTTL) * time.Second,
			CryptoTTL:     time.Duration(cfg.Market.SmartCache.CryptoTTL) * time.Second,
			CacheSize:     cfg.Market.SmartCache.CacheSize,
		}
		svc := market.NewCachedServiceWithConfig(base, quoteStore, &market.CachedServiceConfig{
			SmartCacheEnabled: true,
			SmartCacheConfig:  smartCfg,
		})
		slog.Info("market service with smart cache", "trading_ttl", cfg.Market.SmartCache.TradingTTL,
			"closed_ttl", cfg.Market.SmartCache.ClosedTTL, "crypto_ttl", cfg.Market.SmartCache.CryptoTTL)
		return svc
	}

	slog.Info("market service initialized", "cache", "default")
	return market.NewCachedService(base, quoteStore)
}
