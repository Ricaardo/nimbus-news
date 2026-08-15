package systemconfig

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStrictAndPorts(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config", "system.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtimes["agent"].Enabled || cfg.Runtimes["embedding"].Enabled {
		t.Fatal("runtimes must default disabled")
	}
	ports, err := LoadPorts(cfg.Resolve(cfg.PortsRegistry))
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{6901, 8081, 8787, 8812, 8820, 8830, 11111} {
		if !ports.Contains(port) {
			t.Fatalf("registry omitted %d", port)
		}
	}
}

func TestLoadRejectsUnknownDuplicateInterpolationAndCommandString(t *testing.T) {
	cases := []string{
		"version: 1\nunknown: true\n",
		"version: 1\nversion: 1\n",
		"version: 1\nports_registry: ${PORTS}\n",
		"version: 1\ncommands:\n  git:\n    command: git status\n",
	}
	for i, data := range cases {
		path := filepath.Join(t.TempDir(), "bad.yaml")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestPortsRejectDuplicateAndUnknown(t *testing.T) {
	for _, data := range []string{
		"services: [{name: a, port: 1}, {name: b, port: 1}]\nretired: []\n",
		"services: []\nretired: []\nextra: true\n",
	} {
		path := filepath.Join(t.TempDir(), "ports.yaml")
		_ = os.WriteFile(path, []byte(data), 0o600)
		if _, err := LoadPorts(path); err == nil {
			t.Fatal("invalid ports accepted")
		}
	}
}

func TestLoadRejectsEnabledRuntimeAndShellCommand(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "system.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, replacement := range map[string]string{"runtime": strings.Replace(string(data), "enabled: false", "enabled: true", 1), "shell": strings.Replace(string(data), "argv: [git]", "argv: [sh, -c, git]", 1)} {
		path := filepath.Join(t.TempDir(), name+".yaml")
		if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("%s config accepted", name)
		}
	}
}

func TestEvidenceDir(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(workspace, "news", "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "system.yaml")
	config := []byte("version: 1\nports_registry: ../../docs/ports.yaml\ncandidate:\n  root: " + workspace + "\n  marker: .nimbus-candidate-root\n  release_dir: releases\n  evidence_dir: evidence\n  backup_dir: backups\n  restore_dir: restores\nrelease:\n  max_file_bytes: 10485760\n  include: [\"*.go\"]\n  exclude: [\"vendor\"]\nbackup:\n  required_objects:\n    - {name: news, kind: bolt, path: news.db}\n    - {name: control, kind: sqlite, path: control.db}\n    - {name: signals, kind: jsonl, path: signals.jsonl}\n    - {name: shadow_feed, kind: rolling_jsonl, path: shadow.jsonl}\n")
	if err := os.WriteFile(cfgPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		releaseID string
		want      string
	}{
		{"abcdef1234567890abcdef1234567890abcdef12", filepath.Join(workspace, "evidence", "release-abcdef123456")},
		{"ab", filepath.Join(workspace, "evidence", "release-ab")},
		{"ab!@cd12xy", filepath.Join(workspace, "evidence", "release-abcd12")},
		{"!!!", filepath.Join(workspace, "evidence", "release-unknown")},
	}
	for _, tt := range tests {
		got := cfg.EvidenceDir(tt.releaseID)
		if got != tt.want {
			t.Errorf("EvidenceDir(%q) = %q, want %q", tt.releaseID, got, tt.want)
		}
	}
}

func TestFingerprintUsesValidatedImmutableSnapshot(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(workspace, "news", "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "system.yaml")
	data := []byte("# SNAPSHOT_PRIVATE_MARKER\nversion: 1\nports_registry: ../../docs/ports.yaml\ncandidate:\n  root: candidate\n  marker: .nimbus-candidate-root\n  release_dir: releases\n  evidence_dir: evidence\n  backup_dir: backups\n  restore_dir: restores\nrelease:\n  max_file_bytes: 1024\n  include: [cmd]\n  exclude: [vendor]\nbackup:\n  required_objects:\n    - {name: news, kind: bolt, path: news.db}\n    - {name: control, kind: sqlite, path: control.db}\n    - {name: signals, kind: jsonl, path: signals.jsonl}\n    - {name: shadow_feed, kind: rolling_jsonl, path: shadow.jsonl}\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	firstHash, firstRaw, firstEffective, err := cfg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	firstRaw[0] = '!'
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing.yaml"), path); err != nil {
		t.Fatal(err)
	}
	secondHash, secondRaw, secondEffective, err := cfg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash || !bytes.Equal(secondRaw, data) || !bytes.Equal(firstEffective, secondEffective) {
		t.Fatal("fingerprint changed after validated path replacement")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("SNAPSHOT_PRIVATE_MARKER")) || bytes.Contains(firstEffective, []byte("SNAPSHOT_PRIVATE_MARKER")) {
		t.Fatal("private raw snapshot leaked through JSON")
	}
	if _, err := Load(path); err == nil {
		t.Fatal("symlink config accepted")
	}
}

func TestPortsAuthoritySnapshotAndRequiredBackupCannotBeOverridden(t *testing.T) {
	systemData, err := os.ReadFile(filepath.Join("..", "..", "config", "system.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	portsData, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "ports.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	configPath, portsPath := writeSystemLayout(t, systemData, portsData)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	first, err := cfg.LoadPortsSnapshot()
	if err != nil || first.Verify() != nil || len(first.Raw()) == 0 {
		t.Fatalf("first registry snapshot: %+v %v", first, err)
	}
	if err := os.WriteFile(portsPath, append(portsData, []byte("\n# changed\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := cfg.LoadPortsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 == second.SHA256 || first.Verify() != nil || second.Verify() != nil {
		t.Fatal("registry snapshot did not bind immutable raw bytes")
	}

	override := strings.Replace(string(systemData), "ports_registry: ../../docs/ports.yaml", "ports_registry: /tmp/ports.yaml", 1)
	overridePath, _ := writeSystemLayout(t, []byte(override), portsData)
	if _, err := Load(overridePath); err == nil {
		t.Fatal("ports registry override accepted")
	}
	empty := string(systemData[:bytes.Index(systemData, []byte("backup:"))]) + "backup:\n  required_objects: []\n"
	emptyPath, _ := writeSystemLayout(t, []byte(empty), portsData)
	if _, err := Load(emptyPath); err == nil {
		t.Fatal("empty required backup object set accepted")
	}
	for name, changed := range map[string]string{
		"missing": strings.Replace(string(systemData), "    - {name: shadow_feed, kind: rolling_jsonl, path: feed/shadow.jsonl}\n", "", 1),
		"renamed": strings.Replace(string(systemData), "name: shadow_feed", "name: renamed_feed", 1),
		"kind":    strings.Replace(string(systemData), "name: news, kind: bolt", "name: news, kind: sqlite", 1),
	} {
		path, _ := writeSystemLayout(t, []byte(changed), portsData)
		if _, err := Load(path); err == nil {
			t.Fatalf("%s fixed backup object mutation accepted", name)
		}
	}
}

func writeSystemLayout(t *testing.T, config, ports []byte) (string, string) {
	t.Helper()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(workspace, "news", "config")
	docsDir := filepath.Join(workspace, "docs")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(docsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "system.yaml")
	portsPath := filepath.Join(docsDir, "ports.yaml")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(portsPath, ports, 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, portsPath
}
