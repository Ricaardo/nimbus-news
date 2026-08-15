package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
	moderncsqlite "modernc.org/sqlite"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
	"github.com/Ricaardo/nimbus-os/news/internal/snapshotipc"
)

const (
	BackupSchema               = "nimbus-backup/v2"
	backupDirectory            = "backups"
	maxBackupManifestBytes     = 1 << 20
	ErrBoltOnlineRequired      = "BOLT_ONLINE_SNAPSHOT_REQUIRED"
	ErrSQLiteOwnerRequired     = "SQLITE_OWNER_MEDIATED_SNAPSHOT_REQUIRED"
	ErrOwnerSnapshotRequired   = "OWNER_MEDIATED_SNAPSHOT_REQUIRED"
	ErrRollingFileInconsistent = "ROLLING_FILE_INCONSISTENT"
)

type BackupSource struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type BackupObject struct {
	Name         string                   `json:"name"`
	Kind         string                   `json:"kind"`
	SourcePath   string                   `json:"source_path"`
	ObjectPath   string                   `json:"object_path"`
	SHA256       string                   `json:"sha256"`
	Size         int64                    `json:"size"`
	OwnerReceipt snapshotipc.OwnerReceipt `json:"owner_receipt"`
}

type BackupManifest struct {
	Schema            string                 `json:"schema"`
	BackupID          string                 `json:"backup_id"`
	ReleaseID         string                 `json:"release_id"`
	ConfigSHA256      string                 `json:"config_sha256"`
	BackupRootBinding string                 `json:"backup_root_binding"`
	Directory         string                 `json:"directory"`
	CreatedAt         string                 `json:"created_at"`
	Objects           []BackupObject         `json:"objects"`
	SnapshotSet       snapshotipc.SetReceipt `json:"snapshot_set"`
}

type RequiredObject struct {
	Name       string
	Kind       string
	SourcePath string
}

type BackupRequirements struct {
	RootBinding string
	Objects     []RequiredObject
	root        candidate.Root
}

type BackupOptions struct {
	ReleaseID    string
	ConfigSHA256 string
	Sources      []BackupSource
	Directory    string
	Now          time.Time
	Snapshotter  SnapshotSetProvider
	// BeforeRestat is test-only fault injection for rolling-file consistency.
	BeforeRestat func()
}

type SnapshotSetProvider interface {
	SnapshotSet(context.Context, string, snapshotipc.ObjectConsumer) (snapshotipc.SetReceipt, error)
}

