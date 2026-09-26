import { useEffect, useId, useMemo, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { useT } from "../i18n";
import {
  createNativeBindingBatchClient,
  type NativeBindingBatchInventory,
  type NativeBindingBatchRequest,
  type NativeBindingChange,
  type NativeBindingOperation,
  type NativeBindingPreview,
  type NativeBindingRestriction,
  type NativeBindingRollbackRequest,
} from "../api/nativeBindingBatch";

type View = "select" | "review" | "history";
interface Review {
  preview: NativeBindingPreview;
  operationID?: string;
}
interface Props {
  initialView?: "select" | "history" | "undo";
  onClose: () => void;
  onChanged: () => Promise<void>;
}
const PLAN_LABELS = new Map([
  ["free", "Free"], ["plus", "Plus"], ["team", "Team"],
  ["pro", "Pro"], ["enterprise", "Enterprise"], ["edu", "Edu"],
]);
const prefix = "mapping.nativeBatch.";

// Error bodies may include rejected user data. Present only controlled text.
function errorKey(error: unknown): string {
  const value = error as { message?: string; response?: { status?: number; data?: { error?: { code?: string } } } };
  if (value?.message === "batch_session_changed") return "sessionChanged";
  if (value?.message === "batch_inventory_incomplete") return "incomplete";
  if (value?.message === "batch_selection_changed") return "selectionChanged";
  if (value?.response?.status === 409) return "conflict";
  if (value?.response?.status === 404) return "historyMissing";
  if (value?.response?.status === 413) return "tooLarge";
  return "requestFailed";
}

export default function NativeBindingBatch({ initialView = "select", onClose, onChanged }: Props) {
  const t = useT();
  const [client] = useState(createNativeBindingBatchClient);
  const [view, setView] = useState<View>(initialView === "select" ? "select" : "history");
  const [inventory, setInventory] = useState<NativeBindingBatchInventory | null>(null);
  // Plaintext keys live only in this mounted dialog. JSX uses catalog previews
  // and numeric positions, never these values as DOM attributes or React keys.
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(new Set());
  const [authIDs, setAuthIDs] = useState<Set<string>>(new Set());
  const [keyQuery, setKeyQuery] = useState("");
  const [credentialQuery, setCredentialQuery] = useState("");
  const [review, setReview] = useState<Review | null>(null);
  const [operations, setOperations] = useState<NativeBindingOperation[]>([]);
  const [operation, setOperation] = useState<NativeBindingOperation | null>(null);
  const [pending, setPending] = useState(true);
  const [committing, setCommitting] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const busy = useRef(false);
  const alive = useRef(true);
  const dialog = useRef<HTMLDivElement>(null);
  const titleID = useId();
  const feedback = useRef<HTMLDivElement>(null);

  useEffect(() => {
    alive.current = true;
    const previous = document.activeElement;
    dialog.current?.focus();
    void (async () => {
      const [inventoryResult, historyResult] = await Promise.allSettled([client.loadInventory(), client.history()]);
      if (!alive.current) return;
      if (inventoryResult.status === "fulfilled") {
        setInventory(inventoryResult.value);
        setSelectedKeys(new Set(inventoryResult.value.apiKeys));
      } else setError(t(prefix + errorKey(inventoryResult.reason)));
      if (historyResult.status === "fulfilled") {
        setOperations(historyResult.value);
        if (initialView === "undo") {
          const latest = historyResult.value.find((item) => item.kind === "batch" && !item.reverted_by);
          setOperation(latest ?? null);
          if (!latest) setNotice(t(prefix + "nothingToUndo"));
        }
      } else if (initialView !== "select") setError(t(prefix + "historyFailed"));
      setPending(false);
    })();
    return () => {
      alive.current = false;
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
    };
    // A dialog is bound to the host/session and locale present when it opens.
  }, [client]);

  const close = () => {
    if (committing) return;
    setInventory(null);
    setSelectedKeys(new Set());
    onClose();
  };
  const run = async (action: () => Promise<void>) => {
    if (busy.current || pending) return;
    busy.current = true;
    setPending(true);
    setError("");
    setNotice("");
    try { await action(); }
    catch (cause) {
      if (alive.current) {
        setError(t(prefix + errorKey(cause)));
        setReview(null);
      }
    } finally {
      busy.current = false;
      if (alive.current) {
        setPending(false);
        feedback.current?.focus();
      }
    }
  };
  const keyEntries = useMemo(() => {
    if (!inventory) return [];
    return inventory.catalog.entries.filter((entry) => typeof inventory.apiKeys[entry.key_index] === "string")
      .sort((a, b) => a.key_index - b.key_index);
  }, [inventory]);
  const visibleKeys = keyEntries.filter((entry) => [entry.key_preview, entry.binding?.name, entry.binding?.id]
    .some((value) => value?.toLowerCase().includes(keyQuery.trim().toLowerCase())));
  const credentials = inventory?.credentials.credentials ?? [];
  const visibleCredentials = credentials.filter((credential) => [
    credential.email, credential.label, credential.name, credential.id, credential.provider, credential.plan,
  ].some((value) => value?.toLowerCase().includes(credentialQuery.trim().toLowerCase())));
  const complete = inventory?.credentials.identitiesComplete === true;
  const identities = new Map(credentials.map((credential) => [credential.id, credential]));
  const identityText = (id: string) => {
    const credential = identities.get(id);
    return credential?.email || credential?.label || credential?.name || id;
  };
  const selectKey = (key: string) => setSelectedKeys((previous) => {
    const next = new Set(previous);
    if (next.has(key)) next.delete(key); else next.add(key);
    return next;
  });
  const selectCredential = (id: string) => setAuthIDs((previous) => {
    const next = new Set(previous);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });
  const freshInventory = async () => {
    const fresh = await client.loadInventory();
    if (!alive.current) throw new Error("batch_session_changed");
    setInventory(fresh);
    if (fresh.credentials.identitiesComplete !== true) throw new Error("batch_inventory_incomplete");
    return fresh;
  };
  const batchRequest = (fresh: NativeBindingBatchInventory): NativeBindingBatchRequest => {
    const currentKeys = new Set(fresh.apiKeys);
    const currentIDs = new Set(fresh.credentials.credentials.map((item) => item.id));
    if (selectedKeys.size === 0 || authIDs.size === 0
      || [...selectedKeys].some((key) => !currentKeys.has(key))
      || [...authIDs].some((id) => !currentIDs.has(id)
        || fresh.credentials.credentials.find((item) => item.id === id)?.identityVerified === false)) throw new Error("batch_selection_changed");
    return {
      api_keys: fresh.apiKeys,
      selected_indices: fresh.apiKeys.flatMap((key, index) => selectedKeys.has(key) ? [index] : []),
      auth_ids: [...authIDs].sort(), available_auth_ids: [...currentIDs].sort(), catalog_complete: true,
    };
  };
  const rollbackRequest = (fresh: NativeBindingBatchInventory, operationID: string): NativeBindingRollbackRequest => ({
    operation_id: operationID, api_keys: fresh.apiKeys,
    available_auth_ids: fresh.credentials.credentials.map((item) => item.id).sort(), catalog_complete: true,
  });
  const previewBatch = () => run(async () => {
    const fresh = await freshInventory();
    const preview = await client.preview(batchRequest(fresh));
    if (!alive.current) return;
    setReview({ preview });
    setView("review");
  });
  const previewRestore = (item: NativeBindingOperation) => run(async () => {
    const fresh = await freshInventory();
    const preview = await client.previewRollback(rollbackRequest(fresh, item.id));
    if (!alive.current) return;
    setReview({ preview, operationID: item.id });
    setView("review");
  });
  const commit = () => run(async () => {
    if (!review?.preview.can_apply || review.preview.noop || review.preview.conflicts.length) return;
    setCommitting(true);
    try {
      const fresh = await freshInventory();
      const result = review.operationID
        ? await client.rollback({ ...rollbackRequest(fresh, review.operationID), expected_revision: review.preview.revision })
        : await client.apply({ ...batchRequest(fresh), expected_revision: review.preview.revision });
      if (!alive.current) return;
      setReview(null);
      setOperation(result.operation ?? null);
      setView("history");
      setNotice(t(prefix + (result.noop ? "noop" : "completed"), { count: result.changed }));
      // A successful mutation stays successful if a later display refresh fails.
      const updates = await Promise.allSettled([client.history(), onChanged()]);
      if (!alive.current) return;
      if (updates[0].status === "fulfilled") setOperations(updates[0].value);
      else setError(t(prefix + "historyFailed"));
    } finally { if (alive.current) setCommitting(false); }
  });
  const showHistory = () => run(async () => {
    const history = await client.history();
    if (!alive.current) return;
    setOperations(history);
    setReview(null);
    setView("history");
  });
  const restart = () => {
    if (pending) return;
    setReview(null);
    setError("");
    setNotice("");
    setView("select");
  };
  const restriction = (value: NativeBindingRestriction | null) => {
    if (!value) return <span className="muted">{t(prefix + "unbound")}</span>;
    if (value.group) return <span className="native-binding-group mono">{value.group}</span>;
    return <ul className="native-batch-identities">{value.auth_ids?.map((id) => <li key={id}>{identityText(id)}</li>)}</ul>;
  };
  const changes = (rows: NativeBindingChange[], newDefaults: boolean) => (
    <div className="native-batch-changes">
      {rows.map((change) => (
        <article className="native-batch-change" key={change.binding_id}>
          <div className="native-batch-change-title"><strong>{change.name || change.binding_id}</strong><span className="mono">{change.key_preview}</span></div>
          <div className="native-batch-diff">
            <div><h4>{t(prefix + "before")}</h4>{restriction(change.before)}</div>
            <div><h4>{t(prefix + "after")}</h4>{restriction(change.after)}</div>
          </div>
          {!change.before && change.after && <p className="native-batch-new">{t(prefix + (newDefaults ? "newBinding" : "restoreExisting"))}</p>}
          {change.before && change.after && inventory?.catalog.entries.some((entry) => entry.binding?.id === change.binding_id && !entry.binding.enabled)
            && <p className="native-batch-new">{t(prefix + "staysDisabled")}</p>}
          {change.before && !change.after && <p className="native-batch-new">{t(prefix + "restoreUnbound")}</p>}
        </article>
      ))}
    </div>
  );
  const conflictLabel = (conflict: NativeBindingPreview["conflicts"][number]) => {
    const codes = new Map([
      ["catalog_incomplete", "conflictCatalog"], ["auth_unavailable", "conflictAuth"],
      ["host_key_missing", "conflictKey"], ["binding_changed", "conflictBinding"],
      ["binding_identity_changed", "conflictIdentity"], ["binding_modified", "conflictModified"],
      ["group_unavailable", "conflictGroup"], ["invalid_restriction", "conflictRestriction"],
    ]);
    const binding = inventory?.catalog.entries.find((entry) => entry.binding?.id === conflict.binding_id)?.binding;
    const change = review?.preview.changes.find((item) => item.binding_id === conflict.binding_id);
    const label = binding?.name || change?.name || conflict.binding_id;
    const reason = t(prefix + (codes.get(conflict.code) ?? "conflictOther"));
    return label ? `${label}: ${reason}` : reason;
  };
  const trapFocus = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") { event.preventDefault(); close(); return; }
    if (event.key !== "Tab") return;
    const focusable = Array.from(dialog.current?.querySelectorAll<HTMLElement>(
      'button:not(:disabled), input:not(:disabled), select:not(:disabled), [tabindex="0"]',
    ) ?? []);
    if (!focusable.length) { event.preventDefault(); dialog.current?.focus(); return; }
    const first = focusable[0], last = focusable[focusable.length - 1];
    if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) {
      event.preventDefault(); last.focus();
    } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === dialog.current)) {
      event.preventDefault(); first.focus();
    }
  };

  return (
    <div className="modal-overlay" onMouseDown={(event) => { if (event.target === event.currentTarget) close(); }}>
      <div className="modal native-batch-modal" role="dialog" aria-modal="true" aria-labelledby={titleID}
        ref={dialog} tabIndex={-1} onKeyDown={trapFocus} aria-busy={pending}>
        <div className="native-batch-header">
          <h3 id={titleID}>{t(prefix + (view === "history" ? "history" : view === "review" ? "review" : "title"))}</h3>
          <button type="button" className="btn sm" disabled={committing} onClick={close}>{t(prefix + "close")}</button>
        </div>
        <p className="native-batch-intro">{t(prefix + "scope")}</p>
        <div ref={feedback} tabIndex={-1} className="native-batch-feedback">
          {error && <p className="error" role="alert">{error}</p>}
          {notice && <p role="status">{notice}</p>}
          {pending && <p className="muted" role="status">{t(prefix + "working")}</p>}
        </div>
        {view === "select" && (
          <>
            {!complete && !pending && <p className="error" role="alert">{t(prefix + "incomplete")}</p>}
            <div className="native-batch-selection">
              <section aria-labelledby="native-batch-keys-title">
                <h4 id="native-batch-keys-title">{t(prefix + "keys")}</h4>
                <label className="sr-only" htmlFor="native-batch-key-search">{t(prefix + "keySearch")}</label>
                <input id="native-batch-key-search" type="search" value={keyQuery} disabled={pending}
                  onChange={(event) => setKeyQuery(event.target.value)} placeholder={t(prefix + "keySearch")} />
                <div className="native-batch-selection-tools">
                  <span>{t(prefix + "selectedKeys", { count: selectedKeys.size, total: keyEntries.length })}</span>
                  <button type="button" className="btn sm" disabled={pending || !visibleKeys.length} onClick={() => setSelectedKeys((previous) => {
                    const next = new Set(previous);
                    for (const entry of visibleKeys) next.add(inventory!.apiKeys[entry.key_index]);
                    return next;
                  })}>{t(prefix + "selectResults")}</button>
                  <button type="button" className="btn sm" disabled={pending || !selectedKeys.size} onClick={() => setSelectedKeys(new Set())}>{t(prefix + "clear")}</button>
                </div>
                <div className="native-batch-options">
                  {visibleKeys.map((entry) => (
                    <label className="native-batch-key" key={entry.key_index}>
                      <input type="checkbox" checked={selectedKeys.has(inventory!.apiKeys[entry.key_index])} disabled={pending}
                        onChange={() => selectKey(inventory!.apiKeys[entry.key_index])} />
                      <span><strong>{entry.binding?.name || entry.binding?.id || t(prefix + "keyNumber", { count: entry.key_index + 1 })}</strong>
                        <span className="mono">{entry.key_preview}</span>
                        {!entry.binding && <small>{t(prefix + "willCreate")}</small>}
                        {entry.binding && !entry.binding.enabled && <small>{t(prefix + "staysDisabled")}</small>}
                      </span>
                    </label>
                  ))}
                  {!visibleKeys.length && <p className="muted">{t(prefix + "noKeys")}</p>}
                </div>
              </section>
              <section aria-labelledby="native-batch-credentials-title">
                <h4 id="native-batch-credentials-title">{t(prefix + "credentials")}</h4>
                <label className="sr-only" htmlFor="native-batch-credential-search">{t(prefix + "credentialSearch")}</label>
                <input id="native-batch-credential-search" type="search" value={credentialQuery} disabled={pending}
                  onChange={(event) => setCredentialQuery(event.target.value)} placeholder={t(prefix + "credentialSearch")} />
                <div className="native-batch-selection-tools">
                  <span>{t(prefix + "selectedCredentials", { count: authIDs.size })}</span>
                  <button type="button" className="btn sm" disabled={pending || !complete || !visibleCredentials.length} onClick={() => setAuthIDs((previous) => {
                    const next = new Set(previous);
                    for (const item of visibleCredentials) if (item.identityVerified !== false) next.add(item.id);
                    return next;
                  })}>{t(prefix + "selectResults")}</button>
                  <button type="button" className="btn sm" disabled={pending || !authIDs.size} onClick={() => setAuthIDs(new Set())}>{t(prefix + "clear")}</button>
                </div>
                <div className="native-batch-options">
                  {visibleCredentials.map((item) => (
                    <label className="native-batch-credential" key={item.id}>
                      <input type="checkbox" checked={authIDs.has(item.id)} disabled={pending || !complete || item.identityVerified === false}
                        onChange={() => selectCredential(item.id)} />
                      <span><strong>{identityText(item.id)}</strong><span className="mono">{item.id}</span>
                        <small>{[item.provider, item.provider === "codex" ? PLAN_LABELS.get(item.plan?.toLowerCase() ?? "") : undefined].filter(Boolean).join(" · ")}</small>
                      </span>
                    </label>
                  ))}
                  {!visibleCredentials.length && <p className="muted">{t(prefix + "noCredentials")}</p>}
                </div>
              </section>
            </div>
            <div className="native-batch-actions">
              <button type="button" className="btn primary" disabled={pending || !complete || !selectedKeys.size || !authIDs.size} onClick={() => { void previewBatch(); }}>{t(prefix + "preview")}</button>
              <button type="button" className="btn" disabled={pending} onClick={() => { void run(async () => {
                const fresh = await freshInventory();
                setSelectedKeys((previous) => new Set([...previous].filter((key) => fresh.apiKeys.includes(key))));
                setAuthIDs((previous) => new Set([...previous].filter((id) => fresh.credentials.credentials.some((item) => item.id === id))));
              }); }}>{t(prefix + "refresh")}</button>
              <button type="button" className="btn" disabled={pending} onClick={() => { void showHistory(); }}>{t(prefix + "history")}</button>
            </div>
          </>
        )}
        {view === "review" && (
          <>
            {review && <>
              <p>{t(prefix + "changeCount", { count: review.preview.changes.length })}</p>
              {review.operationID && <p className="native-batch-intro">{t(prefix + "restoreScope")}</p>}
              {!!review.preview.conflicts.length && <div className="error" role="alert"><p>{t(prefix + "conflict")}</p>
                <ul>{review.preview.conflicts.map((conflict, index) => <li key={index}>{conflictLabel(conflict)}</li>)}</ul>
              </div>}
              {review.preview.noop && <p role="status">{t(prefix + "noop")}</p>}
              {!!review.preview.warnings.length && <p className="native-batch-new">{t(prefix + "reviewWarnings")}</p>}
              {changes(review.preview.changes, !review.operationID)}
            </>}
            <div className="native-batch-actions">
              <button type="button" className="btn primary" disabled={pending || !review?.preview.can_apply || review.preview.noop || !!review.preview.conflicts.length}
                onClick={() => { void commit(); }}>{t(prefix + (review?.operationID ? "confirmRestore" : "confirm"))}</button>
              <button type="button" className="btn" disabled={pending} onClick={restart}>{t(prefix + "back")}</button>
              <button type="button" className="btn" disabled={pending} onClick={() => { void showHistory(); }}>{t(prefix + "history")}</button>
            </div>
          </>
        )}
        {view === "history" && (
          <>
            <p className="native-batch-intro">{t(prefix + "historyHint")}</p>
            <div className="native-batch-history">
              <div className="native-batch-operation-list">
                {operations.map((item) => (
                  <button type="button" className={`native-batch-operation${operation?.id === item.id ? " active" : ""}`}
                    key={item.id} disabled={pending} onClick={() => setOperation(item)} aria-pressed={operation?.id === item.id}>
                    <strong>{t(prefix + (item.kind === "batch" ? "batchKind" : "rollbackKind"))}</strong>
                    <time dateTime={item.created_at}>{new Date(item.created_at).toLocaleString()}</time>
                    <span>{t(prefix + "changeCount", { count: item.changes.length })}</span>
                    {item.reverted_by && <small>{t(prefix + "reverted")}</small>}
                  </button>
                ))}
                {!operations.length && !pending && <p className="muted">{t(prefix + "emptyHistory")}</p>}
              </div>
              <div className="native-batch-operation-detail">
                {operation ? <>
                  <h4>{t(prefix + "details")}</h4>
                  <p className="mono">{operation.id}</p>
                  {operation.source_operation_id && <p className="muted">{t(prefix + "sourceOperation")} <span className="mono">{operation.source_operation_id}</span></p>}
                  {changes(operation.changes, operation.kind === "batch")}
                  <p>{t(prefix + "restoreBefore")}</p>
                  <button type="button" className="btn" disabled={pending} onClick={() => { void previewRestore(operation); }}>{t(prefix + "previewRestore")}</button>
                </> : <p className="muted">{t(prefix + "chooseOperation")}</p>}
              </div>
            </div>
            <div className="native-batch-actions">
              <button type="button" className="btn" disabled={pending} onClick={restart}>{t(prefix + "back")}</button>
              <button type="button" className="btn" disabled={pending} onClick={() => { void showHistory(); }}>{t(prefix + "refreshHistory")}</button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
