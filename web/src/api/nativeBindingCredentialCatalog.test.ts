import { beforeEach, describe, expect, it, vi } from "vitest";
import type { NativeCredentialOption } from "../types";

const mocks = vi.hoisted(() => ({
  apiClient: vi.fn(),
  get: vi.fn(),
  post: vi.fn(),
  fetchAIProviderCredentials: vi.fn(),
}));

vi.mock("./client", () => ({
  apiClient: mocks.apiClient,
  pluginPath: (suffix: string) => "/plugin" + suffix,
}));

vi.mock("./aiProviderCredentials", () => ({
  fetchAIProviderCredentials: mocks.fetchAIProviderCredentials,
}));

import { fetchNativeBindingCredentialCatalog } from "./mappings";

function authFile(id: string, email = id + "@example.com"): Record<string, unknown> {
  return { id, email, name: id + ".json", provider: "codex", id_token: { plan_type: "team" } };
}

function givenFiles(files: unknown[]): void {
  mocks.get.mockImplementation((url: string) => {
    if (url === "/v0/management/auth-files") return Promise.resolve({ data: { files } });
    return Promise.reject(new Error("Unexpected management request: " + url));
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  mocks.apiClient.mockReturnValue(mocks);
  givenFiles([]);
  mocks.fetchAIProviderCredentials.mockResolvedValue([]);
});

describe("native binding credential catalog identities", () => {
  it("sorts by email before labels and filenames while preserving each exact Auth ID", async () => {
    givenFiles([
      { ...authFile("runtime/a.json", "zulu@example.com"), name: "a-first.json", label: "A first label" },
      { ...authFile("runtime/z.json", "alpha@example.com"), name: "z-last.json", label: "Z last label" },
    ]);

    const catalog = await fetchNativeBindingCredentialCatalog();

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["runtime/z.json", "runtime/a.json"]);
    expect(catalog.identitiesComplete).toBe(true);
    expect(catalog.credentials[0]).toMatchObject({
      id: "runtime/z.json",
      email: "alpha@example.com",
      name: "z-last.json",
      label: "Z last label",
      provider: "codex",
      source: "auth_file",
      plan: "team",
    });
  });

  it("deduplicates exact IDs without merging separate credentials that share an email", async () => {
    givenFiles([
      { ...authFile("runtime/a"), email: "same@example.com", label: "First identity" },
      { ...authFile("runtime/a"), email: "replacement@example.com", label: "Duplicate identity" },
      { ...authFile("runtime/b"), email: "same@example.com" },
    ]);

    const catalog = await fetchNativeBindingCredentialCatalog();

    expect(catalog.credentials).toHaveLength(2);
    expect(catalog.credentials.map(({ id }) => id).sort()).toEqual(["runtime/a", "runtime/b"]);
    expect(catalog.credentials.find(({ id }) => id === "runtime/a")).toMatchObject({
      email: "same@example.com",
      label: "First identity",
    });
  });

  it("merges both identity sources without replacing an auth-file identity with a duplicate AI-provider ID", async () => {
    givenFiles([{ ...authFile("shared-id"), label: "Auth file identity" }]);
    mocks.fetchAIProviderCredentials.mockResolvedValue([
      { id: "shared-id", provider: "codex", label: "Duplicate AI identity", source: "ai_provider" },
      { id: "codex:apikey:abcdef123456", provider: "codex", label: "AI identity", source: "ai_provider" },
    ]);

    const catalog = await fetchNativeBindingCredentialCatalog();

    expect(catalog.credentials).toHaveLength(2);
    expect(catalog.credentials.find(({ id }) => id === "shared-id")).toMatchObject({
      label: "Auth file identity",
      source: "auth_file",
    });
    expect(catalog.credentials.find(({ id }) => id === "codex:apikey:abcdef123456")).toMatchObject({
      label: "AI identity",
      source: "ai_provider",
    });
  });

  it("keeps configured runtime identities in the AI group without overwriting live provider or availability", async () => {
    const id = "runtime:codex:authoritative";
    givenFiles([{
      ...authFile(id), concurrency_config: true, runtime_only: true,
      status: "rate-limited", disabled: true, unavailable: true, weight: 4, auth_index: "runtime-index",
      account_id: "sentinel-account", access_token: "sentinel-access",
      "api-key": "sentinel-runtime-key", "base-url": "https://sentinel-runtime.invalid",
    }]);
    mocks.fetchAIProviderCredentials.mockResolvedValue([{
      id, provider: "config-provider", name: "Configured Codex", label: "Config label",
      source: "ai_provider", status: "configured", disabled: false, unavailable: false,
      authIndex: "safe-index", identityVerified: false,
      account_id: "sentinel-config-account", "api-key": "sentinel-config-key",
      "base-url": "https://sentinel-config.invalid",
    }]);

    const catalog = await fetchNativeBindingCredentialCatalog();
    expect(catalog.identitiesComplete).toBe(true);
    expect(catalog.credentials).toHaveLength(1);
    expect(catalog.credentials[0]).toMatchObject({
      id, provider: "codex", source: "ai_provider", status: "rate-limited",
      disabled: true, unavailable: true, authIndex: "runtime-index",
    });
    expect(catalog.credentials[0].identityVerified).not.toBe(false);
    expect(JSON.stringify(catalog)).not.toContain("sentinel-");
  });

  it("distinguishes configured new providers from runtime-only OAuth without config metadata", async () => {
    givenFiles([
      { id: "new-provider-runtime", provider: "new-provider", concurrency_config: true, runtime_only: true, name: "New runtime" },
      { ...authFile("oauth-runtime"), runtime_only: true },
    ]);
    const catalog = await fetchNativeBindingCredentialCatalog();
    expect(catalog.identitiesComplete).toBe(true);
    expect(catalog.credentials.find(({ id }) => id === "new-provider-runtime")).toMatchObject({
      provider: "new-provider", source: "ai_provider", name: "New runtime",
    });
    expect(catalog.credentials.find(({ id }) => id === "oauth-runtime")).toMatchObject({ source: "auth_file" });
    expect(mocks.fetchAIProviderCredentials).toHaveBeenCalledWith(mocks, false);
    expect(mocks.get.mock.calls.some(([url]) => url === "/v0/management/auth-files/models")).toBe(false);
  });

  it("does not invent Auth IDs from filenames, emails, array positions, or malformed IDs", async () => {
    givenFiles([
      { name: "display-only.json", email: "missing@example.com", provider: "codex" },
      { id: " ", name: "blank-id.json", provider: "codex" },
      { id: 23, name: "numeric-id.json", provider: "codex" },
      null,
      { ...authFile(" Runtime/Case-Sensitive.json "), email: "valid@example.com" },
    ]);

    const catalog = await fetchNativeBindingCredentialCatalog();

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["Runtime/Case-Sensitive.json"]);
    expect(catalog.identitiesComplete).toBe(false);
  });

  it("retains valid identity rows but marks the inventory incomplete when a null row is present", async () => {
    givenFiles([authFile("valid-runtime"), null]);
    const catalog = await fetchNativeBindingCredentialCatalog();
    expect(catalog.credentials.map(({ id }) => id)).toEqual(["valid-runtime"]);
    expect(catalog.identitiesComplete).toBe(false);
    expect(mocks.post).not.toHaveBeenCalled();
  });

  it("projects only safe display fields", async () => {
    givenFiles([
      {
        ...authFile("tenant/codex.json"),
        provider: " CODEX ",
        email: " display@example.com ",
        path: "/auth/tenant/codex.json",
        weight: 3,
        tier: "not-a-codex-claim",
        status: "active",
        disabled: true,
        unavailable: true,
        access_token: "sentinel-access-secret",
        refresh_token: "sentinel-refresh-secret",
        "api-key": "sentinel-api-secret",
        account: "sentinel-account-field",
        account_id: "sentinel-account-secret",
        arbitrary: "sentinel-arbitrary-secret",
        note: "sentinel-note-secret",
        priority: 9,
        websockets: true,
        metadata: { access_token: "sentinel-metadata-secret" },
        id_token: { plan_type: " Team ", account_id: "sentinel-claim-secret" },
      },
      { id: "ag.json", type: "ANTIGRAVITY", tier: " Pro ", id_token: { plan_type: "not-an-ag-claim" } },
    ]);

    const catalog = await fetchNativeBindingCredentialCatalog();

    expect(catalog.credentials.find(({ id }) => id === "tenant/codex.json")).toMatchObject({
      provider: "codex",
      email: "display@example.com",
      plan: "team",
      disabled: true,
      unavailable: true,
    });
    expect(catalog.credentials.find(({ id }) => id === "ag.json")).toMatchObject({ provider: "antigravity", plan: "pro" });
    expect(JSON.stringify(catalog)).not.toContain("sentinel-");
  });
});

