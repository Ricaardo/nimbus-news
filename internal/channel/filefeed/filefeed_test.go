package filefeed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/channel"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

func TestSendWritesLegacyAndV2Feeds(t *testing.T) {
	dir := t.TempDir()
	v1Path := filepath.Join(dir, "breaking.jsonl")
	v2Path := filepath.Join(dir, "breaking.v2.jsonl")
	ch, err := NewFileFeedChannel(channel.Config{
		Name: "news-feed",
		Type: "filefeed",
		Mode: channel.ModePush,
		Options: map[string]interface{}{
			"path":      v1Path,
			"v2_path":   v2Path,
			"max_lines": 10,
		},
	})
	if err != nil {
		t.Fatalf("NewFileFeedChannel: %v", err)
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	msg := model.NewNewsMessage("Apple supplier reports stronger AI demand", "body")
	msg.Source = "example"
	msg.Link = "https://example.com/news/1"
	msg.Tags = []string{"US:AAPL"}
	msg.CreateTime = time.Date(2026, 6, 21, 9, 30, 0, 0, time.UTC)
	msg.SetAIComment("苹果供应链称 AI 服务器需求增强。")
	msg.SetMetadata("economic_impact", "medium")

	if err := ch.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var legacy map[string]interface{}
	readOneJSONLine(t, v1Path, &legacy)
	if _, ok := legacy["version"]; ok {
		t.Fatalf("legacy feed should not include version: %#v", legacy)
	}
	if legacy["zh"] != "苹果供应链称 AI 服务器需求增强。" {
		t.Fatalf("unexpected legacy zh: %#v", legacy["zh"])
	}

	var v2 map[string]interface{}
	readOneJSONLine(t, v2Path, &v2)
	if got := v2["version"]; got != float64(2) {
		t.Fatalf("unexpected v2 version: %#v", got)
	}
	if v2["summary_zh"] != "苹果供应链称 AI 服务器需求增强。" {
		t.Fatalf("unexpected v2 summary_zh: %#v", v2["summary_zh"])
	}
	if v2["event_id"] == "" {
		t.Fatalf("missing event_id: %#v", v2)
	}
}

func TestSendPropagatesV2AppendFailure(t *testing.T) {
	dir := t.TempDir()
	v2Directory := filepath.Join(dir, "v2-is-a-directory")
	if err := os.MkdirAll(v2Directory, 0o755); err != nil {
		t.Fatal(err)
	}
	ch, err := NewFileFeedChannel(channel.Config{
		Name: "news-feed", Type: "filefeed", Mode: channel.ModePush,
		Options: map[string]interface{}{
			"path": filepath.Join(dir, "breaking.jsonl"), "v2_path": v2Directory,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(context.Background(), model.NewNewsMessage("title", "body")); err == nil {
		t.Fatal("v2 append failure was hidden")
	}
}

func TestSnapshotV1DuringConcurrentSendAndRoll(t *testing.T) {
	dir := t.TempDir()
	v1Path := filepath.Join(dir, "breaking.jsonl")
	v2Path := filepath.Join(dir, "breaking.v2.jsonl")
	owner := newSnapshotChannel(t, v1Path, v2Path, 7)
	if err := owner.Send(context.Background(), snapshotMessage(0)); err != nil {
		t.Fatal(err)
	}

	writeErr := make(chan error, 1)
	go func() {
		for i := 1; i <= 100; i++ {
			if err := owner.Send(context.Background(), snapshotMessage(i)); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	for i := 0; i < 12; i++ {
		var output bytes.Buffer
		result, err := owner.Snapshot(context.Background(), &output)
		if err != nil {
			t.Fatal(err)
		}
		if result.Kind != SnapshotKindV1 || result.Size != int64(output.Len()) {
			t.Fatalf("result=%+v bytes=%d", result, output.Len())
		}
		sum := sha256.Sum256(output.Bytes())
		if result.SHA256 != fmt.Sprintf("%x", sum) {
			t.Fatalf("snapshot hash=%q", result.SHA256)
		}
		lines := bytes.Split(bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), []byte{'\n'})
		if len(lines) == 0 || len(lines) > 7 || output.Bytes()[output.Len()-1] != '\n' {
			t.Fatalf("rolling snapshot lines=%d data=%q", len(lines), output.Bytes())
		}
		for _, line := range lines {
			var feed map[string]interface{}
			if err := json.Unmarshal(line, &feed); err != nil {
				t.Fatalf("decode v1: %v", err)
			}
			if _, hasVersion := feed["version"]; hasVersion {
				t.Fatalf("snapshot included v2 record: %#v", feed)
			}
		}
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if _, err := owner.Snapshot(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	v1, err := os.ReadFile(v1Path)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := os.ReadFile(v2Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), v1) || bytes.Equal(output.Bytes(), v2) {
		t.Fatal("snapshot must contain exactly the required v1 feed")
	}
}

func TestSnapshotV1MissingEmptyStrictAndRepeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breaking.jsonl")
	owner := newSnapshotChannel(t, path, "", 10)
	var output bytes.Buffer
	if result, err := owner.Snapshot(context.Background(), &output); err == nil || result != (store.SnapshotResult{}) || output.Len() != 0 {
		t.Fatalf("missing result=%+v bytes=%q err=%v", result, output.Bytes(), err)
	}

	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := owner.Snapshot(context.Background(), &output)
	if err != nil {
		t.Fatal(err)
	}
	emptyHash := sha256.Sum256(nil)
	if result.Kind != SnapshotKindV1 || result.Size != 0 || result.SHA256 != fmt.Sprintf("%x", emptyHash) {
		t.Fatalf("empty result=%+v", result)
	}

	if err := owner.Send(context.Background(), snapshotMessage(1)); err != nil {
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
		"partial":       []byte(`{"source":"test"}`),
		"invalid":       []byte("{not-json}\n"),
		"null":          []byte("null\n"),
		"array":         []byte("[]\n"),
		"string":        []byte("\"record\"\n"),
		"number":        []byte("1\n"),
		"empty_object":  []byte("{}\n"),
		"missing_ts":    []byte("{\"epoch\":1,\"source\":\"test\",\"title\":\"title\"}\n"),
		"invalid_ts":    []byte("{\"ts\":\"not-time\",\"epoch\":1,\"source\":\"test\",\"title\":\"title\"}\n"),
		"missing_epoch": []byte("{\"ts\":\"2026-07-26T00:00:00Z\",\"source\":\"test\",\"title\":\"title\"}\n"),
		"timestamp_mismatch": []byte(
			"{\"ts\":\"2026-07-26T00:00:00Z\",\"epoch\":1,\"source\":\"test\",\"title\":\"title\"}\n",
		),
		"missing_source": []byte("{\"ts\":\"2026-07-26T00:00:00Z\",\"epoch\":1,\"title\":\"title\"}\n"),
		"missing_content": []byte(
			"{\"ts\":\"2026-07-26T00:00:00Z\",\"epoch\":1,\"source\":\"test\"}\n",
		),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var strictOutput bytes.Buffer
			if result, err := owner.Snapshot(context.Background(), &strictOutput); err == nil || result != (store.SnapshotResult{}) || strictOutput.Len() != 0 {
				t.Fatalf("strict result=%+v bytes=%q err=%v", result, strictOutput.Bytes(), err)
			}
			assertNoSnapshotTemps(t, filepath.Dir(path))
		})
	}
}

func TestSnapshotV1CancellationAndWriterFailure(t *testing.T) {
	owner := newSnapshotChannel(t, filepath.Join(t.TempDir(), "breaking.jsonl"), "", 10)
	if err := owner.Send(context.Background(), snapshotMessage(1)); err != nil {
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
	assertNoSnapshotTemps(t, filepath.Dir(owner.path))

	if result, err := owner.Snapshot(context.Background(), shortSnapshotWriter{}); !errors.Is(err, io.ErrShortWrite) || result != (store.SnapshotResult{}) {
		t.Fatalf("short writer result=%+v err=%v", result, err)
	}
	assertNoSnapshotTemps(t, filepath.Dir(owner.path))
}

func TestSnapshotV1SlowWriterDoesNotBlockSendOrRoll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breaking.jsonl")
	owner := newSnapshotChannel(t, path, "", 1)
	if err := owner.Send(context.Background(), snapshotMessage(1)); err != nil {
		t.Fatal(err)
	}

	writer := newBlockingSnapshotWriter()
	snapshotDone := make(chan error, 1)
	go func() {
		_, err := owner.Snapshot(context.Background(), writer)
		snapshotDone <- err
	}()
	<-writer.started
	assertNoSnapshotTemps(t, filepath.Dir(path))

	sendDone := make(chan error, 1)
	go func() { sendDone <- owner.Send(context.Background(), snapshotMessage(2)) }()
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send/roll blocked on snapshot writer")
	}
	close(writer.release)
	if err := <-snapshotDone; err != nil {
		t.Fatal(err)
	}
	assertNoSnapshotTemps(t, filepath.Dir(path))
	if !bytes.Contains(writer.Bytes(), []byte("snapshot title 1")) || bytes.Contains(writer.Bytes(), []byte("snapshot title 2")) {
		t.Fatalf("snapshot boundary changed during roll: %s", writer.Bytes())
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(current, []byte("snapshot title 1")) || !bytes.Contains(current, []byte("snapshot title 2")) {
		t.Fatalf("roll did not preserve latest record: %s", current)
	}
}

func TestRollPreservesExistingModeAndInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breaking.jsonl")
	owner := newSnapshotChannel(t, path, "", 1)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Send(context.Background(), snapshotMessage(1)); err != nil {
		t.Fatal(err)
	}
	if err := owner.Send(context.Background(), snapshotMessage(2)); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != 0o600 {
		t.Fatalf("roll changed mode to %o", after.Mode().Perm())
	}
	if !os.SameFile(before, after) {
		t.Fatal("roll replaced the owner inode")
	}
}

func TestSnapshotV1CaptureLimitsCancellationAndCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "breaking.jsonl")
	owner := newSnapshotChannel(t, path, "", 10)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, fileFeedSnapshotMaxTotalBytes+1); err != nil {
		t.Fatal(err)
	}
	if result, err := owner.Snapshot(context.Background(), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "total exceeds") || result != (store.SnapshotResult{}) {
		t.Fatalf("total result=%+v err=%v", result, err)
	}
	assertNoSnapshotTemps(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	err := captureSnapshot(ctx, cancelAfterRead{reader: bytes.NewReader([]byte("complete")), cancel: cancel}, io.Discard, int64(len("complete")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("capture cancellation err=%v", err)
	}
	if err := captureSnapshot(context.Background(), bytes.NewReader([]byte("complete")), shortSnapshotWriter{}, int64(len("complete"))); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("capture short write err=%v", err)
	}
	assertNoSnapshotTemps(t, dir)
}

