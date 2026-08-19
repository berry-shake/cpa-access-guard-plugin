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
} from "../types";
import { readPlanType } from "./models";

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
  if (!Array.isArray(list)) return [];
  const out: CredentialDescriptor[] = [];
  for (const item of list) {
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

// --- models.dev pricing sync ---

export interface PricingSyncResult {
  at: string;
  updated: number;
  unmatched: number;
  skipped: number;
  catalog?: number;
  pricing_file?: string;
  error?: string;
}

export interface PricingSyncStatus {
  enabled: boolean;
  interval_hours?: number;
  url?: string;
  pricing_file?: string;
  catalog_size?: number;
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
}

export async function fetchModelPricing(): Promise<ModelPricing[]> {
  const c = apiClient();
  const { data } = await c.get<{ models?: ModelPricing[] }>(pluginPath("/pricing"));
  return data.models ?? [];
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
