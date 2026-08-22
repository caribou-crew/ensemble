// Latency rule CRUD. The API's LatencyStore (core/proxy/latency.go) keys a
// rule by (target, path) — Set() upserts by matching that pair exactly, so
// letting an "edit" change target/path would silently create a NEW rule
// and orphan the old one rather than replacing it. This view sidesteps
// that trap by treating (target, path) as immutable once a rule exists:
// editing only ever touches the delay/enabled fields, and changing the key
// means delete-then-add.
import { useEffect, useState } from 'react';
import { Badge, Spinner } from '@ensemble/design-system';
import { useAsync } from '@ensemble/design-system/useAsync';
import { api, messageOf } from '../api/client';
import type { LatencyRule } from '../api/types';
import './LatencyView.css';

const NEW_KEY = '\u0000new';

function ruleKey(target: string, path: string): string {
  return `${target}␟${path}`;
}

interface Draft {
  target: string;
  path: string;
  fixedMs: string;
  p50: string;
  p95: string;
  p99: string;
  enabled: boolean;
}

const EMPTY_DRAFT: Draft = { target: '', path: '', fixedMs: '', p50: '', p95: '', p99: '', enabled: true };

function draftFromRule(r: LatencyRule): Draft {
  return {
    target: r.target,
    path: r.path,
    fixedMs: r.fixedMs ? String(r.fixedMs) : '',
    p50: r.p50 ? String(r.p50) : '',
    p95: r.p95 ? String(r.p95) : '',
    p99: r.p99 ? String(r.p99) : '',
    enabled: r.enabled,
  };
}

function ruleFromDraft(d: Draft): LatencyRule {
  const num = (s: string) => (s.trim() === '' ? undefined : Number(s));
  return {
    target: d.target.trim(),
    path: d.path.trim(),
    fixedMs: num(d.fixedMs),
    p50: num(d.p50),
    p95: num(d.p95),
    p99: num(d.p99),
    enabled: d.enabled,
  };
}

function delaySummary(r: LatencyRule): string {
  if (r.fixedMs) return `${r.fixedMs}ms fixed`;
  if (r.p50 || r.p95 || r.p99) return `p50 ${r.p50 ?? 0} / p95 ${r.p95 ?? 0} / p99 ${r.p99 ?? 0}ms`;
  return '—';
}

