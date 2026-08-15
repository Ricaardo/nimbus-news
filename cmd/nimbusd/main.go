// Command nimbusd is the integrated Go shadow candidate. It never starts with
// implicit production addresses or real notification channels.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	ossignal "os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Ricaardo/nimbus-os/datasources/app/nimbuscore"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
	"github.com/Ricaardo/nimbus-os/news/internal/channel/filefeed"
	"github.com/Ricaardo/nimbus-os/news/internal/platformapp"
	childRuntime "github.com/Ricaardo/nimbus-os/news/internal/runtime"
	newsSignal "github.com/Ricaardo/nimbus-os/news/internal/signal"
	"github.com/Ricaardo/nimbus-os/news/internal/snapshotipc"
	"github.com/Ricaardo/nimbus-os/news/internal/store"
	"github.com/Ricaardo/nimbus-os/news/internal/systemconfig"
	"github.com/Ricaardo/nimbus-os/news/internal/warehouse"
)

func main() {
	systemConfigPath := flag.String("system-config", "config/system.yaml", "strict candidate system configuration")
	configPath := flag.String("config", os.Getenv("NIMBUSD_NEWS_CONFIG"), "news config path (required)")
	candidateRoot := flag.String("candidate-root", os.Getenv("NIMBUSD_CANDIDATE_ROOT"), "existing isolated candidate data directory (required)")
	newsAddr := flag.String("news-addr", os.Getenv("NIMBUSD_NEWS_ADDR"), "candidate news listen address (required)")
	newsStore := flag.String("news-store", os.Getenv("NIMBUSD_NEWS_STORE"), "candidate news BoltDB path (required)")
	shadowFeed := flag.String("shadow-feed", os.Getenv("NIMBUSD_SHADOW_FEED"), "candidate filefeed JSONL path (required)")
	signalStore := flag.String("signal-store", os.Getenv("NIMBUSD_SIGNAL_STORE"), "candidate signal JSONL path (required)")
	dataAddr := flag.String("data-addr", os.Getenv("NIMBUSD_DATA_ADDR"), "candidate strict data listen address (required)")
	dataplaneAddr := flag.String("dataplane-addr", os.Getenv("NIMBUSD_DATAPLANE_ADDR"), "candidate facade listen address (required)")
	controlDB := flag.String("control-db", os.Getenv("NIMBUSD_CONTROL_DB"), "candidate control SQLite path (required)")
	warehouseRoot := flag.String("warehouse-root", os.Getenv("NIMBUSD_WAREHOUSE_ROOT"), "candidate warehouse root (optional; never served)")
	warehouseManifest := flag.String("warehouse-manifest", os.Getenv("NIMBUSD_WAREHOUSE_MANIFEST"), "explicit warehouse staging manifest to validate (optional)")
	agentRuntimeCommand := flag.String("agent-runtime-command", os.Getenv("NIMBUSD_AGENT_RUNTIME_COMMAND"), "opt-in Bun runtime command as a JSON string array (optional, candidate only)")
	embeddingRuntimeCommand := flag.String("embedding-runtime-command", os.Getenv("NIMBUSD_EMBEDDING_RUNTIME_COMMAND"), "opt-in Python runtime command as a JSON string array (optional, candidate only)")
	flag.Parse()
	systemConfig, err := systemconfig.Load(*systemConfigPath)
	if err != nil {
		exitf("nimbusd: system config: %v", err)
	}
	registry, err := systemConfig.LoadPortsSnapshot()
	if err != nil {
		exitf("nimbusd: ports registry: %v", err)
	}
	configFingerprint, _, _, err := systemConfig.Fingerprint()
	if err != nil {
		exitf("nimbusd: system config fingerprint: %v", err)
	}

	validated, err := validateCandidateConfig(candidateConfig{
		ConfigPath: *configPath, CandidateRoot: *candidateRoot, NewsAddr: *newsAddr,
		NewsStore: *newsStore, ShadowFeed: *shadowFeed, SignalStore: *signalStore,
		DataAddr: *dataAddr, DataplaneAddr: *dataplaneAddr, ControlDB: *controlDB,
		WarehouseRoot: *warehouseRoot, WarehouseManifest: *warehouseManifest,
		AgentRuntimeCommand: *agentRuntimeCommand, EmbeddingRuntimeCommand: *embeddingRuntimeCommand,
		ConfiguredRoot: systemConfig.CandidateRootPath(), Registry: registry.Registry,
		ProtectedPaths: systemConfig.ProtectedProductionPaths(),
	})
	if err != nil {
		exitf("nimbusd: %v", err)
	}

	signalJSONL, err := newsSignal.NewJSONLStore(validated.SignalStore)
	if err != nil {
		exitf("nimbusd: %v", err)
	}
	warehouseStatus := warehouse.InspectCandidate(validated.WarehouseRoot, validated.WarehouseManifest)
	slog.Info("candidate warehouse status",
		"enabled", warehouseStatus.Enabled, "prepared", warehouseStatus.Prepared,
		"serving", warehouseStatus.Serving, "production_owner", warehouseStatus.ProductionOwner,
		"manifest_sha256", warehouseStatus.ManifestSHA256, "error", warehouseStatus.Error,
	)
	coreApp, err := nimbuscore.New(nimbuscore.Config{
		DataListenAddr: validated.DataAddr, DataplaneListenAddr: validated.DataplaneAddr, ControlDBPath: validated.ControlDB,
	})
	if err != nil {
		exitf("nimbusd: %v", err)
	}
	platform, err := platformapp.New(platformapp.Options{
		ConfigPath: validated.ConfigPath, ListenAddr: validated.NewsAddr, StorePath: validated.NewsStore,
		CandidateRoot: validated.CandidateRoot, Shadow: true, ShadowFeedPath: validated.ShadowFeed,
		SignalHandler:   newsSignal.NewHandler(signalJSONL),
		WarehouseStatus: warehouse.StatusHandler(warehouseStatus),
		Trading:         platformapp.TraderOptions{Mode: "off"},
	})
	if err != nil {
		_ = coreApp.Close()
		exitf("nimbusd: %v", err)
	}
	root, err := candidate.NewRoot(validated.CandidateRoot, systemConfig.ProtectedProductionPaths()...)
	if err != nil {
		_ = platform.Close()
		_ = coreApp.Close()
		exitf("nimbusd: snapshot root: %v", err)
	}
	snapshotServer, err := snapshotipc.NewServer(snapshotipc.ServerConfig{
		Root: root, ConfigSHA256: configFingerprint, Expected: expectedSnapshotObjects(systemConfig),
		Owners: []snapshotipc.Owner{
			{
				Name: "news", Kind: "bolt", ResultKind: store.BoltSnapshotKind, SourcePath: validated.NewsStore,
				Snapshot: func(ctx context.Context, destination io.Writer) (snapshotipc.OwnerResult, error) {
					result, err := platform.SnapshotBolt(ctx, destination)
					return snapshotipc.OwnerResult{Kind: result.Kind, Size: result.Size, SHA256: result.SHA256}, err
				},
			},
			{
				Name: "control", Kind: "sqlite", ResultKind: "sqlite", SourcePath: validated.ControlDB,
				Snapshot: func(ctx context.Context, destination io.Writer) (snapshotipc.OwnerResult, error) {
					result, err := coreApp.SnapshotControl(ctx, destination)
					return snapshotipc.OwnerResult{Kind: "sqlite", Size: result.Size, SHA256: result.SHA256, SchemaVersion: result.SchemaVersion}, err
				},
			},
			{
				Name: "signals", Kind: "jsonl", ResultKind: newsSignal.SnapshotKind, SourcePath: validated.SignalStore,
				Snapshot: func(ctx context.Context, destination io.Writer) (snapshotipc.OwnerResult, error) {
					result, err := signalJSONL.Snapshot(ctx, destination)
					return snapshotipc.OwnerResult{Kind: result.Kind, Size: result.Size, SHA256: result.SHA256}, err
				},
			},
			{
				Name: "shadow_feed", Kind: "rolling_jsonl", ResultKind: filefeed.SnapshotKindV1, SourcePath: validated.ShadowFeed,
				Snapshot: func(ctx context.Context, destination io.Writer) (snapshotipc.OwnerResult, error) {
					result, err := platform.SnapshotFeed(ctx, destination)
					return snapshotipc.OwnerResult{Kind: result.Kind, Size: result.Size, SHA256: result.SHA256}, err
				},
			},
		},
	})
	if err != nil {
		_ = platform.Close()
		_ = coreApp.Close()
		exitf("nimbusd: snapshot server: %v", err)
	}
	defer snapshotServer.Close()

	ctx, cancel := ossignal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	runtimes := startRuntimeCandidates(ctx, validated)
	defer func() {
		for _, supervisor := range runtimes {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			_ = supervisor.Shutdown(shutdownCtx)
			shutdownCancel()
			_ = supervisor.Close()
		}
	}()
	errs := make(chan error, 3)
	go func() { errs <- coreApp.Run(ctx) }()
	go func() { errs <- platform.Run(ctx) }()
	go func() { errs <- snapshotServer.Serve(ctx) }()
	first := <-errs
	cancel()
	second := <-errs
	third := <-errs
	if err := errors.Join(first, second, third); err != nil {
		exitf("nimbusd: %v", err)
	}
}

