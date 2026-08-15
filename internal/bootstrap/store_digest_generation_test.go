package bootstrap

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/config"
	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	bolt "go.etcd.io/bbolt"
)

func TestInitStoreContextNewsWriteDoesNotInvalidateDigestGeneration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stores, err := InitStoreContext(ctx, &config.PlatformConfig{
		Store: config.PlatformStoreConfig{
			Path: filepath.Join(t.TempDir(), "platform.db"),
			News: config.NewsStoreConfig{MaxItems: 10, TTL: 3600},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stores.Close()

	admin, ok := stores.Digest.(digest.AdminStore)
	if !ok {
		t.Fatalf("digest store does not expose admin stats: %T", stores.Digest)
	}
	if err := stores.Digest.Enqueue(
		context.Background(), "digest", digest.Closing, &model.Message{ID: "digest"},
	); err != nil {
		t.Fatal(err)
	}
	first, err := admin.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeCanonical, beforeIndexed := readDigestGenerations(t, stores)

	now := time.Now().UTC()
	if err := stores.News.Save(context.Background(), &model.News{
		ID: "news", Title: "unrelated", Source: "test", CreateTime: now, FetchTime: now,
	}); err != nil {
		t.Fatal(err)
	}
	afterCanonical, afterIndexed := readDigestGenerations(t, stores)
	if afterCanonical != beforeCanonical || afterIndexed != beforeIndexed ||
		afterIndexed != afterCanonical {
		t.Fatalf("digest generations changed after news write: before=%d/%d after=%d/%d",
			beforeCanonical, beforeIndexed, afterCanonical, afterIndexed)
	}
	second, err := admin.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("digest stats changed after unrelated news write: first=%+v second=%+v", first, second)
	}
}

func readDigestGenerations(t *testing.T, stores *Stores) (uint64, uint64) {
	t.Helper()
	var canonical uint64
	var indexed uint64
	if err := stores.DB.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte("digest_index_meta_v1"))
		if meta == nil {
			t.Fatal("digest metadata bucket missing")
		}
		canonicalRaw := meta.Get([]byte("canonical_generation"))
		indexedRaw := meta.Get([]byte("indexed_generation"))
		if len(canonicalRaw) != 8 || len(indexedRaw) != 8 {
			t.Fatalf("invalid digest generations: canonical=%x indexed=%x", canonicalRaw, indexedRaw)
		}
		canonical = binary.BigEndian.Uint64(canonicalRaw)
		indexed = binary.BigEndian.Uint64(indexedRaw)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return canonical, indexed
}
