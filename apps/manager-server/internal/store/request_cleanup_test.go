package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func cleanupTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.db.Exec(`update usage_data_migrations set status='completed'`); err != nil {
		t.Fatal(err)
	}
	return st
}

func cleanupTestEvent(label string, timestampMS int64) usage.Event {
	return usage.Event{EventHash: testCanonicalHash(label), TimestampMS: timestampMS,
		Timestamp: time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:     label, InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CreatedAtMS: timestampMS}
}

func cleanupCatchUpAll(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		nowMS := time.Now().UnixMilli()
		if _, err := st.CatchUpAccountHistoryRollups(ctx, 1000, nowMS); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CatchUpDashboardHourlyRollups(ctx, 1000, nowMS); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CatchUpUsageHourlyAggregate(ctx, 1000, nowMS); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CatchUpUsageMonitoringProjection(ctx, 1000, nowMS); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CatchUpUsageMonitoringMetadata(ctx, 1000, nowMS); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CatchUpUsageMonitoringStats(ctx, 1000, nowMS); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CatchUpCodexLegacyIdentityEvidence(ctx, 1000, nowMS); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CatchUpUsagePricing(ctx, 1000, nowMS); err != nil {
			t.Fatal(err)
		}
		var target int64
		if err := st.db.QueryRow(`select coalesce(max(id),0) from usage_events`).Scan(&target); err != nil {
			t.Fatal(err)
		}
		ready, err := st.RequestCleanupRebuildReady(ctx, target, true)
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			return
		}
	}
	t.Fatal("derived rollups did not reach ready state")
}

func TestRequestCleanupPreservesRecentAndLateOldEventsAndRebuilds(t *testing.T) {
	st := cleanupTestStore(t)
	ctx := context.Background()
	base := int64(1_778_000_000_000)
	// 死信无法按请求时间可靠归属，不应顺带删除。
	if _, err := st.db.Exec(`insert into dead_letter_events(payload,error,created_at_ms) values('{}','fixture',?)`, base); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertEvents(ctx, []usage.Event{
		cleanupTestEvent("oldtoken", base), cleanupTestEvent("recenttoken", base+20_000),
	}); err != nil {
		t.Fatal(err)
	}
	cleanupCatchUpAll(t, st)
	preview, err := st.PreviewRequestCleanup(ctx, base+10_000)
	if err != nil {
		t.Fatal(err)
	}
	if preview.EventCount != 1 {
		t.Fatalf("preview event_count=%d want 1", preview.EventCount)
	}
	if _, err := st.InsertEvents(ctx, []usage.Event{cleanupTestEvent("lateoldtoken", base+1_000)}); err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateRequestCleanup(ctx, "0123456789abcdef0123456789abcdef", preview)
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, done, err := st.DeleteRequestCleanupBatch(ctx, run.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
	}
	run, err = st.FinalizeRequestCleanup(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.DeletedCount != 1 {
		t.Fatalf("deleted_count=%d want 1", run.DeletedCount)
	}
	var deadLetterCount int
	if err := st.db.QueryRow(`select count(*) from dead_letter_events`).Scan(&deadLetterCount); err != nil {
		t.Fatal(err)
	}
	if deadLetterCount != 1 {
		t.Fatalf("unrelated dead letters changed: count=%d", deadLetterCount)
	}
	var oldCount, recentCount, lateCount int
	for _, check := range []struct {
		hash  string
		count *int
	}{
		{testCanonicalHash("oldtoken"), &oldCount},
		{testCanonicalHash("recenttoken"), &recentCount},
		{testCanonicalHash("lateoldtoken"), &lateCount},
	} {
		if err := st.db.QueryRow(`select count(*) from usage_events where event_hash=?`, check.hash).Scan(check.count); err != nil {
			t.Fatal(err)
		}
	}
	if oldCount != 0 || recentCount != 1 || lateCount != 1 {
		t.Fatalf("remaining raw events: old=%d recent=%d late=%d", oldCount, recentCount, lateCount)
	}
	var oldFTS, recentFTS int
	if err := st.db.QueryRow(`select count(*) from usage_monitoring_event_search_v1 where usage_monitoring_event_search_v1 match 'old'`).Scan(&oldFTS); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`select count(*) from usage_monitoring_event_search_v1 where usage_monitoring_event_search_v1 match 'rec'`).Scan(&recentFTS); err != nil {
		t.Fatal(err)
	}
	if oldFTS != 0 || recentFTS != 1 {
		t.Fatalf("FTS old=%d recent=%d", oldFTS, recentFTS)
	}
	cleanupCatchUpAll(t, st)
	var calls int
	if err := st.db.QueryRow(`select coalesce(sum(calls),0) from usage_hourly_aggregate_v1`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("rebuilt aggregate calls=%d want 2", calls)
	}
	ready, err := st.RequestCleanupRebuildReady(ctx, run.RebuildTargetID, true)
	if err != nil || !ready {
		t.Fatalf("rebuild ready=%t err=%v", ready, err)
	}
}

func TestRequestCleanupAllEventsCanReachReady(t *testing.T) {
	st := cleanupTestStore(t)
	ctx := context.Background()
	base := int64(1_778_000_000_000)
	if _, err := st.InsertEvents(ctx, []usage.Event{cleanupTestEvent("onlytoken", base)}); err != nil {
		t.Fatal(err)
	}
	preview, err := st.PreviewRequestCleanup(ctx, base+1000)
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateRequestCleanup(ctx, "abcdef0123456789abcdef0123456789", preview)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.DeleteRequestCleanupBatch(ctx, run.ID, 1000); err != nil {
		t.Fatal(err)
	}
	if _, done, err := st.DeleteRequestCleanupBatch(ctx, run.ID, 1000); err != nil || !done {
		t.Fatalf("done=%t err=%v", done, err)
	}
	run, err = st.FinalizeRequestCleanup(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.RebuildTargetID != 0 {
		t.Fatalf("rebuild target=%d want 0", run.RebuildTargetID)
	}
	cleanupCatchUpAll(t, st)
	ready, err := st.RequestCleanupRebuildReady(ctx, 0, true)
	if err != nil || !ready {
		t.Fatalf("empty rebuild ready=%t err=%v", ready, err)
	}
}