func newSnapshotChannel(t *testing.T, path, v2Path string, maxLines int) *FileFeedChannel {
	t.Helper()
	raw, err := NewFileFeedChannel(channel.Config{
		Name: "news-feed", Type: "filefeed", Mode: channel.ModePush,
		Options: map[string]interface{}{"path": path, "v2_path": v2Path, "max_lines": maxLines},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := raw.(*FileFeedChannel)
	if err := owner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return owner
}

func snapshotMessage(index int) *model.Message {
	msg := model.NewNewsMessage(fmt.Sprintf("snapshot title %d", index), "body")
	msg.Source = "snapshot-test"
	msg.CreateTime = time.Date(2026, 7, 26, 1, 2, index%60, 0, time.UTC)
	return msg
}

type snapshotFailingWriter struct {
	err error
}

func (w snapshotFailingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type shortSnapshotWriter struct{}

func (shortSnapshotWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

type cancelAfterRead struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (r cancelAfterRead) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.cancel()
	return n, err
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

func assertNoSnapshotTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".snapshot-") {
			t.Fatalf("snapshot temp leaked: %s", entry.Name())
		}
	}
}

func readOneJSONLine(t *testing.T, path string, out interface{}) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("Unmarshal(%s): %v\ndata=%s", path, err, data)
	}
}
