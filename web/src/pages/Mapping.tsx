import { useEffect, useState, useCallback } from "react";
import { useNavigate, useParams, useLocation } from "react-router-dom";
import { useT } from "../i18n";
import type { AliasMapping, AliasTarget, ClassifyRule, ClassifyPreviewResponse, CredentialDescriptor } from "../types";
import { fetchAliases, upsertAlias, deleteAlias, fetchClassifyRules, upsertClassifyRule, deleteClassifyRule, reorderClassifyRules, classifyPreview, fetchCredentialDescriptors, canPreviewClassifyField } from "../api/mappings";
import NativeKeyBindingsTab from "../components/NativeKeyBindings";
import PricingTable from "../components/PricingTable";

type MappingTab = "alias" | "classify" | "native" | "pricing";
type MappingSection = "native" | "pricing" | "classify" | "alias";

export default function Mapping({ section = "alias" }: { section?: MappingSection }) {
  const loc = useLocation();
  const defaultTab: MappingTab = section;
  const [tab, setTab] = useState<MappingTab>(defaultTab);

  useEffect(() => { setTab(defaultTab); }, [defaultTab]);

  // Pick up returned state (new targets from ModelPick, etc.)
  useEffect(() => {
    const requested = loc.state?.mappingTab;
    const allowed = requested === section;
    if (allowed) setTab(requested as MappingTab);
  }, [loc.state, section]);

  return (
    <div className="map-page">
      {tab === "alias" && <AliasListTab />}
      {tab === "classify" && <ClassifyTab />}
      {tab === "native" && <NativeKeyBindingsTab />}
      {tab === "pricing" && <PricingTable />}
    </div>
  );
}

// --- Alias List Tab ---

function AliasListTab() {
  const t = useT();
  const nav = useNavigate();
  const [aliases, setAliases] = useState<AliasMapping[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setAliases(await fetchAliases());
    } catch (e: unknown) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const handleDelete = async (aliasName: string) => {
    try {
      await deleteAlias(aliasName);
      await load();
    } catch (e: unknown) {
      setError(String(e));
    }
  };

  return (
    <>
      <div className="map-toolbar">
        <button className="btn primary" onClick={() => nav("/mapping/alias/new")}>
          + {t("mapping.newAlias")}
        </button>
      </div>
      {error && <div className="error">{error}</div>}
      {loading ? (
        <div className="muted" style={{ padding: 20 }}>{t("keys.loading") || "Loading..."}</div>
      ) : aliases.length === 0 ? (
        <div className="muted" style={{ padding: 20 }}>No aliases</div>
      ) : (
        <div className="alias-grid">
          {aliases.map((a) => (
            <AliasCard key={a.alias} alias={a} onDelete={handleDelete} onEdit={(name) => nav(`/mapping/alias/${encodeURIComponent(name)}`)} />
          ))}
        </div>
      )}
    </>
  );
}

