import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import {
  IconGithub,
  IconBookOpen,
  IconExternalLink,
  IconCode,
  IconArrowUpFromLine,
} from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import {
  useAuthStore,
  useConfigStore,
  useNotificationStore,
  useModelsStore,
  useThemeStore,
} from '@/stores';
import { apiKeysApi } from '@/services/api/apiKeys';
import { usageServiceApi, type UsageServiceStatus } from '@/services/api/usageService';
import { isDemoMode } from '@/features/demo/demoMode';
import { sha256Hex } from '@/utils/apiKeyHash';
import { clearAccountsOverviewRouteSnapshot } from '@/features/accounts/model/accountsOverviewRouteSnapshot';
import {
  buildUsageHeaderSnapshotScopeKey,
  useUsageHeaderSnapshotStore,
} from '@/stores/useUsageHeaderSnapshotStore';
import { classifyModels, modelDisplayLabel } from '@/utils/models';
import { STORAGE_KEY_AUTH, STORAGE_KEY_QUOTA_CACHE } from '@/utils/constants';
import iconGemini from '@/assets/icons/gemini.svg';
import iconClaude from '@/assets/icons/claude.svg';
import iconDevinLight from '@/assets/icons/devin.svg';
import iconDevinDark from '@/assets/icons/devin-dark.svg';
import iconOpenaiLight from '@/assets/icons/openai-light.svg';
import iconOpenaiDark from '@/assets/icons/openai-dark.svg';
import iconQwen from '@/assets/icons/qwen.svg';
import iconKimiLight from '@/assets/icons/kimi-light.svg';
import iconKimiDark from '@/assets/icons/kimi-dark.svg';
import iconGlm from '@/assets/icons/glm.svg';
import iconGrok from '@/assets/icons/grok.svg';
import iconGrokDark from '@/assets/icons/grok-dark.svg';
import iconDeepseek from '@/assets/icons/deepseek.svg';
import iconMinimax from '@/assets/icons/minimax.svg';
import iconMeta from '@/assets/icons/meta.svg';
import { DatabaseStatusCard } from './components/DatabaseStatusCard';
import { RequestCleanupCard, type RequestCleanupPreview } from './components/RequestCleanupCard';
import { useManagerUpdates } from './ManagerUpdates';
import styles from './SystemPage.module.scss';

// 同一已完成作业只失效一次，返回系统页不应重复清空新加载的概览。
let lastReconciledRequestCleanup: { scopeKey: string; runId: string } | null = null;

const MODEL_CATEGORY_ICONS: Record<string, string | { light: string; dark: string }> = {
  devin: { light: iconDevinLight, dark: iconDevinDark },
  muse: iconMeta,
  gpt: { light: iconOpenaiLight, dark: iconOpenaiDark },
  claude: iconClaude,
  gemini: iconGemini,
  qwen: iconQwen,
  kimi: { light: iconKimiLight, dark: iconKimiDark },
  glm: iconGlm,
  grok: { light: iconGrok, dark: iconGrokDark },
  deepseek: iconDeepseek,
  minimax: iconMinimax,
};

const DATABASE_STATUS_REFRESH_INTERVAL_MS = 60_000;

export function SystemPage() {
  const connectionScope = useAuthStore((state) =>
    JSON.stringify([state.apiBase, state.managementKey])
  );
  const { managerServiceBase } = usePanelFeatureAvailability();
  return <SystemPageContent key={`${connectionScope}:${managerServiceBase}`} />;
}