func expectedSnapshotObjects(config systemconfig.Config) []snapshotipc.ExpectedObject {
	objects := config.BackupObjects()
	out := make([]snapshotipc.ExpectedObject, len(objects))
	for index, object := range objects {
		out[index] = snapshotipc.ExpectedObject{Name: object.Name, Kind: object.Kind, SourcePath: object.Path}
	}
	return out
}

type candidateConfig struct {
	ConfigPath, CandidateRoot, NewsAddr, NewsStore, ShadowFeed, SignalStore string
	DataAddr, DataplaneAddr, ControlDB                                      string
	WarehouseRoot, WarehouseManifest                                        string
	AgentRuntimeCommand, EmbeddingRuntimeCommand                            string
	ConfiguredRoot                                                          string
	Registry                                                                systemconfig.PortRegistry
	ProtectedPaths                                                          []string
}

func validateCandidateConfig(config candidateConfig) (candidateConfig, error) {
	required := []struct{ name, value string }{
		{"config", config.ConfigPath}, {"candidate-root", config.CandidateRoot}, {"news-addr", config.NewsAddr},
		{"news-store", config.NewsStore}, {"shadow-feed", config.ShadowFeed}, {"signal-store", config.SignalStore},
		{"data-addr", config.DataAddr}, {"dataplane-addr", config.DataplaneAddr}, {"control-db", config.ControlDB},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return candidateConfig{}, fmt.Errorf("--%s (or matching NIMBUSD_* env) is required", field.name)
		}
	}
	for _, address := range []string{config.NewsAddr, config.DataAddr, config.DataplaneAddr} {
		port, err := candidatePort(address)
		if err != nil {
			return candidateConfig{}, fmt.Errorf("invalid candidate address %q: %w", address, err)
		}
		if config.Registry.Contains(port) {
			return candidateConfig{}, fmt.Errorf("active or retired Nimbus port %d is forbidden for candidate address %q", port, address)
		}
	}
	configuredRoot, err := filepath.Abs(config.ConfiguredRoot)
	if err != nil {
		return candidateConfig{}, err
	}
	suppliedRoot, err := filepath.Abs(config.CandidateRoot)
	if err != nil || filepath.Clean(suppliedRoot) != filepath.Clean(configuredRoot) {
		return candidateConfig{}, fmt.Errorf("candidate root must exactly match validated system config")
	}
	root, err := candidate.NewRoot(config.CandidateRoot, config.ProtectedPaths...)
	if err != nil {
		return candidateConfig{}, err
	}
	config.CandidateRoot = root.Path()
	if strings.TrimSpace(config.WarehouseRoot) != "" || strings.TrimSpace(config.WarehouseManifest) != "" {
		if strings.TrimSpace(config.WarehouseRoot) == "" || strings.TrimSpace(config.WarehouseManifest) == "" {
			return candidateConfig{}, fmt.Errorf("--warehouse-root and --warehouse-manifest must be configured together")
		}
		config.WarehouseRoot, err = root.RequireFile("warehouse root", config.WarehouseRoot)
		if err != nil {
			return candidateConfig{}, err
		}
		config.WarehouseManifest, err = root.RequireFile("warehouse manifest", config.WarehouseManifest)
		if err != nil {
			return candidateConfig{}, err
		}
	}
	paths := []struct {
		label string
		value *string
	}{
		{"news store", &config.NewsStore}, {"shadow feed", &config.ShadowFeed},
		{"signal store", &config.SignalStore}, {"control database", &config.ControlDB},
	}
	seen := make(map[string]string)
	knownProduction := make(map[string]struct{}, len(config.ProtectedPaths))
	for _, path := range config.ProtectedPaths {
		absolute, err := filepath.Abs(path)
		if err == nil {
			knownProduction[filepath.Clean(absolute)] = struct{}{}
		}
	}
	for _, item := range paths {
		canonical, err := root.RequireFile(item.label, *item.value)
		if err != nil {
			return candidateConfig{}, err
		}
		if prior, exists := seen[canonical]; exists {
			return candidateConfig{}, fmt.Errorf("%s and %s must use distinct files", prior, item.label)
		}
		if _, exists := knownProduction[canonical]; exists {
			return candidateConfig{}, fmt.Errorf("%s %q is a known production path", item.label, canonical)
		}
		seen[canonical] = item.label
		*item.value = canonical
	}
	return config, nil
}

