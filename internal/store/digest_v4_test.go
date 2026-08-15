package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Ricaardo/nimbus-os/news/internal/digest"
	"github.com/Ricaardo/nimbus-os/news/internal/model"
	bolt "go.etcd.io/bbolt"
)

func TestRoutedAdmissionQuotaIdempotencyCriticalAndRestart(t *testing.T) {
	owner := newDigestTestOwner(t, "route-v4.db")
	now := time.Date(2026, 8, 1, 15, 59, 0, 0, time.UTC)
	ds, err := NewDigestStoreWithPriorities(owner.DB(), map[string]int{"source": 12})
	if err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return now }
	if err := ds.Enqueue(context.Background(), "source", digest.Closing, &model.Message{ID: "legacy-priority"}); err != nil {
		t.Fatal(err)
	}
	legacy, err := ds.ListItems(context.Background(), digest.ItemFilter{Briefing: digest.Closing, State: digest.Pending})
	if err != nil || len(legacy) != 1 || legacy[0].Priority != 12 {
		t.Fatalf("legacy priority items=%+v err=%v", legacy, err)
	}
	request := digest.RouteRequest{Source: "source", MessageID: "one", Lane: digest.RouteDigest, Briefing: digest.USPreview, MaxPerDay: 1, Priority: 12}
	msg := &model.Message{ID: "one", Title: "Topic", Link: "https://EXAMPLE.com/story?a=tracking"}
	first, err := ds.EnqueueRoutedDigest(context.Background(), request, msg)
	if err != nil || !first.Admitted || first.Existing || first.Used != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	again, err := ds.EnqueueRoutedDigest(context.Background(), request, msg)
	if err != nil || !again.Admitted || !again.Existing || again.Used != 1 {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	request.MessageID = "two"
	full, err := ds.EnqueueRoutedDigest(context.Background(), request, &model.Message{ID: "two"})
	if err != nil || full.Admitted || full.Used != 1 {
		t.Fatalf("full=%+v err=%v", full, err)
	}
	request.Critical = true
	critical, err := ds.EnqueueRoutedDigest(context.Background(), request, &model.Message{ID: "two"})
	if err != nil || !critical.Admitted || critical.Used != 1 {
		t.Fatalf("critical=%+v err=%v", critical, err)
	}
	restarted, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return now }
	request.Lane = digest.RouteDirect
	request.MaxPerDay = 9
	existing, err := restarted.AdmitDirect(context.Background(), request)
	if err != nil || !existing.Existing || existing.Lane != digest.RouteDigest || existing.Used != 1 || existing.Limit != 1 {
		t.Fatalf("existing=%+v err=%v", existing, err)
	}
	now = now.Add(time.Minute)
	request.Critical = false
	nextDay, err := restarted.AdmitDirect(context.Background(), request)
	if err != nil || !nextDay.Admitted || nextDay.Existing || nextDay.Used != 1 {
		t.Fatalf("Shanghai day rollover=%+v err=%v", nextDay, err)
	}
}

