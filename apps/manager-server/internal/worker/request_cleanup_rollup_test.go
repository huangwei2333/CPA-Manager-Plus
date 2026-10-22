package worker

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestRequestCleanupPausesOnlyConcurrentRollupWorkers(t *testing.T) {
	sqlDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	st := store.New(sqlDB)
	ctx := context.Background()
	if _, err := sqlDB.Exec(`update usage_data_migrations set status='completed'`); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-2 * time.Hour).UnixMilli()
	if _, err := st.InsertEvents(ctx, []usage.Event{
		usageHourlyAggregateWorkerEvent("cleanup-old", base, 10),
		usageHourlyAggregateWorkerEvent("cleanup-retained", base+20_000, 20),
	}); err != nil {
		t.Fatal(err)
	}
	preview, err := st.PreviewRequestCleanup(ctx, base+10_000)
	if err != nil || preview.EventCount != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	run, err := st.CreateRequestCleanup(ctx, "1234567890abcdef1234567890abcdef", preview)
	if err != nil {
		t.Fatal(err)
	}
	workers := []func(context.Context) bool{
		NewAccountHistoryRollupWorker(st).catchUp,
		NewUsageHourlyAggregateWorker(st).catchUp,
		NewUsagePricingRollupWorker(st).catchUp,
	}
	assertPaused := func(stage string) {
		t.Helper()
		for _, catchUp := range workers {
			if catchUp(ctx) {
				t.Fatalf("%s: paused worker reported pending work", stage)
			}
		}
		var checkpoints, aggregateRows, pricingRows int
		for _, item := range []struct {
			query string
			into  *int
		}{
			{`select coalesce(max(last_event_id),0) from usage_rollup_checkpoints where name='account_history'`, &checkpoints},
			{`select count(*) from usage_hourly_aggregate_v1`, &aggregateRows},
			{`select count(*) from usage_pricing_hourly_rollups_v1`, &pricingRows},
		} {
			if err := sqlDB.QueryRow(item.query).Scan(item.into); err != nil {
				t.Fatal(err)
			}
		}
		if checkpoints != 0 || aggregateRows != 0 || pricingRows != 0 {
			t.Fatalf("%s: competing workers wrote derived data: account=%d aggregate=%d pricing=%d", stage, checkpoints, aggregateRows, pricingRows)
		}
	}
	assertPaused("queued")
	if _, _, err := st.DeleteRequestCleanupBatch(ctx, run.ID, 1000); err != nil {
		t.Fatal(err)
	}
	if _, done, err := st.DeleteRequestCleanupBatch(ctx, run.ID, 1000); err != nil || !done {
		t.Fatalf("finish deletion done=%t err=%v", done, err)
	}
	if _, err := st.FinalizeRequestCleanup(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	assertPaused("rebuilding")
	if err := st.SetRequestCleanupState(ctx, run.ID, "failed", "test_fixture"); err != nil {
		t.Fatal(err)
	}
	assertPaused("failed with partial deletion")
	if _, err := st.ResumeRequestCleanup(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	assertPaused("resumed rebuilding")
	if err := st.SetRequestCleanupState(ctx, run.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	paused, err := st.RequestCleanupOwnsRollups(ctx)
	if err != nil || paused {
		t.Fatalf("completed run still owns rollups: paused=%t err=%v", paused, err)
	}
	if NewUsageHourlyAggregateWorker(st).catchUp(ctx) {
		t.Fatal("aggregate catch-up remained pending after cleanup completion")
	}
	state, err := st.UsageHourlyAggregateState(ctx)
	if err != nil || state.CoverageEventID != 2 {
		t.Fatalf("aggregate did not resume: state=%+v err=%v", state, err)
	}
}
