import { beforeEach, describe, expect, it, vi } from "vitest";
import { createNativeBindingBatchClient } from "./nativeBindingBatch";

const mocks = vi.hoisted(() => ({
  get: vi.fn(), post: vi.fn(), apiClient: vi.fn(), getSession: vi.fn(), catalog: vi.fn(),
}));
vi.mock("./client", () => ({ apiClient: mocks.apiClient, pluginPath: (path: string) => "/plugin" + path }));
vi.mock("./mappings", () => ({ fetchNativeBindingCredentialCatalog: mocks.catalog }));
vi.mock("../store/session", () => ({ getSession: mocks.getSession }));

const keys = ["sk-synthetic-batch-api-alpha", "sk-synthetic-batch-api-beta"];
const client = { get: mocks.get, post: mocks.post };
const catalog = { entries: [{ key_index: 0, key_preview: "sk-sy...alpha" }], orphan_bindings: [] };
const identities = { credentials: [{ id: "auth-a", provider: "codex" }], identitiesComplete: true, groups: {}, unavailableGroups: [], groupsAvailable: false };
const body = { api_keys: keys, selected_indices: [0], auth_ids: ["auth-a"], available_auth_ids: ["auth-a"], catalog_complete: true };
const preview = { revision: "review-revision", changes: [], conflicts: [], can_apply: true, noop: false, warnings: [] };

beforeEach(() => {
  vi.resetAllMocks();
  mocks.apiClient.mockReturnValue(client);
  mocks.getSession.mockReturnValue({ baseUrl: "http://fixture.invalid", secretKey: "synthetic-management" });
  mocks.get.mockImplementation(async (path: string) => {
    if (path.endsWith("/api-keys")) return { data: { "api-keys": keys } };
    if (path.endsWith("/classify-rules")) return { data: { rules: [] } };
    if (path.endsWith("/history")) return { data: { operations: [] } };
    throw new Error("unexpected path");
  });
  mocks.post.mockImplementation(async (path: string) => ({ data: path.endsWith("/catalog") ? catalog : { preview } }));
  mocks.catalog.mockResolvedValue(identities);
});

describe("native binding batch API", () => {
  it("uses one captured authenticated client for key inventory, identities, preview and commit", async () => {
    const api = createNativeBindingBatchClient();
    await expect(api.loadInventory()).resolves.toEqual({ apiKeys: keys, catalog, credentials: identities });
    expect(mocks.catalog).toHaveBeenCalledWith([], client);
    expect(mocks.post).toHaveBeenCalledWith("/plugin/native-key-bindings/catalog", { api_keys: keys });
    await expect(api.preview(body)).resolves.toEqual(preview);
    const commit = { ...body, expected_revision: preview.revision };
    mocks.post.mockResolvedValueOnce({ data: { changed: 1, noop: false } });
    await expect(api.apply(commit)).resolves.toEqual({ changed: 1, noop: false });
    expect(mocks.post).toHaveBeenLastCalledWith("/plugin/native-key-bindings/batch", commit);
    expect(mocks.apiClient).toHaveBeenCalledTimes(1);
    expect(mocks.get.mock.calls.some(([path]) => String(path).includes("/auth-files/models"))).toBe(false);
    for (const [path] of [...mocks.get.mock.calls, ...mocks.post.mock.calls]) {
      for (const key of keys) expect(path).not.toContain(key);
    }
  });

  it("blocks a dialog's reads and writes after the management session changes", async () => {
    const api = createNativeBindingBatchClient();
    mocks.getSession.mockReturnValue({ baseUrl: "http://other-fixture.invalid", secretKey: "other-management" });
    await expect(api.preview(body)).rejects.toThrow("batch_session_changed");
    await expect(api.apply({ ...body, expected_revision: "revision" })).rejects.toThrow("batch_session_changed");
    await expect(api.loadInventory()).rejects.toThrow("batch_session_changed");
    await expect(api.history()).rejects.toThrow("batch_session_changed");
    expect(mocks.post).not.toHaveBeenCalled();
    expect(mocks.get).not.toHaveBeenCalled();
  });

  it("does not send host keys into a later request when the session changes during inventory loading", async () => {
    const api = createNativeBindingBatchClient();
    mocks.get.mockImplementation(async (path: string) => {
      if (path.endsWith("/api-keys")) {
        mocks.getSession.mockReturnValue({ baseUrl: "http://changed-fixture.invalid", secretKey: "changed-management" });
        return { data: { "api-keys": keys } };
      }
      return { data: { rules: [] } };
    });
    await expect(api.loadInventory()).rejects.toThrow("batch_session_changed");
    expect(mocks.post).not.toHaveBeenCalled();
    expect(mocks.catalog).not.toHaveBeenCalled();
  });

  it("passes classification failure separately while retaining the identity completeness signal", async () => {
    const api = createNativeBindingBatchClient();
    mocks.get.mockImplementation(async (path: string) => {
      if (path.endsWith("/classify-rules")) throw new Error("rules unavailable");
      return { data: { "api-keys": keys } };
    });
    await expect(api.loadInventory()).resolves.toEqual({ apiKeys: keys, catalog, credentials: identities });
    expect(mocks.catalog).toHaveBeenCalledWith(null, client);
  });

  it.each([{}, { "api-keys": "not-a-list" }, { "api-keys": [keys[0], null] }])(
    "rejects an incomplete host key response without previewing it as an empty selection: %j", async (payload) => {
      mocks.get.mockResolvedValue({ data: payload });
      await expect(createNativeBindingBatchClient().loadInventory()).rejects.toThrow("batch_inventory_unavailable");
      expect(mocks.post).not.toHaveBeenCalled();
    },
  );

  it("reads complete history details and sends restore IDs only in the request body", async () => {
    const api = createNativeBindingBatchClient();
    await expect(api.history()).resolves.toEqual([]);
    const restore = { operation_id: "history-entry", api_keys: keys, available_auth_ids: ["auth-a"], catalog_complete: true };
    await expect(api.previewRollback(restore)).resolves.toEqual(preview);
    expect(mocks.post).toHaveBeenLastCalledWith("/plugin/native-key-bindings/rollback-preview", restore);
    const commit = { ...restore, expected_revision: preview.revision };
    mocks.post.mockResolvedValueOnce({ data: { changed: 1, noop: false } });
    await api.rollback(commit);
    expect(mocks.post).toHaveBeenLastCalledWith("/plugin/native-key-bindings/rollback", commit);
  });
});
