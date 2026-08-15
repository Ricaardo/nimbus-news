package signal

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

func TestJSONLStoreSnapshotDuringConcurrentAppend(t *testing.T) {
	owner, err := NewJSONLStore(filepath.Join(t.TempDir(), "signals.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Append(context.Background(), snapshotEvent(0)); err != nil {
		t.Fatal(err)
	}

	writeErr := make(chan error, 1)
	go func() {
		for i := 1; i <= 100; i++ {
			if err := owner.Append(context.Background(), snapshotEvent(i)); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	for i := 0; i < 8; i++ {
		var output bytes.Buffer
		result, err := owner.Snapshot(context.Background(), &output)
		if err != nil {
			t.Fatal(err)
		}
		if result.Kind != SnapshotKind || result.Size != int64(output.Len()) {
			t.Fatalf("result=%+v bytes=%d", result, output.Len())
		}
		sum := sha256.Sum256(output.Bytes())
		if result.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("snapshot hash=%q", result.SHA256)
		}
		assertSignalSnapshot(t, output.Bytes())
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
}

func TestJSONLStoreSnapshotStrictMissingAndRepeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signals.jsonl")
	owner, err := NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var missing bytes.Buffer
	result, err := owner.Snapshot(context.Background(), &missing)
	if err != nil {
		t.Fatal(err)
	}
	emptyHash := sha256.Sum256(nil)
	if result.Kind != SnapshotKind || result.Size != 0 || result.SHA256 != hex.EncodeToString(emptyHash[:]) {
		t.Fatalf("missing result=%+v", result)
	}

	if err := owner.Append(context.Background(), snapshotEvent(1)); err != nil {
		t.Fatal(err)
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
	if firstResult != secondResult || !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("consecutive snapshots differ: first=%+v second=%+v", firstResult, secondResult)
	}

	for name, data := range map[string][]byte{
		"partial":  []byte(`{"version":1}`),
		"invalid":  []byte("{not-json}\n"),
		"semantic": []byte("{}\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if result, err := owner.Snapshot(context.Background(), &output); err == nil || result != (store.SnapshotResult{}) || output.Len() != 0 {
				t.Fatalf("strict snapshot result=%+v bytes=%q err=%v", result, output.Bytes(), err)
			}
		})
	}
}

func TestJSONLStoreSnapshotCancellationAndWriterFailure(t *testing.T) {
	owner, err := NewJSONLStore(filepath.Join(t.TempDir(), "signals.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Append(context.Background(), snapshotEvent(1)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := owner.Snapshot(ctx, &bytes.Buffer{}); !errors.Is(err, context.Canceled) || result != (store.SnapshotResult{}) {
		t.Fatalf("cancel result=%+v err=%v", result, err)
	}

	writeFailure := errors.New("write failed")
	if result, err := owner.Snapshot(context.Background(), snapshotFailingWriter{err: writeFailure}); !errors.Is(err, writeFailure) || result != (store.SnapshotResult{}) {
		t.Fatalf("writer result=%+v err=%v", result, err)
	}

	streamCtx, streamCancel := context.WithCancel(context.Background())
	if result, err := owner.Snapshot(streamCtx, snapshotCancelWriter{cancel: streamCancel}); !errors.Is(err, context.Canceled) || result != (store.SnapshotResult{}) {
		t.Fatalf("stream cancel result=%+v err=%v", result, err)
	}
}

func TestJSONLStoreSnapshotLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signals.jsonl")
	owner, err := NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(snapshotEvent(1))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, append(append([]byte(nil), line...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	result, err := owner.snapshot(context.Background(), &output, snapshotLimits{
		maxLineBytes: len(line) - 1, maxTotalBytes: int64(len(line) + 1),
	})
	if err == nil || !strings.Contains(err.Error(), "line 1 exceeds") || result != (store.SnapshotResult{}) || output.Len() != 0 {
		t.Fatalf("line limit result=%+v bytes=%d err=%v", result, output.Len(), err)
	}

	record := append(append([]byte(nil), line...), '\n')
	if err := os.WriteFile(path, append(append([]byte(nil), record...), record...), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	result, err = owner.snapshot(context.Background(), &output, snapshotLimits{
		maxLineBytes: len(line), maxTotalBytes: int64(len(record)),
	})
	if err == nil || !strings.Contains(err.Error(), "total exceeds") || result != (store.SnapshotResult{}) || output.Len() != 0 {
		t.Fatalf("total limit result=%+v bytes=%d err=%v", result, output.Len(), err)
	}
}

func TestJSONLStoreSlowSnapshotWriterDoesNotBlockAppend(t *testing.T) {
	owner, err := NewJSONLStore(filepath.Join(t.TempDir(), "signals.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Append(context.Background(), snapshotEvent(1)); err != nil {
		t.Fatal(err)
	}

	writer := newBlockingSnapshotWriter()
	snapshotDone := make(chan error, 1)
	go func() {
		_, err := owner.Snapshot(context.Background(), writer)
		snapshotDone <- err
	}()
	<-writer.started

	appendDone := make(chan error, 1)
	go func() { appendDone <- owner.Append(context.Background(), snapshotEvent(2)) }()
	select {
	case err := <-appendDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Append blocked on snapshot writer")
	}
	close(writer.release)
	if err := <-snapshotDone; err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(writer.Bytes()))
	count := 0
	for scanner.Scan() {
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("fixed snapshot boundary contains %d records, want 1", count)
	}
}

func snapshotEvent(index int) Event {
	return Event{
		Version: Version, SignalID: fmt.Sprintf("macro:2026-07-26:%016d", index),
		Type: TypeMacro, AsOf: "2026-07-26", Symbols: []string{"US:AAPL"},
		Source: "snapshot-test", SourceID: fmt.Sprintf("row-%d", index),
		Facts: json.RawMessage(fmt.Sprintf(`{"index":%d}`, index)),
		Provenance: Provenance{
			RawHash: "sha256:abcdef", RetrievedAt: "2026-07-26T00:00:00Z",
		},
	}
}

func assertSignalSnapshot(t *testing.T, data []byte) {
	t.Helper()
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("snapshot is not newline complete: %q", data)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode snapshot: %v", err)
		}
		if err := event.Validate(); err != nil {
			t.Fatalf("validate snapshot: %v", err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

type snapshotFailingWriter struct {
	err error
}

func (w snapshotFailingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type snapshotCancelWriter struct {
	cancel context.CancelFunc
}

func (w snapshotCancelWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

type blockingSnapshotWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	output  bytes.Buffer
}

func newBlockingSnapshotWriter() *blockingSnapshotWriter {
	return &blockingSnapshotWriter{started: make(chan struct{}), release: make(chan struct{})}
}

func (w *blockingSnapshotWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.Write(p)
}

func (w *blockingSnapshotWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.output.Bytes()...)
}