func startRuntimeCandidates(parent context.Context, config candidateConfig) []*childRuntime.Supervisor {
	type runtimeConfig struct{ name, command string }
	configured := []runtimeConfig{
		{name: "agent", command: config.AgentRuntimeCommand},
		{name: "embedding", command: config.EmbeddingRuntimeCommand},
	}
	started := make([]*childRuntime.Supervisor, 0, len(configured))
	for _, item := range configured {
		if strings.TrimSpace(item.command) == "" {
			continue
		}
		command, err := childRuntime.ParseCommand(item.command)
		if err != nil {
			slog.Warn("candidate runtime degraded", "runtime", item.name, "error", err)
			continue
		}
		supervisor, err := childRuntime.New(childRuntime.Config{
			Name: item.name, Command: command, QueueSize: 4,
			RequestHandler: func(_ context.Context, method string, _ json.RawMessage) (any, *childRuntime.RPCError) {
				return nil, childRuntime.NewError(childRuntime.DomainPermissionDenied, false, "candidate has no approval channel for "+method)
			},
		})
		if err != nil {
			slog.Warn("candidate runtime degraded", "runtime", item.name, "error", err)
			continue
		}
		started = append(started, supervisor)
		go probeRuntime(parent, item.name, supervisor)
	}
	return started
}

func probeRuntime(parent context.Context, name string, supervisor *childRuntime.Supervisor) {
	healthCtx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	err := supervisor.Initialize(healthCtx)
	if err == nil {
		err = supervisor.Health(healthCtx)
	}
	if err != nil {
		slog.Warn("candidate runtime degraded", "runtime", name, "error", err)
	} else {
		slog.Info("candidate runtime healthy", "runtime", name)
	}
}

func candidatePort(address string) (int, error) {
	host, service, err := net.SplitHostPort(address)
	if err != nil {
		return 0, err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return 0, fmt.Errorf("host must be a numeric loopback IP")
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		if strings.Contains(host, ":") || ipv4[0] != 127 {
			return 0, fmt.Errorf("host must be in 127.0.0.0/8 or ::1")
		}
	} else if !ip.Equal(net.IPv6loopback) {
		return 0, fmt.Errorf("host must be in 127.0.0.0/8 or ::1")
	}
	if service == "" {
		return 0, fmt.Errorf("port is required")
	}
	port, err := strconv.Atoi(service)
	if err != nil || strconv.Itoa(port) != service {
		return 0, fmt.Errorf("port must be canonical decimal")
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("port %d is out of range", port)
	}
	return port, nil
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
