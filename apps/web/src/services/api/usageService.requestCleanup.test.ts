//
//  usageService.requestCleanup.test.ts
//  CPA-Manager-Plus
//
//  Created by [huang wei] on 2026/10/22.
//  功能描述：验证请求清理状态发现与已知任务查询的错误语义。
//

import axios, { AxiosError, AxiosHeaders, type AxiosResponse } from 'axios';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { usageServiceApi } from './usageService';

const get = vi.spyOn(axios, 'get');
const post = vi.spyOn(axios, 'post');
const notFoundError = (): AxiosError => {
  const response: AxiosResponse = {
    data: { error: 'request cleanup run not found' },
    status: 404,
    statusText: 'Not Found',
    headers: {},
    config: { headers: new AxiosHeaders() },
  };
  return new AxiosError('Not Found', 'ERR_BAD_REQUEST', response.config, undefined, response);
};

beforeEach(() => {
  get.mockReset();
  post.mockReset();
});

describe('request cleanup status', () => {
  it('omits run_id when discovering a task and treats 404 as no prior task', async () => {
    const signal = new AbortController().signal;
    get.mockImplementation(async (url) => {
      if (String(url).includes('/request-cleanup/status')) throw notFoundError();
      return { data: null };
    });

    await expect(
      usageServiceApi.getRequestCleanupStatus(
        'http://manager.local:18317/',
        null,
        'admin-key',
        signal
      )
    ).resolves.toBeNull();
    expect(get).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/usage/request-cleanup/status',
      expect.objectContaining({
        headers: { Authorization: 'Bearer admin-key' },
        params: undefined,
        signal,
      })
    );
  });

  it('preserves 404 for a known task ID so the caller can recover a stale session', async () => {
    get.mockImplementation(async (url) => {
      if (String(url).includes('/request-cleanup/status')) throw notFoundError();
      return { data: null };
    });

    await expect(
      usageServiceApi.getRequestCleanupStatus('http://manager.local:18317', 'old-run', 'admin-key')
    ).rejects.toMatchObject({ status: 404 });
    expect(get).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/usage/request-cleanup/status',
      expect.objectContaining({ params: { run_id: 'old-run' } })
    );
  });

  it('sends only fields accepted by the execute endpoint', async () => {
    post.mockImplementation(async (url) => ({
      data: String(url).includes('/request-cleanup/execute')
        ? { run_id: 'new-run', status: 'queued' }
        : null,
    }));

    await usageServiceApi.executeRequestCleanup(
      'http://manager.local:18317',
      {
        cutoff_timestamp_ms: 1_700_000_000_000,
        target_event_id: 50,
        event_count: 3,
        preview_token: 'preview-token',
        expires_at_ms: 1_700_000_060_000,
      },
      'admin-key'
    );

    expect(post).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/usage/request-cleanup/execute',
      {
        cutoff_timestamp_ms: 1_700_000_000_000,
        target_event_id: 50,
        event_count: 3,
        preview_token: 'preview-token',
        confirmation: 'DELETE_REQUEST_DATA',
      },
      expect.objectContaining({ headers: { Authorization: 'Bearer admin-key' } })
    );
  });
});