func TestCriticalAdmissionsDoNotConsumeOrdinaryQuota(t *testing.T) {
	owner := newDigestTestOwner(t, "route-critical-reserve-v4.db")
	ds := mustDigestStore(t, owner.DB(), time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	for _, id := range []string{"critical-one", "critical-two"} {
		admission, err := ds.AdmitDirect(context.Background(), digest.RouteRequest{
			Source: "source", MessageID: id, Lane: digest.RouteDirect, MaxPerDay: 1, Critical: true,
		})
		if err != nil || !admission.Admitted || admission.Used != 0 {
			t.Fatalf("critical %s admission=%+v err=%v", id, admission, err)
		}
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte(digestRouteAdmissionsBucket)).Get(directAdmissionKey("source", "critical-one"))
		var record routeAdmissionRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if !record.Critical || record.MessageID != "critical-one" || record.Lane != digest.RouteDirect {
			t.Fatalf("critical audit record=%+v", record)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ordinary, err := ds.AdmitDirect(context.Background(), digest.RouteRequest{
		Source: "source", MessageID: "ordinary", Lane: digest.RouteDirect, MaxPerDay: 1,
	})
	if err != nil || !ordinary.Admitted || ordinary.Used != 1 {
		t.Fatalf("ordinary admission=%+v err=%v", ordinary, err)
	}
	full, err := ds.AdmitDirect(context.Background(), digest.RouteRequest{
		Source: "source", MessageID: "ordinary-two", Lane: digest.RouteDirect, MaxPerDay: 1,
	})
	if err != nil || full.Admitted || full.Used != 1 {
		t.Fatalf("full admission=%+v err=%v", full, err)
	}
}

func TestDirectAdmissionCompletionIsDurableAndRetryDoesNotRecharge(t *testing.T) {
	owner := newDigestTestOwner(t, "route-direct-completion-v4.db")
	now := time.Date(2026, 8, 1, 15, 59, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	request := digest.RouteRequest{Source: "source", MessageID: "retry", Lane: digest.RouteDirect, MaxPerDay: 1, Channels: []string{"main", "mirror"}, Payload: &model.Message{ID: "retry", Metadata: map[string]interface{}{"ai_eval_status": "success"}}}
	first, err := ds.AdmitDirect(context.Background(), request)
	if err != nil || !first.Admitted || first.Existing || first.Completed || first.Used != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	pending, err := ds.LookupDirect(context.Background(), request)
	if err != nil || !pending.Existing || pending.Completed || len(pending.Channels) != 2 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	again, err := ds.AdmitDirect(context.Background(), request)
	if err != nil || !again.Existing || again.Used != 1 {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	claim, err := ds.ClaimDirect(context.Background(), "source", "retry", time.Minute)
	if err != nil || claim == nil || len(claim.Channels) != 2 || claim.Message.GetStringMetadata("ai_eval_status") != "success" {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	for _, channel := range claim.Channels {
		if err := ds.CompleteDirectChannel(context.Background(), claim.Source, claim.MessageID, claim.Token, channel); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(2 * time.Minute) // crosses Shanghai midnight
	restarted := mustDigestStore(t, owner.DB(), now)
	completed, err := restarted.LookupDirect(context.Background(), request)
	if err != nil || !completed.Completed || len(completed.Channels) != 0 {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	readmitted, err := restarted.AdmitDirect(context.Background(), request)
	if err != nil || !readmitted.Existing || !readmitted.Completed || len(readmitted.Channels) != 0 || readmitted.Used != 1 {
		t.Fatalf("cross-midnight completed admission=%+v err=%v", readmitted, err)
	}
}

func TestDirectClaimFencingPartialRecoveryAndMidnightIdentity(t *testing.T) {
	owner := newDigestTestOwner(t, "route-direct-recovery-v4.db")
	now := time.Date(2026, 8, 1, 15, 59, 0, 0, time.UTC)
	ds, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return now }
	request := digest.RouteRequest{Source: "rss", MessageID: "stable", Lane: digest.RouteDirect, MaxPerDay: 2,
		Channels: []string{"good", "retry"}, Payload: &model.Message{ID: "stable", Content: "payload", Metadata: map[string]interface{}{"ai_score": 9.0}}}
	if admission, err := ds.AdmitDirect(context.Background(), request); err != nil || admission.Used != 1 {
		t.Fatalf("admit=%+v err=%v", admission, err)
	}
	claims := make(chan *digest.DirectClaim, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, claimErr := ds.ClaimDirect(context.Background(), "rss", "stable", time.Minute)
			if claimErr != nil {
				t.Errorf("claim: %v", claimErr)
			}
			claims <- claim
		}()
	}
	wg.Wait()
	close(claims)
	var winner *digest.DirectClaim
	for claim := range claims {
		if claim != nil {
			if winner != nil {
				t.Fatal("more than one concurrent claimant")
			}
			winner = claim
		}
	}
	if winner == nil || winner.Message.Content != "payload" || len(winner.Channels) != 2 {
		t.Fatalf("winner=%+v", winner)
	}
	now = now.Add(2 * time.Minute) // crosses Shanghai midnight
	second, err := ds.ClaimDirect(context.Background(), "rss", "stable", time.Minute)
	if err != nil || second == nil || second.Token == winner.Token {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if err := ds.CompleteDirectChannel(context.Background(), "rss", "stable", winner.Token, "good"); err == nil {
		t.Fatal("stale claimant completed a channel")
	}
	if err := ds.CompleteDirectChannel(context.Background(), "rss", "stable", second.Token, "good"); err != nil {
		t.Fatal(err)
	}
	if err := ds.ReleaseDirect(context.Background(), "rss", "stable", second.Token); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return now }
	pending, err := restarted.ClaimPendingDirect(context.Background(), 10, time.Minute)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	_, hasScore := pending[0].Message.GetMetadata("ai_score")
	if len(pending[0].Channels) != 1 || pending[0].Channels[0] != "retry" || !hasScore {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if existing, err := restarted.AdmitDirect(context.Background(), request); err != nil || !existing.Existing || existing.Used != 1 {
		t.Fatalf("midnight existing=%+v err=%v", existing, err)
	}
}

func TestDirectChannelBackoffAndPermanentFailureAudit(t *testing.T) {
	owner := newDigestTestOwner(t, "route-direct-backoff-v4.db")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ds, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return now }
	for _, tc := range []struct{ id, channel string }{{"retry", "flaky"}, {"removed", "gone"}} {
		request := digest.RouteRequest{Source: "rss", MessageID: tc.id, Lane: digest.RouteDirect, Channels: []string{tc.channel}, Payload: &model.Message{ID: tc.id}}
		if _, err := ds.AdmitDirect(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		claim, err := ds.ClaimDirect(context.Background(), "rss", tc.id, time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("claim %s=%+v err=%v", tc.id, claim, err)
		}
		permanent := tc.id == "removed"
		if err := ds.FailDirectChannel(context.Background(), "rss", tc.id, claim.Token, tc.channel, "test failure", permanent); err != nil {
			t.Fatal(err)
		}
		if err := ds.ReleaseDirect(context.Background(), "rss", tc.id, claim.Token); err != nil {
			t.Fatal(err)
		}
	}
	if claims, err := ds.ClaimPendingDirect(context.Background(), 10, time.Minute); err != nil || len(claims) != 0 {
		t.Fatalf("deferred claim=%+v err=%v", claims, err)
	}
	now = now.Add(31 * time.Second)
	if claims, err := ds.ClaimPendingDirect(context.Background(), 10, time.Minute); err != nil || len(claims) != 1 || claims[0].MessageID != "retry" {
		t.Fatalf("due claim=%+v err=%v", claims, err)
	}
	state, err := ds.LookupDirect(context.Background(), digest.RouteRequest{Source: "rss", MessageID: "removed"})
	if err != nil || !state.Completed {
		t.Fatalf("removed state=%+v err=%v", state, err)
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte(digestRouteAdmissionsBucket)).Get(directAdmissionKey("rss", "removed"))
		var record routeAdmissionRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if !record.Failures["gone"].Terminal || record.Failures["gone"].LastError != "test failure" {
			t.Fatalf("failure audit=%+v", record.Failures)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDirectChannelTenthTransientFailureIsTerminalAcrossRestart(t *testing.T) {
	owner := newDigestTestOwner(t, "route-direct-max-attempt-v4.db")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ds, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return now }
	request := digest.RouteRequest{Source: "rss", MessageID: "ten", Lane: digest.RouteDirect,
		Channels: []string{"flaky"}, Payload: &model.Message{ID: "ten"}}
	if _, err := ds.AdmitDirect(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 9; attempt++ {
		claim, err := ds.ClaimDirect(context.Background(), "rss", "ten", time.Minute)
		if err != nil || claim == nil {
			t.Fatalf("attempt %d claim=%+v err=%v", attempt, claim, err)
		}
		if err := ds.FailDirectChannel(context.Background(), "rss", "ten", claim.Token, "flaky", "transient", false); err != nil {
			t.Fatal(err)
		}
		if err := ds.ReleaseDirect(context.Background(), "rss", "ten", claim.Token); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * time.Hour)
	}
	restarted, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return now }
	claim, err := restarted.ClaimDirect(context.Background(), "rss", "ten", time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("tenth claim=%+v err=%v", claim, err)
	}
	if err := restarted.FailDirectChannel(context.Background(), "rss", "ten", claim.Token, "flaky", "last transient", false); err != nil {
		t.Fatal(err)
	}
	state, err := restarted.LookupDirect(context.Background(), request)
	if err != nil || !state.Completed {
		t.Fatalf("terminal state=%+v err=%v", state, err)
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte(digestRouteAdmissionsBucket)).Get(directAdmissionKey("rss", "ten"))
		var record routeAdmissionRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		failure := record.Failures["flaky"]
		if failure.Attempts != 10 || !failure.Terminal || failure.LastError != "last transient" {
			t.Fatalf("failure audit=%+v", failure)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeRouteAdmissionsRetainsIncompleteLegacyDirect(t *testing.T) {
	owner := newDigestTestOwner(t, "route-purge-legacy-v4.db")
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	ds, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return now }
	oldDate := "2026-08-01"
	keys := map[string][]byte{
		"incomplete": routeAdmissionKey(oldDate, "rss", "legacy-incomplete"),
		"complete":   routeAdmissionKey(oldDate, "rss", "legacy-complete"),
		"digest":     routeAdmissionKey(oldDate, "rss", "old-digest"),
		"corrupt":    routeAdmissionKey(oldDate, "rss", "corrupt"),
	}
	if err := owner.DB().Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(digestRouteAdmissionsBucket))
		records := map[string]routeAdmissionRecord{
			"incomplete": {Date: oldDate, Source: "rss", MessageID: "legacy-incomplete", Lane: digest.RouteDirect, CreatedAt: now.Add(-9 * 24 * time.Hour), Channels: []string{"main"}, Message: &model.Message{ID: "legacy-incomplete"}},
			"complete":   {Date: oldDate, Source: "rss", MessageID: "legacy-complete", Lane: digest.RouteDirect, CreatedAt: now.Add(-9 * 24 * time.Hour), CompletedAt: now.Add(-8 * 24 * time.Hour)},
			"digest":     {Date: oldDate, Source: "rss", MessageID: "old-digest", Lane: digest.RouteDigest, CreatedAt: now.Add(-9 * 24 * time.Hour)},
		}
		for name, record := range records {
			if err := putJSON(bucket, keys[name], record); err != nil {
				return err
			}
		}
		return bucket.Put(keys["corrupt"], []byte("{"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := ds.PurgeRouteAdmissions(context.Background(), 3*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(digestRouteAdmissionsBucket))
		if bucket.Get(keys["incomplete"]) == nil {
			t.Fatal("purge removed an incomplete legacy direct admission")
		}
		if bucket.Get(keys["complete"]) != nil || bucket.Get(keys["digest"]) != nil || bucket.Get(keys["corrupt"]) != nil {
			t.Fatal("purge retained completed legacy history past retention")
		}
		quarantine := tx.Bucket([]byte(digestRouteQuarantineBucket))
		if quarantine.Stats().KeyN != 1 {
			t.Fatalf("quarantine count=%d", quarantine.Stats().KeyN)
		}
		return quarantine.ForEach(func(_, raw []byte) error {
			var audit routeAdmissionQuarantineRecord
			if err := json.Unmarshal(raw, &audit); err != nil {
				return err
			}
			if !bytes.Equal(audit.OriginalKey, keys["corrupt"]) || !bytes.Equal(audit.Raw, []byte("{")) || audit.DecodeError == "" {
				t.Fatalf("quarantine audit=%+v", audit)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRouteAdmissionQuarantineIsBoundedAndNotRecovered(t *testing.T) {
	owner := newDigestTestOwner(t, "route-quarantine-bounds-v4.db")
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	if err := owner.DB().Update(func(tx *bolt.Tx) error {
		admissions := tx.Bucket([]byte(digestRouteAdmissionsBucket))
		for i := 0; i < digestQuarantineMaxRecords+5; i++ {
			key := routeAdmissionKey("2026-08-01", "rss", fmt.Sprintf("corrupt-%03d", i))
			if err := admissions.Put(key, []byte("{")); err != nil {
				return err
			}
		}
		quarantine := tx.Bucket([]byte(digestRouteQuarantineBucket))
		oldAt := now.Add(-digestQuarantineRetention - time.Hour)
		return putJSON(quarantine, routeQuarantineKey(oldAt, []byte("old"), []byte("bad")), routeAdmissionQuarantineRecord{
			QuarantinedAt: oldAt, OriginalKey: []byte("old"), Raw: []byte("bad"), DecodeError: "old",
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := ds.PurgeRouteAdmissions(context.Background(), 3*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		if got := tx.Bucket([]byte(digestRouteAdmissionsBucket)).Stats().KeyN; got != 0 {
			t.Fatalf("active corrupt admissions=%d", got)
		}
		if got := tx.Bucket([]byte(digestRouteQuarantineBucket)).Stats().KeyN; got != digestQuarantineMaxRecords {
			t.Fatalf("bounded quarantine=%d want=%d", got, digestQuarantineMaxRecords)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	claims, err := ds.ClaimPendingDirect(context.Background(), 10, time.Minute)
	if err != nil || len(claims) != 0 {
		t.Fatalf("quarantined admissions were rescanned: claims=%+v err=%v", claims, err)
	}
}

func TestRoutedAdmissionCrossLaneSameDayAndNextDayContract(t *testing.T) {
	owner := newDigestTestOwner(t, "route-cross-lane-v4.db")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	ds, err := NewDigestStore(owner.DB())
	if err != nil {
		t.Fatal(err)
	}
	ds.now = func() time.Time { return now }
	direct := digest.RouteRequest{Source: "rss", MessageID: "direct-pending", Lane: digest.RouteDirect, MaxPerDay: 10,
		Channels: []string{"main"}, Payload: &model.Message{ID: "direct-pending"}}
	if got, err := ds.AdmitDirect(context.Background(), direct); err != nil || !got.Admitted || got.Existing {
		t.Fatalf("direct first=%+v err=%v", got, err)
	}
	digestRequest := digest.RouteRequest{Source: "rss", MessageID: "direct-pending", Lane: digest.RouteDigest, Briefing: digest.USPreview, MaxPerDay: 10}
	if got, err := ds.EnqueueRoutedDigest(context.Background(), digestRequest, &model.Message{ID: "direct-pending"}); err != nil || !got.Existing || got.Lane != digest.RouteDirect {
		t.Fatalf("same-day direct->digest=%+v err=%v", got, err)
	}
	direct.MessageID = "direct-completed"
	direct.Payload = &model.Message{ID: "direct-completed"}
	if got, err := ds.AdmitDirect(context.Background(), direct); err != nil || !got.Admitted || got.Existing {
		t.Fatalf("completed direct admission=%+v err=%v", got, err)
	}
	claim, err := ds.ClaimDirect(context.Background(), "rss", "direct-completed", time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("completed direct claim=%+v err=%v", claim, err)
	}
	if err := ds.CompleteDirectChannel(context.Background(), claim.Source, claim.MessageID, claim.Token, "main"); err != nil {
		t.Fatal(err)
	}
	direct.MessageID = "direct-legacy"
	direct.Payload = &model.Message{ID: "direct-legacy"}
	if got, err := ds.AdmitDirect(context.Background(), direct); err != nil || !got.Admitted || got.Existing {
		t.Fatalf("legacy direct admission=%+v err=%v", got, err)
	}
	if err := owner.DB().Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(digestRouteAdmissionsBucket))
		stableKey := directAdmissionKey("rss", "direct-legacy")
		raw := append([]byte(nil), bucket.Get(stableKey)...)
		if len(raw) == 0 {
			return fmt.Errorf("stable direct record missing")
		}
		if err := bucket.Put(routeAdmissionKey("2026-08-01", "rss", "direct-legacy"), raw); err != nil {
			return err
		}
		return bucket.Delete(stableKey)
	}); err != nil {
		t.Fatal(err)
	}
	digestRequest.MessageID = "digest-first"
	if got, err := ds.EnqueueRoutedDigest(context.Background(), digestRequest, &model.Message{ID: "digest-first"}); err != nil || !got.Admitted || got.Existing {
		t.Fatalf("digest first=%+v err=%v", got, err)
	}
	direct.MessageID = "digest-first"
	direct.Payload = &model.Message{ID: "digest-first"}
	if got, err := ds.AdmitDirect(context.Background(), direct); err != nil || !got.Existing || got.Lane != digest.RouteDigest {
		t.Fatalf("same-day digest->direct=%+v err=%v", got, err)
	}
	now = now.Add(24 * time.Hour)
	assertFence := func(id string, completed bool) {
		t.Helper()
		direct.MessageID = id
		direct.Payload = &model.Message{ID: id}
		gotDirect, err := ds.AdmitDirect(context.Background(), direct)
		if err != nil || !gotDirect.Existing || gotDirect.Lane != digest.RouteDirect || gotDirect.Limit != 10 || gotDirect.Completed != completed {
			t.Fatalf("next-day direct fence %s=%+v err=%v", id, gotDirect, err)
		}
		digestRequest.MessageID = id
		gotDigest, err := ds.EnqueueRoutedDigest(context.Background(), digestRequest, &model.Message{ID: id})
		if err != nil || !gotDigest.Existing || gotDigest.Lane != digest.RouteDirect || gotDigest.Limit != 10 || gotDigest.Completed != completed {
			t.Fatalf("next-day digest fence %s=%+v err=%v", id, gotDigest, err)
		}
	}
	assertFence("direct-pending", false)
	assertFence("direct-completed", true)
	assertFence("direct-legacy", false)
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		date := now.In(digestShanghai).Format("2006-01-02")
		quotas := tx.Bucket([]byte(digestRouteQuotaBucket))
		if directUsed := readRouteQuota(quotas.Get(routeQuotaKey(date, "rss", digest.RouteDirect))); directUsed != 0 {
			t.Fatalf("next-day direct quota charged by fence: %d", directUsed)
		}
		if digestUsed := readRouteQuota(quotas.Get(routeQuotaKey(date, "rss", digest.RouteDigest))); digestUsed != 0 {
			t.Fatalf("next-day digest quota charged by fence: %d", digestUsed)
		}
		if items := tx.Bucket([]byte(digestBucketV1)).Stats().KeyN; items != 1 {
			t.Fatalf("fenced digest enqueue changed item count: %d", items)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	direct.MessageID = "digest-first"
	direct.Payload = &model.Message{ID: "digest-first"}
	if got, err := ds.AdmitDirect(context.Background(), direct); err != nil || !got.Admitted || got.Existing || got.Lane != digest.RouteDirect {
		t.Fatalf("next-day digest->direct=%+v err=%v", got, err)
	}
}

func TestRoutedAdmissionConcurrentQuotaIsAtomic(t *testing.T) {
	owner := newDigestTestOwner(t, "route-concurrent-v4.db")
	ds := mustDigestStore(t, owner.DB(), time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	var wg sync.WaitGroup
	results := make(chan digest.RouteAdmission, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			request := digest.RouteRequest{Source: "source", MessageID: string(rune('a' + i)), Lane: digest.RouteDirect, MaxPerDay: 5}
			admission, err := ds.AdmitDirect(context.Background(), request)
			if err != nil {
				t.Errorf("admit: %v", err)
				return
			}
			results <- admission
		}(i)
	}
	wg.Wait()
	close(results)
	admitted := 0
	for result := range results {
		if result.Admitted {
			admitted++
		}
	}
	if admitted != 5 {
		t.Fatalf("admitted=%d want=5", admitted)
	}
}

func TestLeaseTopicsDoesNotSplitAndPrepareCoversEveryMember(t *testing.T) {
	owner := newDigestTestOwner(t, "topics-v4.db")
	now := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	for _, input := range []struct {
		source, id, title, link string
		priority                int
	}{
		{"a", "a", "Markets rally", "https://first.example/story?one=1", 10},
		{"b", "b", " markets: RALLY! ", "https://second.example/other?two=2", 10},
		{"c", "c", "Rates fall", "https://example.com/other", 0},
	} {
		if err := ds.EnqueueWithOptions(context.Background(), input.source, digest.USPreview,
			&model.Message{ID: input.id, Title: input.title, Link: input.link}, input.priority, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := ds.LeaseTopics(context.Background(), digest.USPreview, 1, time.Hour)
	if err != nil || lease == nil || len(lease.Topics) != 1 || len(lease.Items) != 2 {
		t.Fatalf("lease=%+v err=%v", lease, err)
	}
	delivery, err := ds.PrepareDelivery(context.Background(), lease.ID, &model.Message{ID: "rendered"}, []string{"sink"}, now.Add(time.Hour))
	if err != nil || len(delivery.ItemIDs) != len(lease.Items) {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
	if err := ds.EnqueueWithOptions(context.Background(), "d", digest.USPreview,
		&model.Message{ID: "d", Title: "Markets rally", Link: "https://third.example/new-story?late=1"}, 100, time.Hour); err != nil {
		t.Fatal(err)
	}
	next, err := ds.LeaseTopics(context.Background(), digest.USPreview, 1, time.Hour)
	if err != nil || next == nil || len(next.Items) != 1 || next.Items[0].Source != "c" {
		t.Fatalf("active topic was split or unrelated topic omitted: next=%+v err=%v", next, err)
	}
}

func TestLeaseTopicsTitleSeparationURLFallbackAndDurableIdentity(t *testing.T) {
	owner := newDigestTestOwner(t, "topic-alias-v4.db")
	now := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	inputs := []struct {
		source, id, title, content, link string
	}{
		{"a", "tracking-one", "", "first", "https://example.com/story?utm_source=one"},
		{"b", "tracking-two", "", "second", "https://example.com/story?utm_source=two#fragment"},
		{"c", "apple", "Apple raises guidance", "", "https://example.com/shared"},
		{"d", "tesla", "Tesla raises guidance", "", "https://example.com/shared"},
	}
	for _, input := range inputs {
		if err := ds.EnqueueWithOptions(context.Background(), input.source, digest.USPreview,
			&model.Message{ID: input.id, Title: input.title, Content: input.content, Link: input.link}, 10, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	before := readRawDigestItems(t, owner.DB())
	lease, err := ds.LeaseTopics(context.Background(), digest.USPreview, 12, time.Hour)
	if err != nil || lease == nil || len(lease.Topics) != 3 || len(lease.Items) != 4 {
		t.Fatalf("lease=%+v err=%v", lease, err)
	}
	sizes := make([]int, 0, len(lease.Topics))
	for _, topic := range lease.Topics {
		sizes = append(sizes, len(topic.Items))
	}
	sort.Ints(sizes)
	if got := fmt.Sprint(sizes); got != "[1 1 2]" {
		t.Fatalf("topic sizes=%s want [1 1 2]", got)
	}
	after := readRawDigestItems(t, owner.DB())
	for id, original := range before {
		leased := after[id]
		if leased.ID != original.ID || leased.TopicKey != original.TopicKey || leased.SchemaVersion != original.SchemaVersion {
			t.Fatalf("durable identity changed for %s: before=%+v after=%+v", id, original, leased)
		}
	}
}

func TestLeaseTopicsDoesNotPersistReadTimeLegacyUpgrades(t *testing.T) {
	owner := newDigestTestOwner(t, "topic-lease-no-upgrade-v4.db")
	now := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	ds := mustDigestStore(t, owner.DB(), now)
	items := []digest.Item{
		{ID: "selected", SchemaVersion: 3, Source: "legacy", Briefing: digest.USPreview, Message: &model.Message{Title: "Selected"}, State: digest.Pending, Priority: 100, CreatedAt: now},
		{ID: "active", SchemaVersion: 3, Source: "legacy", Briefing: digest.USPreview, Message: &model.Message{Title: "Active"}, State: digest.Leased, LeaseID: "existing", LeaseUntil: now.Add(time.Hour), CreatedAt: now},
		{ID: "terminal", SchemaVersion: 3, Source: "legacy", Briefing: digest.USPreview, Message: &model.Message{Title: "Terminal"}, State: digest.Acked, CreatedAt: now},
		{ID: "other", SchemaVersion: 3, Source: "legacy", Briefing: digest.Closing, Message: &model.Message{Title: "Other"}, State: digest.Pending, CreatedAt: now},
	}
	for _, item := range items {
		putDigestTestJSON(t, owner.DB(), digestBucketV1, item.ID, item)
	}
	before := make(map[string][]byte)
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(digestBucketV1))
		for _, item := range items {
			before[item.ID] = append([]byte(nil), bucket.Get([]byte(item.ID))...)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := ds.LeaseTopics(context.Background(), digest.USPreview, 1, time.Hour)
	if err != nil || lease == nil || len(lease.Items) != 1 || lease.Items[0].ID != "selected" {
		t.Fatalf("lease=%+v err=%v", lease, err)
	}
	if err := owner.DB().View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(digestBucketV1))
		for _, id := range []string{"active", "terminal", "other"} {
			if !bytes.Equal(before[id], bucket.Get([]byte(id))) {
				t.Fatalf("read-only legacy item %s was rewritten", id)
			}
		}
		var selected digest.Item
		if err := json.Unmarshal(bucket.Get([]byte("selected")), &selected); err != nil {
			return err
		}
		if selected.State != digest.Leased || selected.SchemaVersion != 3 || selected.TopicKey != "" || selected.ID != "selected" {
			t.Fatalf("selected legacy identity changed: %+v", selected)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDigestTopicMigrationConservesPendingOnlyAndIsIdempotent(t *testing.T) {
	owner := newDigestTestOwner(t, "migration-v4.db")
	mustDigestStore(t, owner.DB(), time.Now())
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	pendingMessage := &model.Message{Title: "Title"}
	pendingMessage.SetMetadata("ai_score", 9)
	pending := digest.Item{ID: "pending", SchemaVersion: 3, Source: "known", Briefing: digest.USPreview, Message: pendingMessage, State: digest.Pending, CreatedAt: now}
	acked := digest.Item{ID: "acked", SchemaVersion: 3, Source: "known", Briefing: digest.USPreview, State: digest.Acked, CreatedAt: now}
	putDigestTestJSON(t, owner.DB(), digestBucketV1, pending.ID, pending)
	putDigestTestJSON(t, owner.DB(), digestBucketV1, acked.ID, acked)
	preview, err := PreviewDigestTopics(context.Background(), owner.DB(), map[string]int{"known": 10})
	if err != nil {
		t.Fatal(err)
	}
	report, err := ApplyDigestTopics(context.Background(), owner.DB(), map[string]int{"known": 10}, preview.BeforeHash)
	if err != nil || report.Updated != 1 || report.BeforeHash != report.AfterHash || report.BeforeCount != 2 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	items := readRawDigestItems(t, owner.DB())
	if items["pending"].SchemaVersion != 4 || items["pending"].Priority != 40 || items["pending"].TopicKey == "" {
		t.Fatalf("pending=%+v", items["pending"])
	}
	if items["acked"].SchemaVersion != 3 || items["acked"].TopicKey != "" {
		t.Fatalf("acked changed=%+v", items["acked"])
	}
	again, err := ApplyDigestTopics(context.Background(), owner.DB(), nil, preview.BeforeHash)
	if err != nil || !again.AlreadyMarked || again.Updated != 0 {
		t.Fatalf("again=%+v err=%v", again, err)
	}
}

func TestDigestTopicMigrationPreviewAndFencedApply(t *testing.T) {
	owner := newDigestTestOwner(t, "migration-preview-v4.db")
	mustDigestStore(t, owner.DB(), time.Now())
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	item := digest.Item{ID: "pending", SchemaVersion: 3, Source: "known", Briefing: digest.USPreview,
		Message: &model.Message{Title: "Title"}, State: digest.Pending, CreatedAt: now}
	putDigestTestJSON(t, owner.DB(), digestBucketV1, item.ID, item)

	preview, err := PreviewDigestTopics(context.Background(), owner.DB(), map[string]int{"known": 25})
	if err != nil || preview.Mode != "dry-run" || preview.Applied || preview.MarkerPresent ||
		preview.Updated != 1 || preview.BeforeHash == "" || preview.BeforeHash != preview.AfterHash {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if got := readRawDigestItems(t, owner.DB())[item.ID]; got.SchemaVersion != 3 || got.TopicKey != "" {
		t.Fatalf("preview mutated item: %+v", got)
	}
	if _, err := ApplyDigestTopics(context.Background(), owner.DB(), map[string]int{"known": 25}, "wrong"); err == nil {
		t.Fatal("expected hash mismatch")
	}
	if got := readRawDigestItems(t, owner.DB())[item.ID]; got.SchemaVersion != 3 || got.TopicKey != "" {
		t.Fatalf("failed apply mutated item: %+v", got)
	}
	applied, err := ApplyDigestTopics(context.Background(), owner.DB(), map[string]int{"known": 25}, preview.BeforeHash)
	if err != nil || applied.Mode != "apply" || !applied.Applied || !applied.MarkerPresent || applied.Updated != 1 {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
	again, err := ApplyDigestTopics(context.Background(), owner.DB(), map[string]int{"known": 25}, preview.BeforeHash)
	if err != nil || !again.AlreadyMarked || again.Applied || !again.MarkerPresent || again.Updated != 0 {
		t.Fatalf("again=%+v err=%v", again, err)
	}
}

func TestDigestTopicMigrationCorruptionRollsBack(t *testing.T) {
	owner := newDigestTestOwner(t, "migration-corrupt-v4.db")
	mustDigestStore(t, owner.DB(), time.Now())
	item := digest.Item{ID: "good", SchemaVersion: 3, Source: "known", Briefing: digest.USPreview, Message: &model.Message{Title: "Title"}, State: digest.Pending, CreatedAt: time.Now()}
	putDigestTestJSON(t, owner.DB(), digestBucketV1, item.ID, item)
	if err := owner.DB().Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(digestBucketV1)).Put([]byte("bad"), []byte("{"))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewDigestTopics(context.Background(), owner.DB(), map[string]int{"known": 40}); err == nil {
		t.Fatal("expected corruption error")
	}
	items := readRawDigestItems(t, owner.DB())
	if items["good"].SchemaVersion != 3 || items["good"].TopicKey != "" {
		t.Fatalf("good item changed after rollback: %+v", items["good"])
	}
}

func readRawDigestItems(t *testing.T, db *bolt.DB) map[string]digest.Item {
	t.Helper()
	out := make(map[string]digest.Item)
	if err := db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(digestBucketV1)).ForEach(func(key, raw []byte) error {
			var item digest.Item
			if json.Unmarshal(raw, &item) == nil {
				out[string(key)] = item
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
