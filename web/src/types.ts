// Shapes mirrored from CPA Access Guard (internal/policy/config.go).
// and CPA management responses. Only the fields the UI needs are declared.

export interface ModelRule {
  alias: string;
  provider: string;
  target_model: string;
  // Optional tier/plan narrowing for providers whose auth files carry an
  // identity claim (codex plan_type, antigravity tier). Empty = "any file for
  // the provider" (legacy). The plugin's Scheduler filters auth candidates by
  // this so a downstream key pinned to, say, codex "team" only ever lands on a
  // team auth file. UI catalog groups mirror this value.
  group?: string;
  input_price_per_million?: number;
  output_price_per_million?: number;
  cache_read_price_per_million?: number;
  cache_write_price_per_million?: number;
  // billing_mode selects how this alias is billed per successful request:
  //   - "tokens" (default): bill by token counts using the three prices above.
  //   - "per_call": bill a fixed per_call_usd per successful request, ignoring
  //     token counts. The token-price fields are preserved but dormant.
  billing_mode?: "tokens" | "per_call";
  // per_call_usd is the fixed USD charge per successful request when
  // billing_mode === "per_call". 0 is allowed (free calls). Only meaningful
  // under "per_call".
  per_call_usd?: number;
}

export interface UsageSummary {
  daily_usd: number;
  weekly_usd: number;
  daily_limit_usd: number;
  weekly_limit_usd: number;
  daily_reset_at?: string;
  weekly_reset_at?: string;
  // Cache reporting (omitted when zero). Hit-rate is derived client-side as
  // cache_read_tokens / (cache_read_tokens + input_tokens).
  daily_cache_cost_usd?: number;
  weekly_cache_cost_usd?: number;
  daily_cache_read_tokens?: number;
  weekly_cache_read_tokens?: number;
  daily_cache_write_tokens?: number;
  weekly_cache_write_tokens?: number;
  daily_cache_write_cost_usd?: number;
  weekly_cache_write_cost_usd?: number;
  daily_input_tokens?: number;
  weekly_input_tokens?: number;
  // Call counts: successful requests billed into the window (token or
  // per-call). Failed requests don't count. Display only.
  daily_call_count?: number;
  weekly_call_count?: number;
}

export interface KeyPublic {
  id: string;
  name: string;
  enabled: boolean;
  key_preview: string;
  rpm: number;
  models: ModelRule[];
  aliases?: KeyAliasRef[];
  daily_limit_usd: number;
  weekly_limit_usd: number;
  // Per-key override for GET /v1/models (see KeyFormValues).
  allow_models_endpoint?: boolean;
  usage: UsageSummary;
  created_at?: string;
  updated_at?: string;
}

export interface KeyWriteRequest {
  id: string;
  name?: string;
  enabled?: boolean;
  key?: string;
  rpm?: number;
  models?: ModelRule[];
  aliases?: KeyAliasRef[];
  daily_limit_usd?: number;
  weekly_limit_usd?: number;
  allow_models_endpoint?: boolean;
}

export interface CreateKeyResponse {
  key: KeyPublic;
  plain_key: string;
  generated: boolean;
}

export interface RotateKeyResponse {
  key: KeyPublic;
  plain_key: string;
  generated: boolean;
}

// UsageWindow mirrors policy.UsageWindow: a dollar total bound to a window
// start, plus cache/input/output/call counters for display. The key detail
// page reads one Daily and one Weekly per alias.
export interface UsageWindow {
  total_usd: number;
  window_start?: string;
  cache_read_tokens?: number;
  cache_cost_usd?: number;
  input_tokens?: number;
  output_tokens?: number;
  call_count?: number;
}

// AliasUsageEntry mirrors policy.AliasUsageEntry: one row of the per-alias
// usage breakdown for a key. Configured aliases have in_config=true; aliases
// with historical usage that are no longer in the key's config have
// in_config=false (residuals).
export interface AliasUsageEntry {
  alias: string;
  provider?: string;
  target_model?: string;
  billing_mode?: "tokens" | "per_call";
  per_call_usd?: number;
  in_config: boolean;
  daily: UsageWindow;
  weekly: UsageWindow;
}

export interface KeyUsageResponse {
  key_id: string;
  key_name: string;
  daily_limit_usd: number;
  weekly_limit_usd: number;
  aliases: AliasUsageEntry[];
}

