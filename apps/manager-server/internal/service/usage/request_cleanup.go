package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/sqliteutil"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

var (
	ErrRequestCleanupInvalidRequest = errors.New("invalid request cleanup request")
	ErrRequestCleanupNoEvents       = errors.New("no request data matches the cleanup cutoff")
	ErrRequestCleanupUnavailable    = errors.New("request cleanup worker is unavailable")
	ErrRequestCleanupConflict       = store.ErrRequestCleanupUnavailable
	ErrRequestCleanupChanged        = store.ErrRequestCleanupChanged
	ErrRequestCleanupNotFound       = store.ErrRequestCleanupNotFound
	ErrRequestCleanupInvalidState   = store.ErrRequestCleanupState
)

const (
	requestCleanupPreviewTTL    = 10 * time.Minute
	requestCleanupScanInterval  = 2 * time.Second
	requestCleanupBatchSize     = 1000
	requestCleanupRebuildRounds = 8
	requestCleanupYieldDelay    = 20 * time.Millisecond
)

type RequestCleanupPreview struct {
	store.RequestCleanupPreview
	PreviewToken string `json:"preview_token"`
	ExpiresAtMS  int64  `json:"expires_at_ms"`
}

type RequestCleanupStatus struct {
	RunID             string `json:"run_id"`
	Status            string `json:"status"`
	CutoffTimestampMS int64  `json:"cutoff_timestamp_ms"`
	TargetEventID     int64  `json:"target_event_id"`
	TargetCount       int64  `json:"target_count"`
	DeletedCount      int64  `json:"deleted_count"`
	RebuildStatus     string `json:"rebuild_status"`
	ErrorCode         string `json:"error_code,omitempty"`
	CreatedAtMS       int64  `json:"created_at_ms"`
	UpdatedAtMS       int64  `json:"updated_at_ms"`
}

type requestCleanupPreviewEntry struct {
	preview     store.RequestCleanupPreview
	expiresAtMS int64
	runID       string
}

type requestCleanupManager struct {
	store            *store.Store
	aggregateEnabled bool
	mu               sync.Mutex
	previews         map[string]requestCleanupPreviewEntry
	started          bool
	rootCtx          context.Context
	done             chan struct{}
	wake             chan struct{}
}

func newRequestCleanupManager(st *store.Store) *requestCleanupManager {
	return &requestCleanupManager{store: st, previews: make(map[string]requestCleanupPreviewEntry), wake: make(chan struct{}, 1)}
}

