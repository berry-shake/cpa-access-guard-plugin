import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  patch: vi.fn(),
  delete: vi.fn(),
}));

vi.mock("./client", () => ({
  apiClient: () => mocks,
  pluginPath: (suffix: string) => "/plugin" + suffix,
}));

import {
  canPreviewClassifyField,
  classifyPreview,
  createNativeKeyBinding,
  deleteNativeKeyBinding,
  fetchCredentialDescriptors,
  fetchNativeCredentialOptions,
  fetchNativeKeyBindingCatalog,
  fetchNativeKeyBindings,
  fetchTopLevelAPIKeys,
  updateNativeKeyBinding,
} from "./mappings";

const binding = {
  id: "client-a",
  name: "Client A",
  enabled: true,
  key_preview: "sk-ab...wxyz",
  group: "classify:vip",
};

beforeEach(() => {
  vi.resetAllMocks();
  mocks.get.mockImplementation((url: string) => {
    if (url === "/v0/management/auth-files/models") return Promise.reject(new Error("No model fixture"));
    const root = url.slice("/v0/management/".length);
    return Promise.resolve({ data: { [root]: [] } });
  });
});

describe("native key binding API", () => {
  it("lists exact runtime Auth IDs for direct binding without guessing from file names", async () => {
    mocks.get.mockResolvedValueOnce({
      data: {
        files: [
          {
            id: "tenant/codex-b.json",
            name: "codex-b.json",
            provider: "CODEX",
            label: "Account B",
            email: "b@example.com",
            status: "active",
            id_token: { plan_type: "Team" },
          },
          {
            id: "tenant/ag-a.json",
            name: "ag-a.json",
            type: "antigravity",
            tier: "Pro",
            disabled: true,
          },
          { name: "display-only.json", provider: "codex" },
          { id: "tenant/codex-b.json", provider: "codex", label: "duplicate" },
        ],
      },
    });

    await expect(fetchNativeCredentialOptions()).resolves.toEqual([
      {
        id: "tenant/ag-a.json",
        provider: "antigravity",
        name: "ag-a.json",
        label: undefined,
        email: undefined,
        status: undefined,
        plan: "pro",
        disabled: true,
        unavailable: false,
        source: "auth_file",
      },
      {
        id: "tenant/codex-b.json",
        provider: "codex",
        name: "codex-b.json",
        label: "Account B",
        email: "b@example.com",
        status: "active",
        plan: "team",
        disabled: false,
        unavailable: false,
        source: "auth_file",
      },
    ]);
    expect(mocks.get).toHaveBeenCalledWith("/v0/management/auth-files");
  });

  it.each([
    { label: "invalid root", data: { files: "not-an-array" } },
    { label: "invalid row", data: { files: [{ id: "valid-runtime", provider: "codex" }, null] } },
  ])("rejects an auth-file inventory with an $label instead of returning an empty or partial editor list", async ({ data }) => {
    mocks.get.mockResolvedValueOnce({ data });
    await expect(fetchNativeCredentialOptions()).rejects.toThrow();
  });

  it.each([true, false])("keeps the authoritative configured runtime identity and state with model discovery success=%s", async (modelsAvailable) => {
    const runtimeID = "codex:apikey:36f5c62aaa48";
    mocks.get.mockImplementation((url: string, options?: { params?: { name?: string } }) => {
      if (url === "/v0/management/auth-files") return Promise.resolve({ data: { files: [{
        id: runtimeID, provider: "codex", name: "Runtime Codex", concurrency_config: true, runtime_only: true,
        status: "cooldown", disabled: true, unavailable: true,
        account: "sentinel-account-field", account_id: "sentinel-account-secret", access_token: "sentinel-token-secret",
        "api-key": "sentinel-runtime-secret", "base-url": "https://sentinel-runtime.invalid",
      }] } });
      if (url === "/v0/management/codex-api-key") return Promise.resolve({ data: { "codex-api-key": [{
        "api-key": "demo-key", "base-url": "http://127.0.0.1:9/v1", "auth-index": "safe-config-index",
        models: [{ alias: "config-only-model" }],
      }] } });
      if (url === "/v0/management/auth-files/models") {
        expect(options?.params?.name).toBe(runtimeID);
        if (!modelsAvailable) return Promise.reject(new Error("Runtime models temporarily unavailable"));
        return Promise.resolve({ data: { models: [{ id: "runtime-spark" }] } });
      }
      const root = url.slice("/v0/management/".length);
      return Promise.resolve({ data: { [root]: [] } });
    });

    const result = await fetchNativeCredentialOptions();
    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({
      id: runtimeID, provider: "codex", source: "ai_provider", status: "cooldown",
      disabled: true, unavailable: true, authIndex: "safe-config-index",
    });
    if (modelsAvailable) expect(result[0].models).toEqual(["runtime-spark"]);
    else expect(result[0].models ?? []).not.toContain("config-only-model");
    expect(result[0].identityVerified).not.toBe(false);
    expect(JSON.stringify(result)).not.toMatch(/sentinel-|demo-key|127\.0\.0\.1|config-only-model/);
  });

  it("keeps an exact configured runtime ID selectable when models fail and no known config endpoint supplies it", async () => {
    const runtimeID = "new-provider:runtime:exact-id";
    mocks.get.mockImplementation((url: string) => {
      if (url === "/v0/management/auth-files") return Promise.resolve({ data: { files: [{
        id: runtimeID, provider: "new-provider", name: "New provider", concurrency_config: true,
        runtime_only: true, status: "active", disabled: false, unavailable: false,
      }] } });
      if (url === "/v0/management/auth-files/models") return Promise.reject(new Error("Model catalog unavailable"));
      const root = url.slice("/v0/management/".length);
      return Promise.resolve({ data: { [root]: [] } });
    });

    const result = await fetchNativeCredentialOptions();
    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({ id: runtimeID, provider: "new-provider", source: "ai_provider", status: "active" });
    expect(result[0].identityVerified).not.toBe(false);
    expect(mocks.get).toHaveBeenCalledWith("/v0/management/auth-files/models", { params: { name: runtimeID } });
  });

  it("does not classify runtime-only OAuth credentials as AI-provider configuration", async () => {
    mocks.get.mockResolvedValueOnce({ data: { files: [{
      id: "oauth-runtime-id", provider: "codex", runtime_only: true, concurrency_config: false,
      email: "oauth@example.test", status: "active",
    }] } });
    const result = await fetchNativeCredentialOptions();
    expect(result).toHaveLength(1);
    expect(result[0]).toMatchObject({ id: "oauth-runtime-id", source: "auth_file", email: "oauth@example.test" });
  });

  it("retains config-derived IDs and failed verification for older hosts without configured runtime rows", async () => {
    mocks.get.mockImplementation((url: string) => {
      if (url === "/v0/management/auth-files") return Promise.resolve({ data: { files: [] } });
      if (url === "/v0/management/codex-api-key") return Promise.resolve({ data: { "codex-api-key": [{
        "api-key": "demo-key", "base-url": "http://127.0.0.1:9/v1", models: [{ alias: "legacy-spark" }],
      }] } });
      if (url === "/v0/management/auth-files/models") return Promise.reject(new Error("Unknown runtime ID"));
      const root = url.slice("/v0/management/".length);
      return Promise.resolve({ data: { [root]: [] } });
    });
    await expect(fetchNativeCredentialOptions()).resolves.toMatchObject([{
      id: "codex:apikey:36f5c62aaa48", source: "ai_provider", models: ["legacy-spark"], identityVerified: false,
    }]);
  });

  it("lists normalized top-level keys through CPA Management", async () => {
    mocks.get.mockResolvedValueOnce({
      data: { "api-keys": [" sk-alpha ", "sk-beta", "", null, 3, "sk-alpha"] },
    });

    await expect(fetchTopLevelAPIKeys()).resolves.toEqual(["sk-alpha", "sk-beta"]);
    expect(mocks.get).toHaveBeenCalledWith("/v0/management/api-keys");

    mocks.get.mockResolvedValueOnce({ data: { "api-keys": null } });
    await expect(fetchTopLevelAPIKeys()).resolves.toEqual([]);
  });

  it("matches top-level keys in a protected JSON body without putting them in the URL", async () => {
    const catalog = {
      entries: [{ key_index: 0, key_preview: "sk-al...lpha", binding }],
      orphan_bindings: [],
    };
    mocks.post.mockResolvedValueOnce({ data: catalog });
    const apiKeys = ["sk-alpha-secret"];

    await expect(fetchNativeKeyBindingCatalog(apiKeys)).resolves.toEqual(catalog);
    expect(mocks.post).toHaveBeenCalledWith("/plugin/native-key-bindings/catalog", { api_keys: apiKeys });
    expect(String(mocks.post.mock.calls[0][0])).not.toContain("sk-alpha-secret");
  });

  it("lists native key bindings and tolerates an omitted list", async () => {
    mocks.get.mockResolvedValueOnce({ data: { bindings: [binding] } });
    await expect(fetchNativeKeyBindings()).resolves.toEqual([binding]);
    expect(mocks.get).toHaveBeenCalledWith("/plugin/native-key-bindings");

    mocks.get.mockResolvedValueOnce({ data: {} });
    await expect(fetchNativeKeyBindings()).resolves.toEqual([]);
  });

  it("creates a binding with the one-time plaintext key", async () => {
    mocks.post.mockResolvedValueOnce({ data: { binding } });
    const input = {
      id: "client-a",
      name: "Client A",
      enabled: true,
      key: "sk-secret",
      group: "classify:vip",
    };

    await expect(createNativeKeyBinding(input)).resolves.toEqual(binding);
    expect(mocks.post).toHaveBeenCalledWith("/plugin/native-key-bindings", input);
  });

  it("patches fields without requiring another plaintext key", async () => {
    mocks.patch.mockResolvedValueOnce({ data: { binding: { ...binding, enabled: false } } });
    const input = { id: "client-a", enabled: false };

    await updateNativeKeyBinding(input);
    expect(mocks.patch).toHaveBeenCalledWith("/plugin/native-key-bindings", input);
    expect(mocks.patch.mock.calls[0][1]).not.toHaveProperty("key");
  });

  it.each([true, false])("sends an explicit round-robin setting of %s without changing other fields", async (roundRobin) => {
    const updated = { ...binding, round_robin: roundRobin };
    mocks.patch.mockResolvedValueOnce({ data: { binding: updated } });
    const input = { id: binding.id, round_robin: roundRobin };

    await expect(updateNativeKeyBinding(input)).resolves.toEqual(updated);
    expect(mocks.patch).toHaveBeenCalledWith("/plugin/native-key-bindings", input);
    expect(mocks.patch.mock.calls[0][1]).not.toHaveProperty("enabled");
    expect(mocks.patch.mock.calls[0][1]).not.toHaveProperty("key");
  });

  it("deletes by id in the request body", async () => {
    mocks.delete.mockResolvedValueOnce({ data: {} });
    await deleteNativeKeyBinding("client-a");
    expect(mocks.delete).toHaveBeenCalledWith("/plugin/native-key-bindings", {
      data: { id: "client-a" },
    });
  });
});

