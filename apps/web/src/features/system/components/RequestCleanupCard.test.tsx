//
//  RequestCleanupCard.test.tsx
//  CPA-Manager-Plus
//
//  Created by [huang wei] on 2026/10/22.
//  功能描述：验证请求清理预览、确认及连接作用域失效行为。
//

import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import {
  RequestCleanupCard,
  type RequestCleanupPreview,
  type RequestCleanupStatus,
} from './RequestCleanupCard';

const mocks = vi.hoisted(() => ({
  onPreview:
    vi.fn<(cutoffTimestampMs: number, signal: AbortSignal) => Promise<RequestCleanupPreview>>(),
  onExecute: vi.fn<(preview: RequestCleanupPreview, signal: AbortSignal) => Promise<string>>(),
  onStatus:
    vi.fn<(runId: string | null, signal: AbortSignal) => Promise<RequestCleanupStatus | null>>(),
  onResume: vi.fn<(runId: string, signal: AbortSignal) => Promise<void>>(),
  onCompleted: vi.fn(),
  showConfirmation: vi.fn(),
  hideConfirmation: vi.fn(),
  showNotification: vi.fn(),
  confirmation: null as {
    onConfirm: () => void | Promise<void>;
    secondConfirmation?: { message: string };
  } | null,
}));

vi.mock('react-i18next', () => {
  const t = (key: string, values?: Record<string, unknown>) =>
    values ? `${key}:${Object.values(values).join('|')}` : key;
  return { useTranslation: () => ({ t, i18n: { language: 'en' } }) };
});