function AliasCard({ alias, onDelete, onEdit }: { alias: AliasMapping; onDelete: (n: string) => void; onEdit: (n: string) => void }) {
  const t = useT();
  const [refCount] = useState<number | null>(null);
  // refCount would come from a separate API call or be included in the list response.
  // For now, show "unreferenced" as placeholder.
  return (
    <div className="alias-card">
      <div className="alias-card-head">
        <span className="alias-card-name">{alias.alias}</span>
        <span className="alias-dispatch-badge">
          {alias.dispatch === "priority" ? t("mapping.alias.priority") : t("mapping.alias.roundRobin")}
        </span>
      </div>
      <div className="alias-targets">
        {alias.targets.slice(0, 3).map((tgt, i) => (
          <div key={i} className="alias-target-row">
            <span>{tgt.provider} · {tgt.target_model}</span>
            {tgt.group && <span className="alias-target-group">{tgt.group}</span>}
          </div>
        ))}
        {alias.targets.length > 3 && (
          <div className="alias-target-row" style={{ opacity: 0.6 }}>
            {t("mapping.moreTargets", { n: alias.targets.length - 3 })}
          </div>
        )}
      </div>
      <div className="alias-pricing">
        {alias.billing_mode === "per_call" ? (
          <>{t("mapping.alias.perCallUnit")} ${alias.per_call_usd ?? 0}/{t("mapping.alias.perCallUnit")}</>
        ) : (
          <>{t("mapping.alias.input")} ${alias.input_price_per_million ?? 0} / {t("mapping.alias.output")} ${alias.output_price_per_million ?? 0} / {t("mapping.alias.cache")} ${alias.cache_read_price_per_million ?? 0} {t("mapping.alias.perMillion")}</>
        )}
      </div>
      <div className={"alias-refs" + (refCount === 0 ? " zero" : "")}>
        {refCount && refCount > 0 ? t("mapping.refs", { n: refCount }) : t("mapping.unreferenced")}
      </div>
      <div className="alias-actions">
        <button className="btn sm" onClick={() => onEdit(alias.alias)}>{t("mapping.edit")}</button>
        <button
          className="btn sm danger-outline"
          disabled={refCount !== null && refCount > 0}
          title={refCount && refCount > 0 ? t("mapping.deleteBlocked", { n: refCount }) : ""}
          onClick={() => onDelete(alias.alias)}
        >
          {t("mapping.delete")}
        </button>
      </div>
    </div>
  );
}

// --- Classify Tab ---