export default function LatencyView() {
  // DEVIATION FROM THE BRIEF'S WATCH-OUT 4, RECORDED HERE RATHER THAN SILENTLY: the brief
  // prescribes a version-counter refetch for `rules` (the pattern used by EntityDetail and
  // Task 15's `mutate`), discarding each mutation's own response in favor of a reload. That
  // breaks LatencyView.test.tsx as written: its `fetchMock` returns `initialRules` for every
  // unconditioned GET and `updatedRules` only for the PUT, so a post-save refetch would land
  // '100ms fixed' right back on screen instead of the '250ms fixed' the test asserts — the
  // test's own comment says the point is proving "the actual round trip, not an optimistic
  // local echo" of the MUTATION's response, which a refetch-and-discard design cannot satisfy.
  // That test is protected by the "do not modify test files during migration" rule, so the
  // migration is what has to bend: `rules` is seeded ONCE from useAsync's one-shot load
  // (deps: [], never re-triggered) and every mutation writes it directly with its own
  // response, exactly as before. This is safe specifically because nothing here ever
  // re-triggers the load — unlike the general case watch-out 4 warns about, there is no
  // second load-completion that could land after a mutation and clobber it. The original
  // hand-rolled code's own comment made the same point: "the `!rules` loading gate below
  // makes a racing mutation impossible in practice."
  const { data: initialRules, error: loadError } = useAsync(() => api.latencyList(), []);
  const [rules, setRules] = useState<LatencyRule[] | null>(null);
  useEffect(() => {
    if (initialRules !== null) setRules(initialRules);
  }, [initialRules]);

  const [editingKey, setEditingKey] = useState<string | null>(null);
  const [draft, setDraft] = useState<Draft>(EMPTY_DRAFT);
  const [formError, setFormError] = useState<string | null>(null);
  // Mirrors the pre-migration shape: one `error` state serves both a load failure and every
  // non-form mutation failure (toggle/delete/armAll/reset — saveEdit's own failure goes to
  // `formError` inline instead), each replacing the table with a full-view error banner.
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (loadError) setError(loadError.message);
  }, [loadError]);
  const [busy, setBusy] = useState(false);

  function startNew() {
    setEditingKey(NEW_KEY);
    setDraft(EMPTY_DRAFT);
    setFormError(null);
  }

  function startEdit(r: LatencyRule) {
    setEditingKey(ruleKey(r.target, r.path));
    setDraft(draftFromRule(r));
    setFormError(null);
  }

  function cancelEdit() {
    setEditingKey(null);
    setFormError(null);
  }

  async function saveEdit() {
    const rule = ruleFromDraft(draft);
    if (!rule.target) {
      setFormError('target is required');
      return;
    }
    setBusy(true);
    try {
      const result = await api.latencyUpsert(rule);
      setRules(result);
      setEditingKey(null);
      setFormError(null);
    } catch (err) {
      setFormError(messageOf(err, 'failed to save rule'));
    } finally {
      setBusy(false);
    }
  }

  async function toggleEnabled(r: LatencyRule) {
    setBusy(true);
    try {
      const result = await api.latencyUpsert({ ...r, enabled: !r.enabled });
      setRules(result);
    } catch (err) {
      setError(messageOf(err, 'failed to update rule'));
    } finally {
      setBusy(false);
    }
  }

  async function deleteRule(r: LatencyRule) {
    if (!window.confirm(`Delete the latency rule for ${r.target} ${r.path || '(all paths)'}?`)) return;
    setBusy(true);
    try {
      const result = await api.latencyDelete(r.target, r.path);
      setRules(result);
    } catch (err) {
      setError(messageOf(err, 'failed to delete rule'));
    } finally {
      setBusy(false);
    }
  }

  async function armAll(enabled: boolean) {
    if (!window.confirm(`${enabled ? 'Arm' : 'Disarm'} every latency rule?`)) return;
    setBusy(true);
    try {
      const result = await api.latencyArmAll(enabled);
      setRules(result);
    } catch (err) {
      setError(messageOf(err, 'failed to update rules'));
    } finally {
      setBusy(false);
    }
  }

  async function resetAll() {
    if (!window.confirm('Remove every latency rule? This cannot be undone.')) return;
    setBusy(true);
    try {
      const result = await api.latencyReset();
      setRules(result);
    } catch (err) {
      setError(messageOf(err, 'failed to reset rules'));
    } finally {
      setBusy(false);
    }
  }

  if (error) {
    return (
      <div className="latency-view latency-view--error">
        <Badge tone="red">offline</Badge>
        <span>{error}</span>
      </div>
    );
  }

  if (!rules) {
    return (
      <div className="latency-view latency-view--loading">
        <Spinner />
        <span>loading latency rules…</span>
      </div>
    );
  }

  return (
    <div className="latency-view">
      <div className="latency-view__toolbar">
        <button type="button" onClick={startNew} disabled={busy}>
          + add rule
        </button>
        <button type="button" onClick={() => void armAll(true)} disabled={busy || rules.length === 0}>
          arm all
        </button>
        <button type="button" onClick={() => void armAll(false)} disabled={busy || rules.length === 0}>
          disarm all
        </button>
        <button
          type="button"
          className="latency-view__danger"
          onClick={() => void resetAll()}
          disabled={busy || rules.length === 0}
        >
          reset
        </button>
        <button type="button" disabled title="Datadog import — stretch S.1">
          APM import
        </button>
        <span className="latency-view__hint">rule changes are recorded in Traffic as annotation hops</span>
      </div>
      <table className="latency-table">
        <thead>
          <tr>
            <th>target</th>
            <th>path</th>
            <th>delay</th>
            <th>enabled</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {editingKey === NEW_KEY && (
            <LatencyEditRow draft={draft} setDraft={setDraft} onSave={saveEdit} onCancel={cancelEdit} busy={busy} error={formError} isNew />
          )}
          {rules.length === 0 && editingKey !== NEW_KEY && (
            <tr>
              <td colSpan={5} className="latency-table__empty">
                no latency rules configured
              </td>
            </tr>
          )}
          {rules.map((r) => {
            const key = ruleKey(r.target, r.path);
            if (editingKey === key) {
              return (
                <LatencyEditRow
                  key={key}
                  draft={draft}
                  setDraft={setDraft}
                  onSave={saveEdit}
                  onCancel={cancelEdit}
                  busy={busy}
                  error={formError}
                />
              );
            }
            return (
              <tr key={key} className="latency-table__row">
                <td className="latency-table__target">{r.target}</td>
                <td className="latency-table__path">
                  {r.path || <span className="latency-table__all">(all paths)</span>}
                </td>
                <td className="latency-table__delay">{delaySummary(r)}</td>
                <td>
                  <button
                    type="button"
                    className="latency-table__toggle"
                    onClick={() => void toggleEnabled(r)}
                    disabled={busy}
                  >
                    <Badge tone={r.enabled ? 'green' : 'neutral'}>{r.enabled ? 'armed' : 'disarmed'}</Badge>
                  </button>
                </td>
                <td className="latency-table__actions">
                  <button type="button" onClick={() => startEdit(r)} disabled={busy}>
                    edit
                  </button>
                  <button type="button" onClick={() => void deleteRule(r)} disabled={busy}>
                    delete
                  </button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function LatencyEditRow({
  draft,
  setDraft,
  onSave,
  onCancel,
  busy,
  error,
  isNew = false,
}: {
  draft: Draft;
  setDraft: (d: Draft) => void;
  onSave: () => void;
  onCancel: () => void;
  busy: boolean;
  error: string | null;
  isNew?: boolean;
}) {
  function field<K extends keyof Draft>(key: K, value: Draft[K]) {
    setDraft({ ...draft, [key]: value });
  }

  return (
    <tr className="latency-table__row latency-table__row--edit">
      <td>
        <input
          name="target"
          className="latency-table__input"
          value={draft.target}
          onChange={(e) => field('target', e.target.value)}
          placeholder="service or *"
          disabled={!isNew}
          title={isNew ? undefined : "target/path form the rule's key — delete and re-add to change them"}
        />
      </td>
      <td>
        <input
          name="path"
          className="latency-table__input"
          value={draft.path}
          onChange={(e) => field('path', e.target.value)}
          placeholder="/path prefix"
          disabled={!isNew}
          title={isNew ? undefined : "target/path form the rule's key — delete and re-add to change them"}
        />
      </td>
      <td className="latency-table__edit-delay">
        <input
          name="fixedMs"
          type="number"
          className="latency-table__input latency-table__input--num"
          value={draft.fixedMs}
          onChange={(e) => field('fixedMs', e.target.value)}
          placeholder="fixed ms"
        />
        <input
          name="p50"
          type="number"
          className="latency-table__input latency-table__input--num"
          value={draft.p50}
          onChange={(e) => field('p50', e.target.value)}
          placeholder="p50"
        />
        <input
          name="p95"
          type="number"
          className="latency-table__input latency-table__input--num"
          value={draft.p95}
          onChange={(e) => field('p95', e.target.value)}
          placeholder="p95"
        />
        <input
          name="p99"
          type="number"
          className="latency-table__input latency-table__input--num"
          value={draft.p99}
          onChange={(e) => field('p99', e.target.value)}
          placeholder="p99"
        />
      </td>
      <td>
        <input name="enabled" type="checkbox" checked={draft.enabled} onChange={(e) => field('enabled', e.target.checked)} />
      </td>
      <td className="latency-table__actions">
        <button type="button" onClick={onSave} disabled={busy}>
          save
        </button>
        <button type="button" onClick={onCancel} disabled={busy}>
          cancel
        </button>
        {error && <div className="latency-table__form-error">{error}</div>}
      </td>
    </tr>
  );
}
