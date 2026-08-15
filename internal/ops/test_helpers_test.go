package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
	"github.com/Ricaardo/nimbus-os/news/internal/snapshotipc"
)

func candidateRoot(t *testing.T) candidate.Root {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := candidate.InitRoot(path); err != nil {
		t.Fatal(err)
	}
	root, err := candidate.NewRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

type fakeSnapshotSetProvider struct {
	configHash  string
	rootBinding string
	sources     map[string]BackupSource
	data        map[string][]byte
	failObject  string
}

func (p *fakeSnapshotSetProvider) SnapshotSet(ctx context.Context, releaseID string, consume snapshotipc.ObjectConsumer) (snapshotipc.SetReceipt, error) {
	requestID := strings.Repeat("1", 64)
	setID := strings.Repeat("2", 64)
	now := time.Now().UTC()
	hashes := make([]string, 0, len(snapshotipc.FixedObjects()))
	for _, fixed := range snapshotipc.FixedObjects() {
		if err := ctx.Err(); err != nil {
			return snapshotipc.SetReceipt{}, err
		}
		source := p.sources[fixed.Name]
		begin := snapshotipc.ObjectBegin{
			RequestID: requestID, SnapshotSetID: setID, ObjectName: fixed.Name, Kind: fixed.Kind,
		}
		var emitted snapshotipc.OwnerReceipt
		err := consume(begin, func(destination io.Writer) (snapshotipc.OwnerReceipt, error) {
			if p.failObject == fixed.Name {
				return snapshotipc.OwnerReceipt{}, io.ErrUnexpectedEOF
			}
			if _, err := destination.Write(p.data[fixed.Name]); err != nil {
				return snapshotipc.OwnerReceipt{}, err
			}
			receipt := snapshotipc.OwnerReceipt{
				Schema: snapshotipc.ReceiptSchema, RequestID: requestID, SnapshotSetID: setID,
				ObjectName: fixed.Name, Kind: fixed.Kind, OwnerKind: fakeOwnerKind(fixed.Name),
				ReleaseID: releaseID, ConfigSHA256: p.configHash,
				CandidateRootBinding: p.rootBinding, SourceSHA256: snapshotipc.HashSourcePath(source.Path),
				SHA256: sha256Hex(p.data[fixed.Name]), Size: int64(len(p.data[fixed.Name])),
				OwnerUID: os.Geteuid(), StartedAt: now.Format(time.RFC3339Nano),
				CompletedAt: now.Add(time.Millisecond).Format(time.RFC3339Nano),
			}
			if fixed.Name == "control" {
				receipt.SchemaVersion = snapshotipc.ControlSchemaVersion
			}
			resignOwnerReceipt(&receipt)
			emitted = receipt
			return receipt, nil
		})
		if err != nil {
			return snapshotipc.SetReceipt{}, err
		}
		hashes = append(hashes, emitted.Hash)
	}
	set := snapshotipc.SetReceipt{
		Schema: snapshotipc.SetSchema, RequestID: requestID, SnapshotSetID: setID,
		ReleaseID: releaseID, ConfigSHA256: p.configHash,
		CandidateRootBinding: p.rootBinding, ReceiptHashes: hashes, OwnerUID: os.Geteuid(),
		StartedAt: now.Format(time.RFC3339Nano), CompletedAt: now.Add(2 * time.Millisecond).Format(time.RFC3339Nano),
	}
	resignSetReceipt(&set)
	return set, nil
}

func fixedSnapshotFixture(t *testing.T, root candidate.Root, configHash string) ([]BackupSource, *fakeSnapshotSetProvider) {
	t.Helper()
	newsPath := filepath.Join(root.Path(), "news.db")
	newsDB, err := bolt.Open(newsPath, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := newsDB.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("test"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("key"), []byte("value"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := newsDB.Close(); err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(root.Path(), "control.db")
	controlDB, err := sql.Open("sqlite", controlPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controlDB.Exec("create table test(value text); insert into test values ('ok')"); err != nil {
		t.Fatal(err)
	}
	if err := controlDB.Close(); err != nil {
		t.Fatal(err)
	}
	signalsPath := filepath.Join(root.Path(), "signals", "events.jsonl")
	feedPath := filepath.Join(root.Path(), "feed", "shadow.jsonl")
	writeTestFile(t, signalsPath, []byte("{}\n"), 0o600)
	writeTestFile(t, feedPath, []byte("{}\n"), 0o600)
	sources := []BackupSource{
		{Name: "news", Kind: "bolt", Path: newsPath},
		{Name: "control", Kind: "sqlite", Path: controlPath},
		{Name: "signals", Kind: "jsonl", Path: signalsPath},
		{Name: "shadow_feed", Kind: "rolling_jsonl", Path: feedPath},
	}
	return sources, fakeSnapshotProviderForSources(t, root, configHash, sources)
}

func fakeSnapshotProviderForSources(t *testing.T, root candidate.Root, configHash string, sources []BackupSource) *fakeSnapshotSetProvider {
	t.Helper()
	capability, err := root.OpenCapability()
	if err != nil {
		t.Fatal(err)
	}
	binding := capability.Binding()
	capability.Close()
	data := make(map[string][]byte, len(sources))
	sourceMap := make(map[string]BackupSource, len(sources))
	for _, source := range sources {
		value, err := os.ReadFile(source.Path)
		if err != nil {
			t.Fatal(err)
		}
		data[source.Name] = value
		sourceMap[source.Name] = source
	}
	return &fakeSnapshotSetProvider{
		configHash: configHash, rootBinding: binding, sources: sourceMap, data: data,
	}
}

func fakeOwnerKind(name string) string {
	switch name {
	case "news":
		return "bolt"
	case "control":
		return "sqlite"
	case "signals":
		return "signals_jsonl"
	case "shadow_feed":
		return "news_feed_jsonl_v1"
	default:
		return ""
	}
}

func resignOwnerReceipt(receipt *snapshotipc.OwnerReceipt) {
	receipt.Hash = ""
	payload, _ := json.Marshal(receipt)
	receipt.Hash = sha256Hex(payload)
}

func resignSetReceipt(receipt *snapshotipc.SetReceipt) {
	receipt.Hash = ""
	payload, _ := json.Marshal(receipt)
	receipt.Hash = sha256Hex(payload)
}
