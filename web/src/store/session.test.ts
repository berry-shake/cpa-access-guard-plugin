import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { _resetKeyCache } from "./panelAuth";
import { cpampAuth, cpampV2Auth, cpampV2WithoutKey } from "./__fixtures__/cpampAuth";
import {
  setSession,
  clearSession,
  getSession,
  isAuthed,
  subscribe,
  bootstrapFromPanel,
} from "./session";

beforeEach(() => clearSession());

describe("session storage", () => {
  it("starts unauthenticated", () => {
    expect(isAuthed()).toBe(false);
    expect(getSession()).toBeNull();
  });

  it("stores base url and key in memory", () => {
    setSession("http://localhost:8317/", "secret-xyz");
    const s = getSession();
    expect(s).not.toBeNull();
    expect(s!.baseUrl).toBe("http://localhost:8317");
    expect(s!.secretKey).toBe("secret-xyz");
    expect(isAuthed()).toBe(true);
  });

  it("adds http:// scheme when missing", () => {
    setSession("127.0.0.1:8317", "k");
    expect(getSession()!.baseUrl).toBe("http://127.0.0.1:8317");
  });

  it("preserves https://", () => {
    setSession("https://cpa.example.com/", "k");
    expect(getSession()!.baseUrl).toBe("https://cpa.example.com");
  });

  it("trims trailing slashes", () => {
    setSession("http://h:8317///", "k");
    expect(getSession()!.baseUrl).toBe("http://h:8317");
  });

  it("clears on logout", () => {
    setSession("http://h", "k");
    clearSession();
    expect(isAuthed()).toBe(false);
    expect(getSession()).toBeNull();
  });

  it("notifies subscribers on set and clear", () => {
    let calls = 0;
    const unsub = subscribe(() => calls++);
    setSession("http://h", "k");
    clearSession();
    expect(calls).toBeGreaterThanOrEqual(2);
    unsub();
  });

  it("is not authed when key empty", () => {
    setSession("http://h", "");
    expect(isAuthed()).toBe(false);
  });
});

describe("CPAMP saved-session bootstrap", () => {
  const realSelf = window.self;
  const realTop = window.top;

  beforeEach(() => {
    _resetKeyCache();
    localStorage.clear();
    Object.defineProperty(window, "self", { value: window, configurable: true });
    Object.defineProperty(window, "top", { value: {}, configurable: true });
    localStorage.setItem("cli-proxy-auth", cpampV2Auth);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    localStorage.clear();
    clearSession();
    _resetKeyCache();
    Object.defineProperty(window, "self", { value: realSelf, configurable: true });
    Object.defineProperty(window, "top", { value: realTop, configurable: true });
  });

  it("verifies the remembered CPAMP admin key through its saved API base", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ enabled: true })));
    vi.stubGlobal("fetch", fetch);
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    expect(await bootstrapFromPanel()).toBe(true);
    expect(fetch).toHaveBeenCalledWith(cpampAuth.apiBase + "/v0/management/plugins/access-guard/status", {
      headers: { Authorization: "Bearer " + cpampAuth.managementKey },
    });
    expect(getSession()).toEqual({ baseUrl: cpampAuth.apiBase, secretKey: cpampAuth.managementKey });
    expect(setItem).not.toHaveBeenCalled();
  });

  it("keeps authentication false while the remembered key is being verified", async () => {
    let finish: (response: Response) => void = () => {};
    const pendingResponse = new Promise<Response>((resolve) => { finish = resolve; });
    vi.stubGlobal("fetch", vi.fn().mockReturnValue(pendingResponse));
    const bootstrap = bootstrapFromPanel();
    expect(isAuthed()).toBe(false);
    expect(getSession()).toBeNull();
    finish(new Response("{}"));
    expect(await bootstrap).toBe(true);
    expect(isAuthed()).toBe(true);
  });

  it("never emits an authenticated state when the saved key is rejected", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 401 })));
    const states: boolean[] = [];
    const unsubscribe = subscribe(() => states.push(isAuthed()));
    try {
      expect(await bootstrapFromPanel()).toBe(false);
      expect(states).not.toContain(true);
    } finally {
      unsubscribe();
    }
  });

  it("can restore again after the previous in-memory session has been lost", async () => {
    const fetch = vi.fn().mockImplementation(async () => new Response("{}"));
    vi.stubGlobal("fetch", fetch);
    expect(await bootstrapFromPanel()).toBe(true);
    clearSession();
    expect(await bootstrapFromPanel()).toBe(true);
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(localStorage.getItem("cli-proxy-auth")).toBe(cpampV2Auth);
  });

  it("leaves the user logged out when the saved admin key is rejected", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 401 })));
    expect(await bootstrapFromPanel()).toBe(false);
    expect(getSession()).toBeNull();
    expect(localStorage.getItem("cli-proxy-auth")).toBe(cpampV2Auth);
  });

  it("leaves the user logged out when verification cannot reach the manager", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Network error")));
    expect(await bootstrapFromPanel()).toBe(false);
    expect(getSession()).toBeNull();
  });

  it("does not send a request when CPAMP did not persist a key", async () => {
    localStorage.setItem("cli-proxy-auth", cpampV2WithoutKey);
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    expect(await bootstrapFromPanel()).toBe(false);
    expect(fetch).not.toHaveBeenCalled();
    expect(getSession()).toBeNull();
  });
});
