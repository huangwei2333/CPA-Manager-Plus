package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrRequestCleanupUnavailable = errors.New("request cleanup is unavailable while another maintenance operation exists")
	ErrRequestCleanupChanged     = errors.New("request cleanup preview is no longer current")
	ErrRequestCleanupNotFound    = errors.New("request cleanup run was not found")
	ErrRequestCleanupState       = errors.New("request cleanup run is in an invalid state")
)

type RequestCleanupPreview struct {
	CutoffTimestampMS int64 `json:"cutoff_timestamp_ms"`
	TargetEventID     int64 `json:"target_event_id"`
	EventCount        int64 `json:"event_count"`
}

type RequestCleanupRun struct {
	ID                string `json:"run_id"`
	Status            string `json:"status"`
	CutoffTimestampMS int64  `json:"cutoff_timestamp_ms"`
	TargetEventID     int64  `json:"target_event_id"`
	TargetCount       int64  `json:"target_count"`
	LastDeletedID     int64  `json:"-"`
	DeletedCount      int64  `json:"deleted_count"`
	ErrorCode         string `json:"error_code,omitempty"`
	RebuildTargetID   int64  `json:"-"`
	ResetDone         bool   `json:"-"`
	CreatedAtMS       int64  `json:"created_at_ms"`
	UpdatedAtMS       int64  `json:"updated_at_ms"`
}

const requestCleanupRunColumns = `id, status, cutoff_timestamp_ms, target_event_id,
	target_count, last_deleted_event_id, deleted_count, error_code,
	rebuild_target_event_id, reset_done, created_at_ms, updated_at_ms`

func readRequestCleanupRun(row *sql.Row) (RequestCleanupRun, error) {
	var run RequestCleanupRun
	var resetDone int
	err := row.Scan(&run.ID, &run.Status, &run.CutoffTimestampMS, &run.TargetEventID,
		&run.TargetCount, &run.LastDeletedID, &run.DeletedCount, &run.ErrorCode,
		&run.RebuildTargetID, &resetDone, &run.CreatedAtMS, &run.UpdatedAtMS)
	run.ResetDone = resetDone != 0
	return run, err
}

func requestCleanupUnavailableTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var blocked bool
	err := tx.QueryRowContext(ctx, `select
		exists(select 1 from usage_archive_runs limit 1)
		or exists(select 1 from usage_archive_event_refs limit 1)
		or exists(select 1 from usage_maintenance_locks limit 1)
		or exists(select 1 from usage_archive_deleted_coverage_daily limit 1)
		or exists(select 1 from usage_request_cleanup_runs
			where status in ('queued','deleting','rebuilding') or (status = 'failed' and deleted_count > 0)
			limit 1)`).Scan(&blocked)
	return blocked, err
}

func (s *Store) PreviewRequestCleanup(ctx context.Context, cutoffMS int64) (RequestCleanupPreview, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RequestCleanupPreview{}, err
	}
	defer func() { _ = tx.Rollback() }()
	blocked, err := requestCleanupUnavailableTx(ctx, tx)
	if err != nil {
		return RequestCleanupPreview{}, err
	}
	if blocked {
		return RequestCleanupPreview{}, ErrRequestCleanupUnavailable
	}
	var preview RequestCleanupPreview
	preview.CutoffTimestampMS = cutoffMS
	err = tx.QueryRowContext(ctx, `select coalesce(max(id),0) from usage_events`).Scan(&preview.TargetEventID)
	if err != nil {
		return RequestCleanupPreview{}, err
	}
	err = tx.QueryRowContext(ctx, `select count(*) from usage_events
		where timestamp_ms < ? and id <= ?`, cutoffMS, preview.TargetEventID).Scan(&preview.EventCount)
	if err != nil {
		return RequestCleanupPreview{}, err
	}
	return preview, tx.Commit()
}

