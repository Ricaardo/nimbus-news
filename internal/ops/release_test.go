package ops

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeBuilder struct {
	calls      int
	data       []byte
	invocation BuildInvocation
	hook       func(BuildInvocation) error
}

func (b *fakeBuilder) Build(_ context.Context, invocation BuildInvocation) (ToolchainRecord, error) {
	b.calls++
	b.invocation = invocation
	if b.hook != nil {
		if err := b.hook(invocation); err != nil {
			return ToolchainRecord{}, err
		}
	}
	if err := os.WriteFile(invocation.OutputPath, b.data, 0o755); err != nil {
		return ToolchainRecord{}, err
	}
	return ToolchainRecord{Executable: "/test/go", Version: "go test", SHA256: strings.Repeat("a", 64)}, nil
}

func TestReleaseBuildsFromCleanSourceAndBindsProvenance(t *testing.T) {
	repo := cleanReleaseRepo(t)
	root := candidateRoot(t)
	builder := &fakeBuilder{data: []byte("built-from-source")}
	manifest, path, err := BuildReleaseFromSource(context.Background(), root, builder, BuildSpec{
		Repositories:   map[string]string{"news": repo},
		Builds:         []ArtifactBuildSpec{{Name: "nimbusd", Repository: "news", Argv: approvedBuildArgv("./cmd/nimbusd")}},
		Include:        []string{"cmd"},
		Exclude:        []string{"excluded"},
		MaxFileBytes:   128,
		ConfigSHA256:   strings.Repeat("c", 64),
		RegistrySHA256: strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if builder.calls != 1 || len(manifest.Builds) != 1 || manifest.Artifacts[0].BuildID != manifest.Builds[0].BuildID {
		t.Fatalf("missing build provenance: %+v", manifest)
	}
	if manifest.Repositories[0].Head == "" || manifest.Repositories[0].WorktreeSHA256 == "" || !manifest.Repositories[0].Clean || manifest.Builds[0].Toolchain.Version == "" {
		t.Fatalf("incomplete provenance: %+v", manifest)
	}
	capability, err := root.OpenCapability()
	if err != nil {
		t.Fatal(err)
	}
	data, err := capability.ReadFile(path, 1<<20, 0o600)
	capability.Close()
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyRelease(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyReleaseArtifacts(root, verified); err != nil {
		t.Fatal(err)
	}
	if builder.invocation.RepositoryRoot == repo || !strings.HasPrefix(builder.invocation.RepositoryRoot, root.Path()+string(filepath.Separator)) || !strings.HasPrefix(builder.invocation.GoWorkPath, root.Path()+string(filepath.Separator)) {
		t.Fatalf("builder did not use isolated workspace: %+v", builder.invocation)
	}
	if err := VerifyReleaseCurrent(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo, "cmd", "nimbusd", "main.go"), []byte("changed"), 0o600)
	if err := VerifyReleaseCurrent(context.Background(), manifest); err == nil {
		t.Fatal("changed source accepted")
	}
}

func TestReleaseUsesImmutableMultiRepoSnapshotAndRejectsForgedPolicy(t *testing.T) {
	newsRepo := cleanReleaseRepo(t)
	dependencyRepo := cleanReleaseRepo(t)
	dependencyPath := filepath.Join(dependencyRepo, "cmd", "nimbusd", "main.go")
	original, err := os.ReadFile(dependencyPath)
	if err != nil {
		t.Fatal(err)
	}
	root := candidateRoot(t)
	builder := &fakeBuilder{data: []byte("snapshot-binary")}
	builder.hook = func(invocation BuildInvocation) error {
		snapshotDependency := filepath.Join(filepath.Dir(invocation.RepositoryRoot), "datasources", "cmd", "nimbusd", "main.go")
		snapshot, err := os.ReadFile(snapshotDependency)
		if err != nil {
			return err
		}
		if !bytes.Equal(snapshot, original) {
			return fmt.Errorf("snapshot dependency changed")
		}
		if err := os.WriteFile(dependencyPath, []byte("temporary malicious source"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(dependencyPath, original, 0o600)
	}
	builds := []ArtifactBuildSpec{{Name: "nimbusd", Repository: "news", Argv: approvedBuildArgv("./cmd/nimbusd")}}
	manifest, _, err := BuildReleaseFromSource(context.Background(), root, builder, BuildSpec{
		Repositories: map[string]string{"news": newsRepo, "datasources": dependencyRepo},
		Builds:       builds, Include: []string{"cmd"}, MaxFileBytes: 1 << 20,
		ConfigSHA256: strings.Repeat("c", 64), RegistrySHA256: strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := ReleasePolicy{Repositories: map[string]string{"news": newsRepo, "datasources": dependencyRepo}, Builds: builds}
	if err := VerifyReleasePolicy(manifest, policy); err != nil {
		t.Fatal(err)
	}
	forged := policy
	forged.Builds = []ArtifactBuildSpec{{Name: "nimbusd", Repository: "news", Argv: []string{"cp", "/tmp/arbitrary", BuildOutputPlaceholder}}}
	if err := VerifyReleasePolicy(manifest, forged); err == nil {
		t.Fatal("forged build policy accepted")
	}
	forged = policy
	forged.Repositories = map[string]string{"news": newsRepo, "datasources": t.TempDir()}
	if err := VerifyReleasePolicy(manifest, forged); err == nil {
		t.Fatal("forged repository policy accepted")
	}
	forgedManifest := manifest
	forgedManifest.Builds = append([]BuildRecord(nil), manifest.Builds...)
	forgedManifest.Artifacts = append([]ArtifactRecord(nil), manifest.Artifacts...)
	forgedManifest.Builds[0].Argv = []string{"cp", "/tmp/arbitrary", BuildOutputPlaceholder}
	forgedManifest.Builds[0].BuildID = ""
	buildPayload, _ := canonicalJSON(forgedManifest.Builds[0])
	forgedManifest.Builds[0].BuildID = sha256Hex(buildPayload)
	forgedManifest.Artifacts[0].BuildID = forgedManifest.Builds[0].BuildID
	forgedManifest.ReleaseID = ""
	manifestPayload, _ := canonicalJSON(forgedManifest)
	forgedManifest.ReleaseID = sha256Hex(manifestPayload)
	encoded, _ := canonicalJSON(forgedManifest)
	if _, err := VerifyRelease(encoded); err == nil {
		t.Fatal("rehashed arbitrary build manifest accepted")
	}
}

func TestReleaseRejectsDirtyBeforeRunnerAndPrebuiltArtifacts(t *testing.T) {
	repo := cleanReleaseRepo(t)
	writeTestFile(t, filepath.Join(repo, "dirty.txt"), []byte("dirty"), 0o600)
	root := candidateRoot(t)
	builder := &fakeBuilder{data: []byte("binary")}
	_, _, err := BuildReleaseFromSource(context.Background(), root, builder, BuildSpec{
		Repositories:   map[string]string{"news": repo},
		Builds:         []ArtifactBuildSpec{{Name: "nimbusd", Repository: "news", Argv: approvedBuildArgv("./cmd/nimbusd")}},
		Include:        []string{"cmd"},
		MaxFileBytes:   128,
		ConfigSHA256:   strings.Repeat("c", 64),
		RegistrySHA256: strings.Repeat("d", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "dirty worktree") || builder.calls != 0 {
		t.Fatalf("dirty source reached builder: calls=%d err=%v", builder.calls, err)
	}
	if _, _, err := BuildRelease(context.Background(), root, ReleaseOptions{Artifacts: map[string]string{"nimbusd": "/tmp/arbitrary"}}); err == nil {
		t.Fatal("prebuilt artifact accepted")
	}
}

func TestReleaseRejectsArbitraryBuilderAndSourceTOCTOU(t *testing.T) {
	repo := cleanReleaseRepo(t)
	root := candidateRoot(t)
	builder := &fakeBuilder{data: []byte("binary")}
	_, _, err := BuildReleaseFromSource(context.Background(), root, builder, BuildSpec{
		Repositories:   map[string]string{"news": repo},
		Builds:         []ArtifactBuildSpec{{Name: "nimbusd", Repository: "news", Argv: []string{"cp", "/tmp/arbitrary", BuildOutputPlaceholder}}},
		MaxFileBytes:   128,
		ConfigSHA256:   strings.Repeat("c", 64),
		RegistrySHA256: strings.Repeat("d", 64),
	})
	if err == nil || builder.calls != 0 {
		t.Fatalf("arbitrary builder accepted: calls=%d err=%v", builder.calls, err)
	}

	path := filepath.Join(repo, "cmd", "nimbusd", "main.go")
	_, _, err = BuildReleaseFromSource(context.Background(), root, builder, BuildSpec{
		Repositories:   map[string]string{"news": repo},
		Builds:         []ArtifactBuildSpec{{Name: "nimbusd", Repository: "news", Argv: approvedBuildArgv("./cmd/nimbusd")}},
		Include:        []string{"cmd"},
		MaxFileBytes:   128,
		ConfigSHA256:   strings.Repeat("c", 64),
		RegistrySHA256: strings.Repeat("d", 64),
		BeforeFileRestat: func(current string) {
			if current == path {
				_ = os.WriteFile(current, []byte("changed-size"), 0o600)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed while hashing") || builder.calls != 0 {
		t.Fatalf("source TOCTOU accepted: calls=%d err=%v", builder.calls, err)
	}
}

func TestExecBuilderUsesControlledSnapshotGoWork(t *testing.T) {
	t.Setenv("GOFLAGS", "-overlay=/definitely/outside/overlay.json")
	t.Setenv("GOENV", filepath.Join(t.TempDir(), "hostile-go-env"))
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("CGO_ENABLED", "1")
	t.Setenv("CC", "/definitely/outside/compiler")
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "test@example.invalid")
	runGit(t, repo, "config", "user.name", "test")
	writeTestFile(t, filepath.Join(repo, "go.mod"), []byte("module example.invalid/snapshot\n\ngo 1.26.3\n"), 0o600)
	writeTestFile(t, filepath.Join(repo, "cmd", "nimbusd", "main.go"), []byte("package main\nfunc main() {}\n"), 0o600)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-qm", "initial")
	canonical, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	root := candidateRoot(t)
	manifest, _, err := BuildReleaseFromSource(context.Background(), root, ExecBuilder{}, BuildSpec{
		Repositories: map[string]string{"news": canonical},
		Builds:       []ArtifactBuildSpec{{Name: "nimbusd", Repository: "news", Argv: approvedBuildArgv("./cmd/nimbusd")}},
		Include:      []string{"."}, MaxFileBytes: 64 << 20,
		ConfigSHA256: strings.Repeat("c", 64), RegistrySHA256: strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Artifacts) != 1 || manifest.WorkspaceSHA256 != sha256Hex(controlledGoWork(map[string]string{"news": canonical})) {
		t.Fatalf("controlled workspace not bound: %+v", manifest)
	}
}

func TestControlledGoEnvironmentIsExact(t *testing.T) {
	whitelist := []string{"HOME", "TMPDIR", "GOCACHE", "GOMODCACHE", "GOPATH", "GOPROXY", "GOSUMDB", "GONOSUMDB", "GOPRIVATE", "SSL_CERT_FILE", "SSL_CERT_DIR"}
	for _, name := range whitelist {
		t.Setenv(name, "/controlled/"+strings.ToLower(name))
	}
	for _, name := range []string{"GOFLAGS", "GOENV", "GOTOOLCHAIN", "CGO_ENABLED", "CC", "CXX", "GOROOT"} {
		t.Setenv(name, "hostile")
	}
	want := map[string]string{
		"PATH":        "/usr/local/bin:/usr/bin:/bin",
		"GOWORK":      "/candidate/go.work",
		"GOENV":       "off",
		"GOFLAGS":     "",
		"GOTOOLCHAIN": "go1.26.3",
		"CGO_ENABLED": "0",
	}
	for _, name := range whitelist {
		want[name] = "/controlled/" + strings.ToLower(name)
	}
	got := make(map[string]string)
	for _, assignment := range controlledGoEnvironment("/candidate/go.work") {
		name, value, found := strings.Cut(assignment, "=")
		if !found || name == "" {
			t.Fatalf("invalid environment assignment %q", assignment)
		}
		if _, exists := got[name]; exists {
			t.Fatalf("duplicate environment assignment %q", name)
		}
		got[name] = value
	}
	if len(got) != len(want) {
		t.Fatalf("environment keys=%v want=%v", got, want)
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("%s=%q want %q", name, got[name], value)
		}
	}
}

func cleanReleaseRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "test@example.invalid")
	runGit(t, repo, "config", "user.name", "test")
	writeTestFile(t, filepath.Join(repo, "cmd", "nimbusd", "main.go"), []byte("package main\n"), 0o600)
	writeTestFile(t, filepath.Join(repo, "excluded", "large"), []byte(strings.Repeat("x", 256)), 0o600)
	if err := os.Symlink("cmd/nimbusd/main.go", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-qm", "initial")
	canonical, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func approvedBuildArgv(target string) []string {
	return []string{"go", "build", "-trimpath", "-o", BuildOutputPlaceholder, target}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, data)
	}
}
