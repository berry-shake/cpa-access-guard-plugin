import { beforeEach, describe, expect, it, vi } from "vitest";
import { createNativeQuotaResetClient } from "./nativeQuotaReset";

const mocks = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), apiClient: vi.fn(), getSession: vi.fn() }));
vi.mock("./client", () => ({ apiClient: mocks.apiClient, pluginPath: (path: string) => "/plugin" + path }));
vi.mock("../store/session", () => ({ getSession: mocks.getSession }));

const keys = ["sk-synthetic-reset-alpha", "sk-synthetic-reset-beta", "sk-synthetic-reset-unbound"];
const binding = (id: string, enabled = true) => ({
  id, name: id.toUpperCase(), enabled, created_at: "2026-09-27T08:00:00Z", key_preview: "sk-..." + id,
});
const catalog = () => ({
  entries: [
    { key_index: 1, key_preview: "sk-...beta", binding: binding("beta", false) },
    { key_index: 0, key_preview: "sk-...alpha", binding: binding("alpha") },
    { key_index: 2, key_preview: "sk-...unbound" },
  ],
  orphan_bindings: [binding("removed")],
});

beforeEach(() => {
  vi.resetAllMocks();
  mocks.getSession.mockReturnValue({ baseUrl: "http://reset-fixture.invalid", secretKey: "synthetic-management" });
  mocks.apiClient.mockReturnValue({ get: mocks.get, post: mocks.post });
  mocks.get.mockResolvedValue({ data: { "api-keys": keys } });
  mocks.post.mockResolvedValue({ data: catalog() });
});

describe("native quota batch reset API", () => {
  it("maps actual host indices to bindings, includes disabled bindings and excludes unbound and orphan keys", async () => {
    const api = createNativeQuotaResetClient();
    await expect(api.loadBindings()).resolves.toEqual([
      { id: "beta", name: "BETA", keyPreview: "sk-...beta", enabled: false, createdAt: "2026-09-27T08:00:00Z", key: keys[1] },
      { id: "alpha", name: "ALPHA", keyPreview: "sk-...alpha", enabled: true, createdAt: "2026-09-27T08:00:00Z", key: keys[0] },
    ]);
    expect(mocks.get).toHaveBeenCalledTimes(1);
    expect(mocks.get).toHaveBeenCalledWith("/v0/management/api-keys");
    expect(mocks.post).toHaveBeenCalledTimes(1);
    expect(mocks.post).toHaveBeenCalledWith("/plugin/native-key-bindings/catalog", { api_keys: keys });
    expect(mocks.apiClient).toHaveBeenCalledTimes(1);
    for (const [path] of [...mocks.get.mock.calls, ...mocks.post.mock.calls]) {
      for (const key of keys) expect(path).not.toContain(key);
    }
  });

  it("accepts legacy bindings whose creation timestamp is omitted", async () => {
    const data = catalog();
    delete (data.entries[0].binding as { created_at?: string }).created_at;
    mocks.post.mockResolvedValue({ data });
    const rows = await createNativeQuotaResetClient().loadBindings();
    expect(rows[0].createdAt).toBe("");
    expect(rows[0].key).toBe(keys[1]);
  });

  it("sends only explicitly selected binding IDs to the reset endpoint using the captured client", async () => {
    const api = createNativeQuotaResetClient();
    await api.loadBindings();
    const result = { reset: true, ids: ["beta"], count: 1 };
    mocks.post.mockResolvedValueOnce({ data: result });
    await expect(api.reset(["beta"])).resolves.toEqual(result);
    expect(mocks.post).toHaveBeenLastCalledWith("/plugin/native-key-bindings/reset-quota-batch", { ids: ["beta"] });
    expect(mocks.apiClient).toHaveBeenCalledTimes(1);
  });

  it("deduplicates the host key inventory before requesting its catalog", async () => {
    mocks.get.mockResolvedValue({ data: { "api-keys": [keys[0], "  ", " " + keys[0] + " ", keys[1], keys[2]] } });
    const rows = await createNativeQuotaResetClient().loadBindings();
    expect(rows.map((row) => row.key)).toEqual([keys[1], keys[0]]);
    expect(mocks.post).toHaveBeenCalledWith("/plugin/native-key-bindings/catalog", { api_keys: keys });
  });

  it.each([{}, { "api-keys": "invalid" }, { "api-keys": [keys[0], null] }])(
    "rejects an incomplete host key inventory before requesting a catalog: %j", async (data) => {
      mocks.get.mockResolvedValue({ data });
      await expect(createNativeQuotaResetClient().loadBindings()).rejects.toThrow("quota_reset_inventory_unavailable");
      expect(mocks.post).not.toHaveBeenCalled();
    },
  );

  it.each([
    { entries: [] },
    { entries: [{ key_index: 0 }, { key_index: 0 }, { key_index: 2 }] },
    { entries: [{ key_index: 0 }, { key_index: 1 }, { key_index: 9 }] },
    { entries: [{ key_index: 0, binding: binding("same") }, { key_index: 1, binding: binding("same") }, { key_index: 2 }] },
  ])("rejects catalog responses that cannot identify every host key safely: %j", async (data) => {
    mocks.post.mockResolvedValue({ data });
    await expect(createNativeQuotaResetClient().loadBindings()).rejects.toThrow("quota_reset_inventory_unavailable");
    expect(mocks.post).toHaveBeenCalledTimes(1);
  });

  it("blocks both reads and resets after the management session changes", async () => {
    const api = createNativeQuotaResetClient();
    mocks.getSession.mockReturnValue({ baseUrl: "http://other-fixture.invalid", secretKey: "other-synthetic-management" });
    await expect(api.loadBindings()).rejects.toThrow("quota_reset_session_changed");
    await expect(api.reset(["alpha"])).rejects.toThrow("quota_reset_session_changed");
    expect(mocks.get).not.toHaveBeenCalled();
    expect(mocks.post).not.toHaveBeenCalled();
  });

  it("does not forward host keys after a session change during the inventory request", async () => {
    const api = createNativeQuotaResetClient();
    mocks.get.mockImplementationOnce(async () => {
      mocks.getSession.mockReturnValue(null);
      return { data: { "api-keys": keys } };
    });
    await expect(api.loadBindings()).rejects.toThrow("quota_reset_session_changed");
    expect(mocks.post).not.toHaveBeenCalled();
  });

  it("rejects a catalog result from an expired session", async () => {
    const api = createNativeQuotaResetClient();
    mocks.post.mockImplementationOnce(async () => {
      mocks.getSession.mockReturnValue(null);
      return { data: catalog() };
    });
    await expect(api.loadBindings()).rejects.toThrow("quota_reset_session_changed");
  });

  it("does not retry a failed reset automatically", async () => {
    const cause = { response: { status: 500, data: { error: "persistence_failed" } } };
    mocks.post.mockRejectedValueOnce(cause);
    await expect(createNativeQuotaResetClient().reset(["alpha"])).rejects.toBe(cause);
    expect(mocks.post).toHaveBeenCalledTimes(1);
  });
});
