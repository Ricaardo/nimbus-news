package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/config"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
	bolt "go.etcd.io/bbolt"
)

const migrationName = "digest-topics-v4"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", migrationName, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet(migrationName, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "existing BoltDB path (required; use a verified copy for rehearsal)")
	configPath := flags.String("config", "", "platform config used to map source priorities (required)")
	mode := flags.String("mode", "dry-run", "dry-run or apply")
	expectedHash := flags.String("expected-before-hash", "", "dry-run before_hash required by apply")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *dbPath == "" || *configPath == "" {
		return errors.New("--db and --config are required and positional arguments are not accepted")
	}
	if *mode != "dry-run" && *mode != "apply" {
		return fmt.Errorf("invalid --mode %q", *mode)
	}
	if *mode == "apply" && *expectedHash == "" {
		return errors.New("--expected-before-hash is required in apply mode")
	}
	if *mode == "dry-run" && *expectedHash != "" {
		return errors.New("--expected-before-hash is only valid in apply mode")
	}

	resolvedDB, err := existingRegularFile(*dbPath)
	if err != nil {
		return err
	}
	cfg, err := config.LoadPlatform(*configPath)
	if err != nil {
		return fmt.Errorf("load platform config: %w", err)
	}
	priorities := digestPriorities(cfg)
	options := &bolt.Options{Timeout: 2 * time.Second, ReadOnly: *mode == "dry-run"}
	db, err := bolt.Open(resolvedDB, 0o600, options)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	var report store.DigestTopicMigrationReport
	if *mode == "dry-run" {
		report, err = store.PreviewDigestTopics(ctx, db, priorities)
	} else {
		report, err = store.ApplyDigestTopics(ctx, db, priorities, *expectedHash)
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func existingRegularFile(path string) (string, error) {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve database path: %w", err)
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect database path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("database path must be an existing regular file, not a symlink: %s", resolved)
	}
	return resolved, nil
}

func digestPriorities(cfg *config.PlatformConfig) map[string]int {
	priorities := make(map[string]int)
	if cfg == nil {
		return priorities
	}
	for _, source := range cfg.Sources {
		if source.Routing != nil && source.Routing.Digest != nil {
			priorities[source.Name] = source.Routing.Digest.Priority
		}
	}
	return priorities
}
