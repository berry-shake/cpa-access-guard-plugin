import { apiClient, pluginPath } from "./client";
import type {
  AliasMapping,
  ClassifyRule,
  CredentialDescriptor,
  ClassifyPreviewResponse,
  NativeKeyBinding,
  NativeKeyBindingCatalog,
  NativeKeyBindingCreateRequest,
  NativeKeyBindingUpdateRequest,
  NativeCredentialOption,
} from "../types";
import { readPlanType } from "./models";
import { fetchAIProviderCredentials } from "./aiProviderCredentials";

// --- Alias mapping CRUD ---

export async function fetchAliases(): Promise<AliasMapping[]> {
  const c = apiClient();
  const { data } = await c.get<{ aliases: AliasMapping[] }>(pluginPath("/aliases"));
  return data.aliases ?? [];
}

export async function upsertAlias(alias: AliasMapping): Promise<AliasMapping> {
  const c = apiClient();
  const { data } = await c.post<{ alias: AliasMapping }>(pluginPath("/aliases"), alias);
  return data.alias;
}

export async function deleteAlias(aliasName: string): Promise<void> {
  const c = apiClient();
  await c.delete(pluginPath("/aliases"), { data: { alias: aliasName } });
}

// --- Classification rule CRUD ---

export async function fetchClassifyRules(): Promise<ClassifyRule[]> {
  const c = apiClient();
  const { data } = await c.get<{ rules: ClassifyRule[] }>(pluginPath("/classify-rules"));
  return data.rules ?? [];
}

export async function upsertClassifyRule(rule: ClassifyRule): Promise<ClassifyRule> {
  const c = apiClient();
  const { data } = await c.post<{ rule: ClassifyRule }>(pluginPath("/classify-rules"), rule);
  return data.rule;
}

export async function deleteClassifyRule(name: string): Promise<void> {
  const c = apiClient();
  await c.delete(pluginPath("/classify-rules"), { data: { name } });
}

export async function reorderClassifyRules(names: string[]): Promise<void> {
  const c = apiClient();
  await c.post(pluginPath("/classify-rules/reorder"), { names });
}

// --- Classify preview ---

export async function classifyPreview(
  descriptors: CredentialDescriptor[],
  rules?: ClassifyRule[],
): Promise<ClassifyPreviewResponse> {
  const c = apiClient();
  const body: Record<string, unknown> = { descriptors };
  if (rules !== undefined) body.rules = rules;
  const { data } = await c.post<ClassifyPreviewResponse>(pluginPath("/classify-preview"), body);
  // Older/incompatible backends may return only group aggregates. Treat that
  // as unavailable: a missing per-rule result must never become a UI claim of
  // "0 matches" for every rule.
  const raw = data as unknown as Record<string, unknown> | null;
  const ruleMatches = raw?.["rule_matches"];
  if (!ruleMatches || typeof ruleMatches !== "object" || Array.isArray(ruleMatches)) {
    throw new Error("classify-preview response is missing rule_matches");
  }
  return data;
}

// Fields that this UI can reconstruct closely enough from CPA's /auth-files
// management response to preview. Runtime classification can use other safe
// Scheduler Auth.Attributes too, but that endpoint does not expose provenance:
// note/priority/websockets may be management Metadata fallbacks even when the
// Scheduler candidate has no same-named Attribute. Do not show an exact count
// for those fields; a false positive is worse than an explicit "unavailable".
const CLASSIFY_PREVIEW_FIELDS = new Set([
  "filename",
  "id",
  "provider",
  "plan_type",
  "tier",
  "path",
  "weight",
]);

export function canPreviewClassifyField(field: string): boolean {
  return CLASSIFY_PREVIEW_FIELDS.has(field.trim().toLowerCase());
}

function copyDescriptorStringAttribute(
  source: Record<string, unknown>,
  target: Record<string, string>,
  key: string,
): void {
  const value = source[key];
  if (typeof value === "string" && value.trim()) {
    target[key] = value.trim();
  }
}