describe("native binding credential catalog availability and request cost", () => {
  it("accepts the alternate auth-files response key without making a second request", async () => {
    mocks.get.mockResolvedValue({ data: { "auth-files": [authFile("A")] } });

    const catalog = await fetchNativeBindingCredentialCatalog();

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["A"]);
    expect(mocks.get).toHaveBeenCalledTimes(1);
  });

  it.each([null, {}, { files: "not-an-array" }])(
    "does not treat an incompatible auth-file response as a successfully loaded empty inventory (%j)",
    async (data) => {
      mocks.get.mockResolvedValue({ data });

      const catalog = await fetchNativeBindingCredentialCatalog();

      expect(catalog.credentials).toEqual([]);
      expect(catalog.identitiesComplete).toBe(false);
    },
  );

  it("retains safe AI-provider identities when auth-file loading fails", async () => {
    const aiCredential: NativeCredentialOption = {
      id: "codex:apikey:123456789abc",
      provider: "codex",
      name: "Codex #1",
      source: "ai_provider",
      status: "configured",
    };
    mocks.get.mockRejectedValue(new Error("Auth-file endpoint unavailable"));
    mocks.fetchAIProviderCredentials.mockResolvedValue([aiCredential]);

    const catalog = await fetchNativeBindingCredentialCatalog();

    expect(catalog.credentials).toHaveLength(1);
    expect(catalog.credentials[0]).toMatchObject(aiCredential);
    expect(catalog.identitiesComplete).toBe(false);
    expect(mocks.post).not.toHaveBeenCalled();
  });

  it("returns an unavailable empty catalog when both identity sources fail", async () => {
    mocks.get.mockRejectedValue(new Error("Auth-file endpoint unavailable"));
    mocks.fetchAIProviderCredentials.mockRejectedValue(new Error("AI-provider endpoint unavailable"));

    const catalog = await fetchNativeBindingCredentialCatalog();

    expect(catalog.credentials).toEqual([]);
    expect(catalog.identitiesComplete).toBe(false);
  });

  it("retains auth-file identities but marks the inventory incomplete when AI-provider loading fails", async () => {
    givenFiles([authFile("A")]);
    mocks.fetchAIProviderCredentials.mockRejectedValue(new Error("AI-provider endpoint temporarily unavailable"));

    const catalog = await fetchNativeBindingCredentialCatalog();

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["A"]);
    expect(catalog.identitiesComplete).toBe(false);
    expect(mocks.post).not.toHaveBeenCalled();
  });

  it("loads one shared auth-file snapshot without per-credential model requests", async () => {
    const files = Array.from({ length: 240 }, (_, index) => authFile("tenant/" + index));
    givenFiles(files);

    const catalog = await fetchNativeBindingCredentialCatalog();

    expect(catalog.credentials).toHaveLength(240);
    expect(catalog.identitiesComplete).toBe(true);
    expect(mocks.get).toHaveBeenCalledTimes(1);
    expect(mocks.get).toHaveBeenCalledWith("/v0/management/auth-files");
    expect(mocks.get.mock.calls.some(([url]) => url === "/v0/management/auth-files/models")).toBe(false);
    expect(mocks.post).not.toHaveBeenCalled();
    expect(mocks.fetchAIProviderCredentials).toHaveBeenCalledTimes(1);
    expect(mocks.fetchAIProviderCredentials).toHaveBeenCalledWith(mocks, false);
  });

  it("keeps the captured management client after the session changes while the identity snapshot is loading", async () => {
    let resolveAuthFiles!: (value: { data: { files: Record<string, unknown>[] } }) => void;
    const authFiles = new Promise<{ data: { files: Record<string, unknown>[] } }>((resolve) => {
      resolveAuthFiles = resolve;
    });
    mocks.get.mockReturnValue(authFiles);
    const nextHostClient = { get: vi.fn(), post: vi.fn() };

    const pending = fetchNativeBindingCredentialCatalog();
    mocks.apiClient.mockReturnValue(nextHostClient);
    resolveAuthFiles({ data: { files: [authFile("old-host-auth-id")] } });
    const catalog = await pending;

    expect(catalog.identitiesComplete).toBe(true);
    expect(catalog.credentials.map(({ id }) => id)).toEqual(["old-host-auth-id"]);
    expect(mocks.apiClient).toHaveBeenCalledTimes(1);
    expect(mocks.get).toHaveBeenCalledWith("/v0/management/auth-files");
    expect(mocks.fetchAIProviderCredentials).toHaveBeenCalledWith(mocks, false);
    expect(nextHostClient.get).not.toHaveBeenCalled();
    expect(nextHostClient.post).not.toHaveBeenCalled();
  });
});
