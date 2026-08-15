package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	bolt "go.etcd.io/bbolt"
)

// DigestTopicMigrationReport proves that the canonical item set was conserved.
// BeforeHash and AfterHash cover the sorted IDs, not mutable record contents.
type DigestTopicMigrationReport struct {
	Mode          string `json:"mode"`
	Applied       bool   `json:"applied"`
	MarkerPresent bool   `json:"marker_present"`
	Scanned       int    `json:"scanned"`
	Updated       int    `json:"updated"`
	BeforeCount   int    `json:"before_count"`
	AfterCount    int    `json:"after_count"`
	BeforeHash    string `json:"before_hash"`
	AfterHash     string `json:"after_hash"`
	AlreadyMarked bool   `json:"already_marked"`
}

// PreviewDigestTopics reports the exact pending-item changes without mutating
// the database. It is safe to run against a read-only offline copy.
func PreviewDigestTopics(ctx context.Context, db *bolt.DB, priorities map[string]int) (DigestTopicMigrationReport, error) {
	return runDigestTopicMigration(ctx, db, priorities, false, "")
}

// ApplyDigestTopics upgrades pending canonical items after fencing the input
// against a hash produced by PreviewDigestTopics. Startup deliberately does not
// invoke this migration against the live database.
func ApplyDigestTopics(ctx context.Context, db *bolt.DB, priorities map[string]int, expectedBeforeHash string) (DigestTopicMigrationReport, error) {
	if expectedBeforeHash == "" {
		return DigestTopicMigrationReport{}, fmt.Errorf("digest topic migration: expected before hash is required")
	}
	return runDigestTopicMigration(ctx, db, priorities, true, expectedBeforeHash)
}

func runDigestTopicMigration(ctx context.Context, db *bolt.DB, priorities map[string]int, apply bool, expectedBeforeHash string) (DigestTopicMigrationReport, error) {
	var report DigestTopicMigrationReport
	if db == nil {
		return report, fmt.Errorf("digest topic migration: db is required")
	}
	report.Mode = "dry-run"
	if apply {
		report.Mode = "apply"
	}
	run := db.View
	if apply {
		run = db.Update
	}
	err := run(func(tx *bolt.Tx) error {
		if err := contextError(ctx); err != nil {
			return err
		}
		items := tx.Bucket([]byte(digestBucketV1))
		if items == nil {
			return fmt.Errorf("digest topic migration: item bucket is missing")
		}
		migrations := tx.Bucket([]byte(digestMigrationsBucket))
		if migrations != nil && migrations.Get([]byte(digestTopicsV4Migration)) != nil {
			report.MarkerPresent = true
			report.AlreadyMarked = true
		}
		var ids []string
		updates := make(map[string]digest.Item)
		if err := items.ForEach(func(key, raw []byte) error {
			if err := contextError(ctx); err != nil {
				return err
			}
			report.Scanned++
			var item digest.Item
			if err := json.Unmarshal(raw, &item); err != nil {
				return fmt.Errorf("digest topic migration: decode item %q: %w", key, err)
			}
			if item.ID == "" || item.ID != string(key) {
				return fmt.Errorf("digest topic migration: item %q has invalid identity", key)
			}
			ids = append(ids, item.ID)
			if report.AlreadyMarked || item.State != digest.Pending {
				return nil
			}
			priority := clampDigestPriority(priorities[item.Source])
			if item.Message != nil {
				if raw, ok := item.Message.GetMetadata("ai_score"); ok {
					if score, ok := numericScore(raw); ok {
						if mapped := clampDigestPriority(int(score*10) - 50); mapped > priority {
							priority = mapped
						}
					}
				}
			}
			topicKey := digest.TopicKey(item.Briefing, item.Message, item.CreatedAt)
			if item.SchemaVersion == digest.SchemaVersion && item.Priority == priority && item.TopicKey == topicKey {
				return nil
			}
			item.SchemaVersion = digest.SchemaVersion
			item.Priority = priority
			item.TopicKey = topicKey
			updates[string(key)] = item
			return nil
		}); err != nil {
			return err
		}
		report.BeforeCount = len(ids)
		report.BeforeHash = digestConservationHash(ids)
		report.AfterCount = report.BeforeCount
		report.AfterHash = report.BeforeHash
		report.Updated = len(updates)
		if apply && expectedBeforeHash != report.BeforeHash {
			return fmt.Errorf("digest topic migration: before hash mismatch")
		}
		if !apply || report.AlreadyMarked {
			return nil
		}
		for key, item := range updates {
			if err := putDigestItem(tx, []byte(key), item); err != nil {
				return err
			}
		}
		var afterIDs []string
		if err := items.ForEach(func(key, raw []byte) error {
			var item digest.Item
			if err := json.Unmarshal(raw, &item); err != nil {
				return fmt.Errorf("digest topic migration: verify item %q: %w", key, err)
			}
			afterIDs = append(afterIDs, item.ID)
			return nil
		}); err != nil {
			return err
		}
		report.AfterCount = len(afterIDs)
		report.AfterHash = digestConservationHash(afterIDs)
		if report.BeforeCount != report.AfterCount || report.BeforeHash != report.AfterHash {
			return fmt.Errorf("digest topic migration: conservation check failed")
		}
		report.Applied = true
		report.MarkerPresent = true
		migrations, err := tx.CreateBucketIfNotExists([]byte(digestMigrationsBucket))
		if err != nil {
			return err
		}
		marker, err := json.Marshal(report)
		if err != nil {
			return err
		}
		return migrations.Put([]byte(digestTopicsV4Migration), marker)
	})
	if err != nil {
		return DigestTopicMigrationReport{}, err
	}
	return report, nil
}

func digestConservationHash(ids []string) string {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	hash := sha256.New()
	for _, id := range ids {
		hash.Write([]byte(id))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
