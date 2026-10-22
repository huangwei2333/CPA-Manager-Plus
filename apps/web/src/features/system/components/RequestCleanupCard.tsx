//
//  RequestCleanupCard.tsx
//  CPA-Manager-Plus
//
//  Created by [huang wei] on 2026/10/22.
//  功能描述：在系统信息页预览并手动清理指定时间之前的请求数据。
//

import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { useNotificationStore } from '@/stores';
import { formatDateTime } from '@/utils/format';
import styles from './RequestCleanupCard.module.scss';

export interface RequestCleanupPreview {
  cutoffTimestampMs: number;
  targetEventId: number;
  eventCount: number;
  previewToken: string;
  expiresAtMs: number;
}

export interface RequestCleanupStatus {
  runId: string;
  status: 'queued' | 'deleting' | 'rebuilding' | 'completed' | 'failed';
  targetCount: number;
  deletedCount: number;
  rebuildStatus: 'pending' | 'rebuilding' | 'ready' | 'failed';
  errorCode?: string;
}

interface RequestCleanupCardProps {
  isAvailable: boolean;
  storageKey: string;
  onPreview: (cutoffTimestampMs: number, signal: AbortSignal) => Promise<RequestCleanupPreview>;
  onExecute: (preview: RequestCleanupPreview, signal: AbortSignal) => Promise<string>;
  onStatus: (runId: string | null, signal: AbortSignal) => Promise<RequestCleanupStatus | null>;
  onResume: (runId: string, signal: AbortSignal) => Promise<void>;
  onCompleted?: () => void;
}

const toLocalDateTimeValue = (timestampMs: number): string => {
  const date = new Date(timestampMs);
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(timestampMs - offset).toISOString().slice(0, 16);
};

const parseCutoff = (value: string): number | null => {
  if (!value) return null;
  const timestampMs = new Date(value).getTime();
  return Number.isFinite(timestampMs) && timestampMs > 0 && timestampMs < Date.now()
    ? timestampMs
    : null;
};

const readStoredRunId = (key: string): { runId: string | null; storageAvailable: boolean } => {
  try {
    if (typeof globalThis.sessionStorage === 'undefined') {
      return { runId: null, storageAvailable: false };
    }
    return { runId: globalThis.sessionStorage.getItem(key) || null, storageAvailable: true };
  } catch {
    return { runId: null, storageAvailable: false };
  }
};

