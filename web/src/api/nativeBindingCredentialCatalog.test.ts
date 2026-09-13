import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ClassifyRule, CredentialDescriptor, NativeCredentialOption } from "../types";

const mocks = vi.hoisted(() => ({
  apiClient: vi.fn(),
  get: vi.fn(),
  post: vi.fn(),
  fetchAIProviderCredentials: vi.fn(),
}));

vi.mock("./client", () => ({
  apiClient: mocks.apiClient,
  pluginPath: (suffix: string) => "/plugin" + suffix,
}));

vi.mock("./aiProviderCredentials", () => ({
  fetchAIProviderCredentials: mocks.fetchAIProviderCredentials,
}));

import { fetchNativeBindingCredentialCatalog } from "./mappings";

function rule(name: string, group: string, field = "filename", enabled = true): ClassifyRule {
  return { name, group, field, enabled, pattern: ".+" };
}

function authFile(id: string, email = id + "@example.com"): Record<string, unknown> {
  return { id, email, name: id + ".json", provider: "codex", id_token: { plan_type: "team" } };
}

function givenFiles(files: unknown[]): void {
  mocks.get.mockImplementation((url: string) => {
    if (url === "/v0/management/auth-files") return Promise.resolve({ data: { files } });
    return Promise.reject(new Error("Unexpected management request: " + url));
  });
}

function givenPreview(groups: Record<string, string[]>, ruleMatches: Record<string, unknown> = {}): void {
  mocks.post.mockResolvedValue({
    data: {
      groups,
      group_counts: Object.fromEntries(Object.entries(groups).map(([name, ids]) => [name, ids.length])),
      rule_matches: ruleMatches,
    },
  });
}

function previewDescriptors(): CredentialDescriptor[] {
  return (mocks.post.mock.calls[0][1] as { descriptors: CredentialDescriptor[] }).descriptors;
}

beforeEach(() => {
  vi.resetAllMocks();
  mocks.apiClient.mockReturnValue(mocks);
  givenFiles([]);
  givenPreview({});
  mocks.fetchAIProviderCredentials.mockResolvedValue([]);
});

