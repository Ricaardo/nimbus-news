//go:build !race

package store

import (
	"testing"

	legacybolt "github.com/boltdb/bolt"
	bbolt "go.etcd.io/bbolt"
)

// TestBboltReadsAndExtendsLegacyBoltFile is an executable file-format fixture:
// the first process owner is legacy Bolt v1.3.1, while every subsequent open,
// read and write is performed by the production bbolt driver.
func TestBboltReadsAndExtendsLegacyBoltFile(t *testing.T) {
	path := t.TempDir() + "/legacy.db"
	legacy, err := legacybolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Update(func(tx *legacybolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("compat"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("legacy"), []byte("v1.3.1"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	modern, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := modern.View(func(tx *bbolt.Tx) error {
		if got := string(tx.Bucket([]byte("compat")).Get([]byte("legacy"))); got != "v1.3.1" {
			t.Fatalf("legacy value=%q", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := modern.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte("compat")).Put([]byte("bbolt"), []byte("v1.4.3"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := modern.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket([]byte("compat"))
		if string(bucket.Get([]byte("legacy"))) != "v1.3.1" || string(bucket.Get([]byte("bbolt"))) != "v1.4.3" {
			t.Fatalf("reopened fixture lost values")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
