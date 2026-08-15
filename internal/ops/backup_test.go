package ops

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/snapshotipc"

	_ "modernc.org/sqlite"
)

func TestBackupJSONLAndIsolatedRestore(t *testing.T) {
	ctx := context.Background()
	root := candidateRoot(t)
	configHash := strings.Repeat("c", 64)
	sources, snapshotter := fixedSnapshotFixture(t, root, configHash)
	manifest, path, err := CreateBackup(ctx, root, BackupOptions{
		ReleaseID: "release", ConfigSHA256: configHash, Sources: sources,
		Snapshotter: snapshotter, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Objects) != 4 {
		t.Fatalf("objects=%d", len(manifest.Objects))
	}
	requirements, err := NewBackupRequirements(root, RequiredObjects(sources))
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	alternateManifestPath := filepath.Join(root.Path(), "copied-manifest.json")
	writeTestFile(t, alternateManifestPath, manifestData, 0o600)
	if _, err := RestoreVerify(ctx, root, alternateManifestPath, requirements); err == nil {
		t.Fatal("manifest copied outside fixed content-addressed path accepted")
	}
	result, err := RestoreVerify(ctx, root, path, requirements)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || !strings.HasPrefix(result.Directory, root.Path()+string(filepath.Separator)) {
		t.Fatalf("unsafe restore: %+v", result)
	}
	if receipt, err := VerifyRestoreReceipt(root, result.ReceiptPath, manifest); err != nil || receipt.Hash != result.Receipt.Hash {
		t.Fatalf("restore receipt: %+v %v", receipt, err)
	}
	receiptData, err := os.ReadFile(result.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	copiedReceiptPath := filepath.Join(root.Path(), "copied-receipt.json")
	writeTestFile(t, copiedReceiptPath, receiptData, 0o600)
	if _, err := LoadRestoreReceipt(root, copiedReceiptPath); err == nil {
		t.Fatal("restore receipt copied outside fixed CAS path accepted")
	}
	mutatedReceipt := result.Receipt
	mutatedReceipt.Directory = "restores/" + strings.Repeat("a", 64)
	mutatedReceipt.Hash = ""
	payload, _ := canonicalJSON(mutatedReceipt)
	mutatedReceipt.Hash = sha256Hex(payload)
	payload, _ = canonicalJSON(mutatedReceipt)
	capability, err := root.OpenCapability()
	if err != nil {
		t.Fatal(err)
	}
	mutatedReceiptPath, err := capability.WriteCAS("restores/receipts", ".json", payload)
	capability.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRestoreReceipt(root, mutatedReceiptPath); err == nil {
		t.Fatal("re-signed restore receipt with alternate directory accepted")
	}
	wrongObjects := manifest
	wrongObjects.Objects = append([]BackupObject(nil), manifest.Objects...)
	wrongObjects.Objects[0].SHA256 = strings.Repeat("a", 64)
	if _, err := VerifyRestoreReceipt(root, result.ReceiptPath, wrongObjects); err == nil {
		t.Fatal("restore receipt accepted wrong trusted object set")
	}
	restoredObject := filepath.Join(result.Directory, "signals.jsonl")
	if err := os.WriteFile(restoredObject, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRestoreReceipt(root, result.ReceiptPath, manifest); err == nil {
		t.Fatal("restore receipt accepted modified restored object")
	}
	if err := os.WriteFile(restoredObject, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(restoredObject, restoredObject+".missing"); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRestoreReceipt(root, result.ReceiptPath, manifest); err == nil {
		t.Fatal("restore receipt accepted missing restored object")
	}
	if _, err := RestoreVerify(ctx, root, path, requirements); err == nil {
		t.Fatal("restore reused existing directory")
	}
	if err := VerifyBackupObjects(root, manifest, requirements); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteOnlineBackupRequiresOwnerMediatedSnapshot(t *testing.T) {
	root := candidateRoot(t)
	source := filepath.Join(root.Path(), "sources", "wal.db")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("pragma journal_mode=wal; create table t(v text); insert into t values ('from-wal')"); err != nil {
		t.Fatal(err)
	}
	sources := []BackupSource{{Name: "db", Kind: "sqlite", Path: source}}
	manifest, path, err := CreateBackup(context.Background(), root, BackupOptions{ReleaseID: "r", ConfigSHA256: "c", Sources: sources, Now: time.Now().UTC()})
	if err == nil || !strings.Contains(err.Error(), ErrSQLiteOwnerRequired) {
		t.Fatalf("sqlite pathname backup accepted: %v", err)
	}
	if manifest.BackupID != "" || path != "" {
		t.Fatalf("sqlite refusal published result: manifest=%+v path=%q", manifest, path)
	}
	if _, err := os.Stat(filepath.Join(root.Path(), "backups", "manifests")); !os.IsNotExist(err) {
		t.Fatalf("sqlite refusal published manifest directory: %v", err)
	}
}

func TestSQLiteRefusalDoesNotPublishManifestAfterJSONLObject(t *testing.T) {
	root := candidateRoot(t)
	jsonl := filepath.Join(root.Path(), "sources", "feed.jsonl")
	sqlite := filepath.Join(root.Path(), "sources", "control.db")
	writeTestFile(t, jsonl, []byte("{}\n"), 0o600)
	writeTestFile(t, sqlite, []byte("not-opened-as-sqlite"), 0o600)
	manifest, path, err := CreateBackup(context.Background(), root, BackupOptions{
		ReleaseID: "r", ConfigSHA256: "c", Now: time.Now().UTC(),
		Sources: []BackupSource{
			{Name: "a-jsonl", Kind: "jsonl", Path: jsonl},
			{Name: "z-sqlite", Kind: "sqlite", Path: sqlite},
		},
	})
	if err == nil || !strings.Contains(err.Error(), ErrSQLiteOwnerRequired) {
		t.Fatalf("sqlite accepted after JSONL: %v", err)
	}
	if manifest.BackupID != "" || path != "" {
		t.Fatalf("sqlite refusal published result: manifest=%+v path=%q", manifest, path)
	}
	manifestDir := filepath.Join(root.Path(), "backups", "manifests")
	entries, readErr := os.ReadDir(manifestDir)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("sqlite refusal published %d manifests", len(entries))
	}
}

func TestBackupManifestRequiresTrustedExactObjectSet(t *testing.T) {
	root := candidateRoot(t)
	configHash := strings.Repeat("c", 64)
	sources, snapshotter := fixedSnapshotFixture(t, root, configHash)
	manifest, _, err := CreateBackup(context.Background(), root, BackupOptions{
		ReleaseID: "r", ConfigSHA256: configHash, Sources: sources,
		Snapshotter: snapshotter, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := NewBackupRequirements(root, RequiredObjects(sources))
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackup(manifest, requirements); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackup(manifest); err == nil {
		t.Fatal("manifest verified without trusted requirements")
	}
	for name, mutate := range map[string]func(*BackupManifest){
		"empty":      func(value *BackupManifest) { value.Objects = nil },
		"missing":    func(value *BackupManifest) { value.Objects = nil },
		"extra":      func(value *BackupManifest) { value.Objects = append(value.Objects, value.Objects[0]) },
		"wrong_kind": func(value *BackupManifest) { value.Objects[0].Kind = "sqlite" },
		"wrong_root": func(value *BackupManifest) { value.BackupRootBinding = strings.Repeat("a", 64) },
		"duplicate":  func(value *BackupManifest) { value.Objects = append(value.Objects, value.Objects[0]) },
		"escape":     func(value *BackupManifest) { value.Objects[0].ObjectPath = "../outside" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := manifest
			changed.Objects = append([]BackupObject(nil), manifest.Objects...)
			mutate(&changed)
			resignBackup(&changed)
			if err := VerifyBackup(changed, requirements); err == nil {
				t.Fatal("mutated manifest accepted")
			}
		})
	}
	otherRoot := candidateRoot(t)
	if _, err := NewBackupRequirements(otherRoot, RequiredObjects(sources)); err == nil {
		t.Fatal("required sources outside candidate root accepted")
	}
	if _, err := NewBackupRequirements(root, nil); err == nil {
		t.Fatal("empty requirements accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	writeTestFile(t, outside, []byte("{}\n"), 0o600)
	link := filepath.Join(root.Path(), "linked-source.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	linked := RequiredObjects(sources)
	linked[2].SourcePath = link
	if _, err := NewBackupRequirements(root, linked); err == nil {
		t.Fatal("symlinked required source accepted")
	}

	changed := manifest
	changed.Objects = append([]BackupObject(nil), manifest.Objects...)
	changed.Objects[0].OwnerReceipt.SHA256 = strings.Repeat("a", 64)
	resignOwnerReceipt(&changed.Objects[0].OwnerReceipt)
	changed.SnapshotSet.ReceiptHashes = append([]string(nil), manifest.SnapshotSet.ReceiptHashes...)
	changed.SnapshotSet.ReceiptHashes[0] = changed.Objects[0].OwnerReceipt.Hash
	resignSetReceipt(&changed.SnapshotSet)
	resignBackup(&changed)
	if err := VerifyBackup(changed, requirements); err == nil {
		t.Fatal("self-consistent forged owner receipt accepted")
	}

	changed = manifest
	changed.Objects = append([]BackupObject(nil), manifest.Objects...)
	changed.Objects[0].OwnerReceipt.RequestID = strings.Repeat("f", 64)
	resignOwnerReceipt(&changed.Objects[0].OwnerReceipt)
	changed.SnapshotSet.ReceiptHashes = append([]string(nil), manifest.SnapshotSet.ReceiptHashes...)
	changed.SnapshotSet.ReceiptHashes[0] = changed.Objects[0].OwnerReceipt.Hash
	resignSetReceipt(&changed.SnapshotSet)
	resignBackup(&changed)
	if err := VerifyBackup(changed, requirements); err == nil {
		t.Fatal("owner receipt from a different request accepted")
	}

	for name, mutateReceipt := range map[string]func(*snapshotipc.OwnerReceipt){
		"wrong_owner_kind": func(receipt *snapshotipc.OwnerReceipt) { receipt.OwnerKind = "pathname-copy" },
		"wrong_schema":     func(receipt *snapshotipc.OwnerReceipt) { receipt.SchemaVersion = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := manifest
			changed.Objects = append([]BackupObject(nil), manifest.Objects...)
			mutateReceipt(&changed.Objects[1].OwnerReceipt)
			resignOwnerReceipt(&changed.Objects[1].OwnerReceipt)
			changed.SnapshotSet.ReceiptHashes = append([]string(nil), manifest.SnapshotSet.ReceiptHashes...)
			changed.SnapshotSet.ReceiptHashes[1] = changed.Objects[1].OwnerReceipt.Hash
			resignSetReceipt(&changed.SnapshotSet)
			resignBackup(&changed)
			if err := VerifyBackup(changed, requirements); err == nil {
				t.Fatal("invalid fixed owner metadata accepted")
			}
		})
	}

	changed = manifest
	changed.Directory = "alternate-backups"
	resignBackup(&changed)
	if err := VerifyBackup(changed, requirements); err == nil {
		t.Fatal("alternate manifest directory accepted")
	}
	changed = manifest
	changed.Objects = append([]BackupObject(nil), manifest.Objects...)
	changed.Objects[0].Size = snapshotipc.MaxObjectBytes + 1
	resignBackup(&changed)
	if err := VerifyBackup(changed, requirements); err == nil {
		t.Fatal("oversized declared object accepted")
	}
	changed = manifest
	changed.Objects = append([]BackupObject(nil), manifest.Objects...)
	changed.Objects[0].ObjectPath = strings.TrimSuffix(changed.Objects[0].ObjectPath, ".db") + ".jsonl"
	resignBackup(&changed)
	if err := VerifyBackup(changed, requirements); err == nil {
		t.Fatal("object with kind-mismatched CAS suffix accepted")
	}
}

func resignBackup(manifest *BackupManifest) {
	manifest.BackupID = ""
	payload, _ := canonicalJSON(*manifest)
	manifest.BackupID = sha256Hex(payload)
}

func TestBackupHardRefusals(t *testing.T) {
	root := candidateRoot(t)
	source := filepath.Join(root.Path(), "sources", "feed.jsonl")
	writeTestFile(t, source, []byte("{}\n"), 0o600)
	base := BackupOptions{ReleaseID: "r", ConfigSHA256: "c", Now: time.Now().UTC()}
	bolt := base
	bolt.Sources = []BackupSource{{Name: "bolt", Kind: "bolt", Path: source}}
	if _, _, err := CreateBackup(context.Background(), root, bolt); err == nil || !strings.Contains(err.Error(), ErrBoltOnlineRequired) {
		t.Fatalf("bolt accepted: %v", err)
	}
	rolling := base
	rolling.Sources = []BackupSource{{Name: "feed", Kind: "rolling_jsonl", Path: source}}
	if _, _, err := CreateBackup(context.Background(), root, rolling); err == nil || !strings.Contains(err.Error(), ErrOwnerSnapshotRequired) {
		t.Fatalf("rolling pathname snapshot accepted: %v", err)
	}
}

func TestBackupRequiresExactOwnerSetAndPublishesNoManifestOnFailure(t *testing.T) {
	root := candidateRoot(t)
	configHash := strings.Repeat("c", 64)
	sources, snapshotter := fixedSnapshotFixture(t, root, configHash)
	incomplete := append([]BackupSource(nil), sources[:3]...)
	if _, _, err := CreateBackup(context.Background(), root, BackupOptions{
		ReleaseID: "r", ConfigSHA256: configHash, Sources: incomplete,
		Snapshotter: snapshotter, Now: time.Now().UTC(),
	}); err == nil {
		t.Fatal("incomplete fixed owner set accepted")
	}

	snapshotter.failObject = "control"
	manifest, path, err := CreateBackup(context.Background(), root, BackupOptions{
		ReleaseID: "r", ConfigSHA256: configHash, Sources: sources,
		Snapshotter: snapshotter, Now: time.Now().UTC(),
	})
	if err == nil || manifest.BackupID != "" || path != "" {
		t.Fatalf("failed owner set published result: manifest=%+v path=%q err=%v", manifest, path, err)
	}
	entries, readErr := os.ReadDir(filepath.Join(root.Path(), "backups", "manifests"))
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed owner set published %d manifests", len(entries))
	}
}