vi.mock('@/stores', () => {
  const useNotificationStore = Object.assign(
    () => ({
      showConfirmation: (options: typeof mocks.confirmation) => {
        mocks.confirmation = options;
        mocks.showConfirmation(options);
      },
      hideConfirmation: mocks.hideConfirmation,
      showNotification: mocks.showNotification,
    }),
    { getState: () => ({ confirmation: { options: mocks.confirmation } }) }
  );
  return { useNotificationStore };
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const firstCutoffInput = '2026-10-01T10:00';
const secondCutoffInput = '2026-10-02T10:00';
let renderer: ReactTestRenderer | undefined;

const clickButton = (variant: 'secondary' | 'danger') =>
  renderer!.root.findAllByType(Button).find((button) => button.props.variant === variant)!;

const changeCutoff = async (value: string) => {
  await act(async () => {
    renderer!.root.findByType('input').props.onChange({ target: { value } });
  });
};

const mount = async (isAvailable = true) => {
  await act(async () => {
    renderer = create(
      <RequestCleanupCard
        isAvailable={isAvailable}
        storageKey="manager-scope"
        onPreview={mocks.onPreview}
        onExecute={mocks.onExecute}
        onStatus={mocks.onStatus}
        onResume={mocks.onResume}
        onCompleted={mocks.onCompleted}
      />
    );
  });
};

beforeEach(() => {
  vi.setSystemTime(new Date('2026-10-22T12:00:00Z'));
  mocks.confirmation = null;
  mocks.onPreview.mockImplementation(async (cutoffTimestampMs) => ({
    cutoffTimestampMs,
    eventCount: 3,
    targetEventId: 40,
    previewToken: 'preview-token',
    expiresAtMs: Date.now() + 60_000,
  }));
  mocks.onExecute.mockResolvedValue('run-id');
  mocks.onStatus.mockImplementation(async (runId) =>
    runId === null
      ? null
      : {
          runId,
          status: 'completed',
          targetCount: 3,
          deletedCount: 3,
          rebuildStatus: 'ready',
        }
  );
  mocks.onResume.mockResolvedValue(undefined);
});

afterEach(async () => {
  if (renderer) await act(async () => renderer?.unmount());
  renderer = undefined;
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe('RequestCleanupCard', () => {
  it('shows irreversible and quota-cycle risks before either confirmation', async () => {
    await mount();

    expect(
      renderer!.root
        .findAllByType('strong')
        .some((node) => node.children.includes('system_info.request_cleanup_irreversible'))
    ).toBe(true);
    expect(
      renderer!.root
        .findAllByType('p')
        .some(
          (node) =>
            node.children.includes('system_info.request_cleanup_preserved') &&
            node.children.includes('system_info.request_cleanup_quota_warning')
        )
    ).toBe(true);
    expect(mocks.showConfirmation).not.toHaveBeenCalled();
  });

  it('does not show completed deletion as completed statistics while rebuilding', async () => {
    mocks.onStatus.mockImplementation(async (runId) => ({
      runId: runId ?? 'rebuilding-run',
      status: 'rebuilding',
      targetCount: 5,
      deletedCount: 5,
      rebuildStatus: 'rebuilding',
    }));
    await mount();

    expect(renderer!.root.findAllByType('progress')).toHaveLength(0);
    expect(
      renderer!.root
        .findAllByType('span')
        .some((node) =>
          node.children.join('').includes('system_info.request_cleanup_status_rebuilding')
        )
    ).toBe(true);
    expect(mocks.onCompleted).not.toHaveBeenCalled();
  });

  it('names a failed statistics rebuild and offers the same job for recovery', async () => {
    mocks.onStatus.mockImplementation(async (runId) => ({
      runId: runId ?? 'failed-rebuild-run',
      status: 'failed',
      targetCount: 5,
      deletedCount: 5,
      rebuildStatus: 'failed',
      errorCode: 'request_cleanup_rebuild_failed',
    }));
    await mount();

    expect(renderer!.root.findAllByType('progress')).toHaveLength(0);
    expect(
      renderer!.root
        .findAllByType('span')
        .some((node) =>
          node.children.join('').includes('system_info.request_cleanup_status_rebuild_failed')
        )
    ).toBe(true);
    expect(
      renderer!.root
        .findAllByProps({ role: 'alert' })
        .some((node) =>
          node.children.join('').includes('system_info.request_cleanup_rebuild_failure_reason')
        )
    ).toBe(true);
    expect(
      renderer!.root
        .findAllByType(Button)
        .some((button) => button.props.children === 'system_info.request_cleanup_resume')
    ).toBe(true);
    expect(mocks.onCompleted).not.toHaveBeenCalled();
  });

  it('recognizes the deployed legacy failure code after all requests were deleted', async () => {
    mocks.onStatus.mockImplementation(async (runId) => ({
      runId: runId ?? 'legacy-failed-run',
      status: 'failed',
      targetCount: 5,
      deletedCount: 5,
      rebuildStatus: 'failed',
      errorCode: 'request_cleanup_failed',
    }));
    await mount();

    expect(
      renderer!.root
        .findAllByType('span')
        .some((node) =>
          node.children.join('').includes('system_info.request_cleanup_status_rebuild_failed')
        )
    ).toBe(true);
    expect(
      renderer!.root
        .findAllByProps({ role: 'alert' })
        .some((node) =>
          node.children.join('').includes('system_info.request_cleanup_rebuild_failure_reason')
        )
    ).toBe(true);
  });

  it('does not mislabel a finalization failure as a statistics rebuild failure', async () => {
    mocks.onStatus.mockImplementation(async (runId) => ({
      runId: runId ?? 'delete-failed-run',
      status: 'failed',
      targetCount: 5,
      deletedCount: 5,
      rebuildStatus: 'failed',
      errorCode: 'request_cleanup_delete_failed',
    }));
    await mount();

    expect(
      renderer!.root
        .findAllByType('span')
        .some((node) =>
          node.children.join('').includes('system_info.request_cleanup_status_rebuild_failed')
        )
    ).toBe(false);
    expect(
      renderer!.root
        .findAllByProps({ role: 'alert' })
        .some((node) =>
          node.children.join('').includes('system_info.request_cleanup_delete_failure_reason')
        )
    ).toBe(true);
  });

  it('checks the server for a preexisting task before enabling a new preview', async () => {
    let resolveDiscovery!: (value: RequestCleanupStatus | null) => void;
    mocks.onStatus.mockImplementationOnce(
      () => new Promise<RequestCleanupStatus | null>((resolve) => (resolveDiscovery = resolve))
    );
    await mount();
    await changeCutoff(firstCutoffInput);
    expect(mocks.onStatus).toHaveBeenCalledWith(null, expect.any(AbortSignal));
    expect(clickButton('secondary').props.disabled).toBe(true);

    await act(async () => resolveDiscovery(null));
    expect(clickButton('secondary').props.disabled).toBe(false);
  });

  it('recovers an active task from the server when no session ID was saved', async () => {
    mocks.onStatus.mockImplementation(async (runId) => ({
      runId: runId ?? 'recovered-run',
      status: 'deleting',
      targetCount: 5,
      deletedCount: 2,
      rebuildStatus: 'pending',
    }));
    await mount();
    expect(mocks.onStatus).toHaveBeenCalledWith(null, expect.any(AbortSignal));
    expect(mocks.onStatus).toHaveBeenCalledWith('recovered-run', expect.any(AbortSignal));
    await changeCutoff(firstCutoffInput);
    expect(clickButton('secondary').props.disabled).toBe(true);
    expect(
      renderer!.root.findAllByType('code').some((node) => node.children.includes('recovered-run'))
    ).toBe(true);
  });

  it('recovers a completed task after execute loses its response', async () => {
    mocks.onExecute.mockRejectedValueOnce(new Error('network interrupted'));
    let discoveryCount = 0;
    mocks.onStatus.mockImplementation(async (runId) => {
      if (runId === null && discoveryCount++ === 0) return null;
      return {
        runId: runId ?? 'completed-run',
        status: 'completed',
        targetCount: 3,
        deletedCount: 3,
        rebuildStatus: 'ready',
      };
    });
    await mount();
    await changeCutoff(firstCutoffInput);
    await act(async () => clickButton('secondary').props.onClick());
    await act(async () => clickButton('danger').props.onClick());
    await act(async () => mocks.confirmation?.onConfirm());

    expect(mocks.onStatus).toHaveBeenCalledWith(null, expect.any(AbortSignal));
    expect(mocks.onStatus).toHaveBeenCalledTimes(2);
    expect(mocks.onCompleted).not.toHaveBeenCalled();
    expect(clickButton('secondary').props.disabled).toBe(false);
    expect(
      renderer!.root
        .findAllByProps({ role: 'status' })
        .some((node) => node.children.includes('system_info.request_cleanup_previous_success'))
    ).toBe(true);
  });

  it('remains usable when access to sessionStorage throws', async () => {
    const original = Object.getOwnPropertyDescriptor(globalThis, 'sessionStorage');
    Object.defineProperty(globalThis, 'sessionStorage', {
      configurable: true,
      get: () => {
        throw new Error('storage blocked');
      },
    });
    try {
      await mount();
      await changeCutoff(firstCutoffInput);
      expect(clickButton('secondary').props.disabled).toBe(false);
      await act(async () => clickButton('secondary').props.onClick());
      await act(async () => clickButton('danger').props.onClick());
      await act(async () => mocks.confirmation?.onConfirm());
      expect(mocks.onCompleted).toHaveBeenCalledTimes(1);
      expect(
        renderer!.root
          .findAllByProps({ role: 'alert' })
          .some((node) =>
            node.children.some(
              (child) =>
                typeof child === 'string' && child.includes('request_cleanup_storage_unavailable')
            )
          )
      ).toBe(true);
    } finally {
      if (original) Object.defineProperty(globalThis, 'sessionStorage', original);
      else Reflect.deleteProperty(globalThis, 'sessionStorage');
    }
  });

  it('requires a manual cutoff and a matching nonempty preview before cleanup can be confirmed', async () => {
    await mount();
    expect(clickButton('secondary').props.disabled).toBe(true);
    expect(clickButton('danger').props.disabled).toBe(true);

    await changeCutoff(firstCutoffInput);
    expect(clickButton('secondary').props.disabled).toBe(false);
    await act(async () => clickButton('secondary').props.onClick());

    expect(mocks.onPreview).toHaveBeenCalledWith(
      new Date(firstCutoffInput).getTime(),
      expect.any(AbortSignal)
    );
    expect(clickButton('danger').props.disabled).toBe(false);
    await act(async () => clickButton('danger').props.onClick());
    expect(mocks.confirmation?.secondConfirmation).toBeDefined();
    expect(mocks.onExecute).not.toHaveBeenCalled();

    await act(async () => mocks.confirmation?.onConfirm());
    expect(mocks.onExecute).toHaveBeenCalledTimes(1);
    expect(mocks.onCompleted).toHaveBeenCalledTimes(1);
    expect(clickButton('danger').props.disabled).toBe(true);
  });

  it('does not allow execution when the preview contains no request events', async () => {
    mocks.onPreview.mockImplementation(async (cutoffTimestampMs) => ({
      cutoffTimestampMs,
      eventCount: 0,
      targetEventId: 0,
      previewToken: 'empty-preview',
      expiresAtMs: Date.now() + 60_000,
    }));
    await mount();
    await changeCutoff(firstCutoffInput);
    await act(async () => clickButton('secondary').props.onClick());

    expect(clickButton('danger').props.disabled).toBe(true);
    expect(mocks.showConfirmation).not.toHaveBeenCalled();
    expect(mocks.onExecute).not.toHaveBeenCalled();
  });

  it('shows a localized empty-range message for the backend 422 response', async () => {
    mocks.onPreview.mockRejectedValueOnce(
      Object.assign(new Error('no events'), { code: 'request_cleanup_no_events', status: 422 })
    );
    await mount();
    await changeCutoff(firstCutoffInput);
    await act(async () => clickButton('secondary').props.onClick());

    expect(clickButton('danger').props.disabled).toBe(true);
    expect(
      renderer!.root
        .findAllByProps({ role: 'alert' })
        .some((node) => node.children.includes('system_info.request_cleanup_no_events'))
    ).toBe(true);
  });

  it('invalidates a preview and saved confirmation when the cutoff changes', async () => {
    await mount();
    await changeCutoff(firstCutoffInput);
    await act(async () => clickButton('secondary').props.onClick());
    await act(async () => clickButton('danger').props.onClick());
    const savedConfirmation = mocks.confirmation;

    await changeCutoff(secondCutoffInput);
    expect(mocks.hideConfirmation).toHaveBeenCalled();
    expect(clickButton('danger').props.disabled).toBe(true);
    await act(async () => savedConfirmation?.onConfirm());
    expect(mocks.onExecute).not.toHaveBeenCalled();
  });

  it('ignores a late preview response after the cutoff changes', async () => {
    let resolvePreview!: (value: RequestCleanupPreview) => void;
    mocks.onPreview.mockImplementationOnce(
      () => new Promise<RequestCleanupPreview>((resolve) => (resolvePreview = resolve))
    );
    await mount();
    await changeCutoff(firstCutoffInput);
    await act(async () => clickButton('secondary').props.onClick());
    await changeCutoff(secondCutoffInput);

    await act(async () => {
      resolvePreview({
        cutoffTimestampMs: new Date(firstCutoffInput).getTime(),
        targetEventId: 40,
        eventCount: 2,
        previewToken: 'preview-token',
        expiresAtMs: Date.now() + 60_000,
      });
    });
    expect(clickButton('danger').props.disabled).toBe(true);
    expect(mocks.showConfirmation).not.toHaveBeenCalled();
  });

  it('revokes a saved confirmation when its connection scope unmounts', async () => {
    await mount();
    await changeCutoff(firstCutoffInput);
    await act(async () => clickButton('secondary').props.onClick());
    await act(async () => clickButton('danger').props.onClick());
    const savedConfirmation = mocks.confirmation;
    await act(async () => renderer?.unmount());
    renderer = undefined;

    expect(mocks.hideConfirmation).toHaveBeenCalled();
    await act(async () => savedConfirmation?.onConfirm());
    expect(mocks.onExecute).not.toHaveBeenCalled();
  });

  it('revokes a saved confirmation when manager capability disappears', async () => {
    await mount();
    await changeCutoff(firstCutoffInput);
    await act(async () => clickButton('secondary').props.onClick());
    await act(async () => clickButton('danger').props.onClick());
    const savedConfirmation = mocks.confirmation;

    await act(async () => {
      renderer!.update(
        <RequestCleanupCard
          isAvailable={false}
          storageKey="manager-scope"
          onPreview={mocks.onPreview}
          onExecute={mocks.onExecute}
          onStatus={mocks.onStatus}
          onResume={mocks.onResume}
          onCompleted={mocks.onCompleted}
        />
      );
    });

    expect(mocks.hideConfirmation).toHaveBeenCalled();
    await act(async () => savedConfirmation?.onConfirm());
    expect(mocks.onExecute).not.toHaveBeenCalled();
  });

  it('never revives a saved confirmation after switching connection scope and back', async () => {
    await mount();
    await changeCutoff(firstCutoffInput);
    await act(async () => clickButton('secondary').props.onClick());
    await act(async () => clickButton('danger').props.onClick());
    const savedConfirmation = mocks.confirmation;

    const renderScope = (scope: string) => (
      <RequestCleanupCard
        key={scope}
        isAvailable
        storageKey={scope}
        onPreview={mocks.onPreview}
        onExecute={mocks.onExecute}
        onStatus={mocks.onStatus}
        onResume={mocks.onResume}
        onCompleted={mocks.onCompleted}
      />
    );
    await act(async () => renderer!.update(renderScope('other-scope')));
    await act(async () => renderer!.update(renderScope('manager-scope')));
    await act(async () => savedConfirmation?.onConfirm());

    expect(mocks.hideConfirmation).toHaveBeenCalled();
    expect(mocks.onExecute).not.toHaveBeenCalled();
    expect(clickButton('danger').props.disabled).toBe(true);
  });

  it('ignores a late resume response after manager capability is revoked', async () => {
    const failedStatus: RequestCleanupStatus = {
      runId: 'failed-run',
      status: 'failed',
      targetCount: 5,
      deletedCount: 2,
      rebuildStatus: 'failed',
    };
    mocks.onStatus.mockResolvedValue(failedStatus);
    let resolveResume!: () => void;
    mocks.onResume.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          resolveResume = resolve;
        })
    );
    await mount();
    const resume = renderer!.root
      .findAllByType(Button)
      .find((button) => button.props.children === 'system_info.request_cleanup_resume');
    expect(resume).toBeDefined();
    await act(async () => resume!.props.onClick());
    await act(async () => {
      renderer!.update(
        <RequestCleanupCard
          isAvailable={false}
          storageKey="manager-scope"
          onPreview={mocks.onPreview}
          onExecute={mocks.onExecute}
          onStatus={mocks.onStatus}
          onResume={mocks.onResume}
          onCompleted={mocks.onCompleted}
        />
      );
    });
    await act(async () => resolveResume());

    expect(mocks.onStatus).toHaveBeenCalledTimes(2);
    expect(mocks.onCompleted).not.toHaveBeenCalled();
  });

  it('keeps the cleanup disabled when manager capability is absent', async () => {
    await mount(false);
    await changeCutoff(firstCutoffInput);
    expect(clickButton('secondary').props.disabled).toBe(true);
    expect(clickButton('danger').props.disabled).toBe(true);
  });
});
