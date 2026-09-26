import { apiClient, pluginPath } from "./client";
import { getSession } from "../store/session";
import type { NativeKeyBindingCatalog } from "../types";

export interface NativeQuotaResetBinding {
  id: string;
  name: string;
  keyPreview: string;
  enabled: boolean;
  createdAt: string;
  // Identity comparison only. Never render or persist the full key.
  key: string;
}

export interface NativeQuotaResetResult {
  reset: true;
  ids: string[];
  count: number;
}

export function createNativeQuotaResetClient() {
  const session = getSession();
  const client = apiClient();
  const base = pluginPath("/native-key-bindings");
  const checkSession = () => {
    if (getSession() !== session) throw new Error("quota_reset_session_changed");
  };
  return {
    async loadBindings(): Promise<NativeQuotaResetBinding[]> {
      checkSession();
      const response = await client.get<{ "api-keys"?: unknown }>("/v0/management/api-keys");
      checkSession();
      const raw = response.data?.["api-keys"];
      if (!Array.isArray(raw) || raw.some((key) => typeof key !== "string")) {
        throw new Error("quota_reset_inventory_unavailable");
      }
      const keys = Array.from(new Set((raw as string[]).map((key) => key.trim()).filter(Boolean)));
      const { data } = await client.post<NativeKeyBindingCatalog>(base + "/catalog", { api_keys: keys });
      checkSession();
      if (!Array.isArray(data?.entries) || data.entries.length !== keys.length) {
        throw new Error("quota_reset_inventory_unavailable");
      }
      const indices = new Set<number>();
      const ids = new Set<string>();
      const bindings: NativeQuotaResetBinding[] = [];
      for (const entry of data.entries) {
        if (!Number.isInteger(entry.key_index) || entry.key_index < 0 || entry.key_index >= keys.length || indices.has(entry.key_index)) {
          throw new Error("quota_reset_inventory_unavailable");
        }
        indices.add(entry.key_index);
        if (!entry.binding) continue;
        const binding = entry.binding;
        if (!binding.id || typeof binding.id !== "string" || ids.has(binding.id)) {
          throw new Error("quota_reset_inventory_unavailable");
        }
        ids.add(binding.id);
        bindings.push({
          id: binding.id, name: binding.name || binding.id,
          keyPreview: entry.key_preview || binding.key_preview || "<redacted>",
          enabled: binding.enabled, createdAt: typeof binding.created_at === "string" ? binding.created_at : "",
          key: keys[entry.key_index],
        });
      }
      return bindings;
    },
    async reset(ids: string[]): Promise<NativeQuotaResetResult> {
      checkSession();
      const { data } = await client.post<NativeQuotaResetResult>(base + "/reset-quota-batch", { ids });
      checkSession();
      return data;
    },
  };
}