function ClassifyTab() {
  const t = useT();
  const nav = useNavigate();
  const [rules, setRules] = useState<ClassifyRule[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [previewData, setPreviewData] = useState<ClassifyPreviewResponse | null>(null);
  const [previewError, setPreviewError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    setPreviewError("");
    setPreviewData(null);
    try {
      const list = await fetchClassifyRules();
      setRules(list);

      let descriptors: CredentialDescriptor[];
      try {
        descriptors = await fetchCredentialDescriptors();
      } catch (e: unknown) {
        // A failed auth-files request is not the same as a successful empty
        // list. Keep previewData null so the cards say "unavailable" instead
        // of presenting a dangerous false zero-match result.
        setPreviewError(String(e));
        return;
      }

      // Evaluate the current rules against the safe subset reconstructed from
      // CPA's management descriptors. This is a regex preview, not proof that
      // an auth is currently schedulable.
      try {
        setPreviewData(await classifyPreview(descriptors));
      } catch (e: unknown) {
        setPreviewError(String(e));
      }
    } catch (e: unknown) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const handleReorder = async (names: string[]) => {
    try {
      await reorderClassifyRules(names);
      await load();
    } catch (e: unknown) {
      setError(String(e));
    }
  };

  const handleDelete = async (name: string) => {
    try {
      await deleteClassifyRule(name);
      await load();
    } catch (e: unknown) {
      setError(String(e));
    }
  };

  const moveRule = (idx: number, dir: -1 | 1) => {
    const newOrder = [...rules];
    const target = idx + dir;
    if (target < 0 || target >= newOrder.length) return;
    [newOrder[idx], newOrder[target]] = [newOrder[target], newOrder[idx]];
    void handleReorder(newOrder.map((r) => r.name));
  };

  return (
    <>
      <div className="native-binding-notice" role="note">
        <div className="native-binding-notice-icon" aria-hidden="true">ⓘ</div>
        <div>
          <strong>{t("mapping.rule.previewNoticeTitle")}</strong>
          <p>{t("mapping.rule.previewNotice")}</p>
        </div>
      </div>
      <div className="map-toolbar">
        <button className="btn primary" onClick={() => nav("/classify/rule/new")}>
          + {t("mapping.newRule")}
        </button>
      </div>
      {error && <div className="error">{error}</div>}
      {previewError && (
        <div className="error">
          {t("mapping.rule.previewLoadFailed", { error: previewError })}
        </div>
      )}
      {loading ? (
        <div className="muted" style={{ padding: 20 }}>Loading...</div>
      ) : (
        <div className="rule-list">
          {/* Built-in rules (read-only) */}
          <div className="rule-builtin-card">
            <h3>{t("mapping.rule.builtin")} ({t("mapping.rule.builtinReadOnly")})</h3>
            <div className="rule-builtin-row">
              <span className="info-icon">ⓘ</span>
              plan_type → {`<detected>`}
            </div>
            <div className="rule-builtin-row">
              <span className="info-icon">ⓘ</span>
              tier → {`<detected>`}
            </div>
            <div className="rule-builtin-desc">{t("mapping.rule.builtinDesc")}</div>
          </div>

          {/* Custom rules */}
          <div className="section-label" style={{ marginTop: 16 }}>{t("mapping.rule.custom")}</div>
          {rules.length === 0 ? (
            <div className="muted" style={{ padding: 20 }}>No custom rules</div>
          ) : (
            rules.map((rule, idx) => (
              <RuleCard
                key={rule.name}
                rule={rule}
                idx={idx}
                total={rules.length}
                onMoveUp={() => moveRule(idx, -1)}
                onMoveDown={() => moveRule(idx, 1)}
                onEdit={() => nav(`/classify/rule/${encodeURIComponent(rule.name)}`)}
                onDelete={() => handleDelete(rule.name)}
                previewData={previewData}
              />
            ))
          )}
        </div>
      )}
    </>
  );
}

function RuleCard({
  rule, idx, total, onMoveUp, onMoveDown, onEdit, onDelete, previewData,
}: {
  rule: ClassifyRule;
  idx: number;
  total: number;
  onMoveUp: () => void;
  onMoveDown: () => void;
  onEdit: () => void;
  onDelete: () => void;
  previewData: ClassifyPreviewResponse | null;
}) {
  const t = useT();
  const [expanded, setExpanded] = useState(false);
  const [matchCount, setMatchCount] = useState<number | null>(null);
  const [matchedFiles, setMatchedFiles] = useState<string[]>([]);
  const [page, setPage] = useState(0);
  const pageSize = 50;
  const previewable = canPreviewClassifyField(rule.field);
  const rulePreviewAvailable = previewable
    && previewData !== null
    && Array.isArray(previewData.rule_matches?.[rule.name]);

  // Use the rule-specific result. A group is a union, so reading the group
  // aggregate would give every same-group rule the same misleading count.
  useEffect(() => {
    const files = previewData?.rule_matches?.[rule.name];
    if (!rulePreviewAvailable || !Array.isArray(files)) {
      setMatchCount(null);
      setMatchedFiles([]);
      return;
    }
    setMatchCount(files.length);
    setMatchedFiles(files);
  }, [previewData, rule.name, rulePreviewAvailable]);

  const pageCount = Math.ceil(matchedFiles.length / pageSize);
  const pageFiles = matchedFiles.slice(page * pageSize, (page + 1) * pageSize);

  return (
    <div className="rule-card">
      <div className="rule-card-head">
        <div className="rule-card-order">
          <button onClick={onMoveUp} disabled={idx === 0} title={t("mapping.rule.moveUp")}>↑</button>
          <button onClick={onMoveDown} disabled={idx === total - 1} title={t("mapping.rule.moveDown")}>↓</button>
        </div>
        <div className="rule-card-main" onClick={() => setExpanded(!expanded)} style={{ cursor: "pointer" }}>
          <div className="rule-card-name">{rule.name}</div>
          <div className="rule-card-sub">{rule.field}: {rule.pattern} → {rule.group}</div>
        </div>
        <div className="rule-card-right">
          <span className={"rule-match-badge" + (matchCount === 0 ? " zero" : "")}>
            {!rulePreviewAvailable
              ? t("mapping.rule.matchUnavailable")
              : matchCount !== null
              ? (matchCount > 0 ? t("mapping.rule.matchCount", { n: matchCount }) : t("mapping.rule.matchCountZero"))
              : "..."}
          </span>
          <label className="switch" title={t("mapping.rule.enabled")}>
            <input type="checkbox" checked={rule.enabled} readOnly />
            <span className="track"><span className="thumb" /></span>
          </label>
          <button className="btn sm" onClick={onEdit}>{t("mapping.edit")}</button>
          <button className="btn sm danger-outline" onClick={onDelete}>{t("mapping.delete")}</button>
        </div>
      </div>
      {expanded && (
        <div className="rule-detail">
          <div className="rule-detail-files">
            {pageFiles.length === 0 ? (
              <div className="muted" style={{ padding: 8 }}>
                {!previewable
                  ? t("mapping.rule.previewUnavailableDetail")
                  : !rulePreviewAvailable
                  ? t("mapping.rule.previewLoadFailedDetail")
                  : t("mapping.rule.noFiles")}
              </div>
            ) : (
              pageFiles.map((f, i) => (
                <div key={i} className="rule-detail-file">
                  <span>{f}</span>
                </div>
              ))
            )}
          </div>
          {pageCount > 1 && (
            <div className="rule-pager">
              <button onClick={() => setPage(Math.max(0, page - 1))} disabled={page === 0}>
                {t("mapping.rule.prevPage")}
              </button>
              <span className="page-info">{t("mapping.rule.pageInfo", { cur: page + 1, total: pageCount })}</span>
              <button onClick={() => setPage(Math.min(pageCount - 1, page + 1))} disabled={page >= pageCount - 1}>
                {t("mapping.rule.nextPage")}
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// --- Alias Edit Form ---

// Survives ModelPick remounts (and React Strict Mode) better than router
// state alone — without this, filling the alias name then picking targets
// wipes the name when AliasEditForm remounts empty.
const ALIAS_FORM_DRAFT_KEY = "access-guard:alias-form-draft";

function readAliasFormDraft(): AliasMapping | null {
  try {
    const raw = sessionStorage.getItem(ALIAS_FORM_DRAFT_KEY);
    if (!raw) return null;
    return JSON.parse(raw) as AliasMapping;
  } catch {
    return null;
  }
}

function writeAliasFormDraft(draft: AliasMapping) {
  try {
    sessionStorage.setItem(ALIAS_FORM_DRAFT_KEY, JSON.stringify(draft));
  } catch {
    /* private mode / quota — router state is the fallback */
  }
}

function clearAliasFormDraft() {
  try {
    sessionStorage.removeItem(ALIAS_FORM_DRAFT_KEY);
  } catch {
    /* ignore */
  }
}

export function AliasEditForm() {
  const t = useT();
  const nav = useNavigate();
  const { aliasName } = useParams();
  const loc = useLocation();
  const isNew = aliasName === "new" || !aliasName;

  const locState = loc.state as { draftAlias?: AliasMapping; pickedTargets?: AliasTarget[] } | null;
  const returnDraft = locState?.draftAlias;
  const returnTargets = locState?.pickedTargets;

  const [alias, setAlias] = useState<AliasMapping>(() => {
    const draft = returnDraft ?? readAliasFormDraft();
    if (draft) {
      return {
        ...draft,
        targets: returnTargets ?? draft.targets ?? [],
      };
    }
    return {
      alias: isNew ? "" : decodeURIComponent(aliasName ?? ""),
      targets: [],
      dispatch: "round-robin",
      billing_mode: "tokens",
      pricing_mode: "auto",
      input_price_per_million: 0,
      output_price_per_million: 0,
      cache_read_price_per_million: 0,
      per_call_usd: 0,
    };
  });
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  // Keep session draft in sync while editing so a picker trip never loses fields.
  useEffect(() => {
    writeAliasFormDraft(alias);
  }, [alias]);

  // Load existing alias if editing — skip when this form already has a draft
  // for the same alias (picker return / in-progress edit).
  useEffect(() => {
    if (isNew) return;
    if (returnDraft) return;
    const name = decodeURIComponent(aliasName ?? "");
    const session = readAliasFormDraft();
    if (session && session.alias === name) return;
    void fetchAliases().then((list) => {
      const found = list.find((a) => a.alias === name);
      if (found) setAlias(found);
    }).catch((e: unknown) => setError(String(e)));
  }, [aliasName, isNew, returnDraft]);

  // Apply targets returned from ModelPick (draft fields come from session/router).
  useEffect(() => {
    if (!returnTargets) return;
    setAlias((prev) => {
      const base = returnDraft ?? prev;
      return { ...base, targets: returnTargets };
    });
  }, [returnTargets, returnDraft]);

  const leaveForm = (toMapping = true) => {
    clearAliasFormDraft();
    if (toMapping) nav("/mapping", { state: { mappingTab: "alias" } });
  };

  const handleSave = async () => {
    setSaving(true);
    setError("");
    try {
      await upsertAlias(alias);
      leaveForm(true);
    } catch (e: unknown) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  const addTarget = () => {
    // Persist draft before leaving so name/dispatch/pricing survive the picker.
    writeAliasFormDraft(alias);
    const here = `/mapping/alias/${isNew ? "new" : encodeURIComponent(alias.alias || aliasName || "new")}`;
    nav("/mapping/pick-target", {
      state: {
        returnTo: here,
        currentTargets: alias.targets,
        draftAlias: alias,
      },
    });
  };

  const removeTarget = (idx: number) => {
    setAlias((prev) => ({ ...prev, targets: prev.targets.filter((_, i) => i !== idx) }));
  };

  return (
    <div className="map-form-page">
      <div className="map-form-card">
        <div className="map-form-head">
          <a className="back-link" onClick={() => leaveForm(true)}>
            ← {t("mapping.back")}
          </a>
          <h1>{isNew ? t("mapping.alias.newTitle") : t("mapping.alias.editTitle")}</h1>
        </div>
        <div className="map-form-row">
          <label>{t("mapping.alias.name")}</label>
          <input
            className="mono"
            value={alias.alias}
            onChange={(e) => setAlias({ ...alias, alias: e.target.value })}
            disabled={!isNew}
            placeholder="my-alias"
          />
        </div>
        <div className="map-form-row">
          <label>{t("mapping.alias.dispatch")}</label>
          <div className="map-dispatch-seg" role="group" aria-label={t("mapping.alias.dispatch")}>
            <button
              type="button"
              className={"map-dispatch-btn" + (alias.dispatch === "round-robin" ? " active" : "")}
              onClick={() => setAlias({ ...alias, dispatch: "round-robin" })}
            >
              <span className="map-dispatch-title">{t("mapping.alias.roundRobin")}</span>
              <span className="map-dispatch-desc">{t("mapping.alias.roundRobinDesc")}</span>
            </button>
            <button
              type="button"
              className={"map-dispatch-btn" + (alias.dispatch === "priority" ? " active" : "")}
              onClick={() => setAlias({ ...alias, dispatch: "priority" })}
            >
              <span className="map-dispatch-title">{t("mapping.alias.priority")}</span>
              <span className="map-dispatch-desc">{t("mapping.alias.priorityDesc")}</span>
            </button>
          </div>
        </div>
        <div className="map-form-row">
          <label>{t("mapping.alias.targets")}</label>
          <div className="map-form-targets">
            {alias.targets.map((tgt, i) => (
              <div key={i} className="map-form-target-row">
                <span className="mono">{tgt.provider} · {tgt.target_model} {tgt.group ? `· ${tgt.group}` : ""}</span>
                <button className="remove-btn" onClick={() => removeTarget(i)}>×</button>
              </div>
            ))}
          </div>
          <button className="btn" onClick={addTarget}>+ {t("mapping.alias.addTarget")}</button>
        </div>
        <div className="map-form-row">
          <label>{t("mapping.alias.billing")}</label>
          <label className="switch" style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
            <input
              type="checkbox"
              checked={alias.billing_mode === "per_call"}
              onChange={(e) => setAlias({ ...alias, billing_mode: e.target.checked ? "per_call" : "tokens" })}
            />
            <span className="track"><span className="thumb" /></span>
            <span>{alias.billing_mode === "per_call" ? t("mapping.alias.perCall") : t("mapping.alias.tokens")}</span>
          </label>
        </div>
        {alias.billing_mode === "tokens" ? (
          <>
            <div className="map-form-row">
              <label>{t("mapping.alias.pricingMode")}</label>
              <select
                value={alias.pricing_mode ?? "auto"}
                onChange={(e) => setAlias({ ...alias, pricing_mode: e.target.value === "manual" ? "manual" : "auto" })}
              >
                <option value="auto">{t("mapping.alias.pricingAuto")}</option>
                <option value="manual">{t("mapping.alias.pricingManual")}</option>
              </select>
              <small>{t("mapping.alias.pricingModeHint")}</small>
            </div>
            <div className="map-form-row">
              <label>{t("mapping.alias.input")} ($/1M)</label>
              <input
                className="mono"
                type="number"
                step="0.01"
                value={alias.input_price_per_million ?? 0}
                onChange={(e) => setAlias({ ...alias, input_price_per_million: parseFloat(e.target.value) || 0 })}
              />
            </div>
            <div className="map-form-row">
              <label>{t("mapping.alias.output")} ($/1M)</label>
              <input
                className="mono"
                type="number"
                step="0.01"
                value={alias.output_price_per_million ?? 0}
                onChange={(e) => setAlias({ ...alias, output_price_per_million: parseFloat(e.target.value) || 0 })}
              />
            </div>
            <div className="map-form-row">
              <label>{t("mapping.alias.cache")} ($/1M)</label>
              <input
                className="mono"
                type="number"
                step="0.01"
                value={alias.cache_read_price_per_million ?? 0}
                onChange={(e) => setAlias({ ...alias, cache_read_price_per_million: parseFloat(e.target.value) || 0 })}
              />
            </div>
          </>
        ) : (
          <div className="map-form-row">
            <label>{t("mapping.alias.perCallUnit")} ($/{t("mapping.alias.perCallUnit")})</label>
            <input
              className="mono"
              type="number"
              step="0.01"
              value={alias.per_call_usd ?? 0}
              onChange={(e) => setAlias({ ...alias, per_call_usd: parseFloat(e.target.value) || 0 })}
            />
          </div>
        )}
        {error && <div className="error">{error}</div>}
        <div className="map-form-foot">
          <button className="btn primary" onClick={handleSave} disabled={saving}>
            {saving ? "..." : t("mapping.save")}
          </button>
          <button className="btn" onClick={() => leaveForm(true)}>
            {t("mapping.cancel")}
          </button>
        </div>
      </div>
    </div>
  );
}

// --- Rule Edit Form ---

export function RuleEditForm() {
  const t = useT();
  const nav = useNavigate();
  const { ruleName } = useParams();
  const isNew = ruleName === "new" || !ruleName;

  const [rule, setRule] = useState<ClassifyRule>({
    name: isNew ? "" : decodeURIComponent(ruleName),
    field: "plan_type",
    pattern: "",
    group: "",
    enabled: true,
  });
  const [customField, setCustomField] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  // Load existing rule if editing.
  useEffect(() => {
    if (isNew) return;
    void fetchClassifyRules().then((list) => {
      const found = list.find((r) => r.name === decodeURIComponent(ruleName));
      if (found) {
        setRule(found);
        if (["filename", "provider", "plan_type", "tier"].includes(found.field)) {
          setCustomField("");
        } else {
          setCustomField(found.field);
          setRule((prev) => ({ ...prev, field: "custom" }));
        }
      }
    }).catch((e: unknown) => setError(String(e)));
  }, [ruleName, isNew]);

  const effectiveField = (rule.field === "custom" ? customField : rule.field).trim().toLowerCase();

  const handleSave = async () => {
    setSaving(true);
    setError("");
    try {
      const draft = { ...rule, field: effectiveField };
      // Validate with the same Go/RE2 engine used at runtime. JavaScript's
      // RegExp grammar differs (for example (?i) and lookarounds), so browser-
      // only validation would reject valid rules and approve invalid ones.
      await classifyPreview([], [draft]);
      await upsertClassifyRule(draft);
      nav("/classify", { state: { mappingTab: "classify" } });
    } catch (e: unknown) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="map-form-page">
      <div className="map-form-card">
        <div className="map-form-head">
          <a className="back-link" onClick={() => nav("/classify", { state: { mappingTab: "classify" } })}>
            ← {t("mapping.back")}
          </a>
          <h1>{isNew ? t("mapping.rule.newTitle") : t("mapping.rule.editTitle")}</h1>
        </div>
        <div className="map-form-row">
          <label>{t("mapping.rule.name")}</label>
          <input
            value={rule.name}
            onChange={(e) => setRule({ ...rule, name: e.target.value })}
            disabled={!isNew}
            placeholder="my-rule"
          />
        </div>
        <div className="map-form-row">
          <label>{t("mapping.rule.field")}</label>
          <select
            value={rule.field}
            onChange={(e) => setRule({ ...rule, field: e.target.value })}
          >
            <option value="filename">{t("mapping.rule.fieldFilename")}</option>
            <option value="provider">{t("mapping.rule.fieldProvider")}</option>
            <option value="plan_type">{t("mapping.rule.fieldPlanType")}</option>
            <option value="tier">{t("mapping.rule.fieldTier")}</option>
            <option value="custom">{t("mapping.rule.fieldCustom")}</option>
          </select>
        </div>
        {rule.field === "custom" && (
          <div className="map-form-row">
            <label>{t("mapping.rule.customField")}</label>
            <input
              className="mono"
              value={customField}
              onChange={(e) => setCustomField(e.target.value)}
              placeholder="auth_kind"
            />
            <p className="native-binding-field-hint">{t("mapping.rule.customFieldHint")}</p>
          </div>
        )}
        <div className="map-form-row">
          <label>{t("mapping.rule.regex")}</label>
          <input
            className="mono"
            value={rule.pattern}
            onChange={(e) => setRule({ ...rule, pattern: e.target.value })}
            placeholder="^team$"
          />
          <p className="native-binding-field-hint">{t("mapping.rule.regexHint")}</p>
        </div>
        <div className="map-form-row">
          <label>{t("mapping.rule.group")}</label>
          <input
            className="mono"
            value={rule.group}
            onChange={(e) => setRule({ ...rule, group: e.target.value })}
            placeholder="team"
          />
        </div>
        <div className="map-form-row">
          <label className="switch" style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
            <input
              type="checkbox"
              checked={rule.enabled}
              onChange={(e) => setRule({ ...rule, enabled: e.target.checked })}
            />
            <span className="track"><span className="thumb" /></span>
            <span>{t("mapping.rule.enabled")}</span>
          </label>
        </div>
        {error && <div className="error">{error}</div>}
        <div className="map-form-foot">
          <button className="btn primary" onClick={handleSave} disabled={saving}>
            {saving ? "..." : t("mapping.save")}
          </button>
          <button className="btn" onClick={() => nav("/classify", { state: { mappingTab: "classify" } })}>
            {t("mapping.cancel")}
          </button>
        </div>
      </div>
    </div>
  );
}
