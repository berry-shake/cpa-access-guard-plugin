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
  NativeBindingCredentialCatalog,
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
  client = apiClient(),
): Promise<ClassifyPreviewResponse> {
  const body: Record<string, unknown> = { descriptors };
  if (rules !== undefined) body.rules = rules;
  const { data } = await client.post<ClassifyPreviewResponse>(pluginPath("/classify-preview"), body);
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

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function authFileRows(payload: unknown): Record<string, unknown>[] | null {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) return null;
  const root = payload as Record<string, unknown>;
  const rows = root["files"] ?? root["auth-files"];
  if (!Array.isArray(rows)) return null;
  return rows.filter((row): row is Record<string, unknown> =>
    row !== null && typeof row === "object" && !Array.isArray(row));
}

function authFileDescriptor(entry: Record<string, unknown>, allowNameFallback: boolean): CredentialDescriptor | undefined {
  const id = optionalString(entry["id"]) ?? (allowNameFallback ? optionalString(entry["name"]) : undefined);
  if (!id) return undefined;
  const provider = (optionalString(entry["provider"]) ?? optionalString(entry["type"]) ?? "").toLowerCase();
  const attributes: Record<string, string> = {};
  const plan = provider === "codex" ? readPlanType(entry) : "";
  if (plan) attributes["plan_type"] = plan;
  const tier = optionalString(entry["tier"]);
  if (provider === "antigravity" && tier) attributes["tier"] = tier.toLowerCase();
  copyDescriptorStringAttribute(entry, attributes, "path");
  copyDescriptorScalarAttribute(entry, attributes, "weight");
  return { id, provider, attributes };
}

function authFileIdentity(entry: Record<string, unknown>): NativeCredentialOption | undefined {
  const id = optionalString(entry["id"]);
  if (!id) return undefined;
  const provider = (optionalString(entry["provider"]) ?? optionalString(entry["type"]) ?? "").toLowerCase();
  const tier = optionalString(entry["tier"]);
  const plan = provider === "codex"
    ? readPlanType(entry)
    : provider === "antigravity" && tier ? tier.toLowerCase() : "";
  return {
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
  };
}

