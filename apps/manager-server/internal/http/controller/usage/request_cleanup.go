package usage

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	usagesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type requestCleanupPreviewRequest struct {
	CutoffTimestampMS int64 `json:"cutoff_timestamp_ms"`
}

type requestCleanupExecuteRequest struct {
	CutoffTimestampMS int64  `json:"cutoff_timestamp_ms"`
	TargetEventID     int64  `json:"target_event_id"`
	EventCount        int64  `json:"event_count"`
	PreviewToken      string `json:"preview_token"`
	Confirmation      string `json:"confirmation"`
}

type requestCleanupResumeRequest struct {
	RunID string `json:"run_id"`
}

func (h *Handler) handleRequestCleanup(w http.ResponseWriter, r *http.Request, path string) {
	switch path {
	case usageRequestCleanupPath + "/preview":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		var req requestCleanupPreviewRequest
		if err := decodeSingleJSON(w, r, &req); err != nil {
			writeRequestCleanupRequestError(w, err)
			return
		}
		preview, err := h.App.UsageService.PreviewRequestCleanup(r.Context(), req.CutoffTimestampMS)
		if err != nil {
			h.writeRequestCleanupError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, preview)
	case usageRequestCleanupPath + "/execute":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		var req requestCleanupExecuteRequest
		if err := decodeSingleJSON(w, r, &req); err != nil {
			writeRequestCleanupRequestError(w, err)
			return
		}
		if req.Confirmation != "DELETE_REQUEST_DATA" {
			writeRequestCleanupRequestError(w, usagesvc.ErrRequestCleanupInvalidRequest)
			return
		}
		status, err := h.App.UsageService.ExecuteRequestCleanup(r.Context(),
			storeRequestCleanupPreview(req), req.PreviewToken)
		if err != nil {
			h.writeRequestCleanupError(w, r, err)
			return
		}
		w.Header().Set("Location", usageRequestCleanupPath+"/status?run_id="+status.RunID)
		w.Header().Set("Retry-After", "2")
		response.JSON(w, http.StatusAccepted, status)
	case usageRequestCleanupPath + "/status":
		if r.Method != http.MethodGet {
			response.MethodNotAllowed(w)
			return
		}
		if len(r.URL.Query()["run_id"]) > 1 {
			writeRequestCleanupRequestError(w, usagesvc.ErrRequestCleanupInvalidRequest)
			return
		}
		status, err := h.App.UsageService.RequestCleanupStatus(r.Context(), strings.TrimSpace(r.URL.Query().Get("run_id")))
		if err != nil {
			h.writeRequestCleanupError(w, r, err)
			return
		}
		response.JSON(w, http.StatusOK, status)
	case usageRequestCleanupPath + "/resume":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		var req requestCleanupResumeRequest
		if err := decodeSingleJSON(w, r, &req); err != nil {
			writeRequestCleanupRequestError(w, err)
			return
		}
		status, err := h.App.UsageService.ResumeRequestCleanup(r.Context(), req.RunID)
		if err != nil {
			h.writeRequestCleanupError(w, r, err)
			return
		}
		w.Header().Set("Location", usageRequestCleanupPath+"/status?run_id="+status.RunID)
		w.Header().Set("Retry-After", "2")
		response.JSON(w, http.StatusAccepted, status)
	default:
		http.NotFound(w, r)
	}
}

func storeRequestCleanupPreview(req requestCleanupExecuteRequest) store.RequestCleanupPreview {
	return store.RequestCleanupPreview{CutoffTimestampMS: req.CutoffTimestampMS, TargetEventID: req.TargetEventID, EventCount: req.EventCount}
}

func writeRequestCleanupRequestError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	code := "request_cleanup_invalid_request"
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		status = http.StatusRequestEntityTooLarge
		code = "request_cleanup_request_too_large"
	}
	response.JSON(w, status, map[string]any{"error": "invalid request cleanup request", "code": code})
}

func (h *Handler) writeRequestCleanupError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	code := "request_cleanup_failed"
	message := "request cleanup failed"
	switch {
	case errors.Is(err, usagesvc.ErrRequestCleanupInvalidRequest):
		status = http.StatusBadRequest
		code = "request_cleanup_invalid_request"
		message = "invalid request cleanup request"
	case errors.Is(err, usagesvc.ErrRequestCleanupNoEvents):
		status = http.StatusUnprocessableEntity
		code = "request_cleanup_no_events"
		message = "no request data matches the cleanup cutoff"
	case errors.Is(err, usagesvc.ErrRequestCleanupNotFound):
		status = http.StatusNotFound
		code = "request_cleanup_not_found"
		message = "request cleanup run was not found"
	case errors.Is(err, usagesvc.ErrRequestCleanupConflict):
		status = http.StatusConflict
		code = "request_cleanup_conflict"
		message = "another usage maintenance operation is active"
	case errors.Is(err, usagesvc.ErrRequestCleanupChanged):
		status = http.StatusConflict
		code = "request_cleanup_preview_changed"
		message = "request cleanup preview expired or changed"
	case errors.Is(err, usagesvc.ErrRequestCleanupInvalidState):
		status = http.StatusConflict
		code = "request_cleanup_invalid_state"
		message = "request cleanup cannot resume in its current state"
	case errors.Is(err, usagesvc.ErrRequestCleanupUnavailable):
		status = http.StatusServiceUnavailable
		code = "request_cleanup_unavailable"
		message = "request cleanup worker is unavailable"
	}
	if status >= http.StatusInternalServerError {
		log.Printf("request cleanup API failed: %v", err)
	}
	payload := map[string]any{"error": message, "code": code}
	if status == http.StatusConflict && errors.Is(err, usagesvc.ErrRequestCleanupConflict) {
		if active, activeErr := h.App.UsageService.RequestCleanupStatus(r.Context(), ""); activeErr == nil && active.Status != "completed" {
			payload["run_id"] = active.RunID
		}
	}
	response.JSON(w, status, payload)
}
