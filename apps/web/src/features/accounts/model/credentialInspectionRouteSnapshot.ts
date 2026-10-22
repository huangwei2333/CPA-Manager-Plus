//
//  credentialInspectionRouteSnapshot.ts
//  CPA-Manager-Plus
//
//  Created by huang wei on 2026/10/22.
//  功能描述：在同一连接的页面切换期间暂存凭证检查结果。
//

import type { CredentialInspectionSnapshot } from '@/features/monitoring/model/credentialInspectionSnapshot';

// 路由切换期间保留单个连接的最新检查结果；不写入浏览器持久存储。
let latestSnapshot: { scopeKey: string; snapshot: CredentialInspectionSnapshot } | null = null;

export const readCredentialInspectionRouteSnapshot = (
  scopeKey: string
): CredentialInspectionSnapshot | null => {
  if (!scopeKey.split('\u001f')[0] || latestSnapshot?.scopeKey !== scopeKey) {
    return null;
  }
  return latestSnapshot.snapshot;
};

export const saveCredentialInspectionRouteSnapshot = (
  scopeKey: string,
  snapshot: CredentialInspectionSnapshot
): void => {
  if (!scopeKey.split('\u001f')[0]) return;
  latestSnapshot = { scopeKey, snapshot };
};

export const clearCredentialInspectionRouteSnapshot = (): void => {
  latestSnapshot = null;
};
