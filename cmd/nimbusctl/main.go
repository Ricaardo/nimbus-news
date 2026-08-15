// Command nimbusctl is a JSON-first, candidate-only operational control plane.
// It never invokes launchctl and has no production mutation implementation.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
	"github.com/Ricaardo/nimbus-os/news/internal/ops"
	"github.com/Ricaardo/nimbus-os/news/internal/snapshotipc"
	"github.com/Ricaardo/nimbus-os/news/internal/systemconfig"
)

type output struct {
	OK      bool   `json:"ok"`
	Command string `json:"command"`
	Result  any    `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	global := flag.NewFlagSet("nimbusctl", flag.ContinueOnError)
	global.SetOutput(os.Stderr)
	configPath := global.String("config", "config/system.yaml", "strict system config")
	candidateOverride := global.String("candidate-root", "", "candidate root (read-only commands only; writes must match config)")
	if err := global.Parse(args); err != nil {
		return emit("parse", nil, err)
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		return emit("help", nil, fmt.Errorf("command required"))
	}
	cfg, err := systemconfig.Load(*configPath)
	if err != nil {
		return emit(remaining[0], nil, err)
	}
	mutating := isMutating(remaining)
	rootPath, err := commandRoot(cfg, *candidateOverride, mutating)
	if err != nil {
		return emit(remaining[0], nil, err)
	}
	ctx := context.Background()
	command := strings.Join(remaining[:min(2, len(remaining))], " ")

	switch remaining[0] {
	case "status":
		return emit(command, ops.Status(cfg), nil)
	case "doctor":
		report := ops.Doctor(ctx, cfg)
		return emit(command, report, reportError(report))
	case "contracts":
		if len(remaining) != 2 || remaining[1] != "check" {
			return emit(command, nil, fmt.Errorf("usage: contracts check"))
		}
		report := ops.ContractsCheck(filepath.Join(cfg.WorkspaceRoot(), "docs", "contracts"))
		return emit(command, report, reportError(report))
	case "candidate":
		return candidateCommand(command, remaining[1:], cfg, rootPath)
	case "release":
		return releaseCommand(ctx, command, remaining[1:], cfg, rootPath)
	case "evidence":
		return evidenceCommand(command, remaining[1:], cfg, rootPath)
	case "backup":
		return backupCommand(ctx, command, remaining[1:], cfg, rootPath)
	case "restore-verify":
		return restoreCommand(ctx, command, remaining[1:], cfg, rootPath)
	case "preflight":
		return preflightCommand(ctx, command, remaining[1:], cfg, rootPath)
	case "cutover":
		return cutoverCommand(ctx, command, remaining[1:], cfg, rootPath)
	default:
		return emit(command, nil, fmt.Errorf("unknown command %q", remaining[0]))
	}
}

func candidateCommand(command string, args []string, cfg systemconfig.Config, rootPath string) int {
	if len(args) != 1 || args[0] != "init" {
		return emit(command, nil, fmt.Errorf("usage: candidate init"))
	}
	markerErr := candidate.InitRoot(rootPath, cfg.ProtectedProductionPaths()...)
	root, err := candidate.NewRoot(rootPath, cfg.ProtectedProductionPaths()...)
	if err != nil {
		if markerErr != nil {
			err = markerErr
		}
		return emit(command, nil, err)
	}
	// If a previous initialization wrote and fsync'd the marker but failed
	// before creating the key, this explicit command can safely finish that
	// fail-closed partial state. An existing key is never overwritten.
	err = ops.InitializeOperatorKey(root, nil)
	return emit(command, map[string]any{"root": rootPath, "operator_key_created": err == nil}, err)
}

func releaseCommand(ctx context.Context, command string, args []string, cfg systemconfig.Config, rootPath string) int {
	if len(args) == 0 {
		return emit(command, nil, fmt.Errorf("release subcommand required"))
	}
	root, err := openCommandRoot(cfg, rootPath, args[0] == "build")
	if err != nil {
		return emit(command, nil, err)
	}
	fingerprint, _, _, err := cfg.Fingerprint()
	if err != nil {
		return emit(command, nil, err)
	}
	registry, err := cfg.LoadPortsSnapshot()
	if err != nil {
		return emit(command, nil, err)
	}
	switch args[0] {
	case "build":
		fs := newFlags(command)
		if err := fs.Parse(args[1:]); err != nil {
			return emit(command, nil, err)
		}
		if fs.NArg() != 0 {
			return emit(command, nil, fmt.Errorf("release build accepts no repository or binary overrides"))
		}
		policy := releasePolicy(cfg)
		manifest, path, err := ops.BuildReleaseFromSource(ctx, root, ops.ExecBuilder{}, ops.BuildSpec{
			Repositories: policy.Repositories, Builds: policy.Builds, Include: cfg.Release.Include,
			Exclude: cfg.Release.Exclude, MaxFileBytes: cfg.Release.MaxFileBytes,
			ConfigSHA256: fingerprint, RegistrySHA256: registry.SHA256,
		})
		return emit(command, map[string]any{"manifest": manifest, "path": path}, err)
	case "verify":
		fs := newFlags(command)
		path := fs.String("manifest", "", "release manifest below candidate root")
		if err := fs.Parse(args[1:]); err != nil {
			return emit(command, nil, err)
		}
		data, err := readCandidateFile(root, *path)
		if err != nil {
			return emit(command, nil, err)
		}
		manifest, err := ops.VerifyRelease(data)
		if err == nil {
			err = ops.VerifyReleaseArtifacts(root, manifest)
		}
		if err == nil {
			err = ops.VerifyReleasePolicy(manifest, releasePolicy(cfg))
		}
		if err == nil {
			err = ops.VerifyReleaseCurrent(ctx, manifest)
		}
		if err == nil && (manifest.ConfigSHA256 != fingerprint || manifest.RegistrySHA256 != registry.SHA256) {
			err = fmt.Errorf("release: current config or registry hash mismatch")
		}
		return emit(command, manifest, err)
	default:
		return emit(command, nil, fmt.Errorf("unknown release subcommand"))
	}
}

func evidenceCommand(command string, args []string, cfg systemconfig.Config, rootPath string) int {
	if len(args) == 0 {
		return emit(command, nil, fmt.Errorf("evidence subcommand required"))
	}
	root, err := openCommandRoot(cfg, rootPath, args[0] == "record")
	if err != nil {
		return emit(command, nil, err)
	}
	switch args[0] {
	case "record":
		fs := newFlags(command)
		release := fs.String("release", "", "release id")
		configHash := fs.String("config-hash", "", "config hash")
		kind := fs.String("kind", "", "evidence kind")
		probe := fs.String("probe", "", "probe id")
		keyID := fs.String("key-id", "", "credential key id only")
		expiry := fs.String("credential-expiry", "", "credential expiry RFC3339")
		started := fs.String("started", "", "start RFC3339")
		ended := fs.String("ended", "", "end RFC3339")
		samples := fs.Int64("samples", 0, "sample count")
		passed := fs.Bool("passed", false, "probe passed")
		restoreReceipt := fs.String("restore-receipt", "", "persisted restore receipt for backup_restore evidence")
		if err := fs.Parse(args[1:]); err != nil {
			return emit(command, nil, err)
		}
		detail := map[string]string{}
		if *restoreReceipt != "" {
			receipt, receiptErr := ops.LoadRestoreReceipt(root, *restoreReceipt)
			if receiptErr != nil {
				return emit(command, nil, receiptErr)
			}
			detail["backup_id"] = receipt.BackupID
			detail["restore_receipt"] = *restoreReceipt
		}
		event, path, err := ops.RecordEvidence(root, cfg.EvidenceDir(*release), ops.Evidence{
			ReleaseID: *release, ConfigSHA256: *configHash, Kind: *kind, ProbeID: *probe,
			KeyID: *keyID, CredentialExpiry: *expiry, StartedAt: *started, EndedAt: *ended,
			Samples: *samples, Passed: *passed, Detail: detail,
		})
		return emit(command, map[string]any{"evidence": event, "path": path}, err)
	case "verify":
		fs := newFlags(command)
		release := fs.String("release", "", "release id")
		configHash := fs.String("config-hash", "", "config hash")
		if err := fs.Parse(args[1:]); err != nil {
			return emit(command, nil, err)
		}
		events, err := ops.LoadEvidenceChain(root, cfg.EvidenceDir(*release), *release, *configHash)
		if err == nil {
			err = ops.VerifyEvidence(events, *release, *configHash, time.Now().UTC())
		}
		return emit(command, map[string]any{"events": len(events)}, err)
	default:
		return emit(command, nil, fmt.Errorf("unknown evidence subcommand"))
	}
}

func backupCommand(ctx context.Context, command string, args []string, cfg systemconfig.Config, rootPath string) int {
	fs := newFlags(command)
	release := fs.String("release", "", "release id")
	configHash := fs.String("config-hash", "", "config hash")
	if err := fs.Parse(args); err != nil {
		return emit(command, nil, err)
	}
	root, err := openCommandRoot(cfg, rootPath, true)
	if err != nil {
		return emit(command, nil, err)
	}
	fingerprint, _, _, err := cfg.Fingerprint()
	if err != nil {
		return emit(command, nil, err)
	}
	if *configHash != "" && *configHash != fingerprint {
		return emit(command, nil, fmt.Errorf("backup: --config-hash must match the validated system config"))
	}
	sources := backupSources(cfg)
	requirements, err := ops.NewBackupRequirements(root, ops.RequiredObjects(sources))
	if err != nil {
		return emit(command, nil, err)
	}
	manifest, path, err := ops.CreateBackup(ctx, root, ops.BackupOptions{
		ReleaseID: *release, ConfigSHA256: fingerprint, Sources: sources,
		Directory: cfg.Candidate.BackupDir, Now: time.Now().UTC(),
		Snapshotter: snapshotipc.Client{
			Root: root, ConfigSHA256: fingerprint, Expected: snapshotExpectedObjects(cfg),
		},
	})
	if err == nil {
		err = ops.VerifyBackupObjects(root, manifest, requirements)
	}
	return emit(command, map[string]any{"backup": manifest, "path": path}, err)
}

func restoreCommand(ctx context.Context, command string, args []string, cfg systemconfig.Config, rootPath string) int {
	fs := newFlags(command)
	manifest := fs.String("manifest", "", "backup manifest below candidate root")
	if err := fs.Parse(args); err != nil {
		return emit(command, nil, err)
	}
	root, err := openCommandRoot(cfg, rootPath, true)
	if err != nil {
		return emit(command, nil, err)
	}
	requirements, err := backupRequirements(root, cfg)
	if err != nil {
		return emit(command, nil, err)
	}
	result, err := ops.RestoreVerify(ctx, root, *manifest, requirements)
	return emit(command, result, err)
}

func preflightCommand(ctx context.Context, command string, args []string, cfg systemconfig.Config, rootPath string) int {
	fs := newFlags(command)
	releasePath := fs.String("release", "", "release manifest below candidate root")
	backupPath := fs.String("backup", "", "backup manifest below candidate root")
	if err := fs.Parse(args); err != nil {
		return emit(command, nil, err)
	}
	if *releasePath == "" || *backupPath == "" {
		report := ops.Report{
			Schema: "nimbus-preflight/v1", OK: false, ProductionMutationAllowed: false,
			Checks:   []ops.Check{{Name: "gate_inputs", OK: false, Code: "CHECK_FAILED", Message: "verified release and required backup manifests are required"}},
			Blockers: []string{"verified release is missing", "required backup is missing", "external/shadow/soak/backup-restore/rollback-retention evidence is not verified", "production mutation is forbidden"},
		}
		return emit(command, report, reportError(report))
	}
	root, err := openCommandRoot(cfg, rootPath, false)
	if err != nil {
		return emit(command, nil, err)
	}
	inputs, err := loadGateInputs(root, cfg, *releasePath, *backupPath)
	if err != nil {
		return emit(command, nil, err)
	}
	report := ops.Preflight(ctx, root, inputs, time.Now().UTC())
	return emit(command, report, reportError(report))
}

func cutoverCommand(ctx context.Context, command string, args []string, cfg systemconfig.Config, rootPath string) int {
	if len(args) == 0 {
		return emit(command, nil, fmt.Errorf("cutover subcommand required"))
	}
	root, err := openCommandRoot(cfg, rootPath, true)
	if err != nil {
		return emit(command, nil, err)
	}
	environment := ops.ProductionCutoverEnvironment()
	switch args[0] {
	case "plan":
		fs := newFlags(command)
		releasePath := fs.String("release", "", "release manifest below candidate root")
		backupPath := fs.String("backup", "", "backup manifest below candidate root")
		label := fs.String("target-label", "candidate", "must be candidate")
		target := fs.String("target-path", "", "candidate target path")
		port := fs.Int("target-port", 0, "unregistered candidate port")
		if err := fs.Parse(args[1:]); err != nil {
			return emit(command, nil, err)
		}
		inputs, err := loadGateInputs(root, cfg, *releasePath, *backupPath)
		if err != nil {
			return emit(command, nil, err)
		}
		plan, path, err := ops.CreateCutoverPlan(ctx, root, inputs, ops.CutoverTarget{Label: *label, Path: *target, Port: *port}, environment)
		return emit(command, map[string]any{"plan": plan, "path": path}, err)
	case "token":
		fs := newFlags(command)
		planPath := fs.String("plan", "", "plan below candidate root")
		action := fs.String("action", "activate-candidate", "candidate action")
		challenge := fs.String("challenge", "", "exact operator challenge")
		ttl := fs.Duration("ttl", 10*time.Minute, "max 15m")
		if err := fs.Parse(args[1:]); err != nil {
			return emit(command, nil, err)
		}
		plan, err := loadPlan(root, *planPath)
		if err != nil {
			return emit(command, nil, err)
		}
		token, path, err := ops.CreateOperatorToken(root, plan, *action, *challenge, *ttl, environment)
		return emit(command, map[string]any{"token": token, "path": path}, err)
	case "apply", "rollback":
		fs := newFlags(command)
		releasePath := fs.String("release", "", "release manifest below candidate root")
		backupPath := fs.String("backup", "", "backup manifest below candidate root")
		planPath := fs.String("plan", "", "plan below candidate root")
		tokenPath := fs.String("token", "", "operator token below candidate root")
		if err := fs.Parse(args[1:]); err != nil {
			return emit(command, nil, err)
		}
		inputs, err := loadGateInputs(root, cfg, *releasePath, *backupPath)
		if err != nil {
			return emit(command, nil, err)
		}
		plan, err := loadPlan(root, *planPath)
		if err != nil {
			return emit(command, nil, err)
		}
		var token ops.OperatorToken
		if err := readCandidateJSON(root, *tokenPath, &token); err != nil {
			return emit(command, nil, err)
		}
		if args[0] == "apply" {
			receipt, path, err := ops.ApplyCutover(ctx, root, inputs, plan, token, environment)
			return emit(command, map[string]any{"receipt": receipt, "path": path}, err)
		}
		receipt, path, err := ops.RollbackCandidate(ctx, root, inputs, plan, token, environment)
		return emit(command, map[string]any{"receipt": receipt, "path": path}, err)
	default:
		return emit(command, nil, fmt.Errorf("unknown cutover subcommand"))
	}
}

func loadGateInputs(root candidate.Root, cfg systemconfig.Config, releasePath, backupPath string) (ops.TrustedInputs, error) {
	var inputs ops.TrustedInputs
	fingerprint, _, _, err := cfg.Fingerprint()
	if err != nil {
		return inputs, err
	}
	inputs.ConfigSHA256 = fingerprint
	inputs.Registry, err = cfg.LoadPortsSnapshot()
	if err != nil {
		return inputs, err
	}
	inputs.ReleasePolicy = releasePolicy(cfg)
	releaseData, err := readCandidateFile(root, releasePath)
	if err != nil {
		return inputs, err
	}
	inputs.Release, err = ops.VerifyRelease(releaseData)
	if err != nil {
		return inputs, err
	}
	inputs.BackupRequirements, err = backupRequirements(root, cfg)
	if err != nil {
		return inputs, err
	}
	inputs.Backup, err = ops.LoadBackupManifest(root, backupPath, inputs.BackupRequirements)
	if err != nil {
		return inputs, err
	}
	inputs.Evidence, err = ops.LoadEvidenceChain(root, cfg.EvidenceDir(inputs.Release.ReleaseID), inputs.Release.ReleaseID, inputs.Release.ConfigSHA256)
	if err != nil {
		return inputs, err
	}
	return inputs, nil
}

func releasePolicy(cfg systemconfig.Config) ops.ReleasePolicy {
	return ops.ReleasePolicy{
		Repositories: map[string]string{
			"news": cfg.RepositoryRoot(), "datasources": filepath.Join(cfg.WorkspaceRoot(), "datasources"),
			"guanfu": filepath.Join(cfg.WorkspaceRoot(), "guanfu"),
		},
		Builds: []ops.ArtifactBuildSpec{
			{Name: "nimbusctl", Repository: "news", Argv: []string{"go", "build", "-trimpath", "-o", ops.BuildOutputPlaceholder, "./cmd/nimbusctl"}},
			{Name: "nimbusd", Repository: "news", Argv: []string{"go", "build", "-trimpath", "-o", ops.BuildOutputPlaceholder, "./cmd/nimbusd"}},
		},
	}
}

func backupSources(cfg systemconfig.Config) []ops.BackupSource {
	objects := cfg.BackupObjects()
	out := make([]ops.BackupSource, len(objects))
	for i, object := range objects {
		out[i] = ops.BackupSource{Name: object.Name, Kind: object.Kind, Path: object.Path}
	}
	return out
}

func backupRequirements(root candidate.Root, cfg systemconfig.Config) (ops.BackupRequirements, error) {
	return ops.NewBackupRequirements(root, ops.RequiredObjects(backupSources(cfg)))
}

func snapshotExpectedObjects(cfg systemconfig.Config) []snapshotipc.ExpectedObject {
	objects := cfg.BackupObjects()
	out := make([]snapshotipc.ExpectedObject, len(objects))
	for index, object := range objects {
		out[index] = snapshotipc.ExpectedObject{Name: object.Name, Kind: object.Kind, SourcePath: object.Path}
	}
	return out
}

func commandRoot(cfg systemconfig.Config, override string, mutating bool) (string, error) {
	configured, err := filepath.Abs(cfg.CandidateRootPath())
	if err != nil {
		return "", err
	}
	configured = filepath.Clean(configured)
	if override == "" {
		return configured, nil
	}
	supplied, err := filepath.Abs(override)
	if err != nil {
		return "", err
	}
	supplied = filepath.Clean(supplied)
	if mutating && supplied != configured {
		return "", fmt.Errorf("%s: candidate-root override must exactly match validated config", ops.ErrProductionMutationForbidden)
	}
	return supplied, nil
}

func openCommandRoot(cfg systemconfig.Config, path string, mutating bool) (candidate.Root, error) {
	if mutating {
		return candidate.NewRoot(path, cfg.ProtectedProductionPaths()...)
	}
	return candidate.NewRoot(path)
}

func isMutating(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "candidate", "backup", "restore-verify", "cutover":
		return true
	case "release", "evidence":
		return len(args) > 1 && (args[1] == "build" || args[1] == "record")
	default:
		return false
	}
}

func loadPlan(root candidate.Root, path string) (ops.CutoverPlan, error) {
	var plan ops.CutoverPlan
	err := readCandidateJSON(root, path, &plan)
	return plan, err
}

func readCandidateJSON(root candidate.Root, path string, destination any) error {
	data, err := readCandidateFile(root, path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("extra JSON value")
		}
		return err
	}
	return nil
}

func readCandidateFile(root candidate.Root, path string) ([]byte, error) {
	capability, err := root.OpenCapability()
	if err != nil {
		return nil, err
	}
	defer capability.Close()
	relative, err := capability.Relative(path)
	if err != nil {
		return nil, err
	}
	return capability.ReadFile(relative, 64<<20, 0o600)
}

func reportError(report ops.Report) error {
	if report.OK {
		return nil
	}
	return fmt.Errorf("one or more checks failed")
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func emit(command string, result any, err error) int {
	value := output{OK: err == nil, Command: command, Result: result}
	code := 0
	if err != nil {
		value.Error = err.Error()
		code = 1
	}
	_ = json.NewEncoder(os.Stdout).Encode(value)
	return code
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
