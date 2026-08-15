package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Store 存储接口
type Store interface {
	// Exists 检查键是否存在
	Exists(bucket, key string) (bool, error)
	// Set 设置键值
	Set(bucket, key string, ttl time.Duration) error
	// Delete 删除键
	Delete(bucket, key string) error
	// Close 关闭存储
	Close() error
}

// BoltStore BoltDB 存储实现
type BoltStore struct {
	db *bolt.DB
}

const BoltSnapshotKind = "bolt"

var expiringBuckets = map[string]struct{}{
	"dedup":         {},
	"source_state":  {},
	"unified_dedup": {},
}

// SnapshotResult describes the exact bytes written by an owner-mediated snapshot.
type SnapshotResult struct {
	Kind   string
	Size   int64
	SHA256 string
}

// NewBoltStore 创建 BoltDB 存储
func NewBoltStore(path string) (*BoltStore, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 10 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("failed to open bolt db: %w", err)
	}
	return &BoltStore{db: db}, nil
}

// Exists 检查键是否存在
func (s *BoltStore) Exists(bucket, key string) (bool, error) {
	var exists bool
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		v := b.Get([]byte(key))
		if v != nil {
			// 检查是否过期
			expireTime := bytesToTime(v)
			if expireTime.After(time.Now()) {
				exists = true
			}
		}
		return nil
	})
	return exists, err
}

// Set 设置键值（带过期时间）
func (s *BoltStore) Set(bucket, key string, ttl time.Duration) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(bucket))
		if err != nil {
			return err
		}
		expireTime := time.Now().Add(ttl)
		return b.Put([]byte(key), timeToBytes(expireTime))
	})
}

// Delete 删除键
func (s *BoltStore) Delete(bucket, key string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(key))
	})
}

// Close 关闭存储
func (s *BoltStore) Close() error {
	return s.db.Close()
}

// DB 返回底层的 BoltDB 实例（用于 QuoteStore 等需要直接访问的场景）
func (s *BoltStore) DB() *bolt.DB {
	return s.db
}

// Snapshot writes a consistent Bolt database image from the currently open DB.
// It never reopens the database pathname or accepts a destination pathname.
func (s *BoltStore) Snapshot(ctx context.Context, w io.Writer) (SnapshotResult, error) {
	if ctx == nil {
		return SnapshotResult{}, fmt.Errorf("bolt snapshot: context is required")
	}
	if w == nil {
		return SnapshotResult{}, fmt.Errorf("bolt snapshot: writer is required")
	}
	if err := ctx.Err(); err != nil {
		return SnapshotResult{}, err
	}

	writer := &snapshotWriter{ctx: ctx, writer: w, hash: sha256.New()}
	err := s.db.View(func(tx *bolt.Tx) error {
		_, err := tx.WriteTo(writer)
		return err
	})
	if err != nil {
		if writer.err != nil {
			return SnapshotResult{}, fmt.Errorf("bolt snapshot: %w", writer.err)
		}
		return SnapshotResult{}, fmt.Errorf("bolt snapshot: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return SnapshotResult{}, err
	}
	return SnapshotResult{
		Kind:   BoltSnapshotKind,
		Size:   writer.size,
		SHA256: hex.EncodeToString(writer.hash.Sum(nil)),
	}, nil
}

type snapshotWriter struct {
	ctx    context.Context
	writer io.Writer
	hash   hash.Hash
	size   int64
	err    error
}

func (w *snapshotWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return 0, err
	}
	n, err := w.writer.Write(p)
	if n > 0 {
		_, _ = w.hash.Write(p[:n])
		w.size += int64(n)
	}
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

// Cleanup 清理过期数据
func (s *BoltStore) Cleanup() error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			if _, ok := expiringBuckets[string(name)]; !ok {
				return nil
			}
			var keysToDelete [][]byte
			c := b.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				expireTime := bytesToTime(v)
				if expireTime.Before(time.Now()) {
					keysToDelete = append(keysToDelete, k)
				}
			}
			for _, k := range keysToDelete {
				if err := b.Delete(k); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func timeToBytes(t time.Time) []byte {
	return []byte(t.Format(time.RFC3339))
}

func bytesToTime(b []byte) time.Time {
	t, err := time.Parse(time.RFC3339, string(b))
	if err != nil {
		// 如果解析失败，返回一个过去的时间，使该条目被视为已过期
		return time.Now().Add(-24 * time.Hour) // 返回24小时前的时间
	}
	return t
}

// MemoryStore 内存存储实现（用于测试）
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string]map[string]time.Time // bucket -> key -> expireTime
}

// NewMemoryStore 创建内存存储
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		data: make(map[string]map[string]time.Time),
	}
}

// Exists 检查键是否存在
func (s *MemoryStore) Exists(bucket, key string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if b, ok := s.data[bucket]; ok {
		if expireTime, ok := b[key]; ok {
			return expireTime.After(time.Now()), nil
		}
	}
	return false, nil
}

// Set 设置键值
func (s *MemoryStore) Set(bucket, key string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.data[bucket]; !ok {
		s.data[bucket] = make(map[string]time.Time)
	}
	s.data[bucket][key] = time.Now().Add(ttl)
	return nil
}

// Delete 删除键
func (s *MemoryStore) Delete(bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if b, ok := s.data[bucket]; ok {
		delete(b, key)
	}
	return nil
}

// Close 关闭存储
func (s *MemoryStore) Close() error {
	return nil
}

// GenerateKey 生成消息的唯一键
func GenerateKey(sourceName, sinkName, msgID, title string) string {
	data := fmt.Sprintf("%s|%s|%s|%s", sourceName, sinkName, msgID, title)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}
