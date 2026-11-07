import { sha256Hex } from "../sha256";
import type { AxiosInstance } from "axios";
import { apiClient } from "./client";
import type { NativeCredentialOption } from "../types";

type LooseObject = Record<string, unknown>;

interface ChannelSpec {
  endpoint: string;
  root: string;
  idKind: string;
  provider: string;
  label: string;
  extendedIdentity: boolean;
}

const CHANNELS: ChannelSpec[] = [
  {
    endpoint: "gemini-api-key",
    root: "gemini-api-key",
    idKind: "gemini:apikey",
    provider: "gemini",
    label: "Gemini",
    extendedIdentity: true,
  },
  {
    endpoint: "interactions-api-key",
    root: "interactions-api-key",
    idKind: "gemini-interactions:apikey",
    provider: "gemini-interactions",
    label: "Interactions API",
    extendedIdentity: true,
  },
  {
    endpoint: "claude-api-key",
    root: "claude-api-key",
    idKind: "claude:apikey",
    provider: "claude",
    label: "Claude",
    extendedIdentity: true,
  },
  {
    endpoint: "codex-api-key",
    root: "codex-api-key",
    idKind: "codex:apikey",
    provider: "codex",
    label: "Codex",
    extendedIdentity: true,
  },
  {
    endpoint: "xai-api-key",
    root: "xai-api-key",
    idKind: "xai:apikey",
    provider: "xai",
    label: "xAI",
    extendedIdentity: true,
  },
  {
    endpoint: "vertex-api-key",
    root: "vertex-api-key",
    idKind: "vertex:apikey",
    provider: "vertex",
    label: "Vertex",
    extendedIdentity: false,
  },
];

interface PendingCredential extends NativeCredentialOption {
  fallbackModels: string[];
}

function asObject(value: unknown): LooseObject {
  return value && typeof value === "object" && !Array.isArray(value)
    ? value as LooseObject
    : {};
}