function copyDescriptorScalarAttribute(
  source: Record<string, unknown>,
  target: Record<string, string>,
  key: string,
): void {
  const value = source[key];
  if (typeof value === "string" && value.trim()) {
    target[key] = value.trim();
  } else if (typeof value === "number" && Number.isFinite(value)) {
    target[key] = String(value);
  } else if (typeof value === "boolean") {
    target[key] = String(value);
  }
}

// fetchCredentialDescriptors pulls the auth-file list from CPA and builds
// CredentialDescriptor[] for the classify-preview endpoint. It deliberately
// copies only fields whose management representation corresponds to the
// Scheduler Attribute used at runtime. Arbitrary auth JSON/Metadata fields are
// not Scheduler Attributes and are therefore not guessed here.
export async function fetchCredentialDescriptors(): Promise<CredentialDescriptor[]> {
  const c = apiClient();
  const { data } = await c.get<unknown>("/v0/management/auth-files");
  const root = data as Record<string, unknown> | null;
  const list = root?.["files"] ?? root?.["auth-files"];
  const items = Array.isArray(list) ? list : [];
  const out: CredentialDescriptor[] = [];
  for (const item of items) {
    const o = (item ?? {}) as Record<string, unknown>;
    const id = ((o["id"] as string) ?? (o["name"] as string) ?? "").trim();
    if (!id) continue;
    const provider = ((o["provider"] as string) ?? (o["type"] as string) ?? "").trim().toLowerCase();
    const attrs: Record<string, string> = {};
    // Codex exposes plan_type through its id_token claims. Antigravity uses a
    // separate top-level tier value; never relabel one provider's identity as
    // the other provider's Scheduler Attribute.
    const planType = provider === "codex" ? readPlanType(o) : "";
    if (planType) attrs["plan_type"] = planType;
    const tier = (o["tier"] as string) ?? "";
    if (provider === "antigravity" && typeof tier === "string" && tier.trim()) {
      attrs["tier"] = tier.trim().toLowerCase();
    }
    // `path` is emitted from Auth.Attributes by CPA. `weight` is normalized
    // into Auth.Attributes by CPA's file synthesizer (including plugin-parsed
    // files). Do not copy note/priority/websockets here: the management route
    // may synthesize those values from Metadata even when scheduler.pick will
    // not receive a corresponding Attribute.
    copyDescriptorStringAttribute(o, attrs, "path");
    copyDescriptorScalarAttribute(o, attrs, "weight");
    out.push({ id, provider, attributes: attrs });
  }
  return out;
}

// Fetch exact runtime Auth IDs for direct native-key restrictions. Unlike the
// classify preview adapter above, this path must never fall back from `id` to a
// display/file name: the Scheduler compares candidate IDs exactly, and guessing
// would create a binding that looks valid in the UI but can never match.
function credentialModelIDs(payload: unknown): string[] {
  const root = payload as Record<string, unknown> | null;
  const raw = root?.["models"];
  if (!Array.isArray(raw)) return [];
  const unique = new Map<string, string>();
  for (const item of raw) {
    const model = (item ?? {}) as Record<string, unknown>;
    const value = [model["id"], model["model"], model["name"]]
      .find((candidate) => typeof candidate === "string" && candidate.trim());
    if (typeof value !== "string") continue;
    const id = value.trim();
    if (!unique.has(id.toLowerCase())) unique.set(id.toLowerCase(), id);
  }
  return Array.from(unique.values()).sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
}