function SystemPageContent() {
  const { t, i18n } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const auth = useAuthStore();
  const featureAvailability = usePanelFeatureAvailability();
  const updates = useManagerUpdates();
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);

  const models = useModelsStore((state) => state.models);
  const modelsLoading = useModelsStore((state) => state.loading);
  const modelsError = useModelsStore((state) => state.error);
  const modelsCache = useModelsStore((state) => state.cache);
  const fetchModelsFromStore = useModelsStore((state) => state.fetchModels);

  const [modelStatus, setModelStatus] = useState<{
    type: 'success' | 'warning' | 'error' | 'muted';
    message: string;
  }>();

  const [databaseStatus, setDatabaseStatus] = useState<UsageServiceStatus | null>(null);
  const [databaseLoading, setDatabaseLoading] = useState(false);
  const [databaseError, setDatabaseError] = useState('');
  const databaseRequestId = useRef(0);
  const modelRequestId = useRef(0);
  useEffect(
    () => () => {
      databaseRequestId.current += 1;
      modelRequestId.current += 1;
    },
    []
  );

  const apiKeysCache = useRef<string[]>([]);

  const otherLabel = useMemo(
    () => (i18n.language?.toLowerCase().startsWith('zh') ? '其他' : 'Other'),
    [i18n.language]
  );
  const groupedModels = useMemo(() => classifyModels(models, { otherLabel }), [models, otherLabel]);
  const getIconForCategory = (categoryId: string): string | null => {
    const iconEntry = MODEL_CATEGORY_ICONS[categoryId];
    if (!iconEntry) return null;
    if (typeof iconEntry === 'string') return iconEntry;
    return resolvedTheme === 'dark' ? iconEntry.dark : iconEntry.light;
  };

  const normalizeApiKeyList = (input: unknown): string[] => {
    if (!Array.isArray(input)) return [];
    const seen = new Set<string>();
    const keys: string[] = [];

    input.forEach((item) => {
      const record =
        item !== null && typeof item === 'object' && !Array.isArray(item)
          ? (item as Record<string, unknown>)
          : null;
      const value =
        typeof item === 'string'
          ? item
          : record
            ? (record['api-key'] ?? record['apiKey'] ?? record.key ?? record.Key)
            : '';
      const trimmed = String(value ?? '').trim();
      if (!trimmed || seen.has(trimmed)) return;
      seen.add(trimmed);
      keys.push(trimmed);
    });

    return keys;
  };

  const resolveApiKeysForModels = useCallback(async () => {
    if (apiKeysCache.current.length) {
      return apiKeysCache.current;
    }

    const configKeys = normalizeApiKeyList(config?.apiKeys);
    if (configKeys.length) {
      apiKeysCache.current = configKeys;
      return configKeys;
    }

    try {
      const list = await apiKeysApi.list();
      const normalized = normalizeApiKeyList(list);
      if (normalized.length) {
        apiKeysCache.current = normalized;
      }
      return normalized;
    } catch (err) {
      console.warn('Auto loading API keys for models failed:', err);
      return [];
    }
  }, [config?.apiKeys]);

  const fetchModels = async ({ forceRefresh = false }: { forceRefresh?: boolean } = {}) => {
    const requestId = ++modelRequestId.current;
    if (auth.connectionStatus !== 'connected') {
      setModelStatus({
        type: 'warning',
        message: t('notification.connection_required'),
      });
      return;
    }

    if (!auth.apiBase) {
      showNotification(t('notification.connection_required'), 'warning');
      return;
    }

    if (forceRefresh) {
      apiKeysCache.current = [];
    }

    setModelStatus({ type: 'muted', message: t('system_info.models_loading') });
    try {
      const apiKeys = await resolveApiKeysForModels();
      if (requestId !== modelRequestId.current) return;
      const primaryKey = apiKeys[0];
      const list = await fetchModelsFromStore(auth.apiBase, primaryKey, forceRefresh);
      if (requestId !== modelRequestId.current) return;
      const hasModels = list.length > 0;
      setModelStatus({
        type: hasModels ? 'success' : 'warning',
        message: hasModels
          ? t('system_info.models_count', { count: list.length })
          : t('system_info.models_empty'),
      });
    } catch (err: unknown) {
      if (requestId !== modelRequestId.current) return;
      const message = err instanceof Error ? err.message : typeof err === 'string' ? err : '';
      const suffix = message ? `: ${message}` : '';
      const text = `${t('system_info.models_error')}${suffix}`;
      setModelStatus({ type: 'error', message: text });
    }
  };

  const refreshDatabaseStatus = useCallback(async () => {
    const requestId = databaseRequestId.current + 1;
    databaseRequestId.current = requestId;

    if (
      !featureAvailability.managerServiceAvailable ||
      !featureAvailability.managerServiceBase ||
      !auth.managementKey
    ) {
      setDatabaseStatus(null);
      setDatabaseError('');
      setDatabaseLoading(false);
      return;
    }

    setDatabaseLoading(true);
    setDatabaseError('');
    try {
      const status = await usageServiceApi.getStatus(
        featureAvailability.managerServiceBase,
        auth.managementKey
      );
      if (databaseRequestId.current !== requestId) return;
      setDatabaseStatus(status);
    } catch (err: unknown) {
      if (databaseRequestId.current !== requestId) return;
      setDatabaseError(err instanceof Error ? err.message : String(err));
    } finally {
      if (databaseRequestId.current === requestId) {
        setDatabaseLoading(false);
      }
    }
  }, [
    auth.managementKey,
    featureAvailability.managerServiceAvailable,
    featureAvailability.managerServiceBase,
  ]);

  const canUseRequestCleanup =
    !featureAvailability.checking &&
    featureAvailability.panelHostMode === 'manager_embedded' &&
    featureAvailability.managerServiceAvailable &&
    Boolean(featureAvailability.managerServiceBase && auth.managementKey) &&
    !(__DEMO_SITE__ && isDemoMode());
  const cleanupScopeKey = useMemo(
    () => sha256Hex(`${featureAvailability.managerServiceBase}\u0000${auth.managementKey ?? ''}`),
    [featureAvailability.managerServiceBase, auth.managementKey]
  );
  const previewRequestCleanup = useCallback(
    async (cutoffTimestampMs: number, signal: AbortSignal): Promise<RequestCleanupPreview> => {
      const result = await usageServiceApi.previewRequestCleanup(
        featureAvailability.managerServiceBase,
        cutoffTimestampMs,
        auth.managementKey,
        signal
      );
      return {
        cutoffTimestampMs: result.cutoff_timestamp_ms,
        targetEventId: result.target_event_id,
        eventCount: result.event_count,
        previewToken: result.preview_token,
        expiresAtMs: result.expires_at_ms,
      };
    },
    [featureAvailability.managerServiceBase, auth.managementKey]
  );
  const executeRequestCleanup = useCallback(
    async (preview: RequestCleanupPreview, signal: AbortSignal) => {
      const result = await usageServiceApi.executeRequestCleanup(
        featureAvailability.managerServiceBase,
        {
          cutoff_timestamp_ms: preview.cutoffTimestampMs,
          target_event_id: preview.targetEventId,
          event_count: preview.eventCount,
          preview_token: preview.previewToken,
          expires_at_ms: preview.expiresAtMs,
        },
        auth.managementKey,
        signal
      );
      return result.run_id;
    },
    [featureAvailability.managerServiceBase, auth.managementKey]
  );
  const getRequestCleanupStatus = useCallback(
    async (runId: string | null, signal: AbortSignal) => {
      const result = await usageServiceApi.getRequestCleanupStatus(
        featureAvailability.managerServiceBase,
        runId,
        auth.managementKey,
        signal
      );
      if (!result) return null;
      return {
        runId: result.run_id,
        status: result.status,
        targetCount: result.target_count,
        deletedCount: result.deleted_count,
        rebuildStatus: result.rebuild_status,
        errorCode: result.error_code,
      };
    },
    [featureAvailability.managerServiceBase, auth.managementKey]
  );
  const resumeRequestCleanup = useCallback(
    async (runId: string, signal: AbortSignal) => {
      await usageServiceApi.resumeRequestCleanup(
        featureAvailability.managerServiceBase,
        runId,
        auth.managementKey,
        signal
      );
    },
    [featureAvailability.managerServiceBase, auth.managementKey]
  );
  const handleRequestCleanupCompleted = useCallback(
    (runId: string) => {
      if (
        lastReconciledRequestCleanup?.scopeKey === cleanupScopeKey &&
        lastReconciledRequestCleanup.runId === runId
      )
        return;
      lastReconciledRequestCleanup = { scopeKey: cleanupScopeKey, runId };
      clearAccountsOverviewRouteSnapshot();
      const headerSnapshots = useUsageHeaderSnapshotStore.getState();
      if (
        headerSnapshots.scopeKey ===
        buildUsageHeaderSnapshotScopeKey(featureAvailability.managerServiceBase, auth.managementKey)
      ) {
        headerSnapshots.activateScope('');
      }
      void refreshDatabaseStatus();
    },
    [
      cleanupScopeKey,
      featureAvailability.managerServiceBase,
      auth.managementKey,
      refreshDatabaseStatus,
    ]
  );

  const handleClearLoginStorage = () => {
    showConfirmation({
      title: t('system_info.clear_login_title', { defaultValue: 'Clear Login Storage' }),
      message: t('system_info.clear_login_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: () => {
        auth.logout();
        if (typeof localStorage === 'undefined') return;
        const keysToRemove = [
          STORAGE_KEY_AUTH,
          STORAGE_KEY_QUOTA_CACHE,
          'isLoggedIn',
          'apiBase',
          'apiUrl',
          'managementKey',
        ];
        keysToRemove.forEach((key) => localStorage.removeItem(key));
        showNotification(t('notification.login_storage_cleared'), 'success');
      },
    });
  };

  useEffect(() => {
    fetchConfig().catch(() => {
      // ignore
    });
  }, [fetchConfig]);

  useEffect(() => {
    fetchModels();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auth.connectionStatus, auth.apiBase]);

  useEffect(() => {
    if (featureAvailability.checking) return;
    void refreshDatabaseStatus();
  }, [featureAvailability.checking, refreshDatabaseStatus]);

  useEffect(() => {
    if (!featureAvailability.managerServiceAvailable) return;
    const timer = window.setInterval(() => {
      void refreshDatabaseStatus();
    }, DATABASE_STATUS_REFRESH_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [featureAvailability.managerServiceAvailable, refreshDatabaseStatus]);

  useHeaderRefresh(refreshDatabaseStatus, featureAvailability.managerServiceAvailable);

  return (
    <div className={styles.container}>
      <div className={styles.content}>
        <Card
          title={t('system_info.quick_links_title')}
          extra={
            updates.available && (
              <Link to="/system/updates" className="btn btn-secondary btn-sm">
                <IconArrowUpFromLine size={16} aria-hidden="true" />
                {t('manager_updates.title')}
              </Link>
            )
          }
        >
          <p className={styles.sectionDescription}>{t('system_info.quick_links_desc')}</p>
          <div className={styles.quickLinks}>
            <a
              href="https://github.com/router-for-me/CLIProxyAPI"
              target="_blank"
              rel="noopener noreferrer"
              className={styles.linkCard}
            >
              <div className={`${styles.linkIcon} ${styles.github}`}>
                <IconGithub size={22} />
              </div>
              <div className={styles.linkContent}>
                <div className={styles.linkTitle}>
                  {t('system_info.link_main_repo')}
                  <IconExternalLink size={14} />
                </div>
                <div className={styles.linkDesc}>{t('system_info.link_main_repo_desc')}</div>
              </div>
            </a>

            <a
              href="https://github.com/seakee/CPA-Manager-Plus"
              target="_blank"
              rel="noopener noreferrer"
              className={styles.linkCard}
            >
              <div className={`${styles.linkIcon} ${styles.github}`}>
                <IconCode size={22} />
              </div>
              <div className={styles.linkContent}>
                <div className={styles.linkTitle}>
                  {t('system_info.link_webui_repo')}
                  <IconExternalLink size={14} />
                </div>
                <div className={styles.linkDesc}>{t('system_info.link_webui_repo_desc')}</div>
              </div>
            </a>

            <a
              href="https://seakee.github.io/CPA-Manager-Plus/docs/"
              target="_blank"
              rel="noopener noreferrer"
              className={styles.linkCard}
            >
              <div className={`${styles.linkIcon} ${styles.docs}`}>
                <IconBookOpen size={22} />
              </div>
              <div className={styles.linkContent}>
                <div className={styles.linkTitle}>
                  {t('system_info.link_docs')}
                  <IconExternalLink size={14} />
                </div>
                <div className={styles.linkDesc}>{t('system_info.link_docs_desc')}</div>
              </div>
            </a>
          </div>
        </Card>

        {featureAvailability.managerServiceAvailable && (
          <DatabaseStatusCard
            status={databaseStatus}
            loading={databaseLoading}
            error={databaseError}
            cleanup={
              canUseRequestCleanup ? (
                <RequestCleanupCard
                  key={cleanupScopeKey}
                  embedded
                  isAvailable={canUseRequestCleanup}
                  storageKey={cleanupScopeKey}
                  onPreview={previewRequestCleanup}
                  onExecute={executeRequestCleanup}
                  onStatus={getRequestCleanupStatus}
                  onResume={resumeRequestCleanup}
                  onCompleted={handleRequestCleanupCompleted}
                />
              ) : undefined
            }
          />
        )}

        <Card
          title={t('system_info.models_title')}
          extra={
            <Button
              variant="secondary"
              size="sm"
              onClick={() => fetchModels({ forceRefresh: true })}
              loading={modelsLoading}
            >
              {t('common.refresh')}
            </Button>
          }
        >
          <p className={styles.sectionDescription}>{t('system_info.models_desc')}</p>
          {modelStatus && (
            <div className={`status-badge ${modelStatus.type}`}>{modelStatus.message}</div>
          )}
          {modelsError && <div className="error-box">{modelsError}</div>}
          {modelsLoading && !modelsCache ? (
            <div className="hint">{t('common.loading')}</div>
          ) : !modelsCache ? null : models.length === 0 ? (
            <div className="hint">{t('system_info.models_empty')}</div>
          ) : (
            <div className="item-list">
              {groupedModels.map((group) => {
                const iconSrc = getIconForCategory(group.id);
                return (
                  <div key={group.id} className="item-row">
                    <div className="item-meta">
                      <div className={styles.groupTitle}>
                        {iconSrc && <img src={iconSrc} alt="" className={styles.groupIcon} />}
                        <span className="item-title">{group.label}</span>
                      </div>
                      <div className="item-subtitle">
                        {t('system_info.models_count', { count: group.items.length })}
                      </div>
                    </div>
                    <div className={styles.modelTags}>
                      {group.items.map((model) => {
                        const label = modelDisplayLabel(model);
                        return (
                          <span
                            key={`${model.name}-${label || 'default'}`}
                            className={styles.modelTag}
                            title={model.description || ''}
                          >
                            <span className={styles.modelName}>{model.name}</span>
                            {label && <span className={styles.modelAlias}>{label}</span>}
                          </span>
                        );
                      })}
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </Card>

        <Card title={t('system_info.clear_login_title')}>
          <p className={styles.sectionDescription}>{t('system_info.clear_login_desc')}</p>
          <div className={styles.clearLoginActions}>
            <Button variant="danger" onClick={handleClearLoginStorage}>
              {t('system_info.clear_login_button')}
            </Button>
          </div>
        </Card>
      </div>
    </div>
  );
}
