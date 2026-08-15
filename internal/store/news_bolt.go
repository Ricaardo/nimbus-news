package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/model"
	bolt "go.etcd.io/bbolt"
)

var (
	newsItemsBucketV1  = []byte("news_items_v1")
	newsTimeBucketV1   = []byte("news_time_v1")
	newsSourceBucketV1 = []byte("news_source_v1")
	newsMetaBucketV1   = []byte("news_meta_v1")
	newsItemCountKey   = []byte("item_count")
)

// NewPersistentNewsStoreContext creates a write-through news store backed by
// the platform's owner-managed Bolt database. The in-memory ring remains the
// query path and is restored from canonical records during startup.
func NewPersistentNewsStoreContext(ctx context.Context, db *bolt.DB, cfg NewsStoreConfig) (*NewsStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("news store: context is required")
	}
	if db == nil {
		return nil, fmt.Errorf("news store: bolt database is required")
	}
	if cfg.MaxItems <= 0 || cfg.MaxItems > maxNewsStoreMaxItems {
		return nil, fmt.Errorf("news store: max_items must be between 1 and %d", maxNewsStoreMaxItems)
	}
	ns := newNewsStore(cfg)
	ns.db = db
	items, err := ns.rebuildPersistent(ctx)
	if err != nil {
		return nil, err
	}
	for _, news := range items {
		ns.recent = append(ns.recent, news)
		ns.newsMap[news.ID] = news
	}
	ns.startCleanup(ctx)
	return ns, nil
}

func (s *NewsStore) rebuildPersistent(ctx context.Context) ([]*model.News, error) {
	var retained []*model.News
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		items, err := tx.CreateBucketIfNotExists(newsItemsBucketV1)
		if err != nil {
			return err
		}
		if err := recreateBucket(tx, newsTimeBucketV1); err != nil {
			return err
		}
		if err := recreateBucket(tx, newsSourceBucketV1); err != nil {
			return err
		}
		if err := recreateBucket(tx, newsMetaBucketV1); err != nil {
			return err
		}

		cutoff := time.Now().Add(-s.ttl)
		err = items.ForEach(func(id, raw []byte) error {
			var news model.News
			if err := json.Unmarshal(raw, &news); err != nil {
				return fmt.Errorf("decode canonical news %q: %w", id, err)
			}
			if news.ID == "" || news.ID != string(id) {
				return fmt.Errorf("canonical news key/id mismatch for %q", id)
			}
			if !news.FetchTime.After(cutoff) {
				return nil
			}
			copy := news
			retained = append(retained, &copy)
			return nil
		})
		if err != nil {
			return err
		}
		sortNewsOldestFirst(retained)
		if len(retained) > s.maxItems {
			retained = retained[len(retained)-s.maxItems:]
		}

		keep := make(map[string]*model.News, len(retained))
		for _, news := range retained {
			keep[news.ID] = news
		}
		var remove [][]byte
		if err := items.ForEach(func(id, _ []byte) error {
			if keep[string(id)] == nil {
				remove = append(remove, append([]byte(nil), id...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, id := range remove {
			if err := items.Delete(id); err != nil {
				return err
			}
		}
		for _, news := range retained {
			if err := putNewsIndexes(tx, news); err != nil {
				return err
			}
		}
		if err := putNewsItemCount(tx, len(retained)); err != nil {
			return err
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("initialize persistent news store: %w", err)
	}
	return retained, nil
}

func recreateBucket(tx *bolt.Tx, name []byte) error {
	if tx.Bucket(name) != nil {
		if err := tx.DeleteBucket(name); err != nil {
			return err
		}
	}
	_, err := tx.CreateBucket(name)
	return err
}

func sortNewsOldestFirst(items []*model.News) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].FetchTime.Equal(items[j].FetchTime) {
			return items[i].ID < items[j].ID
		}
		return items[i].FetchTime.Before(items[j].FetchTime)
	})
}

func (s *NewsStore) savePersistent(ctx context.Context, news *model.News) (bool, map[string]struct{}, error) {
	if news == nil || news.ID == "" {
		return false, nil, fmt.Errorf("news store: news and id are required")
	}
	if ctx == nil {
		return false, nil, fmt.Errorf("news store: context is required")
	}
	if err := ctx.Err(); err != nil {
		return false, nil, err
	}
	raw, err := json.Marshal(news)
	if err != nil {
		return false, nil, fmt.Errorf("encode news %q: %w", news.ID, err)
	}

	retained := false
	deleted := make(map[string]struct{})
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		items := tx.Bucket(newsItemsBucketV1)
		if items == nil {
			return fmt.Errorf("canonical news bucket missing")
		}
		if items.Get([]byte(news.ID)) != nil {
			retained = true
			return nil
		}
		if err := items.Put([]byte(news.ID), raw); err != nil {
			return err
		}
		if err := putNewsIndexes(tx, news); err != nil {
			return err
		}
		itemCount, err := getNewsItemCount(tx)
		if err != nil {
			return err
		}
		if err := putNewsItemCount(tx, itemCount+1); err != nil {
			return err
		}
		removed, err := pruneNews(tx, time.Now().Add(-s.ttl), s.maxItems)
		if err != nil {
			return err
		}
		for _, id := range removed {
			deleted[id] = struct{}{}
		}
		_, retained = deleted[news.ID]
		retained = !retained
		return ctx.Err()
	})
	if err != nil {
		return false, nil, fmt.Errorf("persist news %q: %w", news.ID, err)
	}
	return retained, deleted, nil
}

