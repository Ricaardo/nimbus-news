package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestBoltSnapshotIsOpenableDuringConcurrentWrites(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "owner.db")
	owner, err := NewBoltStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := owner.Set("dedup", "before", time.Hour); err != nil {
		t.Fatal(err)
	}

	writeErr := make(chan error, 1)
	go func() {
		for i := 0; i < 100; i++ {
			if err := owner.Set("dedup", time.Now().Add(time.Duration(i)).String(), time.Hour); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	for i := 0; i < 4; i++ {
		var snapshot bytes.Buffer
		result, err := owner.Snapshot(context.Background(), &snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if result.Kind != BoltSnapshotKind || result.Size != int64(snapshot.Len()) {
			t.Fatalf("result=%+v bytes=%d", result, snapshot.Len())
		}
		sum := sha256.Sum256(snapshot.Bytes())
		if result.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("snapshot hash=%q", result.SHA256)
		}

		snapshotPath := filepath.Join(t.TempDir(), "snapshot.db")
		if err := os.WriteFile(snapshotPath, snapshot.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		copied, err := bolt.Open(snapshotPath, 0o600, &bolt.Options{ReadOnly: true})
		if err != nil {
			t.Fatalf("open snapshot: %v", err)
		}
		err = copied.View(func(tx *bolt.Tx) error {
			bucket := tx.Bucket([]byte("dedup"))
			if bucket == nil || bucket.Get([]byte("before")) == nil {
				return errors.New("snapshot missing committed value")
			}
			return nil
		})
		closeErr := copied.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
}

func TestBoltSnapshotCancellationWriterFailureAndRepeat(t *testing.T) {
	owner, err := NewBoltStore(filepath.Join(t.TempDir(), "owner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := owner.Set("dedup", "key", time.Hour); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := owner.Snapshot(ctx, &bytes.Buffer{}); !errors.Is(err, context.Canceled) || result != (SnapshotResult{}) {
		t.Fatalf("cancel result=%+v err=%v", result, err)
	}

	writeFailure := errors.New("write failed")
	if result, err := owner.Snapshot(context.Background(), failingWriter{err: writeFailure}); !errors.Is(err, writeFailure) || result != (SnapshotResult{}) {
		t.Fatalf("writer result=%+v err=%v", result, err)
	}

	var first, second bytes.Buffer
	firstResult, err := owner.Snapshot(context.Background(), &first)
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := owner.Snapshot(context.Background(), &second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) || firstResult != secondResult {
		t.Fatalf("consecutive snapshots differ: first=%+v second=%+v", firstResult, secondResult)
	}
}

func TestBoltSnapshotTransferBoundariesLeaveOwnerUsable(t *testing.T) {
	owner, err := NewBoltStore(filepath.Join(t.TempDir(), "owner.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := owner.Set("dedup", "key", time.Hour); err != nil {
		t.Fatal(err)
	}

	partialFailure := errors.New("partial write failed")
	tests := []struct {
		name   string
		writer func(context.CancelFunc) io.Writer
		want   error
	}{
		{
			name:   "short write",
			writer: func(context.CancelFunc) io.Writer { return shortWriter{} },
			want:   io.ErrShortWrite,
		},
		{
			name:   "partial write failure",
			writer: func(context.CancelFunc) io.Writer { return partialFailingWriter{err: partialFailure} },
			want:   partialFailure,
		},
		{
			name:   "transport cancellation",
			writer: func(cancel context.CancelFunc) io.Writer { return cancelingWriter{cancel: cancel} },
			want:   context.Canceled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result, err := owner.Snapshot(ctx, test.writer(cancel))
			if !errors.Is(err, test.want) || result != (SnapshotResult{}) {
				t.Fatalf("result=%+v err=%v want=%v", result, err, test.want)
			}

			var output bytes.Buffer
			next, err := owner.Snapshot(context.Background(), &output)
			if err != nil {
				t.Fatalf("owner unusable after failed transfer: %v", err)
			}
			if next.Kind != BoltSnapshotKind || next.Size != int64(output.Len()) || output.Len() == 0 {
				t.Fatalf("next result=%+v bytes=%d", next, output.Len())
			}
		})
	}
}

type failingWriter struct {
	err error
}

func (w failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

type partialFailingWriter struct {
	err error
}

func (w partialFailingWriter) Write(p []byte) (int, error) {
	n := len(p) / 2
	if n == 0 && len(p) > 0 {
		n = 1
	}
	return n, w.err
}

type cancelingWriter struct {
	cancel context.CancelFunc
}

func (w cancelingWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}
