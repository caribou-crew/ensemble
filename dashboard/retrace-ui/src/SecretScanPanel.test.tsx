import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SecretScanPanel, acceptNotice } from './App';
import { ApiError, secretFindingsOf, type AcceptBundle, type SecretFinding } from './api/client';

const finding: SecretFinding = {
  file: 'wire.jsonl',
  seq: 1,
  path: 'resp.body.session_key',
  kind: 'jwt',
  suggestion: 'add `redact: [session_key]` to retrace.yaml and re-record',
};

describe('secretFindingsOf', () => {
  it('extracts findings only from a refusal the server marked forcible', () => {
    const scanErr = new ApiError(409, 'refusing to promote', {
      error: 'refusing to promote',
      forcible: true,
      secretFindings: [finding],
    });
    expect(secretFindingsOf(scanErr)).toEqual([finding]);
  });

  it('returns null for every other failure — no force button on a refusal the CLI would not override', () => {
    // A plain refusal (a fatal capture verdict, a mask typo): same 409, no marker.
    expect(secretFindingsOf(new ApiError(409, 'refusing to promote', { error: 'capture broken' }))).toBeNull();
    // A body that names findings but was never marked forcible.
    expect(
      secretFindingsOf(new ApiError(409, 'x', { secretFindings: [finding] })),
    ).toBeNull();
    // Not an ApiError at all (a network failure).
    expect(secretFindingsOf(new Error('fetch failed'))).toBeNull();
  });
});

describe('acceptNotice with forced secret findings', () => {
  it('says out loud what the forced accept committed', () => {
    const bundle: AcceptBundle = {
      dir: '/x/.retrace-ref/web/checkout/reference',
      files: ['manifest.json'],
      bytes: 1,
      runId: 'r1',
      captureStatus: 'ok',
      unmatchedMasks: [],
      secretFindings: [finding],
    };
    const notice = acceptNotice('web', 'checkout', bundle);
    expect(notice).toContain('WARNING');
    expect(notice).toContain('resp.body.session_key');
    expect(notice).toContain('acceptedWithSecrets');
  });
});

describe('SecretScanPanel', () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it('shows each finding and fires force only from its own button', () => {
    const onCancel = vi.fn();
    const onForce = vi.fn();
    act(() => {
      root.render(
        <SecretScanPanel findings={[finding]} busy={false} onCancel={onCancel} onForce={onForce} />,
      );
    });
    expect(container.textContent).toContain('resp.body.session_key');
    expect(container.textContent).toContain(finding.suggestion);

    const buttons = Array.from(container.querySelectorAll('button'));
    const force = buttons.find((b) => b.textContent?.includes('--force'))!;
    const cancel = buttons.find((b) => b.textContent === 'cancel')!;
    act(() => cancel.click());
    expect(onCancel).toHaveBeenCalledOnce();
    expect(onForce).not.toHaveBeenCalled();
    act(() => force.click());
    expect(onForce).toHaveBeenCalledOnce();
  });

  it('disables both buttons while an accept is in flight', () => {
    act(() => {
      root.render(
        <SecretScanPanel findings={[finding]} busy={true} onCancel={() => {}} onForce={() => {}} />,
      );
    });
    for (const b of container.querySelectorAll('button')) {
      expect(b.disabled).toBe(true);
    }
  });
});