func CreateBackup(ctx context.Context, root candidate.Root, opts BackupOptions) (BackupManifest, string, error) {
	if opts.ReleaseID == "" || opts.ConfigSHA256 == "" || len(opts.Sources) == 0 {
		return BackupManifest{}, "", fmt.Errorf("backup: release, config, and sources are required")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	if opts.Directory == "" {
		opts.Directory = backupDirectory
	}
	if opts.Directory != backupDirectory {
		return BackupManifest{}, "", fmt.Errorf("backup: directory is fixed")
	}
	if opts.Snapshotter == nil {
		for _, source := range opts.Sources {
			switch source.Kind {
			case "sqlite":
				return BackupManifest{}, "", fmt.Errorf("%s: %s", ErrSQLiteOwnerRequired, source.Name)
			case "bolt":
				return BackupManifest{}, "", fmt.Errorf("%s: %s", ErrBoltOnlineRequired, source.Name)
			}
		}
		return BackupManifest{}, "", fmt.Errorf(ErrOwnerSnapshotRequired)
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return BackupManifest{}, "", err
	}
	defer capability.Close()
	directory, err := capability.Relative(opts.Directory)
	if err != nil {
		return BackupManifest{}, "", fmt.Errorf("backup: invalid directory: %w", err)
	}
	manifest := BackupManifest{Schema: BackupSchema, ReleaseID: opts.ReleaseID, ConfigSHA256: opts.ConfigSHA256, BackupRootBinding: capability.Binding(), Directory: directory, CreatedAt: opts.Now.UTC().Format(time.RFC3339)}
	sources := append([]BackupSource(nil), opts.Sources...)
	sourceByName, err := validateFixedBackupSources(capability, sources)
	if err != nil {
		return BackupManifest{}, "", err
	}
	var receipts []snapshotipc.OwnerReceipt
	setReceipt, err := opts.Snapshotter.SnapshotSet(ctx, opts.ReleaseID, func(begin snapshotipc.ObjectBegin, producer snapshotipc.ObjectProducer) error {
		source := sourceByName[begin.ObjectName]
		suffix, err := backupSuffix(source.Kind)
		if err != nil {
			return err
		}
		var receipt snapshotipc.OwnerReceipt
		result, err := capability.WriteCASStream(ctx, filepath.ToSlash(filepath.Join(directory, "objects")), suffix, snapshotipc.MaxObjectBytes, func(writer io.Writer) error {
			value, snapshotErr := producer(writer)
			receipt = value
			if snapshotErr != nil {
				return snapshotErr
			}
			return verifyOwnerReceiptBinding(receipt, source, opts.ReleaseID, opts.ConfigSHA256, capability.Binding())
		})
		if err != nil {
			return err
		}
		if receipt.Size != result.Size || receipt.SHA256 != result.SHA256 {
			return fmt.Errorf("backup: owner receipt and CAS object mismatch")
		}
		manifest.Objects = append(manifest.Objects, BackupObject{
			Name: source.Name, Kind: source.Kind, SourcePath: filepath.Clean(source.Path),
			ObjectPath: result.Relative, SHA256: result.SHA256, Size: result.Size, OwnerReceipt: receipt,
		})
		receipts = append(receipts, receipt)
		return nil
	})
	if err != nil {
		return BackupManifest{}, "", err
	}
	manifest.SnapshotSet = setReceipt
	if err := verifySnapshotSetBinding(setReceipt, receipts, manifest); err != nil {
		return BackupManifest{}, "", err
	}
	payload, err := canonicalJSON(manifest)
	if err != nil {
		return BackupManifest{}, "", err
	}
	manifest.BackupID = sha256Hex(payload)
	payload, err = canonicalJSON(manifest)
	if err != nil {
		return BackupManifest{}, "", err
	}
	path, err := capability.WriteCAS(filepath.ToSlash(filepath.Join(directory, "manifests")), ".json", payload)
	return manifest, path, err
}

func validateFixedBackupSources(capability *candidate.Capability, sources []BackupSource) (map[string]BackupSource, error) {
	fixed := snapshotipc.FixedObjects()
	if len(sources) != len(fixed) {
		return nil, fmt.Errorf("backup: exactly four fixed owner sources are required")
	}
	byName := make(map[string]BackupSource, len(sources))
	for _, source := range sources {
		kind, ok := snapshotipc.FixedKind(source.Name)
		if !ok || source.Kind != kind || source.Path == "" || byName[source.Name].Name != "" {
			return nil, fmt.Errorf("backup: invalid fixed owner source")
		}
		if _, err := capability.Relative(source.Path); err != nil {
			return nil, fmt.Errorf("backup: source must remain below candidate root")
		}
		byName[source.Name] = source
	}
	for _, object := range fixed {
		if byName[object.Name].Name == "" {
			return nil, fmt.Errorf("backup: fixed owner source is missing")
		}
	}
	return byName, nil
}

func backupSuffix(kind string) (string, error) {
	switch kind {
	case "sqlite", "bolt":
		return ".db", nil
	case "jsonl", "rolling_jsonl":
		return ".jsonl", nil
	default:
		return "", fmt.Errorf("backup: unsupported source kind %q", kind)
	}
}

func verifyOwnerReceiptBinding(receipt snapshotipc.OwnerReceipt, source BackupSource, releaseID, configHash, rootBinding string) error {
	if err := snapshotipc.VerifyOwnerReceipt(receipt); err != nil {
		return err
	}
	if receipt.ObjectName != source.Name || receipt.Kind != source.Kind ||
		receipt.SourceSHA256 != snapshotipc.HashSourcePath(source.Path) ||
		receipt.ReleaseID != releaseID || receipt.ConfigSHA256 != configHash ||
		receipt.CandidateRootBinding != rootBinding || receipt.OwnerUID != os.Geteuid() {
		return fmt.Errorf("backup: owner receipt binding mismatch")
	}
	return nil
}

func verifySnapshotSetBinding(receipt snapshotipc.SetReceipt, owners []snapshotipc.OwnerReceipt, manifest BackupManifest) error {
	if err := snapshotipc.VerifySetReceipt(receipt); err != nil {
		return err
	}
	if receipt.ReleaseID != manifest.ReleaseID || receipt.ConfigSHA256 != manifest.ConfigSHA256 ||
		receipt.CandidateRootBinding != manifest.BackupRootBinding ||
		receipt.OwnerUID != os.Geteuid() || len(receipt.ReceiptHashes) != len(owners) ||
		len(owners) != len(snapshotipc.FixedObjects()) {
		return fmt.Errorf("backup: snapshot set binding mismatch")
	}
	for index, owner := range owners {
		if owner.RequestID != receipt.RequestID || owner.SnapshotSetID != receipt.SnapshotSetID ||
			owner.Hash != receipt.ReceiptHashes[index] {
			return fmt.Errorf("backup: snapshot set owner mismatch")
		}
	}
	return nil
}

func NewBackupRequirements(root candidate.Root, objects []RequiredObject) (BackupRequirements, error) {
	capability, err := root.OpenCapability()
	if err != nil {
		return BackupRequirements{}, err
	}
	defer capability.Close()
	requirements := BackupRequirements{
		RootBinding: capability.Binding(), Objects: append([]RequiredObject(nil), objects...), root: root,
	}
	if err := validateRequiredObjects(requirements.Objects); err != nil {
		return BackupRequirements{}, err
	}
	for _, object := range requirements.Objects {
		canonical, err := root.RequireFile("required backup source", object.SourcePath)
		if err != nil || canonical != object.SourcePath {
			return BackupRequirements{}, fmt.Errorf("backup: required source is outside or unsafe for candidate root")
		}
	}
	return requirements, nil
}

func RequiredObjects(sources []BackupSource) []RequiredObject {
	out := make([]RequiredObject, 0, len(sources))
	for _, source := range sources {
		out = append(out, RequiredObject{Name: source.Name, Kind: source.Kind, SourcePath: filepath.Clean(source.Path)})
	}
	return out
}

type sqliteBackuper interface {
	NewBackup(string) (*moderncsqlite.Backup, error)
}
type sqliteSerializer interface{ Serialize() ([]byte, error) }

func sqliteOnlineBackup(ctx context.Context, path string) ([]byte, error) {
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(path))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var data []byte
	err = conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(sqliteBackuper)
		if !ok {
			return fmt.Errorf("sqlite backup API unavailable")
		}
		backup, err := backuper.NewBackup(":memory:")
		if err != nil {
			return err
		}
		for more := true; more; {
			more, err = backup.Step(-1)
			if err != nil {
				_ = backup.Finish()
				return err
			}
		}
		destination, err := backup.Commit()
		if err != nil {
			return err
		}
		defer destination.Close()
		serializer, ok := destination.(sqliteSerializer)
		if !ok {
			return fmt.Errorf("sqlite serialization unavailable")
		}
		data, err = serializer.Serialize()
		return err
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

func stableJSONL(path string, rolling bool, beforeRestat func()) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	return stableJSONLFile(file, before, rolling, beforeRestat)
}