func (s *Store) CreateRequestCleanup(ctx context.Context, id string, preview RequestCleanupPreview) (RequestCleanupRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RequestCleanupRun{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// 获取 SQLite writer slot，使预览校验与任务创建相对于其他写入原子化。
	if _, err = tx.ExecContext(ctx, `update usage_request_cleanup_runs set updated_at_ms=updated_at_ms where id=''`); err != nil {
		return RequestCleanupRun{}, err
	}
	blocked, err := requestCleanupUnavailableTx(ctx, tx)
	if err != nil {
		return RequestCleanupRun{}, err
	}
	if blocked {
		return RequestCleanupRun{}, ErrRequestCleanupUnavailable
	}
	var latestID, count int64
	if err = tx.QueryRowContext(ctx, `select coalesce(max(id),0) from usage_events`).Scan(&latestID); err != nil {
		return RequestCleanupRun{}, err
	}
	if latestID < preview.TargetEventID {
		return RequestCleanupRun{}, ErrRequestCleanupChanged
	}
	if err = tx.QueryRowContext(ctx, `select count(*) from usage_events where timestamp_ms<? and id<=?`,
		preview.CutoffTimestampMS, preview.TargetEventID).Scan(&count); err != nil {
		return RequestCleanupRun{}, err
	}
	if count != preview.EventCount || count == 0 {
		return RequestCleanupRun{}, ErrRequestCleanupChanged
	}
	nowMS := time.Now().UnixMilli()
	_, err = tx.ExecContext(ctx, `insert into usage_request_cleanup_runs
		(id,cutoff_timestamp_ms,target_event_id,target_count,status,created_at_ms,updated_at_ms)
		values(?,?,?,?,'queued',?,?)`, id, preview.CutoffTimestampMS, preview.TargetEventID,
		preview.EventCount, nowMS, nowMS)
	if err != nil {
		return RequestCleanupRun{}, err
	}
	if err = tx.Commit(); err != nil {
		return RequestCleanupRun{}, err
	}
	return RequestCleanupRun{ID: id, Status: "queued", CutoffTimestampMS: preview.CutoffTimestampMS,
		TargetEventID: preview.TargetEventID, TargetCount: preview.EventCount,
		CreatedAtMS: nowMS, UpdatedAtMS: nowMS}, nil
}

func (s *Store) RequestCleanupRun(ctx context.Context, id string) (RequestCleanupRun, error) {
	run, err := readRequestCleanupRun(s.db.QueryRowContext(ctx,
		`select `+requestCleanupRunColumns+` from usage_request_cleanup_runs where id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return RequestCleanupRun{}, ErrRequestCleanupNotFound
	}
	return run, err
}

func (s *Store) ActiveRequestCleanup(ctx context.Context) (RequestCleanupRun, bool, error) {
	run, err := readRequestCleanupRun(s.db.QueryRowContext(ctx,
		`select `+requestCleanupRunColumns+` from usage_request_cleanup_runs
		where status in ('queued','deleting','rebuilding') or (status='failed' and deleted_count>0)
		order by case when status='failed' then 1 else 0 end, created_at_ms desc limit 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return RequestCleanupRun{}, false, nil
	}
	return run, err == nil, err
}

func (s *Store) LatestRequestCleanup(ctx context.Context) (RequestCleanupRun, bool, error) {
	run, err := readRequestCleanupRun(s.db.QueryRowContext(ctx,
		`select `+requestCleanupRunColumns+` from usage_request_cleanup_runs
		order by created_at_ms desc, rowid desc limit 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return RequestCleanupRun{}, false, nil
	}
	return run, err == nil, err
}

// 每批写入事务只触及已冻结 ID 范围，收集器可在批次之间继续写入新请求。
func (s *Store) DeleteRequestCleanupBatch(ctx context.Context, id string, limit int) (int, bool, error) {
	if limit <= 0 || limit > 1000 {
		return 0, false, fmt.Errorf("invalid cleanup batch limit")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `update usage_request_cleanup_runs set updated_at_ms=?
		where id=? and status in ('queued','deleting')`, time.Now().UnixMilli(), id); err != nil {
		return 0, false, err
	}
	run, err := readRequestCleanupRun(tx.QueryRowContext(ctx,
		`select `+requestCleanupRunColumns+` from usage_request_cleanup_runs where id=?`, id))
	if err != nil {
		return 0, false, err
	}
	if run.Status != "queued" && run.Status != "deleting" {
		return 0, false, ErrRequestCleanupState
	}
	rows, err := tx.QueryContext(ctx, `select id from usage_events where id>? and id<=? and timestamp_ms<?
		order by id limit ?`, run.LastDeletedID, run.TargetEventID, run.CutoffTimestampMS, limit)
	if err != nil {
		return 0, false, err
	}
	ids := make([]any, 0, limit)
	for rows.Next() {
		var eventID int64
		if scanErr := rows.Scan(&eventID); scanErr != nil {
			_ = rows.Close()
			return 0, false, scanErr
		}
		ids = append(ids, eventID)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return 0, false, err
	}
	if err = rows.Close(); err != nil {
		return 0, false, err
	}
	if len(ids) == 0 {
		if run.DeletedCount != run.TargetCount {
			return 0, false, ErrRequestCleanupChanged
		}
		return 0, true, tx.Commit()
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	for _, statement := range []string{
		`delete from usage_monitoring_event_projection_v1 where event_id in (` + placeholders + `)`,
		`delete from usage_cache_accounting_v2_changes where event_id in (` + placeholders + `)`,
		`delete from usage_event_identity_ledger where raw_event_id in (` + placeholders + `)`,
		`delete from usage_events where id in (` + placeholders + `)`,
	} {
		if _, err = tx.ExecContext(ctx, statement, ids...); err != nil {
			return 0, false, err
		}
	}
	lastID := ids[len(ids)-1].(int64)
	_, err = tx.ExecContext(ctx, `update usage_request_cleanup_runs set status='deleting',
		last_deleted_event_id=?, deleted_count=deleted_count+?, updated_at_ms=? where id=?`,
		lastID, len(ids), time.Now().UnixMilli(), id)
	if err != nil {
		return 0, false, err
	}
	return len(ids), false, tx.Commit()
}

func (s *Store) FinalizeRequestCleanup(ctx context.Context, id string) (RequestCleanupRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RequestCleanupRun{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `update usage_request_cleanup_runs set updated_at_ms=? where id=?`, time.Now().UnixMilli(), id); err != nil {
		return RequestCleanupRun{}, err
	}
	run, err := readRequestCleanupRun(tx.QueryRowContext(ctx, `select `+requestCleanupRunColumns+` from usage_request_cleanup_runs where id=?`, id))
	if err != nil {
		return RequestCleanupRun{}, err
	}
	if run.Status != "deleting" && run.Status != "queued" {
		return RequestCleanupRun{}, ErrRequestCleanupState
	}
	if run.DeletedCount != run.TargetCount {
		return RequestCleanupRun{}, ErrRequestCleanupChanged
	}
	var remaining, archiveRows, latestID int64
	if err = tx.QueryRowContext(ctx, `select count(*) from usage_events where timestamp_ms<? and id<=?`, run.CutoffTimestampMS, run.TargetEventID).Scan(&remaining); err != nil {
		return RequestCleanupRun{}, err
	}
	if remaining != 0 {
		return RequestCleanupRun{}, ErrRequestCleanupChanged
	}
	if err = tx.QueryRowContext(ctx, `select count(*) from usage_archive_runs`).Scan(&archiveRows); err != nil {
		return RequestCleanupRun{}, err
	}
	if archiveRows != 0 {
		return RequestCleanupRun{}, ErrRequestCleanupUnavailable
	}
	if err = tx.QueryRowContext(ctx, `select coalesce(max(id),0) from usage_events`).Scan(&latestID); err != nil {
		return RequestCleanupRun{}, err
	}
	for _, table := range []string{
		"usage_account_model_rollups", "usage_dashboard_hourly_rollups", "usage_hourly_aggregate_v1",
		"usage_pricing_hourly_rollups_v1", "usage_pricing_account_rollups_v1",
		"usage_monitoring_account_daily_rollups_v1", "usage_monitoring_api_key_daily_rollups_v1",
		"usage_monitoring_selector_daily_rollups_v1", "usage_monitoring_header_latest_v1",
		"usage_codex_legacy_identity_evidence_v1",
	} {
		if _, err = tx.ExecContext(ctx, `delete from `+table); err != nil {
			return RequestCleanupRun{}, fmt.Errorf("clear %s: %w", table, err)
		}
	}
	// 保留近期请求的哈希去重身份，但取消其“已计入汇总”标记；否则清空聚合表后
	// aggregate worker 会跳过这些仍存在的请求，造成统计永久归零。
	if _, err = tx.ExecContext(ctx, `update usage_event_identity_ledger
		set aggregate_schema_version=0, aggregate_structure_revision=''
		where raw_event_id is not null`); err != nil {
		return RequestCleanupRun{}, err
	}
	for _, statement := range []string{
		`delete from usage_rollup_checkpoints where name in ('account_history','dashboard_hourly')`,
		`delete from usage_rollup_rebuild_state where name in ('account_history','dashboard_hourly')`,
		`update usage_hourly_aggregate_state set status='pending',backfill_last_event_id=0,coverage_event_id=0,
			target_event_id=?,processed_events=0,min_bucket_ms=null,max_bucket_ms=null,updated_at_ms=0,
			finished_at_ms=null,last_error=null`,
		`update usage_pricing_rollup_state set status='clearing',backfill_last_event_id=0,coverage_event_id=0,
			target_event_id=?,processed_events=0,min_bucket_ms=null,max_bucket_ms=null,updated_at_ms=0,
			finished_at_ms=null,last_error=null`,
		`update usage_monitoring_rollup_state set status='clearing',backfill_last_event_id=0,coverage_event_id=0,
			target_event_id=?,processed_events=0,updated_at_ms=0,finished_at_ms=null,last_error=null
			where rollup_name in ('stats_v1','metadata_v1','codex_legacy_identity_v1')`,
	} {
		var execArgs []any
		if strings.Contains(statement, "target_event_id=?") {
			execArgs = []any{latestID}
		}
		if _, err = tx.ExecContext(ctx, statement, execArgs...); err != nil {
			return RequestCleanupRun{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `update usage_request_cleanup_runs set status='rebuilding',reset_done=1,
		rebuild_target_event_id=?,updated_at_ms=? where id=?`, latestID, time.Now().UnixMilli(), id); err != nil {
		return RequestCleanupRun{}, err
	}
	if err = tx.Commit(); err != nil {
		return RequestCleanupRun{}, err
	}
	run.Status = "rebuilding"
	run.ResetDone = true
	run.RebuildTargetID = latestID
	return run, nil
}

func (s *Store) RequestCleanupRebuildReady(ctx context.Context, targetID int64, aggregateEnabled bool) (bool, error) {
	var unready int64
	queries := []string{
		`select count(*) from usage_rollup_checkpoints where name in ('account_history','dashboard_hourly') and last_event_id>=?`,
		`select count(*) from usage_monitoring_rollup_state where rollup_name in ('stats_v1','metadata_v1','codex_legacy_identity_v1','projection_v1') and status in ('ready','catching_up') and coverage_event_id>=?`,
		`select count(*) from usage_pricing_rollup_state where status in ('ready','catching_up') and coverage_event_id>=?`,
	}
	wants := []int64{2, 4, 1}
	if aggregateEnabled {
		queries = append(queries, `select count(*) from usage_hourly_aggregate_state
		where status in ('ready','catching_up') and coverage_event_id>=?`)
		wants = append(wants, 1)
	}
	for i, query := range queries {
		if err := s.db.QueryRowContext(ctx, query, targetID).Scan(&unready); err != nil {
			return false, err
		}
		if unready != wants[i] {
			return false, nil
		}
	}
	return true, nil
}

func (s *Store) SetRequestCleanupState(ctx context.Context, id, status, errorCode string) error {
	if status != "completed" && status != "failed" {
		return ErrRequestCleanupState
	}
	result, err := s.db.ExecContext(ctx, `update usage_request_cleanup_runs set status=?,error_code=?,updated_at_ms=?
		where id=? and status in ('queued','deleting','rebuilding')`, status, errorCode, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrRequestCleanupState
	}
	return nil
}

// 清理或重建期间阻止统计接口返回不完整的派生数据；部分删除后失败也保持不可读。
func (s *Store) RequestCleanupReadReady(ctx context.Context) (bool, error) {
	var blocked bool
	err := s.db.QueryRowContext(ctx, `select exists(select 1 from usage_request_cleanup_runs
		where status in ('deleting','rebuilding') or (status='failed' and deleted_count>0) limit 1)`).Scan(&blocked)
	return !blocked, err
}

func (s *Store) ResumeRequestCleanup(ctx context.Context, id string) (RequestCleanupRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RequestCleanupRun{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `update usage_request_cleanup_runs set updated_at_ms=updated_at_ms where id=?`, id); err != nil {
		return RequestCleanupRun{}, err
	}
	run, err := readRequestCleanupRun(tx.QueryRowContext(ctx, `select `+requestCleanupRunColumns+` from usage_request_cleanup_runs where id=?`, id))
	if err != nil {
		return RequestCleanupRun{}, err
	}
	if run.Status != "failed" || run.DeletedCount == 0 {
		return RequestCleanupRun{}, ErrRequestCleanupState
	}
	status := "deleting"
	if run.ResetDone {
		status = "rebuilding"
	}
	if _, err = tx.ExecContext(ctx, `update usage_request_cleanup_runs set status=?,error_code='',updated_at_ms=? where id=?`, status, time.Now().UnixMilli(), id); err != nil {
		return RequestCleanupRun{}, err
	}
	if err = tx.Commit(); err != nil {
		return RequestCleanupRun{}, err
	}
	run.Status = status
	run.ErrorCode = ""
	return run, nil
}
