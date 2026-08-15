package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
	"github.com/Ricaardo/nimbus-os/news/internal/ops"
	"github.com/Ricaardo/nimbus-os/news/internal/systemconfig"
)

func TestCandidateProcessSnapshotSmoke(t *testing.T) {
	if os.Getenv("NIMBUS_PROCESS_SMOKE") != "1" {
		t.Skip("set NIMBUS_PROCESS_SMOKE=1 to run isolated child-process smoke")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	temporary, err := os.MkdirTemp(realTempBase(t), "nimbus-process-smoke-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	if err := os.Chmod(temporary, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(temporary, "workspace")
	newsRepo := filepath.Join(workspace, "news")
	configDir := filepath.Join(newsRepo, "config")
	candidatePath := filepath.Join(newsRepo, "candidate")
	for _, directory := range []string{
		filepath.Join(workspace, "docs"), configDir, candidatePath,
		filepath.Join(newsRepo, "cmd", "nimbusctl"), filepath.Join(newsRepo, "cmd", "nimbusd"),
		filepath.Join(workspace, "datasources"), filepath.Join(workspace, "guanfu"),
		filepath.Join(temporary, "bin"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	portsData, err := os.ReadFile(filepath.Join(moduleRoot, "..", "docs", "ports.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	writeSmokeFile(t, filepath.Join(workspace, "docs", "ports.yaml"), portsData)
	systemConfigPath := filepath.Join(configDir, "system.yaml")
	writeSmokeFile(t, systemConfigPath, []byte(smokeSystemConfig))
	newsConfigPath := filepath.Join(configDir, "news.yaml")
	writeSmokeFile(t, newsConfigPath, []byte("{}\n"))
	writeSmokeFile(t, filepath.Join(newsRepo, ".gitignore"), []byte("candidate/\n"))
	writeSmokeFile(t, filepath.Join(newsRepo, "cmd", "nimbusctl", "main.go"), []byte("package main\n"))
	writeSmokeFile(t, filepath.Join(newsRepo, "cmd", "nimbusd", "main.go"), []byte("package main\n"))
	writeSmokeFile(t, filepath.Join(workspace, "datasources", "README.md"), []byte("smoke\n"))
	writeSmokeFile(t, filepath.Join(workspace, "guanfu", "README.md"), []byte("smoke\n"))
	for _, repository := range []string{newsRepo, filepath.Join(workspace, "datasources"), filepath.Join(workspace, "guanfu")} {
		initSmokeRepo(t, ctx, repository)
	}

	nimbusctlPath := filepath.Join(temporary, "bin", "nimbusctl")
	nimbusdPath := filepath.Join(temporary, "bin", "nimbusd")
	runSmokeCommand(t, ctx, moduleRoot, "go", "build", "-o", nimbusctlPath, "./cmd/nimbusctl")
	runSmokeCommand(t, ctx, moduleRoot, "go", "build", "-o", nimbusdPath, "./cmd/nimbusd")
	runSmokeCommand(t, ctx, "", nimbusctlPath, "--config", systemConfigPath, "candidate", "init")

	cfg, err := systemconfig.Load(systemConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := candidate.NewRoot(candidatePath, cfg.ProtectedProductionPaths()...)
	if err != nil {
		t.Fatal(err)
	}
	configHash, _, _, err := cfg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := cfg.LoadPortsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	policy := smokeReleasePolicy(workspace)
	release, releasePath, err := ops.BuildReleaseFromSource(ctx, root, processSmokeBuilder{}, ops.BuildSpec{
		Repositories: policy.Repositories, Builds: policy.Builds,
		Include: []string{"cmd"}, Exclude: []string{"data"}, MaxFileBytes: 1 << 20,
		ConfigSHA256: configHash, RegistrySHA256: registry.SHA256,
	})
	if err != nil {
		t.Fatal(err)
	}

	ports := reserveSmokePorts(t, registry.Registry, 3)
	if err := os.MkdirAll(filepath.Join(candidatePath, "signals"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(candidatePath, "feed"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeSmokeFile(t, filepath.Join(candidatePath, "feed", "shadow.jsonl"), nil)
	daemon := exec.CommandContext(ctx, nimbusdPath,
		"--system-config", systemConfigPath,
		"--config", newsConfigPath,
		"--candidate-root", candidatePath,
		"--news-addr", fmt.Sprintf("127.0.0.1:%d", ports[0]),
		"--data-addr", fmt.Sprintf("127.0.0.1:%d", ports[1]),
		"--dataplane-addr", fmt.Sprintf("127.0.0.1:%d", ports[2]),
		"--news-store", filepath.Join(candidatePath, "news.db"),
		"--control-db", filepath.Join(candidatePath, "control.db"),
		"--signal-store", filepath.Join(candidatePath, "signals", "events.jsonl"),
		"--shadow-feed", filepath.Join(candidatePath, "feed", "shadow.jsonl"),
	)
	var daemonLog bytes.Buffer
	daemon.Stdout = &daemonLog
	daemon.Stderr = &daemonLog
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	daemonDone := make(chan error, 1)
	go func() { daemonDone <- daemon.Wait() }()
	stopped := false
	defer func() {
		if stopped {
			return
		}
		if err := daemon.Process.Signal(syscall.SIGTERM); err != nil {
			return
		}
		select {
		case <-daemonDone:
		case <-time.After(5 * time.Second):
			_ = daemon.Process.Kill()
			select {
			case <-daemonDone:
			case <-time.After(time.Second):
			}
		}
	}()
	socketPath := filepath.Join(candidatePath, "run", "snapshot-v1.sock")
	waitForSmokeSocket(t, ctx, socketPath, daemonDone, &daemonLog)

	backupOutput, backupErr := smokeCommand(ctx, "", nimbusctlPath,
		"--config", systemConfigPath, "backup", "--release", release.ReleaseID)
	if backupErr != nil {
		t.Fatalf("backup: %v\n%s\nnimbusd:\n%s", backupErr, backupOutput, daemonLog.String())
	}
	var backupEnvelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Backup ops.BackupManifest `json:"backup"`
			Path   string             `json:"path"`
		} `json:"result"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(backupOutput, &backupEnvelope); err != nil || !backupEnvelope.OK {
		t.Fatalf("backup output: %v %s", err, backupOutput)
	}
	if backupEnvelope.Result.Backup.Schema != ops.BackupSchema ||
		len(backupEnvelope.Result.Backup.Objects) != 4 {
		t.Fatalf("incomplete process backup: %+v", backupEnvelope.Result.Backup)
	}

	restoreOutput := runSmokeCommand(t, ctx, "", nimbusctlPath,
		"--config", systemConfigPath, "restore-verify", "--manifest", backupEnvelope.Result.Path)
	var restoreEnvelope struct {
		OK     bool              `json:"ok"`
		Result ops.RestoreResult `json:"result"`
	}
	if err := json.Unmarshal(restoreOutput, &restoreEnvelope); err != nil || !restoreEnvelope.OK ||
		!restoreEnvelope.Result.Verified {
		t.Fatalf("restore output: %v %s", err, restoreOutput)
	}

	preflightOutput, preflightErr := smokeCommand(ctx, "", nimbusctlPath,
		"--config", systemConfigPath, "preflight",
		"--release", releasePath, "--backup", backupEnvelope.Result.Path)
	if preflightErr == nil {
		t.Fatal("preflight unexpectedly passed without time-bound evidence")
	}
	var preflightEnvelope struct {
		OK     bool       `json:"ok"`
		Result ops.Report `json:"result"`
		Error  string     `json:"error"`
	}
	if err := json.Unmarshal(preflightOutput, &preflightEnvelope); err != nil {
		t.Fatalf("preflight output: %v %s", err, preflightOutput)
	}
	checks := make(map[string]ops.Check, len(preflightEnvelope.Result.Checks))
	for _, check := range preflightEnvelope.Result.Checks {
		checks[check.Name] = check
	}
	if !checks["release"].OK || !checks["backup"].OK || checks["evidence"].OK ||
		strings.Contains(string(preflightOutput), ops.ErrOwnerSnapshotRequired) ||
		strings.Contains(string(preflightOutput), ops.ErrBoltOnlineRequired) ||
		strings.Contains(string(preflightOutput), ops.ErrSQLiteOwnerRequired) {
		t.Fatalf("unexpected preflight gates: %s", preflightOutput)
	}

	if err := daemon.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-daemonDone:
		if err != nil {
			t.Fatalf("nimbusd did not stop cleanly: %v\n%s", err, daemonLog.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("nimbusd did not stop\n%s", daemonLog.String())
	}
	stopped = true
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("snapshot socket remained after SIGTERM: %v", err)
	}
	offlineOutput, offlineErr := smokeCommand(ctx, "", nimbusctlPath,
		"--config", systemConfigPath, "backup", "--release", release.ReleaseID)
	if offlineErr == nil {
		t.Fatalf("offline backup unexpectedly succeeded: %s", offlineOutput)
	}
	if entries, err := os.ReadDir(filepath.Join(candidatePath, "backups", "objects", "staging")); err != nil || len(entries) != 0 {
		t.Fatalf("snapshot staging not clean: entries=%d err=%v", len(entries), err)
	}
	if entries, err := os.ReadDir(filepath.Join(candidatePath, "backups", "manifests")); err != nil || len(entries) != 1 {
		t.Fatalf("unexpected manifests: entries=%d err=%v", len(entries), err)
	}
}

type processSmokeBuilder struct{}

func (processSmokeBuilder) Build(_ context.Context, invocation ops.BuildInvocation) (ops.ToolchainRecord, error) {
	content := []byte("smoke artifact: " + filepath.Base(invocation.OutputPath) + "\n")
	if err := os.WriteFile(invocation.OutputPath, content, 0o755); err != nil {
		return ops.ToolchainRecord{}, err
	}
	sum := sha256.Sum256([]byte("process-smoke-toolchain"))
	return ops.ToolchainRecord{
		Executable: "/usr/local/bin/go", Version: "process-smoke",
		SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func smokeReleasePolicy(workspace string) ops.ReleasePolicy {
	return ops.ReleasePolicy{
		Repositories: map[string]string{
			"news": filepath.Join(workspace, "news"), "datasources": filepath.Join(workspace, "datasources"),
			"guanfu": filepath.Join(workspace, "guanfu"),
		},
		Builds: []ops.ArtifactBuildSpec{
			{Name: "nimbusctl", Repository: "news", Argv: []string{"go", "build", "-trimpath", "-o", ops.BuildOutputPlaceholder, "./cmd/nimbusctl"}},
			{Name: "nimbusd", Repository: "news", Argv: []string{"go", "build", "-trimpath", "-o", ops.BuildOutputPlaceholder, "./cmd/nimbusd"}},
		},
	}
}

func reserveSmokePorts(t *testing.T, registry systemconfig.PortRegistry, count int) []int {
	t.Helper()
	ports := make([]int, 0, count)
	seen := make(map[int]bool, count)
	for len(ports) < count {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		if !registry.Contains(port) && !seen[port] {
			ports = append(ports, port)
			seen[port] = true
		}
	}
	return ports
}

func waitForSmokeSocket(t *testing.T, ctx context.Context, path string, daemonDone <-chan error, log *bytes.Buffer) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 &&
			info.Mode().Perm() == 0o600 {
			return
		}
		select {
		case err := <-daemonDone:
			t.Fatalf("nimbusd exited before socket: %v\n%s", err, log.String())
		case <-ctx.Done():
			t.Fatalf("snapshot socket timeout: %v\n%s", ctx.Err(), log.String())
		case <-ticker.C:
		}
	}
}

func initSmokeRepo(t *testing.T, ctx context.Context, directory string) {
	t.Helper()
	runSmokeCommand(t, ctx, directory, "git", "init", "-q")
	runSmokeCommand(t, ctx, directory, "git", "add", ".")
	runSmokeCommand(t, ctx, directory, "git", "-c", "user.name=Nimbus Smoke", "-c", "user.email=smoke@nimbus.invalid", "commit", "-q", "-m", "smoke")
}

func runSmokeCommand(t *testing.T, ctx context.Context, directory, name string, args ...string) []byte {
	t.Helper()
	output, err := smokeCommand(ctx, directory, name, args...)
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return output
}

func smokeCommand(ctx context.Context, directory, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	return command.CombinedOutput()
}

func writeSmokeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

const smokeSystemConfig = `version: 1
ports_registry: ../../docs/ports.yaml
candidate:
  root: ../candidate
  marker: .nimbus-candidate-root
  release_dir: releases
  evidence_dir: evidence
  backup_dir: backups
  restore_dir: restores
runtimes: {}
commands:
  git: {argv: [git]}
  go: {argv: [go]}
release:
  max_file_bytes: 1048576
  include: [cmd]
  exclude: [data]
backup:
  required_objects:
    - {name: news, kind: bolt, path: news.db}
    - {name: control, kind: sqlite, path: control.db}
    - {name: signals, kind: jsonl, path: signals/events.jsonl}
    - {name: shadow_feed, kind: rolling_jsonl, path: feed/shadow.jsonl}
`