export async function fetchNativeCredentialOptions(): Promise<NativeCredentialOption[]> {
  const c = apiClient();
  const [{ data }, aiProviderCredentials] = await Promise.all([
    c.get<unknown>("/v0/management/auth-files"),
    fetchAIProviderCredentials(c),
  ]);
  const root = data as Record<string, unknown> | null;
  const list = root?.["files"] ?? root?.["auth-files"];
  const items = Array.isArray(list) ? list : [];

  const byID = new Map<string, NativeCredentialOption>();
  const optionalString = (value: unknown): string | undefined =>
    typeof value === "string" && value.trim() ? value.trim() : undefined;
  for (const item of items) {
    const entry = (item ?? {}) as Record<string, unknown>;
    const id = optionalString(entry["id"]);
    if (!id || byID.has(id)) continue;
    const provider = (optionalString(entry["provider"]) ?? optionalString(entry["type"]) ?? "").toLowerCase();
    const tier = optionalString(entry["tier"]);
    const plan = provider === "codex"
      ? readPlanType(entry)
      : provider === "antigravity" && tier
        ? tier.toLowerCase()
        : "";
    byID.set(id, {
      id,
      provider,
      name: optionalString(entry["name"]),
      label: optionalString(entry["label"]),
      email: optionalString(entry["email"]),
      status: optionalString(entry["status"]),
      plan: plan || undefined,
      disabled: entry["disabled"] === true,
      unavailable: entry["unavailable"] === true,
      source: "auth_file",
    });
  }

  const authFileCredentials = Array.from(byID.values());
  let modelCursor = 0;
  await Promise.all(Array.from({ length: Math.min(6, authFileCredentials.length) }, async () => {
    for (;;) {
      const index = modelCursor++;
      if (index >= authFileCredentials.length) return;
      const credential = authFileCredentials[index];
      try {
        const { data: modelData } = await c.get<unknown>("/v0/management/auth-files/models", {
          params: { name: credential.id },
        });
        credential.models = credentialModelIDs(modelData);
      } catch {
        // This model list is display metadata. Runtime enforcement still uses
        // the exact Auth ID, so a temporary catalog error must not widen access.
      }
    }
  }));

  for (const credential of aiProviderCredentials) {
    if (!credential.id || byID.has(credential.id)) continue;
    byID.set(credential.id, credential);
  }

  return Array.from(byID.values()).sort((a, b) => {
    const sourceA = a.source === "ai_provider" ? 1 : 0;
    const sourceB = b.source === "ai_provider" ? 1 : 0;
    if (sourceA !== sourceB) return sourceA - sourceB;
    const byProvider = a.provider.localeCompare(b.provider);
    if (byProvider !== 0) return byProvider;
    const labelA = a.label ?? a.email ?? a.name ?? a.id;
    const labelB = b.label ?? b.email ?? b.name ?? b.id;
    const byLabel = labelA.localeCompare(labelB);
    return byLabel !== 0 ? byLabel : a.id.localeCompare(b.id);
  });
}

// --- CPA top-level API-key binding CRUD ---

// Read the host's current in-memory api-keys list through CPA's Management
// API. Keep plaintext values in component memory only: callers must never
// render them, put them in URLs, or persist them in browser storage.
export async function fetchTopLevelAPIKeys(): Promise<string[]> {
  const c = apiClient();
  const { data } = await c.get<{ "api-keys"?: unknown }>("/v0/management/api-keys");
  const raw = data?.["api-keys"];
  if (!Array.isArray(raw)) return [];

  const keys: string[] = [];
  const seen = new Set<string>();
  for (const value of raw) {
    if (typeof value !== "string") continue;
    const key = value.trim();
    if (!key || seen.has(key)) continue;
    seen.add(key);
    keys.push(key);
  }
  return keys;
}

// Ask the plugin to correlate host keys with persisted bindings by exact
// caller_scope. Sending the keys in a Management-authenticated JSON body
// avoids the collision risk of matching redacted previews and keeps secrets
// out of URLs and responses.
export async function fetchNativeKeyBindingCatalog(apiKeys: string[]): Promise<NativeKeyBindingCatalog> {
  const c = apiClient();
  const { data } = await c.post<Partial<NativeKeyBindingCatalog>>(
    pluginPath("/native-key-bindings/catalog"),
    { api_keys: apiKeys },
  );
  return {
    entries: Array.isArray(data.entries) ? data.entries : [],
    orphan_bindings: Array.isArray(data.orphan_bindings) ? data.orphan_bindings : [],
  };
}

export async function fetchNativeKeyBindings(): Promise<NativeKeyBinding[]> {
  const c = apiClient();
  const { data } = await c.get<{ bindings: NativeKeyBinding[] }>(pluginPath("/native-key-bindings"));
  return data.bindings ?? [];
}

