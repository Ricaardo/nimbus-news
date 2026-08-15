package ops

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
)

const (
	CutoverPlanSchema              = "nimbus-cutover-plan/v2"
	OperatorTokenSchema            = "nimbus-operator-token/v2"
	ErrProductionMutationForbidden = "PRODUCTION_MUTATION_FORBIDDEN"
	operatorKeyPath                = "cutover/operator.hmac"
)

type CutoverPlan struct {
	Schema               string `json:"schema"`
	PlanID               string `json:"plan_id"`
	ReleaseID            string `json:"release_id"`
	ConfigSHA256         string `json:"config_sha256"`
	RegistrySHA256       string `json:"registry_sha256"`
	BackupID             string `json:"backup_id"`
	EvidenceTail         string `json:"evidence_tail"`
	CandidateRootBinding string `json:"candidate_root_binding"`
	TargetLabel          string `json:"target_label"`
	TargetPath           string `json:"target_path"`
	TargetSHA256         string `json:"target_sha256"`
	TargetSize           int64  `json:"target_size"`
	TargetPort           int    `json:"target_port"`
	Host                 string `json:"host"`
	CreatedAt            string `json:"created_at"`
}

type CutoverTarget struct {
	Label string
	Path  string
	Port  int
}

type OperatorToken struct {
	Schema         string `json:"schema"`
	Signature      string `json:"signature"`
	PlanID         string `json:"plan_id"`
	ReleaseID      string `json:"release_id"`
	ConfigSHA256   string `json:"config_sha256"`
	RegistrySHA256 string `json:"registry_sha256"`
	BackupID       string `json:"backup_id"`
	EvidenceTail   string `json:"evidence_tail"`
	Host           string `json:"host"`
	Action         string `json:"action"`
	Challenge      string `json:"challenge"`
	Nonce          string `json:"nonce"`
	ExpiresAt      string `json:"expires_at"`
}

type ActivationReceipt struct {
	Schema    string `json:"schema"`
	PlanID    string `json:"plan_id"`
	TokenHash string `json:"token_hash"`
	Action    string `json:"action"`
	CreatedAt string `json:"created_at"`
	Effect    string `json:"effect"`
}

type HostProvider interface {
	Hostname() (string, error)
}

type Clock interface {
	Now() time.Time
}

type KeyStore interface {
	Load(candidate.Root) ([]byte, error)
	Consume(candidate.Root, string, string) error
}

type CutoverEnvironment struct {
	Host   HostProvider
	Clock  Clock
	Random io.Reader
	Keys   KeyStore
}

type OSHost struct{}

func (OSHost) Hostname() (string, error) { return os.Hostname() }

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type CandidateKeyStore struct{}

func ProductionCutoverEnvironment() CutoverEnvironment {
	return CutoverEnvironment{Host: OSHost{}, Clock: RealClock{}, Random: rand.Reader, Keys: CandidateKeyStore{}}
}

func InitializeOperatorKey(root candidate.Root, random io.Reader) error {
	if random == nil {
		random = rand.Reader
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(random, key); err != nil {
		return fmt.Errorf("operator key: random source: %w", err)
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return err
	}
	defer capability.Close()
	file, err := capability.CreateExclusive(operatorKeyPath, 0o600)
	if err != nil {
		return fmt.Errorf("operator key: create exclusive: %w", err)
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = capability.Unlink(operatorKeyPath)
		}
	}()
	if _, err := file.Write(key); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := capability.SyncDir("cutover"); err != nil {
		return err
	}
	committed = true
	return nil
}

func (CandidateKeyStore) Load(root candidate.Root) ([]byte, error) {
	capability, err := root.OpenCapability()
	if err != nil {
		return nil, err
	}
	defer capability.Close()
	key, err := capability.ReadFile(operatorKeyPath, 32, 0o600)
	if err != nil {
		return nil, fmt.Errorf("operator key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("operator key: invalid length")
	}
	return key, nil
}

func (CandidateKeyStore) Consume(root candidate.Root, nonce, tokenHash string) error {
	if len(nonce) != 64 || !validSHA256(nonce) || !validSHA256(tokenHash) {
		return fmt.Errorf("operator token: invalid replay binding")
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return err
	}
	defer capability.Close()
	relative := "cutover/replay/" + nonce + ".json"
	file, err := capability.CreateExclusive(relative, 0o600)
	if os.IsExist(err) {
		return fmt.Errorf("operator token: replay rejected")
	}
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = capability.Unlink(relative)
		}
	}()
	payload, _ := json.Marshal(map[string]string{"nonce": nonce, "token_hash": tokenHash})
	payload = append(payload, '\n')
	if _, err := file.Write(payload); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := capability.SyncDir("cutover/replay"); err != nil {
		return err
	}
	committed = true
	return nil
}

