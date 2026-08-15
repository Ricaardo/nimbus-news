package candidate

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRootRejectsEscapeAndSymlinkParent(t *testing.T) {
	base := canonicalTempDir(t)
	rootDir := filepath.Join(base, "candidate")
	outside := filepath.Join(base, "production")
	if err := os.MkdirAll(rootDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InitRoot(rootDir); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.RequireFile("store", filepath.Join(rootDir, "news.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := root.RequireFile("store", filepath.Join(outside, "news.db")); err == nil {
		t.Fatal("outside path accepted")
	}
	if err := os.Symlink(outside, filepath.Join(rootDir, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := root.RequireFile("store", filepath.Join(rootDir, "escape", "news.db")); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestRootRequiresStrictExplicitMarker(t *testing.T) {
	root := canonicalTempDir(t)
	if _, err := NewRoot(root); err == nil {
		t.Fatal("root without marker accepted")
	}
	if err := InitRoot(root); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRoot(root); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, MarkerName)
	if err := os.Chmod(marker, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRoot(root); err == nil {
		t.Fatal("insecure marker permissions accepted")
	}
	if err := os.Chmod(marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRoot(root); err == nil {
		t.Fatal("invalid marker content accepted")
	}
}

func TestRootRejectsBroadOrProtectedScopeBeforeMarker(t *testing.T) {
	base := canonicalTempDir(t)
	production := filepath.Join(base, "production", "warehouse.db")
	if err := os.MkdirAll(filepath.Dir(production), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(production, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InitRoot(base, production); err == nil {
		t.Fatal("ancestor of protected production path accepted")
	}
	if _, err := NewRoot(string(filepath.Separator)); err == nil {
		t.Fatal("filesystem root accepted")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := NewRoot(home); err == nil {
			t.Fatal("home directory accepted")
		}
	}
}

func TestRootRejectsSymlinkAndReplacement(t *testing.T) {
	t.Run("root symlink", func(t *testing.T) {
		base := canonicalTempDir(t)
		realRoot := filepath.Join(base, "candidate")
		if err := os.Mkdir(realRoot, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := InitRoot(realRoot); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(base, "candidate-link")
		if err := os.Symlink(realRoot, link); err != nil {
			t.Fatal(err)
		}
		if _, err := NewRoot(link); err == nil {
			t.Fatal("symlink candidate root accepted")
		}
	})

	t.Run("parent symlink", func(t *testing.T) {
		base := canonicalTempDir(t)
		realParent := filepath.Join(base, "real-parent")
		realRoot := filepath.Join(realParent, "candidate")
		if err := os.MkdirAll(realRoot, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := InitRoot(realRoot); err != nil {
			t.Fatal(err)
		}
		linkParent := filepath.Join(base, "linked-parent")
		if err := os.Symlink(realParent, linkParent); err != nil {
			t.Fatal(err)
		}
		if _, err := NewRoot(filepath.Join(linkParent, "candidate")); err == nil {
			t.Fatal("symlink parent accepted")
		}
	})

	t.Run("root replacement", func(t *testing.T) {
		base := canonicalTempDir(t)
		rootPath := filepath.Join(base, "candidate")
		if err := os.Mkdir(rootPath, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := InitRoot(rootPath); err != nil {
			t.Fatal(err)
		}
		root, err := NewRoot(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(rootPath, filepath.Join(base, "candidate-original")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(rootPath, 0o750); err != nil {
			t.Fatal(err)
		}
		if _, err := root.RequireFile("store", filepath.Join(rootPath, "news.db")); err == nil {
			t.Fatal("replaced candidate root accepted")
		}
	})
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

func TestCapabilityListAnchoredToDirFd(t *testing.T) {
	base := canonicalTempDir(t)
	rootDir := filepath.Join(base, "candidate")
	if err := os.MkdirAll(rootDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := InitRoot(rootDir); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	cap, err := root.OpenCapability()
	if err != nil {
		t.Fatal(err)
	}
	defer cap.Close()
	if err := cap.MkdirAll("listtest/sub", 0o700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		rel     string
		content []byte
		mode    os.FileMode
	}{
		{"listtest/a.txt", []byte("hello"), 0o600},
		{"listtest/b.dat", []byte("world!!"), 0o600},
		{"listtest/sub/c.txt", []byte("nested"), 0o600},
	} {
		file, err := cap.CreateExclusive(entry.rel, entry.mode)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.content); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// Change CWD to a different directory
	origCwd, _ := os.Getwd()
	if err := os.Chdir(os.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origCwd)
	entries, err := cap.List("listtest")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	for _, entry := range entries {
		switch entry.Name {
		case "a.txt":
			if entry.Size != 5 {
				t.Errorf("a.txt size=%d want 5", entry.Size)
			}
		case "b.dat":
			if entry.Size != 7 {
				t.Errorf("b.dat size=%d want 7", entry.Size)
			}
		case "sub":
			if !entry.Mode.IsDir() {
				t.Error("sub should be a directory")
			}
		default:
			t.Errorf("unexpected entry: %s", entry.Name)
		}
	}
	// Verify subdirectory entries too
	subEntries, err := cap.List("listtest/sub")
	if err != nil {
		t.Fatal(err)
	}
	if len(subEntries) != 1 || subEntries[0].Name != "c.txt" {
		t.Fatalf("sub listing: %+v", subEntries)
	}
}

func TestCapabilityRejectsRootReplacementAndSymlinkedCASParent(t *testing.T) {
	base := canonicalTempDir(t)
	rootPath := filepath.Join(base, "candidate")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := InitRoot(rootPath); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := root.OpenCapability()
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := capability.WriteCAS("escape", ".json", []byte("{}\n")); err == nil {
		t.Fatal("symlinked CAS parent accepted")
	}
	if err := os.Rename(rootPath, filepath.Join(base, "candidate-original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := InitRoot(rootPath); err != nil {
		t.Fatal(err)
	}
	if _, err := capability.WriteCAS("objects", ".json", []byte("{}\n")); err == nil {
		t.Fatal("replaced root accepted by pinned capability")
	}
}

func TestWriteCASStreamPublishesAtomicallyAndCleansFailedStaging(t *testing.T) {
	rootPath := canonicalTempDir(t)
	if err := InitRoot(rootPath); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := root.OpenCapability()
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()

	if err := capability.MkdirAll("objects/staging", 0o700); err != nil {
		t.Fatal(err)
	}
	stale, err := capability.CreateExclusive("objects/staging/crashed.part", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Write([]byte("crashed")); err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	stalePath, err := capability.Absolute("objects/staging/crashed.part")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(stalePath, old, old); err != nil {
		t.Fatal(err)
	}

	content := []byte("owner snapshot")
	result, err := capability.WriteCASStream(context.Background(), "objects", ".db", 1<<20, func(destination io.Writer) error {
		_, err := destination.Write(content)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := capability.ReadFile(result.Relative, int64(len(content)), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(content) || result.Size != int64(len(content)) {
		t.Fatalf("published object mismatch: %+v %q", result, stored)
	}
	duplicate, err := capability.WriteCASStream(context.Background(), "objects", ".db", 1<<20, func(destination io.Writer) error {
		_, err := destination.Write(content)
		return err
	})
	if err != nil || duplicate.Relative != result.Relative {
		t.Fatalf("idempotent CAS failed: %+v %v", duplicate, err)
	}

	injected := errors.New("injected producer failure")
	if _, err := capability.WriteCASStream(context.Background(), "objects", ".db", 1<<20, func(destination io.Writer) error {
		if _, err := destination.Write([]byte("partial")); err != nil {
			return err
		}
		return injected
	}); !errors.Is(err, injected) {
		t.Fatalf("producer failure lost: %v", err)
	}
	if _, err := capability.WriteCASStream(context.Background(), "objects", ".db", 3, func(destination io.Writer) error {
		_, err := destination.Write([]byte("too large"))
		return err
	}); err == nil {
		t.Fatal("stream size limit was not enforced")
	}
	staging, err := capability.List("objects/staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(staging) != 0 {
		t.Fatalf("failed streams left staging files: %+v", staging)
	}
}
