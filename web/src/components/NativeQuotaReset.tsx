import { useEffect, useId, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { useT } from "../i18n";
import { createNativeQuotaResetClient, type NativeQuotaResetBinding } from "../api/nativeQuotaReset";

interface Props {
  onClose: () => void;
  onReset: (count: number) => Promise<void>;
}
const prefix = "mapping.nativeQuotaReset.";
const sameBinding = (before: NativeQuotaResetBinding, after: NativeQuotaResetBinding) =>
  before.id === after.id && before.key === after.key && before.createdAt === after.createdAt;

function safeErrorKey(cause: unknown): string {
  const error = cause as { message?: string; response?: { status?: number } };
  if (error?.message === "quota_reset_session_changed") return "sessionChanged";
  if (error?.message === "quota_reset_selection_changed" || error?.response?.status === 404) return "selectionChanged";
  return "failed";
}

export default function NativeQuotaReset({ onClose, onReset }: Props) {
  const t = useT();
  const [client] = useState(createNativeQuotaResetClient);
  const [bindings, setBindings] = useState<NativeQuotaResetBinding[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [resetting, setResetting] = useState(false);
  const [completed, setCompleted] = useState<number | null>(null);
  const [error, setError] = useState("");
  const [refreshWarning, setRefreshWarning] = useState(false);
  const alive = useRef(true);
  const busy = useRef(false);
  const generation = useRef(0);
  const dialog = useRef<HTMLDivElement>(null);
  const feedback = useRef<HTMLDivElement>(null);
  const titleID = useId();
  const warningID = useId();

  useEffect(() => {
    alive.current = true;
    const version = ++generation.current;
    const previous = document.activeElement;
    dialog.current?.focus();
    void client.loadBindings().then((rows) => {
      if (!alive.current || version !== generation.current) return;
      setBindings(rows);
      setSelected(new Set(rows.map((row) => row.id)));
    }).catch((cause) => {
      if (alive.current && version === generation.current) setError(t(prefix + safeErrorKey(cause)));
    }).finally(() => {
      if (alive.current && version === generation.current) setLoading(false);
    });
    return () => {
      alive.current = false;
      generation.current++;
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
    };
  }, [client]);

  const close = () => {
    if (busy.current) return;
    setBindings([]);
    setSelected(new Set());
    onClose();
  };
  const visible = bindings.filter((binding) => [binding.name, binding.id, binding.keyPreview]
    .some((value) => value.toLowerCase().includes(query.trim().toLowerCase())));
  const toggle = (id: string) => setSelected((previous) => {
    const next = new Set(previous);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });
  const refresh = async () => {
    if (busy.current || loading || completed !== null) return;
    setLoading(true);
    setError("");
    try {
      const rows = await client.loadBindings();
      if (!alive.current) return;
      setSelected((previous) => new Set(rows.filter((row) => previous.has(row.id)
        && bindings.some((before) => sameBinding(before, row))).map((row) => row.id)));
      setBindings(rows);
    } catch (cause) {
      if (alive.current) setError(t(prefix + safeErrorKey(cause)));
    } finally {
      if (alive.current) setLoading(false);
    }
  };
  const submit = async () => {
    if (busy.current || loading || completed !== null || !selected.size) return;
    busy.current = true;
    setResetting(true);
    setError("");
    try {
      const chosen = bindings.filter((row) => selected.has(row.id));
      const current = await client.loadBindings();
      if (!alive.current) return;
      if (chosen.length !== selected.size || chosen.some((before) => !current.some((after) => sameBinding(before, after)))) {
        throw new Error("quota_reset_selection_changed");
      }
      const result = await client.reset(chosen.map((row) => row.id));
      if (!alive.current) return;
      setCompleted(result.count);
      setBindings([]);
      setSelected(new Set());
      try { await onReset(result.count); }
      catch { if (alive.current) setRefreshWarning(true); }
    } catch (cause) {
      if (alive.current) setError(t(prefix + safeErrorKey(cause)));
    } finally {
      busy.current = false;
      if (alive.current) {
        setResetting(false);
        feedback.current?.focus();
      }
    }
  };
  const trapFocus = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") { event.preventDefault(); close(); return; }
    if (event.key !== "Tab") return;
    const targets = Array.from(dialog.current?.querySelectorAll<HTMLElement>(
      'button:not(:disabled), input:not(:disabled), [tabindex="0"]',
    ) ?? []);
    if (!targets.length) { event.preventDefault(); dialog.current?.focus(); return; }
    const first = targets[0], last = targets[targets.length - 1];
    if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) {
      event.preventDefault(); last.focus();
    } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === dialog.current)) {
      event.preventDefault(); first.focus();
    }
  };
  const disabled = loading || resetting || completed !== null;

  return (
    <div className="modal-overlay" onMouseDown={(event) => { if (event.target === event.currentTarget) close(); }}>
      <div className="modal native-batch-modal native-quota-reset" role="dialog" aria-modal="true"
        aria-labelledby={titleID} aria-describedby={warningID} aria-busy={loading || resetting}
        tabIndex={-1} ref={dialog} onKeyDown={trapFocus}>
        <div className="native-batch-header">
          <h3 id={titleID}>{t(prefix + "title")}</h3>
          <button className="btn sm" type="button" disabled={resetting} onClick={close}>{t(prefix + "close")}</button>
        </div>
        <p className="native-batch-intro">{t(prefix + "scope")}</p>
        <p className="native-quota-reset-warning" id={warningID}>{t(prefix + "warning")}</p>
        <div className="native-batch-feedback" tabIndex={-1} ref={feedback}>
          {error && <p className="error" role="alert">{error}</p>}
          {loading && <p className="muted" role="status">{t(prefix + "loading")}</p>}
          {completed !== null && <p role="status">{t(prefix + "completed", { count: completed })}</p>}
          {refreshWarning && <p className="error" role="alert">{t(prefix + "refreshFailed")}</p>}
        </div>
        {completed === null && <>
          <label className="sr-only" htmlFor="native-quota-reset-search">{t(prefix + "search")}</label>
          <input id="native-quota-reset-search" className="input" type="search" value={query} disabled={disabled}
            onChange={(event) => setQuery(event.target.value)} placeholder={t(prefix + "search")} />
          <div className="native-batch-selection-tools">
            <span>{t(prefix + "selected", { count: selected.size, total: bindings.length })}</span>
            <button className="btn sm" type="button" disabled={disabled || !visible.length} onClick={() => setSelected((previous) => {
              const next = new Set(previous);
              for (const binding of visible) next.add(binding.id);
              return next;
            })}>{t(prefix + "selectAll")}</button>
            <button className="btn sm" type="button" disabled={disabled || !selected.size} onClick={() => setSelected(new Set())}>{t(prefix + "clear")}</button>
          </div>
          <div className="native-batch-options">
            {visible.map((binding) => <label className="native-batch-key native-quota-reset-key" key={binding.id}>
              <input type="checkbox" checked={selected.has(binding.id)} disabled={disabled} onChange={() => toggle(binding.id)} />
              <span><strong>{binding.name}</strong><span className="mono">{binding.keyPreview}</span>
                {!binding.enabled && <small>{t(prefix + "disabledBinding")}</small>}
              </span>
            </label>)}
            {!visible.length && !loading && <p className="muted">{t(prefix + (bindings.length ? "noMatches" : "empty"))}</p>}
          </div>
        </>}
        <div className="native-batch-actions">
          {completed === null ? <>
            <button className="btn danger-outline" type="button" disabled={disabled || !selected.size} onClick={() => { void submit(); }}>
              {t(prefix + (resetting ? "resetting" : "confirm"), { count: selected.size })}
            </button>
            <button className="btn" type="button" disabled={disabled} onClick={() => { void refresh(); }}>{t(prefix + "refresh")}</button>
            <button className="btn" type="button" disabled={resetting} onClick={close}>{t(prefix + "cancel")}</button>
          </> : <button className="btn" type="button" disabled={resetting} onClick={close}>{t(prefix + "close")}</button>}
        </div>
      </div>
    </div>
  );
}
