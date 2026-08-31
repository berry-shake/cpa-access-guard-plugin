import { useEffect, useMemo, useState } from "react";
import { fetchCatalog } from "../api/models";
import { useT } from "../i18n";
import type { CatalogModel, NativeAllowedModel, NativeModelAccessPolicy } from "../types";

interface Props {
  value: NativeModelAccessPolicy;
  disabled?: boolean;
  catalogOverride?: CatalogModel[];
  onRefreshOverride?: () => void | Promise<void>;
  onChange: (value: NativeModelAccessPolicy) => void;
}

interface NativeModelGroup {
  provider: string;
  models: string[];
}

function modelKey(model: NativeAllowedModel): string {
  return model.provider.trim().toLowerCase() + "\0" + model.model.trim().toLowerCase();
}

function normalizedModels(models: NativeAllowedModel[]): NativeAllowedModel[] {
  const unique = new Map<string, NativeAllowedModel>();
  for (const model of models) {
    const provider = model.provider.trim().toLowerCase();
    const name = model.model.trim();
    if (!provider || !name) continue;
    const normalized = { provider, model: name };
    if (!unique.has(modelKey(normalized))) unique.set(modelKey(normalized), normalized);
  }
  return Array.from(unique.values()).sort((a, b) =>
    a.provider.localeCompare(b.provider) || a.model.toLowerCase().localeCompare(b.model.toLowerCase()),
  );
}

// Native-key model authorization is independent from credential tiers. The
// same provider/model may appear in several catalog groups, so collapse those
// rows before rendering checkboxes.
export function buildNativeModelGroups(catalog: CatalogModel[]): NativeModelGroup[] {
  const providers = new Map<string, Map<string, string>>();
  for (const row of catalog) {
    const provider = row.provider.trim().toLowerCase();
    const model = row.model.trim();
    if (!provider || !model) continue;
    let models = providers.get(provider);
    if (!models) {
      models = new Map<string, string>();
      providers.set(provider, models);
    }
    const key = model.toLowerCase();
    if (!models.has(key)) models.set(key, model);
  }
  return Array.from(providers, ([provider, models]) => ({
    provider,
    models: Array.from(models.values()).sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase())),
  })).sort((a, b) => a.provider.localeCompare(b.provider));
}