func requestCleanupRandomID(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func validRequestCleanupRunID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (s *Service) PreviewRequestCleanup(ctx context.Context, cutoffMS int64) (RequestCleanupPreview, error) {
	if cutoffMS <= 0 || cutoffMS > time.Now().UnixMilli() {
		return RequestCleanupPreview{}, ErrRequestCleanupInvalidRequest
	}
	preview, err := s.store.PreviewRequestCleanup(ctx, cutoffMS)
	if err != nil {
		return RequestCleanupPreview{}, err
	}
	if preview.EventCount == 0 {
		return RequestCleanupPreview{}, ErrRequestCleanupNoEvents
	}
	token, err := requestCleanupRandomID(24)
	if err != nil {
		return RequestCleanupPreview{}, err
	}
	expiresAtMS := time.Now().Add(requestCleanupPreviewTTL).UnixMilli()
	m := s.requestCleanup
	m.mu.Lock()
	for key, entry := range m.previews {
		if entry.expiresAtMS <= time.Now().UnixMilli() {
			delete(m.previews, key)
		}
	}
	m.previews[token] = requestCleanupPreviewEntry{preview: preview, expiresAtMS: expiresAtMS}
	m.mu.Unlock()
	return RequestCleanupPreview{RequestCleanupPreview: preview, PreviewToken: token, ExpiresAtMS: expiresAtMS}, nil
}

func (s *Service) ExecuteRequestCleanup(ctx context.Context, preview store.RequestCleanupPreview, token string) (RequestCleanupStatus, error) {
	if preview.CutoffTimestampMS <= 0 || preview.TargetEventID < 0 || preview.EventCount <= 0 || len(token) != 48 {
		return RequestCleanupStatus{}, ErrRequestCleanupInvalidRequest
	}
	m := s.requestCleanup
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.previews[token]
	if !ok || entry.expiresAtMS <= time.Now().UnixMilli() || entry.preview != preview {
		return RequestCleanupStatus{}, ErrRequestCleanupChanged
	}
	if entry.runID != "" {
		run, err := m.store.RequestCleanupRun(ctx, entry.runID)
		if err != nil {
			return RequestCleanupStatus{}, err
		}
		return s.requestCleanupStatus(ctx, run)
	}
	if !m.started || m.rootCtx.Err() != nil {
		return RequestCleanupStatus{}, ErrRequestCleanupUnavailable
	}
	id, err := requestCleanupRandomID(16)
	if err != nil {
		return RequestCleanupStatus{}, err
	}
	run, err := m.store.CreateRequestCleanup(ctx, id, preview)
	if err != nil {
		return RequestCleanupStatus{}, err
	}
	entry.runID = id
	m.previews[token] = entry
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return s.requestCleanupStatus(ctx, run)
}

func (s *Service) RequestCleanupStatus(ctx context.Context, runID string) (RequestCleanupStatus, error) {
	var run store.RequestCleanupRun
	var err error
	if runID == "" {
		var found bool
		run, found, err = s.store.LatestRequestCleanup(ctx)
		if err != nil {
			return RequestCleanupStatus{}, err
		}
		if !found {
			return RequestCleanupStatus{}, ErrRequestCleanupNotFound
		}
	} else {
		if !validRequestCleanupRunID(runID) {
			return RequestCleanupStatus{}, ErrRequestCleanupInvalidRequest
		}
		run, err = s.store.RequestCleanupRun(ctx, runID)
		if err != nil {
			return RequestCleanupStatus{}, err
		}
	}
	return s.requestCleanupStatus(ctx, run)
}

func (s *Service) requestCleanupStatus(ctx context.Context, run store.RequestCleanupRun) (RequestCleanupStatus, error) {
	rebuildStatus := "pending"
	switch run.Status {
	case "completed":
		rebuildStatus = "ready"
	case "failed":
		rebuildStatus = "failed"
	case "rebuilding":
		ready, err := s.store.RequestCleanupRebuildReady(ctx, run.RebuildTargetID, s.requestCleanup.aggregateEnabled)
		if err != nil {
			return RequestCleanupStatus{}, err
		}
		if ready {
			rebuildStatus = "ready"
		} else {
			rebuildStatus = "rebuilding"
		}
	}
	return RequestCleanupStatus{RunID: run.ID, Status: run.Status,
		CutoffTimestampMS: run.CutoffTimestampMS, TargetEventID: run.TargetEventID,
		TargetCount: run.TargetCount, DeletedCount: run.DeletedCount,
		RebuildStatus: rebuildStatus, ErrorCode: run.ErrorCode,
		CreatedAtMS: run.CreatedAtMS, UpdatedAtMS: run.UpdatedAtMS}, nil
}

func (s *Service) ResumeRequestCleanup(ctx context.Context, runID string) (RequestCleanupStatus, error) {
	if !validRequestCleanupRunID(runID) {
		return RequestCleanupStatus{}, ErrRequestCleanupInvalidRequest
	}
	m := s.requestCleanup
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || m.rootCtx.Err() != nil {
		return RequestCleanupStatus{}, ErrRequestCleanupUnavailable
	}
	run, err := m.store.ResumeRequestCleanup(ctx, runID)
	if err != nil {
		return RequestCleanupStatus{}, err
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return s.requestCleanupStatus(ctx, run)
}

func (s *Service) RequestCleanupReadReady(ctx context.Context) (bool, error) {
	return s.store.RequestCleanupReadReady(ctx)
}

func (s *Service) StartRequestCleanupJobs(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := s.requestCleanup
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	m.rootCtx = ctx
	m.done = make(chan struct{})
	done := m.done
	m.mu.Unlock()
	go func() { defer close(done); m.loop(ctx) }()
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *Service) WaitRequestCleanupJobs(ctx context.Context) error {
	m := s.requestCleanup
	m.mu.Lock()
	done := m.done
	m.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *requestCleanupManager) loop(ctx context.Context) {
	ticker := time.NewTicker(requestCleanupScanInterval)
	defer ticker.Stop()
	busyStreak := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-ticker.C:
		}
		run, found, err := m.store.ActiveRequestCleanup(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[request-cleanup] 检查作业失败：%v", err)
			}
			continue
		}
		if !found || run.Status == "failed" {
			continue
		}
		if err := m.advance(ctx, run); err != nil {
			if ctx.Err() != nil {
				return
			}
			if sqliteutil.IsBusyError(err) {
				busyStreak++
				if busyStreak == 1 || busyStreak%10 == 0 {
					log.Printf("[request-cleanup] SQLite 写锁争用，作业 %s 将重试（第 %d 次）：%v", run.ID, busyStreak, err)
				}
				delay := time.Duration(min(busyStreak, 10)) * 200 * time.Millisecond
				if !waitRequestCleanupYield(ctx, delay) {
					return
				}
				continue
			}
			busyStreak = 0
			log.Printf("[request-cleanup] 作业 %s 处理失败：%v", run.ID, err)
			failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			code := "request_cleanup_failed"
			latest, inspectErr := m.store.RequestCleanupRun(failureCtx, run.ID)
			if inspectErr == nil {
				if latest.ResetDone {
					code = "request_cleanup_rebuild_failed"
				} else {
					code = "request_cleanup_delete_failed"
				}
			} else {
				log.Printf("[request-cleanup] 获取作业 %s 阶段失败：%v", run.ID, inspectErr)
			}
			if stateErr := m.store.SetRequestCleanupState(failureCtx, run.ID, "failed", code); stateErr != nil {
				log.Printf("[request-cleanup] 记录作业 %s 失败状态时出错：%v", run.ID, stateErr)
			}
			cancel()
		} else {
			busyStreak = 0
		}
	}
}

