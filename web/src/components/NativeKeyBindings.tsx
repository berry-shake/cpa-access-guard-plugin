import { useCallback, useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { useT } from "../i18n";
import {
  createNativeKeyBinding,
  deleteNativeKeyBinding,
  fetchClassifyRules,
  fetchNativeKeyBindingCatalog,
  fetchNativeCredentialOptions,
  fetchTopLevelAPIKeys,
  resetNativeKeyBindingQuota,
  updateNativeKeyBinding,
} from "../api/mappings";
import type {
  ClassifyRule,
  NativeCredentialOption,
  NativeKeyBinding,
  NativeKeyBindingCatalog,
} from "../types";

const BUILTIN_GROUPS = ["free", "team", "plus", "supported"] as const;
const MANUAL_GROUP_OPTION = "__manual_group__";
type CredentialRestrictionMode = "group" | "auth_ids";

/**
 * Return the safe suggestions shown by the group selector. Operators can
 * switch to a separate manual input for host/provider groups introduced after
 * this UI was built. Disabled classify rules are omitted because they cannot
 * match a credential until re-enabled.
 */
export function buildNativeBindingGroupOptions(rules: ClassifyRule[]): string[] {
  const options = new Set<string>(BUILTIN_GROUPS);
  for (const rule of rules) {
    if (!rule.enabled) continue;
    let group = rule.group.trim().toLowerCase();
    if (!group) continue;
    if (!group.startsWith("classify:")) group = "classify:" + group;
    if (group !== "classify:") options.add(group);
  }
  return Array.from(options);
}

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
  // Never render it or use it in a DOM attribute, URL, log, or storage key.
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
      initialName?: string;
    }
  | { mode: "edit"; binding: NativeKeyBinding };

function suggestedBindingID(index: number, rows: NativeKeyRow[]): string {
  const ids = new Set(rows.flatMap((row) => row.binding ? [row.binding.id.toLowerCase()] : []));
  const base = `native-key-${index + 1}`;
  if (!ids.has(base)) return base;
  let suffix = 2;
  while (ids.has(`${base}-${suffix}`)) suffix++;
  return `${base}-${suffix}`;
}

