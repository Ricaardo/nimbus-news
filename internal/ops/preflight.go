package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
	"github.com/Ricaardo/nimbus-os/news/internal/systemconfig"
)

type Check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

type Report struct {
	Schema                    string   `json:"schema"`
	OK                        bool     `json:"ok"`
	ProductionMutationAllowed bool     `json:"production_mutation_allowed"`
	Checks                    []Check  `json:"checks"`
	Blockers                  []string `json:"blockers,omitempty"`
}

type TrustedInputs struct {
	ConfigSHA256       string
	Release            ReleaseManifest
	Evidence           []Evidence
	Backup             BackupManifest
	BackupRequirements BackupRequirements
	Registry           systemconfig.PortRegistrySnapshot
	ReleasePolicy      ReleasePolicy
}

func Status(cfg systemconfig.Config) Report {
	report := Report{Schema: "nimbus-ops-report/v1", ProductionMutationAllowed: false}
	report.Checks = append(report.Checks, Check{Name: "config", OK: true, Message: cfg.Path})
	for name, runtime := range cfg.Runtimes {
		report.Checks = append(report.Checks, Check{Name: "runtime:" + name, OK: !runtime.Enabled, Code: "DISABLED_BY_DEFAULT", Message: fmt.Sprintf("enabled=%t", runtime.Enabled)})
	}
	sort.Slice(report.Checks, func(i, j int) bool { return report.Checks[i].Name < report.Checks[j].Name })
	report.Blockers = []string{"production mutation is structurally forbidden", "external/shadow/soak/backup-restore/rollback-retention evidence must be recorded before candidate activation"}
	report.OK = true
	return report
}