func stableJSONLFile(file *os.File, before os.FileInfo, rolling bool, beforeRestat func()) ([]byte, error) {
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	if beforeRestat != nil {
		beforeRestat()
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf(ErrRollingFileInconsistent)
	}
	if len(data) == 0 {
		return data, nil
	}
	last := strings.LastIndexByte(string(data), '\n')
	if last < 0 {
		return nil, fmt.Errorf("jsonl: no complete record")
	}
	if rolling && last != len(data)-1 {
		return nil, fmt.Errorf(ErrRollingFileInconsistent)
	}
	prefix := data[:last+1]
	for _, line := range strings.Split(strings.TrimSuffix(string(prefix), "\n"), "\n") {
		if !json.Valid([]byte(line)) {
			return nil, fmt.Errorf("jsonl: invalid complete record")
		}
	}
	return prefix, nil
}

type pinnedBackupSource struct {
	file     *os.File
	before   os.FileInfo
	relative string
}

func openPinnedBackupSource(capability *candidate.Capability, path string) (*pinnedBackupSource, error) {
	relative, err := capability.Relative(path)
	if err != nil {
		return nil, err
	}
	file, info, err := capability.OpenRead(relative, 0)
	if err != nil {
		return nil, err
	}
	return &pinnedBackupSource{file: file, before: info, relative: relative}, nil
}

