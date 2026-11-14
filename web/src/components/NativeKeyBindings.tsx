import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import { useT } from "../i18n";
import NativeModelAccessPicker from "./NativeModelAccessPicker";
import WeeklyQuotaRemaining from "./WeeklyQuotaRemaining";
import NativeBindingBatch from "./NativeBindingBatch";
import NativeQuotaReset from "./NativeQuotaReset";
import { copyTextToClipboard } from "../clipboard";
import { createNativeKeyNotesClient, currentNativeKeyNotes, nativeKeyNote } from "../api/nativeKeyNotes";
import { getSession } from "../store/session";
import {
  createNativeKeyBinding,
  deleteNativeKeyBinding,
  fetchNativeKeyBindingCatalog,
  fetchNativeBindingCredentialCatalog,
  fetchNativeCredentialOptions,
  fetchTopLevelAPIKeys,
  resetNativeKeyBindingQuota,
  updateNativeKeyBinding,
} from "../api/mappings";
import type {
  NativeCredentialOption,
  NativeBindingCredentialCatalog,
  NativeKeyBinding,
  NativeKeyBindingCatalog,
  NativeModelAccessPolicy,
} from "../types";

function messageFromError(error: unknown): string {
  if (error && typeof error === "object") {
    const candidate = error as {
      message?: unknown;
      response?: { data?: { error?: { message?: unknown } } };
    };
    const apiMessage = candidate.response?.data?.error?.message;
    if (typeof apiMessage === "string" && apiMessage.trim()) return apiMessage;
    if (typeof candidate.message === "string" && candidate.message.trim()) return candidate.message;
  }
  return String(error);
}

interface NativeKeyRow {
  rowKey: string;
  present: boolean;
  keyPreview: string;
  // Plaintext exists only for a current host entry and stays in React memory.
  // Never display it on the card or use it in an attribute, URL, log, or storage.
  apiKey?: string;
  topLevelIndex?: number;
  binding?: NativeKeyBinding;
}

export function buildNativeKeyRows(apiKeys: string[], catalog: NativeKeyBindingCatalog): NativeKeyRow[] {
  const rows: NativeKeyRow[] = [];
  const entries = [...catalog.entries].sort((a, b) => a.key_index - b.key_index);
  for (const entry of entries) {
    const apiKey = apiKeys[entry.key_index];
    if (typeof apiKey !== "string" || !apiKey) continue;
    rows.push({
      rowKey: `host:${entry.key_index}`,
      present: true,
      keyPreview: entry.key_preview || "<redacted>",
      apiKey,
      topLevelIndex: entry.key_index,
      binding: entry.binding ?? undefined,
    });
  }
  for (const binding of catalog.orphan_bindings) {
    rows.push({
      rowKey: `orphan:${binding.id}`,
      present: false,
      keyPreview: binding.key_preview || "<redacted>",
      binding,
    });
  }
  return rows;
}

interface SelectedTopLevelKey {
  apiKey: string;
  keyPreview: string;
}

type EditorState =
  | {
      mode: "create";
      selectedKey?: SelectedTopLevelKey;
      initialID?: string;
    }
  | { mode: "edit"; binding: NativeKeyBinding; currentKey?: string };

function suggestedBindingID(index: number, rows: NativeKeyRow[]): string {
  const ids = new Set(rows.flatMap((row) => row.binding ? [row.binding.id.toLowerCase()] : []));
  const base = `native-key-${index + 1}`;
  if (!ids.has(base)) return base;
  let suffix = 2;
  while (ids.has(`${base}-${suffix}`)) suffix++;
  return `${base}-${suffix}`;
}

function NativeKeyCopyButton({ apiKey }: { apiKey?: string }) {
  const t = useT();
  const [status, setStatus] = useState<"idle" | "copying" | "copied" | "failed">("idle");
  useEffect(() => { setStatus("idle"); }, [apiKey]);

  const copy = async () => {
    if (!apiKey) return;
    setStatus("copying");
    try {
      await copyTextToClipboard(apiKey);
      setStatus("copied");
    } catch {
      // Clipboard errors are not user content and must never echo key material.
      setStatus("failed");
    }
  };

  return (
    <>
      <button
        className="btn sm native-key-copy"
        type="button"
        disabled={!apiKey || status === "copying"}
        title={t(apiKey ? "mapping.native.copyKey" : "mapping.native.copyKeyUnavailable")}
        onClick={() => { void copy(); }}
      >
        {t(status === "copied" ? "mapping.native.keyCopied" : "mapping.native.copyKey")}
      </button>
      {status === "copied" && <span className="sr-only" role="status">{t("mapping.native.keyCopied")}</span>}
      {status === "failed" && (
        <span className="native-key-copy-status" role="alert">{t("mapping.native.copyKeyFailed")}</span>
      )}
    </>
  );
}

const CODEX_PLAN_LABELS = new Map([
  ["free", "Free"], ["plus", "Plus"], ["team", "Team"],
  ["pro", "Pro"], ["enterprise", "Enterprise"], ["edu", "Edu"],
]);

