// Shapes mirrored from Access Guard (internal/policy/config.go).
// and CPA management responses. Only the fields the UI needs are declared.

// One provider/model row offered by the native-key model allow-list picker.
export interface CatalogModel {
  provider: string;
  model: string;
}

export interface StatusResponse {
  enabled: boolean;
  state_file: string;
  pricing_file?: string;
  pricing_catalog_size?: number;
}

export interface NativeBindingUsageSummary {
  rpm_limit: number;
  daily_usd_limit: number;
  weekly_usd_limit: number;
  rpm_used: number;
  daily_usd_used: number;
  weekly_usd_used: number;
  daily_calls: number;
  weekly_calls: number;
  weekly_reset_at?: string;
}

export interface NativeAllowedModel {
  provider: string;
  model: string;
}

export interface NativeModelAccessPolicy {
  mode: "all" | "allowlist";
  models: NativeAllowedModel[];
}

// NativeKeyBinding maps a CPA top-level `api-keys` entry to an exact Auth ID
// allow-list. The plaintext key and derived caller_scope are deliberately
// absent from management responses; only a redacted preview is exposed to the
// UI.
export interface NativeKeyBinding {
  id: string;
  name: string;
  enabled: boolean;
  // Older backends omit this field; default to the existing scheduling behavior.
  round_robin?: boolean;
  key_preview: string;
  // Legacy credential group from older versions. Read-only: such a binding
  // has no auth_ids, comes back with needs_reselection and refuses traffic
  // until credentials are picked again.
  group?: string;
  auth_ids?: string[];
  // Missing only when talking to a pre-fork.13 backend; treat it as `all`.
  model_access?: NativeModelAccessPolicy;
  needs_reselection?: boolean;
  // Usage limits; 0/omitted = unlimited. Pricing comes from the model price
  // table.
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
  round_robin?: boolean;
  key: string;
  auth_ids?: string[];
  model_access?: NativeModelAccessPolicy;
  rpm?: number;
  daily_usd?: number;
  weekly_usd?: number;
}

export interface NativeKeyBindingUpdateRequest {
  id: string;
  name?: string;
  enabled?: boolean;
  round_robin?: boolean;
  // Empty/omitted keeps the currently bound top-level API key.
  key?: string;
  auth_ids?: string[];
  model_access?: NativeModelAccessPolicy;
  rpm?: number;
  daily_usd?: number;
  weekly_usd?: number;
}

// One exact Scheduler auth candidate exposed by CPA's Management API. Direct
// native-key bindings persist only `id`; the other fields are display hints.
export interface NativeCredentialOption {
  id: string;
  provider: string;
  name?: string;
  label?: string;
  email?: string;
  status?: string;
  plan?: string;
  disabled?: boolean;
  unavailable?: boolean;
  source?: "auth_file" | "ai_provider";
  authIndex?: string;
  configIndex?: number;
  models?: string[];
  identityVerified?: boolean;
}

// Read-only identity data for native-key cards, keyed by exact Auth ID.
export interface NativeBindingCredentialCatalog {
  credentials: NativeCredentialOption[];
  identitiesComplete: boolean;
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
