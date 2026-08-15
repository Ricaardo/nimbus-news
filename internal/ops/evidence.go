package ops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/candidate"
	"github.com/Ricaardo/nimbus-os/news/internal/systemconfig"

	"golang.org/x/sys/unix"
)

const EvidenceSchema = "nimbus-evidence/v1"

var evidenceProcessLocks sync.Map

const (
	EvidenceExternal          = "external"
	EvidenceShadow            = "shadow72h"
	EvidenceSoak              = "soak24h"
	EvidenceBackupRestore     = "backup_restore"
	EvidenceRollbackRetention = "rollback_retention7d"
)

type Evidence struct {
	Schema           string            `json:"schema"`
	Hash             string            `json:"hash"`
	PreviousHash     string            `json:"previous_hash"`
	ReleaseID        string            `json:"release_id"`
	ConfigSHA256     string            `json:"config_sha256"`
	Kind             string            `json:"kind"`
	ProbeID          string            `json:"probe_id"`
	KeyID            string            `json:"key_id,omitempty"`
	CredentialExpiry string            `json:"credential_expiry,omitempty"`
	StartedAt        string            `json:"started_at"`
	EndedAt          string            `json:"ended_at"`
	Samples          int64             `json:"samples"`
	Passed           bool              `json:"passed"`
	Detail           map[string]string `json:"detail,omitempty"`
}

// EvidenceDirForRelease returns the evidence directory for a specific release
// under the given base directory. It includes the release ID prefix for isolation.
// Corresponds to Config.EvidenceDir(); keep the two in sync.
func EvidenceDirForRelease(baseDir, releaseID string) string {
	return filepath.Join(baseDir, "release-"+systemconfig.ReleasePrefix(releaseID))
}

func RecordEvidence(root candidate.Root, directory string, event Evidence) (Evidence, string, error) {
	if event.Schema == "" {
		event.Schema = EvidenceSchema
	}
	if event.Schema != EvidenceSchema || event.ReleaseID == "" || event.ConfigSHA256 == "" || event.ProbeID == "" {
		return event, "", fmt.Errorf("evidence: missing binding or probe id")
	}
	if event.Hash != "" || event.PreviousHash != "" {
		return event, "", fmt.Errorf("evidence: hash fields are output-only")
	}
	if hasSensitiveEvidence(event) {
		return event, "", fmt.Errorf("evidence: credential secret/hash fields are forbidden")
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return event, "", err
	}
	defer capability.Close()
	base, err := capability.Relative(directory)
	if err != nil {
		return event, "", err
	}
	chainDirectory := evidenceChainDirectory(base, event.ReleaseID, event.ConfigSHA256)
	lockKey := capability.Binding() + ":" + chainDirectory
	processLock, _ := evidenceProcessLocks.LoadOrStore(lockKey, &sync.Mutex{})
	processLock.(*sync.Mutex).Lock()
	defer processLock.(*sync.Mutex).Unlock()
	if err := capability.MkdirAll(chainDirectory, 0o700); err != nil {
		return event, "", err
	}
	lock, err := capability.OpenLock(chainDirectory + "/chain.lock")
	if err != nil {
		return event, "", err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return event, "", err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	existing, err := loadEvidenceFrom(capability, chainDirectory)
	if err != nil {
		return event, "", err
	}
	for _, recorded := range existing {
		if recorded.ReleaseID != event.ReleaseID || recorded.ConfigSHA256 != event.ConfigSHA256 {
			return event, "", fmt.Errorf("evidence: chain binding mismatch")
		}
	}
	if len(existing) > 0 {
		tail := existing[len(existing)-1]
		event.PreviousHash = tail.Hash
	}
	payload, err := canonicalJSON(event)
	if err != nil {
		return event, "", err
	}
	event.Hash = sha256Hex(payload)
	payload, err = canonicalJSON(event)
	if err != nil {
		return event, "", err
	}
	path, err := capability.WriteCAS(chainDirectory, ".json", payload)
	return event, path, err
}

func LoadEvidence(root candidate.Root, directory string) ([]Evidence, error) {
	cap, err := root.OpenCapability()
	if err != nil {
		return nil, err
	}
	defer cap.Close()
	base, err := cap.Relative(directory)
	if err != nil {
		return nil, err
	}
	entries, err := cap.List(base)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var chains []string
	for _, entry := range entries {
		if entry.Mode.IsDir() && strings.HasPrefix(entry.Name, "chain-") {
			chains = append(chains, entry.Name)
		}
	}
	if len(chains) == 0 {
		return nil, nil
	}
	if len(chains) != 1 {
		return nil, fmt.Errorf("evidence: release/config binding is required")
	}
	return loadEvidenceFrom(cap, base+"/"+chains[0])
}

func LoadEvidenceChain(root candidate.Root, directory, releaseID, configHash string) ([]Evidence, error) {
	if releaseID == "" || configHash == "" {
		return nil, fmt.Errorf("evidence: release/config binding is required")
	}
	capability, err := root.OpenCapability()
	if err != nil {
		return nil, err
	}
	defer capability.Close()
	base, err := capability.Relative(directory)
	if err != nil {
		return nil, err
	}
	events, err := loadEvidenceFrom(capability, evidenceChainDirectory(base, releaseID, configHash))
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	return events, err
}

func loadEvidenceFrom(capability *candidate.Capability, directory string) ([]Evidence, error) {
	entries, err := capability.List(directory)
	if err != nil {
		return nil, err
	}
	var unordered []Evidence
	for _, entry := range entries {
		if entry.Mode.IsDir() || !strings.HasSuffix(entry.Name, ".json") {
			continue
		}
		if !entry.Mode.IsRegular() || entry.Mode.Perm() != 0o600 {
			return nil, fmt.Errorf("evidence: unsafe record mode")
		}
		data, err := capability.ReadFile(directory+"/"+entry.Name, 1<<20, 0o600)
		if err != nil {
			return nil, err
		}
		var event Evidence
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			return nil, err
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return nil, fmt.Errorf("evidence: extra JSON value")
			}
			return nil, err
		}
		unordered = append(unordered, event)
	}
	if len(unordered) == 0 {
		return nil, nil
	}
	byPrevious := make(map[string]Evidence, len(unordered))
	for _, event := range unordered {
		if _, exists := byPrevious[event.PreviousHash]; exists {
			return nil, fmt.Errorf("evidence: forked hash chain")
		}
		byPrevious[event.PreviousHash] = event
	}
	out := make([]Evidence, 0, len(unordered))
	previous := ""
	for len(out) < len(unordered) {
		event, ok := byPrevious[previous]
		if !ok {
			return nil, fmt.Errorf("evidence: disconnected hash chain")
		}
		out = append(out, event)
		previous = event.Hash
	}
	for i, event := range out {
		copy := event
		hash := copy.Hash
		copy.Hash = ""
		payload, err := canonicalJSON(copy)
		if err != nil || hash == "" || sha256Hex(payload) != hash {
			return nil, fmt.Errorf("evidence: hash chain mismatch")
		}
		if i == 0 && event.PreviousHash != "" {
			return nil, fmt.Errorf("evidence: disconnected hash chain")
		}
	}
	return out, nil
}

