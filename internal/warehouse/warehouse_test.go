package warehouse

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"golang.org/x/sys/unix"
)

type fixedCapacity uint64

func (capacity fixedCapacity) AvailableBytes(string) (uint64, error) { return uint64(capacity), nil }

func TestContractFixtures(t *testing.T) {
	root := filepath.Join("..", "..", "..", "docs", "contracts", "warehouse-staging-v1.fixtures")
	valid, err := os.ReadFile(filepath.Join(root, "valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(valid); err != nil {
		t.Fatalf("valid fixture: %v", err)
	}
	invalid, err := filepath.Glob(filepath.Join(root, "invalid-*.json"))
	if err != nil || len(invalid) == 0 {
		t.Fatalf("invalid fixtures: %v", err)
	}
	for _, path := range invalid {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseManifest(data); err == nil {
			t.Errorf("accepted invalid fixture %s", path)
		}
	}
}

func TestValidateRejectsUnknownDuplicatePathAndTimestamp(t *testing.T) {
	root, manifestPath := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000001")
	manifest := readManifest(t, manifestPath)

	t.Run("unknown", func(t *testing.T) {
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.Replace(string(data), `"files":`, `"unknown":true,"files":`, 1))
		if _, err := ParseManifest(data); err == nil {
			t.Fatal("accepted unknown field")
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		copy := manifest
		copy.Files = append(copy.Files, copy.Files[0])
		if _, err := ParseManifest(marshalManifest(t, copy)); err == nil {
			t.Fatal("accepted duplicate source_db/table")
		}
	})
	t.Run("path traversal", func(t *testing.T) {
		copy := manifest
		copy.Files = append([]File(nil), manifest.Files...)
		copy.Files[0].Path = "../outside.parquet"
		if _, err := ParseManifest(marshalManifest(t, copy)); err == nil {
			t.Fatal("accepted path traversal")
		}
	})
	t.Run("invalid RFC3339", func(t *testing.T) {
		copy := manifest
		copy.CreatedAt = "2026-07-23 01:00:00"
		if _, err := ParseManifest(marshalManifest(t, copy)); err == nil {
			t.Fatal("accepted invalid timestamp")
		}
	})
	t.Run("unknown producer", func(t *testing.T) {
		copy := manifest
		copy.Producer = "mystery"
		if _, err := ParseManifest(marshalManifest(t, copy)); err == nil {
			t.Fatal("accepted unknown producer")
		}
	})
	t.Run("missing required row_count", func(t *testing.T) {
		var raw map[string]any
		if err := json.Unmarshal(marshalManifest(t, manifest), &raw); err != nil {
			t.Fatal(err)
		}
		delete(raw["files"].([]any)[0].(map[string]any), "row_count")
		data, _ := json.Marshal(raw)
		if _, err := ParseManifest(data); err == nil {
			t.Fatal("missing row_count accepted")
		}
	})
	t.Run("missing required nullable", func(t *testing.T) {
		var raw map[string]any
		if err := json.Unmarshal(marshalManifest(t, manifest), &raw); err != nil {
			t.Fatal(err)
		}
		field := raw["files"].([]any)[0].(map[string]any)["schema"].([]any)[0].(map[string]any)
		delete(field, "nullable")
		data, _ := json.Marshal(raw)
		if _, err := ParseManifest(data); err == nil {
			t.Fatal("missing nullable accepted")
		}
	})
	t.Run("recursive duplicate key", func(t *testing.T) {
		data := strings.Replace(string(marshalManifest(t, manifest)), `"name":"id"`, `"name":"id","name":"other"`, 1)
		if _, err := ParseManifest([]byte(data)); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("duplicate nested key error=%v", err)
		}
	})
	t.Run("arbitrary RFC3339 offset", func(t *testing.T) {
		copy := manifest
		copy.AsOf = "2026-07-22T20:00:00-04:00"
		copy.Files[0].AsOf = copy.AsOf
		if _, err := ParseManifest(marshalManifest(t, copy)); err != nil {
			t.Fatalf("valid RFC3339 offset rejected: %v", err)
		}
	})
	_ = root
}

