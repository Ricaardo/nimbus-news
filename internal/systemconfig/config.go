package systemconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

type Candidate struct {
	Root       string `yaml:"root" json:"root"`
	Marker     string `yaml:"marker" json:"marker"`
	ReleaseDir string `yaml:"release_dir" json:"release_dir"`
	// EvidenceDir is the root evidence directory. Each release receives its own
	// subdirectory named "release-<first 12 hex chars of release ID>" to
	// prevent evidence-chain corruption across releases.
	EvidenceDir string `yaml:"evidence_dir" json:"evidence_dir"`
	BackupDir   string `yaml:"backup_dir" json:"backup_dir"`
	RestoreDir  string `yaml:"restore_dir" json:"restore_dir"`
}
type Runtime struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	Argv    []string `yaml:"argv" json:"argv"`
}
type Command struct {
	Argv []string `yaml:"argv" json:"argv"`
}
type Release struct {
	MaxFileBytes int64    `yaml:"max_file_bytes" json:"max_file_bytes"`
	Include      []string `yaml:"include" json:"include"`
	Exclude      []string `yaml:"exclude" json:"exclude"`
}
type BackupObject struct {
	Name string `yaml:"name" json:"name"`
	Kind string `yaml:"kind" json:"kind"`
	Path string `yaml:"path" json:"path"`
}
type Backup struct {
	RequiredObjects []BackupObject `yaml:"required_objects" json:"required_objects"`
}
type Config struct {
	Version       int                `yaml:"version" json:"version"`
	PortsRegistry string             `yaml:"ports_registry" json:"ports_registry"`
	Candidate     Candidate          `yaml:"candidate" json:"candidate"`
	Runtimes      map[string]Runtime `yaml:"runtimes" json:"runtimes"`
	Commands      map[string]Command `yaml:"commands" json:"commands"`
	Release       Release            `yaml:"release" json:"release"`
	Backup        Backup             `yaml:"backup" json:"backup"`
	Path          string             `yaml:"-" json:"-"`
	rawSnapshot   string
	rawSHA256     string
}

