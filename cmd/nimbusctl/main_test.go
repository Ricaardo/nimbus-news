package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
	"github.com/Ricaardo/nimbus-os/news/internal/ops"
	"github.com/Ricaardo/nimbus-os/news/internal/snapshotipc"
	"github.com/Ricaardo/nimbus-os/news/internal/systemconfig"
)

func TestStatusIsJSONAndReadOnly(t *testing.T) {
	old := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	code := run([]string{"--config", filepath.Join("..", "..", "config", "system.yaml"), "status"})
	_ = write.Close()
	os.Stdout = old
	data, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	_ = read.Close()
	if code != 0 {
		t.Fatalf("status exit=%d output=%s", code, data)
	}
	var result output
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("not JSON: %v %s", err, data)
	}
	if !result.OK {
		t.Fatalf("status failed: %+v", result)
	}
}

func TestCandidateInitCreatesExclusiveKeyWithoutPrintingIt(t *testing.T) {
	configPath, rootPath, _ := testSystemConfig(t)
	code, data := captureRun(t, []string{"--config", configPath, "candidate", "init"})
	if code != 0 {
		t.Fatalf("candidate init exit=%d output=%s", code, data)
	}
	keyPath := filepath.Join(rootPath, "cutover", "operator.hmac")
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 || info.Mode().Perm() != 0o600 || bytes.Contains(data, key) {
		t.Fatalf("unsafe operator key: len=%d mode=%o output=%s", len(key), info.Mode().Perm(), data)
	}
	code, _ = captureRun(t, []string{"--config", configPath, "candidate", "init"})
	if code == 0 {
		t.Fatal("candidate init overwrote marker/key")
	}
	recoveryConfig, recoveryRoot, _ := testSystemConfig(t)
	if err := candidate.InitRoot(recoveryRoot); err != nil {
		t.Fatal(err)
	}
	code, data = captureRun(t, []string{"--config", recoveryConfig, "candidate", "init"})
	if code != 0 {
		t.Fatalf("partial marker state was not recoverable: %d %s", code, data)
	}
	if key, err := os.ReadFile(filepath.Join(recoveryRoot, "cutover", "operator.hmac")); err != nil || len(key) != 32 {
		t.Fatalf("recovered key: len=%d err=%v", len(key), err)
	}
}

func TestMutatingRootOverrideAndProductionAncestorAreRejected(t *testing.T) {
	configPath, rootPath, workspace := testSystemConfig(t)
	if err := candidate.InitRoot(rootPath); err != nil {
		t.Fatal(err)
	}
	code, data := captureRun(t, []string{"--config", configPath, "--candidate-root", workspace, "backup", "--release", "r", "--config-hash", "c"})
	if code == 0 || !strings.Contains(string(data), ops.ErrProductionMutationForbidden) {
		t.Fatalf("mutating override accepted: %d %s", code, data)
	}
	if err := candidate.InitRoot(workspace); err != nil {
		t.Fatal(err)
	}
	cfg, err := systemconfig.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openCommandRoot(cfg, workspace, true); err == nil {
		t.Fatal("marked production ancestor accepted")
	}
}

func TestReadOnlyOverrideStillRejectsSymlinkAndReleaseBuildHasNoPrebuiltFlags(t *testing.T) {
	configPath, rootPath, workspace := testSystemConfig(t)
	if err := candidate.InitRoot(rootPath); err != nil {
		t.Fatal(err)
	}
	root, err := candidate.NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ops.InitializeOperatorKey(root, bytes.NewReader(bytes.Repeat([]byte{1}, 32))); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "candidate-link")
	if err := os.Symlink(rootPath, link); err != nil {
		t.Fatal(err)
	}
	code, _ := captureRun(t, []string{"--config", configPath, "--candidate-root", link, "release", "verify", "--manifest", "missing.json"})
	if code == 0 {
		t.Fatal("symlinked read-only root accepted")
	}
	code, data := captureRun(t, []string{"--config", configPath, "release", "build", "--nimbusd", "/tmp/prebuilt"})
	if code == 0 || !strings.Contains(string(data), "flag provided but not defined") {
		t.Fatalf("prebuilt release flag accepted: %d %s", code, data)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "releases")); !os.IsNotExist(err) {
		t.Fatalf("release builder ran after forbidden flag: %v", err)
	}
}

