//
//  accountsOverviewRouteSnapshot.ts
//  CPA-Manager-Plus
//
//  Created by huang wei on 2026/10/22.
//  功能描述：在同一 Manager 连接内暂存凭证概览的历史和窗口数据。
//

import type {
  MonitoringAccountHistoryItem,
  MonitoringAccountWindowUsageItem,
} from '@/services/api/usageService';
import type { UsageValueRow } from './usageValueRows';

export interface AccountHistoryRouteItem {
  item: MonitoringAccountHistoryItem;
  identitySignature: string;
}

export interface ListWindowUsageRouteContext {
  pageKeys: string[];
  identityByRowKey: Map<string, string>;
  asOfMs: number;
}

export interface AccountWindowUsageRouteContext {
  rowKey: string;
  identitySignature: string;
  asOfMs: number;
}

export interface AccountsOverviewRouteSnapshot {
  accountHistoryByRowKey: Map<string, AccountHistoryRouteItem>;
  listWindowUsageByKey: Map<string, MonitoringAccountWindowUsageItem>;
  listWindowUsageQueryContext: ListWindowUsageRouteContext | null;
  accountWindowUsageByKey: Map<string, MonitoringAccountWindowUsageItem>;
  accountWindowUsageQueryContext: AccountWindowUsageRouteContext | null;
  usageRows: UsageValueRow[];
  usageRowsIdentitySignature: string;
}

let latestSnapshot: { scopeKey: string; value: AccountsOverviewRouteSnapshot } | null = null;

const cloneSnapshot = (value: AccountsOverviewRouteSnapshot): AccountsOverviewRouteSnapshot => ({
  accountHistoryByRowKey: new Map(value.accountHistoryByRowKey),
  listWindowUsageByKey: new Map(value.listWindowUsageByKey),
  listWindowUsageQueryContext: value.listWindowUsageQueryContext
    ? {
        ...value.listWindowUsageQueryContext,
        pageKeys: [...value.listWindowUsageQueryContext.pageKeys],
        identityByRowKey: new Map(value.listWindowUsageQueryContext.identityByRowKey),
      }
    : null,
  accountWindowUsageByKey: new Map(value.accountWindowUsageByKey),
  accountWindowUsageQueryContext: value.accountWindowUsageQueryContext
    ? { ...value.accountWindowUsageQueryContext }
    : null,
  usageRows: [...value.usageRows],
  usageRowsIdentitySignature: value.usageRowsIdentitySignature,
});

export const readAccountsOverviewRouteSnapshot = (
  scopeKey: string
): AccountsOverviewRouteSnapshot | null => {
  if (!scopeKey.split('\u001f')[0] || latestSnapshot?.scopeKey !== scopeKey) {
    return null;
  }
  return cloneSnapshot(latestSnapshot.value);
};

export const saveAccountsOverviewRouteSnapshot = (
  scopeKey: string,
  value: AccountsOverviewRouteSnapshot
): void => {
  if (!scopeKey.split('\u001f')[0]) return;
  latestSnapshot = { scopeKey, value: cloneSnapshot(value) };
};

export const clearAccountsOverviewRouteSnapshot = (): void => {
  latestSnapshot = null;
};