func verifyPinnedBackupSource(capability *candidate.Capability, source *pinnedBackupSource) error {
	if source == nil {
		return fmt.Errorf("backup: pinned source is required")
	}
	after, err := source.file.Stat()
	if err != nil {
		return err
	}
	current, currentInfo, err := capability.OpenRead(source.relative, 0)
	if err != nil {
		return err
	}
	current.Close()
	if !os.SameFile(source.before, after) || !os.SameFile(source.before, currentInfo) || source.before.Size() != after.Size() || source.before.Size() != currentInfo.Size() || source.before.Mode() != after.Mode() || source.before.Mode() != currentInfo.Mode() || !source.before.ModTime().Equal(after.ModTime()) || !source.before.ModTime().Equal(currentInfo.ModTime()) {
		return fmt.Errorf("backup: source path or inode changed during snapshot")
	}
	return capability.ValidateRoot()
}

type RestoreResult struct {
	Schema      string         `json:"schema"`
	BackupID    string         `json:"backup_id"`
	Directory   string         `json:"directory"`
	Verified    bool           `json:"verified"`
	Receipt     RestoreReceipt `json:"receipt"`
	ReceiptPath string         `json:"receipt_path"`
}

type RestoreReceipt struct {
	Schema            string `json:"schema"`
	Hash              string `json:"hash"`
	BackupID          string `json:"backup_id"`
	BackupRootBinding string `json:"backup_root_binding"`
	ObjectsSHA256     string `json:"objects_sha256"`
	Directory         string `json:"directory"`
	Verified          bool   `json:"verified"`
}

func RestoreVerify(ctx context.Context, root candidate.Root, manifestPath string, requirements ...BackupRequirements) (RestoreResult, error) {
	cap, err := root.OpenCapability()
	if err != nil {
		return RestoreResult{}, err
	}
	defer cap.Close()
	manifestRel, err := cap.Relative(manifestPath)
	if err != nil {
		return RestoreResult{}, err
	}
	data, err := cap.ReadFile(manifestRel, maxBackupManifestBytes, 0o600)
	if err != nil {
		return RestoreResult{}, err
	}
	if manifestRel != backupManifestRelative(data) {
		return RestoreResult{}, fmt.Errorf("backup: manifest path is not the fixed content address")
	}
	manifest, err := VerifyBackupManifest(data, requirements...)
	if err != nil {
		return RestoreResult{}, err
	}
	if manifest.BackupRootBinding != cap.Binding() {
		return RestoreResult{}, fmt.Errorf("backup: root binding mismatch")
	}
	want := manifest.BackupID
	relRestore := filepath.ToSlash(filepath.Join("restores", want))
	if err := cap.MkdirExclusive(relRestore, 0o700); err != nil {
		return RestoreResult{}, fmt.Errorf("restore: isolated directory must be new: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = cap.RemoveTree(relRestore)
		}
	}()
	directory, err := cap.Absolute(relRestore)
	if err != nil {
		return RestoreResult{}, err
	}
	for _, object := range manifest.Objects {
		if !safeName(object.Name) {
			return RestoreResult{}, fmt.Errorf("restore: unsafe object name")
		}
		suffix, err := backupSuffix(object.Kind)
		if err != nil {
			return RestoreResult{}, err
		}
		objectRel, err := cap.Relative(object.ObjectPath)
		if err != nil {
			return RestoreResult{}, err
		}
		objectData, err := cap.ReadFile(objectRel, snapshotipc.MaxObjectBytes, 0o600)
		if err != nil {
			return RestoreResult{}, err
		}
		if int64(len(objectData)) != object.Size || sha256Hex(objectData) != object.SHA256 {
			return RestoreResult{}, fmt.Errorf("restore: object mismatch")
		}
		relTarget := filepath.ToSlash(filepath.Join(relRestore, object.Name+suffix))
		target, err := cap.Absolute(relTarget)
		if err != nil {
			return RestoreResult{}, err
		}
		file, err := cap.CreateExclusive(relTarget, 0o600)
		if err != nil {
			return RestoreResult{}, err
		}
		_, writeErr := file.Write(objectData)
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return RestoreResult{}, err
		}
		if object.Kind == "sqlite" {
			if err := sqliteQuickCheck(ctx, target); err != nil {
				return RestoreResult{}, err
			}
		}
	}
	if err := cap.SyncDir(relRestore); err != nil {
		return RestoreResult{}, err
	}
	objectsPayload, err := canonicalJSON(manifest.Objects)
	if err != nil {
		return RestoreResult{}, err
	}
	receipt := RestoreReceipt{
		Schema: "nimbus-restore-receipt/v1", BackupID: want, BackupRootBinding: cap.Binding(),
		ObjectsSHA256: sha256Hex(objectsPayload), Directory: relRestore, Verified: true,
	}
	receiptPayload, err := canonicalJSON(receipt)
	if err != nil {
		return RestoreResult{}, err
	}
	receipt.Hash = sha256Hex(receiptPayload)
	receiptPayload, err = canonicalJSON(receipt)
	if err != nil {
		return RestoreResult{}, err
	}
	receiptPath, err := cap.WriteCAS("restores/receipts", ".json", receiptPayload)
	if err != nil {
		return RestoreResult{}, err
	}
	ok = true
	return RestoreResult{Schema: "nimbus-restore/v1", BackupID: want, Directory: directory, Verified: true, Receipt: receipt, ReceiptPath: receiptPath}, nil
}

