package bootstrap

import (
	"context"
	"log/slog"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/Ricaardo/nimbus-os/news/internal/config"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

// Stores holds all storage layer components.
type Stores struct {
	Store  store.Store
	DB     *bolt.DB
	News   *store.NewsStore
	Quote  *store.QuoteStore
	Digest digest.Store

	cancel      context.CancelFunc
	cleanupDone chan struct{}
	closeOnce   sync.Once
	closeErr    error
}

// InitConfig loads the platform configuration.
func InitConfig(configPath string) (*config.ConfigManager, error) {
	cm, err := config.NewConfigManager(configPath)
	if err != nil {
		return nil, err
	}
	cfg := cm.Get()
	slog.Info("config loaded", "channels", len(cfg.Channels), "sources", len(cfg.Sources))
	return cm, nil
}

// InitStore initializes all storage backends.
func InitStore(cfg *config.PlatformConfig) (*Stores, error) {
	return InitStoreContext(context.Background(), cfg)
}

// InitStoreContext initializes storage and ties maintenance work to ctx.
func InitStoreContext(ctx context.Context, cfg *config.PlatformConfig) (*Stores, error) {
	ctx, cancel := context.WithCancel(ctx)
	boltStore, err := store.NewBoltStore(cfg.Store.Path)
	if err != nil {
		cancel()
		return nil, err
	}
	digestStore, err := store.NewDigestStoreWithPriorities(boltStore.DB(), digestSourcePriorities(cfg))
	if err != nil {
		cancel()
		boltStore.Close()
		return nil, err
	}

	news, err := store.NewPersistentNewsStoreContext(ctx, boltStore.DB(), store.NewsStoreConfig{
		MaxItems: cfg.Store.News.MaxItems,
		TTL:      cfg.GetNewsTTL(),
	})
	if err != nil {
		cancel()
		boltStore.Close()
		return nil, err
	}

	// Periodically clean up expired dedup keys to prevent unbounded BoltDB growth.
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := boltStore.Cleanup(); err != nil {
					slog.Warn("boltdb cleanup error", "error", err)
				} else {
					slog.Debug("boltdb cleanup done")
				}
				if err := digestStore.Purge(ctx, time.Now().Add(-7*24*time.Hour), 10000); err != nil {
					slog.Warn("digest tombstone purge error", "error", err)
				}
				if err := digestStore.PurgeRouteAdmissions(ctx, 7*24*time.Hour); err != nil {
					slog.Warn("digest route admission purge error", "error", err)
				}
			}
		}
	}()

	quote, err := store.NewQuoteStore(boltStore.DB(), store.QuoteStoreConfig{MaxDays: 30})
	if err != nil {
		cancel()
		<-cleanupDone
		news.WaitCleanup()
		boltStore.Close()
		return nil, err
	}
	slog.Info("quote history store initialized")

	return &Stores{Store: boltStore, DB: boltStore.DB(), News: news, Quote: quote, Digest: digestStore, cancel: cancel, cleanupDone: cleanupDone}, nil
}

// Digest source priority is passed explicitly from validated routing config.
// Unknown and newly added sources start at neutral zero.
func digestSourcePriorities(cfg *config.PlatformConfig) map[string]int {
	priorities := make(map[string]int)
	if cfg == nil {
		return priorities
	}
	for _, source := range cfg.Sources {
		if source.Routing != nil && source.Routing.Digest != nil {
			priorities[source.Name] = source.Routing.Digest.Priority
		}
	}
	return priorities
}

// Close stops background maintenance before closing BoltDB. It is idempotent.
func (s *Stores) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.cleanupDone != nil {
			<-s.cleanupDone
		}
		if s.News != nil {
			s.News.WaitCleanup()
		}
		if s.Store != nil {
			s.closeErr = s.Store.Close()
		}
	})
	return s.closeErr
}

// InitMarketDB initializes the SQLite market database.
func InitMarketDB(path string) *store.MarketDB {
	db, err := store.NewMarketDB(path)
	if err != nil {
		slog.Warn("MarketDB not available", "error", err)
		return nil
	}
	slog.Info("MarketDB loaded", "symbols", db.SymbolCount())
	return db
}