func TestBackupUsesOwnerSnapshotSocketAndPublishesV2Manifest(t *testing.T) {
	configPath, rootPath, _ := testSystemConfig(t)
	if err := candidate.InitRoot(rootPath); err != nil {
		t.Fatal(err)
	}
	root, err := candidate.NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := systemconfig.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configHash, _, _, err := cfg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	expected := snapshotExpectedObjects(cfg)
	resultKinds := map[string]string{
		"news": "bolt", "control": "sqlite",
		"signals": "signals_jsonl", "shadow_feed": "news_feed_jsonl_v1",
	}
	owners := make([]snapshotipc.Owner, 0, len(expected))
	for _, object := range expected {
		object := object
		owners = append(owners, snapshotipc.Owner{
			Name: object.Name, Kind: object.Kind, ResultKind: resultKinds[object.Name],
			SourcePath: object.SourcePath,
			Snapshot: func(_ context.Context, destination io.Writer) (snapshotipc.OwnerResult, error) {
				content := []byte(object.Name + " snapshot\n")
				if _, err := destination.Write(content); err != nil {
					return snapshotipc.OwnerResult{}, err
				}
				sum := sha256.Sum256(content)
				result := snapshotipc.OwnerResult{
					Kind: resultKinds[object.Name], SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content)),
				}
				if object.Name == "control" {
					result.SchemaVersion = snapshotipc.ControlSchemaVersion
				}
				return result, nil
			},
		})
	}
	server, err := snapshotipc.NewServer(snapshotipc.ServerConfig{
		Root: root, ConfigSHA256: configHash, Expected: expected, Owners: owners,
	})
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(serverCtx) }()
	defer func() {
		cancel()
		_ = server.Close()
		<-done
	}()

	code, data := captureRun(t, []string{"--config", configPath, "backup", "--release", "release-1"})
	if code != 0 {
		t.Fatalf("owner backup exit=%d output=%s", code, data)
	}
	entries, err := os.ReadDir(filepath.Join(rootPath, "backups", "manifests"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("manifest entries=%d err=%v", len(entries), err)
	}
	manifestPath := filepath.Join(rootPath, "backups", "manifests", entries[0].Name())
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := backupRequirements(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ops.VerifyBackupManifest(manifestData, requirements)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != ops.BackupSchema || len(manifest.Objects) != 4 ||
		len(manifest.SnapshotSet.ReceiptHashes) != 4 {
		t.Fatalf("incomplete owner manifest: %+v", manifest)
	}

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	code, _ = captureRun(t, []string{"--config", configPath, "backup", "--release", "release-2"})
	if code == 0 {
		t.Fatal("backup succeeded without owner daemon")
	}
	entries, err = os.ReadDir(filepath.Join(rootPath, "backups", "manifests"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("daemon refusal published manifest: entries=%d err=%v", len(entries), err)
	}
}

func captureRun(t *testing.T, args []string) (int, []byte) {
	t.Helper()
	old := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	code := run(args)
	_ = write.Close()
	os.Stdout = old
	data, err := io.ReadAll(read)
	_ = read.Close()
	if err != nil {
		t.Fatal(err)
	}
	return code, data
}

func testSystemConfig(t *testing.T) (string, string, string) {
	t.Helper()
	temporary, err := os.MkdirTemp(realTempBase(t), "nimbusctl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	workspace, err := filepath.EvalSymlinks(temporary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(workspace, "news", "config")
	rootPath := filepath.Join(workspace, "news", "candidate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	ports, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "ports.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "docs", "ports.yaml"), ports, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "system.yaml")
	config := "version: 1\nports_registry: ../../docs/ports.yaml\ncandidate:\n  root: ../candidate\n  marker: .nimbus-candidate-root\n  release_dir: releases\n  evidence_dir: evidence\n  backup_dir: backups\n  restore_dir: restores\nruntimes: {}\ncommands:\n  git: {argv: [git]}\n  go: {argv: [go]}\nrelease:\n  max_file_bytes: 1048576\n  include: [cmd]\n  exclude: [data]\nbackup:\n  required_objects:\n    - {name: news, kind: bolt, path: news.db}\n    - {name: control, kind: sqlite, path: control.db}\n    - {name: signals, kind: jsonl, path: signals/events.jsonl}\n    - {name: shadow_feed, kind: rolling_jsonl, path: feed/shadow.jsonl}\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, rootPath, workspace
}

func realTempBase(t *testing.T) string {
	t.Helper()
	var best string
	for _, value := range []string{os.TempDir(), "/tmp"} {
		absolute, err := filepath.Abs(value)
		if err != nil {
			continue
		}
		canonical, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			continue
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() {
			continue
		}
		if best == "" || len(canonical) < len(best) {
			best = canonical
		}
	}
	if best == "" {
		t.Fatal("no real temporary base directory")
	}
	return best
}
