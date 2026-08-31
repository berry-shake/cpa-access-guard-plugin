import { describe, expect, it, vi } from "vitest";
import type { AxiosInstance } from "axios";
import {
  deriveRuntimeAuthIDForTest,
  fetchAIProviderCredentials,
} from "./aiProviderCredentials";

function clientWith(get: ReturnType<typeof vi.fn>): AxiosInstance {
  return { get } as unknown as AxiosInstance;
}

describe("AI provider credential adapter", () => {
  it("matches CPA StableIDGenerator test vectors", async () => {
    await expect(deriveRuntimeAuthIDForTest("codex:apikey", [
      "demo-key",
      "http://127.0.0.1:9/v1",
      "",
      "",
      "",
    ])).resolves.toBe("codex:apikey:36f5c62aaa48");

    await expect(deriveRuntimeAuthIDForTest("gemini:apikey", [
      "gem-key",
      "https://example.test",
      "http://proxy",
      "team",
      "X-A\0one\0",
    ])).resolves.toBe("gemini:apikey:a7f038ea3d9f");
  });

  it("sanitizes a Codex config credential and loads its runtime models by exact Auth ID", async () => {
    const secret = "demo-key";
    const expectedID = "codex:apikey:36f5c62aaa48";
    const get = vi.fn((url: string, options?: { params?: { name?: string } }) => {
      if (url === "/v0/management/codex-api-key") {
        return Promise.resolve({
          data: {
            "codex-api-key": [{
              "api-key": secret,
              "base-url": "http://127.0.0.1:9/v1",
              "auth-index": "safe-index-12345678",
              models: [{ name: "upstream-luna", alias: "gpt-5.6-luna" }],
            }],
          },
        });
      }
      if (url === "/v0/management/auth-files/models") {
        expect(options?.params?.name).toBe(expectedID);
        return Promise.resolve({ data: { models: [{ id: "gpt-5.6-luna" }] } });
      }
      const root = url.slice("/v0/management/".length);
      return Promise.resolve({ data: { [root]: [] } });
    });

    const result = await fetchAIProviderCredentials(clientWith(get));
    expect(result).toEqual([{
      id: expectedID,
      provider: "codex",
      name: "Codex #1",
      label: "Codex",
      status: "configured",
      disabled: false,
      unavailable: false,
      source: "ai_provider",
      authIndex: "safe-index-12345678",
      configIndex: 0,
      models: ["gpt-5.6-luna"],
      identityVerified: true,
    }]);
    const serialized = JSON.stringify(result);
    expect(serialized).not.toContain(secret);
    expect(serialized).not.toContain("upstream-luna");
  });

  it("enumerates every supported config-provider family and keeps duplicate ID ordinals deterministic", async () => {
    const endpointEntries: Record<string, unknown> = {
      "gemini-api-key": [{ "api-key": "g", models: [{ alias: "gem" }] }],
      "interactions-api-key": [{ "api-key": "i", models: [{ alias: "interact" }] }],
      "claude-api-key": [{ "api-key": "c", models: [{ alias: "claude" }] }],
      "codex-api-key": [
        { "api-key": "same", models: [{ alias: "luna" }] },
        { "api-key": "same", models: [{ alias: "luna" }] },
      ],
      "xai-api-key": [{ "api-key": "x", models: [{ alias: "grok" }] }],
      "vertex-api-key": [{ "api-key": "v", models: [{ alias: "vertex" }] }],
    };
    const get = vi.fn((url: string) => {
      if (url === "/v0/management/openai-compatibility") {
        return Promise.resolve({
          data: {
            "openai-compatibility": [{
              name: "demo",
              "base-url": "https://compat.invalid/v1",
              "api-key-entries": [{ "api-key": "o" }],
              models: [{ alias: "compat" }],
            }],
          },
        });
      }
      if (url === "/v0/management/auth-files/models") {
        return Promise.reject(new Error("runtime catalog unavailable in fixture"));
      }
      const root = url.slice("/v0/management/".length);
      return Promise.resolve({ data: { [root]: endpointEntries[root] ?? [] } });
    });

    const result = await fetchAIProviderCredentials(clientWith(get));
    expect(result.map((entry) => entry.provider)).toEqual([
      "gemini",
      "gemini-interactions",
      "claude",
      "codex",
      "codex",
      "xai",
      "vertex",
      "openai-compatible-demo",
    ]);
    expect(result[4].id).toBe(result[3].id + "-1");
    expect(result.find((entry) => entry.provider === "openai-compatible-demo")?.models).toEqual(["compat"]);
    expect(JSON.stringify(result)).not.toMatch(/\"api-key\"|compat\.invalid|\"same\"/);
  });

  it("propagates management authentication failures", async () => {
    const failure = Object.assign(new Error("unauthorized"), { response: { status: 401 } });
    const get = vi.fn(() => Promise.reject(failure));
    await expect(fetchAIProviderCredentials(clientWith(get))).rejects.toBe(failure);
  });
});