func LoadRestoreReceipt(root candidate.Root, path string) (RestoreReceipt, error) {
	capability, err := root.OpenCapability()
	if err != nil {
		return RestoreReceipt{}, err
	}
	defer capability.Close()
	relative, err := capability.Relative(path)
	if err != nil {
		return RestoreReceipt{}, err
	}
	data, err := capability.ReadFile(relative, 1<<20, 0o600)
	if err != nil {
		return RestoreReceipt{}, err
	}
	if relative != restoreReceiptRelative(data) {
		return RestoreReceipt{}, fmt.Errorf("restore receipt: path is not the fixed content address")
	}
	var receipt RestoreReceipt
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return RestoreReceipt{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return RestoreReceipt{}, fmt.Errorf("restore receipt: extra JSON value")
	}
	hash := receipt.Hash
	receipt.Hash = ""
	payload, _ := canonicalJSON(receipt)
	receipt.Hash = hash
	expectedDirectory := filepath.ToSlash(filepath.Join("restores", receipt.BackupID))
	if receipt.Schema != "nimbus-restore-receipt/v1" || !receipt.Verified ||
		!validSHA256(receipt.BackupID) || receipt.BackupRootBinding != capability.Binding() ||
		!validSHA256(receipt.ObjectsSHA256) || sha256Hex(payload) != hash ||
		receipt.Directory != expectedDirectory {
		return RestoreReceipt{}, fmt.Errorf("restore receipt: invalid binding")
	}
	return receipt, nil
}

func restoreReceiptRelative(data []byte) string {
	return filepath.ToSlash(filepath.Join("restores", "receipts", sha256Hex(data)+".json"))
}

func VerifyRestoreReceipt(root candidate.Root, path string, backup BackupManifest) (RestoreReceipt, error) {
	receipt, err := LoadRestoreReceipt(root, path)
	if err != nil {
		return RestoreReceipt{}, err
	}
	if backup.BackupID == "" || receipt.BackupID != backup.BackupID || receipt.BackupRootBinding != backup.BackupRootBinding {
		return RestoreReceipt{}, fmt.Errorf("restore receipt: backup binding mismatch")
	}
	objectsPayload, err := canonicalJSON(backup.Objects)
	if err != nil {
		return RestoreReceipt{}, err
	}
	if receipt.ObjectsSHA256 != sha256Hex(objectsPayload) {
		return RestoreReceipt{}, fmt.Errorf("restore receipt: object set mismatch")
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return RestoreReceipt{}, err
	}
	defer capability.Close()
	for _, object := range backup.Objects {
		if object.Size < 0 || object.Size > snapshotipc.MaxObjectBytes {
			return RestoreReceipt{}, fmt.Errorf("restore receipt: object exceeds size limit")
		}
		suffix, err := backupSuffix(object.Kind)
		if err != nil {
			return RestoreReceipt{}, err
		}
		relative := filepath.ToSlash(filepath.Join(receipt.Directory, object.Name+suffix))
		data, err := capability.ReadFile(relative, snapshotipc.MaxObjectBytes, 0o600)
		if err != nil {
			return RestoreReceipt{}, fmt.Errorf("restore receipt %s: %w", object.Name, err)
		}
		if int64(len(data)) != object.Size || sha256Hex(data) != object.SHA256 {
			return RestoreReceipt{}, fmt.Errorf("restore receipt: restored object mismatch")
		}
	}
	return receipt, nil
}