describe("credential classification preview", () => {
  it("copies only management fields that correspond to runtime Scheduler attributes", async () => {
    mocks.get.mockResolvedValueOnce({
      data: {
        files: [
          {
            id: "tenant-a/codex.json",
            provider: "CODEX",
            id_token: { plan_type: "Team" },
            tier: "must-not-be-copied-for-codex",
            note: " tenant-a ",
            path: "/auth/tenant-a/codex.json",
            priority: 7,
            weight: "3",
            websockets: false,
            email: "metadata-only@example.com",
          },
          {
            id: "ag.json",
            provider: "antigravity",
            id_token: { plan_type: "must-not-be-copied-for-antigravity" },
            tier: "Pro",
          },
        ],
      },
    });

    await expect(fetchCredentialDescriptors()).resolves.toEqual([
      {
        id: "tenant-a/codex.json",
        provider: "codex",
        attributes: {
          plan_type: "team",
          path: "/auth/tenant-a/codex.json",
          weight: "3",
        },
      },
      {
        id: "ag.json",
        provider: "antigravity",
        attributes: { tier: "pro" },
      },
    ]);
  });

  it("marks the exact UI-previewable field subset", () => {
    for (const field of ["filename", "ID", "provider", "plan_type", "tier", "path", "weight"]) {
      expect(canPreviewClassifyField(field)).toBe(true);
    }
    for (const field of ["note", "priority", "websockets", "auth_kind", "source_backend", "email", "access_token"]) {
      expect(canPreviewClassifyField(field)).toBe(false);
    }
  });

  it("distinguishes omitted rules from an explicitly empty draft rule set", async () => {
    mocks.post.mockResolvedValue({ data: { groups: {}, group_counts: {}, rule_matches: {} } });
    await classifyPreview([]);
    expect(mocks.post).toHaveBeenNthCalledWith(1, "/plugin/classify-preview", { descriptors: [] });

    await classifyPreview([], []);
    expect(mocks.post).toHaveBeenNthCalledWith(2, "/plugin/classify-preview", { descriptors: [], rules: [] });
  });

  it("rejects an incompatible preview response instead of implying zero matches", async () => {
    mocks.post.mockResolvedValueOnce({ data: { groups: {}, group_counts: {} } });
    await expect(classifyPreview([])).rejects.toThrow("missing rule_matches");
  });
});
