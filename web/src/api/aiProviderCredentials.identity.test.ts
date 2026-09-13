import type { AxiosInstance } from "axios";
import { describe, expect, it, vi } from "vitest";
import { fetchAIProviderCredentials } from "./aiProviderCredentials";

const managementPaths = [
  "gemini-api-key",
  "interactions-api-key",
  "claude-api-key",
  "codex-api-key",
  "xai-api-key",
  "vertex-api-key",
  "openai-compatibility",
].map((endpoint) => "/v0/management/" + endpoint);
const modelsPath = "/v0/management/auth-files/models";

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function clientWith(get: ReturnType<typeof vi.fn>): AxiosInstance {
  return { get } as unknown as AxiosInstance;
}

function credentialResponse(url: string, clientName: string, count = 1) {
  if (url === modelsPath) {
    return { data: { models: [{ id: "runtime-spark" }] } };
  }
  const root = url.slice("/v0/management/".length);
  return {
    data: {
      [root]: root === "codex-api-key"
        ? Array.from({ length: count }, (_, index) => ({
          "api-key": clientName + "-secret-" + index,
          "base-url": "https://" + clientName + ".invalid/v1",
          "auth-index": clientName + "-index-" + index,
          models: [{ alias: "fallback-spark" }],
        }))
        : [],
    },
  };
}

function failureWithStatus(status: number) {
  return Object.assign(new Error("management request failed"), { response: { status } });
}

describe("AI provider identity-only inventory", () => {
  it("uses exactly seven management requests for 240 credentials without model requests", async () => {
    const get = vi.fn((url: string) => Promise.resolve(credentialResponse(url, "inventory", 240)));

    const result = await fetchAIProviderCredentials(clientWith(get), false);

    expect(get.mock.calls.map(([url]) => url).sort()).toEqual([...managementPaths].sort());
    expect(result).toHaveLength(240);
    expect(new Set(result.map((credential) => credential.id)).size).toBe(240);
    expect(result.every((credential) => credential.provider === "codex")).toBe(true);
    expect(result.every((credential) => !("models" in credential))).toBe(true);
    expect(result.every((credential) => !("identityVerified" in credential))).toBe(true);
    expect(result[0].authIndex).toBe("inventory-index-0");
    expect(JSON.stringify(result)).not.toMatch(/inventory-secret|inventory\.invalid|fallback-spark|fallbackModels/);
  });

  it("keeps runtime model loading enabled by default", async () => {
    const get = vi.fn((url: string) => Promise.resolve(credentialResponse(url, "default")));

    const result = await fetchAIProviderCredentials(clientWith(get));

    expect(get).toHaveBeenCalledTimes(8);
    expect(get).toHaveBeenCalledWith(modelsPath, { params: { name: result[0].id } });
    expect(result[0].models).toEqual(["runtime-spark"]);
    expect(result[0].identityVerified).toBe(true);
  });

  it.each(managementPaths)("treats unsupported endpoint %s as an absent provider", async (missingPath) => {
    const get = vi.fn((url: string) => url === missingPath
      ? Promise.reject(failureWithStatus(404))
      : Promise.resolve(credentialResponse(url, "supported")));

    const result = await fetchAIProviderCredentials(clientWith(get), false);

    expect(get).toHaveBeenCalledTimes(7);
    expect(result).toHaveLength(missingPath.endsWith("/codex-api-key") ? 0 : 1);
  });

  it.each([429, 500, 502])("rejects HTTP %i instead of reporting an incomplete identity inventory", async (status) => {
    const failure = failureWithStatus(status);
    const get = vi.fn((url: string) => url.endsWith("/claude-api-key")
      ? Promise.reject(failure)
      : Promise.resolve(credentialResponse(url, "incomplete")));

    await expect(fetchAIProviderCredentials(clientWith(get), false)).rejects.toBe(failure);
    expect(get).toHaveBeenCalledTimes(7);
  });

  it("rejects transport failures in identity-only mode", async () => {
    const failure = new Error("connection reset");
    const get = vi.fn((url: string) => url.endsWith("/xai-api-key")
      ? Promise.reject(failure)
      : Promise.resolve(credentialResponse(url, "offline")));

    await expect(fetchAIProviderCredentials(clientWith(get), false)).rejects.toBe(failure);
  });

  it.each([401, 403])("preserves HTTP %i authentication failures in both modes", async (status) => {
    const failure = failureWithStatus(status);
    const get = vi.fn(() => Promise.reject(failure));
    const client = clientWith(get);

    await expect(fetchAIProviderCredentials(client, false)).rejects.toBe(failure);
    await expect(fetchAIProviderCredentials(client, true)).rejects.toBe(failure);
  });
});

