import { useCallback, useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { useT } from "../i18n";
import {
  createNativeKeyBinding,
  deleteNativeKeyBinding,
  fetchClassifyRules,
  fetchNativeKeyBindingCatalog,
  fetchTopLevelAPIKeys,
  updateNativeKeyBinding,
} from "../api/mappings";
import type { ClassifyRule, NativeKeyBinding, NativeKeyBindingCatalog } from "../types";

const BUILTIN_GROUPS = ["free", "team", "plus", "supported"] as const;

/**
 * Return the safe suggestions shown by the group datalist. The input remains
 * free-form so operators can use host/provider groups introduced after this
 * UI was built. Disabled classify rules are omitted because they cannot match
 * a credential until re-enabled.
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
          <p className="native-binding-path-warning">
            <span aria-hidden="true">⚠ </span>{t("mapping.native.pathNotice")}
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
        <button className="btn primary" type="button" onClick={() => setEditor({ mode: "create" })}>
          + {t("mapping.native.newBinding")}
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
                    <dt>{t("mapping.native.group")}</dt>
                    <dd>
                      <span className={`native-binding-group mono${binding ? "" : " unrestricted"}`}>
                        {binding?.group || t("mapping.native.defaultScheduling")}
                      </span>
                    </dd>
                  </div>
                </dl>
                {!row.present && (
                  <p className="native-binding-orphan-note">{t("mapping.native.orphanHint")}</p>
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
  const [group, setGroup] = useState(binding?.group ?? "");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const createKey = selectedKey?.apiKey ?? plainKey;
  const canSave = id.trim() !== "" && group.trim() !== "" && (editing || createKey.trim() !== "");

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
        const input = {
          id: binding.id,
          name: name.trim(),
          enabled,
          group: group.trim(),
          ...(plainKey.trim() ? { key: plainKey.trim() } : {}),
        };
        await updateNativeKeyBinding(input);
      } else {
        await createNativeKeyBinding({
          id: id.trim(),
          name: name.trim() || undefined,
          enabled,
          key: createKey.trim(),
          group: group.trim(),
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
          <div className="map-form-row">
            <label htmlFor="native-binding-group">{t("mapping.native.group")}</label>
            <input
              id="native-binding-group"
              className="mono"
              list="native-binding-group-options"
              value={group}
              onChange={(e) => setGroup(e.target.value)}
              disabled={saving}
              autoComplete="off"
              placeholder="team / classify:vip"
              required
            />
            <datalist id="native-binding-group-options">
              {groupOptions.map((option) => <option key={option} value={option} />)}
            </datalist>
            <p className="native-binding-field-hint">{t("mapping.native.groupHint")}</p>
          </div>
          <div className="map-form-row">
            <label className="switch">
              <input
                type="checkbox"
                checked={enabled}
                onChange={(e) => setEnabled(e.target.checked)}
                disabled={saving}
              />
              <span className="track"><span className="thumb" /></span>
              <span>{t("mapping.native.enableBinding")}</span>
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