func CreateCutoverPlan(ctx context.Context, root candidate.Root, inputs TrustedInputs, target CutoverTarget, environment CutoverEnvironment) (CutoverPlan, string, error) {
	now, host, err := cutoverContext(environment)
	if err != nil {
		return CutoverPlan{}, "", err
	}
	if err := VerifyTrustedInputs(ctx, root, inputs, now); err != nil {
		return CutoverPlan{}, "", err
	}
	if target.Label != "candidate" || target.Port < 1 || target.Port > 65535 || inputs.Registry.Registry.Contains(target.Port) {
		return CutoverPlan{}, "", fmt.Errorf(ErrProductionMutationForbidden)
	}
	targetArtifact, targetPath, err := verifyNimbusdTarget(root, inputs, target.Path)
	if err != nil {
		return CutoverPlan{}, "", fmt.Errorf("%s: %w", ErrProductionMutationForbidden, err)
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return CutoverPlan{}, "", err
	}
	binding := capability.Binding()
	capability.Close()
	tail := evidenceTail(inputs.Evidence)
	if tail == "" {
		return CutoverPlan{}, "", fmt.Errorf("cutover: evidence tail is required")
	}
	plan := CutoverPlan{
		Schema: CutoverPlanSchema, ReleaseID: inputs.Release.ReleaseID,
		ConfigSHA256: inputs.Release.ConfigSHA256, RegistrySHA256: inputs.Registry.SHA256,
		BackupID: inputs.Backup.BackupID, EvidenceTail: tail, CandidateRootBinding: binding,
		TargetLabel: target.Label, TargetPath: targetPath,
		TargetSHA256: targetArtifact.SHA256, TargetSize: targetArtifact.Size, TargetPort: target.Port,
		Host: host, CreatedAt: now.Format(time.RFC3339),
	}
	payload, err := canonicalJSON(plan)
	if err != nil {
		return plan, "", err
	}
	plan.PlanID = sha256Hex(payload)
	payload, err = canonicalJSON(plan)
	if err != nil {
		return plan, "", err
	}
	path, err := writeCAS(root, "cutover/plans", ".json", payload)
	return plan, path, err
}

func CreateOperatorToken(root candidate.Root, plan CutoverPlan, action, challenge string, ttl time.Duration, environment CutoverEnvironment) (OperatorToken, string, error) {
	now, host, err := cutoverContext(environment)
	if err != nil {
		return OperatorToken{}, "", err
	}
	if err := verifyPlanAt(root, plan, host, now); err != nil {
		return OperatorToken{}, "", err
	}
	if ttl <= 0 || ttl > 15*time.Minute || challenge != expectedChallenge(action, plan.PlanID) || !allowedAction(action) {
		return OperatorToken{}, "", fmt.Errorf("operator token: invalid binding or lifetime")
	}
	if environment.Random == nil || environment.Keys == nil {
		return OperatorToken{}, "", fmt.Errorf("operator token: environment is incomplete")
	}
	nonceBytes := make([]byte, 32)
	if _, err := io.ReadFull(environment.Random, nonceBytes); err != nil {
		return OperatorToken{}, "", err
	}
	key, err := environment.Keys.Load(root)
	if err != nil {
		return OperatorToken{}, "", err
	}
	token := OperatorToken{
		Schema: OperatorTokenSchema, PlanID: plan.PlanID, ReleaseID: plan.ReleaseID,
		ConfigSHA256: plan.ConfigSHA256, RegistrySHA256: plan.RegistrySHA256,
		BackupID: plan.BackupID, EvidenceTail: plan.EvidenceTail, Host: host,
		Action: action, Challenge: challenge, Nonce: hex.EncodeToString(nonceBytes),
		ExpiresAt: now.Add(ttl).UTC().Format(time.RFC3339),
	}
	signature, err := signOperatorToken(token, key)
	if err != nil {
		return OperatorToken{}, "", err
	}
	token.Signature = signature
	payload, err := canonicalJSON(token)
	if err != nil {
		return OperatorToken{}, "", err
	}
	path, err := writeCAS(root, "cutover/tokens", ".json", payload)
	return token, path, err
}

func ApplyCutover(ctx context.Context, root candidate.Root, inputs TrustedInputs, plan CutoverPlan, token OperatorToken, environment CutoverEnvironment) (ActivationReceipt, string, error) {
	return recordCandidateAction(ctx, root, inputs, plan, token, "activate-candidate", environment)
}

