package usage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	usageevent "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	_ "modernc.org/sqlite"
)

func requestCleanupTestStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`update usage_data_migrations set status='completed'`); err != nil {
		t.Fatal(err)
	}
	return st
}

func waitForRequestCleanup(t *testing.T, svc *Service, id string) RequestCleanupStatus {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		status, err := svc.RequestCleanupStatus(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if status.Status == "completed" || status.Status == "failed" {
			return status
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("request cleanup did not finish")
	return RequestCleanupStatus{}
}

func TestRequestCleanupJobCompletesAndPreviewTokenIsOneShot(t *testing.T) {
	st := requestCleanupTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := New(st, WithArchive(ArchiveConfig{AggregateReadsEnabled: true}))
	if err := svc.StartRequestCleanupJobs(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		waitCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = svc.WaitRequestCleanupJobs(waitCtx)
	})
	oldMS := time.Now().Add(-3 * time.Hour).UnixMilli()
	event := usageevent.Event{EventHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TimestampMS: oldMS, Timestamp: time.UnixMilli(oldMS).UTC().Format(time.RFC3339Nano),
		Model: "cleanup-test", InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CreatedAtMS: oldMS}
	if _, err := st.InsertEvents(context.Background(), []usageevent.Event{event}); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewRequestCleanup(context.Background(), time.Now().Add(-2*time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.ExecuteRequestCleanup(context.Background(), preview.RequestCleanupPreview, preview.PreviewToken)
	if err != nil {
		t.Fatal(err)
	}
	finished := waitForRequestCleanup(t, svc, created.RunID)
	if finished.Status != "completed" || finished.RebuildStatus != "ready" || finished.DeletedCount != 1 {
		t.Fatalf("cleanup status=%+v", finished)
	}
	replay, err := svc.ExecuteRequestCleanup(context.Background(), preview.RequestCleanupPreview, preview.PreviewToken)
	if err != nil {
		t.Fatal(err)
	}
	if replay.RunID != created.RunID {
		t.Fatalf("replay created second run: %s != %s", replay.RunID, created.RunID)
	}
	latest, err := svc.RequestCleanupStatus(context.Background(), "")
	if err != nil || latest.RunID != created.RunID {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	if _, err := svc.ResumeRequestCleanup(context.Background(), created.RunID); err != ErrRequestCleanupInvalidState {
		t.Fatalf("completed run resumed: %v", err)
	}
}

func TestRequestCleanupFailedBatchResumesAfterStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update usage_data_migrations set status='completed'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	oldMS := time.Now().Add(-3 * time.Hour).UnixMilli()
	makeEvent := func(hash string, timestampMS int64) usageevent.Event {
		return usageevent.Event{EventHash: hash, TimestampMS: timestampMS,
			Timestamp: time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
			Model:     "cleanup-resume-test", CreatedAtMS: timestampMS}
	}
	if _, err := st.InsertEvents(context.Background(), []usageevent.Event{
		makeEvent("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", oldMS),
		makeEvent("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", oldMS+1000),
		makeEvent("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", oldMS+2*60*60*1000),
	}); err != nil {
		t.Fatal(err)
	}
	preview, err := st.PreviewRequestCleanup(context.Background(), oldMS+60*60*1000)
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateRequestCleanup(context.Background(), "abcdefabcdefabcdefabcdefabcdefab", preview)
	if err != nil {
		t.Fatal(err)
	}
	if count, done, err := st.DeleteRequestCleanupBatch(context.Background(), run.ID, 1); err != nil || done || count != 1 {
		t.Fatalf("first batch count=%d done=%t err=%v", count, done, err)
	}
	if err := st.SetRequestCleanupState(context.Background(), run.ID, "failed", "request_cleanup_failed"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	svc := New(reopened, WithArchive(ArchiveConfig{AggregateReadsEnabled: true}))
	before, err := svc.RequestCleanupStatus(context.Background(), "")
	if err != nil || before.RunID != run.ID || before.Status != "failed" || before.DeletedCount != 1 {
		t.Fatalf("discover failed run=%+v err=%v", before, err)
	}
	ready, err := svc.RequestCleanupReadReady(context.Background())
	if err != nil || ready {
		t.Fatalf("partial failed read ready=%t err=%v", ready, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := svc.StartRequestCleanupJobs(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		waitCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = svc.WaitRequestCleanupJobs(waitCtx)
	})
	if _, err := svc.ResumeRequestCleanup(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	finished := waitForRequestCleanup(t, svc, run.ID)
	if finished.Status != "completed" || finished.DeletedCount != 2 || finished.RebuildStatus != "ready" {
		t.Fatalf("resumed status=%+v", finished)
	}
	remaining, err := reopened.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].EventHash != "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" {
		t.Fatalf("recent request was lost or old requests remain: %+v", remaining)
	}
}
