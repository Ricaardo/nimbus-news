package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	types "github.com/Ricaardo/nimbus-os/datasources/market/types"
	"github.com/Ricaardo/nimbus-os/news/internal/model"

	bolt "go.etcd.io/bbolt"
)

// QuoteStore 行情历史数据存储
type QuoteStore struct {
	db      *bolt.DB
	mu      sync.RWMutex
	maxDays int // 保留天数
	bucket  string
}

// QuoteStoreConfig 行情存储配置
type QuoteStoreConfig struct {
	MaxDays int // 保留天数，默认 30 天
}

// QuoteRecord 行情记录（定义已随 market 包迁至 datasources；alias 保持兼容，
// 使 *QuoteStore 直接满足 market.QuoteStore 接口）
type QuoteRecord = types.QuoteRecord

// NewQuoteStore 创建行情存储
func NewQuoteStore(db *bolt.DB, cfg QuoteStoreConfig) (*QuoteStore, error) {
	if cfg.MaxDays <= 0 {
		cfg.MaxDays = 30
	}

	bucket := "quotes"

	// 创建 bucket
	err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucket))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create quotes bucket failed: %w", err)
	}

	return &QuoteStore{
		db:      db,
		maxDays: cfg.MaxDays,
		bucket:  bucket,
	}, nil
}

// Save 保存单条行情记录
func (s *QuoteStore) Save(ctx context.Context, quote *model.MarketQuote) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record := &QuoteRecord{
		Symbol:        quote.Symbol,
		Name:          quote.Name,
		Price:         quote.Price,
		Change:        quote.Change,
		ChangePercent: quote.ChangePercent,
		Volume:        quote.Volume,
		High:          quote.High,
		Low:           quote.Low,
		Open:          quote.Open,
		PrevClose:     quote.PrevClose,
		MarketCap:     quote.MarketCap,
		Currency:      quote.Currency,
		Exchange:      quote.Exchange,
		AssetType:     quote.AssetType,
		Timestamp:     quote.UpdateTime,
	}

	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}

	// 使用 symbol:timestamp 作为 key
	key := fmt.Sprintf("%s:%d", quote.Symbol, record.Timestamp.Unix())

	data, err := json.Marshal(record)
	if err != nil {
		return err
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(s.bucket))
		return b.Put([]byte(key), data)
	})
}

// SaveBatch 批量保存行情记录
func (s *QuoteStore) SaveBatch(ctx context.Context, quotes []*model.MarketQuote) error {
	for _, q := range quotes {
		if err := s.Save(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// GetHistory 获取指定标的的历史行情
func (s *QuoteStore) GetHistory(ctx context.Context, symbol string, days int) ([]*QuoteRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if days <= 0 {
		days = 7
	}

	cutoff := time.Now().AddDate(0, 0, -days)
	prefix := []byte(symbol + ":")

	var records []*QuoteRecord

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(s.bucket))
		c := b.Cursor()

		for k, v := c.Seek(prefix); k != nil && len(k) > len(prefix) && string(k[:len(prefix)]) == string(prefix); k, v = c.Next() {
			var record QuoteRecord
			if err := json.Unmarshal(v, &record); err != nil {
				continue
			}
			if record.Timestamp.After(cutoff) {
				records = append(records, &record)
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	// 按时间排序
	sort.Slice(records, func(i, j int) bool {
		return records[i].Timestamp.Before(records[j].Timestamp)
	})

	return records, nil
}

// GetLatest 获取指定标的的最新行情
func (s *QuoteStore) GetLatest(ctx context.Context, symbol string) (*QuoteRecord, error) {
	records, err := s.GetHistory(ctx, symbol, 1)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	return records[len(records)-1], nil
}

// GetAllSymbols 获取所有有记录的标的代码
func (s *QuoteStore) GetAllSymbols(ctx context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	symbolMap := make(map[string]bool)

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(s.bucket))
		c := b.Cursor()

		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			// 解析 key: symbol:timestamp
			key := string(k)
			for i := len(key) - 1; i >= 0; i-- {
				if key[i] == ':' {
					symbolMap[key[:i]] = true
					break
				}
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	symbols := make([]string, 0, len(symbolMap))
	for s := range symbolMap {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)

	return symbols, nil
}

// Cleanup 清理过期数据
func (s *QuoteStore) Cleanup(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().AddDate(0, 0, -s.maxDays)

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(s.bucket))
		c := b.Cursor()

		var keysToDelete [][]byte

		for k, v := c.First(); k != nil; k, v = c.Next() {
			var record QuoteRecord
			if err := json.Unmarshal(v, &record); err != nil {
				continue
			}
			if record.Timestamp.Before(cutoff) {
				keysToDelete = append(keysToDelete, append([]byte{}, k...))
			}
		}

		for _, key := range keysToDelete {
			if err := b.Delete(key); err != nil {
				return err
			}
		}

		if len(keysToDelete) > 0 {
			fmt.Printf("QuoteStore: cleaned up %d expired records\n", len(keysToDelete))
		}

		return nil
	})
}

// Count 获取记录数
func (s *QuoteStore) Count(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(s.bucket))
		count = b.Stats().KeyN
		return nil
	})

	return count, err
}
