import { apiClient, pluginPath } from "./client";
import { fetchNativeBindingCredentialCatalog } from "./mappings";
import { getSession } from "../store/session";
import type { ClassifyRule, NativeBindingCredentialCatalog, NativeKeyBindingCatalog } from "../types";

export interface NativeBindingRestriction {
  group?: string;
  auth_ids?: string[];
}

export interface NativeBindingChange {
  binding_id: string;
  name: string;
  key_preview: string;
  before: NativeBindingRestriction | null;
  after: NativeBindingRestriction | null;
}

export interface NativeBindingPreview {
  revision: string;
  changes: NativeBindingChange[];
  conflicts: Array<{ binding_id: string; code: string }>;
  can_apply: boolean;
  noop: boolean;
  warnings: string[];
}

export interface NativeBindingOperation {
  id: string;
  kind: "batch" | "rollback";
  source_operation_id?: string;
  created_at: string;
  changes: NativeBindingChange[];
  reverted_by?: string;
}

export interface NativeBindingMutationResult {
  operation?: NativeBindingOperation;
  changed: number;
  noop: boolean;
}

export interface NativeBindingBatchInventory {
  apiKeys: string[];
  catalog: NativeKeyBindingCatalog;
  credentials: NativeBindingCredentialCatalog;
}

export interface NativeBindingBatchRequest {
  api_keys: string[];
  selected_indices: number[];
  auth_ids: string[];
  available_auth_ids: string[];
  catalog_complete: boolean;
  expected_revision?: string;
}

export interface NativeBindingRollbackRequest {
  operation_id: string;
  api_keys: string[];
  available_auth_ids: string[];
  catalog_complete: boolean;
  expected_revision?: string;
}

// One dialog owns one authenticated client. Never combine keys read from one
// CPA session with writes to another session after a login/host switch.
export function createNativeBindingBatchClient() {
  const session = getSession();
  const client = apiClient();
  const base = pluginPath("/native-key-bindings");
  const checkSession = () => {
    if (getSession() !== session) throw new Error("batch_session_changed");
  };
  const post = async <T,>(suffix: string, body: unknown): Promise<T> => {
    checkSession();
    const { data } = await client.post<T>(base + suffix, body);
    checkSession();
    return data;
  };
  return {
    async loadInventory(): Promise<NativeBindingBatchInventory> {
      checkSession();
      const [keyResponse, ruleResponse] = await Promise.all([
        client.get<{ "api-keys"?: unknown }>("/v0/management/api-keys"),
        client.get<{ rules: ClassifyRule[] }>(pluginPath("/classify-rules")).catch(() => null),
      ]);
      checkSession();
      const rawKeys = keyResponse.data["api-keys"];
      if (!Array.isArray(rawKeys) || rawKeys.some((key) => typeof key !== "string")) {
        throw new Error("batch_inventory_unavailable");
      }
      const apiKeys = Array.from(new Set((rawKeys as string[]).map((key) => key.trim()).filter(Boolean)));
      const [catalog, credentials] = await Promise.all([
        post<NativeKeyBindingCatalog>("/catalog", { api_keys: apiKeys }),
        fetchNativeBindingCredentialCatalog(ruleResponse?.data.rules ?? null, client),
      ]);
      checkSession();
      if (!Array.isArray(catalog.entries) || !Array.isArray(catalog.orphan_bindings)) {
        throw new Error("batch_inventory_unavailable");
      }
      return { apiKeys, catalog, credentials };
    },
    async history(): Promise<NativeBindingOperation[]> {
      checkSession();
      const { data } = await client.get<{ operations: NativeBindingOperation[] }>(base + "/history");
      checkSession();
      if (!Array.isArray(data.operations)) throw new Error("batch_history_unavailable");
      return data.operations;
    },
    async preview(body: NativeBindingBatchRequest): Promise<NativeBindingPreview> {
      return (await post<{ preview: NativeBindingPreview }>("/batch-preview", body)).preview;
    },
    apply(body: NativeBindingBatchRequest) {
      return post<NativeBindingMutationResult>("/batch", body);
    },
    async previewRollback(body: NativeBindingRollbackRequest): Promise<NativeBindingPreview> {
      return (await post<{ preview: NativeBindingPreview }>("/rollback-preview", body)).preview;
    },
    rollback(body: NativeBindingRollbackRequest) {
      return post<NativeBindingMutationResult>("/rollback", body);
    },
  };
}