describe("AI provider in-flight client and mode isolation", () => {
  it.each([false, true])("shares the exact in-flight promise for one client with includeModels=%s", async (includeModels) => {
    const gate = deferred<void>();
    const get = vi.fn(async (url: string) => {
      await gate.promise;
      return credentialResponse(url, "shared");
    });
    const client = clientWith(get);

    const first = fetchAIProviderCredentials(client, includeModels);
    const second = fetchAIProviderCredentials(client, includeModels);
    const third = fetchAIProviderCredentials(client, includeModels);

    expect(second).toBe(first);
    expect(third).toBe(first);
    expect(get).toHaveBeenCalledTimes(7);
    gate.resolve();
    const results = await Promise.all([first, second, third]);
    expect(results[1]).toBe(results[0]);
    expect(results[2]).toBe(results[0]);
    expect(get).toHaveBeenCalledTimes(includeModels ? 8 : 7);
  });

  it("does not share credentials across clients while both loads are pending", async () => {
    const gateA = deferred<void>();
    const gateB = deferred<void>();
    const getA = vi.fn(async (url: string) => {
      await gateA.promise;
      return credentialResponse(url, "host-a");
    });
    const getB = vi.fn(async (url: string) => {
      await gateB.promise;
      return credentialResponse(url, "host-b");
    });

    const first = fetchAIProviderCredentials(clientWith(getA), false);
    const second = fetchAIProviderCredentials(clientWith(getB), false);

    expect(second).not.toBe(first);
    expect(getA).toHaveBeenCalledTimes(7);
    expect(getB).toHaveBeenCalledTimes(7);
    gateB.resolve();
    const resultB = await second;
    expect(resultB.map((credential) => credential.authIndex)).toEqual(["host-b-index-0"]);
    gateA.resolve();
    const resultA = await first;
    expect(resultA.map((credential) => credential.authIndex)).toEqual(["host-a-index-0"]);
    expect(resultA[0].id).not.toBe(resultB[0].id);
  });

  it("keeps identity-only and full loads separate for the same client", async () => {
    const gate = deferred<void>();
    const get = vi.fn(async (url: string) => {
      await gate.promise;
      return credentialResponse(url, "modes");
    });
    const client = clientWith(get);

    const identities = fetchAIProviderCredentials(client, false);
    const full = fetchAIProviderCredentials(client, true);

    expect(full).not.toBe(identities);
    expect(fetchAIProviderCredentials(client, false)).toBe(identities);
    expect(fetchAIProviderCredentials(client)).toBe(full);
    expect(get).toHaveBeenCalledTimes(14);
    gate.resolve();
    const [identityResult, fullResult] = await Promise.all([identities, full]);
    expect(identityResult[0].id).toBe(fullResult[0].id);
    expect(identityResult[0]).not.toHaveProperty("models");
    expect(fullResult[0].models).toEqual(["runtime-spark"]);
    expect(get.mock.calls.filter(([url]) => url === modelsPath)).toHaveLength(1);
  });

  it.each([false, true])("refreshes completed loads instead of caching inventory with includeModels=%s", async (includeModels) => {
    let revision = "before";
    const get = vi.fn((url: string) => Promise.resolve(credentialResponse(url, revision)));
    const client = clientWith(get);
    const first = fetchAIProviderCredentials(client, includeModels);
    const before = await first;
    revision = "after";

    const second = fetchAIProviderCredentials(client, includeModels);
    const after = await second;

    expect(second).not.toBe(first);
    expect(before[0].authIndex).toBe("before-index-0");
    expect(after[0].authIndex).toBe("after-index-0");
    expect(get).toHaveBeenCalledTimes(includeModels ? 16 : 14);
  });

  it("clears a rejected shared load so the same client can retry", async () => {
    const gate = deferred<void>();
    const failure = failureWithStatus(500);
    let recovered = false;
    const get = vi.fn(async (url: string) => {
      await gate.promise;
      if (!recovered && url.endsWith("/codex-api-key")) throw failure;
      return credentialResponse(url, "recovered");
    });
    const client = clientWith(get);
    const first = fetchAIProviderCredentials(client, false);
    const shared = fetchAIProviderCredentials(client, false);
    expect(shared).toBe(first);
    const rejected = expect(first).rejects.toBe(failure);
    gate.resolve();
    await rejected;
    recovered = true;

    const retry = fetchAIProviderCredentials(client, false);

    expect(retry).not.toBe(first);
    await expect(retry).resolves.toMatchObject([{ authIndex: "recovered-index-0" }]);
    expect(get).toHaveBeenCalledTimes(14);
  });
});
