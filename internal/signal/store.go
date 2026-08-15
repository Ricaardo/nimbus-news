package signal

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

const (
	SnapshotKind          = "signals_jsonl"
	snapshotMaxLineBytes  = 4 * 1024 * 1024
	snapshotMaxTotalBytes = int64(256 * 1024 * 1024)
)

type snapshotLimits struct {
	maxLineBytes  int
	maxTotalBytes int64
}

var defaultSnapshotLimits = snapshotLimits{
	maxLineBytes:  snapshotMaxLineBytes,
	maxTotalBytes: snapshotMaxTotalBytes,
}

type Query struct {
	Type   string
	Symbol string
	AsOf   string
	Latest bool
}

type QueryStore interface {
	Query(context.Context, Query) ([]Event, error)
}

type JSONLStore struct {
	path string
	mu   sync.Mutex
}

func NewJSONLStore(path string) (*JSONLStore, error) {
	if path == "" {
		return nil, fmt.Errorf("signal: store path is required")
	}
	return &JSONLStore{path: path}, nil
}

func (s *JSONLStore) Append(_ context.Context, event Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(event)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

// Snapshot writes the stable, complete contents owned by this store. A missing
// store is an empty snapshot; an existing partial or invalid record is rejected.
func (s *JSONLStore) Snapshot(ctx context.Context, w io.Writer) (store.SnapshotResult, error) {
	return s.snapshot(ctx, w, defaultSnapshotLimits)
}

func (s *JSONLStore) snapshot(ctx context.Context, w io.Writer, limits snapshotLimits) (store.SnapshotResult, error) {
	if ctx == nil {
		return store.SnapshotResult{}, fmt.Errorf("signal snapshot: context is required")
	}
	if w == nil {
		return store.SnapshotResult{}, fmt.Errorf("signal snapshot: writer is required")
	}
	if limits.maxLineBytes <= 0 || limits.maxTotalBytes <= 0 {
		return store.SnapshotResult{}, fmt.Errorf("signal snapshot: positive limits are required")
	}
	if err := ctx.Err(); err != nil {
		return store.SnapshotResult{}, err
	}

	file, size, missing, err := s.openSnapshotFile()
	if err != nil {
		return store.SnapshotResult{}, err
	}
	if missing {
		sum := sha256.Sum256(nil)
		return store.SnapshotResult{Kind: SnapshotKind, SHA256: hex.EncodeToString(sum[:])}, nil
	}
	defer file.Close()
	if size > limits.maxTotalBytes {
		return store.SnapshotResult{}, fmt.Errorf("signal snapshot: total exceeds %d bytes", limits.maxTotalBytes)
	}
	if err := validateSnapshot(ctx, io.NewSectionReader(file, 0, size), limits); err != nil {
		return store.SnapshotResult{}, err
	}
	return writeSnapshot(ctx, w, io.NewSectionReader(file, 0, size), size)
}

func (s *JSONLStore) openSnapshotFile() (*os.File, int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil, 0, true, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("signal snapshot: open: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, false, fmt.Errorf("signal snapshot: stat: %w", err)
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, 0, false, fmt.Errorf("signal snapshot: owner file is not regular")
	}
	return file, info.Size(), false, nil
}

func validateSnapshot(ctx context.Context, r io.Reader, limits snapshotLimits) error {
	reader := bufio.NewReaderSize(&contextReader{ctx: ctx, reader: r}, limits.maxLineBytes+1)
	for lineNumber := 1; ; lineNumber++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return fmt.Errorf("signal snapshot: line %d exceeds %d bytes", lineNumber, limits.maxLineBytes)
		}
		if errors.Is(err, io.EOF) {
			if len(record) != 0 {
				return fmt.Errorf("signal snapshot: incomplete final record")
			}
			break
		}
		if err != nil {
			return fmt.Errorf("signal snapshot: read line %d: %w", lineNumber, err)
		}
		line := record[:len(record)-1]
		if len(line) > limits.maxLineBytes {
			return fmt.Errorf("signal snapshot: line %d exceeds %d bytes", lineNumber, limits.maxLineBytes)
		}
		var event Event
		if len(line) == 0 || json.Unmarshal(line, &event) != nil {
			return fmt.Errorf("signal snapshot: decode line %d", lineNumber)
		}
		if err := event.Validate(); err != nil {
			return fmt.Errorf("signal snapshot: validate line %d: %w", lineNumber, err)
		}
	}
	return nil
}

func writeSnapshot(ctx context.Context, w io.Writer, r io.Reader, size int64) (store.SnapshotResult, error) {
	hash := sha256.New()
	written := int64(0)
	buffer := make([]byte, 32*1024)
	for written < size {
		if err := ctx.Err(); err != nil {
			return store.SnapshotResult{}, err
		}
		next := int64(len(buffer))
		if remaining := size - written; remaining < next {
			next = remaining
		}
		n, err := io.ReadFull(&contextReader{ctx: ctx, reader: r}, buffer[:next])
		if err != nil {
			return store.SnapshotResult{}, fmt.Errorf("signal snapshot: read output: %w", err)
		}
		chunk := buffer[:n]
		n, err = w.Write(chunk)
		if n < 0 || n > len(chunk) {
			return store.SnapshotResult{}, fmt.Errorf("signal snapshot: invalid write count %d", n)
		}
		if n > 0 {
			_, _ = hash.Write(chunk[:n])
			written += int64(n)
		}
		if err != nil {
			return store.SnapshotResult{}, fmt.Errorf("signal snapshot: write: %w", err)
		}
		if n != len(chunk) {
			return store.SnapshotResult{}, fmt.Errorf("signal snapshot: write: %w", io.ErrShortWrite)
		}
	}
	if err := ctx.Err(); err != nil {
		return store.SnapshotResult{}, err
	}
	return store.SnapshotResult{Kind: SnapshotKind, Size: written, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (s *JSONLStore) Query(ctx context.Context, query Query) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return []Event{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var events []Event
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("signal: decode line %d: %w", line, err)
		}
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("signal: validate line %d: %w", line, err)
		}
		if query.Type != "" && event.Type != query.Type {
			continue
		}
		if query.AsOf != "" && event.AsOf != query.AsOf {
			continue
		}
		if query.Symbol != "" && !contains(event.Symbols, query.Symbol) {
			continue
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].AsOf != events[j].AsOf {
			return events[i].AsOf < events[j].AsOf
		}
		return events[i].Provenance.RetrievedAt < events[j].Provenance.RetrievedAt
	})
	if query.Latest && len(events) > 0 {
		return events[len(events)-1:], nil
	}
	return events, nil
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