func RollbackCandidate(ctx context.Context, root candidate.Root, inputs TrustedInputs, plan CutoverPlan, token OperatorToken, environment CutoverEnvironment) (ActivationReceipt, string, error) {
	return recordCandidateAction(ctx, root, inputs, plan, token, "rollback-candidate", environment)
}

func recordCandidateAction(ctx context.Context, root candidate.Root, inputs TrustedInputs, plan CutoverPlan, token OperatorToken, action string, environment CutoverEnvironment) (ActivationReceipt, string, error) {
	now, host, err := cutoverContext(environment)
	if err != nil {
		return ActivationReceipt{}, "", err
	}
	if err := VerifyTrustedInputs(ctx, root, inputs, now); err != nil {
		return ActivationReceipt{}, "", err
	}
	if err := verifyPlanAt(root, plan, host, now); err != nil {
		return ActivationReceipt{}, "", err
	}
	if err := verifyPlanInputs(plan, inputs); err != nil {
		return ActivationReceipt{}, "", err
	}
	if plan.TargetLabel != "candidate" || inputs.Registry.Registry.Contains(plan.TargetPort) {
		return ActivationReceipt{}, "", fmt.Errorf(ErrProductionMutationForbidden)
	}
	targetArtifact, targetPath, err := verifyNimbusdTarget(root, inputs, plan.TargetPath)
	if err != nil {
		return ActivationReceipt{}, "", fmt.Errorf("%s: %w", ErrProductionMutationForbidden, err)
	}
	if targetPath != plan.TargetPath || targetArtifact.SHA256 != plan.TargetSHA256 ||
		targetArtifact.Size != plan.TargetSize {
		return ActivationReceipt{}, "", fmt.Errorf("%s: cutover target binding mismatch", ErrProductionMutationForbidden)
	}
	if environment.Keys == nil {
		return ActivationReceipt{}, "", fmt.Errorf("operator token: key store is required")
	}
	key, err := environment.Keys.Load(root)
	if err != nil {
		return ActivationReceipt{}, "", err
	}
	tokenHash, err := verifyOperatorToken(token, plan, action, host, now, key)
	if err != nil {
		return ActivationReceipt{}, "", err
	}
	if err := environment.Keys.Consume(root, token.Nonce, tokenHash); err != nil {
		return ActivationReceipt{}, "", err
	}
	receipt := ActivationReceipt{
		Schema: "nimbus-candidate-action/v1", PlanID: plan.PlanID, TokenHash: tokenHash,
		Action: action, CreatedAt: now.Format(time.RFC3339),
		Effect: "candidate-pointer-record-only; no launchctl or production mutation",
	}
	payload, _ := canonicalJSON(receipt)
	path, err := writeCAS(root, "cutover/receipts", ".json", payload)
	return receipt, path, err
}

func verifyPlan(plan CutoverPlan) error {
	if plan.Schema != CutoverPlanSchema || plan.PlanID == "" || plan.ReleaseID == "" || !validSHA256(plan.ConfigSHA256) || !validSHA256(plan.RegistrySHA256) || plan.BackupID == "" || plan.EvidenceTail == "" || plan.CandidateRootBinding == "" || plan.TargetLabel != "candidate" || plan.TargetPath == "" || !validSHA256(plan.TargetSHA256) || plan.TargetSize < 0 || plan.TargetPort < 1 || plan.TargetPort > 65535 || plan.Host == "" {
		return fmt.Errorf("cutover: invalid plan")
	}
	want := plan.PlanID
	plan.PlanID = ""
	payload, _ := canonicalJSON(plan)
	if sha256Hex(payload) != want {
		return fmt.Errorf("cutover: plan hash mismatch")
	}
	return nil
}

