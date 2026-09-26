// In-memory session only. Never persisted to localStorage.
// A refresh discards this session; an embedded panel may restore its own
// already-persisted credential through bootstrapFromPanel().

import { readPanelAuth } from "./panelAuth";
import type { StatusResponse } from "../types";

interface Session {
  baseUrl: string;
  secretKey: string;
}

let current: Session | null = null;

const listeners = new Set<() => void>();

function normalizeBase(url: string): string {
  let u = url.trim();
  if (u === "") return "";
  u = u.replace(/\/+$/, "");
  if (!/^https?:\/\//i.test(u)) u = "http://" + u;
  return u;
}

export function setSession(baseUrl: string, secretKey: string): Session {
  const session: Session = {
    baseUrl: normalizeBase(baseUrl),
    secretKey: secretKey.trim(),
  };
  current = session;
  emit();
  return session;
}

export function clearSession(): void {
  current = null;
  emit();
}

export function getSession(): Session | null {
  return current;
}

export function isAuthed(): boolean {
  return current !== null && current.secretKey !== "" && current.baseUrl !== "";
}

export function subscribe(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function emit(): void {
  for (const fn of listeners) fn();
}

// Attempt to restore a session from the CPA or CPAMP panel's saved key
// (only available when this UI is loaded as a same-origin iframe inside the
// panel AND the user checked "remember password" there). On success the
// session is set and true is returned; on any failure the session is cleared
// and false is returned so the caller falls back to the login page.
export async function bootstrapFromPanel(): Promise<boolean> {
  const auth = readPanelAuth();
  if (!auth) return false;
  const candidate: Session = {
    baseUrl: normalizeBase(auth.apiBase),
    secretKey: auth.managementKey.trim(),
  };
  try {
    // Do not publish an authenticated state until verification succeeds.
    // Otherwise a rejection toggles Shell's auth effect and retries forever.
    await verifyCandidateSession(fetch, candidate);
    setSession(candidate.baseUrl, candidate.secretKey);
    return true;
  } catch {
    clearSession();
    return false;
  }
}

// Probe helper: confirm the key works by hitting the plugin status route.
// Returns the session on success; throws on non-2xx.
export async function verifySession(
  fetchImpl: typeof fetch,
): Promise<Session> {
  const s = current;
  if (!s) throw new Error("no session");
  return verifyCandidateSession(fetchImpl, s);
}

async function verifyCandidateSession(fetchImpl: typeof fetch, s: Session): Promise<Session> {
  const res = await fetchImpl(s.baseUrl + "/v0/management/plugins/access-guard/status", {
    headers: { Authorization: "Bearer " + s.secretKey },
  });
  if (!res.ok) {
    throw new Error("management key rejected (" + res.status + ")");
  }
  await res.json() as StatusResponse;
  return s;
}