describe("native binding credential catalog identities", () => {
  it("sorts by email before labels and filenames while preserving each exact Auth ID", async () => {
    givenFiles([
      { ...authFile("runtime/a.json", "zulu@example.com"), name: "a-first.json", label: "A first label" },
      { ...authFile("runtime/z.json", "alpha@example.com"), name: "z-last.json", label: "Z last label" },
    ]);

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["runtime/z.json", "runtime/a.json"]);
    expect(catalog.identitiesComplete).toBe(true);
    expect(catalog.credentials[0]).toMatchObject({
      id: "runtime/z.json",
      email: "alpha@example.com",
      name: "z-last.json",
      label: "Z last label",
      provider: "codex",
      source: "auth_file",
      plan: "team",
    });
  });

  it("deduplicates exact IDs without merging separate credentials that share an email", async () => {
    givenFiles([
      { ...authFile("runtime/a"), email: "same@example.com", label: "First identity" },
      { ...authFile("runtime/a"), email: "replacement@example.com", label: "Duplicate identity" },
      { ...authFile("runtime/b"), email: "same@example.com" },
    ]);

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.credentials).toHaveLength(2);
    expect(catalog.credentials.map(({ id }) => id).sort()).toEqual(["runtime/a", "runtime/b"]);
    expect(catalog.credentials.find(({ id }) => id === "runtime/a")).toMatchObject({
      email: "same@example.com",
      label: "First identity",
    });
    expect(previewDescriptors().map(({ id }) => id).sort()).toEqual(["runtime/a", "runtime/b"]);
  });

  it("merges both identity sources without replacing an auth-file identity with a duplicate AI-provider ID", async () => {
    givenFiles([{ ...authFile("shared-id"), label: "Auth file identity" }]);
    mocks.fetchAIProviderCredentials.mockResolvedValue([
      { id: "shared-id", provider: "codex", label: "Duplicate AI identity", source: "ai_provider" },
      { id: "codex:apikey:abcdef123456", provider: "codex", label: "AI identity", source: "ai_provider" },
    ]);

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.credentials).toHaveLength(2);
    expect(catalog.credentials.find(({ id }) => id === "shared-id")).toMatchObject({
      label: "Auth file identity",
      source: "auth_file",
    });
    expect(catalog.credentials.find(({ id }) => id === "codex:apikey:abcdef123456")).toMatchObject({
      label: "AI identity",
      source: "ai_provider",
    });
  });

  it("does not invent Auth IDs from filenames, emails, array positions, or malformed IDs", async () => {
    givenFiles([
      { name: "display-only.json", email: "missing@example.com", provider: "codex" },
      { id: " ", name: "blank-id.json", provider: "codex" },
      { id: 23, name: "numeric-id.json", provider: "codex" },
      null,
      { ...authFile(" Runtime/Case-Sensitive.json "), email: "valid@example.com" },
    ]);

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["Runtime/Case-Sensitive.json"]);
    expect(catalog.identitiesComplete).toBe(false);
    expect(catalog.groupsAvailable).toBe(false);
    expect(catalog.groups).toEqual({});
  });

  it("projects safe display fields and only runtime-supported classification attributes", async () => {
    givenFiles([
      {
        ...authFile("tenant/codex.json"),
        provider: " CODEX ",
        email: " display@example.com ",
        path: "/auth/tenant/codex.json",
        weight: 3,
        tier: "not-a-codex-claim",
        status: "active",
        disabled: true,
        unavailable: true,
        access_token: "sentinel-access-secret",
        refresh_token: "sentinel-refresh-secret",
        "api-key": "sentinel-api-secret",
        account_id: "sentinel-account-secret",
        arbitrary: "sentinel-arbitrary-secret",
        note: "sentinel-note-secret",
        priority: 9,
        websockets: true,
        metadata: { access_token: "sentinel-metadata-secret" },
        id_token: { plan_type: " Team ", account_id: "sentinel-claim-secret" },
      },
      { id: "ag.json", type: "ANTIGRAVITY", tier: " Pro ", id_token: { plan_type: "not-an-ag-claim" } },
    ]);

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.credentials.find(({ id }) => id === "tenant/codex.json")).toMatchObject({
      provider: "codex",
      email: "display@example.com",
      plan: "team",
      disabled: true,
      unavailable: true,
    });
    expect(previewDescriptors()).toEqual(expect.arrayContaining([
      {
        id: "tenant/codex.json",
        provider: "codex",
        attributes: { plan_type: "team", path: "/auth/tenant/codex.json", weight: "3" },
      },
      { id: "ag.json", provider: "antigravity", attributes: { tier: "pro" } },
    ]));
    expect(JSON.stringify(catalog)).not.toContain("sentinel-");
    expect(JSON.stringify(previewDescriptors())).not.toContain("display@example.com");
    expect(JSON.stringify(previewDescriptors())).not.toContain("sentinel-");
  });

  it("filters unknown preview IDs instead of creating credential identities from them", async () => {
    givenFiles([authFile("known")]);
    const rules = [rule("custom", "vip")];
    givenPreview(
      { vip: ["known", "missing", "display-only.json"] },
      { custom: ["known", "known", "missing", "display-only.json"] },
    );

    const catalog = await fetchNativeBindingCredentialCatalog(rules);

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["known"]);
    expect(catalog.identitiesComplete).toBe(true);
    expect(catalog.groups["classify:vip"]).toEqual(["known"]);
    expect(catalog.groupsAvailable).toBe(true);
    expect(catalog.unavailableGroups).toContain("classify:vip");
    expect(JSON.stringify(catalog.groups)).not.toContain("missing");
    expect(JSON.stringify(catalog.groups)).not.toContain("display-only.json");
  });

  it("marks built-in groups with unknown preview identities unavailable", async () => {
    givenFiles([authFile("known")]);
    givenPreview({ team: ["known", "missing"] });

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.groups.team).toEqual(["known"]);
    expect(catalog.unavailableGroups).toContain("team");
    expect(catalog.credentials.map(({ id }) => id)).toEqual(["known"]);
  });
});