func verifyNimbusdTarget(root candidate.Root, inputs TrustedInputs, supplied string) (ArtifactRecord, string, error) {
	var target ArtifactRecord
	found := false
	for _, artifact := range inputs.Release.Artifacts {
		if artifact.Name != "nimbusd" {
			continue
		}
		if found {
			return ArtifactRecord{}, "", fmt.Errorf("cutover: duplicate nimbusd artifact")
		}
		target = artifact
		found = true
	}
	if !found || target.Size < 0 || target.Size > inputs.Release.MaxFileBytes ||
		!validSHA256(target.SHA256) {
		return ArtifactRecord{}, "", fmt.Errorf("cutover: verified nimbusd artifact is required")
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return ArtifactRecord{}, "", err
	}
	defer capability.Close()
	expectedPath, err := capability.Absolute(target.Path)
	if err != nil {
		return ArtifactRecord{}, "", err
	}
	expected, err := root.RequireFile("verified nimbusd artifact", expectedPath)
	if err != nil {
		return ArtifactRecord{}, "", err
	}
	actual, err := root.RequireFile("candidate activation target", supplied)
	if err != nil {
		return ArtifactRecord{}, "", err
	}
	if actual != expected {
		return ArtifactRecord{}, "", fmt.Errorf("cutover: target is not the verified nimbusd artifact")
	}
	data, err := capability.ReadFile(target.Path, inputs.Release.MaxFileBytes, 0o600)
	if err != nil {
		return ArtifactRecord{}, "", err
	}
	if int64(len(data)) != target.Size || sha256Hex(data) != target.SHA256 {
		return ArtifactRecord{}, "", fmt.Errorf("cutover: nimbusd artifact metadata/hash mismatch")
	}
	return target, expected, nil
}

func verifyPlanAt(root candidate.Root, plan CutoverPlan, host string, now time.Time) error {
	if err := verifyPlan(plan); err != nil {
		return err
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return err
	}
	binding := capability.Binding()
	capability.Close()
	if plan.CandidateRootBinding != binding || plan.Host != host {
		return fmt.Errorf("cutover: root or host binding mismatch")
	}
	created, err := time.Parse(time.RFC3339, plan.CreatedAt)
	if err != nil || created.After(now.Add(5*time.Minute)) || now.Sub(created) > 24*time.Hour {
		return fmt.Errorf("cutover: plan is expired or has invalid time")
	}
	return nil
}

func verifyPlanInputs(plan CutoverPlan, inputs TrustedInputs) error {
	if plan.ReleaseID != inputs.Release.ReleaseID || plan.ConfigSHA256 != inputs.Release.ConfigSHA256 || plan.RegistrySHA256 != inputs.Registry.SHA256 || plan.BackupID != inputs.Backup.BackupID || plan.EvidenceTail != evidenceTail(inputs.Evidence) {
		return fmt.Errorf("cutover: trusted inputs changed")
	}
	return nil
}

func verifyOperatorToken(token OperatorToken, plan CutoverPlan, action, host string, now time.Time, key []byte) (string, error) {
	if token.Schema != OperatorTokenSchema || token.Signature == "" || token.PlanID != plan.PlanID || token.ReleaseID != plan.ReleaseID || token.ConfigSHA256 != plan.ConfigSHA256 || token.RegistrySHA256 != plan.RegistrySHA256 || token.BackupID != plan.BackupID || token.EvidenceTail != plan.EvidenceTail || token.Host != host || token.Action != action || token.Challenge != expectedChallenge(action, plan.PlanID) || !validSHA256(token.Nonce) {
		return "", fmt.Errorf("operator token: binding mismatch")
	}
	expected, err := signOperatorToken(token, key)
	if err != nil {
		return "", err
	}
	provided, err := hex.DecodeString(token.Signature)
	if err != nil || !hmac.Equal(provided, mustDecodeHex(expected)) {
		return "", fmt.Errorf("operator token: signature mismatch")
	}
	expiry, err := time.Parse(time.RFC3339, token.ExpiresAt)
	if err != nil || !expiry.After(now) || expiry.After(now.Add(15*time.Minute)) {
		return "", fmt.Errorf("operator token: expired or lifetime exceeds 15 minutes")
	}
	payload, err := canonicalJSON(token)
	if err != nil {
		return "", err
	}
	return sha256Hex(payload), nil
}

func signOperatorToken(token OperatorToken, key []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("operator key: invalid length")
	}
	token.Signature = ""
	payload, err := canonicalJSON(token)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func mustDecodeHex(value string) []byte {
	decoded, _ := hex.DecodeString(value)
	return decoded
}

func cutoverContext(environment CutoverEnvironment) (time.Time, string, error) {
	if environment.Clock == nil || environment.Host == nil {
		return time.Time{}, "", fmt.Errorf("cutover: environment is incomplete")
	}
	now := environment.Clock.Now().UTC()
	host, err := environment.Host.Hostname()
	if err != nil || host == "" {
		return time.Time{}, "", fmt.Errorf("cutover: hostname unavailable")
	}
	return now, host, nil
}

func evidenceTail(events []Evidence) string {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].Hash
}

func allowedAction(action string) bool {
	return action == "activate-candidate" || action == "rollback-candidate"
}

func expectedChallenge(action, planID string) string { return action + ":" + planID }