// A model the user can pick when creating/editing a key.
export interface CatalogModel {
  provider: string;
  // group is set for providers whose auth files carry a tier/plan identity
  // (codex plan_type, antigravity tier). Same model may appear under several
  // groups when multiple tiers' auth files all support it — each is a distinct
  // selectable row pinning a different tier.
  group?: string;
  model: string;
}

export interface StatusResponse {
  enabled: boolean;
  state_file: string;
  pricing_file?: string;
  pricing_catalog_size?: number;
  key_count: number;
  rpm_usage?: Record<string, unknown>;
}

// --- Advanced Mapping types ---

// AliasTarget is one selectable destination for an alias.
export interface AliasTarget {
  provider: string;
  target_model: string;
  group?: string;
}

// AliasMapping is one entry in the global alias mapping table.
export interface AliasMapping {
  alias: string;
  targets: AliasTarget[];
  dispatch: "round-robin" | "priority";
  billing_mode: "tokens" | "per_call";
  input_price_per_million?: number;
  output_price_per_million?: number;
  cache_read_price_per_million?: number;
  cache_write_price_per_million?: number;
  per_call_usd?: number;
}

// ClassifyRule is a user-defined credential classification rule.
export interface ClassifyRule {
  name: string;
  field: string; // "filename" | "provider" | "plan_type" | "tier" | custom
  pattern: string; // regex
  group: string; // target group name
  enabled: boolean;
}

// KeyAliasRef is a key's reference to a global alias, with optional per-key
// price overrides (null = use global default).
export interface KeyAliasRef {
  alias: string;
  input_price_per_million?: number | null;
  output_price_per_million?: number | null;
  cache_read_price_per_million?: number | null;
  cache_write_price_per_million?: number | null;
  per_call_usd?: number | null;
}

// CredentialDescriptor is a normalized credential description for classify preview.
export interface CredentialDescriptor {
  id: string;
  provider: string;
  attributes?: Record<string, string>;
}

// ClassifyPreviewResponse is the result of POST /classify-preview.
export interface ClassifyPreviewResponse {
  groups: Record<string, string[]>; // group name → credential IDs
  group_counts: Record<string, number>; // group name → count
  rule_matches: Record<string, string[]>; // rule name → that rule's own credential IDs
}

// NativeKeyBinding maps a CPA top-level `api-keys` entry to one credential
// group. The plaintext key and derived caller_scope are deliberately absent
// from management responses; only a redacted preview is exposed to the UI.
export interface NativeBindingUsageSummary {
  rpm_limit: number;
  daily_usd_limit: number;
  weekly_usd_limit: number;
  rpm_used: number;
  daily_usd_used: number;
  weekly_usd_used: number;
  daily_calls: number;
  weekly_calls: number;
}

export interface NativeKeyBinding {
  id: string;
  name: string;
  enabled: boolean;
  key_preview: string;
  group: string;
  // Usage limits; 0/omitted = unlimited. Pricing comes from the global
  // alias table.
  rpm?: number;
  daily_usd?: number;
  weekly_usd?: number;
  usage?: NativeBindingUsageSummary;
  created_at?: string;
  updated_at?: string;
}

export interface NativeKeyBindingCreateRequest {
  id: string;
  name?: string;
  enabled?: boolean;
  key: string;
  group: string;
  rpm?: number;
  daily_usd?: number;
  weekly_usd?: number;
}

export interface NativeKeyBindingUpdateRequest {
  id: string;
  name?: string;
  enabled?: boolean;
  // Empty/omitted keeps the currently bound top-level API key.
  key?: string;
  group?: string;
  rpm?: number;
  daily_usd?: number;
  weekly_usd?: number;
}

// NativeKeyCatalogEntry correlates one CPA top-level api-keys item with an
// existing Access Guard binding. The plaintext key is deliberately absent;
// key_index points back to the in-memory list returned by CPA Management.
export interface NativeKeyCatalogEntry {
  key_index: number;
  key_preview: string;
  binding?: NativeKeyBinding | null;
}

export interface NativeKeyBindingCatalog {
  entries: NativeKeyCatalogEntry[];
  // Bindings whose key is no longer present in CPA's top-level api-keys.
  orphan_bindings: NativeKeyBinding[];
}
