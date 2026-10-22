package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	usagesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	usageevent "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestRequestCleanupAPIRequiresAdminAndConfirmsDeletion(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	svc := usagesvc.New(st, usagesvc.WithArchive(usagesvc.ArchiveConfig{AggregateReadsEnabled: true}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := svc.StartRequestCleanupJobs(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		waitCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = svc.WaitRequestCleanupJobs(waitCtx)
	})
	handler := &Handler{App: &app.Context{AdminAuthService: adminauthsvc.New(cfg, st), UsageService: svc}}
	httpHandler := http.HandlerFunc(handler.Handle)
	denied := testutil.Request(t, httpHandler, http.MethodGet, usageRequestCleanupPath+"/status", "", "wrong-key")
	if denied.Code == http.StatusOK {
		t.Fatalf("unauthorized status=%d", denied.Code)
	}
	missing := testutil.Request(t, httpHandler, http.MethodGet, usageRequestCleanupPath+"/status", "", testutil.AdminKey)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
	unknownField := testutil.Request(t, httpHandler, http.MethodPost, usageRequestCleanupPath+"/preview",
		`{"cutoff_timestamp_ms":1,"unexpected":true}`, testutil.AdminKey)
	if unknownField.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", unknownField.Code, unknownField.Body.String())
	}
	multipleValues := testutil.Request(t, httpHandler, http.MethodPost, usageRequestCleanupPath+"/preview",
		`{"cutoff_timestamp_ms":1}{"cutoff_timestamp_ms":2}`, testutil.AdminKey)
	if multipleValues.Code != http.StatusBadRequest {
		t.Fatalf("multiple values status=%d body=%s", multipleValues.Code, multipleValues.Body.String())
	}
	oldMS := time.Now().Add(-3 * time.Hour).UnixMilli()
	event := usageevent.Event{EventHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		TimestampMS: oldMS, Timestamp: time.UnixMilli(oldMS).UTC().Format(time.RFC3339Nano),
		Model: "cleanup-api-test", CreatedAtMS: oldMS}
	if _, err := st.InsertEvents(context.Background(), []usageevent.Event{event}); err != nil {
		t.Fatal(err)
	}
	previewBody := fmt.Sprintf(`{"cutoff_timestamp_ms":%d}`, time.Now().Add(-2*time.Hour).UnixMilli())
	previewResponse := testutil.Request(t, httpHandler, http.MethodPost, usageRequestCleanupPath+"/preview", previewBody, testutil.AdminKey)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview usagesvc.RequestCleanupPreview
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	requestBody := fmt.Sprintf(`{"cutoff_timestamp_ms":%d,"target_event_id":%d,"event_count":%d,"preview_token":%q,"confirmation":"WRONG"}`,
		preview.CutoffTimestampMS, preview.TargetEventID, preview.EventCount, preview.PreviewToken)
	rejected := testutil.Request(t, httpHandler, http.MethodPost, usageRequestCleanupPath+"/execute", requestBody, testutil.AdminKey)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("bad confirmation status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	requestBody = fmt.Sprintf(`{"cutoff_timestamp_ms":%d,"target_event_id":%d,"event_count":%d,"preview_token":%q,"confirmation":"DELETE_REQUEST_DATA"}`,
		preview.CutoffTimestampMS, preview.TargetEventID, preview.EventCount, preview.PreviewToken)
	accepted := testutil.Request(t, httpHandler, http.MethodPost, usageRequestCleanupPath+"/execute", requestBody, testutil.AdminKey)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("execute status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	var status usagesvc.RequestCleanupStatus
	if err := json.Unmarshal(accepted.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.RunID == "" {
		t.Fatal("missing run ID")
	}
	replay := testutil.Request(t, httpHandler, http.MethodPost, usageRequestCleanupPath+"/execute", requestBody, testutil.AdminKey)
	if replay.Code != http.StatusAccepted {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayStatus usagesvc.RequestCleanupStatus
	if err := json.Unmarshal(replay.Body.Bytes(), &replayStatus); err != nil {
		t.Fatal(err)
	}
	if replayStatus.RunID != status.RunID {
		t.Fatalf("replay run=%s want=%s", replayStatus.RunID, status.RunID)
	}
}

func TestRequestCleanupBlocksCompatibleUsageAndExportDuringDeletion(t *testing.T) {
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	ctx := context.Background()
	oldMS := time.Now().Add(-3 * time.Hour).UnixMilli()
	if _, err := st.InsertEvents(ctx, []usageevent.Event{{
		EventHash:   "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		TimestampMS: oldMS, Timestamp: time.UnixMilli(oldMS).UTC().Format(time.RFC3339Nano),
		Model: "cleanup-guard-test", CreatedAtMS: oldMS,
	}}); err != nil {
		t.Fatal(err)
	}
	preview, err := st.PreviewRequestCleanup(ctx, oldMS+1000)
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateRequestCleanup(ctx, "abcdef0123456789abcdef0123456789", preview)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.DeleteRequestCleanupBatch(ctx, run.ID, 1); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{App: &app.Context{Config: cfg, AdminAuthService: adminauthsvc.New(cfg, st), UsageService: usagesvc.New(st)}}
	for _, path := range []string{"/v0/management/usage", "/v0/management/usage/export"} {
		t.Run(path, func(t *testing.T) {
			recorder := testutil.Request(t, http.HandlerFunc(handler.Handle), http.MethodGet, path, "", testutil.AdminKey)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var payload map[string]string
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["code"] != "request_cleanup_rebuilding" {
				t.Fatalf("unexpected response code: %q", payload["code"])
			}
		})
	}
}
