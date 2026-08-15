package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
)

func TestRunDryRunAndFencedApply(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "copy.db")
	owner, err := store.NewBoltStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	digests, err := store.NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := digests.Enqueue(context.Background(), "feed", digest.USPreview, &model.Message{ID: "one", Title: "Title"}); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	configPath := writeConfig(t)

	var previewOut bytes.Buffer
	if err := run(context.Background(), []string{"--db", dbPath, "--config", configPath}, &previewOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var preview store.DigestTopicMigrationReport
	if err := json.Unmarshal(previewOut.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Mode != "dry-run" || preview.Applied || preview.BeforeHash == "" {
		t.Fatalf("preview=%+v", preview)
	}

	var applyOut bytes.Buffer
	if err := run(context.Background(), []string{"--db", dbPath, "--config", configPath,
		"--mode", "apply", "--expected-before-hash", preview.BeforeHash}, &applyOut, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var applied store.DigestTopicMigrationReport
	if err := json.Unmarshal(applyOut.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || !applied.MarkerPresent || applied.BeforeHash != preview.BeforeHash {
		t.Fatalf("applied=%+v", applied)
	}
}

func TestRunRejectsUnsafeOrUnfencedTargets(t *testing.T) {
	configPath := writeConfig(t)
	missing := filepath.Join(t.TempDir(), "missing.db")
	if err := run(context.Background(), []string{"--db", missing, "--config", configPath}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected missing database rejection")
	}
	dbPath := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"--db", dbPath, "--config", configPath, "--mode", "apply"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected unfenced apply rejection")
	}
	link := filepath.Join(t.TempDir(), "copy-link.db")
	if err := os.Symlink(dbPath, link); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"--db", link, "--config", configPath}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func writeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "platform.yaml")
	data := []byte(`server: {name: test, port: 8081}
store:
  type: bolt
  path: unused.db
  news: {max_items: 100, ttl: 3600}
sources:
  - name: feed
    type: rss
    url: https://example.com/feed
    routing:
      ai_policy: required
      on_ai_failure: silent
      digest: {min_score: 7, max_per_day: 3, priority: 40, briefing_target: us_preview}
      default: silent
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
