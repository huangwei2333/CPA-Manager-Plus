//
//  authFileListSnapshot.ts
//  CPA-Manager-Plus
//
//  Created by huang wei on 2026/10/22.
//  功能描述：在同一连接的页面切换期间暂存凭证列表。
//

import type { AuthFileItem } from '@/types';

// 仅在当前浏览器进程内保留最近一次连接的列表，页面切换时可先显示旧数据。
// 不持久化凭证元数据；连接切换或登出后丢弃，避免跨账号展示。
let latestSnapshot: { connectionFingerprint: string; files: AuthFileItem[] } | null = null;

export const readAuthFileListSnapshot = (connectionFingerprint: string): AuthFileItem[] | null => {
  if (!connectionFingerprint || latestSnapshot?.connectionFingerprint !== connectionFingerprint) {
    latestSnapshot = null;
    return null;
  }
  return [...latestSnapshot.files];
};

export const saveAuthFileListSnapshot = (
  connectionFingerprint: string,
  files: AuthFileItem[]
): void => {
  if (!connectionFingerprint) return;
  latestSnapshot = { connectionFingerprint, files: [...files] };
};

export const clearAuthFileListSnapshot = (): void => {
  latestSnapshot = null;
};