function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function text(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function normalizePrefix(value: unknown): string {
  return text(value).replace(/^\/+|\/+$/g, "");
}

function normalizedHeaders(value: unknown): Record<string, string> {
  const raw = asObject(value);
  const out: Record<string, string> = {};
  for (const [rawKey, rawValue] of Object.entries(raw)) {
    const key = rawKey.trim();
    const valueText = text(rawValue);
    if (key && valueText) out[key] = valueText;
  }
  return out;
}

// This mirrors config.FormatSortedHeaders. HTTP header names are ASCII, so
// JavaScript's default code-unit order is the same as Go's byte order here.
function sortedHeadersIdentity(value: unknown): string {
  const headers = normalizedHeaders(value);
  return Object.keys(headers)
    .sort()
    .map((key) => key + "\0" + headers[key] + "\0")
    .join("");
}

// CPA's StableIDGenerator hashes kind + NUL-delimited, trimmed identity parts
// and appends a deterministic collision ordinal. Only the derived ID leaves
// this adapter; plaintext configuration values never enter component state.
class RuntimeIDGenerator {
  private readonly counters = new Map<string, number>();

  async next(kind: string, parts: string[]): Promise<string> {
    let seed = kind;
    for (const part of parts) seed += "\0" + part.trim();
    const short = (await sha256Hex(seed)).slice(0, 12);
    const base = kind + ":" + short;
    const collision = this.counters.get(base) ?? 0;
    this.counters.set(base, collision + 1);
    return collision === 0 ? base : base + "-" + collision;
  }
}

function fallbackModelIDs(entry: LooseObject, prefix: string): string[] {
  const unique = new Map<string, string>();
  for (const rawModel of asArray(entry["models"])) {
    const model = asObject(rawModel);
    const base = text(model["alias"]) || text(model["name"]) || text(model["id"]);
    if (!base) continue;
    if (!unique.has(base.toLowerCase())) unique.set(base.toLowerCase(), base);
    if (prefix) {
      const prefixed = prefix + "/" + base;
      if (!unique.has(prefixed.toLowerCase())) unique.set(prefixed.toLowerCase(), prefixed);
    }
  }
  return Array.from(unique.values());
}

function runtimeModelIDs(payload: unknown): string[] {
  const root = asObject(payload);
  const unique = new Map<string, string>();
  for (const rawModel of asArray(root["models"])) {
    const model = asObject(rawModel);
    const id = text(model["id"]) || text(model["model"]) || text(model["name"]);
    if (id && !unique.has(id.toLowerCase())) unique.set(id.toLowerCase(), id);
  }
  return Array.from(unique.values()).sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
}

function internalOpenAIProvider(name: string): string {
  const normalized = name.trim().toLowerCase();
  if (!normalized || normalized === "openai-compatibility" || normalized.startsWith("openai-compatible-")) {
    return normalized || "openai-compatibility";
  }
  return "openai-compatible-" + normalized;
}

function isAuthFailure(reason: unknown): boolean {
  const status = (reason as { response?: { status?: number } } | null)?.response?.status;
  return status === 401 || status === 403;
}

async function optionalGet(client: AxiosInstance, path: string, root: string): Promise<unknown> {
  let payload: unknown;
  try {
    const { data } = await client.get(path);
    payload = data;
  } catch (reason) {
    const status = (reason as { response?: { status?: number } } | null)?.response?.status;
    // Unsupported endpoints are absent providers. A failed inventory must not
    // look like an empty provider list in either the editor or the card catalog.
    if (status === 404) return undefined;
    throw reason;
  }
  const object = asObject(payload);
  const entries = object[root];
  if (!Object.prototype.hasOwnProperty.call(object, root)
    || (entries !== null && (!Array.isArray(entries)
      || entries.some((entry) => !entry || typeof entry !== "object" || Array.isArray(entry))))) {
    throw new Error("ai_provider_inventory_unavailable");
  }
  return payload;
}

async function addChannelCredentials(
  out: PendingCredential[],
  generator: RuntimeIDGenerator,
  spec: ChannelSpec,
  payload: unknown,
): Promise<void> {
  const list = asArray(asObject(payload)[spec.root]);
  for (let index = 0; index < list.length; index++) {
    const entry = asObject(list[index]);
    const apiKey = text(entry["api-key"]);
    const baseURL = text(entry["base-url"]);
    if (spec.extendedIdentity && !apiKey && !baseURL) continue;
    const proxyURL = text(entry["proxy-url"]);
    const prefix = normalizePrefix(entry["prefix"]);
    const parts = spec.extendedIdentity
      ? [apiKey, baseURL, proxyURL, prefix, sortedHeadersIdentity(entry["headers"])]
      : [apiKey, baseURL, proxyURL];
    const id = await generator.next(spec.idKind, parts);
    out.push({
      id,
      provider: spec.provider,
      name: spec.label + " #" + (index + 1),
      label: spec.label,
      status: "configured",
      disabled: false,
      unavailable: false,
      source: "ai_provider",
      authIndex: text(entry["auth-index"]) || undefined,
      configIndex: index,
      models: [],
      fallbackModels: fallbackModelIDs(entry, prefix),
    });
  }
}

async function addOpenAICompatibleCredentials(
  out: PendingCredential[],
  generator: RuntimeIDGenerator,
  payload: unknown,
): Promise<void> {
  const list = asArray(asObject(payload)["openai-compatibility"]);
  for (let configIndex = 0; configIndex < list.length; configIndex++) {
    const compat = asObject(list[configIndex]);
    if (compat["disabled"] === true) continue;
    const displayName = text(compat["name"]) || "OpenAI Compatibility";
    const providerName = text(compat["name"]).toLowerCase() || "openai-compatibility";
    const provider = internalOpenAIProvider(providerName);
    const idKind = "openai-compatibility:" + providerName;
    const baseURL = text(compat["base-url"]);
    const prefix = text(compat["prefix"]);
    const fallbackModels = fallbackModelIDs(compat, prefix);
    const rawKeyEntries = compat["api-key-entries"];
    if (rawKeyEntries != null && (!Array.isArray(rawKeyEntries)
      || rawKeyEntries.some((entry) => !entry || typeof entry !== "object" || Array.isArray(entry)))) {
      throw new Error("ai_provider_inventory_unavailable");
    }
    const keyEntries = asArray(rawKeyEntries);
    if (keyEntries.length === 0) {
      out.push({
        id: await generator.next(idKind, [baseURL]),
        provider,
        name: displayName + " #1",
        label: displayName,
        status: "configured",
        disabled: false,
        unavailable: false,
        source: "ai_provider",
        authIndex: text(compat["auth-index"]) || undefined,
        configIndex,
        models: [],
        fallbackModels,
      });
      continue;
    }
    for (let keyIndex = 0; keyIndex < keyEntries.length; keyIndex++) {
      const keyEntry = asObject(keyEntries[keyIndex]);
      out.push({
        id: await generator.next(idKind, [
          text(keyEntry["api-key"]),
          baseURL,
          text(keyEntry["proxy-url"]),
        ]),
        provider,
        name: displayName + " #" + (keyIndex + 1),
        label: displayName,
        status: "configured",
        disabled: false,
        unavailable: false,
        source: "ai_provider",
        authIndex: text(keyEntry["auth-index"]) || undefined,
        configIndex,
        models: [],
        fallbackModels,
      });
    }
  }
}

async function populateRuntimeModels(client: AxiosInstance, credentials: PendingCredential[]): Promise<void> {
  let cursor = 0;
  const workers = Array.from({ length: Math.min(6, credentials.length) }, async () => {
    for (;;) {
      const index = cursor++;
      if (index >= credentials.length) return;
      const credential = credentials[index];
      try {
        const { data } = await client.get("/v0/management/auth-files/models", {
          params: { name: credential.id },
        });
        const models = runtimeModelIDs(data);
        credential.models = models.length > 0 ? models : credential.fallbackModels;
        credential.identityVerified = models.length > 0;
      } catch (reason) {
        if (isAuthFailure(reason)) throw reason;
        credential.models = credential.fallbackModels;
        credential.identityVerified = false;
      }
    }
  });
  await Promise.all(workers);
}

async function loadAIProviderCredentials(client: AxiosInstance, includeModels: boolean): Promise<NativeCredentialOption[]> {
  const sources = [...CHANNELS, { endpoint: "openai-compatibility", root: "openai-compatibility" }];
  const payloads = await Promise.all(sources.map((spec) => optionalGet(client, "/v0/management/" + spec.endpoint, spec.root)));
  const generator = new RuntimeIDGenerator();
  const pending: PendingCredential[] = [];
  for (let index = 0; index < CHANNELS.length; index++) {
    await addChannelCredentials(pending, generator, CHANNELS[index], payloads[index]);
  }
  await addOpenAICompatibleCredentials(pending, generator, payloads[CHANNELS.length]);
  if (includeModels) {
    await populateRuntimeModels(client, pending);
    return pending.map(({ fallbackModels: _discarded, ...credential }) => credential);
  }
  return pending.map(({ fallbackModels: _discarded, models: _models, ...credential }) => credential);
}

const inFlight = new WeakMap<AxiosInstance, Map<boolean, Promise<NativeCredentialOption[]>>>();

// Concurrent loads on the same authenticated client share work. A fresh login
// or host uses a different client and must never inherit another host's data.
export function fetchAIProviderCredentials(client: AxiosInstance = apiClient(), includeModels = true): Promise<NativeCredentialOption[]> {
  let requests = inFlight.get(client);
  if (!requests) {
    requests = new Map();
    inFlight.set(client, requests);
  }
  const current = requests.get(includeModels);
  if (current) return current;
  const request = loadAIProviderCredentials(client, includeModels);
  requests.set(includeModels, request);
  const clear = () => {
    if (requests.get(includeModels) === request) requests.delete(includeModels);
  };
  void request.then(clear, clear);
  return request;
}

export async function deriveRuntimeAuthIDForTest(kind: string, parts: string[]): Promise<string> {
  return new RuntimeIDGenerator().next(kind, parts);
}
