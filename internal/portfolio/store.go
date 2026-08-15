package portfolio

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	bolt "go.etcd.io/bbolt"
)

var (
	positionBucket    = []byte("positions")
	transactionBucket = []byte("transactions")
)

// Store 投资组合存储
type Store struct {
	db *bolt.DB
	mu sync.RWMutex
}

// NewStore 创建投资组合存储
func NewStore(db *bolt.DB) (*Store, error) {
	// 确保 buckets 存在
	err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(positionBucket); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(transactionBucket); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create portfolio buckets failed: %w", err)
	}

	return &Store{db: db}, nil
}

// SavePosition 保存持仓
func (s *Store) SavePosition(ctx context.Context, pos *Position) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(positionBucket)
		if b == nil {
			return fmt.Errorf("bucket not found")
		}

		data, err := json.Marshal(pos)
		if err != nil {
			return err
		}

		// 使用 userID:symbol 作为 key，确保每个用户每个标的只有一条持仓
		key := fmt.Sprintf("%s:%s", pos.UserID, pos.Symbol)
		return b.Put([]byte(key), data)
	})
}

// GetPosition 获取持仓
func (s *Store) GetPosition(ctx context.Context, userID, symbol string) (*Position, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var pos Position
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(positionBucket)
		if b == nil {
			return fmt.Errorf("bucket not found")
		}

		key := fmt.Sprintf("%s:%s", userID, symbol)
		data := b.Get([]byte(key))
		if data == nil {
			return ErrPositionNotFound
		}

		return json.Unmarshal(data, &pos)
	})

	if err != nil {
		return nil, err
	}
	return &pos, nil
}

// DeletePosition 删除持仓
func (s *Store) DeletePosition(ctx context.Context, userID, symbol string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(positionBucket)
		if b == nil {
			return fmt.Errorf("bucket not found")
		}

		key := fmt.Sprintf("%s:%s", userID, symbol)
		return b.Delete([]byte(key))
	})
}

// GetUserPositions 获取用户所有持仓
func (s *Store) GetUserPositions(ctx context.Context, userID string) ([]*Position, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var positions []*Position
	prefix := userID + ":"

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(positionBucket)
		if b == nil {
			return nil
		}

		return b.ForEach(func(k, v []byte) error {
			key := string(k)
			if len(key) > len(prefix) && key[:len(prefix)] == prefix {
				var pos Position
				if err := json.Unmarshal(v, &pos); err != nil {
					return nil // 跳过无效数据
				}
				positions = append(positions, &pos)
			}
			return nil
		})
	})

	return positions, err
}

// SaveTransaction 保存交易记录
func (s *Store) SaveTransaction(ctx context.Context, tx *Transaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(btx *bolt.Tx) error {
		b := btx.Bucket(transactionBucket)
		if b == nil {
			return fmt.Errorf("bucket not found")
		}

		data, err := json.Marshal(tx)
		if err != nil {
			return err
		}

		// 使用 userID:timestamp:id 作为 key
		key := fmt.Sprintf("%s:%d:%s", tx.UserID, tx.CreatedAt.Unix(), tx.ID)
		return b.Put([]byte(key), data)
	})
}

// GetUserTransactions 获取用户交易记录
func (s *Store) GetUserTransactions(ctx context.Context, userID string, limit int) ([]*Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var transactions []*Transaction
	prefix := userID + ":"

	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(transactionBucket)
		if b == nil {
			return nil
		}

		c := b.Cursor()
		// 从后往前遍历获取最新记录
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			key := string(k)
			if len(key) > len(prefix) && key[:len(prefix)] == prefix {
				var txn Transaction
				if err := json.Unmarshal(v, &txn); err != nil {
					continue
				}
				transactions = append(transactions, &txn)
				if limit > 0 && len(transactions) >= limit {
					break
				}
			}
		}
		return nil
	})

	return transactions, err
}

// GetSymbolTransactions 获取指定标的的交易记录
func (s *Store) GetSymbolTransactions(ctx context.Context, userID, symbol string, limit int) ([]*Transaction, error) {
	allTx, err := s.GetUserTransactions(ctx, userID, 0)
	if err != nil {
		return nil, err
	}

	var filtered []*Transaction
	for _, tx := range allTx {
		if tx.Symbol == symbol {
			filtered = append(filtered, tx)
			if limit > 0 && len(filtered) >= limit {
				break
			}
		}
	}

	return filtered, nil
}

// Count 返回持仓总数
func (s *Store) Count(ctx context.Context) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(positionBucket)
		if b == nil {
			return nil
		}
		count = b.Stats().KeyN
		return nil
	})

	return count
}

// CountByUser 返回用户持仓数
func (s *Store) CountByUser(ctx context.Context, userID string) int {
	positions, _ := s.GetUserPositions(ctx, userID)
	return len(positions)
}
