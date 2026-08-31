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

const SHA256_ROUND_CONSTANTS = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

function rotateRight(value: number, bits: number): number {
  return (value >>> bits) | (value << (32 - bits));
}

// A small dependency-free SHA-256 keeps credential discovery working when CPA
// is opened over a non-secure LAN HTTP origin where Web Crypto is unavailable.
function sha256Hex(value: string): string {
  const input = new TextEncoder().encode(value);
  const paddedLength = Math.ceil((input.length + 9) / 64) * 64;
  const padded = new Uint8Array(paddedLength);
  padded.set(input);
  padded[input.length] = 0x80;
  const bitLength = input.length * 8;
  const view = new DataView(padded.buffer);
  view.setUint32(paddedLength - 8, Math.floor(bitLength / 0x1_0000_0000), false);
  view.setUint32(paddedLength - 4, bitLength >>> 0, false);

  let h0 = 0x6a09e667;
  let h1 = 0xbb67ae85;
  let h2 = 0x3c6ef372;
  let h3 = 0xa54ff53a;
  let h4 = 0x510e527f;
  let h5 = 0x9b05688c;
  let h6 = 0x1f83d9ab;
  let h7 = 0x5be0cd19;
  const words = new Uint32Array(64);

  for (let offset = 0; offset < paddedLength; offset += 64) {
    for (let i = 0; i < 16; i++) words[i] = view.getUint32(offset + i * 4, false);
    for (let i = 16; i < 64; i++) {
      const x = words[i - 15];
      const y = words[i - 2];
      const s0 = rotateRight(x, 7) ^ rotateRight(x, 18) ^ (x >>> 3);
      const s1 = rotateRight(y, 17) ^ rotateRight(y, 19) ^ (y >>> 10);
      words[i] = (words[i - 16] + s0 + words[i - 7] + s1) >>> 0;
    }

    let a = h0;
    let b = h1;
    let c = h2;
    let d = h3;
    let e = h4;
    let f = h5;
    let g = h6;
    let h = h7;
    for (let i = 0; i < 64; i++) {
      const sum1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25);
      const choose = (e & f) ^ (~e & g);
      const temp1 = (h + sum1 + choose + SHA256_ROUND_CONSTANTS[i] + words[i]) >>> 0;
      const sum0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22);
      const majority = (a & b) ^ (a & c) ^ (b & c);
      const temp2 = (sum0 + majority) >>> 0;
      h = g;
      g = f;
      f = e;
      e = (d + temp1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (temp1 + temp2) >>> 0;
    }
    h0 = (h0 + a) >>> 0;
    h1 = (h1 + b) >>> 0;
    h2 = (h2 + c) >>> 0;
    h3 = (h3 + d) >>> 0;
    h4 = (h4 + e) >>> 0;
    h5 = (h5 + f) >>> 0;
    h6 = (h6 + g) >>> 0;
    h7 = (h7 + h) >>> 0;
  }

  return [h0, h1, h2, h3, h4, h5, h6, h7]
    .map((word) => word.toString(16).padStart(8, "0"))
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

async function optionalGet(client: AxiosInstance, path: string): Promise<unknown> {
  try {
    const { data } = await client.get(path);
    return data;
  } catch (reason) {
    if (isAuthFailure(reason)) throw reason;
    return undefined;
  }
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
    const keyEntries = asArray(compat["api-key-entries"]);
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

async function loadAIProviderCredentials(client: AxiosInstance): Promise<NativeCredentialOption[]> {
  const paths = CHANNELS.map((spec) => "/v0/management/" + spec.endpoint);
  paths.push("/v0/management/openai-compatibility");
  const payloads = await Promise.all(paths.map((path) => optionalGet(client, path)));
  const generator = new RuntimeIDGenerator();
  const pending: PendingCredential[] = [];
  for (let index = 0; index < CHANNELS.length; index++) {
    await addChannelCredentials(pending, generator, CHANNELS[index], payloads[index]);
  }
  await addOpenAICompatibleCredentials(pending, generator, payloads[CHANNELS.length]);
  await populateRuntimeModels(client, pending);
  return pending.map(({ fallbackModels: _discarded, ...credential }) => credential);
}

let inFlight: Promise<NativeCredentialOption[]> | undefined;

// Concurrent credential/model pickers share one in-flight load, but completed
// results are not retained across logins or refreshes.
export function fetchAIProviderCredentials(client: AxiosInstance = apiClient()): Promise<NativeCredentialOption[]> {
  if (inFlight) return inFlight;
  const request = loadAIProviderCredentials(client);
  inFlight = request;
  const clear = () => {
    if (inFlight === request) inFlight = undefined;
  };
  void request.then(clear, clear);
  return request;
}

export async function deriveRuntimeAuthIDForTest(kind: string, parts: string[]): Promise<string> {
  return new RuntimeIDGenerator().next(kind, parts);
}
