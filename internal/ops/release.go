package ops

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"

	"golang.org/x/sys/unix"
)

const (
	ReleaseSchema          = "nimbus-release/v2"
	BuildOutputPlaceholder = "{output}"
)

type FileRecord struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type RepoRecord struct {
	Name           string       `json:"name"`
	Root           string       `json:"root"`
	Head           string       `json:"head"`
	WorktreeSHA256 string       `json:"worktree_sha256"`
	Clean          bool         `json:"clean"`
	Files          []FileRecord `json:"files"`
}

type ToolchainRecord struct {
	Executable string `json:"executable"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
}

type BuildRecord struct {
	BuildID        string          `json:"build_id"`
	Artifact       string          `json:"artifact"`
	Repository     string          `json:"repository"`
	Argv           []string        `json:"argv"`
	RepoHead       string          `json:"repo_head"`
	WorktreeSHA256 string          `json:"worktree_sha256"`
	Toolchain      ToolchainRecord `json:"toolchain"`
}

type ArtifactRecord struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	BuildID string `json:"build_id"`
}

type ReleaseManifest struct {
	Schema               string           `json:"schema"`
	ReleaseID            string           `json:"release_id"`
	ConfigSHA256         string           `json:"config_sha256"`
	RegistrySHA256       string           `json:"registry_sha256"`
	WorkspaceSHA256      string           `json:"workspace_sha256"`
	CandidateRootBinding string           `json:"candidate_root_binding"`
	Repositories         []RepoRecord     `json:"repositories"`
	Builds               []BuildRecord    `json:"builds"`
	Artifacts            []ArtifactRecord `json:"artifacts"`
	Include              []string         `json:"include"`
	Exclude              []string         `json:"exclude"`
	MaxFileBytes         int64            `json:"max_file_bytes"`
}

type ArtifactBuildSpec struct {
	Name       string
	Repository string
	Argv       []string
}

type BuildSpec struct {
	Repositories   map[string]string
	Builds         []ArtifactBuildSpec
	Include        []string
	Exclude        []string
	MaxFileBytes   int64
	ConfigSHA256   string
	RegistrySHA256 string
	// BeforeFileRestat is deterministic fault injection for TOCTOU tests.
	BeforeFileRestat func(string)
}

type ReleasePolicy struct {
	Repositories map[string]string
	Builds       []ArtifactBuildSpec
}

type BuildInvocation struct {
	RepositoryName string
	RepositoryRoot string
	Argv           []string
	OutputPath     string
	GoWorkPath     string
}

type Builder interface {
	Build(context.Context, BuildInvocation) (ToolchainRecord, error)
}

type ExecBuilder struct{}

func (ExecBuilder) Build(ctx context.Context, invocation BuildInvocation) (ToolchainRecord, error) {
	if len(invocation.Argv) == 0 || invocation.OutputPath == "" {
		return ToolchainRecord{}, fmt.Errorf("release: invalid build invocation")
	}
	if invocation.Argv[0] != "go" {
		return ToolchainRecord{}, fmt.Errorf("release: only the trusted Go toolchain is allowed")
	}
	const trustedGo = "/usr/local/bin/go"
	command := exec.CommandContext(ctx, trustedGo, invocation.Argv[1:]...)
	command.Dir = invocation.RepositoryRoot
	command.Env = controlledGoEnvironment(invocation.GoWorkPath)
	output, err := command.CombinedOutput()
	if err != nil {
		return ToolchainRecord{}, fmt.Errorf("release build %s: %w: %s", invocation.RepositoryName, err, strings.TrimSpace(string(output)))
	}
	executable := trustedGo
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return ToolchainRecord{}, err
	}
	data, err := readOwnedOrSystemRegular(executable, 128<<20)
	if err != nil {
		return ToolchainRecord{}, err
	}
	versionArgs := []string{"--version"}
	if filepath.Base(executable) == "go" {
		versionArgs = []string{"version"}
	}
	versionCommand := exec.CommandContext(ctx, executable, versionArgs...)
	versionCommand.Env = controlledGoEnvironment(invocation.GoWorkPath)
	version, versionErr := versionCommand.CombinedOutput()
	if versionErr != nil {
		version = []byte("unavailable")
	}
	return ToolchainRecord{Executable: executable, Version: strings.TrimSpace(string(version)), SHA256: sha256Hex(data)}, nil
}

func controlledGoEnvironment(goWorkPath string) []string {
	environment := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"GOWORK=" + goWorkPath,
		"GOENV=off",
		"GOFLAGS=",
		"GOTOOLCHAIN=go1.26.3",
		"CGO_ENABLED=0",
	}
	for _, name := range []string{"HOME", "TMPDIR", "GOCACHE", "GOMODCACHE", "GOPATH", "GOPROXY", "GOSUMDB", "GONOSUMDB", "GOPRIVATE", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value := os.Getenv(name); value != "" && !strings.ContainsAny(value, "\x00\n\r") {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

// ReleaseOptions and BuildRelease are retained only so older candidate
// frontends compile. Prebuilt artifact paths are intentionally rejected.
// Deprecated: use BuildReleaseFromSource with a Builder and BuildSpec.
type ReleaseOptions struct {
	Repositories     map[string]string
	Artifacts        map[string]string
	ArtifactData     map[string][]byte
	Include          []string
	Exclude          []string
	MaxFileBytes     int64
	ConfigSHA256     string
	BeforeFileRestat func(string)
}

func BuildRelease(context.Context, candidate.Root, ReleaseOptions) (ReleaseManifest, string, error) {
	return ReleaseManifest{}, "", fmt.Errorf("release: prebuilt artifacts are forbidden; source builder is required")
}

func BuildReleaseFromSource(ctx context.Context, root candidate.Root, builder Builder, spec BuildSpec) (ReleaseManifest, string, error) {
	if builder == nil || spec.MaxFileBytes <= 0 || !validSHA256(spec.ConfigSHA256) || !validSHA256(spec.RegistrySHA256) || len(spec.Repositories) == 0 || len(spec.Builds) == 0 {
		return ReleaseManifest{}, "", fmt.Errorf("release: builder, config, repositories, and builds are required")
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return ReleaseManifest{}, "", err
	}
	defer capability.Close()
	manifest := ReleaseManifest{
		Schema:               ReleaseSchema,
		ConfigSHA256:         spec.ConfigSHA256,
		RegistrySHA256:       spec.RegistrySHA256,
		CandidateRootBinding: capability.Binding(),
		Include:              append([]string(nil), spec.Include...),
		Exclude:              append([]string(nil), spec.Exclude...),
		MaxFileBytes:         spec.MaxFileBytes,
	}
	goWork := controlledGoWork(spec.Repositories)
	manifest.WorkspaceSHA256 = sha256Hex(goWork)
	sort.Strings(manifest.Include)
	sort.Strings(manifest.Exclude)

	repositories := make(map[string]RepoRecord, len(spec.Repositories))
	for _, name := range sortedKeys(spec.Repositories) {
		if !safeName(name) {
			return ReleaseManifest{}, "", fmt.Errorf("release: unsafe repository name")
		}
		repo, inspectErr := inspectRepo(ctx, name, spec.Repositories[name], manifest.Include, manifest.Exclude, spec.MaxFileBytes, spec.BeforeFileRestat)
		if inspectErr != nil {
			return ReleaseManifest{}, "", inspectErr
		}
		if !repo.Clean {
			return ReleaseManifest{}, "", fmt.Errorf("release repository %s: dirty worktree", name)
		}
		repositories[name] = repo
		manifest.Repositories = append(manifest.Repositories, repo)
	}
	if err := validateBuildSpecs(spec.Builds, repositories); err != nil {
		return ReleaseManifest{}, "", err
	}

	tempRelative, _, err := capability.RandomDir("releases/tmp", "build-")
	if err != nil {
		return ReleaseManifest{}, "", err
	}
	defer capability.RemoveTree(tempRelative)
	workspaceRelative := tempRelative + "/workspace"
	if err := capability.MkdirAll(workspaceRelative, 0o700); err != nil {
		return ReleaseManifest{}, "", err
	}
	for _, name := range sortedKeys(spec.Repositories) {
		if err := materializeRepository(ctx, capability, workspaceRelative, repositories[name], manifest.Include, manifest.Exclude, spec.MaxFileBytes); err != nil {
			return ReleaseManifest{}, "", err
		}
	}
	goWorkRelative := workspaceRelative + "/go.work"
	goWorkFile, err := capability.CreateExclusive(goWorkRelative, 0o600)
	if err != nil {
		return ReleaseManifest{}, "", err
	}
	if _, err := goWorkFile.Write(goWork); err != nil {
		goWorkFile.Close()
		return ReleaseManifest{}, "", err
	}
	if err := errors.Join(goWorkFile.Sync(), goWorkFile.Close()); err != nil {
		return ReleaseManifest{}, "", err
	}
	goWorkPath, err := capability.Absolute(goWorkRelative)
	if err != nil {
		return ReleaseManifest{}, "", err
	}
	builds := append([]ArtifactBuildSpec(nil), spec.Builds...)
	sort.Slice(builds, func(i, j int) bool { return builds[i].Name < builds[j].Name })
	for _, build := range builds {
		repo := repositories[build.Repository]
		outputRelative := tempRelative + "/" + build.Name
		outputPath, err := capability.Absolute(outputRelative)
		if err != nil {
			return ReleaseManifest{}, "", err
		}
		argv := make([]string, len(build.Argv))
		for i, value := range build.Argv {
			if value == BuildOutputPlaceholder {
				argv[i] = outputPath
			} else {
				argv[i] = value
			}
		}
		snapshotRepo, err := capability.Absolute(workspaceRelative + "/" + build.Repository)
		if err != nil {
			return ReleaseManifest{}, "", err
		}
		toolchain, err := builder.Build(ctx, BuildInvocation{RepositoryName: build.Repository, RepositoryRoot: snapshotRepo, Argv: argv, OutputPath: outputPath, GoWorkPath: goWorkPath})
		if err != nil {
			return ReleaseManifest{}, "", err
		}
		if err := validateToolchain(toolchain); err != nil {
			return ReleaseManifest{}, "", err
		}
		data, err := capability.ReadFile(outputRelative, spec.MaxFileBytes, 0o755)
		if err != nil {
			return ReleaseManifest{}, "", fmt.Errorf("release artifact %s: %w", build.Name, err)
		}
		for _, currentName := range sortedKeys(spec.Repositories) {
			expected := repositories[currentName]
			current, err := inspectRepo(ctx, expected.Name, expected.Root, manifest.Include, manifest.Exclude, spec.MaxFileBytes, nil)
			if err != nil {
				return ReleaseManifest{}, "", err
			}
			if !sameRepoRecord(expected, current) {
				return ReleaseManifest{}, "", fmt.Errorf("release repository %s changed during build", expected.Name)
			}
		}
		record := BuildRecord{Artifact: build.Name, Repository: repo.Name, Argv: append([]string(nil), build.Argv...), RepoHead: repo.Head, WorktreeSHA256: repo.WorktreeSHA256, Toolchain: toolchain}
		buildPayload, _ := canonicalJSON(record)
		record.BuildID = sha256Hex(buildPayload)
		objectPath, err := capability.WriteCAS("releases/objects", filepath.Ext(build.Name), data)
		if err != nil {
			return ReleaseManifest{}, "", err
		}
		objectRelative, err := capability.Relative(objectPath)
		if err != nil {
			return ReleaseManifest{}, "", err
		}
		manifest.Builds = append(manifest.Builds, record)
		manifest.Artifacts = append(manifest.Artifacts, ArtifactRecord{Name: build.Name, Path: objectRelative, Mode: "600", SHA256: sha256Hex(data), Size: int64(len(data)), BuildID: record.BuildID})
	}
	payload, err := canonicalJSON(manifest)
	if err != nil {
		return ReleaseManifest{}, "", err
	}
	manifest.ReleaseID = sha256Hex(payload)
	payload, err = canonicalJSON(manifest)
	if err != nil {
		return ReleaseManifest{}, "", err
	}
	path, err := capability.WriteCAS("releases/manifests", ".json", payload)
	return manifest, path, err
}

func VerifyRelease(data []byte) (ReleaseManifest, error) {
	var manifest ReleaseManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return manifest, fmt.Errorf("release: extra JSON value")
		}
		return manifest, err
	}
	if manifest.Schema != ReleaseSchema || manifest.ReleaseID == "" || !validSHA256(manifest.ConfigSHA256) || !validSHA256(manifest.RegistrySHA256) || !validSHA256(manifest.WorkspaceSHA256) || manifest.CandidateRootBinding == "" || manifest.MaxFileBytes <= 0 || len(manifest.Repositories) == 0 || len(manifest.Builds) == 0 || len(manifest.Artifacts) == 0 {
		return manifest, fmt.Errorf("release: invalid schema or required values")
	}
	want := manifest.ReleaseID
	manifest.ReleaseID = ""
	payload, _ := canonicalJSON(manifest)
	manifest.ReleaseID = want
	if sha256Hex(payload) != want {
		return manifest, fmt.Errorf("release: id mismatch")
	}
	builds := map[string]BuildRecord{}
	repositoryRecords := make(map[string]RepoRecord, len(manifest.Repositories))
	for _, repository := range manifest.Repositories {
		if repository.Name == "" || repository.Root == "" || repository.Head == "" || !repository.Clean || repository.WorktreeSHA256 == "" {
			return manifest, fmt.Errorf("release: invalid repository provenance")
		}
		if _, exists := repositoryRecords[repository.Name]; exists {
			return manifest, fmt.Errorf("release: duplicate repository")
		}
		repositoryRecords[repository.Name] = repository
	}
	buildSpecs := make([]ArtifactBuildSpec, 0, len(manifest.Builds))
	for _, build := range manifest.Builds {
		copy := build
		id := copy.BuildID
		copy.BuildID = ""
		record, _ := canonicalJSON(copy)
		if id == "" || sha256Hex(record) != id {
			return manifest, fmt.Errorf("release: build provenance mismatch")
		}
		repository, repositoryOK := repositoryRecords[build.Repository]
		if _, exists := builds[id]; exists || !repositoryOK || build.RepoHead != repository.Head || build.WorktreeSHA256 != repository.WorktreeSHA256 {
			return manifest, fmt.Errorf("release: duplicate build")
		}
		if err := validateToolchain(build.Toolchain); err != nil {
			return manifest, err
		}
		builds[id] = build
		buildSpecs = append(buildSpecs, ArtifactBuildSpec{Name: build.Artifact, Repository: build.Repository, Argv: build.Argv})
	}
	if err := validateBuildSpecs(buildSpecs, repositoryRecords); err != nil {
		return manifest, err
	}
	names := map[string]bool{}
	for _, artifact := range manifest.Artifacts {
		build, ok := builds[artifact.BuildID]
		if !ok || build.Artifact != artifact.Name || !safeName(artifact.Name) || names[artifact.Name] || !safeReleaseObjectPath(artifact.Path) || artifact.Size < 0 || artifact.Mode != "600" || len(artifact.SHA256) != 64 {
			return manifest, fmt.Errorf("release: artifact provenance mismatch")
		}
		names[artifact.Name] = true
	}
	return manifest, nil
}

func VerifyReleaseArtifacts(root candidate.Root, manifest ReleaseManifest) error {
	if !validSHA256(manifest.RegistrySHA256) {
		return fmt.Errorf("release: invalid registry hash")
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return err
	}
	defer capability.Close()
	if manifest.CandidateRootBinding != capability.Binding() {
		return fmt.Errorf("release: candidate root binding mismatch")
	}
	for _, artifact := range manifest.Artifacts {
		data, err := capability.ReadFile(artifact.Path, manifest.MaxFileBytes, 0o600)
		if err != nil {
			return fmt.Errorf("release artifact %s: %w", artifact.Name, err)
		}
		if int64(len(data)) != artifact.Size || sha256Hex(data) != artifact.SHA256 {
			return fmt.Errorf("release artifact %s: metadata/hash mismatch", artifact.Name)
		}
	}
	return nil
}

func VerifyReleasePolicy(manifest ReleaseManifest, policy ReleasePolicy) error {
	if len(policy.Repositories) == 0 || len(policy.Builds) == 0 || len(manifest.Repositories) != len(policy.Repositories) || len(manifest.Builds) != len(policy.Builds) {
		return fmt.Errorf("release: trusted source/build policy mismatch")
	}
	expectedRepositories := make(map[string]string, len(policy.Repositories))
	for name, root := range policy.Repositories {
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil {
			return fmt.Errorf("release: trusted repository %s: %w", name, err)
		}
		expectedRepositories[name] = filepath.Clean(canonical)
	}
	for _, repository := range manifest.Repositories {
		if expectedRepositories[repository.Name] != repository.Root {
			return fmt.Errorf("release: trusted repository mismatch")
		}
	}
	if manifest.WorkspaceSHA256 != sha256Hex(controlledGoWork(policy.Repositories)) {
		return fmt.Errorf("release: trusted workspace mismatch")
	}
	expectedBuilds := make(map[string]ArtifactBuildSpec, len(policy.Builds))
	for _, build := range policy.Builds {
		expectedBuilds[build.Name] = build
	}
	for _, build := range manifest.Builds {
		expected, ok := expectedBuilds[build.Artifact]
		if !ok || build.Repository != expected.Repository || !slices.Equal(build.Argv, expected.Argv) {
			return fmt.Errorf("release: trusted build target mismatch")
		}
	}
	return nil
}

func VerifyReleaseCurrent(ctx context.Context, manifest ReleaseManifest) error {
	for _, expected := range manifest.Repositories {
		current, err := inspectRepo(ctx, expected.Name, expected.Root, manifest.Include, manifest.Exclude, manifest.MaxFileBytes, nil)
		if err != nil {
			return err
		}
		if !sameRepoRecord(expected, current) {
			return fmt.Errorf("release repository %s changed after manifest build", expected.Name)
		}
	}
	return nil
}

func validateBuildSpecs(builds []ArtifactBuildSpec, repositories map[string]RepoRecord) error {
	seen := map[string]bool{}
	for _, build := range builds {
		if !safeName(build.Name) || seen[build.Name] {
			return fmt.Errorf("release: invalid or duplicate build name")
		}
		seen[build.Name] = true
		if _, ok := repositories[build.Repository]; !ok {
			return fmt.Errorf("release: build repository is not verified")
		}
		placeholders := 0
		for _, value := range build.Argv {
			if value == "" || strings.ContainsAny(value, "\x00\n\r") {
				return fmt.Errorf("release: unsafe build argv")
			}
			if value == BuildOutputPlaceholder {
				placeholders++
			}
		}
		if len(build.Argv) == 0 || placeholders != 1 {
			return fmt.Errorf("release: build argv requires exactly one output placeholder")
		}
		if len(build.Argv) != 6 || build.Argv[0] != "go" || build.Argv[1] != "build" || build.Argv[2] != "-trimpath" || build.Argv[3] != "-o" || build.Argv[4] != BuildOutputPlaceholder || build.Argv[5] != "./cmd/"+build.Name {
			return fmt.Errorf("release: build argv is not an approved fixed Go build")
		}
	}
	return nil
}

func validateToolchain(toolchain ToolchainRecord) error {
	if toolchain.Executable == "" || toolchain.Version == "" || len(toolchain.SHA256) != 64 {
		return fmt.Errorf("release: incomplete toolchain provenance")
	}
	if _, err := hex.DecodeString(toolchain.SHA256); err != nil {
		return fmt.Errorf("release: invalid toolchain hash")
	}
	return nil
}

func sameRepoRecord(expected, current RepoRecord) bool {
	return expected.Head == current.Head && expected.Clean && current.Clean && expected.WorktreeSHA256 == current.WorktreeSHA256
}

func controlledGoWork(repositories map[string]string) []byte {
	var value strings.Builder
	value.WriteString("go 1.26.3\n\nuse (\n")
	for _, name := range sortedKeys(repositories) {
		value.WriteString("\t./")
		value.WriteString(name)
		value.WriteByte('\n')
	}
	value.WriteString(")\n")
	return []byte(value.String())
}

func materializeRepository(ctx context.Context, capability *candidate.Capability, workspaceRelative string, repository RepoRecord, include, exclude []string, maxFileBytes int64) error {
	destination := workspaceRelative + "/" + repository.Name
	if err := capability.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "git", "archive", "--format=tar", repository.Head)
	command.Dir = repository.Root
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return err
	}
	extractErr := extractRepositoryArchive(capability, destination, tar.NewReader(stdout), include, exclude, maxFileBytes)
	waitErr := command.Wait()
	if extractErr != nil {
		return extractErr
	}
	if waitErr != nil {
		return fmt.Errorf("release: materialize repository %s: %w: %s", repository.Name, waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func extractRepositoryArchive(capability *candidate.Capability, destination string, archive *tar.Reader, include, exclude []string, maxFileBytes int64) error {
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Clean(header.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("release: unsafe repository archive path")
		}
		if !includedPath(name, include, exclude) {
			continue
		}
		relative := destination + "/" + name
		switch header.Typeflag {
		case tar.TypeDir:
			if err := capability.MkdirAll(relative, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxFileBytes {
				return fmt.Errorf("release: source file exceeds max_file_bytes")
			}
			file, err := capability.CreateExclusive(relative, 0o600)
			if err != nil {
				return err
			}
			written, writeErr := io.CopyN(file, archive, header.Size)
			closeErr := file.Close()
			if err := errors.Join(writeErr, closeErr); err != nil || written != header.Size {
				return errors.Join(err, fmt.Errorf("release: incomplete repository archive file"))
			}
		case tar.TypeSymlink:
			return fmt.Errorf("release: source symlinks are not allowed in build inputs")
		case tar.TypeXHeader, tar.TypeXGlobalHeader:
			continue
		default:
			return fmt.Errorf("release: unsupported repository archive entry %d", header.Typeflag)
		}
	}
}

func inspectRepo(ctx context.Context, name, root string, include, exclude []string, max int64, hook func(string)) (RepoRecord, error) {
	canonical, directory, info, err := openRepository(root)
	if err != nil {
		return RepoRecord{}, err
	}
	defer directory.Close()
	head, err := git(ctx, canonical, "rev-parse", "HEAD")
	if err != nil {
		return RepoRecord{}, fmt.Errorf("release repo %s: %w", name, err)
	}
	status, err := git(ctx, canonical, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return RepoRecord{}, err
	}
	listed, err := git(ctx, canonical, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return RepoRecord{}, err
	}
	paths := splitNUL(listed)
	sort.Strings(paths)
	record := RepoRecord{Name: name, Root: canonical, Head: strings.TrimSpace(head), Clean: len(status) == 0}
	for _, path := range paths {
		if !includedPath(path, include, exclude) {
			continue
		}
		file, err := inspectRepoFile(directory, path, max, hook)
		if err != nil {
			return record, fmt.Errorf("release repo %s %s: %w", name, path, err)
		}
		record.Files = append(record.Files, file)
	}
	currentPath, current, currentInfo, err := openRepository(canonical)
	if err != nil {
		return RepoRecord{}, err
	}
	_ = current.Close()
	if currentPath != canonical || !os.SameFile(info, currentInfo) {
		return RepoRecord{}, fmt.Errorf("release repository root changed while inspecting")
	}
	payload, _ := canonicalJSON(record.Files)
	record.WorktreeSHA256 = sha256Hex(payload)
	return record, nil
}

func openRepository(path string) (string, *os.File, os.FileInfo, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, nil, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", nil, nil, err
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, nil, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(canonical, string(filepath.Separator)), string(filepath.Separator)) {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return "", nil, nil, openErr
		}
		fd = next
	}
	directory := os.NewFile(uintptr(fd), canonical)
	if directory == nil {
		_ = unix.Close(fd)
		return "", nil, nil, fmt.Errorf("release: invalid repository descriptor")
	}
	info, err := directory.Stat()
	if err != nil {
		directory.Close()
		return "", nil, nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(stat.Uid) != os.Geteuid() {
		directory.Close()
		return "", nil, nil, fmt.Errorf("release: repository must be a current-user directory")
	}
	return canonical, directory, info, nil
}

func includedPath(path string, include, exclude []string) bool {
	match := func(rule string) bool {
		rule = filepath.ToSlash(filepath.Clean(rule))
		path = filepath.ToSlash(path)
		if rule == "." {
			return true
		}
		if rule == "" {
			return false
		}
		if path == rule || strings.HasPrefix(path, strings.TrimSuffix(rule, "/")+"/") {
			return true
		}
		matched, err := filepath.Match(rule, path)
		return err == nil && matched
	}
	selected := len(include) == 0
	for _, rule := range include {
		if match(rule) {
			selected = true
			break
		}
	}
	if !selected {
		return false
	}
	for _, rule := range exclude {
		if match(rule) {
			return false
		}
	}
	return true
}

func inspectRepoFile(root *os.File, path string, max int64, hook func(string)) (FileRecord, error) {
	path = filepath.ToSlash(path)
	if filepath.Clean(path) != filepath.FromSlash(path) || filepath.IsAbs(path) || strings.HasPrefix(path, "../") {
		return FileRecord{}, fmt.Errorf("unsafe path")
	}
	parent, name, err := openRepoParent(root, path)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return FileRecord{Path: path, Kind: "deleted"}, nil
		}
		return FileRecord{}, err
	}
	defer unix.Close(parent)
	var before unix.Stat_t
	if err := unix.Fstatat(parent, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return FileRecord{Path: path, Kind: "deleted"}, nil
		}
		return FileRecord{}, err
	}
	record := FileRecord{Path: path, Mode: strconv.FormatUint(uint64(before.Mode), 8)}
	var data []byte
	switch before.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		buffer := make([]byte, 4096)
		n, err := unix.Readlinkat(parent, name, buffer)
		if err != nil {
			return record, err
		}
		data = buffer[:n]
		record.Kind = "symlink"
	case unix.S_IFREG:
		fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return record, err
		}
		file := os.NewFile(uintptr(fd), path)
		if file == nil {
			_ = unix.Close(fd)
			return record, fmt.Errorf("release: invalid source descriptor")
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return record, err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 || info.Size() > max {
			file.Close()
			return record, fmt.Errorf("release: unsafe or oversized source file")
		}
		data, err = io.ReadAll(io.LimitReader(file, max+1))
		if err != nil {
			file.Close()
			return record, err
		}
		if int64(len(data)) > max {
			file.Close()
			return record, fmt.Errorf("file exceeds max_file_bytes")
		}
		if hook != nil {
			hook(filepath.Join(root.Name(), filepath.FromSlash(path)))
		}
		afterInfo, err := file.Stat()
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return record, errors.Join(err, closeErr)
		}
		if !os.SameFile(info, afterInfo) || info.Size() != afterInfo.Size() || info.Mode() != afterInfo.Mode() || !info.ModTime().Equal(afterInfo.ModTime()) {
			return record, fmt.Errorf("file changed while hashing")
		}
		record.Kind = "file"
		record.Size = int64(len(data))
	default:
		return record, fmt.Errorf("unsupported file type")
	}
	if hook != nil && record.Kind == "symlink" {
		hook(filepath.Join(root.Name(), filepath.FromSlash(path)))
	}
	var after unix.Stat_t
	if err := unix.Fstatat(parent, name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode || before.Size != after.Size {
		return record, fmt.Errorf("file changed while hashing")
	}
	sum := sha256.Sum256(data)
	record.SHA256 = hex.EncodeToString(sum[:])
	return record, nil
}

func openRepoParent(root *os.File, path string) (int, string, error) {
	parts := strings.Split(path, "/")
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return -1, "", err
	}
	for _, component := range parts[:len(parts)-1] {
		if component == "" || component == "." || component == ".." {
			unix.Close(fd)
			return -1, "", fmt.Errorf("unsafe path component")
		}
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(fd)
		if err != nil {
			return -1, "", err
		}
		fd = next
	}
	return fd, parts[len(parts)-1], nil
}

func safeReleaseObjectPath(value string) bool {
	clean := filepath.ToSlash(filepath.Clean(value))
	return clean == value && strings.HasPrefix(clean, "releases/objects/") && !strings.Contains(strings.TrimPrefix(clean, "releases/objects/"), "/")
}

func readOwnedOrSystemRegular(path string, max int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("release: invalid toolchain descriptor")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > max {
		return nil, fmt.Errorf("release: invalid toolchain executable")
	}
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil || int64(len(data)) > max {
		return nil, fmt.Errorf("release: toolchain executable exceeds limit")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || info.Mode() != after.Mode() || !info.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("release: toolchain changed while hashing")
	}
	return data, nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	out, err := command.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func splitNUL(value string) []string {
	parts := strings.Split(value, "\x00")
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
