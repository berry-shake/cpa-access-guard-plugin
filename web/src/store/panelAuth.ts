// Reuse the CPA or CPAMP panel's saved management key when this web UI is
// loaded as a same-origin iframe inside that panel.
//
// The panel stores its auth state (apiBase +, only when "remember password"
// is checked, managementKey) in localStorage under key "cli-proxy-auth", run
// through a reversible XOR+base64 obfuscation (see the panel's
// src/utils/encryption.ts). Because the panel and this plugin resource are
// same-origin (both served by CPA), they share the SAME localStorage and the
// SAME obfuscation key, so we can decode the panel's stored blob here and
// skip a second login. Legacy v1 includes host + userAgent; CPAMP v2 uses
// host only, with its own versioned salt.
//
// SECURITY: This only reads the panel's already-persisted key; it never writes
// the key to storage itself. The plugin's own "key in memory only" rule stands.
// Reads stay within this frame's origin-scoped storage; credentials are never
// read from or sent to a parent window. We also require an embedded page.

const ENC_PREFIX_V1 = "enc::v1::";
const ENC_PREFIX_V2 = "enc::v2::";
const SECRET_SALT = "cli-proxy-api-webui::secure-storage";
const STORAGE_KEY = "cli-proxy-auth";

export interface PanelAuth {
  apiBase: string;
  managementKey: string;
}

let cachedKeyBytes: Uint8Array | null = null;
let cachedV2KeyBytes: Uint8Array | null = null;

function encodeText(text: string): Uint8Array {
  return new TextEncoder().encode(text);
}

function decodeText(bytes: Uint8Array): string {
  return new TextDecoder().decode(bytes);
}

// Mirrors the panel's getKeyBytes(): `${SECRET_SALT}|${host}|${userAgent}`.
function getKeyBytes(): Uint8Array {
  if (cachedKeyBytes) return cachedKeyBytes;
  try {
    cachedKeyBytes = encodeText(`${SECRET_SALT}|${window.location.host}|${navigator.userAgent}`);
  } catch {
    cachedKeyBytes = encodeText(SECRET_SALT);
  }
  return cachedKeyBytes;
}

// Matches CPAMP v2: `${SECRET_SALT}|v2|${host}`, independent of userAgent.
function getV2KeyBytes(): Uint8Array {
  if (cachedV2KeyBytes) return cachedV2KeyBytes;
  try {
    cachedV2KeyBytes = encodeText(`${SECRET_SALT}|v2|${window.location.host}`);
  } catch {
    cachedV2KeyBytes = encodeText(`${SECRET_SALT}|v2`);
  }
  return cachedV2KeyBytes;
}

// Exposed for tests: clear the cached key so a changed host/userAgent is used.
export function _resetKeyCache(): void {
  cachedKeyBytes = null;
  cachedV2KeyBytes = null;
}

function xorBytes(data: Uint8Array, keyBytes: Uint8Array): Uint8Array {
  const result = new Uint8Array(data.length);
  for (let i = 0; i < data.length; i++) {
    result[i] = data[i] ^ keyBytes[i % keyBytes.length];
  }
  return result;
}

function toBase64(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
  return btoa(binary);
}

function fromBase64(base64: string): Uint8Array {
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

// Legacy v1 encoder retained for compatibility fixtures; never persists data.
export function obfuscateData(value: string): string {
  if (!value) return value;
  try {
    return `${ENC_PREFIX_V1}${toBase64(xorBytes(encodeText(value), getKeyBytes()))}`;
  } catch {
    return value;
  }
}

// Decode both panel formats without migrating or rewriting the saved value.
export function deobfuscateData(payload: string): string {
  if (!payload) return payload;
  const version = payload.startsWith(ENC_PREFIX_V2) ? 2 : payload.startsWith(ENC_PREFIX_V1) ? 1 : 0;
  if (!version) return payload;
  try {
    const prefix = version === 2 ? ENC_PREFIX_V2 : ENC_PREFIX_V1;
    const keyBytes = version === 2 ? getV2KeyBytes() : getKeyBytes();
    const encrypted = fromBase64(payload.slice(prefix.length));
    return decodeText(xorBytes(encrypted, keyBytes));
  } catch {
    return payload;
  }
}

// True when this page is running inside an iframe (the panel embeds us here).
export function isEmbedded(): boolean {
  try {
    return window.self !== window.top;
  } catch {
    // Cross-origin access to window.top throws → treat as not-embedded/safe.
    return false;
  }
}

// The panel's zustand persist envelope: { state: {...}, version: number }.
// managementKey is present only when the user checked "remember password".
function extractAuth(parsed: unknown): PanelAuth | null {
  if (!parsed || typeof parsed !== "object") return null;
  const root = parsed as Record<string, unknown>;
  // The persisted shape is { state, version }; fall back to the object itself
  // in case the envelope is absent (older/different storage layouts).
  const state =
    (root.state as Record<string, unknown> | undefined) ?? (root as Record<string, unknown>);
  const apiBase = typeof state.apiBase === "string" ? state.apiBase : "";
  const managementKey = typeof state.managementKey === "string" ? state.managementKey : "";
  if (!apiBase || !managementKey) return null;
  return { apiBase, managementKey };
}

// Read the panel's saved auth from shared same-origin localStorage.
// Returns null when: not embedded, no stored value, decode failure, or the
// user did not choose "remember password" (no managementKey persisted).
export function readPanelAuth(): PanelAuth | null {
  if (!isEmbedded()) return null;
  let raw: string | null;
  try {
    raw = localStorage.getItem(STORAGE_KEY);
  } catch {
    return null;
  }
  if (!raw) return null;
  let decoded: string;
  try {
    decoded = deobfuscateData(raw);
  } catch {
    return null;
  }
  try {
    return extractAuth(JSON.parse(decoded));
  } catch {
    return null;
  }
}
