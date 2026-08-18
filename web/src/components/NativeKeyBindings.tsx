import { useCallback, useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { useT } from "../i18n";
import {
  createNativeKeyBinding,
  deleteNativeKeyBinding,
  fetchClassifyRules,
  fetchNativeKeyBindings,
  updateNativeKeyBinding,
} from "../api/mappings";
import type { ClassifyRule, NativeKeyBinding } from "../types";

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

type EditorState = { mode: "create" } | { mode: "edit"; binding: NativeKeyBinding };

export default function NativeKeyBindingsTab() {
  const t = useT();
  const [bindings, setBindings] = useState<NativeKeyBinding[]>([]);
  const [rules, setRules] = useState<ClassifyRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [editor, setEditor] = useState<EditorState | null>(null);
  const [pendingID, setPendingID] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [nextBindings, nextRules] = await Promise.all([
        fetchNativeKeyBindings(),
        fetchClassifyRules().catch(() => [] as ClassifyRule[]),
      ]);
      setBindings(nextBindings);
      setRules(nextRules);
    } catch (e: unknown) {
      setError(messageFromError(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const groupOptions = useMemo(() => buildNativeBindingGroupOptions(rules), [rules]);

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

  return (
    <>
      <div className="native-binding-notice" role="note">
        <div className="native-binding-notice-icon" aria-hidden="true">ⓘ</div>
        <div>
          <strong>{t("mapping.native.noticeTitle")}</strong>
          <p>{t("mapping.native.noticeBody")}</p>
          <p>{t("mapping.native.rotateNotice")}</p>
          <p className="native-binding-unbind-warning">
            <span aria-hidden="true">⚠ </span>{t("mapping.native.unbindNotice")}
          </p>
          <p className="native-binding-path-warning">
            <span aria-hidden="true">⚠ </span>{t("mapping.native.pathNotice")}
          </p>
        </div>
      </div>

      <div className="map-toolbar">
        <button className="btn primary" type="button" onClick={() => setEditor({ mode: "create" })}>
          + {t("mapping.native.newBinding")}
        </button>
      </div>

      {error && <div className="error" role="alert">{error}</div>}
      {loading ? (
        <div className="muted native-binding-empty">{t("keys.loading")}</div>
      ) : bindings.length === 0 ? (
        <div className="native-binding-empty">
          <strong>{t("mapping.native.emptyTitle")}</strong>
          <span className="muted">{t("mapping.native.emptyBody")}</span>
        </div>
      ) : (
        <div className="native-binding-grid">
          {bindings.map((binding) => {
            const pending = pendingID === binding.id;
            return (
              <article className={"native-binding-card" + (binding.enabled ? "" : " disabled")} key={binding.id}>
                <div className="native-binding-card-head">
                  <div className="native-binding-identity">
                    <strong>{binding.name || binding.id}</strong>
                    <span className="mono">{binding.id}</span>
                  </div>
                  <span className={"native-binding-status " + (binding.enabled ? "enabled" : "disabled")}>
                    {t(binding.enabled ? "mapping.native.enabled" : "mapping.native.disabled")}
                  </span>
                </div>
                <dl className="native-binding-meta">
                  <div>
                    <dt>{t("mapping.native.keyPreview")}</dt>
                    <dd className="mono">{binding.key_preview || "<redacted>"}</dd>
                  </div>
                  <div>
                    <dt>{t("mapping.native.group")}</dt>
                    <dd><span className="native-binding-group mono">{binding.group}</span></dd>
                  </div>
                </dl>
                <div className="native-binding-actions">
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
                </div>
              </article>
            );
          })}
        </div>
      )}

      {editor && (
        <NativeKeyBindingEditor
          key={editor.mode === "create" ? "__new" : editor.binding.id}
          binding={editor.mode === "edit" ? editor.binding : undefined}
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
  groupOptions: string[];
  onCancel: () => void;
  onSaved: () => Promise<void>;
}

function NativeKeyBindingEditor({ binding, groupOptions, onCancel, onSaved }: EditorProps) {
  const t = useT();
  const editing = !!binding;
  const [id, setID] = useState(binding?.id ?? "");
  const [name, setName] = useState(binding?.name ?? "");
  const [enabled, setEnabled] = useState(binding?.enabled ?? true);
  const [plainKey, setPlainKey] = useState("");
  const [group, setGroup] = useState(binding?.group ?? "");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const canSave = id.trim() !== "" && group.trim() !== "" && (editing || plainKey.trim() !== "");

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
          key: plainKey.trim(),
          group: group.trim(),
        });
      }
      // Drop the only UI-held plaintext copy before closing the editor.
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
              autoFocus={!editing}
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
