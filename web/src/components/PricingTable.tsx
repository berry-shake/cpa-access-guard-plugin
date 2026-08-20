import { useCallback, useEffect, useMemo, useState } from "react";
import { useT } from "../i18n";
import {
  deleteModelPricing,
  fetchModelPricing,
  fetchPricingSyncStatus,
  runPricingSync,
  upsertModelPricing,
  type ModelPricing,
  type PricingSyncStatus,
} from "../api/mappings";

const VISIBLE_CAP = 200;

const emptyRow = (): ModelPricing => ({
  modelId: "",
  displayName: "",
  inputCostPerMillion: "0",
  outputCostPerMillion: "0",
  cacheReadCostPerMillion: "0",
  cacheCreationCostPerMillion: "0",
});

export default function PricingTable() {
  const t = useT();
  const [rows, setRows] = useState<ModelPricing[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [syncing, setSyncing] = useState(false);
  const [syncStatus, setSyncStatus] = useState<PricingSyncStatus | null>(null);
  const [editing, setEditing] = useState<ModelPricing | null>(null);
  const [isNew, setIsNew] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [list, status] = await Promise.all([
        fetchModelPricing(),
        fetchPricingSyncStatus().catch(() => null),
      ]);
      setRows(list);
      setSyncStatus(status);
    } catch (e: unknown) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return rows;
    return rows.filter((row) =>
      row.modelId.toLowerCase().includes(q) ||
      (row.displayName || "").toLowerCase().includes(q),
    );
  }, [rows, query]);

  const visible = filtered.slice(0, VISIBLE_CAP);

  const handleSyncNow = async () => {
    setSyncing(true);
    setError("");
    try {
      const result = await runPricingSync();
      if (result.error) setError(result.error);
      await load();
    } catch (e: unknown) {
      setError(String(e));
    } finally {
      setSyncing(false);
    }
  };

  const handleDelete = async (modelId: string) => {
    if (!confirm(t("mapping.pricing.deleteConfirm", { id: modelId }))) return;
    try {
      await deleteModelPricing(modelId);
      await load();
    } catch (e: unknown) {
      setError(String(e));
    }
  };

  return (
    <>
      <div className="map-toolbar pricing-toolbar">
        <input
          className="input"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("mapping.pricing.search")}
          style={{ maxWidth: 280, marginRight: "auto" }}
        />
        {syncStatus?.last_result && !syncStatus.last_result.error && (
          <span className="muted" style={{ alignSelf: "center" }}>
            {t("mapping.pricingSync.last", {
              catalog: syncStatus.catalog_size ?? syncStatus.last_result.catalog ?? rows.length,
            })}
          </span>
        )}
        <button className="btn" disabled={syncing} onClick={() => { void handleSyncNow(); }}>
          {syncing ? t("mapping.pricingSync.running") : t("mapping.pricingSync.runNow")}
        </button>
        <button className="btn primary" onClick={() => { setIsNew(true); setEditing(emptyRow()); }}>
          + {t("mapping.pricing.add")}
        </button>
      </div>
      {error && <div className="error">{error}</div>}
      {loading ? (
        <div className="muted" style={{ padding: 20 }}>{t("keys.loading")}</div>
      ) : rows.length === 0 ? (
        <div className="muted" style={{ padding: 20 }}>{t("mapping.pricing.empty")}</div>
      ) : (
        <>
          <div className="muted" style={{ marginBottom: 8, fontSize: 13 }}>
            {t("mapping.pricing.count", { shown: visible.length, total: filtered.length })}
            {t("mapping.pricing.perMillion")}
          </div>
          <div className="card table-wrap">
            <table className="pricing-table">
              <thead>
                <tr>
                  <th>{t("mapping.pricing.modelId")}</th>
                  <th>{t("mapping.pricing.displayName")}</th>
                  <th className="num">{t("mapping.pricing.input")}</th>
                  <th className="num">{t("mapping.pricing.output")}</th>
                  <th className="num">{t("mapping.pricing.cacheRead")}</th>
                  <th className="num">{t("mapping.pricing.cacheWrite")}</th>
                  <th className="num">{t("mapping.edit")}</th>
                </tr>
              </thead>
              <tbody>
                {visible.map((row) => (
                  <tr key={row.modelId}>
                    <td className="mono">{row.modelId}</td>
                    <td>{row.displayName}</td>
                    <td className="num mono">${row.inputCostPerMillion}</td>
                    <td className="num mono">${row.outputCostPerMillion}</td>
                    <td className="num mono">${row.cacheReadCostPerMillion}</td>
                    <td className="num mono">${row.cacheCreationCostPerMillion}</td>
                    <td className="num">
                      <div className="actions" style={{ justifyContent: "flex-end" }}>
                        <button className="btn sm" onClick={() => { setIsNew(false); setEditing(row); }}>
                          {t("mapping.edit")}
                        </button>
                        <button className="btn sm danger-outline" onClick={() => { void handleDelete(row.modelId); }}>
                          {t("mapping.delete")}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
      {editing && (
        <PricingEditModal
          row={editing}
          isNew={isNew}
          onClose={() => { setEditing(null); setIsNew(false); }}
          onSaved={async () => { setEditing(null); setIsNew(false); await load(); }}
        />
      )}
    </>
  );
}

function PricingEditModal({
  row,
  isNew,
  onClose,
  onSaved,
}: {
  row: ModelPricing;
  isNew: boolean;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const t = useT();
  const [form, setForm] = useState<ModelPricing>(row);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const set = (key: keyof ModelPricing, value: string) => {
    setForm((prev) => ({ ...prev, [key]: value }));
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (isNew && !form.modelId.trim()) {
      setError(t("mapping.pricing.modelIdRequired"));
      return;
    }
    setSaving(true);
    setError("");
    try {
      await upsertModelPricing({
        ...form,
        displayName: form.displayName.trim() || form.modelId.trim(),
      });
      await onSaved();
    } catch (err: unknown) {
      setError(String(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="modal-overlay" onClick={onClose}>
      <form className="modal" onClick={(e) => e.stopPropagation()} onSubmit={(e) => { void submit(e); }}>
        <h3>{isNew ? t("mapping.pricing.add") : t("mapping.pricing.editTitle", { id: row.modelId })}</h3>
        {error && <div className="error">{error}</div>}
        <div className="form-row">
          <label>{t("mapping.pricing.modelId")}</label>
          <input className="input" value={form.modelId} disabled={!isNew} onChange={(e) => set("modelId", e.target.value)} />
        </div>
        <div className="form-row">
          <label>{t("mapping.pricing.displayName")}</label>
          <input className="input" value={form.displayName} onChange={(e) => set("displayName", e.target.value)} />
        </div>
        <div className="row2">
          <div className="form-row">
            <label>{t("mapping.pricing.input")}</label>
            <input className="input" value={form.inputCostPerMillion} onChange={(e) => set("inputCostPerMillion", e.target.value)} />
          </div>
          <div className="form-row">
            <label>{t("mapping.pricing.output")}</label>
            <input className="input" value={form.outputCostPerMillion} onChange={(e) => set("outputCostPerMillion", e.target.value)} />
          </div>
        </div>
        <div className="row2">
          <div className="form-row">
            <label>{t("mapping.pricing.cacheRead")}</label>
            <input className="input" value={form.cacheReadCostPerMillion} onChange={(e) => set("cacheReadCostPerMillion", e.target.value)} />
          </div>
          <div className="form-row">
            <label>{t("mapping.pricing.cacheWrite")}</label>
            <input className="input" value={form.cacheCreationCostPerMillion} onChange={(e) => set("cacheCreationCostPerMillion", e.target.value)} />
          </div>
        </div>
        <p className="muted" style={{ fontSize: 12 }}>{t("mapping.pricing.perMillionHint")}</p>
        <div className="actions" style={{ justifyContent: "flex-end", marginTop: 16 }}>
          <button type="button" className="btn" onClick={onClose}>{t("mapping.cancel")}</button>
          <button type="submit" className="btn primary" disabled={saving}>
            {saving ? t("mapping.native.saving") : t("mapping.save")}
          </button>
        </div>
      </form>
    </div>
  );
}