func (s *NewsStore) purgePersistent(ctx context.Context) (map[string]struct{}, error) {
	if ctx == nil {
		return nil, fmt.Errorf("news store cleanup: context is required")
	}
	deleted := make(map[string]struct{})
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		removed, err := pruneNews(tx, time.Now().Add(-s.ttl), s.maxItems)
		if err != nil {
			return err
		}
		for _, id := range removed {
			deleted[id] = struct{}{}
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("cleanup persistent news: %w", err)
	}
	return deleted, nil
}

func pruneNews(tx *bolt.Tx, cutoff time.Time, maxItems int) ([]string, error) {
	items := tx.Bucket(newsItemsBucketV1)
	byTime := tx.Bucket(newsTimeBucketV1)
	if items == nil || byTime == nil || tx.Bucket(newsSourceBucketV1) == nil ||
		tx.Bucket(newsMetaBucketV1) == nil {
		return nil, fmt.Errorf("news store buckets missing")
	}
	itemCount, err := getNewsItemCount(tx)
	if err != nil {
		return nil, err
	}
	var deleted []string
	for {
		cursor := byTime.Cursor()
		key, id := cursor.First()
		if key == nil {
			if itemCount != 0 {
				return nil, fmt.Errorf("news time index empty with %d canonical records", itemCount)
			}
			break
		}
		fetchTime, err := newsTimeFromKey(key)
		if err != nil {
			return nil, err
		}
		if fetchTime.After(cutoff) && itemCount <= maxItems {
			break
		}
		record := items.Get(id)
		if record == nil {
			if err := byTime.Delete(key); err != nil {
				return nil, err
			}
			continue
		}
		var news model.News
		if err := json.Unmarshal(record, &news); err != nil {
			return nil, fmt.Errorf("decode canonical news %q: %w", id, err)
		}
		if err := deleteNewsRecord(tx, &news); err != nil {
			return nil, err
		}
		itemCount--
		deleted = append(deleted, news.ID)
	}
	if err := putNewsItemCount(tx, itemCount); err != nil {
		return nil, err
	}
	return deleted, nil
}

func getNewsItemCount(tx *bolt.Tx) (int, error) {
	meta := tx.Bucket(newsMetaBucketV1)
	if meta == nil {
		return 0, fmt.Errorf("news metadata bucket missing")
	}
	raw := meta.Get(newsItemCountKey)
	if len(raw) != 8 {
		return 0, fmt.Errorf("news item count metadata missing or invalid")
	}
	count := binary.BigEndian.Uint64(raw)
	if count > uint64(maxNewsStoreMaxItems)+1 {
		return 0, fmt.Errorf("news item count metadata out of range: %d", count)
	}
	return int(count), nil
}

func putNewsItemCount(tx *bolt.Tx, count int) error {
	if count < 0 {
		return fmt.Errorf("news item count cannot be negative")
	}
	meta := tx.Bucket(newsMetaBucketV1)
	if meta == nil {
		return fmt.Errorf("news metadata bucket missing")
	}
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(count))
	return meta.Put(newsItemCountKey, raw[:])
}

func putNewsIndexes(tx *bolt.Tx, news *model.News) error {
	byTime := tx.Bucket(newsTimeBucketV1)
	bySource := tx.Bucket(newsSourceBucketV1)
	if byTime == nil || bySource == nil {
		return fmt.Errorf("news indexes missing")
	}
	timeKey := newsTimeKey(news.FetchTime, news.ID)
	if err := byTime.Put(timeKey, []byte(news.ID)); err != nil {
		return err
	}
	return bySource.Put(newsSourceKey(news.Source, timeKey), []byte(news.ID))
}

func deleteNewsRecord(tx *bolt.Tx, news *model.News) error {
	timeKey := newsTimeKey(news.FetchTime, news.ID)
	if err := tx.Bucket(newsItemsBucketV1).Delete([]byte(news.ID)); err != nil {
		return err
	}
	if err := tx.Bucket(newsTimeBucketV1).Delete(timeKey); err != nil {
		return err
	}
	return tx.Bucket(newsSourceBucketV1).Delete(newsSourceKey(news.Source, timeKey))
}

func newsTimeKey(fetchTime time.Time, id string) []byte {
	key := make([]byte, 12+len(id))
	// Flip the seconds sign bit so big-endian byte ordering matches time
	// ordering, including zero/pre-epoch timestamps that must age out first.
	binary.BigEndian.PutUint64(key[:8], uint64(fetchTime.Unix())^(uint64(1)<<63))
	binary.BigEndian.PutUint32(key[8:12], uint32(fetchTime.Nanosecond()))
	copy(key[12:], id)
	return key
}

func newsTimeFromKey(key []byte) (time.Time, error) {
	if len(key) < 12 {
		return time.Time{}, fmt.Errorf("invalid news time index key")
	}
	seconds := int64(binary.BigEndian.Uint64(key[:8]) ^ (uint64(1) << 63))
	nanos := int64(binary.BigEndian.Uint32(key[8:12]))
	return time.Unix(seconds, nanos), nil
}

func newsSourceKey(source string, timeKey []byte) []byte {
	key := make([]byte, 4+len(source)+len(timeKey))
	binary.BigEndian.PutUint32(key[:4], uint32(len(source)))
	copy(key[4:], source)
	copy(key[4+len(source):], timeKey)
	return key
}