export default function NativeModelAccessPicker({
  value,
  disabled = false,
  catalogOverride,
  onRefreshOverride,
  onChange,
}: Props) {
  const t = useT();
  const [catalog, setCatalog] = useState<CatalogModel[]>([]);
  const [loading, setLoading] = useState(value.mode === "allowlist");
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [reload, setReload] = useState(0);

  useEffect(() => {
    if (value.mode !== "allowlist") {
      setLoading(false);
      return;
    }
    if (catalogOverride !== undefined) {
      setCatalog(catalogOverride);
      setError("");
      setLoading(false);
      return;
    }
    let alive = true;
    setLoading(true);
    setError("");
    const selectedProviders = new Set(value.models.map((model) => model.provider.trim().toLowerCase()));
    void fetchCatalog(selectedProviders)
      .then((rows) => {
        if (alive) setCatalog(rows);
      })
      .catch((reason: unknown) => {
        if (!alive) return;
        const message = reason instanceof Error ? reason.message : String(reason);
        setError(message || t("mapping.native.modelLoadFailed"));
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
    // Reload only when entering allow-list mode or explicitly refreshing. A
    // checkbox toggle must not refetch the whole CPA model catalog.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value.mode, reload, catalogOverride]);

  const groups = useMemo(() => buildNativeModelGroups(catalog), [catalog]);
  const selectedModels = useMemo(() => normalizedModels(value.models), [value.models]);
  const selectedKeys = useMemo(() => new Set(selectedModels.map(modelKey)), [selectedModels]);
  const catalogKeys = useMemo(() => {
    const keys = new Set<string>();
    for (const group of groups) {
      for (const model of group.models) keys.add(modelKey({ provider: group.provider, model }));
    }
    return keys;
  }, [groups]);
  const stale = useMemo(
    () => selectedModels.filter((model) => !catalogKeys.has(modelKey(model))),
    [catalogKeys, selectedModels],
  );
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return groups;
    return groups
      .map((group) => ({
        ...group,
        models: group.models.filter((model) =>
          group.provider.includes(needle) || model.toLowerCase().includes(needle),
        ),
      }))
      .filter((group) => group.models.length > 0);
  }, [groups, query]);

  const replaceModels = (models: NativeAllowedModel[]) => {
    onChange({ mode: "allowlist", models: normalizedModels(models) });
  };
  const toggle = (provider: string, model: string) => {
    const target = { provider, model };
    const key = modelKey(target);
    if (selectedKeys.has(key)) replaceModels(selectedModels.filter((entry) => modelKey(entry) !== key));
    else replaceModels([...selectedModels, target]);
  };
  const select = (models: NativeAllowedModel[]) => replaceModels([...selectedModels, ...models]);
  const clear = (models: NativeAllowedModel[]) => {
    const removed = new Set(models.map(modelKey));
    replaceModels(selectedModels.filter((model) => !removed.has(modelKey(model))));
  };
  const visibleModels = filtered.flatMap((group) =>
    group.models.map((model) => ({ provider: group.provider, model })),
  );

  return (
    <div className="native-model-access">
      <fieldset className="native-restriction-mode native-model-mode">
        <legend>{t("mapping.native.modelMode")}</legend>
        <div className="native-restriction-options">
          <label className={value.mode === "all" ? "active" : ""}>
            <input
              type="radio"
              name="native-model-mode"
              checked={value.mode === "all"}
              disabled={disabled}
              onChange={() => onChange({ mode: "all", models: [] })}
            />
            <span>
              <strong>{t("mapping.native.allModels")}</strong>
              <small>{t("mapping.native.allModelsHint")}</small>
            </span>
          </label>
          <label className={value.mode === "allowlist" ? "active" : ""}>
            <input
              type="radio"
              name="native-model-mode"
              checked={value.mode === "allowlist"}
              disabled={disabled}
              onChange={() => onChange({ mode: "allowlist", models: [] })}
            />
            <span>
              <strong>{t("mapping.native.selectedModels")}</strong>
              <small>{t("mapping.native.selectedModelsHint")}</small>
            </span>
          </label>
        </div>
      </fieldset>

      {value.mode === "allowlist" && (
        <div className="native-model-picker">
          <div className="native-model-toolbar">
            <input
              id="native-model-search"
              type="search"
              value={query}
              disabled={disabled}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t("mapping.native.modelSearch")}
            />
            <button
              className="btn sm"
              type="button"
              disabled={disabled || loading}
              onClick={() => {
                if (onRefreshOverride) void onRefreshOverride();
                else setReload((current) => current + 1);
              }}
            >
              {t("mapping.native.refreshModels")}
            </button>
          </div>
          <div className="native-model-actions">
            <span>{t("mapping.native.modelSelectedCount", { count: selectedModels.length })}</span>
            <button
              className="btn sm"
              type="button"
              disabled={disabled || visibleModels.length === 0}
              onClick={() => select(visibleModels)}
            >
              {t("mapping.native.selectAllModels")}
            </button>
            <button
              className="btn sm"
              type="button"
              disabled={disabled || selectedModels.length === 0}
              onClick={() => replaceModels([])}
            >
              {t("mapping.native.clearAllModels")}
            </button>
          </div>
          {selectedModels.length === 0 && (
            <div className="native-model-deny-warning" role="note">
              {t("mapping.native.noModelsSelectedWarning")}
            </div>
          )}
          {error && <div className="error" role="alert">{error}</div>}
          {stale.length > 0 && (
            <div className="native-model-stale">
              <div className="native-model-provider-head">{t("mapping.native.staleModels")}</div>
              <div className="native-model-options">
                {stale.map((entry) => (
                  <label className="active stale" key={modelKey(entry)}>
                    <input
                      type="checkbox"
                      checked
                      disabled={disabled}
                      onChange={() => toggle(entry.provider, entry.model)}
                    />
                    <span>{entry.model}</span>
                    <small>{entry.provider}</small>
                  </label>
                ))}
              </div>
            </div>
          )}
          {loading && groups.length === 0 ? (
            <div className="muted native-model-empty">{t("mapping.native.loadingModels")}</div>
          ) : groups.length === 0 && !error && stale.length === 0 ? (
            <div className="muted native-model-empty">{t("mapping.native.noModels")}</div>
          ) : filtered.length === 0 && !error ? (
            <div className="muted native-model-empty">{t("mapping.native.noModelMatches")}</div>
          ) : (
            <div className="native-model-groups">
              {filtered.map((group) => {
                const groupModels = group.models.map((model) => ({ provider: group.provider, model }));
                const allSelected = groupModels.every((model) => selectedKeys.has(modelKey(model)));
                return (
                  <section className="native-model-provider" key={group.provider}>
                    <div className="native-model-provider-head">
                      <span>{group.provider}</span>
                      <button
                        className="btn sm"
                        type="button"
                        disabled={disabled}
                        onClick={() => (allSelected ? clear(groupModels) : select(groupModels))}
                      >
                        {t(allSelected ? "mapping.native.clearProviderModels" : "mapping.native.selectProviderModels")}
                      </button>
                    </div>
                    <div className="native-model-options">
                      {group.models.map((model) => {
                        const active = selectedKeys.has(modelKey({ provider: group.provider, model }));
                        return (
                          <label className={active ? "active" : ""} key={model}>
                            <input
                              type="checkbox"
                              checked={active}
                              disabled={disabled}
                              onChange={() => toggle(group.provider, model)}
                            />
                            <span>{model}</span>
                          </label>
                        );
                      })}
                    </div>
                  </section>
                );
              })}
            </div>
          )}
          <p className="native-binding-field-hint">{t("mapping.native.modelAccessHint")}</p>
        </div>
      )}
    </div>
  );
}