func Load(path string) (Config, error) {
	absolute, data, err := readConfigSnapshot(path)
	if err != nil {
		return Config{}, err
	}
	if err := rejectDuplicateYAML(data); err != nil {
		return Config{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("system config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, fmt.Errorf("system config: multiple documents")
	}
	cfg.Path = absolute
	cfg.rawSnapshot = string(data)
	rawHash := sha256.Sum256(data)
	cfg.rawSHA256 = hex.EncodeToString(rawHash[:])
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Resolve(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(c.Path), path))
}

func (c Config) RepositoryRoot() string {
	return filepath.Clean(filepath.Dir(filepath.Dir(c.Path)))
}

func (c Config) WorkspaceRoot() string {
	return filepath.Clean(filepath.Dir(c.RepositoryRoot()))
}

func (c Config) PortsRegistryPath() string {
	return filepath.Join(c.WorkspaceRoot(), "docs", "ports.yaml")
}

func (c Config) CandidateRootPath() string {
	return c.Resolve(c.Candidate.Root)
}

func (c Config) BackupObjects() []BackupObject {
	out := make([]BackupObject, len(c.Backup.RequiredObjects))
	for i, object := range c.Backup.RequiredObjects {
		out[i] = object
		out[i].Path = filepath.Join(c.CandidateRootPath(), filepath.FromSlash(object.Path))
	}
	return out
}

func (c Config) ProtectedProductionPaths() []string {
	workspace := c.WorkspaceRoot()
	return []string{
		workspace,
		filepath.Join(workspace, "news", "data", "platform.db"),
		filepath.Join(workspace, "nimbus", "workspace", "feed", "breaking.jsonl"),
		filepath.Join(workspace, "nimbus", "workspace", "feed", "breaking.v2.jsonl"),
		filepath.Join(workspace, "services", "signal-gateway", "store", "signals.jsonl"),
		filepath.Join(workspace, "data", "control.db"),
		filepath.Join(workspace, "datasources", "data", "control.db"),
		filepath.Join(workspace, "news", "data", "control.db"),
	}
}

// Fingerprint binds the exact raw bytes and the parsed effective config. The
// strict grammar forbids secret/interpolation fields, so both are safe release
// artifacts without storing credentials.
func (c Config) Fingerprint() (string, []byte, []byte, error) {
	if c.rawSnapshot == "" || c.rawSHA256 == "" {
		return "", nil, nil, fmt.Errorf("system config: missing validated snapshot")
	}
	effective, err := json.Marshal(c)
	if err != nil {
		return "", nil, nil, err
	}
	effective = append(effective, '\n')
	effectiveHash := sha256.Sum256(effective)
	bound := sha256.Sum256([]byte(c.rawSHA256 + ":" + hex.EncodeToString(effectiveHash[:])))
	raw := []byte(c.rawSnapshot)
	return hex.EncodeToString(bound[:]), append([]byte(nil), raw...), effective, nil
}

func readConfigSnapshot(path string) (string, []byte, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil, fmt.Errorf("system config: path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	absolute = filepath.Clean(absolute)
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, err
	}
	parts := strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return "", nil, fmt.Errorf("system config: unsafe path component %q: %w", component, openErr)
		}
		fd = next
	}
	fileFD, err := unix.Openat(fd, parts[len(parts)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	_ = unix.Close(fd)
	if err != nil {
		return "", nil, err
	}
	file := os.NewFile(uintptr(fileFD), absolute)
	if file == nil {
		_ = unix.Close(fileFD)
		return "", nil, fmt.Errorf("system config: invalid descriptor")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return "", nil, err
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || !before.Mode().IsRegular() || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return "", nil, fmt.Errorf("system config: must be a current-user regular file with one link")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil {
		return "", nil, err
	}
	if len(data) == 4<<20 {
		return "", nil, fmt.Errorf("system config: file is too large")
	}
	after, err := file.Stat()
	if err != nil {
		return "", nil, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return "", nil, fmt.Errorf("system config: changed while reading")
	}
	return absolute, data, nil
}

// releasePrefix returns the first 12 hex characters of a release ID, safe for
// use as a path component. Release IDs are always SHA-256 hex hashes.
func releasePrefix(releaseID string) string {
	var buf [12]byte
	n := 0
	for _, r := range releaseID {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			buf[n] = byte(r)
			n++
			if n == 12 {
				return string(buf[:])
			}
		}
	}
	if n == 0 {
		return "unknown"
	}
	return string(buf[:n])
}

// ReleasePrefix returns the first 12 hex characters of a release ID, safe for
// use as a path component.
func ReleasePrefix(releaseID string) string { return releasePrefix(releaseID) }

// EvidenceDir returns the per-release evidence directory derived from the release
// fingerprint, ensuring different releases cannot corrupt each other's evidence
// chains.
// Corresponds to ops.EvidenceDirForRelease(); keep the two in sync.
func (c Config) EvidenceDir(releaseID string) string {
	return filepath.Join(c.CandidateRootPath(), filepath.FromSlash(c.Candidate.EvidenceDir), "release-"+releasePrefix(releaseID))
}

func (c Config) validate() error {
	if c.Version != 1 || c.PortsRegistry == "" || c.Candidate.Root == "" || c.Candidate.Marker != ".nimbus-candidate-root" || c.Candidate.ReleaseDir == "" || c.Candidate.EvidenceDir == "" || c.Candidate.BackupDir == "" || c.Candidate.RestoreDir == "" || c.Release.MaxFileBytes <= 0 || len(c.Release.Include) == 0 || len(c.Release.Exclude) == 0 || len(c.Backup.RequiredObjects) == 0 {
		return fmt.Errorf("system config: invalid required values")
	}
	if c.Candidate.ReleaseDir != "releases" || c.Candidate.EvidenceDir != "evidence" || c.Candidate.BackupDir != "backups" || c.Candidate.RestoreDir != "restores" {
		return fmt.Errorf("system config: candidate operation directories are fixed")
	}
	if c.Resolve(c.PortsRegistry) != c.PortsRegistryPath() {
		return fmt.Errorf("system config: ports_registry must be the workspace docs/ports.yaml")
	}
	values := []string{c.PortsRegistry, c.Candidate.Root, c.Candidate.ReleaseDir, c.Candidate.EvidenceDir, c.Candidate.BackupDir, c.Candidate.RestoreDir}
	for _, v := range values {
		if unsafeScalar(v) {
			return fmt.Errorf("system config: interpolation, secret, or shell syntax is forbidden")
		}
	}
	for _, value := range append(append([]string{}, c.Release.Include...), c.Release.Exclude...) {
		if unsafeScalar(value) {
			return fmt.Errorf("system config: unsafe release path rule")
		}
	}
	for name, runtime := range c.Runtimes {
		if unsafeScalar(name) {
			return fmt.Errorf("system config: unsafe runtime name")
		}
		if runtime.Enabled {
			return fmt.Errorf("system config: runtime %s must remain disabled", name)
		}
		if err := validateArgv(runtime.Argv); err != nil {
			return err
		}
	}
	for name, command := range c.Commands {
		if unsafeScalar(name) || len(command.Argv) != 1 || filepath.Base(command.Argv[0]) != name {
			return fmt.Errorf("system config: command %s requires argv", name)
		}
		if err := validateArgv(command.Argv); err != nil {
			return err
		}
	}
	names := map[string]bool{}
	paths := map[string]bool{}
	fixedBackupKinds := map[string]string{
		"news": "bolt", "control": "sqlite",
		"signals": "jsonl", "shadow_feed": "rolling_jsonl",
	}
	if len(c.Backup.RequiredObjects) != len(fixedBackupKinds) {
		return fmt.Errorf("system config: exactly four fixed backup objects are required")
	}
	for _, object := range c.Backup.RequiredObjects {
		path := filepath.ToSlash(filepath.Clean(object.Path))
		if unsafeScalar(object.Name) || unsafeScalar(object.Kind) || unsafeScalar(object.Path) || object.Name == "" || object.Path == "" || filepath.IsAbs(object.Path) || path == "." || path == ".." || strings.HasPrefix(path, "../") || path != object.Path || names[object.Name] || paths[path] {
			return fmt.Errorf("system config: invalid backup required object")
		}
		expectedKind, fixed := fixedBackupKinds[object.Name]
		if !fixed || object.Kind != expectedKind {
			return fmt.Errorf("system config: invalid fixed backup object name or kind")
		}
		names[object.Name] = true
		paths[path] = true
	}
	return nil
}
func validateArgv(argv []string) error {
	for _, value := range argv {
		if value == "" || unsafeScalar(value) {
			return fmt.Errorf("system config: unsafe argv value")
		}
	}
	return nil
}
func unsafeScalar(v string) bool {
	lower := strings.ToLower(v)
	return strings.Contains(v, "${") || strings.Contains(v, "$(") || strings.Contains(v, "`") || strings.ContainsAny(v, "\n\r;|&><") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "api_key") || strings.Contains(lower, "token")
}