export async function createNativeKeyBinding(
  input: NativeKeyBindingCreateRequest,
): Promise<NativeKeyBinding> {
  const c = apiClient();
  const { data } = await c.post<{ binding: NativeKeyBinding }>(pluginPath("/native-key-bindings"), input);
  return data.binding;
}

export async function updateNativeKeyBinding(
  input: NativeKeyBindingUpdateRequest,
): Promise<NativeKeyBinding> {
  const c = apiClient();
  const { data } = await c.patch<{ binding: NativeKeyBinding }>(pluginPath("/native-key-bindings"), input);
  return data.binding;
}

export async function deleteNativeKeyBinding(id: string): Promise<void> {
  const c = apiClient();
  await c.delete(pluginPath("/native-key-bindings"), { data: { id } });
}

export async function resetNativeKeyBindingQuota(id: string): Promise<void> {
  const c = apiClient();
  await c.post(pluginPath("/native-key-bindings/reset-quota"), { id });
}

// --- dual-source model pricing sync ---

export interface PricingSourceSyncState {
  lastAttemptAt?: number;
  lastSyncAt?: number;
  lastSyncError?: string;
  fetched?: number;
  accepted?: number;
}

export interface PricingSourcesState {
  litellm?: PricingSourceSyncState;
  modelsDev?: PricingSourceSyncState;
}

export interface PricingSyncResult {
  at: string;
  updated: number;
  unmatched: number;
  skipped: number;
  catalog_updated?: number;
  catalog?: number;
  litellm?: number;
  models_dev?: number;
  manual?: number;
  legacy?: number;
  known_unpriced?: number;
  stale?: number;
  sources?: PricingSourcesState;
  partial?: boolean;
  pricing_file?: string;
  error?: string;
}

export interface PricingSyncStatus {
  enabled: boolean;
  interval_hours?: number;
  url?: string;
  litellm_url?: string;
  models_dev_url?: string;
  pricing_file?: string;
  catalog_size?: number;
  sources?: PricingSourcesState;
  last_result?: PricingSyncResult | null;
  next_run_at?: string;
}

export async function fetchPricingSyncStatus(): Promise<PricingSyncStatus> {
  const c = apiClient();
  const { data } = await c.get<PricingSyncStatus>(pluginPath("/pricing-sync"));
  return data;
}

export async function runPricingSync(): Promise<PricingSyncResult> {
  const c = apiClient();
  const { data } = await c.post<PricingSyncResult>(pluginPath("/pricing-sync/run"));
  return data;
}

export interface ModelPricing {
  modelId: string;
  displayName: string;
  inputCostPerMillion: string;
  outputCostPerMillion: string;
  cacheReadCostPerMillion: string;
  cacheCreationCostPerMillion: string;
  imageInputCostPerMillion?: string;
  imageOutputCostPerMillion?: string;
  provider?: string;
  source?: "manual" | "alias" | "legacy" | "litellm" | "models.dev" | string;
  sourceModelId?: string;
  status?: "priced" | "known_unpriced" | "stale" | string;
  mode?: "text" | "image_generation" | string;
  lastSeenAt?: number;
}

export interface ModelPricingCatalog {
  models: ModelPricing[];
  deletedModelIds: string[];
}

export async function fetchModelPricing(): Promise<ModelPricingCatalog> {
  const c = apiClient();
  const { data } = await c.get<{ models?: ModelPricing[]; deleted_model_ids?: string[] }>(pluginPath("/pricing"));
  return { models: data.models ?? [], deletedModelIds: data.deleted_model_ids ?? [] };
}

export async function upsertModelPricing(row: ModelPricing): Promise<ModelPricing> {
  const c = apiClient();
  const { data } = await c.post<{ model: ModelPricing }>(pluginPath("/pricing"), row);
  return data.model;
}

export async function deleteModelPricing(modelId: string): Promise<void> {
  const c = apiClient();
  await c.delete(pluginPath("/pricing"), { data: { modelId } });
}

export async function restoreAutomaticModelPricing(modelId: string): Promise<void> {
  const c = apiClient();
  await c.post(pluginPath("/pricing/restore-auto"), { modelId });
}