func Doctor(ctx context.Context, cfg systemconfig.Config) Report {
	report := Report{Schema: "nimbus-ops-report/v1", ProductionMutationAllowed: false, OK: true}
	ports, err := cfg.LoadPortsSnapshot()
	appendCheck(&report, "ports_registry", err, fmt.Sprintf("active=%d retired=%d sha256=%s", len(ports.Registry.Services), len(ports.Registry.Retired), ports.SHA256))
	for _, name := range []string{"git", "go"} {
		command, ok := cfg.Commands[name]
		if !ok || len(command.Argv) == 0 {
			appendCheck(&report, "command:"+name, fmt.Errorf("not configured"), "")
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		args := append(append([]string{}, command.Argv[1:]...), "--version")
		output, probeErr := exec.CommandContext(probeCtx, command.Argv[0], args...).CombinedOutput()
		cancel()
		appendCheck(&report, "command:"+name, probeErr, strings.TrimSpace(string(output)))
	}
	rootPath := cfg.Resolve(cfg.Candidate.Root)
	_, err = candidate.NewRoot(rootPath)
	appendCheck(&report, "candidate_marker", err, rootPath)
	return report
}

func ContractsCheck(directory string) Report {
	report := Report{Schema: "nimbus-ops-report/v1", ProductionMutationAllowed: false, OK: true}
	required := []string{"observation-envelope-v1.schema.json", "runtime-rpc-v1.schema.json", "warehouse-staging-v1.schema.json", "signal-event-v1.md", "news-event-v2.md", "symbol.md"}
	for _, name := range required {
		path := filepath.Join(directory, name)
		data, err := os.ReadFile(path)
		if err == nil && strings.HasSuffix(name, ".json") && !json.Valid(data) {
			err = fmt.Errorf("invalid JSON schema")
		}
		appendCheck(&report, "contract:"+name, err, path)
	}
	return report
}

func Preflight(ctx context.Context, root candidate.Root, inputs TrustedInputs, now time.Time) Report {
	report := Report{Schema: "nimbus-preflight/v1", ProductionMutationAllowed: false, OK: true}
	manifest := inputs.Release
	clean := true
	for _, repo := range manifest.Repositories {
		if !repo.Clean {
			clean = false
			report.Blockers = append(report.Blockers, "dirty repository: "+repo.Name)
		}
	}
	appendCheck(&report, "repositories_clean", boolErr(clean, "DIRTY_WORKTREE"), "dirty state is recorded in the release manifest and blocks activation")
	releaseErr := verifyReleaseForGate(ctx, root, inputs)
	appendCheck(&report, "release", releaseErr, manifest.ReleaseID)
	requiredArtifacts := []string{"nimbusd", "nimbusctl"}
	artifacts := map[string]bool{}
	for _, artifact := range manifest.Artifacts {
		artifacts[artifact.Name] = true
	}
	for _, name := range requiredArtifacts {
		if !artifacts[name] {
			report.Blockers = append(report.Blockers, "missing release artifact: "+name)
			appendCheck(&report, "artifact:"+name, fmt.Errorf("missing"), "")
		}
	}
	registryErr := inputs.Registry.Verify()
	if registryErr == nil && manifest.RegistrySHA256 != inputs.Registry.SHA256 {
		registryErr = fmt.Errorf("ports registry: release hash mismatch")
	}
	appendCheck(&report, "ports_registry", registryErr, inputs.Registry.SHA256)
	evidenceErr := verifyEvidenceForGate(root, inputs.Evidence, manifest.ReleaseID, manifest.ConfigSHA256, inputs.Backup, now)
	appendCheck(&report, "evidence", evidenceErr, "requires five bound evidence kinds")
	backupErr := VerifyBackupObjects(root, inputs.Backup, inputs.BackupRequirements)
	if backupErr == nil && (inputs.Backup.ReleaseID != manifest.ReleaseID || inputs.Backup.ConfigSHA256 != manifest.ConfigSHA256) {
		backupErr = fmt.Errorf("backup binding mismatch")
	}
	appendCheck(&report, "backup", backupErr, inputs.Backup.BackupID)
	report.Blockers = append(report.Blockers, "production apply remains "+ErrProductionMutationForbidden)
	return report
}

func VerifyTrustedInputs(ctx context.Context, root candidate.Root, inputs TrustedInputs, now time.Time) error {
	report := Preflight(ctx, root, inputs, now)
	if !report.OK {
		return fmt.Errorf("cutover: trusted input verification failed")
	}
	return nil
}

func verifyReleaseForGate(ctx context.Context, root candidate.Root, inputs TrustedInputs) error {
	payload, err := canonicalJSON(inputs.Release)
	if err != nil {
		return err
	}
	verified, err := VerifyRelease(payload)
	if err != nil {
		return err
	}
	if inputs.ConfigSHA256 == "" || verified.ConfigSHA256 != inputs.ConfigSHA256 {
		return fmt.Errorf("release: current config hash mismatch")
	}
	if verified.RegistrySHA256 != inputs.Registry.SHA256 {
		return fmt.Errorf("release: ports registry hash mismatch")
	}
	if err := VerifyReleasePolicy(verified, inputs.ReleasePolicy); err != nil {
		return err
	}
	for _, repo := range verified.Repositories {
		if !repo.Clean {
			return fmt.Errorf("DIRTY_WORKTREE: %s", repo.Name)
		}
	}
	if err := VerifyReleaseArtifacts(root, verified); err != nil {
		return err
	}
	return VerifyReleaseCurrent(ctx, verified)
}

func verifyEvidenceForGate(root candidate.Root, events []Evidence, releaseID, configHash string, backup BackupManifest, now time.Time) error {
	if err := VerifyEvidence(events, releaseID, configHash, now); err != nil {
		return err
	}
	externalBound := false
	backupBound := false
	for _, event := range events {
		switch event.Kind {
		case EvidenceExternal:
			externalBound = event.KeyID != "" && event.CredentialExpiry != ""
		case EvidenceBackupRestore:
			receiptPath := event.Detail["restore_receipt"]
			receipt, err := VerifyRestoreReceipt(root, receiptPath, backup)
			backupBound = err == nil && event.Detail["backup_id"] == backup.BackupID && receipt.Verified
		}
	}
	if !externalBound {
		return fmt.Errorf("evidence: external credential identity/expiry is required")
	}
	if !backupBound {
		return fmt.Errorf("evidence: backup restore proof is not bound to the required backup")
	}
	return nil
}

func appendCheck(report *Report, name string, err error, message string) {
	check := Check{Name: name, OK: err == nil, Message: message}
	if err != nil {
		check.Code = safeCode(err.Error())
		if message == "" {
			check.Message = err.Error()
		}
		report.OK = false
	}
	report.Checks = append(report.Checks, check)
}

func safeCode(value string) string {
	for _, code := range []string{ErrProductionMutationForbidden, ErrBoltOnlineRequired, ErrSQLiteOwnerRequired, ErrOwnerSnapshotRequired, ErrRollingFileInconsistent, "DIRTY_WORKTREE"} {
		if strings.Contains(value, code) {
			return code
		}
	}
	return "CHECK_FAILED"
}

func boolErr(ok bool, code string) error {
	if ok {
		return nil
	}
	return errors.New(code)
}