describe("native binding credential catalog groups", () => {
  it("uses server rule matches for custom unions and separates names that collide with built-in tiers", async () => {
    givenFiles([
      authFile("A"), authFile("B"), authFile("C"), authFile("D"),
      { ...authFile("E"), id_token: {} },
    ]);
    const rules = [
      rule("team-custom", " TEAM "),
      rule("vip-one", "VIP"),
      rule("vip-two", "classify:vip"),
    ];
    givenPreview(
      { team: ["A", "B", "D"], vip: ["A", "C"], "classify:vip": ["D"], supported: ["E"] },
      { "team-custom": ["A", "D"], "vip-one": ["A", "C"], "vip-two": ["D", "D"] },
    );

    const catalog = await fetchNativeBindingCredentialCatalog(rules);

    expect(catalog.groupsAvailable).toBe(true);
    expect(catalog.unavailableGroups).toEqual([]);
    expect(catalog.groups["classify:team"].slice().sort()).toEqual(["A", "D"]);
    expect(catalog.groups["classify:vip"].slice().sort()).toEqual(["A", "C", "D"]);
    expect(catalog.groups.team).toEqual(["B"]);
    expect(catalog.groups.supported).toEqual(["E"]);
    expect(catalog.groups).not.toHaveProperty("vip");
    expect(catalog.groups).not.toHaveProperty("classify:classify:vip");
    expect(mocks.post).toHaveBeenCalledWith("/plugin/classify-preview", {
      descriptors: expect.any(Array),
      rules,
    });
  });

  it("uses the server Go/RE2 result instead of re-evaluating rules with JavaScript regular expressions", async () => {
    givenFiles([authFile("A")]);
    const rules = [{ ...rule("go-pattern", "vip", "provider"), pattern: "(?i)^CODEX$" }];
    givenPreview({ vip: ["A"] }, { "go-pattern": ["A"] });

    const catalog = await fetchNativeBindingCredentialCatalog(rules);

    expect(catalog.groups["classify:vip"]).toEqual(["A"]);
    expect(catalog.unavailableGroups).toEqual([]);
  });

  it.each(["note", "email"])(
    "marks enabled unsupported %s rules and built-in fallback groups unavailable while retaining independent custom groups",
    async (field) => {
      givenFiles([authFile("A"), authFile("B"), { ...authFile("C"), id_token: { plan_type: "free" } }]);
      const rules = [rule("unknown", "private", field), rule("known", "vip")];
      givenPreview({ vip: ["A"], team: ["B"], free: ["C"] }, { unknown: [], known: ["A"] });

      const catalog = await fetchNativeBindingCredentialCatalog(rules);

      expect(catalog.groupsAvailable).toBe(true);
      expect(catalog.identitiesComplete).toBe(true);
      expect(catalog.groups["classify:vip"]).toEqual(["A"]);
      expect(catalog.unavailableGroups).toEqual(expect.arrayContaining(["classify:private", "team", "free"]));
      expect(catalog.unavailableGroups).not.toContain("classify:vip");
    },
  );

  it("does not invalidate built-in groups for a disabled unsupported rule", async () => {
    givenFiles([authFile("A"), authFile("B")]);
    const rules = [rule("disabled", "private", "email", false), rule("known", "vip")];
    givenPreview({ vip: ["A"], team: ["B"] }, { disabled: [], known: ["A"] });

    const catalog = await fetchNativeBindingCredentialCatalog(rules);

    expect(catalog.groupsAvailable).toBe(true);
    expect(catalog.unavailableGroups).toEqual([]);
    expect(catalog.groups.team).toEqual(["B"]);
    expect(catalog.groups["classify:vip"]).toEqual(["A"]);
    expect(catalog.groups["classify:private"] ?? []).toEqual([]);
  });

  it("invalidates a custom union when any enabled rule in that union cannot be previewed", async () => {
    givenFiles([authFile("A"), authFile("B")]);
    const rules = [rule("visible-vip", "vip"), rule("unknown-vip", "VIP", "note"), rule("visible-other", "other")];
    givenPreview({ vip: ["A"], other: ["B"] }, { "visible-vip": ["A"], "unknown-vip": [], "visible-other": ["B"] });

    const catalog = await fetchNativeBindingCredentialCatalog(rules);

    expect(catalog.unavailableGroups).toContain("classify:vip");
    expect(catalog.unavailableGroups).not.toContain("classify:other");
    expect(catalog.groups["classify:other"]).toEqual(["B"]);
  });

  it("marks weight-based groups unavailable when an AI-provider identity lacks weight while keeping provider rules usable", async () => {
    givenFiles([{ ...authFile("A"), weight: 2 }]);
    mocks.fetchAIProviderCredentials.mockResolvedValue([
      { id: "codex:apikey:abcdef123456", provider: "codex", source: "ai_provider" },
    ]);
    const rules = [rule("weighted", "weighted", "weight"), rule("provider", "codex", "provider")];
    givenPreview(
      { weighted: ["A"], codex: ["A", "codex:apikey:abcdef123456"] },
      { weighted: ["A"], provider: ["A", "codex:apikey:abcdef123456"] },
    );

    const catalog = await fetchNativeBindingCredentialCatalog(rules);

    expect(catalog.groupsAvailable).toBe(true);
    expect(catalog.unavailableGroups).toContain("classify:weighted");
    expect(catalog.unavailableGroups).not.toContain("classify:codex");
    expect(catalog.groups["classify:codex"].slice().sort()).toEqual(["A", "codex:apikey:abcdef123456"]);
  });

  it.each([undefined, "A", 1, {}])(
    "marks a missing or malformed rule result unavailable instead of claiming zero matches (%j)",
    async (invalidMatches) => {
      givenFiles([authFile("A"), authFile("B")]);
      const rules = [rule("missing", "private"), rule("known", "vip")];
      const ruleMatches: Record<string, unknown> = { known: ["A"] };
      if (invalidMatches !== undefined) ruleMatches.missing = invalidMatches;
      givenPreview({ vip: ["A"], team: ["B"] }, ruleMatches);

      const catalog = await fetchNativeBindingCredentialCatalog(rules);

      expect(catalog.groupsAvailable).toBe(true);
      expect(catalog.unavailableGroups).toEqual(expect.arrayContaining(["classify:private", "team"]));
      expect(catalog.unavailableGroups).not.toContain("classify:vip");
      expect(catalog.groups["classify:vip"]).toEqual(["A"]);
    },
  );

  it("distinguishes an explicit empty rule set from unknown classification rules", async () => {
    givenFiles([authFile("A")]);
    givenPreview({ team: ["A"] });

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.groupsAvailable).toBe(true);
    expect(catalog.groups.team).toEqual(["A"]);
    expect(mocks.post).toHaveBeenCalledWith("/plugin/classify-preview", {
      descriptors: expect.any(Array),
      rules: [],
    });
  });
});