// fetchCredentialDescriptors pulls the auth-file list from CPA and builds
// CredentialDescriptor[] for the classify-preview endpoint. It deliberately
// copies only fields whose management representation corresponds to the
// Scheduler Attribute used at runtime. Arbitrary auth JSON/Metadata fields are
// not Scheduler Attributes and are therefore not guessed here.
export async function fetchCredentialDescriptors(): Promise<CredentialDescriptor[]> {
  const c = apiClient();
  const { data } = await c.get<unknown>("/v0/management/auth-files");
  const out: CredentialDescriptor[] = [];
  for (const entry of authFileRows(data) ?? []) {
    const descriptor = authFileDescriptor(entry, true);
    if (descriptor) out.push(descriptor);
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
  const byID = new Map<string, NativeCredentialOption>();
  for (const entry of authFileRows(data) ?? []) {
    const identity = authFileIdentity(entry);
    if (identity && !byID.has(identity.id)) byID.set(identity.id, identity);
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

function nativeBindingCustomGroup(group: string): string {
  const normalized = group.trim().toLowerCase();
  return normalized.startsWith("classify:") ? normalized : "classify:" + normalized;
}

function descriptorBuiltinGroup(descriptor: CredentialDescriptor): string {
  const attributes = descriptor.attributes ?? {};
  return attributes["plan_type"]?.trim().toLowerCase()
    || attributes["tier"]?.trim().toLowerCase()
    || (["codex", "antigravity"].includes(descriptor.provider) ? "supported" : "");
}

function nativeBindingGroupCatalog(
  descriptors: CredentialDescriptor[],
  rules: ClassifyRule[],
  preview: ClassifyPreviewResponse,
  missingFields: Set<string>,
): Pick<NativeBindingCredentialCatalog, "groups" | "unavailableGroups" | "groupsAvailable"> {
  if (!preview.groups || typeof preview.groups !== "object" || Array.isArray(preview.groups)) {
    return { groups: {}, unavailableGroups: [], groupsAvailable: false };
  }
  const knownIDs = new Set(descriptors.map(({ id }) => id));
  const groups = new Map<string, Set<string>>();
  const unavailable = new Set<string>();
  const customMatches = new Set<string>();
  const customNames = new Set<string>();
  const builtinGroups = new Set(descriptors.map(descriptorBuiltinGroup).filter(Boolean));
  let builtinAvailable = true;
  const safeMembers = (raw: unknown, group: string): string[] => {
    if (!Array.isArray(raw)) {
      unavailable.add(group);
      return [];
    }
    const ids: string[] = [];
    for (const value of raw) {
      if (typeof value !== "string" || !knownIDs.has(value)) {
        unavailable.add(group);
      } else {
        ids.push(value);
      }
    }
    return ids;
  };
  for (const rule of rules) {
    if (!rule.enabled) continue;
    const group = nativeBindingCustomGroup(rule.group);
    customNames.add(rule.group.trim().toLowerCase());
    customNames.add(group);
    if (!groups.has(group)) groups.set(group, new Set());
    const field = rule.field.trim().toLowerCase();
    if (!canPreviewClassifyField(field) || missingFields.has(field)) {
      unavailable.add(group);
      builtinAvailable = false;
      continue;
    }
    const ids = safeMembers(preview.rule_matches[rule.name.trim()], group);
    if (unavailable.has(group)) builtinAvailable = false;
    for (const id of ids) {
      groups.get(group)!.add(id);
      customMatches.add(id);
    }
  }

  // Preview aggregates use raw custom names, which may collide with built-in
  // tiers. Rule-specific results identify every known custom match to subtract
  // before exposing built-in members; native runtime uses classify: prefixes.
  for (const [rawGroup, rawMembers] of Object.entries(preview.groups)) {
    const group = rawGroup.trim().toLowerCase();
    if (!group || group.startsWith("classify:")) continue;
    const members = safeMembers(rawMembers, group).filter((id) => !customMatches.has(id));
    if (members.length === 0 && customNames.has(group)) continue;
    builtinGroups.add(group);
    groups.set(group, new Set(members));
  }
  if (!builtinAvailable) {
    for (const group of builtinGroups) unavailable.add(group);
  }
  return {
    groups: Object.fromEntries(Array.from(groups, ([group, ids]) => [group, Array.from(ids).sort()])),
    unavailableGroups: Array.from(unavailable).sort(),
    groupsAvailable: true,
  };
}

// Load card identities once for the whole key list. This deliberately excludes
// per-credential model discovery and never reconstructs regex matching in JS.
// Missing classification data affects display only, never saved restrictions.
export async function fetchNativeBindingCredentialCatalog(
  rules: ClassifyRule[] | null,
): Promise<NativeBindingCredentialCatalog> {
  const c = apiClient();
  const [authResult, aiResult] = await Promise.allSettled([
    c.get<unknown>("/v0/management/auth-files"),
    fetchAIProviderCredentials(c, false),
  ]);
  const rows = authResult.status === "fulfilled" ? authFileRows(authResult.value.data) : null;
  const byID = new Map<string, NativeCredentialOption>();
  const descriptors = new Map<string, CredentialDescriptor>();
  for (const row of rows ?? []) {
    const identity = authFileIdentity(row);
    const descriptor = authFileDescriptor(row, false);
    if (!identity || !descriptor || byID.has(identity.id)) continue;
    byID.set(identity.id, identity);
    descriptors.set(identity.id, descriptor);
  }
  const missingFields = new Set<string>();
  if (aiResult.status === "fulfilled") {
    for (const credential of aiResult.value) {
      const id = optionalString(credential.id);
      if (!id || byID.has(id)) continue;
      const provider = (optionalString(credential.provider) ?? "").toLowerCase();
      byID.set(id, {
        id, provider,
        email: optionalString(credential.email),
        label: optionalString(credential.label),
        name: optionalString(credential.name),
        status: optionalString(credential.status),
        disabled: credential.disabled === true,
        unavailable: credential.unavailable === true,
        source: "ai_provider",
      });
      // Configured API providers expose exact derived IDs and provider names,
      // but their safe weight attribute is not part of this identity adapter.
      // Do not treat that unknown value as a proven non-match for weight rules.
      descriptors.set(id, { id, provider, attributes: {} });
      missingFields.add("weight");
    }
  }
  const credentials = Array.from(byID.values()).sort((left, right) => {
    const a = left.email ?? left.label ?? left.name ?? left.id;
    const b = right.email ?? right.label ?? right.name ?? right.id;
    return a.localeCompare(b) || left.id.localeCompare(right.id);
  });
  const identitiesComplete = rows !== null && aiResult.status === "fulfilled"
    && rows.every((row) => optionalString(row["id"]) !== undefined);
  const unavailable: NativeBindingCredentialCatalog = {
    credentials, identitiesComplete, groups: {}, unavailableGroups: [], groupsAvailable: false,
  };
  if (rules === null || !identitiesComplete) {
    return unavailable;
  }
  const input = Array.from(descriptors.values());
  try {
    const preview = await classifyPreview(input, rules, c);
    return { credentials, identitiesComplete, ...nativeBindingGroupCatalog(input, rules, preview, missingFields) };
  } catch {
    return unavailable;
  }
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
