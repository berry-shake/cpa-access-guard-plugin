import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}));

vi.mock("./client", () => ({
  apiClient: () => mocks,
  pluginPath: (suffix: string) => "/plugin" + suffix,
}));

import { fetchCatalog } from "./models";

interface RequestOptions {
  params?: { name?: string };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("fetchCatalog auth-file descriptors", () => {
  it("uses the Scheduler auth ID and forwards only provenance-safe attributes", async () => {
    const files = [
      {
        id: "tenant-a/codex-runtime-id.json",
        name: "codex-display.json",
        provider: "CODEX",
        id_token: { plan_type: " Team " },
        tier: "must-not-be-copied-for-codex",
        path: "/auth/tenant-a/codex-display.json",
        weight: 3,
        note: "metadata-fallback",
        priority: 7,
        websockets: false,
        email: "must-not-forward@example.com",
      },
      {
        id: "ag/runtime-auth-id.json",
        name: "antigravity-display.json",
        provider: "antigravity",
        id_token: { plan_type: "must-not-be-copied-for-antigravity" },
        tier: " Pro ",
        path: "/auth/ag/antigravity-display.json",
        weight: "5",
        note: "metadata-fallback",
        priority: 9,
        websockets: true,
      },
    ];

    mocks.get.mockImplementation((url: string, options?: RequestOptions) => {
      if (url === "/v0/management/auth-files") {
        return Promise.resolve({ data: { files } });
      }
      if (url === "/v0/management/auth-files/models") {
        const models = options?.params?.name === files[0].id
          ? [{ id: "gpt-5.4" }]
          : options?.params?.name === files[1].id
            ? [{ id: "claude-sonnet" }]
            : [];
        return Promise.resolve({ data: { models } });
      }
      return Promise.resolve({ data: {} });
    });

    const catalogEntries = [
      {
        provider: "codex",
        group: "classify:tenant-a",
        models: ["gpt-5.4"],
      },
      {
        provider: "antigravity",
        group: "pro",
        models: ["claude-sonnet"],
      },
    ];
    let resolveCatalog!: (value: { data: { entries: typeof catalogEntries } }) => void;
    const catalogResponse = new Promise<{ data: { entries: typeof catalogEntries } }>(
      (resolve) => {
        resolveCatalog = resolve;
      },
    );
    mocks.post.mockReturnValue(catalogResponse);

    let settled = false;
    const pending = fetchCatalog().finally(() => {
      settled = true;
    });
    await vi.waitFor(() => expect(mocks.post).toHaveBeenCalledTimes(1));
    await Promise.resolve();
    const settledBeforeCatalogResponse = settled;
    resolveCatalog({ data: { entries: catalogEntries } });

    await expect(pending).resolves.toEqual([
      { provider: "antigravity", group: "pro", model: "claude-sonnet" },
      { provider: "codex", group: "classify:tenant-a", model: "gpt-5.4" },
    ]);
    expect(settledBeforeCatalogResponse).toBe(false);

    expect(mocks.get).toHaveBeenCalledWith(
      "/v0/management/auth-files/models",
      { params: { name: files[0].id } },
    );
    expect(mocks.get).toHaveBeenCalledWith(
      "/v0/management/auth-files/models",
      { params: { name: files[1].id } },
    );
    expect(mocks.get).not.toHaveBeenCalledWith(
      "/v0/management/auth-files/models",
      { params: { name: files[0].name } },
    );

    expect(mocks.post).toHaveBeenCalledWith("/plugin/catalog", {
      credentials: [
        {
          id: files[0].id,
          provider: "codex",
          attributes: {
            plan_type: "team",
            path: "/auth/tenant-a/codex-display.json",
            weight: "3",
          },
          models: ["gpt-5.4"],
        },
        {
          id: files[1].id,
          provider: "antigravity",
          attributes: {
            tier: "pro",
            path: "/auth/ag/antigravity-display.json",
            weight: "5",
          },
          models: ["claude-sonnet"],
        },
      ],
    });
    const posted = mocks.post.mock.calls[0][1] as {
      credentials: Array<{ attributes?: Record<string, string> }>;
    };
    for (const credential of posted.credentials) {
      expect(credential.attributes).not.toHaveProperty("note");
      expect(credential.attributes).not.toHaveProperty("priority");
      expect(credential.attributes).not.toHaveProperty("websockets");
    }
    expect(posted.credentials[0].attributes).not.toHaveProperty("tier");
    expect(posted.credentials[1].attributes).not.toHaveProperty("plan_type");
  });

  it("keeps Antigravity tier grouping in the legacy fallback", async () => {
    mocks.get.mockImplementation((url: string, options?: RequestOptions) => {
      if (url === "/v0/management/auth-files") {
        return Promise.resolve({
          data: {
            files: [
              {
                id: "ag/runtime-id.json",
                name: "ag-display.json",
                provider: "antigravity",
                tier: " Pro ",
              },
            ],
          },
        });
      }
      if (url === "/v0/management/auth-files/models") {
        expect(options?.params?.name).toBe("ag/runtime-id.json");
        return Promise.resolve({ data: { models: [{ id: "claude-sonnet" }] } });
      }
      return Promise.resolve({ data: {} });
    });
    mocks.post.mockRejectedValue(new Error("catalog route unavailable"));

    await expect(fetchCatalog()).resolves.toEqual([
      { provider: "antigravity", group: "pro", model: "claude-sonnet" },
    ]);
  });
});