describe("native binding credential catalog availability and request cost", () => {
  it("accepts the alternate auth-files response key without making a second request", async () => {
    mocks.get.mockResolvedValue({ data: { "auth-files": [authFile("A")] } });
    givenPreview({ team: ["A"] });

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["A"]);
    expect(catalog.groupsAvailable).toBe(true);
    expect(catalog.groups.team).toEqual(["A"]);
    expect(mocks.get).toHaveBeenCalledTimes(1);
  });

  it.each([null, {}, { files: "not-an-array" }])(
    "does not treat an incompatible auth-file response as a successfully loaded empty inventory (%j)",
    async (data) => {
      mocks.get.mockResolvedValue({ data });

      const catalog = await fetchNativeBindingCredentialCatalog([]);

      expect(catalog.credentials).toEqual([]);
      expect(catalog.identitiesComplete).toBe(false);
      expect(catalog.groups).toEqual({});
      expect(catalog.groupsAvailable).toBe(false);
    },
  );

  it("retains direct identities without claiming groups when classification rules could not be loaded", async () => {
    givenFiles([authFile("A")]);

    const catalog = await fetchNativeBindingCredentialCatalog(null);

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["A"]);
    expect(catalog.identitiesComplete).toBe(true);
    expect(catalog.groupsAvailable).toBe(false);
    expect(catalog.groups).toEqual({});
    expect(mocks.post).not.toHaveBeenCalled();
  });

  it("retains safe AI-provider identities when auth-file loading fails", async () => {
    const aiCredential: NativeCredentialOption = {
      id: "codex:apikey:123456789abc",
      provider: "codex",
      name: "Codex #1",
      source: "ai_provider",
      status: "configured",
    };
    mocks.get.mockRejectedValue(new Error("Auth-file endpoint unavailable"));
    mocks.fetchAIProviderCredentials.mockResolvedValue([aiCredential]);

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.credentials).toHaveLength(1);
    expect(catalog.credentials[0]).toMatchObject(aiCredential);
    expect(catalog.identitiesComplete).toBe(false);
    expect(catalog.groupsAvailable).toBe(false);
    expect(catalog.groups).toEqual({});
    expect(mocks.post).not.toHaveBeenCalled();
  });

  it("returns an unavailable empty catalog when both identity sources fail", async () => {
    mocks.get.mockRejectedValue(new Error("Auth-file endpoint unavailable"));
    mocks.fetchAIProviderCredentials.mockRejectedValue(new Error("AI-provider endpoint unavailable"));

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.credentials).toEqual([]);
    expect(catalog.identitiesComplete).toBe(false);
    expect(catalog.groups).toEqual({});
    expect(catalog.groupsAvailable).toBe(false);
  });

  it("retains auth-file identities without claiming complete groups when AI-provider loading fails", async () => {
    givenFiles([authFile("A")]);
    mocks.fetchAIProviderCredentials.mockRejectedValue(new Error("AI-provider endpoint temporarily unavailable"));

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["A"]);
    expect(catalog.identitiesComplete).toBe(false);
    expect(catalog.groups).toEqual({});
    expect(catalog.groupsAvailable).toBe(false);
    expect(mocks.post).not.toHaveBeenCalled();
  });

  it("preserves direct credential identities after a preview request fails", async () => {
    givenFiles([authFile("A")]);
    mocks.post.mockRejectedValue(new Error("Classification preview unavailable"));

    const catalog = await fetchNativeBindingCredentialCatalog([rule("vip", "vip")]);

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["A"]);
    expect(catalog.identitiesComplete).toBe(true);
    expect(catalog.groups).toEqual({});
    expect(catalog.groupsAvailable).toBe(false);
  });

  it("preserves direct identities when an incompatible backend omits all per-rule results", async () => {
    givenFiles([authFile("A")]);
    mocks.post.mockResolvedValue({ data: { groups: { vip: ["A"] }, group_counts: { vip: 1 } } });

    const catalog = await fetchNativeBindingCredentialCatalog([rule("vip", "vip")]);

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["A"]);
    expect(catalog.identitiesComplete).toBe(true);
    expect(catalog.groups).toEqual({});
    expect(catalog.groupsAvailable).toBe(false);
  });

  it("does not report complete groups when the preview aggregate is malformed", async () => {
    givenFiles([authFile("A")]);
    mocks.post.mockResolvedValue({ data: { groups: null, group_counts: {}, rule_matches: {} } });

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.credentials.map(({ id }) => id)).toEqual(["A"]);
    expect(catalog.identitiesComplete).toBe(true);
    expect(catalog.groups).toEqual({});
    expect(catalog.groupsAvailable).toBe(false);
  });

  it("loads one shared auth-file snapshot and one preview without per-credential model requests", async () => {
    const files = Array.from({ length: 240 }, (_, index) => authFile("tenant/" + index));
    givenFiles(files);
    givenPreview({ team: files.map((file) => String(file.id)) });

    const catalog = await fetchNativeBindingCredentialCatalog([]);

    expect(catalog.credentials).toHaveLength(240);
    expect(catalog.identitiesComplete).toBe(true);
    expect(catalog.groups.team).toHaveLength(240);
    expect(mocks.get).toHaveBeenCalledTimes(1);
    expect(mocks.get).toHaveBeenCalledWith("/v0/management/auth-files");
    expect(mocks.get.mock.calls.some(([url]) => url === "/v0/management/auth-files/models")).toBe(false);
    expect(mocks.post).toHaveBeenCalledTimes(1);
    expect(previewDescriptors()).toHaveLength(240);
    expect(mocks.fetchAIProviderCredentials).toHaveBeenCalledTimes(1);
    expect(mocks.fetchAIProviderCredentials).toHaveBeenCalledWith(mocks, false);
  });

  it("keeps the captured management client after the session changes while the identity snapshot is loading", async () => {
    let resolveAuthFiles!: (value: { data: { files: Record<string, unknown>[] } }) => void;
    const authFiles = new Promise<{ data: { files: Record<string, unknown>[] } }>((resolve) => {
      resolveAuthFiles = resolve;
    });
    mocks.get.mockReturnValue(authFiles);
    givenPreview({ team: ["old-host-auth-id"] });
    const nextHostClient = { get: vi.fn(), post: vi.fn() };

    const pending = fetchNativeBindingCredentialCatalog([]);
    mocks.apiClient.mockReturnValue(nextHostClient);
    resolveAuthFiles({ data: { files: [authFile("old-host-auth-id")] } });
    const catalog = await pending;

    expect(catalog.identitiesComplete).toBe(true);
    expect(catalog.groupsAvailable).toBe(true);
    expect(catalog.groups.team).toEqual(["old-host-auth-id"]);
    expect(mocks.apiClient).toHaveBeenCalledTimes(1);
    expect(mocks.get).toHaveBeenCalledWith("/v0/management/auth-files");
    expect(mocks.fetchAIProviderCredentials).toHaveBeenCalledWith(mocks, false);
    expect(mocks.post).toHaveBeenCalledWith("/plugin/classify-preview", {
      descriptors: [{ id: "old-host-auth-id", provider: "codex", attributes: { plan_type: "team" } }],
      rules: [],
    });
    expect(nextHostClient.get).not.toHaveBeenCalled();
    expect(nextHostClient.post).not.toHaveBeenCalled();
  });
});