func TestValidateRejectsSymlinkCorruptionRowsAndSchema(t *testing.T) {
	t.Run("manifest root symlink", func(t *testing.T) {
		root, _ := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000024")
		base := canonicalTempDir(t)
		link := filepath.Join(base, "staging-link")
		if err := os.Symlink(root, link); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateStaging(filepath.Join(link, "manifest.json")); err == nil {
			t.Fatal("manifest root symlink accepted")
		}
	})
	t.Run("validation stays on opened root when path is replaced", func(t *testing.T) {
		root, manifestPath := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000025")
		moved := filepath.Join(filepath.Dir(root), "staging-original")
		validated, err := validateStagingWithHook(manifestPath, func() error {
			if err := os.Rename(root, moved); err != nil {
				return err
			}
			return os.Mkdir(root, 0o750)
		})
		if err != nil || validated == nil {
			t.Fatalf("validation did not remain anchored to opened root: %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root, manifestPath := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000002")
		manifest := readManifest(t, manifestPath)
		filePath := filepath.Join(root, filepath.FromSlash(manifest.Files[0].Path))
		realPath := filepath.Join(t.TempDir(), "real.parquet")
		data, _ := os.ReadFile(filePath)
		if err := os.WriteFile(realPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realPath, filePath); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateStaging(manifestPath); err == nil || !strings.Contains(err.Error(), "symbolic") {
			t.Fatalf("symlink error=%v", err)
		}
	})
	t.Run("validates opened inode when path is exchanged", func(t *testing.T) {
		root, manifestPath := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000021")
		manifest := readManifest(t, manifestPath)
		filePath := filepath.Join(root, filepath.FromSlash(manifest.Files[0].Path))
		movedPath := filePath + ".moved"
		err := validateFileWithHook(filePath, manifest.Files[0], func() error {
			if err := os.Rename(filePath, movedPath); err != nil {
				return err
			}
			return os.WriteFile(filePath, []byte("replacement"), 0o600)
		})
		if err != nil {
			t.Fatalf("validation reopened exchanged path: %v", err)
		}
	})
	t.Run("corrupt parquet with matching envelope", func(t *testing.T) {
		root, manifestPath := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000020")
		manifest := readManifest(t, manifestPath)
		filePath := filepath.Join(root, filepath.FromSlash(manifest.Files[0].Path))
		corrupt := []byte("not a parquet file")
		if err := os.WriteFile(filePath, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(corrupt)
		manifest.Files[0].ByteSize = int64(len(corrupt))
		manifest.Files[0].SHA256 = hex.EncodeToString(digest[:])
		if err := os.WriteFile(manifestPath, marshalManifest(t, manifest), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateStaging(manifestPath); err == nil || !strings.Contains(err.Error(), "parquet footer") {
			t.Fatalf("corrupt parquet error=%v", err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"hash", func(manifest *Manifest) { manifest.Files[0].SHA256 = strings.Repeat("a", 64) }},
		{"size", func(manifest *Manifest) { manifest.Files[0].ByteSize++ }},
		{"rows", func(manifest *Manifest) { manifest.Files[0].RowCount++ }},
		{"schema", func(manifest *Manifest) { manifest.Files[0].Schema[0].Type = "int64" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, manifestPath := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000003")
			manifest := readManifest(t, manifestPath)
			test.mutate(&manifest)
			if err := os.WriteFile(manifestPath, marshalManifest(t, manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateStaging(manifestPath); err == nil {
				t.Fatalf("accepted %s mismatch", test.name)
			}
		})
	}
}

func TestPublishIsAtomicAndIdempotent(t *testing.T) {
	_, manifestPath := makeStaging(t, 2, "018f2480-5df7-7b45-8000-000000000004")
	candidate := makeCandidateRoot(t)
	result, err := Publish(context.Background(), manifestPath, candidate, fixedCapacity(math.MaxUint64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateStaging(filepath.Join(result.Snapshot, "manifest.json")); err != nil {
		t.Fatalf("published snapshot invalid: %v", err)
	}
	pointer := readPointer(t, result.Pointer)
	if pointer.SnapshotID != result.SnapshotID || pointer.ProductionOwner == nil || *pointer.ProductionOwner {
		t.Fatalf("pointer=%+v result=%+v", pointer, result)
	}
	again, err := Publish(context.Background(), manifestPath, candidate, fixedCapacity(math.MaxUint64))
	if err != nil || again.SnapshotID != result.SnapshotID {
		t.Fatalf("idempotent publish: %+v %v", again, err)
	}
	entries, err := filepath.Glob(filepath.Join(candidate, "snapshots", ".publishing-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary snapshots remain: %v %v", entries, err)
	}
}

func TestConcurrentPublisherUsesExclusiveLock(t *testing.T) {
	_, manifestPath := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000005")
	candidate := makeCandidateRoot(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var firstErr error
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		_, firstErr = publish(context.Background(), manifestPath, candidate, fixedCapacity(math.MaxUint64), func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	if _, err := Publish(context.Background(), manifestPath, candidate, fixedCapacity(math.MaxUint64)); !errors.Is(err, ErrPublishBusy) {
		t.Fatalf("concurrent publish error=%v", err)
	}
	close(release)
	wait.Wait()
	if firstErr != nil {
		t.Fatal(firstErr)
	}
}

func TestPublishAdvisoryLockSurvivesStaleFileAndReleasesAfterCrash(t *testing.T) {
	candidate := canonicalTempDir(t)
	lockPath := filepath.Join(candidate, ".publish.lock")
	if err := os.WriteFile(lockPath, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquirePublishLock(candidate)
	if err != nil {
		t.Fatalf("stale lock file blocked advisory lock: %v", err)
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=TestPublishLockCrashHelper")
	command.Env = append(os.Environ(), "WAREHOUSE_LOCK_CRASH_HELPER=1", "WAREHOUSE_LOCK_ROOT="+candidate)
	err = command.Run()
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 23 {
		t.Fatalf("crash helper error=%v", err)
	}
	lock, err = acquirePublishLock(candidate)
	if err != nil {
		t.Fatalf("process-exit lock was not released: %v", err)
	}
	if err := lock.release(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishLockRejectsUnsafeObjectsWithoutMutation(t *testing.T) {
	t.Run("hardlink", func(t *testing.T) {
		candidate := makeCandidateRoot(t)
		target := filepath.Join(filepath.Dir(candidate), "target")
		if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(target, filepath.Join(candidate, ".publish.lock")); err != nil {
			t.Fatal(err)
		}
		if lock, err := acquirePublishLock(candidate); err == nil {
			_ = lock.release()
			t.Fatal("hard-linked lock accepted")
		}
		data, _ := os.ReadFile(target)
		if string(data) != "sentinel" {
			t.Fatalf("hardlink target mutated: %q", data)
		}
	})

	t.Run("fifo", func(t *testing.T) {
		candidate := makeCandidateRoot(t)
		if err := unix.Mkfifo(filepath.Join(candidate, ".publish.lock"), 0o600); err != nil {
			t.Fatal(err)
		}
		if lock, err := acquirePublishLock(candidate); err == nil {
			_ = lock.release()
			t.Fatal("FIFO lock accepted")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		candidate := makeCandidateRoot(t)
		target := filepath.Join(filepath.Dir(candidate), "target")
		if err := os.WriteFile(target, []byte("sentinel"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(candidate, ".publish.lock")); err != nil {
			t.Fatal(err)
		}
		if lock, err := acquirePublishLock(candidate); err == nil {
			_ = lock.release()
			t.Fatal("symlink lock accepted")
		}
		info, err := os.Stat(target)
		if err != nil || info.Mode().Perm() != 0o640 {
			t.Fatalf("symlink target mode changed: %v %v", info, err)
		}
	})

	t.Run("wrong mode", func(t *testing.T) {
		candidate := makeCandidateRoot(t)
		lockPath := filepath.Join(candidate, ".publish.lock")
		if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if lock, err := acquirePublishLock(candidate); err == nil {
			_ = lock.release()
			t.Fatal("wrong-mode lock accepted")
		}
		info, err := os.Stat(lockPath)
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("wrong-mode lock was chmodded: %v %v", info, err)
		}
	})

	t.Run("candidate root mode", func(t *testing.T) {
		candidate := makeCandidateRoot(t)
		if err := os.Chmod(candidate, 0o755); err != nil {
			t.Fatal(err)
		}
		if lock, err := acquirePublishLock(candidate); err == nil {
			_ = lock.release()
			t.Fatal("insecure candidate root mode accepted")
		}
	})

	t.Run("candidate root symlink", func(t *testing.T) {
		candidate := makeCandidateRoot(t)
		link := filepath.Join(filepath.Dir(candidate), "candidate-link")
		if err := os.Symlink(candidate, link); err != nil {
			t.Fatal(err)
		}
		if lock, err := acquirePublishLock(link); err == nil {
			_ = lock.release()
			t.Fatal("symlink candidate root accepted")
		}
	})
}

func TestPublishLockCrashHelper(t *testing.T) {
	if os.Getenv("WAREHOUSE_LOCK_CRASH_HELPER") != "1" {
		return
	}
	lock, err := acquirePublishLock(os.Getenv("WAREHOUSE_LOCK_ROOT"))
	if err != nil || lock == nil {
		os.Exit(2)
	}
	os.Exit(23)
}

func TestExistingSnapshotHashMismatchKeepsPointer(t *testing.T) {
	_, requestedManifest := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000022")
	_, otherManifest := makeStaging(t, 2, "018f2480-5df7-7b45-8000-000000000023")
	candidate := makeCandidateRoot(t)
	other, err := Publish(context.Background(), otherManifest, candidate, fixedCapacity(math.MaxUint64))
	if err != nil {
		t.Fatal(err)
	}
	pointerBefore, err := os.ReadFile(other.Pointer)
	if err != nil {
		t.Fatal(err)
	}
	requested, err := ValidateStaging(requestedManifest)
	if err != nil {
		t.Fatal(err)
	}
	wrongPath := filepath.Join(candidate, "snapshots", requested.ManifestHash)
	if err := os.Rename(other.Snapshot, wrongPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(context.Background(), requestedManifest, candidate, fixedCapacity(math.MaxUint64)); err == nil || !strings.Contains(err.Error(), "manifest hash") {
		t.Fatalf("existing hash mismatch error=%v", err)
	}
	pointerAfter, _ := os.ReadFile(other.Pointer)
	if string(pointerBefore) != string(pointerAfter) {
		t.Fatal("pointer changed after existing snapshot hash mismatch")
	}
}

func TestInterruptedPublishKeepsOldPointer(t *testing.T) {
	_, firstManifest := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000006")
	candidate := makeCandidateRoot(t)
	if _, err := Publish(context.Background(), firstManifest, candidate, fixedCapacity(math.MaxUint64)); err != nil {
		t.Fatal(err)
	}
	pointerPath := filepath.Join(candidate, "current.json")
	before, _ := os.ReadFile(pointerPath)
	_, secondManifest := makeStaging(t, 2, "018f2480-5df7-7b45-8000-000000000007")
	_, err := publish(context.Background(), secondManifest, candidate, fixedCapacity(math.MaxUint64), func() error {
		return errors.New("injected interruption")
	})
	if err == nil {
		t.Fatal("interrupted publish succeeded")
	}
	after, _ := os.ReadFile(pointerPath)
	if string(before) != string(after) {
		t.Fatal("pointer changed after interrupted publish")
	}
	leftovers, _ := filepath.Glob(filepath.Join(candidate, "snapshots", ".publishing-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary publish not cleaned: %v", leftovers)
	}
}

func TestPublishRejectsCandidateRootReplacementWhileLocked(t *testing.T) {
	_, manifestPath := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000027")
	candidate := makeCandidateRoot(t)
	moved := filepath.Join(filepath.Dir(candidate), "candidate-locked-original")
	replacementSentinel := []byte("replacement-must-remain-untouched")

	_, err := publish(context.Background(), manifestPath, candidate, fixedCapacity(math.MaxUint64), func() error {
		if err := os.Rename(candidate, moved); err != nil {
			return err
		}
		if err := os.Mkdir(candidate, 0o750); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(candidate, "sentinel"), replacementSentinel, 0o600)
	})
	if err == nil || !strings.Contains(err.Error(), "candidate root changed while locked") {
		t.Fatalf("candidate root replacement error=%v", err)
	}
	entries, readErr := os.ReadDir(candidate)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 || entries[0].Name() != "sentinel" {
		t.Fatalf("replacement candidate root was touched: %v", entries)
	}
	data, readErr := os.ReadFile(filepath.Join(candidate, "sentinel"))
	if readErr != nil || !reflect.DeepEqual(data, replacementSentinel) {
		t.Fatalf("replacement sentinel changed: %q %v", data, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(moved, "current.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("old locked root pointer unexpectedly committed: %v", statErr)
	}
	leftovers, globErr := filepath.Glob(filepath.Join(moved, "snapshots", ".publishing-*"))
	if globErr != nil || len(leftovers) != 0 {
		t.Fatalf("failed publish left temporary snapshots: %v %v", leftovers, globErr)
	}
}

func TestPublishRejectsStagingRootReplacementAfterValidation(t *testing.T) {
	root, manifestPath := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000026")
	candidate := makeCandidateRoot(t)
	moved := filepath.Join(filepath.Dir(root), "staging-original")
	_, err := publishWithHooks(context.Background(), manifestPath, candidate, fixedCapacity(math.MaxUint64), func() error {
		if err := os.Rename(root, moved); err != nil {
			return err
		}
		return os.Mkdir(root, 0o750)
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "staging root changed") {
		t.Fatalf("staging root replacement error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(candidate, "current.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pointer created after staging root replacement: %v", err)
	}
}

func TestPreflightRejectsInsufficientCapacity(t *testing.T) {
	_, manifestPath := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000008")
	validated, err := ValidateStaging(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	result, err := Preflight(root, validated, fixedCapacity(validated.TotalBytes))
	if err == nil || result.RequiredBytes <= validated.TotalBytes {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestKeepNewestRetentionProtectsCurrentSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	remove, err := (KeepNewestRetention{Keep: 1}).Plan([]SnapshotInfo{
		{ID: "old-current", CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "middle", CreatedAt: now.Add(-time.Hour)},
		{ID: "new", CreatedAt: now},
	}, "old-current")
	if err != nil || !reflect.DeepEqual(remove, []string{"middle"}) {
		t.Fatalf("remove=%v err=%v", remove, err)
	}
}

func TestParquetTimestampTimezoneAllowlist(t *testing.T) {
	for _, zone := range []string{"UTC", "Etc/UTC", "Z", "+00:00", "-00:00"} {
		t.Run("allow_"+strings.ReplaceAll(zone, "/", "_"), func(t *testing.T) {
			got, err := canonicalType(&arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: zone})
			if err != nil || got != "timestamp_us_utc" {
				t.Fatalf("zone=%q got=%q err=%v", zone, got, err)
			}
		})
	}
	for _, zone := range []string{"America/New_York", "Africa/Abidjan", "GMT"} {
		t.Run("reject_"+strings.ReplaceAll(zone, "/", "_"), func(t *testing.T) {
			if _, err := canonicalType(&arrow.TimestampType{Unit: arrow.Microsecond, TimeZone: zone}); err == nil {
				t.Fatalf("zone %q accepted", zone)
			}
		})
	}
}

func TestCandidateStatusNeverClaimsServingOrOwnership(t *testing.T) {
	root, manifestPath := makeStaging(t, 1, "018f2480-5df7-7b45-8000-000000000009")
	status := InspectCandidate(root, manifestPath)
	if !status.Enabled || !status.Prepared || status.Serving || status.ProductionOwner || status.Error != "" {
		t.Fatalf("status=%+v", status)
	}
	disabled := InspectCandidate("", "")
	if disabled.Enabled || disabled.Serving || disabled.ProductionOwner {
		t.Fatalf("disabled status=%+v", disabled)
	}
}

func TestExternalProducerFixture(t *testing.T) {
	manifest := os.Getenv("WAREHOUSE_MANIFEST_FIXTURE")
	if manifest == "" {
		t.Skip("WAREHOUSE_MANIFEST_FIXTURE is not set")
	}
	if _, err := ValidateStaging(manifest); err != nil {
		t.Fatal(err)
	}
}

func makeStaging(t *testing.T, values int, jobID string) (string, string) {
	t.Helper()
	root := filepath.Join(canonicalTempDir(t), "staging")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(root, "equity", "main.prices.parquet")
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o750); err != nil {
		t.Fatal(err)
	}
	schema := arrow.NewSchema([]arrow.Field{{Name: "id", Type: arrow.PrimitiveTypes.Int32, Nullable: true}}, nil)
	builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
	for index := 0; index < values; index++ {
		builder.Field(0).(*array.Int32Builder).Append(int32(index + 1))
	}
	record := builder.NewRecord()
	builder.Release()
	table := array.NewTableFromRecords(schema, []arrow.Record{record})
	record.Release()
	handle, err := os.Create(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pqarrow.WriteTable(table, handle, math.MaxInt64, parquet.NewWriterProperties(), pqarrow.DefaultWriterProps()); err != nil {
		t.Fatal(err)
	}
	table.Release()
	if err := handle.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(dataPath)
	digest := sha256.Sum256(data)
	manifest := Manifest{
		SchemaVersion: SchemaVersion, JobID: jobID, Producer: "equity-screener", ProducerVersion: "0.1.0",
		CreatedAt: "2026-07-23T01:00:00Z", AsOf: "2026-07-23T00:00:00Z",
		Files: []File{{
			SourceDB: "equity", Table: "main.prices", Path: "equity/main.prices.parquet",
			ByteSize: int64(len(data)), RowCount: int64(values), SHA256: hex.EncodeToString(digest[:]),
			Schema: []Field{{Name: "id", Type: "int32", Nullable: true}}, AsOf: "2026-07-23T00:00:00Z",
		}},
	}
	manifestPath := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifestPath, marshalManifest(t, manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, manifestPath
}

func makeCandidateRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(canonicalTempDir(t), "candidate")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	return root
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func marshalManifest(t *testing.T, manifest Manifest) []byte {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func readManifest(t *testing.T, path string) Manifest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

type currentPointer struct {
	SnapshotID      string `json:"snapshot_id"`
	ProductionOwner *bool  `json:"production_owner"`
}

func readPointer(t *testing.T, path string) currentPointer {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pointer currentPointer
	if err := json.Unmarshal(data, &pointer); err != nil {
		t.Fatal(err)
	}
	return pointer
}