func waitRequestCleanupYield(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (m *requestCleanupManager) advance(ctx context.Context, run store.RequestCleanupRun) error {
	if run.Status == "queued" || run.Status == "deleting" {
		batches := 0
		for ctx.Err() == nil {
			_, done, err := m.store.DeleteRequestCleanupBatch(ctx, run.ID, requestCleanupBatchSize)
			if err != nil {
				return err
			}
			if done {
				break
			}
			batches++
			if batches%4 == 0 && !waitRequestCleanupYield(ctx, requestCleanupYieldDelay) {
				return ctx.Err()
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var err error
		run, err = m.store.FinalizeRequestCleanup(ctx, run.ID)
		if err != nil {
			return err
		}
	}
	if run.Status != "rebuilding" {
		return nil
	}
	// 清理期间由本作业独占派生回填；每轮多个小事务，并定期让出写锁给采集器。
	for round := 0; round < requestCleanupRebuildRounds && ctx.Err() == nil; round++ {
		nowMS := time.Now().UnixMilli()
		if _, err := m.store.CatchUpAccountHistoryRollups(ctx, requestCleanupBatchSize, nowMS); err != nil {
			return err
		}
		// dashboard_hourly 没有常驻 worker，必须由清理作业推进。
		if _, err := m.store.CatchUpDashboardHourlyRollups(ctx, requestCleanupBatchSize, nowMS); err != nil {
			return err
		}
		if m.aggregateEnabled {
			if _, err := m.store.CatchUpUsageHourlyAggregate(ctx, requestCleanupBatchSize, nowMS); err != nil {
				return err
			}
		}
		if _, err := m.store.CatchUpUsageMonitoringProjection(ctx, requestCleanupBatchSize, nowMS); err != nil {
			return err
		}
		if _, err := m.store.CatchUpUsageMonitoringMetadata(ctx, requestCleanupBatchSize, nowMS); err != nil {
			return err
		}
		if _, err := m.store.CatchUpUsageMonitoringStats(ctx, requestCleanupBatchSize, nowMS); err != nil {
			return err
		}
		if _, err := m.store.CatchUpCodexLegacyIdentityEvidence(ctx, requestCleanupBatchSize, nowMS); err != nil {
			return err
		}
		if _, err := m.store.CatchUpUsagePricing(ctx, requestCleanupBatchSize, nowMS); err != nil {
			return err
		}
		ready, err := m.store.RequestCleanupRebuildReady(ctx, run.RebuildTargetID, m.aggregateEnabled)
		if err != nil {
			return err
		}
		if ready {
			return m.store.SetRequestCleanupState(ctx, run.ID, "completed", "")
		}
		if round+1 < requestCleanupRebuildRounds && !waitRequestCleanupYield(ctx, requestCleanupYieldDelay) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}
