package ops

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEvidenceChainDurationsBindingAndTamper(t *testing.T) {
	root := candidateRoot(t)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	var events []Evidence
	cases := []struct {
		kind       string
		start, end time.Time
	}{{EvidenceRollbackRetention, now.Add(-8 * 24 * time.Hour), now.Add(-2 * time.Hour)}, {EvidenceShadow, now.Add(-4 * 24 * time.Hour), now.Add(-time.Hour)}, {EvidenceSoak, now.Add(-25 * time.Hour), now.Add(-30 * time.Minute)}, {EvidenceExternal, now.Add(-20 * time.Minute), now.Add(-10 * time.Minute)}, {EvidenceBackupRestore, now.Add(-9 * time.Minute), now.Add(-time.Minute)}}
	for i, item := range cases {
		event, _, err := RecordEvidence(root, "evidence", Evidence{ReleaseID: "release", ConfigSHA256: "config", Kind: item.kind, ProbeID: "probe", KeyID: "key-id", CredentialExpiry: now.Add(24 * time.Hour).Format(time.RFC3339), StartedAt: item.start.Format(time.RFC3339), EndedAt: item.end.Format(time.RFC3339), Samples: int64(i + 1), Passed: true})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	loaded, err := LoadEvidenceChain(root, "evidence", "release", "config")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidence(loaded, "release", "config", now); err != nil {
		t.Fatal(err)
	}
	tampered := append([]Evidence(nil), events...)
	tampered[2].Samples++
	if err := VerifyEvidence(tampered, "release", "config", now); err == nil || !strings.Contains(err.Error(), "hash chain") {
		t.Fatalf("tamper accepted: %v", err)
	}
	if err := VerifyEvidence(events[:4], "release", "config", now); err == nil {
		t.Fatal("missing evidence accepted")
	}
	if err := VerifyEvidence(events, "other", "config", now); err == nil {
		t.Fatal("release mismatch accepted")
	}
}

func TestEvidenceRejectsCredentialMaterialForkAndClockRollback(t *testing.T) {
	root := candidateRoot(t)
	_, _, err := RecordEvidence(root, "evidence", Evidence{ReleaseID: "r", ConfigSHA256: "c", ProbeID: "p", Detail: map[string]string{"api_token": "x"}})
	if err == nil {
		t.Fatal("credential material accepted")
	}
	now := time.Now().UTC()
	first, _, err := RecordEvidence(root, "fork", Evidence{ReleaseID: "r", ConfigSHA256: "c", Kind: EvidenceExternal, ProbeID: "p", StartedAt: now.Add(-time.Hour).Format(time.RFC3339), EndedAt: now.Format(time.RFC3339), Samples: 1, Passed: true})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = RecordEvidence(root, "fork", Evidence{ReleaseID: "r", ConfigSHA256: "c", Kind: EvidenceExternal, ProbeID: "p", PreviousHash: first.Hash, StartedAt: now.Format(time.RFC3339), EndedAt: now.Format(time.RFC3339), Samples: 1, Passed: true})
	if err == nil {
		t.Fatal("caller-provided previous hash accepted")
	}
	_, _, err = RecordEvidence(root, "fork", Evidence{ReleaseID: "r", ConfigSHA256: "c", Kind: EvidenceExternal, ProbeID: "p2", StartedAt: now.Format(time.RFC3339), EndedAt: now.Format(time.RFC3339), Samples: 1, Passed: true})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEvidenceChain(root, "fork", "r", "c")
	if err != nil || len(loaded) != 2 || loaded[1].PreviousHash != loaded[0].Hash {
		t.Fatalf("automatic chain failed: events=%+v err=%v", loaded, err)
	}
	first.StartedAt = now.Add(time.Hour).Format(time.RFC3339)
	if err := VerifyEvidence([]Evidence{first}, "r", "c", now); err == nil {
		t.Fatal("future clock accepted")
	}
}

func TestEvidenceMultiReleaseIsolation(t *testing.T) {
	root := candidateRoot(t)
	now := time.Now().UTC()
	baseDir := "evidence"
	dirA := EvidenceDirForRelease(baseDir, "release-a")
	dirB := EvidenceDirForRelease(baseDir, "release-b")
	// Build chain for release-A in directory "release-a".
	var evA []Evidence
	kinds := []struct {
		kind       string
		start, end time.Time
	}{
		{EvidenceRollbackRetention, now.Add(-8 * 24 * time.Hour), now.Add(-2 * time.Hour)},
		{EvidenceShadow, now.Add(-4 * 24 * time.Hour), now.Add(-time.Hour)},
		{EvidenceSoak, now.Add(-25 * time.Hour), now.Add(-30 * time.Minute)},
		{EvidenceExternal, now.Add(-20 * time.Minute), now.Add(-10 * time.Minute)},
		{EvidenceBackupRestore, now.Add(-9 * time.Minute), now.Add(-time.Minute)},
	}
	for _, item := range kinds {
		event, _, err := RecordEvidence(root, dirA, Evidence{ReleaseID: "release-a", ConfigSHA256: "cfg-a", Kind: item.kind, ProbeID: "p", StartedAt: item.start.Format(time.RFC3339), EndedAt: item.end.Format(time.RFC3339), Samples: 1, Passed: true})
		if err != nil {
			t.Fatal(err)
		}
		evA = append(evA, event)
	}
	// Build chain for release-B in directory "release-b".
	var evB []Evidence
	for _, item := range kinds {
		event, _, err := RecordEvidence(root, dirB, Evidence{ReleaseID: "release-b", ConfigSHA256: "cfg-b", Kind: item.kind, ProbeID: "p", StartedAt: item.start.Format(time.RFC3339), EndedAt: item.end.Format(time.RFC3339), Samples: 1, Passed: true})
		if err != nil {
			t.Fatal(err)
		}
		evB = append(evB, event)
	}
	// Load from each directory independently.
	loadedA, err := LoadEvidenceChain(root, dirA, "release-a", "cfg-a")
	if err != nil {
		t.Fatal(err)
	}
	loadedB, err := LoadEvidenceChain(root, dirB, "release-b", "cfg-b")
	if err != nil {
		t.Fatal(err)
	}
	// Each chain verifies with its own release ID.
	if err := VerifyEvidence(loadedA, "release-a", "cfg-a", now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEvidence(loadedB, "release-b", "cfg-b", now); err != nil {
		t.Fatal(err)
	}
	// Cross-verify: loading release-A chain against release-B ID must fail.
	if err := VerifyEvidence(loadedA, "release-b", "cfg-b", now); err == nil {
		t.Fatal("cross-release verification accepted")
	}
	// Loading from empty directory returns nil.
	emptyDir := EvidenceDirForRelease(baseDir, "empty-release")
	empty, err := LoadEvidenceChain(root, emptyDir, "empty-release", "cfg")
	if err != nil {
		t.Fatal(err)
	}
	if empty != nil {
		t.Fatal("empty directory returned evidence")
	}
}

func TestEvidenceConcurrentAppendIsOneChainAndConfigIsolated(t *testing.T) {
	root := candidateRoot(t)
	now := time.Now().UTC().Truncate(time.Second)
	const count = 20
	var wait sync.WaitGroup
	errors := make(chan error, count)
	for i := 0; i < count; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, _, err := RecordEvidence(root, "evidence", Evidence{
				ReleaseID:    "release",
				ConfigSHA256: "config-a",
				Kind:         EvidenceExternal,
				ProbeID:      fmt.Sprintf("probe-%02d", index),
				StartedAt:    now.Add(-time.Minute).Format(time.RFC3339),
				EndedAt:      now.Format(time.RFC3339),
				Samples:      1,
				Passed:       true,
			})
			errors <- err
		}(i)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := LoadEvidenceChain(root, "evidence", "release", "config-a")
	if err != nil || len(events) != count {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	for i := 1; i < len(events); i++ {
		if events[i].PreviousHash != events[i-1].Hash {
			t.Fatalf("fork at %d", i)
		}
	}
	if _, _, err := RecordEvidence(root, "evidence", Evidence{ReleaseID: "release", ConfigSHA256: "config-b", Kind: EvidenceExternal, ProbeID: "other", StartedAt: now.Add(-time.Minute).Format(time.RFC3339), EndedAt: now.Format(time.RFC3339), Samples: 1, Passed: true}); err != nil {
		t.Fatal(err)
	}
	other, err := LoadEvidenceChain(root, "evidence", "release", "config-b")
	if err != nil || len(other) != 1 {
		t.Fatalf("config-isolated events=%d err=%v", len(other), err)
	}
	if _, err := LoadEvidence(root, "evidence"); err == nil {
		t.Fatal("ambiguous multi-config load accepted")
	}
}