function BoundCredentialPanel({ binding, catalog, loading, failed }: {
  binding: NativeKeyBinding;
  catalog: NativeBindingCredentialCatalog | null;
  loading: boolean;
  failed: boolean;
}) {
  const t = useT();
  const contentID = useId();
  const [expanded, setExpanded] = useState(false);
  if (binding.needs_reselection) return null;
  const ids = Array.from(new Set(binding.auth_ids ?? []));
  const legacyGroup = ids.length === 0 ? binding.group?.trim() ?? "" : "";
  const byID = new Map(catalog?.credentials.map((credential) => [credential.id, credential]) ?? []);
  const hint = legacyGroup ? t("mapping.native.legacyGroup", { group: legacyGroup })
    : loading ? t("mapping.native.loadingCredentials")
    : failed || !catalog ? t("mapping.native.credentialsLoadFailed") : "";
  const count = legacyGroup ? undefined : ids.length;
  const heading = <>{t("mapping.native.boundAccounts")}{count !== undefined && (
    <span className="native-binding-account-count">{count}</span>
  )}</>;

  return (
    <section className={`native-binding-account-panel${expanded ? " expanded" : ""}`}>
      <h3 className="native-binding-account-heading">{heading}</h3>
      <button
        className="native-binding-account-toggle"
        type="button"
        aria-expanded={expanded}
        aria-controls={contentID}
        onClick={() => setExpanded((value) => !value)}
      >
        <span>{heading}</span>
        <span className="native-binding-account-toggle-label">
          {t(expanded ? "mapping.native.hideAccounts" : "mapping.native.showAccounts")}
          <span className="native-binding-account-chevron" aria-hidden="true" />
        </span>
      </button>
      <div className="native-binding-account-content" id={contentID}>
        {hint ? <p className="native-binding-field-hint">{hint}</p> : (
          <ul className="native-binding-credentials" aria-label={t("mapping.native.boundCredentials")} tabIndex={0}>
            {ids.map((id) => {
              const credential = byID.get(id);
              const identity = credential?.email || (credential?.source === "ai_provider"
                ? credential.name || credential.label : credential?.label || credential?.name) || id;
              const plan = credential?.provider === "codex" && credential.plan
                ? CODEX_PLAN_LABELS.get(credential.plan.trim().toLowerCase()) : undefined;
              return (
                <li className="native-binding-credential" key={id}>
                  <span className="native-binding-credential-identity">{identity}</span>
                  {plan && <span className="native-binding-credential-plan">{plan}</span>}
                  {!credential && (
                    <span className="muted">
                      {t(catalog?.identitiesComplete === false
                        ? "mapping.native.credentialsLoadFailed"
                        : "mapping.native.credentialMissing")}
                    </span>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </section>
  );
}

export default function NativeKeyBindingsTab() {
  const t = useT();
  const [rows, setRows] = useState<NativeKeyRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [editor, setEditor] = useState<EditorState | null>(null);
  const [batchView, setBatchView] = useState<"select" | "history" | "undo" | null>(null);
  const [quotaResetOpen, setQuotaResetOpen] = useState(false);
  const [noticeExpanded, setNoticeExpanded] = useState(false);
  const noticeID = useId();
  const [pendingID, setPendingID] = useState("");
  const [credentialCatalog, setCredentialCatalog] = useState<NativeBindingCredentialCatalog | null>(null);
  const [credentialCatalogLoading, setCredentialCatalogLoading] = useState(true);
  const [credentialCatalogFailed, setCredentialCatalogFailed] = useState(false);
  const [keyNotes, setKeyNotes] = useState<ReadonlyMap<string, string>>(() => new Map());
  const [nameSyncFailed, setNameSyncFailed] = useState(false);
  const loadVersion = useRef(0);

  const load = useCallback(async () => {
    const version = ++loadVersion.current;
    const session = getSession();
    const isCurrent = () => version === loadVersion.current && getSession() === session;
    setLoading(true);
    setError("");
    setKeyNotes(new Map());
    setNameSyncFailed(false);
    setCredentialCatalog(null);
    setCredentialCatalogLoading(true);
    setCredentialCatalogFailed(false);
    try {
      const notesClient = createNativeKeyNotesClient();
      const pendingNotes = notesClient.read();
      const apiKeys = await fetchTopLevelAPIKeys();
      if (!isCurrent()) return;
      const catalog = await fetchNativeKeyBindingCatalog(apiKeys);
      if (!isCurrent()) return;
      const nextRows = buildNativeKeyRows(apiKeys, catalog);
      setRows(nextRows);
      void pendingNotes.then(async (notes) => {
        if (!isCurrent()) return;
        const currentNotes = currentNativeKeyNotes(notes, apiKeys);
        setKeyNotes(currentNotes);
        const updates = nextRows.flatMap(({ apiKey, binding }) => {
          if (!apiKey || !binding) return [];
          const name = nativeKeyNote(currentNotes, apiKey);
          return name && name !== binding.name
            ? [{ id: binding.id, key: apiKey, expected_name: binding.name, name }]
            : [];
        });
        if (!updates.length) return;
        const result = await notesClient.sync(updates);
        if (!isCurrent()) return;
        const refreshed = await fetchNativeKeyBindingCatalog(apiKeys);
        if (!isCurrent()) return;
        setRows(buildNativeKeyRows(apiKeys, refreshed));
        setNameSyncFailed(result.skipped_ids.length > 0);
      }).catch(() => {
        if (isCurrent()) setNameSyncFailed(true);
      });
      if (nextRows.some((row) => row.binding)) {
        // Identity metadata is independent of key management. A failed lookup
        // must not block copying keys or editing their existing restrictions.
        void fetchNativeBindingCredentialCatalog().then((nextCatalog) => {
          if (isCurrent()) setCredentialCatalog(nextCatalog);
        }).catch(() => {
          if (isCurrent()) setCredentialCatalogFailed(true);
        }).finally(() => {
          if (isCurrent()) setCredentialCatalogLoading(false);
        });
      } else {
        setCredentialCatalogLoading(false);
      }
    } catch (e: unknown) {
      if (isCurrent()) {
        setError(messageFromError(e));
        setCredentialCatalogLoading(false);
        setCredentialCatalogFailed(true);
      }
    } finally {
      if (isCurrent()) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
    return () => { loadVersion.current++; };
  }, [load]);

  const topLevelCount = rows.filter((row) => row.present).length;
  const boundCount = rows.filter((row) => row.present && row.binding).length;

  const toggleBinding = async (binding: NativeKeyBinding) => {
    // Disabling removes this scheduling restriction but does not revoke CPA's
    // own top-level key. Confirm that fail-open consequence at the exact point
    // of action; re-enabling the restriction does not need a prompt.
    if (binding.enabled && !window.confirm(t("mapping.native.disableConfirm", { id: binding.id }))) return;
    setPendingID(binding.id);
    setError("");
    try {
      await updateNativeKeyBinding({ id: binding.id, enabled: !binding.enabled });
      await load();
    } catch (e: unknown) {
      setError(messageFromError(e));
    } finally {
      setPendingID("");
    }
  };

  const removeBinding = async (binding: NativeKeyBinding) => {
    if (!window.confirm(t("mapping.native.deleteConfirm", { id: binding.id }))) return;
    setPendingID(binding.id);
    setError("");
    try {
      await deleteNativeKeyBinding(binding.id);
      await load();
    } catch (e: unknown) {
      setError(messageFromError(e));
    } finally {
      setPendingID("");
    }
  };

  const resetQuota = async (binding: NativeKeyBinding) => {
    if (!window.confirm(t("mapping.native.resetQuotaConfirm", { id: binding.id }))) return;
    setPendingID(binding.id);
    setError("");
    try {
      await resetNativeKeyBindingQuota(binding.id);
      await load();
    } catch (e: unknown) {
      setError(messageFromError(e));
    } finally {
      setPendingID("");
    }
  };

  const bindTopLevelKey = (row: NativeKeyRow) => {
    if (!row.present || !row.apiKey || row.topLevelIndex === undefined || row.binding) return;
    setEditor({
      mode: "create",
      selectedKey: { apiKey: row.apiKey, keyPreview: row.keyPreview },
      initialID: suggestedBindingID(row.topLevelIndex, rows),
    });
  };
  const currentEditorBinding = editor?.mode === "edit"
    ? rows.find((row) => row.binding?.id === editor.binding.id && row.apiKey === editor.currentKey)?.binding
    : undefined;

  return (
    <>
      <div className={`native-binding-notice native-binding-help${noticeExpanded ? " expanded" : ""}`} role="note">
        <div className="native-binding-notice-icon" aria-hidden="true">ⓘ</div>
        <div className="native-binding-help-body">
          <strong className="native-binding-help-title">{t("mapping.native.noticeTitle")}</strong>
          <button type="button" className="native-binding-help-toggle"
            aria-expanded={noticeExpanded} aria-controls={noticeID}
            onClick={() => setNoticeExpanded((value) => !value)}>
            <span>{t("mapping.native.mobileNoticeTitle")}</span>
            <span className="native-binding-help-label">{t(noticeExpanded ? "mapping.native.hideNotice" : "mapping.native.showNotice")}</span>
          </button>
          <p className="native-binding-help-summary">{t("mapping.native.mobileNoticeSummary")}</p>
          <div className="native-binding-help-details" id={noticeID}>
            <p>{t("mapping.native.noticeBody")}</p>
            <p className="native-binding-unbind-warning">
              <span aria-hidden="true">⚠ </span>{t("mapping.native.unbindNotice")}
            </p>
          </div>
        </div>
      </div>

      <div className="map-toolbar native-binding-toolbar">
        <span className="muted native-binding-summary">
          {t("mapping.native.summary", { total: topLevelCount, bound: boundCount })}
        </span>
        <button className="btn" type="button" disabled={loading} onClick={() => { void load(); }}>
          {t("mapping.native.refresh")}
        </button>
        <button className="btn" type="button" disabled={loading} onClick={() => setBatchView("select")}>
          {t("mapping.nativeBatch.title")}
        </button>
        <button className="btn" type="button" disabled={loading} onClick={() => setBatchView("undo")}>
          {t("mapping.nativeBatch.undoLast")}
        </button>
        <button className="btn" type="button" disabled={loading} onClick={() => setBatchView("history")}>
          {t("mapping.nativeBatch.history")}
        </button>
        <button className="btn" type="button" disabled={loading} onClick={() => setQuotaResetOpen(true)}>
          {t("mapping.nativeQuotaReset.title")}
        </button>
      </div>

      {error && <div className="error" role="alert">{error}</div>}
      {nameSyncFailed && <div className="error" role="alert">{t("mapping.native.nameSyncFailed")}</div>}
      {loading ? (
        <div className="muted native-binding-empty">{t("mapping.loading")}</div>
      ) : rows.length === 0 && error ? (
        <div className="muted native-binding-empty">{t("mapping.native.loadFailed")}</div>
      ) : rows.length === 0 ? (
        <div className="native-binding-empty">
          <strong>{t("mapping.native.emptyTitle")}</strong>
          <span className="muted">{t("mapping.native.emptyBody")}</span>
        </div>
      ) : (
        <div className="native-binding-grid">
          {rows.map((row) => {
            const binding = row.binding;
            const pending = !!binding && pendingID === binding.id;
            const status = !row.present
              ? "orphan"
              : !binding
                ? "unbound"
                : binding.enabled
                  ? "enabled"
                  : "disabled";
            const ordinal = (row.topLevelIndex ?? 0) + 1;
            const note = nativeKeyNote(keyNotes, row.apiKey);
            const displayName = note || binding?.name || (!binding ? t("mapping.native.topLevelKeyName", { index: ordinal }) : "");
            return (
              <article
                className={`native-binding-card ${status}`}
                key={row.rowKey}
                data-testid={`native-key-row-${status}`}
              >
                <div className="native-binding-card-head">
                  <div className="native-binding-identity">
                    {displayName && <strong>{displayName}</strong>}
                    <span className="mono">
                      {binding?.id || t("mapping.native.topLevelPosition", { index: ordinal })}
                    </span>
                  </div>
                  <span className={`native-binding-status ${status}`}>
                    {t(`mapping.native.${status}`)}
                  </span>
                </div>
                <div className={`native-binding-card-body${binding && !binding.needs_reselection ? " has-accounts" : ""}`}>
                  <div className="native-binding-details">
                    <dl className="native-binding-meta">
                      <div>
                        <dt>{t("mapping.native.keyPreview")}</dt>
                        <dd className="native-key-preview-row">
                          <span className="mono">{row.keyPreview}</span>
                          <NativeKeyCopyButton apiKey={row.apiKey} />
                        </dd>
                      </div>
                      <div>
                        <dt>{t("mapping.native.restriction")}</dt>
                        <dd>
                          <span className={`native-binding-group mono${binding ? "" : " unrestricted"}${binding?.needs_reselection ? " needs-reselection" : ""}`}>
                            {binding?.group?.trim() && !binding.auth_ids?.length
                              ? t("mapping.native.legacyGroup", { group: binding.group.trim() })
                              : binding?.needs_reselection
                              ? t("mapping.native.directReselectionSummary")
                              : binding?.auth_ids?.length
                              ? t("mapping.native.directSummary", { count: binding.auth_ids.length })
                              : t("mapping.native.defaultScheduling")}
                          </span>
                        </dd>
                      </div>
                      {binding && (
                        <div>
                          <dt>{t("mapping.native.modelAccess")}</dt>
                          <dd>
                            <span className={`native-binding-group mono${binding.model_access?.mode === "allowlist" ? "" : " unrestricted"}`}>
                              {binding.model_access?.mode === "allowlist"
                                ? (binding.model_access.models?.length ?? 0) > 0
                                  ? t("mapping.native.modelSummary", { count: binding.model_access.models?.length ?? 0 })
                                  : t("mapping.native.noModelsAllowed")
                                : t("mapping.native.allModels")}
                            </span>
                          </dd>
                        </div>
                      )}
                      {binding?.enabled && binding.round_robin && (
                        <div>
                          <dt>{t("mapping.native.requestRouting")}</dt>
                          <dd>{t("mapping.native.roundRobin")}</dd>
                        </div>
                      )}
                    </dl>
                    {!row.present && (
                      <p className="native-binding-orphan-note">{t("mapping.native.orphanHint")}</p>
                    )}
                    {binding?.enabled && binding.usage && (
                      <dl className="native-binding-meta native-binding-usage">
                        {binding.usage.rpm_limit > 0 && (
                          <div>
                            <dt>{t("mapping.native.rpm")}</dt>
                            <dd className={`mono${binding.usage.rpm_used >= binding.usage.rpm_limit ? " native-quota-full" : ""}`}>
                              {binding.usage.rpm_used}/{binding.usage.rpm_limit}
                            </dd>
                          </div>
                        )}
                        {binding.usage.daily_usd_limit > 0 && (
                          <div>
                            <dt>{t("mapping.native.dailyUsd")}</dt>
                            <dd className={`mono${binding.usage.daily_usd_used >= binding.usage.daily_usd_limit ? " native-quota-full" : ""}`}>
                              ${binding.usage.daily_usd_used.toFixed(2)}/${binding.usage.daily_usd_limit.toFixed(2)}
                            </dd>
                          </div>
                        )}
                        {(binding.usage.daily_calls > 0 || binding.usage.weekly_calls > 0) && (
                          <div>
                            <dt>{t("mapping.native.calls")}</dt>
                            <dd className="mono">
                              {binding.usage.daily_calls}/{binding.usage.weekly_calls}
                            </dd>
                          </div>
                        )}
                      </dl>
                    )}
                  </div>
                  {binding && (
                    <BoundCredentialPanel
                      binding={binding}
                      catalog={credentialCatalog}
                      loading={credentialCatalogLoading}
                      failed={credentialCatalogFailed}
                    />
                  )}
                </div>
                {binding?.enabled && binding.usage && <WeeklyQuotaRemaining usage={binding.usage} />}
                <div className="native-binding-actions">
                  {binding ? (
                    <>
                      <label className="switch" title={t("mapping.native.toggleLabel", { id: binding.id })}>
                        <input
                          type="checkbox"
                          checked={binding.enabled}
                          disabled={pending}
                          onChange={() => { void toggleBinding(binding); }}
                          aria-label={t("mapping.native.toggleLabel", { id: binding.id })}
                        />
                        <span className="track"><span className="thumb" /></span>
                      </label>
                      <button
                        className="btn sm"
                        type="button"
                        disabled={pending}
                        onClick={() => { void resetQuota(binding); }}
                      >
                        {t("mapping.native.resetQuota")}
                      </button>
                      <button
                        className="btn sm"
                        type="button"
                        disabled={pending}
                        onClick={() => setEditor({ mode: "edit", binding, currentKey: row.apiKey })}
                      >
                        {t("mapping.native.editRotate")}
                      </button>
                      <button
                        className="btn sm danger-outline"
                        type="button"
                        disabled={pending}
                        onClick={() => { void removeBinding(binding); }}
                      >
                        {t("mapping.delete")}
                      </button>
                    </>
                  ) : (
                    <>
                      <span className="muted native-binding-free-hint">
                        {t("mapping.native.unboundHint")}
                      </span>
                      <button
                        className="btn sm primary"
                        type="button"
                        onClick={() => bindTopLevelKey(row)}
                      >
                        {t("mapping.native.bindThisKey")}
                      </button>
                    </>
                  )}
                </div>
              </article>
            );
          })}
        </div>
      )}

      {batchView && <NativeBindingBatch initialView={batchView} onClose={() => setBatchView(null)} onChanged={load} />}
      {quotaResetOpen && <NativeQuotaReset onClose={() => setQuotaResetOpen(false)} onReset={load} />}
      {editor && (
        <NativeKeyBindingEditor
          key={editor.mode === "edit" ? editor.binding.id : editor.initialID || "__new"}
          binding={editor.mode === "edit" ? editor.binding : undefined}
          currentKey={editor.mode === "edit" ? editor.currentKey : undefined}
          persistedName={currentEditorBinding?.name}
          keyNotes={keyNotes}
          selectedKey={editor.mode === "create" ? editor.selectedKey : undefined}
          initialID={editor.mode === "create" ? editor.initialID : undefined}
          onCancel={() => setEditor(null)}
          onSaved={async () => {
            setEditor(null);
            await load();
          }}
        />
      )}
    </>
  );
}

interface EditorProps {
  binding?: NativeKeyBinding;
  currentKey?: string;
  persistedName?: string;
  keyNotes: ReadonlyMap<string, string>;
  selectedKey?: SelectedTopLevelKey;
  initialID?: string;
  onCancel: () => void;
  onSaved: () => Promise<void>;
}

function NativeKeyBindingEditor({
  binding,
  currentKey,
  persistedName,
  keyNotes,
  selectedKey,
  initialID,
  onCancel,
  onSaved,
}: EditorProps) {
  const t = useT();
  const editing = !!binding;
  const [id, setID] = useState(binding?.id ?? initialID ?? "");
  const [nameDraft, setNameDraft] = useState<string>();
  const [enabled, setEnabled] = useState(binding?.enabled ?? true);
  const [roundRobin, setRoundRobin] = useState(binding?.round_robin ?? false);
  const [plainKey, setPlainKey] = useState("");
  const [rpm, setRpm] = useState(binding?.rpm ? String(binding.rpm) : "");
  const [dailyUsd, setDailyUsd] = useState(binding?.daily_usd ? String(binding.daily_usd) : "");
  const [weeklyUsd, setWeeklyUsd] = useState(binding?.weekly_usd ? String(binding.weekly_usd) : "");
  const [selectedAuthIDs, setSelectedAuthIDs] = useState<Set<string>>(() => new Set(binding?.auth_ids ?? []));
  const [modelAccess, setModelAccess] = useState<NativeModelAccessPolicy>(() => {
    if (binding?.model_access) {
      const models = binding.model_access.models ?? [];
      return {
        mode: binding.model_access.mode,
        models: models.map((model) => ({ ...model })),
      };
    }
    // A pre-fork.13 binding had unrestricted model access. New bindings start
    // with an empty allow-list so only explicitly checked models can run.
    return binding
      ? { mode: "all", models: [] }
      : { mode: "allowlist", models: [] };
  });
  const [credentialOptions, setCredentialOptions] = useState<NativeCredentialOption[]>([]);
  const [credentialQuery, setCredentialQuery] = useState("");
  const [credentialsLoading, setCredentialsLoading] = useState(true);
  const [credentialError, setCredentialError] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const loadCredentialOptions = useCallback(async () => {
    setCredentialsLoading(true);
    setCredentialError("");
    try {
      setCredentialOptions(await fetchNativeCredentialOptions());
    } catch {
      // Management failures may contain configuration values; never echo them.
      setCredentialError(t("mapping.native.credentialLoadFailed"));
    } finally {
      setCredentialsLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void loadCredentialOptions();
  }, [loadCredentialOptions]);

  const parseLimitNumber = (raw: string): number | undefined => {
    const trimmed = raw.trim();
    if (!trimmed) return 0; // empty input = explicitly clear the limit
    const value = Number(trimmed);
    if (!Number.isFinite(value) || value < 0) return undefined; // invalid → block save
    return value;
  };
  const rpmValue = parseLimitNumber(rpm);
  const dailyValue = parseLimitNumber(dailyUsd);
  const weeklyValue = parseLimitNumber(weeklyUsd);
  const limitsValid = rpmValue !== undefined && dailyValue !== undefined && weeklyValue !== undefined;
  // Omit limit fields entirely when neither the form nor the existing binding
  // carries a limit — keeps the wire payload identical to the pre-limits UI.
  // An explicit 0 is sent only when clearing a previously configured limit.
  const hadLimits = !!(binding && (binding.rpm || binding.daily_usd || binding.weekly_usd));
  const limitField = (value: number | undefined) =>
    value !== undefined && (value > 0 || hadLimits) ? value : undefined;

  const authIDs = useMemo(() => Array.from(selectedAuthIDs).sort(), [selectedAuthIDs]);
  const credentialOptionsByID = useMemo(
    () => new Map(credentialOptions.map((option) => [option.id, option] as const)),
    [credentialOptions],
  );
  const staleAuthIDs = useMemo(
    () => authIDs.filter((authID) => !credentialOptionsByID.has(authID)),
    [authIDs, credentialOptionsByID],
  );
  const credentialModelCatalog = useMemo(() => {
    const rows: Array<{ provider: string; model: string }> = [];
    for (const authID of authIDs) {
      const option = credentialOptionsByID.get(authID);
      if (!option) continue;
      for (const model of option.models ?? []) {
        rows.push({ provider: option.provider, model });
      }
    }
    return rows;
  }, [authIDs, credentialOptionsByID]);
  const visibleCredentialOptions = useMemo(() => {
    const query = credentialQuery.trim().toLowerCase();
    if (!query) return credentialOptions;
    return credentialOptions.filter((option) => [
      option.id,
      option.provider,
      option.name,
      option.label,
      option.email,
      option.status,
      option.plan,
      option.authIndex,
    ].some((value) => value?.toLowerCase().includes(query)));
  }, [credentialOptions, credentialQuery]);
  const visibleCredentialGroups = useMemo(() => {
    const authFiles: NativeCredentialOption[] = [];
    const aiProviders: NativeCredentialOption[] = [];
    for (const option of visibleCredentialOptions) {
      if (option.source === "ai_provider") aiProviders.push(option);
      else authFiles.push(option);
    }
    return [
      { source: "auth_file" as const, options: authFiles },
      { source: "ai_provider" as const, options: aiProviders },
    ].filter((group) => group.options.length > 0 || (group.source === "ai_provider"
      && !credentialQuery.trim() && !credentialsLoading && !credentialError));
  }, [visibleCredentialOptions, credentialQuery, credentialsLoading, credentialError]);
  const selectableVisibleCredentialOptions = useMemo(
    () => visibleCredentialOptions.filter((option) => option.identityVerified !== false),
    [visibleCredentialOptions],
  );
  const toggleAuthID = (authID: string) => {
    setSelectedAuthIDs((previous) => {
      const next = new Set(previous);
      if (next.has(authID)) next.delete(authID);
      else next.add(authID);
      return next;
    });
  };
  const selectVisibleCredentials = () => {
    setSelectedAuthIDs((previous) => {
      const next = new Set(previous);
      for (const option of selectableVisibleCredentialOptions) next.add(option.id);
      return next;
    });
  };
  const clearCredentials = () => setSelectedAuthIDs(new Set());
  const createKey = selectedKey?.apiKey ?? plainKey;
  const replacementKey = plainKey.trim();
  const nameNote = nativeKeyNote(keyNotes, editing ? replacementKey || currentKey : createKey);
  const name = nameNote ?? nameDraft ?? persistedName ?? binding?.name ?? "";
  const canSave = limitsValid && id.trim() !== "" && authIDs.length > 0 && (editing || createKey.trim() !== "");

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!canSave) return;
    // The editor can disable an existing binding just like the card switch.
    // Keep the same explicit fail-open warning on every path that removes the
    // scheduling restriction from a still-valid CPA top-level API key.
    if (binding?.enabled && !enabled && !window.confirm(t("mapping.native.disableConfirm", { id: binding.id }))) return;
    setSaving(true);
    setError("");
    try {
      if (binding) {
        let nameUpdate = nameDraft?.trim();
        if (nameNote) nameUpdate = replacementKey && replacementKey !== currentKey ? nameNote : undefined;
        const input = {
          id: binding.id,
          ...(nameUpdate !== undefined ? { name: nameUpdate } : {}),
          enabled,
          round_robin: roundRobin,
          auth_ids: authIDs,
          model_access: modelAccess,
          rpm: limitField(rpmValue),
          daily_usd: limitField(dailyValue),
          weekly_usd: limitField(weeklyValue),
          ...(replacementKey ? { key: replacementKey } : {}),
        };
        await updateNativeKeyBinding(input);
      } else {
        await createNativeKeyBinding({
          id: id.trim(),
          name: name.trim() || undefined,
          enabled,
          round_robin: roundRobin,
          key: createKey.trim(),
          auth_ids: authIDs,
          model_access: modelAccess,
          rpm: limitField(rpmValue),
          daily_usd: limitField(dailyValue),
          weekly_usd: limitField(weeklyValue),
        });
      }
      // Drop the only editable UI-held plaintext copy before closing. A key
      // selected from the host catalog lives only in the parent modal state,
      // which is destroyed synchronously by onSaved.
      setPlainKey("");
      await onSaved();
    } catch (e: unknown) {
      setError(messageFromError(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="modal-overlay" role="presentation" onMouseDown={(e) => {
      if (e.target === e.currentTarget && !saving) onCancel();
    }}>
      <div className="modal native-binding-editor" role="dialog" aria-modal="true" aria-labelledby="native-binding-editor-title">
        <h3 id="native-binding-editor-title">
          {t(editing ? "mapping.native.editTitle" : "mapping.native.newTitle")}
        </h3>
        <form onSubmit={submit}>
          <div className="map-form-row">
            <label htmlFor="native-binding-id">{t("mapping.native.id")}</label>
            <input
              id="native-binding-id"
              className="mono"
              value={id}
              onChange={(e) => setID(e.target.value)}
              disabled={editing || saving}
              autoFocus={!editing && !initialID}
              autoComplete="off"
              placeholder="client-a"
              required
            />
            <p className="native-binding-field-hint">{t("mapping.native.idHint")}</p>
          </div>
          <div className="map-form-row">
            <label htmlFor="native-binding-name">{t("mapping.native.name")}</label>
            <input
              id="native-binding-name"
              value={name}
              onChange={(e) => setNameDraft(e.target.value)}
              disabled={saving}
              readOnly={nameNote !== undefined}
              aria-describedby={nameNote ? "native-binding-name-source" : undefined}
              autoComplete="off"
            />
            {nameNote && (
              <p id="native-binding-name-source" className="native-binding-field-hint">{t("mapping.native.nameFromCPA")}</p>
            )}
          </div>
          {selectedKey && !editing ? (
            <div className="map-form-row">
              <label>{t("mapping.native.selectedKey")}</label>
              <div className="native-binding-selected-key mono">{selectedKey.keyPreview}</div>
              <p className="native-binding-field-hint">{t("mapping.native.selectedKeyHint")}</p>
            </div>
          ) : (
            <div className="map-form-row">
              <label htmlFor="native-binding-key">
                {t(editing ? "mapping.native.rotateKey" : "mapping.native.plainKey")}
              </label>
              <input
                id="native-binding-key"
                className="mono"
                type="password"
                value={plainKey}
                onChange={(e) => setPlainKey(e.target.value)}
                disabled={saving}
                autoComplete="new-password"
                spellCheck={false}
                placeholder={editing ? t("mapping.native.keepKeyPlaceholder") : "sk-..."}
                required={!editing}
              />
              <p className="native-binding-field-hint">
                {t(editing ? "mapping.native.rotateKeyHint" : "mapping.native.plainKeyHint")}
              </p>
            </div>
          )}
          <div className="map-form-row native-credential-picker-row">
            <label htmlFor="native-credential-search">{t("mapping.native.credentialSelection")}</label>
            <div className="native-credential-toolbar">
              <input
                id="native-credential-search"
                type="search"
                value={credentialQuery}
                onChange={(e) => setCredentialQuery(e.target.value)}
                disabled={saving}
                placeholder={t("mapping.native.credentialSearch")}
              />
              <button
                className="btn sm"
                type="button"
                disabled={saving || credentialsLoading}
                onClick={() => { void loadCredentialOptions(); }}
              >
                {t("mapping.native.refreshCredentials")}
              </button>
            </div>
            <div className="native-credential-summary">
              <span>{t("mapping.native.credentialSelected", { count: authIDs.length })}</span>
              <span className="native-credential-actions">
                <button
                  className="btn sm"
                  type="button"
                  disabled={saving || credentialsLoading || selectableVisibleCredentialOptions.length === 0}
                  onClick={selectVisibleCredentials}
                >
                  {t("mapping.native.selectAllCredentials")}
                </button>
                <button
                  className="btn sm"
                  type="button"
                  disabled={saving || authIDs.length === 0}
                  onClick={clearCredentials}
                >
                  {t("mapping.native.clearCredentials")}
                </button>
              </span>
            </div>
            {credentialError && <div className="error" role="alert">{credentialError}</div>}
            <div className="native-credential-list" role="group" aria-label={t("mapping.native.credentialSelection")}>
              {staleAuthIDs.map((authID) => (
                <label
                  className={`native-credential-option${!credentialsLoading && !credentialError ? " stale" : ""}`}
                  key={`stale:${authID}`}
                >
                  <input
                    type="checkbox"
                    checked
                    onChange={() => toggleAuthID(authID)}
                    disabled={saving}
                  />
                  <span className="native-credential-copy">
                    <strong>{t(!credentialsLoading && !credentialError
                      ? "mapping.native.missingCredential"
                      : "mapping.native.storedCredential")}</strong>
                    <span className="mono">{authID}</span>
                  </span>
                  <span className={`native-credential-status${!credentialsLoading && !credentialError ? " danger" : ""}`}>
                    {t(!credentialsLoading && !credentialError ? "mapping.native.missing" : "mapping.native.stored")}
                  </span>
                </label>
              ))}
              {visibleCredentialGroups.map((group) => (
                <div className="native-credential-source-group" key={group.source}>
                  <div className="native-credential-source-head">
                    <span>{t(group.source === "ai_provider"
                      ? "mapping.native.aiProviderCredentials"
                      : "mapping.native.authFileCredentials")}</span>
                    <small>{group.options.length}</small>
                  </div>
                  {group.options.map((option) => {
                    const displayName = option.source === "ai_provider"
                      ? (option.name && option.name !== option.id ? option.name : option.label) || option.name || option.id
                      : option.label || option.email || option.name || option.id;
                    const normalizedStatus = (option.status ?? "").trim().toLowerCase().replace(/[ -]/g, "_");
                    const identityUnverified = option.identityVerified === false;
                    const unavailable = identityUnverified || option.disabled || option.unavailable || [
                      "disabled",
                      "error",
                      "expired",
                      "revoked",
                      "invalid",
                      "unavailable",
                      "cooldown",
                      "cooling_down",
                      "quota_exhausted",
                      "exhausted",
                      "blocked",
                    ].includes(normalizedStatus);
                    const status = identityUnverified
                      ? t("mapping.native.credentialIdentityUnverified")
                      : option.disabled
                      ? t("mapping.native.credentialDisabled")
                      : option.unavailable
                        ? t("mapping.native.credentialUnavailable")
                        : normalizedStatus === "configured"
                          ? t("mapping.native.credentialConfigured")
                          : normalizedStatus === "" || normalizedStatus === "active"
                            ? t("mapping.native.credentialAvailable")
                            : option.status;
                    const detail = [
                      option.provider,
                      option.plan,
                      option.models ? t("mapping.native.credentialModels", { count: option.models.length }) : "",
                      option.authIndex ? t("mapping.native.credentialIndex", { index: option.authIndex.slice(-8) }) : "",
                    ].filter(Boolean).join(" · ");
                    return (
                      <label className={`native-credential-option${unavailable ? " unavailable" : ""}`} key={option.id}>
                        <input
                          type="checkbox"
                          checked={selectedAuthIDs.has(option.id)}
                          onChange={() => toggleAuthID(option.id)}
                          disabled={saving || identityUnverified}
                        />
                        <span className="native-credential-copy">
                          <strong>{displayName}</strong>
                          <span className="mono">{option.id}</span>
                          <small>{detail}</small>
                        </span>
                        <span className={`native-credential-status${unavailable ? " danger" : ""}`}>{status}</span>
                      </label>
                    );
                  })}
                  {group.source === "ai_provider" && group.options.length === 0 && (
                    <div className="muted native-credential-empty">{t("mapping.native.noAIProviderCredentials")}</div>
                  )}
                </div>
              ))}
              {credentialsLoading && credentialOptions.length === 0 && staleAuthIDs.length === 0 && (
                <div className="muted native-credential-empty">{t("mapping.native.loadingCredentials")}</div>
              )}
              {!credentialsLoading && !credentialError && credentialOptions.length === 0 && staleAuthIDs.length === 0 && (
                <div className="muted native-credential-empty">{t("mapping.native.noCredentials")}</div>
              )}
              {!credentialsLoading && credentialOptions.length > 0 && visibleCredentialOptions.length === 0 && staleAuthIDs.length === 0 && (
                <div className="muted native-credential-empty">{t("mapping.native.noCredentialMatches")}</div>
              )}
            </div>
            <p className="native-binding-field-hint">{t("mapping.native.credentialHint")}</p>
          </div>
          <div className="map-form-row native-model-access-row">
            <NativeModelAccessPicker
              value={modelAccess}
              disabled={saving}
              catalog={credentialModelCatalog}
              loading={credentialsLoading}
              onRefresh={loadCredentialOptions}
              onChange={setModelAccess}
            />
          </div>
          <div className="map-form-row native-binding-routing-row">
            <label className="switch native-binding-enable-switch" htmlFor="native-binding-round-robin">
              <span className="native-binding-enable-label">{t("mapping.native.roundRobin")}</span>
              <input
                id="native-binding-round-robin"
                type="checkbox"
                checked={roundRobin}
                onChange={(e) => setRoundRobin(e.target.checked)}
                disabled={saving}
                aria-describedby="native-binding-round-robin-hint native-binding-round-robin-scope"
              />
              <span className="track" aria-hidden="true"><span className="thumb" /></span>
            </label>
            <p id="native-binding-round-robin-hint" className="native-binding-field-hint">
              {t("mapping.native.roundRobinHint")}
            </p>
            <p id="native-binding-round-robin-scope" className="native-binding-field-hint">
              {t("mapping.native.roundRobinScope")}
            </p>
          </div>
          <div className="map-form-row native-binding-limits-row">
            <label>{t("mapping.native.limitsTitle")}</label>
            <div className="native-binding-limits">
              <div>
                <label htmlFor="native-binding-rpm">{t("mapping.native.rpm")}</label>
                <input
                  id="native-binding-rpm"
                  type="number"
                  min={0}
                  step={1}
                  inputMode="numeric"
                  value={rpm}
                  onChange={(e) => setRpm(e.target.value)}
                  disabled={saving}
                  placeholder={t("mapping.native.unlimited")}
                />
              </div>
              <div>
                <label htmlFor="native-binding-daily">{t("mapping.native.dailyUsd")}</label>
                <input
                  id="native-binding-daily"
                  type="number"
                  min={0}
                  step="0.01"
                  inputMode="decimal"
                  value={dailyUsd}
                  onChange={(e) => setDailyUsd(e.target.value)}
                  disabled={saving}
                  placeholder={t("mapping.native.unlimited")}
                />
              </div>
              <div>
                <label htmlFor="native-binding-weekly">{t("mapping.native.weeklyUsd")}</label>
                <input
                  id="native-binding-weekly"
                  type="number"
                  min={0}
                  step="0.01"
                  inputMode="decimal"
                  value={weeklyUsd}
                  onChange={(e) => setWeeklyUsd(e.target.value)}
                  disabled={saving}
                  placeholder={t("mapping.native.unlimited")}
                />
              </div>
            </div>
            {!limitsValid && (
              <p className="native-binding-field-hint error">{t("mapping.native.limitsInvalid")}</p>
            )}
            <p className="native-binding-field-hint">{t("mapping.native.limitsHint")}</p>
          </div>
          <div className="map-form-row native-binding-enable-row">
            <label className="switch native-binding-enable-switch">
              <span className="native-binding-enable-label">{t("mapping.native.enableBinding")}</span>
              <input
                id="native-binding-enabled"
                type="checkbox"
                checked={enabled}
                onChange={(e) => setEnabled(e.target.checked)}
                disabled={saving}
              />
              <span className="track" aria-hidden="true"><span className="thumb" /></span>
            </label>
          </div>
          {error && <div className="error" role="alert">{error}</div>}
          <div className="map-form-foot">
            <button className="btn primary" type="submit" disabled={!canSave || saving}>
              {saving ? t("mapping.native.saving") : t("mapping.save")}
            </button>
            <button className="btn" type="button" disabled={saving} onClick={onCancel}>
              {t("mapping.cancel")}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