export default function NativeKeyBindingsTab() {
  const t = useT();
  const [rows, setRows] = useState<NativeKeyRow[]>([]);
  const [rules, setRules] = useState<ClassifyRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [editor, setEditor] = useState<EditorState | null>(null);
  const [pendingID, setPendingID] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [apiKeys, nextRules] = await Promise.all([
        fetchTopLevelAPIKeys(),
        fetchClassifyRules().catch(() => [] as ClassifyRule[]),
      ]);
      const catalog = await fetchNativeKeyBindingCatalog(apiKeys);
      setRows(buildNativeKeyRows(apiKeys, catalog));
      setRules(nextRules);
    } catch (e: unknown) {
      setError(messageFromError(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const groupOptions = useMemo(() => buildNativeBindingGroupOptions(rules), [rules]);
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
    const ordinal = row.topLevelIndex + 1;
    setEditor({
      mode: "create",
      selectedKey: { apiKey: row.apiKey, keyPreview: row.keyPreview },
      initialID: suggestedBindingID(row.topLevelIndex, rows),
      initialName: t("mapping.native.defaultName", { index: ordinal }),
    });
  };

  return (
    <>
      <div className="native-binding-notice" role="note">
        <div className="native-binding-notice-icon" aria-hidden="true">ⓘ</div>
        <div>
          <strong>{t("mapping.native.noticeTitle")}</strong>
          <p>{t("mapping.native.noticeBody")}</p>
          <p>{t("mapping.native.catalogNotice")}</p>
          <p>{t("mapping.native.rotateNotice")}</p>
          <p className="native-binding-unbind-warning">
            <span aria-hidden="true">⚠ </span>{t("mapping.native.unbindNotice")}
          </p>
        </div>
      </div>

      <div className="map-toolbar native-binding-toolbar">
        <span className="muted native-binding-summary">
          {t("mapping.native.summary", { total: topLevelCount, bound: boundCount })}
        </span>
        <button className="btn" type="button" disabled={loading} onClick={() => { void load(); }}>
          {t("mapping.native.refresh")}
        </button>
      </div>

      {error && <div className="error" role="alert">{error}</div>}
      {loading ? (
        <div className="muted native-binding-empty">{t("keys.loading")}</div>
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
            return (
              <article
                className={`native-binding-card ${status}`}
                key={row.rowKey}
                data-testid={`native-key-row-${status}`}
              >
                <div className="native-binding-card-head">
                  <div className="native-binding-identity">
                    <strong>
                      {binding?.name || binding?.id || t("mapping.native.topLevelKeyName", { index: ordinal })}
                    </strong>
                    <span className="mono">
                      {binding?.id || t("mapping.native.topLevelPosition", { index: ordinal })}
                    </span>
                  </div>
                  <span className={`native-binding-status ${status}`}>
                    {t(`mapping.native.${status}`)}
                  </span>
                </div>
                <dl className="native-binding-meta">
                  <div>
                    <dt>{t("mapping.native.keyPreview")}</dt>
                    <dd className="mono">{row.keyPreview}</dd>
                  </div>
                  <div>
                    <dt>{t("mapping.native.restriction")}</dt>
                    <dd>
                      <span className={`native-binding-group mono${binding ? "" : " unrestricted"}`}>
                        {binding?.auth_ids?.length
                          ? t("mapping.native.directSummary", { count: binding.auth_ids.length })
                          : binding?.group || t("mapping.native.defaultScheduling")}
                      </span>
                    </dd>
                  </div>
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
                    {binding.usage.weekly_usd_limit > 0 && (
                      <div>
                        <dt>{t("mapping.native.weeklyUsd")}</dt>
                        <dd className={`mono${binding.usage.weekly_usd_used >= binding.usage.weekly_usd_limit ? " native-quota-full" : ""}`}>
                          ${binding.usage.weekly_usd_used.toFixed(2)}/${binding.usage.weekly_usd_limit.toFixed(2)}
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
                        onClick={() => setEditor({ mode: "edit", binding })}
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

      {editor && (
        <NativeKeyBindingEditor
          key={editor.mode === "edit" ? editor.binding.id : editor.initialID || "__new"}
          binding={editor.mode === "edit" ? editor.binding : undefined}
          selectedKey={editor.mode === "create" ? editor.selectedKey : undefined}
          initialID={editor.mode === "create" ? editor.initialID : undefined}
          initialName={editor.mode === "create" ? editor.initialName : undefined}
          groupOptions={groupOptions}
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
  selectedKey?: SelectedTopLevelKey;
  initialID?: string;
  initialName?: string;
  groupOptions: string[];
  onCancel: () => void;
  onSaved: () => Promise<void>;
}

function NativeKeyBindingEditor({
  binding,
  selectedKey,
  initialID,
  initialName,
  groupOptions,
  onCancel,
  onSaved,
}: EditorProps) {
  const t = useT();
  const editing = !!binding;
  const [id, setID] = useState(binding?.id ?? initialID ?? "");
  const [name, setName] = useState(binding?.name ?? initialName ?? "");
  const [enabled, setEnabled] = useState(binding?.enabled ?? true);
  const [plainKey, setPlainKey] = useState("");
  const initialGroup = binding?.group ?? "";
  const initialAuthIDs = binding?.auth_ids ?? [];
  const [restrictionMode, setRestrictionMode] = useState<CredentialRestrictionMode>(
    initialAuthIDs.length > 0 ? "auth_ids" : "group",
  );
  const initialGroupIsManual = initialGroup !== "" && !groupOptions.includes(initialGroup);
  const [selectedGroup, setSelectedGroup] = useState(
    initialGroupIsManual ? MANUAL_GROUP_OPTION : initialGroup,
  );
  const [manualGroup, setManualGroup] = useState(initialGroupIsManual ? initialGroup : "");
  const [rpm, setRpm] = useState(binding?.rpm ? String(binding.rpm) : "");
  const [dailyUsd, setDailyUsd] = useState(binding?.daily_usd ? String(binding.daily_usd) : "");
  const [weeklyUsd, setWeeklyUsd] = useState(binding?.weekly_usd ? String(binding.weekly_usd) : "");
  const [selectedAuthIDs, setSelectedAuthIDs] = useState<Set<string>>(() => new Set(initialAuthIDs));
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
    } catch (e: unknown) {
      setCredentialError(messageFromError(e));
    } finally {
      setCredentialsLoading(false);
    }
  }, []);

  useEffect(() => {
    if (restrictionMode === "auth_ids") void loadCredentialOptions();
  }, [loadCredentialOptions, restrictionMode]);

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

  const usesManualGroup = selectedGroup === MANUAL_GROUP_OPTION;
  const group = usesManualGroup ? manualGroup : selectedGroup;
  const authIDs = useMemo(() => Array.from(selectedAuthIDs).sort(), [selectedAuthIDs]);
  const credentialOptionsByID = useMemo(
    () => new Map(credentialOptions.map((option) => [option.id, option] as const)),
    [credentialOptions],
  );
  const staleAuthIDs = useMemo(
    () => authIDs.filter((authID) => !credentialOptionsByID.has(authID)),
    [authIDs, credentialOptionsByID],
  );
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
    ].some((value) => value?.toLowerCase().includes(query)));
  }, [credentialOptions, credentialQuery]);
  const toggleAuthID = (authID: string) => {
    setSelectedAuthIDs((previous) => {
      const next = new Set(previous);
      if (next.has(authID)) next.delete(authID);
      else next.add(authID);
      return next;
    });
  };
  const createKey = selectedKey?.apiKey ?? plainKey;
  const restrictionValid = restrictionMode === "group" ? group.trim() !== "" : authIDs.length > 0;
  const canSave = limitsValid && id.trim() !== "" && restrictionValid && (editing || createKey.trim() !== "");

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
      const restriction = restrictionMode === "group"
        ? { group: group.trim() }
        : { auth_ids: authIDs };
      if (binding) {
        const input = {
          id: binding.id,
          name: name.trim(),
          enabled,
          ...restriction,
          rpm: limitField(rpmValue),
          daily_usd: limitField(dailyValue),
          weekly_usd: limitField(weeklyValue),
          ...(plainKey.trim() ? { key: plainKey.trim() } : {}),
        };
        await updateNativeKeyBinding(input);
      } else {
        await createNativeKeyBinding({
          id: id.trim(),
          name: name.trim() || undefined,
          enabled,
          key: createKey.trim(),
          ...restriction,
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
              onChange={(e) => setName(e.target.value)}
              disabled={saving}
              autoComplete="off"
              placeholder={id || "Client A"}
            />
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
          <fieldset className="map-form-row native-restriction-mode">
            <legend>{t("mapping.native.restrictionMode")}</legend>
            <div className="native-restriction-options">
              <label className={restrictionMode === "group" ? "active" : ""}>
                <input
                  type="radio"
                  name="native-restriction-mode"
                  value="group"
                  checked={restrictionMode === "group"}
                  onChange={() => setRestrictionMode("group")}
                  disabled={saving}
                />
                <span>
                  <strong>{t("mapping.native.groupMode")}</strong>
                  <small>{t("mapping.native.groupModeHint")}</small>
                </span>
              </label>
              <label className={restrictionMode === "auth_ids" ? "active" : ""}>
                <input
                  type="radio"
                  name="native-restriction-mode"
                  value="auth_ids"
                  checked={restrictionMode === "auth_ids"}
                  onChange={() => setRestrictionMode("auth_ids")}
                  disabled={saving}
                />
                <span>
                  <strong>{t("mapping.native.directMode")}</strong>
                  <small>{t("mapping.native.directModeHint")}</small>
                </span>
              </label>
            </div>
          </fieldset>
          {restrictionMode === "group" ? (
            <div className="map-form-row native-binding-group-row">
              <label htmlFor="native-binding-group">{t("mapping.native.groupField")}</label>
              <div className="native-binding-select-wrap">
                <select
                  id="native-binding-group"
                  className="native-binding-group-select"
                  value={selectedGroup}
                  onChange={(e) => setSelectedGroup(e.target.value)}
                  disabled={saving}
                  aria-describedby="native-binding-group-hint"
                  required
                >
                  <option value="" disabled>{t("mapping.native.groupPlaceholder")}</option>
                  {groupOptions.map((option) => (
                    <option key={option} value={option}>{option}</option>
                  ))}
                  <option value={MANUAL_GROUP_OPTION}>{t("mapping.native.manualGroupOption")}</option>
                </select>
              </div>
              {usesManualGroup && (
                <div className="native-binding-manual-group">
                  <label htmlFor="native-binding-manual-group">{t("mapping.native.manualGroupLabel")}</label>
                  <input
                    id="native-binding-manual-group"
                    className="mono"
                    value={manualGroup}
                    onChange={(e) => setManualGroup(e.target.value)}
                    disabled={saving}
                    autoComplete="off"
                    spellCheck={false}
                    placeholder="classify:vip"
                    aria-describedby="native-binding-group-hint"
                    required
                  />
                </div>
              )}
              <p id="native-binding-group-hint" className="native-binding-field-hint">
                {t("mapping.native.groupHint")}
              </p>
            </div>
          ) : (
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
                {t("mapping.native.credentialSelected", { count: authIDs.length })}
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
                {visibleCredentialOptions.map((option) => {
                  const displayName = option.label || option.email || option.name || option.id;
                  const normalizedStatus = (option.status ?? "").trim().toLowerCase().replace(/[ -]/g, "_");
                  const unavailable = option.disabled || option.unavailable || [
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
                  const status = option.disabled
                    ? t("mapping.native.credentialDisabled")
                    : option.unavailable
                      ? t("mapping.native.credentialUnavailable")
                      : normalizedStatus === "" || normalizedStatus === "active"
                        ? t("mapping.native.credentialAvailable")
                        : option.status;
                  return (
                    <label className={`native-credential-option${unavailable ? " unavailable" : ""}`} key={option.id}>
                      <input
                        type="checkbox"
                        checked={selectedAuthIDs.has(option.id)}
                        onChange={() => toggleAuthID(option.id)}
                        disabled={saving}
                      />
                      <span className="native-credential-copy">
                        <strong>{displayName}</strong>
                        <span className="mono">{option.id}</span>
                        <small>{[option.provider, option.plan].filter(Boolean).join(" · ")}</small>
                      </span>
                      <span className={`native-credential-status${unavailable ? " danger" : ""}`}>{status}</span>
                    </label>
                  );
                })}
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
          )}
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
