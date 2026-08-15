package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
	"github.com/Ricaardo/nimbus-os/news/internal/systemconfig"
)

func TestRuntimeCandidatesAreExplicitOptInAndDegradeIndependently(t *testing.T) {
	config := validCandidateConfig(t, canonicalTempDir(t))
	if got := startRuntimeCandidates(context.Background(), config); len(got) != 0 {
		t.Fatalf("disabled runtimes started: %d", len(got))
	}
	config.AgentRuntimeCommand = "not a JSON command"
	if got := startRuntimeCandidates(context.Background(), config); len(got) != 0 {
		t.Fatalf("invalid degraded runtime started: %d", len(got))
	}
}

func TestSnapshotOwnersComeOnlyFromFixedBackupConfig(t *testing.T) {
	cfg, err := systemconfig.Load(filepath.Join("..", "..", "config", "system.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	objects := expectedSnapshotObjects(cfg)
	if len(objects) != 4 {
		t.Fatalf("snapshot objects=%d", len(objects))
	}
	want := []struct {
		name string
		kind string
	}{
		{name: "news", kind: "bolt"},
		{name: "control", kind: "sqlite"},
		{name: "signals", kind: "jsonl"},
		{name: "shadow_feed", kind: "rolling_jsonl"},
	}
	for index, object := range objects {
		if object.Name != want[index].name || object.Kind != want[index].kind ||
			!filepath.IsAbs(object.SourcePath) {
			t.Fatalf("snapshot object[%d]=%+v", index, object)
		}
	}
}

func TestCandidateRejectsAllActiveAndRetiredPorts(t *testing.T) {
	registry := registryForTest(t)
	ports := []int{6901, 8001, 8081, 8765, 8766, 8787, 8788, 8800, 8810, 8812, 8814, 8820, 8821, 8822, 8824, 8830, 11111}
	for _, port := range ports {
		if !registry.Contains(port) {
			t.Fatalf("registered port %d is not reserved", port)
		}
	}
	for _, port := range ports {
		address := fmt.Sprintf("127.0.0.1:%d", port)
		port, err := candidatePort(address)
		if err != nil {
			t.Fatalf("candidatePort(%q): %v", address, err)
		}
		if !registry.Contains(port) {
			t.Fatalf("address %q bypassed guard as %d", address, port)
		}
		config := validCandidateConfig(t, canonicalTempDir(t))
		config.NewsAddr = address
		if _, err := validateCandidateConfig(config); err == nil {
			t.Fatalf("candidate config accepted registered address %q", address)
		}
	}
}

func TestCandidatePortGuardTracksRegistryWithoutCodeList(t *testing.T) {
	registry := registryForTest(t)
	for _, service := range append(append([]systemconfig.PortService{}, registry.Services...), registry.Retired...) {
		if !registry.Contains(service.Port) {
			t.Fatalf("registry port %d drifted", service.Port)
		}
	}
	if registry.Contains(19081) {
		t.Fatal("candidate-only port unexpectedly registered")
	}
}

func TestCandidatePortCanonicalParsing(t *testing.T) {
	for _, test := range []struct {
		address string
		want    int
	}{
		{"127.0.0.1:18081", 18081},
		{"127.255.255.254:18082", 18082},
		{"[::1]:18083", 18083},
	} {
		got, err := candidatePort(test.address)
		if err != nil || got != test.want {
			t.Fatalf("candidatePort(%q)=%d,%v want %d", test.address, got, err, test.want)
		}
	}
	for _, address := range []string{
		"0.0.0.0:18081",
		"[::]:18081",
		"192.168.1.2:18081",
		"8.8.8.8:18081",
		"localhost:18081",
		"example.invalid:18081",
		":18081",
		"127.0.0.1:08081",
		"127.0.0.1:http",
		"127.0.0.1:",
		"::1:18081",
		"127.0.0.1:0",
		"127.0.0.1:99999",
	} {
		if _, err := candidatePort(address); err == nil {
			t.Fatalf("invalid address %q accepted", address)
		}
	}
}

func TestCandidatePathRejectionDoesNotOpenProtectedFiles(t *testing.T) {
	base := canonicalTempDir(t)
	root := filepath.Join(base, "candidate")
	production := filepath.Join(base, "production")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(production, 0o755); err != nil {
		t.Fatal(err)
	}
	fixed := time.Unix(1_700_000_000, 0)
	protected := map[string]string{}
	for _, name := range []string{"platform.db", "breaking.jsonl", "signals.jsonl", "control.db"} {
		path := filepath.Join(production, name)
		if err := os.WriteFile(path, []byte("production-sentinel-"+name), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, fixed, fixed); err != nil {
			t.Fatal(err)
		}
		protected[name] = path
	}

	baseConfig := validCandidateConfig(t, root)
	tests := []struct {
		name   string
		mutate func(*candidateConfig)
	}{
		{"news store", func(c *candidateConfig) { c.NewsStore = protected["platform.db"] }},
		{"shadow feed", func(c *candidateConfig) { c.ShadowFeed = protected["breaking.jsonl"] }},
		{"signal store", func(c *candidateConfig) { c.SignalStore = protected["signals.jsonl"] }},
		{"control db", func(c *candidateConfig) { c.ControlDB = protected["control.db"] }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := baseConfig
			test.mutate(&config)
			if _, err := validateCandidateConfig(config); err == nil {
				t.Fatal("production path accepted")
			}
			for name, path := range protected {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if !info.ModTime().Equal(fixed) || info.Size() != int64(len("production-sentinel-"+name)) {
					t.Fatalf("protected file %s was modified: mtime=%s size=%d", name, info.ModTime(), info.Size())
				}
			}
		})
	}
}

func TestWarehouseCandidatePathsAreOptionalPairedAndIsolated(t *testing.T) {
	base := canonicalTempDir(t)
	root := filepath.Join(base, "candidate")
	if err := os.MkdirAll(filepath.Join(root, "warehouse", "staging"), 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "warehouse", "staging", "manifest.json")
	if err := os.WriteFile(manifest, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	config := validCandidateConfig(t, root)
	validated, err := validateCandidateConfig(config)
	if err != nil || validated.WarehouseRoot != "" || validated.WarehouseManifest != "" {
		t.Fatalf("disabled warehouse: %+v %v", validated, err)
	}
	config.WarehouseRoot = filepath.Join(root, "warehouse")
	if _, err := validateCandidateConfig(config); err == nil {
		t.Fatal("unpaired warehouse config accepted")
	}
	config.WarehouseManifest = manifest
	validated, err = validateCandidateConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	wantWarehouseRoot, _ := filepath.EvalSymlinks(filepath.Join(root, "warehouse"))
	wantManifest, _ := filepath.EvalSymlinks(manifest)
	if validated.WarehouseRoot != wantWarehouseRoot || validated.WarehouseManifest != wantManifest {
		t.Fatalf("warehouse paths not canonicalized: %+v", validated)
	}
	config.WarehouseManifest = filepath.Join(base, "outside.json")
	if _, err := validateCandidateConfig(config); err == nil {
		t.Fatal("warehouse manifest outside candidate root accepted")
	}
}

func validCandidateConfig(t *testing.T, root string) candidateConfig {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, candidate.MarkerName)); errors.Is(err, os.ErrNotExist) {
		if err := candidate.InitRoot(root); err != nil {
			t.Fatal(err)
		}
	}
	return candidateConfig{
		ConfigPath: "config.yaml", CandidateRoot: root,
		NewsAddr: "127.0.0.1:18081", DataAddr: "127.0.0.1:18821", DataplaneAddr: "[::1]:18800",
		NewsStore: filepath.Join(root, "news.db"), ShadowFeed: filepath.Join(root, "breaking.jsonl"),
		SignalStore: filepath.Join(root, "signals.jsonl"), ControlDB: filepath.Join(root, "control.db"),
		ConfiguredRoot: root, Registry: registryForTest(t),
	}
}

func registryForTest(t *testing.T) systemconfig.PortRegistry {
	t.Helper()
	cfg, err := systemconfig.Load(filepath.Join("..", "..", "config", "system.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := cfg.LoadPortsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Registry
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
