package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceMutationSaveFailureLeavesConfigUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	if err := os.WriteFile(path, []byte("sources:\n  - name: existing\n    type: rss\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	manager.configPath = filepath.Join(t.TempDir(), "missing", "platform.yaml")

	if err := manager.AddSource(SourceConfig{Name: "new", Type: "rss"}); err == nil {
		t.Fatal("expected persistence failure")
	}
	if _, ok := manager.GetSourceConfig("new"); ok {
		t.Fatal("failed persistence mutated in-memory config")
	}
	if _, ok := manager.GetSourceConfig("existing"); !ok {
		t.Fatal("existing source was lost")
	}
}

func TestAddSourcePreservesExplicitDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	if err := os.WriteFile(path, []byte("sources: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if err := manager.AddSource(SourceConfig{Name: "disabled", Type: "rss", Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	stored, ok := manager.GetSourceConfig("disabled")
	if !ok || stored.Enabled == nil || *stored.Enabled {
		t.Fatalf("explicit disabled state was not preserved: %+v", stored)
	}
}

func TestNonSourceMutationInvalidatesReloadPreviewRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	if err := os.WriteFile(path, []byte("sources: []\nchannels:\n  - name: main\n    type: file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	preview, revision, err := manager.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func() error{
		func() error { return manager.UpdateChannel("main", ChannelConfig{Name: "main", Type: "file"}) },
		func() error { return manager.ToggleChannel("main", false) },
		func() error { return manager.UpdateKey("llm:api_key", "test") },
		func() error { return manager.UpdateLLM(LLMConfig{Provider: "test"}) },
		func() error { return manager.AddChannel(ChannelConfig{Name: "second", Type: "file"}) },
		func() error { return manager.UpdateFilters(FiltersConfig{}) },
		func() error { return manager.UpdateAlert(AlertConfig{}) },
		func() error { return manager.UpdateSourceHealth(SourceHealthConfig{}) },
	}
	last := revision
	for i, mutate := range mutations {
		if err := mutate(); err != nil {
			t.Fatalf("mutation %d: %v", i, err)
		}
		_, next, err := manager.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if next != last+1 {
			t.Fatalf("mutation %d revision=%d want=%d", i, next, last+1)
		}
		last = next
	}
	if err := manager.CommitPreview(preview, revision); !IsConflictError(err) {
		t.Fatalf("stale preview commit err=%v", err)
	}
}

func TestRemoveSourceWithRuntimeRestoresExactRawConfigAndOrder(t *testing.T) {
	t.Setenv("SOURCE_API_KEY", "expanded-secret")
	path := filepath.Join(t.TempDir(), "platform.yaml")
	original := `sources:
  - name: first
    type: rss
  - name: secret
    type: rss
    options:
      api_key: ${SOURCE_API_KEY}
  - name: last
    type: rss
`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	runtimeFailure := errors.New("generation changed")
	err = manager.RemoveSourceWithRuntime("secret", func(string) error {
		return runtimeFailure
	})
	var mutationErr *SourceRuntimeMutationError
	if !errors.As(err, &mutationErr) || !errors.Is(err, runtimeFailure) || mutationErr.RollbackErr != nil {
		t.Fatalf("err=%v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != original {
		t.Fatalf("rollback did not restore exact raw file:\n%s", data)
	}
	if strings.Contains(string(data), "expanded-secret") || !strings.Contains(string(data), "${SOURCE_API_KEY}") {
		t.Fatalf("rollback exposed environment value: %s", data)
	}
	cfg := manager.Get()
	if len(cfg.Sources) != 3 ||
		cfg.Sources[0].Name != "first" ||
		cfg.Sources[1].Name != "secret" ||
		cfg.Sources[2].Name != "last" {
		t.Fatalf("rollback changed live source order: %+v", cfg.Sources)
	}
	if got := cfg.Sources[1].Options["api_key"]; got != "expanded-secret" {
		t.Fatalf("live config was not restored with environment expansion: %v", got)
	}
}

func TestRemoveSourceWithRuntimeFencesRollbackAgainstExternalEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	if err := os.WriteFile(path, []byte("sources:\n  - name: existing\n    type: rss\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	external := []byte("sources:\n  - name: external\n    type: rss\n")
	err = manager.RemoveSourceWithRuntime("existing", func(string) error {
		if writeErr := os.WriteFile(path, external, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
		return errors.New("runtime failed")
	})
	var mutationErr *SourceRuntimeMutationError
	if !errors.As(err, &mutationErr) || !IsConflictError(mutationErr.RollbackErr) {
		t.Fatalf("err=%v want rollback conflict", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != string(external) {
		t.Fatalf("rollback overwrote external edit: %s", data)
	}
}

func TestRemoveSourceWithRuntimeCallbackCanReadManager(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	if err := os.WriteFile(path, []byte("sources:\n  - name: existing\n    type: rss\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- manager.RemoveSourceWithRuntime("existing", func(string) error {
			if cfg := manager.Get(); len(cfg.Sources) != 0 {
				return errors.New("callback observed source before deletion")
			}
			snapshot, _, snapshotErr := manager.Snapshot()
			if snapshotErr != nil {
				return snapshotErr
			}
			if len(snapshot.Sources) != 0 {
				return errors.New("snapshot observed source before deletion")
			}
			return nil
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime callback deadlocked while reading ConfigManager")
	}
}

func TestRemoveSourceWithRuntimeDoesNotOverwriteConcurrentMutationOnRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	original := `sources:
  - name: existing
    type: rss
  - name: other
    type: rss
`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	err = manager.RemoveSourceWithRuntime("existing", func(string) error {
		if mutationErr := manager.ToggleSource("other", false); mutationErr != nil {
			t.Fatal(mutationErr)
		}
		return errors.New("runtime failed")
	})
	var mutationErr *SourceRuntimeMutationError
	if !errors.As(err, &mutationErr) || !IsConflictError(mutationErr.RollbackErr) {
		t.Fatalf("err=%v want rollback conflict", err)
	}
	if _, exists := manager.GetSourceConfig("existing"); exists {
		t.Fatal("conflicting rollback restored deleted source")
	}
	other, exists := manager.GetSourceConfig("other")
	if !exists || other.IsEnabled() {
		t.Fatalf("concurrent mutation was lost: %+v", other)
	}
}

func TestRemoveSourceWithRuntimeSuccessKeepsConcurrentMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	original := `sources:
  - name: existing
    type: rss
  - name: other
    type: rss
`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveSourceWithRuntime("existing", func(string) error {
		return manager.ToggleSource("other", false)
	}); err != nil {
		t.Fatal(err)
	}
	if _, exists := manager.GetSourceConfig("existing"); exists {
		t.Fatal("successful runtime removal restored deleted source")
	}
	other, exists := manager.GetSourceConfig("other")
	if !exists || other.IsEnabled() {
		t.Fatalf("concurrent mutation was lost: %+v", other)
	}
}

func TestUpdateSourceWithRuntimeRestoresExactRawConfigAfterSensitiveUpdate(t *testing.T) {
	t.Setenv("SOURCE_API_KEY", "expanded-secret")
	path := filepath.Join(t.TempDir(), "platform.yaml")
	original := `sources:
  - name: first
    type: rss
  - name: secret
    type: rss
    url: https://old.example/feed
    options:
      api_key: ${SOURCE_API_KEY}
  - name: last
    type: rss
`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	runtimeFailure := errors.New("generation changed")
	err = manager.UpdateSourceWithRuntime("secret", SourceConfig{
		Name: "secret",
		Type: "rss",
		URL:  "https://new.example/feed",
		Options: map[string]interface{}{
			"api_key": "replacement-secret",
		},
	}, func() error {
		updated, ok := manager.GetSourceConfig("secret")
		if !ok || updated.URL != "https://new.example/feed" {
			return errors.New("callback did not observe updated source")
		}
		if _, _, snapshotErr := manager.Snapshot(); snapshotErr != nil {
			return snapshotErr
		}
		return runtimeFailure
	})
	var mutationErr *SourceRuntimeMutationError
	if !errors.As(err, &mutationErr) || !errors.Is(err, runtimeFailure) || mutationErr.RollbackErr != nil {
		t.Fatalf("err=%v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != original {
		t.Fatalf("rollback did not restore exact raw file:\n%s", data)
	}
	if strings.Contains(string(data), "expanded-secret") ||
		strings.Contains(string(data), "replacement-secret") ||
		!strings.Contains(string(data), "${SOURCE_API_KEY}") {
		t.Fatalf("rollback did not preserve environment reference: %s", data)
	}
	cfg := manager.Get()
	if len(cfg.Sources) != 3 ||
		cfg.Sources[0].Name != "first" ||
		cfg.Sources[1].Name != "secret" ||
		cfg.Sources[2].Name != "last" {
		t.Fatalf("rollback changed live source order: %+v", cfg.Sources)
	}
	if got := cfg.Sources[1].Options["api_key"]; got != "expanded-secret" {
		t.Fatalf("live config was not restored with environment expansion: %v", got)
	}
}

func TestUpdateSourceWithRuntimeCallbackCanReadManager(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	if err := os.WriteFile(path, []byte("sources:\n  - name: existing\n    type: rss\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- manager.UpdateSourceWithRuntime("existing", SourceConfig{
			Name: "existing",
			Type: "rss",
			URL:  "https://updated.example/feed",
		}, func() error {
			src, ok := manager.GetSourceConfig("existing")
			if !ok || src.URL != "https://updated.example/feed" {
				return errors.New("callback did not observe updated source")
			}
			_, _, snapshotErr := manager.Snapshot()
			return snapshotErr
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime callback deadlocked while reading ConfigManager")
	}
}

func TestUpdateSourceWithRuntimeDoesNotOverwriteConcurrentMutationOnRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	original := `sources:
  - name: existing
    type: rss
  - name: other
    type: rss
`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	err = manager.UpdateSourceWithRuntime("existing", SourceConfig{
		Name: "existing",
		Type: "rss",
		URL:  "https://updated.example/feed",
	}, func() error {
		if mutationErr := manager.ToggleSource("other", false); mutationErr != nil {
			t.Fatal(mutationErr)
		}
		return errors.New("runtime failed")
	})
	var mutationErr *SourceRuntimeMutationError
	if !errors.As(err, &mutationErr) || !IsConflictError(mutationErr.RollbackErr) {
		t.Fatalf("err=%v want rollback conflict", err)
	}
	existing, exists := manager.GetSourceConfig("existing")
	if !exists || existing.URL != "https://updated.example/feed" {
		t.Fatalf("conflicting rollback replaced updated source: %+v", existing)
	}
	other, exists := manager.GetSourceConfig("other")
	if !exists || other.IsEnabled() {
		t.Fatalf("concurrent mutation was lost: %+v", other)
	}
}

func TestUpdateSourceWithRuntimeFencesRollbackAgainstExternalEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	if err := os.WriteFile(path, []byte("sources:\n  - name: existing\n    type: rss\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManager(path)
	if err != nil {
		t.Fatal(err)
	}
	external := []byte("sources:\n  - name: external\n    type: rss\n")
	err = manager.UpdateSourceWithRuntime("existing", SourceConfig{
		Name: "existing",
		Type: "rss",
		URL:  "https://updated.example/feed",
	}, func() error {
		if writeErr := os.WriteFile(path, external, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
		return errors.New("runtime failed")
	})
	var mutationErr *SourceRuntimeMutationError
	if !errors.As(err, &mutationErr) || !IsConflictError(mutationErr.RollbackErr) {
		t.Fatalf("err=%v want rollback conflict", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != string(external) {
		t.Fatalf("rollback overwrote external edit: %s", data)
	}
}