func evidenceChainDirectory(base, releaseID, configHash string) string {
	sum := sha256Hex([]byte(releaseID + "\x00" + configHash))
	return strings.TrimSuffix(base, "/") + "/chain-" + sum
}

func VerifyEvidence(events []Evidence, releaseID, configHash string, now time.Time) error {
	if len(events) == 0 {
		return fmt.Errorf("evidence: missing samples")
	}
	previous := ""
	seen := map[string]bool{}
	last := time.Time{}
	lastEnd := time.Time{}
	for _, event := range events {
		if event.Schema != EvidenceSchema || event.ReleaseID != releaseID || event.ConfigSHA256 != configHash {
			return fmt.Errorf("evidence: release/config mismatch")
		}
		copy := event
		hash := copy.Hash
		copy.Hash = ""
		payload, _ := canonicalJSON(copy)
		if sha256Hex(payload) != hash || copy.PreviousHash != previous {
			return fmt.Errorf("evidence: hash chain mismatch")
		}
		start, err := time.Parse(time.RFC3339, event.StartedAt)
		if err != nil {
			return fmt.Errorf("evidence: invalid start")
		}
		end, err := time.Parse(time.RFC3339, event.EndedAt)
		if err != nil || end.Before(start) || end.After(now.Add(5*time.Minute)) {
			return fmt.Errorf("evidence: invalid time")
		}
		if now.Sub(end) > 30*24*time.Hour {
			return fmt.Errorf("evidence: sample is stale")
		}
		if (!last.IsZero() && start.Before(last)) || (!lastEnd.IsZero() && end.Before(lastEnd)) {
			return fmt.Errorf("evidence: clock moved backwards")
		}
		last = start
		lastEnd = end
		if event.CredentialExpiry != "" {
			expiry, err := time.Parse(time.RFC3339, event.CredentialExpiry)
			if err != nil || !expiry.After(end) || !expiry.After(now) {
				return fmt.Errorf("evidence: credential expired or invalid")
			}
		}
		if !event.Passed || event.Samples < 1 {
			return fmt.Errorf("evidence: failed or empty sample")
		}
		duration := end.Sub(start)
		switch event.Kind {
		case EvidenceShadow:
			if duration < 72*time.Hour {
				return fmt.Errorf("evidence: shadow duration insufficient")
			}
		case EvidenceSoak:
			if duration < 24*time.Hour {
				return fmt.Errorf("evidence: soak duration insufficient")
			}
		case EvidenceRollbackRetention:
			if duration < 7*24*time.Hour {
				return fmt.Errorf("evidence: rollback retention insufficient")
			}
		case EvidenceExternal, EvidenceBackupRestore:
		default:
			return fmt.Errorf("evidence: unknown kind")
		}
		seen[event.Kind] = true
		previous = hash
	}
	for _, kind := range []string{EvidenceExternal, EvidenceShadow, EvidenceSoak, EvidenceBackupRestore, EvidenceRollbackRetention} {
		if !seen[kind] {
			return fmt.Errorf("evidence: missing %s", kind)
		}
	}
	return nil
}
func hasSensitiveEvidence(event Evidence) bool {
	for key, value := range event.Detail {
		lower := strings.ToLower(key)
		lowerValue := strings.ToLower(value)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "hash") || strings.Contains(lower, "credential") || strings.Contains(lowerValue, "secret") || strings.Contains(lowerValue, "token") || strings.Contains(lowerValue, "password") || strings.Contains(lowerValue, "api_key") || strings.Contains(lowerValue, "bearer ") {
			return true
		}
	}
	return false
}