export function RequestCleanupCard({
  isAvailable,
  storageKey,
  onPreview,
  onExecute,
  onStatus,
  onResume,
  onCompleted,
}: RequestCleanupCardProps) {
  const { t, i18n } = useTranslation();
  const { showConfirmation, showNotification, hideConfirmation } = useNotificationStore();
  const [cutoffInput, setCutoffInput] = useState('');
  const [preview, setPreview] = useState<RequestCleanupPreview | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [executing, setExecuting] = useState(false);
  const [previewError, setPreviewError] = useState('');
  const [executeError, setExecuteError] = useState('');
  const [isCompleted, setIsCompleted] = useState(false);
  const [completedFromDiscovery, setCompletedFromDiscovery] = useState(false);
  const [jobStatus, setJobStatus] = useState<RequestCleanupStatus | null>(null);
  const [pollRevision, setPollRevision] = useState(0);
  const sessionKey = `cpamp:request-cleanup:${storageKey}`;
  const [storedRun] = useState(() => readStoredRunId(sessionKey));
  const [runId, setRunId] = useState<string | null>(storedRun.runId);
  const [storageAvailable, setStorageAvailable] = useState(storedRun.storageAvailable);
  const [discoveryDone, setDiscoveryDone] = useState(false);
  const generationRef = useRef(0);
  const previewControllerRef = useRef<AbortController | null>(null);
  const executionControllerRef = useRef<AbortController | null>(null);
  const confirmationRef = useRef<{ onConfirm: () => void | Promise<void> } | null>(null);
  const mountedRef = useRef(true);
  const cutoffTimestampMs = parseCutoff(cutoffInput);
  const hasCurrentPreview =
    preview !== null &&
    cutoffTimestampMs !== null &&
    preview.cutoffTimestampMs === cutoffTimestampMs &&
    preview.expiresAtMs > Date.now();
  const needsResume =
    runId !== null && jobStatus?.status === 'failed' && jobStatus.deletedCount > 0;
  const hasFinishedDeleting =
    jobStatus !== null &&
    jobStatus.targetCount > 0 &&
    jobStatus.deletedCount >= jobStatus.targetCount;
  const hasFailedRebuild =
    jobStatus?.status === 'failed' &&
    (jobStatus.errorCode === 'request_cleanup_rebuild_failed' ||
      (jobStatus.errorCode === 'request_cleanup_failed' && hasFinishedDeleting));
  const showDeletionProgress =
    jobStatus !== null &&
    (jobStatus.status === 'queued' ||
      jobStatus.status === 'deleting' ||
      (jobStatus.status === 'failed' && !hasFinishedDeleting));
  const blocksNewRun =
    (runId === null && !discoveryDone) ||
    (runId !== null && (jobStatus === null || jobStatus.status !== 'failed' || needsResume));
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'local';

  const saveRunId = useCallback(
    (value: string | null) => {
      try {
        if (typeof globalThis.sessionStorage === 'undefined')
          throw new Error('sessionStorage unavailable');
        if (value) globalThis.sessionStorage.setItem(sessionKey, value);
        else globalThis.sessionStorage.removeItem(sessionKey);
      } catch {
        setStorageAvailable(false);
      }
    },
    [sessionKey]
  );

  const revokeConfirmation = () => {
    if (
      confirmationRef.current &&
      useNotificationStore.getState().confirmation.options === confirmationRef.current
    ) {
      hideConfirmation();
    }
    confirmationRef.current = null;
  };

  const invalidatePreview = () => {
    generationRef.current += 1;
    previewControllerRef.current?.abort();
    previewControllerRef.current = null;
    setPreview(null);
    setPreviewLoading(false);
    setPreviewError('');
    setExecuteError('');
    revokeConfirmation();
  };

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      generationRef.current += 1;
      previewControllerRef.current?.abort();
      revokeConfirmation();
    };
    // 页面卸载时撤销只属于该组件的全局确认框。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (isAvailable) return;
    invalidatePreview();
    // 权限能力变化必须使未提交的预览和确认失效。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isAvailable]);

  useEffect(() => {
    if (!isAvailable || runId !== null || discoveryDone) return;
    const controller = new AbortController();
    let timeout: ReturnType<typeof setTimeout> | null = null;
    let active = true;
    const discover = async () => {
      try {
        const status = await onStatus(null, controller.signal);
        if (!active || controller.signal.aborted) return;
        if (status) {
          if (
            !status.runId ||
            !Number.isSafeInteger(status.targetCount) ||
            !Number.isSafeInteger(status.deletedCount)
          ) {
            throw new Error(t('system_info.request_cleanup_invalid_response'));
          }
          if (status.status === 'completed' && status.rebuildStatus === 'ready') {
            setJobStatus(status);
            setIsCompleted(true);
            setCompletedFromDiscovery(true);
            setDiscoveryDone(true);
            setExecuteError('');
            return;
          }
          saveRunId(status.runId);
          setJobStatus(status);
          setRunId(status.runId);
        }
        setDiscoveryDone(true);
        setExecuteError('');
      } catch (error) {
        if (!active || controller.signal.aborted) return;
        setExecuteError(
          error instanceof Error ? error.message : t('system_info.request_cleanup_status_error')
        );
        timeout = setTimeout(() => void discover(), 2_000);
      }
    };
    void discover();
    return () => {
      active = false;
      controller.abort();
      if (timeout !== null) clearTimeout(timeout);
    };
  }, [isAvailable, runId, discoveryDone, onStatus, saveRunId, t]);

  useEffect(() => {
    if (!isAvailable || !runId) return;
    const controller = new AbortController();
    let timeout: ReturnType<typeof setTimeout> | null = null;
    let active = true;
    const poll = async () => {
      try {
        const status = await onStatus(runId, controller.signal);
        if (!active || controller.signal.aborted) return;
        if (
          !status ||
          status.runId !== runId ||
          !Number.isSafeInteger(status.targetCount) ||
          !Number.isSafeInteger(status.deletedCount)
        ) {
          throw new Error(t('system_info.request_cleanup_invalid_response'));
        }
        setJobStatus(status);
        setExecuteError('');
        if (status.status === 'completed' && status.rebuildStatus === 'ready') {
          saveRunId(null);
          setDiscoveryDone(true);
          setRunId(null);
          setPreview(null);
          setIsCompleted(true);
          setCompletedFromDiscovery(false);
          showNotification(t('system_info.request_cleanup_success'), 'success');
          onCompleted?.();
          return;
        }
        if (status.status === 'failed') {
          const isRebuildFailure =
            status.errorCode === 'request_cleanup_rebuild_failed' ||
            (status.errorCode === 'request_cleanup_failed' &&
              status.targetCount > 0 &&
              status.deletedCount >= status.targetCount);
          const isDeletionFailure = status.errorCode === 'request_cleanup_delete_failed';
          setExecuteError(
            t('system_info.request_cleanup_job_failed', {
              code: isRebuildFailure
                ? t('system_info.request_cleanup_rebuild_failure_reason')
                : isDeletionFailure
                  ? t('system_info.request_cleanup_delete_failure_reason')
                  : status.errorCode || t('system_info.request_cleanup_unknown_error'),
            })
          );
          return;
        }
      } catch (error) {
        if (!active || controller.signal.aborted) return;
        if (
          typeof error === 'object' &&
          error !== null &&
          'status' in error &&
          error.status === 404
        ) {
          saveRunId(null);
          setRunId(null);
          setJobStatus(null);
          setDiscoveryDone(false);
          return;
        }
        setExecuteError(
          error instanceof Error ? error.message : t('system_info.request_cleanup_status_error')
        );
      }
      if (active && !controller.signal.aborted) timeout = setTimeout(() => void poll(), 2_000);
    };
    void poll();
    return () => {
      active = false;
      controller.abort();
      if (timeout !== null) clearTimeout(timeout);
    };
  }, [isAvailable, runId, pollRevision, onStatus, onCompleted, saveRunId, showNotification, t]);

  useEffect(() => {
    if (!preview) return;
    const remaining = preview.expiresAtMs - Date.now();
    if (remaining <= 0) {
      setPreview(null);
      revokeConfirmation();
      return;
    }
    const timeout = setTimeout(() => {
      setPreview(null);
      revokeConfirmation();
    }, remaining);
    return () => clearTimeout(timeout);
    // 预览过期时失效确认框；过期时间由服务端决定。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [preview]);

  const handlePreview = async () => {
    if (!isAvailable || executing || blocksNewRun || cutoffTimestampMs === null) return;
    const generation = ++generationRef.current;
    previewControllerRef.current?.abort();
    const controller = new AbortController();
    previewControllerRef.current = controller;
    setPreview(null);
    setPreviewError('');
    setExecuteError('');
    setPreviewLoading(true);
    try {
      const result = await onPreview(cutoffTimestampMs, controller.signal);
      if (generation !== generationRef.current || controller.signal.aborted || !mountedRef.current)
        return;
      if (
        result.cutoffTimestampMs !== cutoffTimestampMs ||
        !Number.isSafeInteger(result.targetEventId) ||
        !Number.isSafeInteger(result.eventCount) ||
        result.eventCount < 0 ||
        !result.previewToken ||
        !Number.isSafeInteger(result.expiresAtMs) ||
        result.expiresAtMs <= Date.now()
      ) {
        throw new Error(t('system_info.request_cleanup_invalid_response'));
      }
      setPreview(result);
      setIsCompleted(false);
      setCompletedFromDiscovery(false);
    } catch (error) {
      if (generation !== generationRef.current || controller.signal.aborted || !mountedRef.current)
        return;
      setPreviewError(
        typeof error === 'object' &&
          error !== null &&
          'code' in error &&
          error.code === 'request_cleanup_no_events'
          ? t('system_info.request_cleanup_no_events')
          : error instanceof Error
            ? error.message
            : t('system_info.request_cleanup_preview_error')
      );
    } finally {
      if (generation === generationRef.current && mountedRef.current) {
        setPreviewLoading(false);
        previewControllerRef.current = null;
      }
    }
  };

  const handleConfirm = () => {
    if (
      !isAvailable ||
      executing ||
      blocksNewRun ||
      !hasCurrentPreview ||
      !preview ||
      preview.eventCount === 0
    )
      return;
    const confirmedPreview = preview;
    const confirmedGeneration = generationRef.current;
    const cutoffLabel = formatDateTime(new Date(preview.cutoffTimestampMs), i18n.language);
    const countLabel = preview.eventCount.toLocaleString(i18n.language);
    const quotaWarning = t('system_info.request_cleanup_quota_warning');
    const confirmation = {
      title: t('system_info.request_cleanup_confirm_title'),
      message: t('system_info.request_cleanup_confirm_message', {
        count: countLabel,
        cutoff: cutoffLabel,
        timezone,
        warning: quotaWarning,
      }),
      variant: 'danger' as const,
      confirmText: t('common.continue', { defaultValue: '继续' }),
      secondConfirmation: {
        title: t('system_info.request_cleanup_second_title'),
        message: t('system_info.request_cleanup_second_message', {
          count: countLabel,
          cutoff: cutoffLabel,
          timezone,
          warning: quotaWarning,
        }),
        variant: 'danger' as const,
        confirmText: t('system_info.request_cleanup_execute'),
      },
      onConfirm: async () => {
        if (
          !mountedRef.current ||
          !isAvailable ||
          confirmedGeneration !== generationRef.current ||
          confirmedPreview.expiresAtMs <= Date.now() ||
          parseCutoff(cutoffInput) !== confirmedPreview.cutoffTimestampMs
        ) {
          return;
        }
        const controller = new AbortController();
        executionControllerRef.current = controller;
        setExecuting(true);
        setExecuteError('');
        try {
          const acceptedRunId = await onExecute(confirmedPreview, controller.signal);
          if (
            controller.signal.aborted ||
            !mountedRef.current ||
            confirmedGeneration !== generationRef.current
          )
            return;
          if (!acceptedRunId) throw new Error(t('system_info.request_cleanup_invalid_response'));
          saveRunId(acceptedRunId);
          setPreview(null);
          setJobStatus(null);
          setRunId(acceptedRunId);
        } catch (error) {
          if (
            controller.signal.aborted ||
            !mountedRef.current ||
            confirmedGeneration !== generationRef.current
          )
            return;
          setPreview(null);
          setDiscoveryDone(false);
          setExecuteError(
            error instanceof Error ? error.message : t('system_info.request_cleanup_error')
          );
        } finally {
          if (mountedRef.current) setExecuting(false);
          if (executionControllerRef.current === controller) executionControllerRef.current = null;
        }
      },
    };
    confirmationRef.current = confirmation;
    showConfirmation(confirmation);
  };

  const handleResume = async () => {
    if (!isAvailable || !needsResume || !runId || executing) return;
    const generation = generationRef.current;
    const controller = new AbortController();
    executionControllerRef.current = controller;
    setExecuting(true);
    setExecuteError('');
    try {
      await onResume(runId, controller.signal);
      if (controller.signal.aborted || !mountedRef.current || generation !== generationRef.current)
        return;
      setJobStatus(null);
      setPollRevision((value) => value + 1);
    } catch (error) {
      if (controller.signal.aborted || !mountedRef.current || generation !== generationRef.current)
        return;
      setExecuteError(
        error instanceof Error ? error.message : t('system_info.request_cleanup_error')
      );
    } finally {
      if (mountedRef.current) setExecuting(false);
      if (executionControllerRef.current === controller) executionControllerRef.current = null;
    }
  };

  return (
    <Card title={t('system_info.request_cleanup_title')} className={styles.card}>
      <p className={styles.description}>{t('system_info.request_cleanup_description')}</p>
      <p className={styles.notice}>
        <strong>{t('system_info.request_cleanup_irreversible')}</strong>{' '}
        {t('system_info.request_cleanup_preserved')}{' '}
        {t('system_info.request_cleanup_quota_warning')}
      </p>
      <div className={styles.controls}>
        <Input
          type="datetime-local"
          label={t('system_info.request_cleanup_cutoff')}
          hint={t('system_info.request_cleanup_timezone', { timezone })}
          value={cutoffInput}
          max={toLocalDateTimeValue(Date.now())}
          disabled={!isAvailable || executing || blocksNewRun}
          error={
            cutoffInput && cutoffTimestampMs === null
              ? t('system_info.request_cleanup_invalid_cutoff')
              : undefined
          }
          onChange={(event) => {
            invalidatePreview();
            setCutoffInput(event.target.value);
          }}
        />
        <div className={styles.actions}>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            disabled={!isAvailable || cutoffTimestampMs === null || executing || blocksNewRun}
            loading={previewLoading}
            onClick={() => void handlePreview()}
          >
            {t('system_info.request_cleanup_preview')}
          </Button>
          <Button
            type="button"
            variant="danger"
            size="sm"
            disabled={
              !isAvailable ||
              blocksNewRun ||
              !hasCurrentPreview ||
              preview?.eventCount === 0 ||
              previewLoading
            }
            loading={executing}
            onClick={handleConfirm}
          >
            {t('system_info.request_cleanup_execute')}
          </Button>
          {needsResume ? (
            <Button
              type="button"
              variant="secondary"
              size="sm"
              loading={executing}
              onClick={() => void handleResume()}
            >
              {t('system_info.request_cleanup_resume')}
            </Button>
          ) : null}
        </div>
      </div>
      <div className={styles.feedback} aria-live="polite" aria-busy={previewLoading || executing}>
        {!storageAvailable ? (
          <p role="alert">{t('system_info.request_cleanup_storage_unavailable')}</p>
        ) : null}
        {runId === null && !discoveryDone ? (
          <p>{t('system_info.request_cleanup_discovering')}</p>
        ) : null}
        {previewLoading ? <p>{t('system_info.request_cleanup_preview_loading')}</p> : null}
        {previewError ? <p role="alert">{previewError}</p> : null}
        {hasCurrentPreview && preview ? (
          <p>
            {preview.eventCount === 0
              ? t('system_info.request_cleanup_no_events')
              : t('system_info.request_cleanup_preview_summary', {
                  count: preview.eventCount.toLocaleString(i18n.language),
                  cutoff: formatDateTime(new Date(preview.cutoffTimestampMs), i18n.language),
                  timezone,
                })}
          </p>
        ) : null}
        {preview && !hasCurrentPreview ? (
          <p role="alert">{t('system_info.request_cleanup_preview_expired')}</p>
        ) : null}
        {runId && jobStatus ? (
          <div className={styles.progress} role="status">
            <span>
              {t(
                `system_info.request_cleanup_status_${hasFailedRebuild ? 'rebuild_failed' : jobStatus.status}`,
                {
                  deleted: jobStatus.deletedCount.toLocaleString(i18n.language),
                  total: jobStatus.targetCount.toLocaleString(i18n.language),
                }
              )}
            </span>
            {showDeletionProgress ? (
              <progress
                max={Math.max(jobStatus.targetCount, 1)}
                value={Math.max(0, Math.min(jobStatus.deletedCount, jobStatus.targetCount))}
                aria-label={t('system_info.request_cleanup_progress')}
              />
            ) : null}
            <details className={styles.taskDetails}>
              <summary>{t('system_info.request_cleanup_task_details')}</summary>
              <code className={styles.runId}>{runId}</code>
            </details>
          </div>
        ) : null}
        {executeError ? <p role="alert">{executeError}</p> : null}
        {executing ? <p>{t('system_info.request_cleanup_executing')}</p> : null}
        {isCompleted ? (
          <p role="status">
            {t(
              completedFromDiscovery
                ? 'system_info.request_cleanup_previous_success'
                : 'system_info.request_cleanup_success'
            )}
          </p>
        ) : null}
      </div>
    </Card>
  );
}
