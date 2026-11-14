import { apiClient, pluginPath } from "./client";
import type {
  NativeKeyBinding,
  NativeKeyBindingCatalog,
  NativeKeyBindingCreateRequest,
  NativeKeyBindingUpdateRequest,
  NativeCredentialOption,
  NativeBindingCredentialCatalog,
} from "../types";
import { fetchAIProviderCredentials } from "./aiProviderCredentials";

function optionalString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

// Codex plan from one /auth-files row. A live CPA ListAuthFiles response
// flattens the id_token claims onto id_token (id_token.plan_type); the nested
// id_token.claims.plan_type shape is tolerated defensively. Returns "" when no
// recognizable claim is present.
export function readPlanType(entry: Record<string, unknown>): string {
  const idToken = entry["id_token"];
  if (!idToken || typeof idToken !== "object") return "";
  const token = idToken as Record<string, unknown>;
  const claims = token["claims"];
  for (const plan of [token["plan_type"], claims && typeof claims === "object"
    ? (claims as Record<string, unknown>)["plan_type"] : undefined]) {
    if (typeof plan === "string" && plan.trim() !== "") return plan.trim().toLowerCase();
  }
  return "";
}

function authFileRows(payload: unknown): Record<string, unknown>[] | null {
  if (!payload || typeof payload !== "object" || Array.isArray(payload)) return null;
  const root = payload as Record<string, unknown>;
  const rows = root["files"] ?? root["auth-files"];
  if (!Array.isArray(rows)) return null;
  return rows.filter((row): row is Record<string, unknown> =>
    row !== null && typeof row === "object" && !Array.isArray(row));
}

function authFileInventoryShapeValid(payload: unknown, rows: Record<string, unknown>[] | null): rows is Record<string, unknown>[] {
  if (rows === null) return false;
  const root = payload as Record<string, unknown>;
  const raw = root["files"] ?? root["auth-files"];
  return Array.isArray(raw) && raw.length === rows.length;
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
    // Runtime-only OAuth/virtual credentials are not config providers.
    source: entry["concurrency_config"] === true ? "ai_provider" : "auth_file",
    authIndex: optionalString(entry["auth_index"]),
  };
}

// A runtime identity is authoritative. Config metadata may improve its display
// name, but may never replace its ID, provider, status, or runtime model list.
// Older hosts omit config identities from /auth-files and still need the
// existing derived-ID fallback. Both native-key views use this same merge.
function mergeNativeCredentialSources(
  runtime: Map<string, NativeCredentialOption>,
  configured: NativeCredentialOption[],
  includeModels = false,
): Map<string, NativeCredentialOption> {
  const merged = new Map(runtime);
  for (const credential of configured) {
    const id = optionalString(credential.id);
    if (!id) continue;
    const current = merged.get(id);
    if (current) {
      if (current.source === "ai_provider" && current.provider === credential.provider) {
        merged.set(id, {
          ...current,
          name: optionalString(credential.name) ?? current.name,
          authIndex: current.authIndex ?? optionalString(credential.authIndex),
        });
      }
      continue;
    }
    merged.set(id, {
      id, provider: (optionalString(credential.provider) ?? "").toLowerCase(),
      email: optionalString(credential.email), label: optionalString(credential.label),
      name: optionalString(credential.name), status: optionalString(credential.status),
      disabled: credential.disabled === true, unavailable: credential.unavailable === true,
      source: "ai_provider", authIndex: optionalString(credential.authIndex),
      ...(includeModels ? {
        models: credential.models, identityVerified: credential.identityVerified,
        configIndex: credential.configIndex,
      } : {}),
    });
  }
  return merged;
}

// Fetch exact runtime Auth IDs for direct native-key restrictions. This path
// must never fall back from `id` to a display/file name: the Scheduler compares
// candidate IDs exactly, and guessing would create a binding that looks valid
// in the UI but can never match.
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
  const rows = authFileRows(data);
  if (!authFileInventoryShapeValid(data, rows)) throw new Error("credential_inventory_unavailable");
  const byID = new Map<string, NativeCredentialOption>();
  for (const entry of rows) {
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

  const merged = mergeNativeCredentialSources(byID, aiProviderCredentials, true);
  return Array.from(merged.values()).sort((a, b) => {
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

// Load card identities once for the whole key list. This deliberately excludes
// per-credential model discovery; a failed lookup affects display only.
export async function fetchNativeBindingCredentialCatalog(
  c = apiClient(),
): Promise<NativeBindingCredentialCatalog> {
  const [authResult, aiResult] = await Promise.allSettled([
    c.get<unknown>("/v0/management/auth-files"),
    fetchAIProviderCredentials(c, false),
  ]);
  const rows = authResult.status === "fulfilled" ? authFileRows(authResult.value.data) : null;
  const byID = new Map<string, NativeCredentialOption>();
  for (const row of rows ?? []) {
    const identity = authFileIdentity(row);
    if (identity && !byID.has(identity.id)) byID.set(identity.id, identity);
  }
  const merged = mergeNativeCredentialSources(byID, aiResult.status === "fulfilled" ? aiResult.value : []);
  const credentials = Array.from(merged.values()).sort((left, right) => {
    const a = left.email ?? left.label ?? left.name ?? left.id;
    const b = right.email ?? right.label ?? right.name ?? right.id;
    return a.localeCompare(b) || left.id.localeCompare(right.id);
  });
  const identitiesComplete = authResult.status === "fulfilled"
    && authFileInventoryShapeValid(authResult.value.data, rows) && aiResult.status === "fulfilled"
    && rows.every((row) => optionalString(row["id"]) !== undefined);
  return { credentials, identitiesComplete };
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
