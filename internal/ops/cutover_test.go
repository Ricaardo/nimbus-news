package ops

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
	"github.com/Ricaardo/nimbus-os/news/internal/systemconfig"
)

type fixedHost string

func (h fixedHost) Hostname() (string, error) { return string(h), nil }

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func TestCutoverUsesVerifiedInputsHMACHostAndOneTimeNonce(t *testing.T) {
	root := candidateRoot(t)
	now := time.Now().UTC().Truncate(time.Second)
	inputs := cutoverInputs(t, root, now)
	if err := InitializeOperatorKey(root, bytes.NewReader(bytes.Repeat([]byte{0x11}, 32))); err != nil {
		t.Fatal(err)
	}
	environment := testCutoverEnvironment(now, "candidate-host", 0x22)
	target := CutoverTarget{Label: "candidate", Path: nimbusdArtifactPath(t, root, inputs), Port: 19081}
	plan, _, err := CreateCutoverPlan(context.Background(), root, inputs, target, environment)
	if err != nil {
		t.Fatal(err)
	}
	challenge := expectedChallenge("activate-candidate", plan.PlanID)
	token, _, err := CreateOperatorToken(root, plan, "activate-candidate", challenge, 10*time.Minute, environment)
	if err != nil {
		t.Fatal(err)
	}
	if token.Signature == "" || token.Host != "candidate-host" || token.ReleaseID != plan.ReleaseID || token.RegistrySHA256 != plan.RegistrySHA256 {
		t.Fatalf("incomplete token binding: %+v", token)
	}
	if _, _, err := ApplyCutover(context.Background(), root, inputs, plan, token, environment); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplyCutover(context.Background(), root, inputs, plan, token, environment); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("replay accepted: %v", err)
	}
}

