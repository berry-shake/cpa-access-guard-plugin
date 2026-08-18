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
  fetchNativeKeyBindings,
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
  vi.clearAllMocks();
});

describe("native key binding API", () => {
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
