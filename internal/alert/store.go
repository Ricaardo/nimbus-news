package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	bolt "go.etcd.io/bbolt"
)

var alertBucket = []byte("alerts")

// Store 告警存储
type Store struct {
	db *bolt.DB
	mu sync.RWMutex
}

// NewStore 创建告警存储
func NewStore(db *bolt.DB) (*Store, error) {
	// 确保 bucket 存在
	err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(alertBucket)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create alert bucket failed: %w", err)
	}

	return &Store{db: db}, nil
}

// Save 保存告警
func (s *Store) Save(ctx context.Context, alert *Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(alertBucket)
		if b == nil {
			return fmt.Errorf("bucket not found")
		}

		data, err := json.Marshal(alert)
		if err != nil {
			return err
		}

		return b.Put([]byte(alert.ID), data)
	})
}

// Get 获取告警
func (s *Store) Get(ctx context.Context, id string) (*Alert, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var alert Alert
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(alertBucket)
		if b == nil {
			return fmt.Errorf("bucket not found")
		}

		data := b.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("alert not found")
		}

		return json.Unmarshal(data, &alert)
	})

	if err != nil {
		return nil, err
	}
	return &alert, nil
}

// Delete 删除告警
func (s *Store) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(alertBucket)
		if b == nil {
			return fmt.Errorf("bucket not found")
		}
		return b.Delete([]byte(id))
	})
}

// GetByUser 获取用户的所有告警
func (s *Store) GetByUser(ctx context.Context, userID string) ([]*Alert, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var alerts []*Alert
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(alertBucket)
		if b == nil {
			return nil
		}

		return b.ForEach(func(k, v []byte) error {
			var alert Alert
			if err := json.Unmarshal(v, &alert); err != nil {
				return nil // 跳过无效数据
			}
			if alert.UserID == userID {
				alerts = append(alerts, &alert)
			}
			return nil
		})
	})

	return alerts, err
}

// LoadAll 加载所有告警
func (s *Store) LoadAll(ctx context.Context) ([]*Alert, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var alerts []*Alert
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(alertBucket)
		if b == nil {
			return nil
		}

		return b.ForEach(func(k, v []byte) error {
			var alert Alert
			if err := json.Unmarshal(v, &alert); err != nil {
				return nil // 跳过无效数据
			}
			alerts = append(alerts, &alert)
			return nil
		})
	})

	return alerts, err
}

// Count 返回告警总数
func (s *Store) Count(ctx context.Context) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(alertBucket)
		if b == nil {
			return nil
		}
		count = b.Stats().KeyN
		return nil
	})

	return count
}

// CountByUser 返回用户告警数
func (s *Store) CountByUser(ctx context.Context, userID string) int {
	alerts, _ := s.GetByUser(ctx, userID)
	return len(alerts)
}