func TestCutoverNegativeBindingMatrix(t *testing.T) {
	root := candidateRoot(t)
	now := time.Now().UTC().Truncate(time.Second)
	inputs := cutoverInputs(t, root, now)
	if err := InitializeOperatorKey(root, bytes.NewReader(bytes.Repeat([]byte{0x31}, 32))); err != nil {
		t.Fatal(err)
	}
	environment := testCutoverEnvironment(now, "host-a", 0x41)
	target := CutoverTarget{Label: "candidate", Path: nimbusdArtifactPath(t, root, inputs), Port: 19081}
	plan, _, err := CreateCutoverPlan(context.Background(), root, inputs, target, environment)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := CreateOperatorToken(root, plan, "activate-candidate", expectedChallenge("activate-candidate", plan.PlanID), 10*time.Minute, environment)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("production label", func(t *testing.T) {
		_, _, err := CreateCutoverPlan(context.Background(), root, inputs, CutoverTarget{Label: "production", Path: target.Path, Port: target.Port}, environment)
		if err == nil || !strings.Contains(err.Error(), ErrProductionMutationForbidden) {
			t.Fatalf("production target accepted: %v", err)
		}
	})
	t.Run("registered port", func(t *testing.T) {
		port := inputs.Registry.Registry.Services[0].Port
		_, _, err := CreateCutoverPlan(context.Background(), root, inputs, CutoverTarget{Label: "candidate", Path: target.Path, Port: port}, environment)
		if err == nil || !strings.Contains(err.Error(), ErrProductionMutationForbidden) {
			t.Fatalf("registered port accepted: %v", err)
		}
	})
	t.Run("outside root", func(t *testing.T) {
		_, _, err := CreateCutoverPlan(context.Background(), root, inputs, CutoverTarget{Label: "candidate", Path: filepath.Join(t.TempDir(), "active"), Port: target.Port}, environment)
		if err == nil || !strings.Contains(err.Error(), ErrProductionMutationForbidden) {
			t.Fatalf("outside path accepted: %v", err)
		}
	})
	t.Run("backup object as target", func(t *testing.T) {
		path := filepath.Join(root.Path(), filepath.FromSlash(inputs.Backup.Objects[0].ObjectPath))
		_, _, err := CreateCutoverPlan(context.Background(), root, inputs, CutoverTarget{Label: "candidate", Path: path, Port: target.Port}, environment)
		if err == nil || !strings.Contains(err.Error(), ErrProductionMutationForbidden) {
			t.Fatalf("backup object accepted as target: %v", err)
		}
	})
	t.Run("operator key as target", func(t *testing.T) {
		path := filepath.Join(root.Path(), filepath.FromSlash(operatorKeyPath))
		_, _, err := CreateCutoverPlan(context.Background(), root, inputs, CutoverTarget{Label: "candidate", Path: path, Port: target.Port}, environment)
		if err == nil || !strings.Contains(err.Error(), ErrProductionMutationForbidden) {
			t.Fatalf("operator key accepted as target: %v", err)
		}
	})
	t.Run("registry hash changed", func(t *testing.T) {
		changed := inputs
		changed.Registry.SHA256 = strings.Repeat("f", 64)
		if _, _, err := ApplyCutover(context.Background(), root, changed, plan, token, environment); err == nil {
			t.Fatal("changed registry accepted")
		}
	})
	t.Run("untrusted repository", func(t *testing.T) {
		changed := inputs
		changed.ReleasePolicy.Repositories = map[string]string{"news": t.TempDir()}
		if _, _, err := ApplyCutover(context.Background(), root, changed, plan, token, environment); err == nil {
			t.Fatal("untrusted release repository accepted")
		}
	})
	t.Run("evidence tampered", func(t *testing.T) {
		changed := inputs
		changed.Evidence = append([]Evidence(nil), inputs.Evidence...)
		changed.Evidence[0].Samples++
		if _, _, err := ApplyCutover(context.Background(), root, changed, plan, token, environment); err == nil {
			t.Fatal("tampered evidence accepted")
		}
	})
	t.Run("restore evidence without receipt", func(t *testing.T) {
		changed := inputs
		changed.Evidence = append([]Evidence(nil), inputs.Evidence...)
		for i := range changed.Evidence {
			changed.Evidence[i].Detail = cloneDetail(changed.Evidence[i].Detail)
			if changed.Evidence[i].Kind == EvidenceBackupRestore {
				changed.Evidence[i].Detail["restore_receipt"] = filepath.Join(root.Path(), "missing-receipt.json")
			}
		}
		resignEvidenceChain(changed.Evidence)
		if _, _, err := ApplyCutover(context.Background(), root, changed, plan, token, environment); err == nil {
			t.Fatal("restore evidence without persisted receipt accepted")
		}
	})
	t.Run("restore evidence from different backup", func(t *testing.T) {
		sources := make([]BackupSource, 0, len(inputs.BackupRequirements.Objects))
		for _, required := range inputs.BackupRequirements.Objects {
			sources = append(sources, BackupSource{Name: required.Name, Kind: required.Kind, Path: required.SourcePath})
		}
		otherBackup, otherPath, err := CreateBackup(context.Background(), root, BackupOptions{
			ReleaseID: inputs.Release.ReleaseID, ConfigSHA256: inputs.ConfigSHA256,
			Sources: sources, Snapshotter: fakeSnapshotProviderForSources(t, root, inputs.ConfigSHA256, sources),
			Now: now.Add(-3 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		otherRestore, err := RestoreVerify(context.Background(), root, otherPath, inputs.BackupRequirements)
		if err != nil {
			t.Fatal(err)
		}
		changed := inputs
		changed.Evidence = append([]Evidence(nil), inputs.Evidence...)
		for i := range changed.Evidence {
			changed.Evidence[i].Detail = cloneDetail(changed.Evidence[i].Detail)
			if changed.Evidence[i].Kind == EvidenceBackupRestore {
				changed.Evidence[i].Detail["backup_id"] = otherBackup.BackupID
				changed.Evidence[i].Detail["restore_receipt"] = otherRestore.ReceiptPath
			}
		}
		resignEvidenceChain(changed.Evidence)
		if _, _, err := ApplyCutover(context.Background(), root, changed, plan, token, environment); err == nil {
			t.Fatal("restore evidence from a different backup accepted")
		}
	})
	t.Run("backup empty", func(t *testing.T) {
		changed := inputs
		changed.Backup.Objects = nil
		resignBackup(&changed.Backup)
		if _, _, err := ApplyCutover(context.Background(), root, changed, plan, token, environment); err == nil {
			t.Fatal("empty backup accepted")
		}
	})
	for name, mutate := range map[string]func(*OperatorToken, *CutoverEnvironment){
		"forgery": func(value *OperatorToken, _ *CutoverEnvironment) { value.Signature = strings.Repeat("0", 64) },
		"host":    func(_ *OperatorToken, value *CutoverEnvironment) { value.Host = fixedHost("host-b") },
		"action":  func(value *OperatorToken, _ *CutoverEnvironment) { value.Action = "rollback-candidate" },
		"plan":    func(value *OperatorToken, _ *CutoverEnvironment) { value.PlanID = strings.Repeat("a", 64) },
		"expiry": func(value *OperatorToken, _ *CutoverEnvironment) {
			value.ExpiresAt = now.Add(-time.Minute).Format(time.RFC3339)
		},
	} {
		t.Run("token "+name, func(t *testing.T) {
			changedToken := token
			changedEnvironment := environment
			mutate(&changedToken, &changedEnvironment)
			if _, _, err := ApplyCutover(context.Background(), root, inputs, plan, changedToken, changedEnvironment); err == nil {
				t.Fatalf("%s token accepted", name)
			}
		})
	}
}

func TestOperatorKeyIsExclusiveAndNotInPlanOrTokenPayload(t *testing.T) {
	root := candidateRoot(t)
	key := bytes.Repeat([]byte{0x55}, 32)
	if err := InitializeOperatorKey(root, bytes.NewReader(key)); err != nil {
		t.Fatal(err)
	}
	if err := InitializeOperatorKey(root, bytes.NewReader(bytes.Repeat([]byte{0x66}, 32))); err == nil {
		t.Fatal("operator key overwritten")
	}
	stored, err := (CandidateKeyStore{}).Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, key) {
		t.Fatal("operator key changed")
	}
}

func nimbusdArtifactPath(t *testing.T, root candidate.Root, inputs TrustedInputs) string {
	t.Helper()
	for _, artifact := range inputs.Release.Artifacts {
		if artifact.Name == "nimbusd" {
			return filepath.Join(root.Path(), filepath.FromSlash(artifact.Path))
		}
	}
	t.Fatal("nimbusd artifact missing")
	return ""
}

func cutoverInputs(t *testing.T, root candidate.Root, now time.Time) TrustedInputs {
	t.Helper()
	cfg, err := systemconfig.Load(filepath.Join("..", "..", "config", "system.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := cfg.LoadPortsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	configHash := strings.Repeat("c", 64)
	repo := cleanReleaseRepo(t)
	manifest, _, err := BuildReleaseFromSource(context.Background(), root, &fakeBuilder{data: []byte("candidate-binary")}, BuildSpec{
		Repositories: map[string]string{"news": repo},
		Builds: []ArtifactBuildSpec{
			{Name: "nimbusctl", Repository: "news", Argv: approvedBuildArgv("./cmd/nimbusctl")},
			{Name: "nimbusd", Repository: "news", Argv: approvedBuildArgv("./cmd/nimbusd")},
		},
		Include: []string{"cmd"}, MaxFileBytes: 1 << 20,
		ConfigSHA256: configHash, RegistrySHA256: registry.SHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	sources, snapshotter := fixedSnapshotFixture(t, root, configHash)
	backup, backupPath, err := CreateBackup(context.Background(), root, BackupOptions{
		ReleaseID: manifest.ReleaseID, ConfigSHA256: configHash, Sources: sources,
		Snapshotter: snapshotter, Now: now.Add(-4 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := NewBackupRequirements(root, RequiredObjects(sources))
	if err != nil {
		t.Fatal(err)
	}
	restore, err := RestoreVerify(context.Background(), root, backupPath, requirements)
	if err != nil {
		t.Fatal(err)
	}
	events := []Evidence{
		{Kind: EvidenceRollbackRetention, ProbeID: "rollback", StartedAt: now.Add(-20 * 24 * time.Hour).Format(time.RFC3339), EndedAt: now.Add(-13 * 24 * time.Hour).Format(time.RFC3339), Samples: 7, Passed: true},
		{Kind: EvidenceShadow, ProbeID: "shadow", StartedAt: now.Add(-12 * 24 * time.Hour).Format(time.RFC3339), EndedAt: now.Add(-9 * 24 * time.Hour).Format(time.RFC3339), Samples: 72, Passed: true},
		{Kind: EvidenceSoak, ProbeID: "soak", StartedAt: now.Add(-8 * 24 * time.Hour).Format(time.RFC3339), EndedAt: now.Add(-7 * 24 * time.Hour).Format(time.RFC3339), Samples: 24, Passed: true},
		{Kind: EvidenceExternal, ProbeID: "external", KeyID: "external-key-id", CredentialExpiry: now.Add(24 * time.Hour).Format(time.RFC3339), StartedAt: now.Add(-6 * 24 * time.Hour).Format(time.RFC3339), EndedAt: now.Add(-6*24*time.Hour + time.Hour).Format(time.RFC3339), Samples: 2, Passed: true},
		{Kind: EvidenceBackupRestore, ProbeID: "restore", StartedAt: now.Add(-5 * 24 * time.Hour).Format(time.RFC3339), EndedAt: now.Add(-5*24*time.Hour + time.Hour).Format(time.RFC3339), Samples: 1, Passed: true, Detail: map[string]string{"backup_id": backup.BackupID, "restore_receipt": restore.ReceiptPath}},
	}
	recorded := make([]Evidence, 0, len(events))
	for _, event := range events {
		event.ReleaseID = manifest.ReleaseID
		event.ConfigSHA256 = configHash
		value, _, err := RecordEvidence(root, "cutover-evidence", event)
		if err != nil {
			t.Fatal(err)
		}
		recorded = append(recorded, value)
	}
	return TrustedInputs{
		ConfigSHA256: configHash, Release: manifest, Evidence: recorded, Backup: backup,
		BackupRequirements: requirements, Registry: registry,
		ReleasePolicy: ReleasePolicy{
			Repositories: map[string]string{"news": repo},
			Builds: []ArtifactBuildSpec{
				{Name: "nimbusctl", Repository: "news", Argv: approvedBuildArgv("./cmd/nimbusctl")},
				{Name: "nimbusd", Repository: "news", Argv: approvedBuildArgv("./cmd/nimbusd")},
			},
		},
	}
}

func testCutoverEnvironment(now time.Time, host string, randomByte byte) CutoverEnvironment {
	return CutoverEnvironment{
		Host: fixedHost(host), Clock: fixedClock{now: now},
		Random: bytes.NewReader(bytes.Repeat([]byte{randomByte}, 32)), Keys: CandidateKeyStore{},
	}
}

func cloneDetail(value map[string]string) map[string]string {
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func resignEvidenceChain(events []Evidence) {
	previous := ""
	for i := range events {
		events[i].PreviousHash = previous
		events[i].Hash = ""
		payload, _ := canonicalJSON(events[i])
		events[i].Hash = sha256Hex(payload)
		previous = events[i].Hash
	}
}