func rejectDuplicateYAML(data []byte) error {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}
	var walk func(*yaml.Node) error
	walk = func(n *yaml.Node) error {
		if n.Kind == yaml.MappingNode {
			seen := map[string]struct{}{}
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i].Value
				if _, ok := seen[key]; ok {
					return fmt.Errorf("system config: duplicate key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(n.Content[i+1]); err != nil {
					return err
				}
			}
		} else {
			for _, child := range n.Content {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(&node)
}

type PortRegistry struct {
	Services []PortService `yaml:"services"`
	Retired  []PortService `yaml:"retired"`
}

type PortRegistrySnapshot struct {
	Path     string       `json:"path"`
	SHA256   string       `json:"sha256"`
	Registry PortRegistry `json:"registry"`
	raw      []byte
}
type PortService struct {
	Name       string `yaml:"name"`
	Port       int    `yaml:"port"`
	Lang       string `yaml:"lang"`
	Launchd    any    `yaml:"launchd"`
	Health     any    `yaml:"health"`
	Retired    any    `yaml:"retired"`
	ReplacedBy any    `yaml:"replaced_by"`
}

func LoadPorts(path string) (PortRegistry, error) {
	_, data, err := readConfigSnapshot(path)
	if err != nil {
		return PortRegistry{}, err
	}
	return parsePorts(data)
}

func (c Config) LoadPortsSnapshot() (PortRegistrySnapshot, error) {
	path := c.PortsRegistryPath()
	absolute, data, err := readConfigSnapshot(path)
	if err != nil {
		return PortRegistrySnapshot{}, err
	}
	if absolute != path || c.Resolve(c.PortsRegistry) != path {
		return PortRegistrySnapshot{}, fmt.Errorf("ports registry: authority path mismatch")
	}
	registry, err := parsePorts(data)
	if err != nil {
		return PortRegistrySnapshot{}, err
	}
	sum := sha256.Sum256(data)
	return PortRegistrySnapshot{Path: path, SHA256: hex.EncodeToString(sum[:]), Registry: registry, raw: append([]byte(nil), data...)}, nil
}

func (s PortRegistrySnapshot) Verify() error {
	if s.Path == "" || len(s.raw) == 0 {
		return fmt.Errorf("ports registry: trusted snapshot is required")
	}
	sum := sha256.Sum256(s.raw)
	if hex.EncodeToString(sum[:]) != s.SHA256 {
		return fmt.Errorf("ports registry: snapshot hash mismatch")
	}
	registry, err := parsePorts(s.raw)
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(registry)
	current, _ := json.Marshal(s.Registry)
	if !bytes.Equal(encoded, current) {
		return fmt.Errorf("ports registry: parsed snapshot mismatch")
	}
	return nil
}

func (s PortRegistrySnapshot) Raw() []byte {
	return append([]byte(nil), s.raw...)
}

func parsePorts(data []byte) (PortRegistry, error) {
	if err := rejectDuplicateYAML(data); err != nil {
		return PortRegistry{}, err
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	var registry PortRegistry
	if err := d.Decode(&registry); err != nil {
		return PortRegistry{}, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return PortRegistry{}, fmt.Errorf("ports registry: multiple documents")
	}
	seen := map[int]string{}
	for _, s := range append(append([]PortService{}, registry.Services...), registry.Retired...) {
		if strings.TrimSpace(s.Name) == "" || s.Port < 1 || s.Port > 65535 {
			return PortRegistry{}, fmt.Errorf("ports registry: invalid port")
		}
		if old, ok := seen[s.Port]; ok {
			return PortRegistry{}, fmt.Errorf("ports registry: duplicate port %d (%s/%s)", s.Port, old, s.Name)
		}
		seen[s.Port] = s.Name
	}
	return registry, nil
}
func (r PortRegistry) Contains(port int) bool {
	for _, s := range r.Services {
		if s.Port == port {
			return true
		}
	}
	for _, s := range r.Retired {
		if s.Port == port {
			return true
		}
	}
	return false
}