func VerifyBackupManifest(data []byte, requirements ...BackupRequirements) (BackupManifest, error) {
	var manifest BackupManifest
	if len(data) == 0 || len(data) > maxBackupManifestBytes {
		return manifest, fmt.Errorf("backup: manifest exceeds size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return manifest, fmt.Errorf("backup: extra JSON value")
		}
		return manifest, err
	}
	if err := VerifyBackup(manifest, requirements...); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func VerifyBackup(manifest BackupManifest, requirements ...BackupRequirements) error {
	required, err := oneBackupRequirements(requirements)
	if err != nil {
		return err
	}
	want := manifest.BackupID
	manifest.BackupID = ""
	payload, _ := canonicalJSON(manifest)
	if manifest.Schema != BackupSchema || want == "" || manifest.ReleaseID == "" || manifest.ConfigSHA256 == "" || manifest.BackupRootBinding == "" || manifest.BackupRootBinding != required.RootBinding || sha256Hex(payload) != want {
		return fmt.Errorf("backup: id mismatch")
	}
	createdAt, err := time.Parse(time.RFC3339, manifest.CreatedAt)
	if err != nil || createdAt.Format(time.RFC3339) != manifest.CreatedAt {
		return fmt.Errorf("backup: invalid created_at")
	}
	if len(manifest.Objects) == 0 || len(manifest.Objects) != len(required.Objects) {
		return fmt.Errorf("backup: required object set mismatch")
	}
	if manifest.Directory != backupDirectory {
		return fmt.Errorf("backup: unsafe directory")
	}
	expected := make(map[string]RequiredObject, len(required.Objects))
	for _, object := range required.Objects {
		expected[object.Name] = object
	}
	fixed := snapshotipc.FixedObjects()
	ownerReceipts := make([]snapshotipc.OwnerReceipt, 0, len(manifest.Objects))
	seenNames := map[string]bool{}
	seenSources := map[string]bool{}
	for index, object := range manifest.Objects {
		expectedObject, ok := expected[object.Name]
		if !ok || index >= len(fixed) || object.Name != fixed[index].Name ||
			seenNames[object.Name] || seenSources[object.SourcePath] ||
			object.Kind != expectedObject.Kind || filepath.Clean(object.SourcePath) != expectedObject.SourcePath ||
			object.Size < 0 || object.Size > snapshotipc.MaxObjectBytes || !validSHA256(object.SHA256) {
			return fmt.Errorf("backup: required object set mismatch")
		}
		seenNames[object.Name] = true
		seenSources[object.SourcePath] = true
		suffix, suffixErr := backupSuffix(object.Kind)
		expectedPath := filepath.ToSlash(filepath.Join(manifest.Directory, "objects", object.SHA256+suffix))
		if suffixErr != nil || !safeObjectPath(manifest.Directory, object.ObjectPath) ||
			object.ObjectPath != expectedPath {
			return fmt.Errorf("backup: unsafe object path")
		}
		source := BackupSource{Name: object.Name, Kind: object.Kind, Path: object.SourcePath}
		if err := verifyOwnerReceiptBinding(object.OwnerReceipt, source, manifest.ReleaseID, manifest.ConfigSHA256, manifest.BackupRootBinding); err != nil ||
			object.OwnerReceipt.Size != object.Size || object.OwnerReceipt.SHA256 != object.SHA256 {
			return fmt.Errorf("backup: owner receipt mismatch")
		}
		ownerReceipts = append(ownerReceipts, object.OwnerReceipt)
	}
	if err := verifySnapshotSetBinding(manifest.SnapshotSet, ownerReceipts, manifest); err != nil {
		return err
	}
	return nil
}

func VerifyBackupObjects(root candidate.Root, manifest BackupManifest, requirements ...BackupRequirements) error {
	if err := VerifyBackup(manifest, requirements...); err != nil {
		return err
	}
	cap, err := root.OpenCapability()
	if err != nil {
		return err
	}
	defer cap.Close()
	if manifest.BackupRootBinding != cap.Binding() {
		return fmt.Errorf("backup: root binding mismatch")
	}
	seen := map[string]bool{}
	for _, object := range manifest.Objects {
		if !safeName(object.Name) || seen[object.Name] {
			return fmt.Errorf("backup: unsafe or duplicate object name")
		}
		seen[object.Name] = true
		objectRel, err := cap.Relative(object.ObjectPath)
		if err != nil {
			return err
		}
		data, err := cap.ReadFile(objectRel, snapshotipc.MaxObjectBytes, 0o600)
		if err != nil {
			return err
		}
		if int64(len(data)) != object.Size || sha256Hex(data) != object.SHA256 {
			return fmt.Errorf("backup: object mismatch")
		}
	}
	return nil
}

func LoadBackupManifest(root candidate.Root, path string, requirements BackupRequirements) (BackupManifest, error) {
	capability, err := root.OpenCapability()
	if err != nil {
		return BackupManifest{}, err
	}
	defer capability.Close()
	relative, err := capability.Relative(path)
	if err != nil {
		return BackupManifest{}, err
	}
	data, err := capability.ReadFile(relative, maxBackupManifestBytes, 0o600)
	if err != nil {
		return BackupManifest{}, err
	}
	if relative != backupManifestRelative(data) {
		return BackupManifest{}, fmt.Errorf("backup: manifest path is not the fixed content address")
	}
	return VerifyBackupManifest(data, requirements)
}

func backupManifestRelative(data []byte) string {
	return filepath.ToSlash(filepath.Join(backupDirectory, "manifests", sha256Hex(data)+".json"))
}

func oneBackupRequirements(values []BackupRequirements) (BackupRequirements, error) {
	if len(values) != 1 || values[0].RootBinding == "" || values[0].root.Path() == "" {
		return BackupRequirements{}, fmt.Errorf("backup: trusted requirements are required")
	}
	if err := validateRequiredObjects(values[0].Objects); err != nil {
		return BackupRequirements{}, err
	}
	capability, err := values[0].root.OpenCapability()
	if err != nil {
		return BackupRequirements{}, err
	}
	if capability.Binding() != values[0].RootBinding {
		capability.Close()
		return BackupRequirements{}, fmt.Errorf("backup: trusted root binding changed")
	}
	capability.Close()
	for _, object := range values[0].Objects {
		canonical, err := values[0].root.RequireFile("required backup source", object.SourcePath)
		if err != nil || canonical != object.SourcePath {
			return BackupRequirements{}, fmt.Errorf("backup: required source is outside or unsafe for candidate root")
		}
	}
	return values[0], nil
}

func validateRequiredObjects(objects []RequiredObject) error {
	fixed := snapshotipc.FixedObjects()
	if len(objects) != len(fixed) {
		return fmt.Errorf("backup: exactly four fixed required objects are required")
	}
	names := map[string]bool{}
	sources := map[string]bool{}
	for _, object := range objects {
		source := filepath.Clean(object.SourcePath)
		kind, fixedOwner := snapshotipc.FixedKind(object.Name)
		if !fixedOwner || kind != object.Kind || !safeName(object.Name) ||
			!supportedBackupKind(object.Kind) || object.SourcePath == "" ||
			!filepath.IsAbs(source) || source != object.SourcePath ||
			names[object.Name] || sources[source] {
			return fmt.Errorf("backup: invalid required object")
		}
		names[object.Name] = true
		sources[source] = true
	}
	return nil
}

func supportedBackupKind(kind string) bool {
	switch kind {
	case "sqlite", "bolt", "jsonl", "rolling_jsonl":
		return true
	default:
		return false
	}
}

func safeRelativeDirectory(value string) bool {
	clean := filepath.ToSlash(filepath.Clean(value))
	return clean != "" && clean != "." && clean == filepath.ToSlash(value) && !filepath.IsAbs(value) && clean != ".." && !strings.HasPrefix(clean, "../")
}

func safeObjectPath(directory, value string) bool {
	if !safeRelativeDirectory(value) {
		return false
	}
	prefix := strings.TrimSuffix(filepath.ToSlash(directory), "/") + "/objects/"
	relative := filepath.ToSlash(value)
	leaf := strings.TrimPrefix(relative, prefix)
	return strings.HasPrefix(relative, prefix) && safeName(leaf) && !strings.Contains(leaf, "/") && len(leaf) > 64 && validSHA256(leaf[:64])
}

func sqliteReadOnlyDSN(path string) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
}

// sqliteQuickCheck validates a SQLite database that was just fsync'd to disk.
// The pathname-based sql.Open is acceptable here because this is a post-write
// read-only integrity check: the file content is already committed, and modernc
// does not expose an fd-based open.
func sqliteQuickCheck(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(path))
	if err != nil {
		return err
	}
	var result string
	queryErr := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result)
	closeErr := db.Close()
	if err := errors.Join(queryErr, closeErr); err != nil || result != "ok" {
		return fmt.Errorf("sqlite quick_check failed: %v %s", err, result)
	}
	return nil
}
