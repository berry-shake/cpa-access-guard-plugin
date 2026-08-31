import { apiClient, pluginPath } from "./client";
import { fetchAIProviderCredentials } from "./aiProviderCredentials";
import type { CatalogModel } from "../types";

/** Prefix applied to custom classify groups in catalog entries / ModelRule.group. */
export const CLASSIFY_GROUP_PREFIX = "classify:";

/** True when group is a custom classify group (classify:name). */
export function isClassifyGroup(group: string | undefined | null): boolean {
  return !!group && group.toLowerCase().startsWith(CLASSIFY_GROUP_PREFIX);
}

/**
 * Human label for a catalog/ModelRule group. Custom classify groups render as
 * "自定义 · name" (via picker.tier.classify); built-in tiers use picker.tier.*.
 */
export function formatTierLabel(
  t: (k: string, v?: Record<string, string | number>) => string,
  group: string,
): string {
  if (isClassifyGroup(group)) {
    const name = group.slice(CLASSIFY_GROUP_PREFIX.length);
    return t("picker.tier.classify", { name });
  }
  const key = "picker.tier." + group;
  const translated = t(key);
  return translated === key ? group : translated;
}

// CPA has no single "list providers+models" endpoint. We compose from several
// management routes. The raw shapes are loose, so each adapter pulls strings out
// defensively and feeds them into normalizeCatalog, which is the unit-tested core.

const STATIC_CHANNELS = [
  "claude",
  "gemini",
  "vertex",
  "aistudio",
  "codex",
  "kimi",
  "antigravity",
  "xai",
] as const;

// Providers whose auth files carry a tier/plan identity claim. Only these get
// tier subgroups in the picker (codex via id_token plan_type, antigravity via
// tier_id). Everything else stays a flat per-provider group — there's no
// meaningful "tier" to split on, and the plugin Scheduler won't filter them.
const TIERED_PROVIDERS = new Set(["codex", "antigravity"]);

// "supported" is the synthetic group for a tiered-provider auth file whose
// identity claim we couldn't read (e.g. an old codex file with no id_token).
// It must NOT be confused with a real tier: a key pinned to "team" never lands
// on a "supported" file, and vice versa. The plugin Scheduler treats
// "supported"/"unknown" as the untiered bucket.
const SUPPORTED_GROUP = "supported";

interface RawEntry {
  provider?: string;
  models?: unknown;
  // group is only meaningful when populated from an auth-file source; static
  // channels and openai-compat entries have no tier concept and leave it empty.
  group?: string;
  // AI-provider rows are backed by one exact runtime Auth ID. They must remain
  // visible even if an OAuth auth file also contributes tiered Codex rows.
  credentialBacked?: boolean;
}

// Collect (provider, [group], model) tuples from heterogeneous CPA responses.
// Within a single (provider, group) bucket, duplicate models are de-duplicated
// — same model supported by multiple same-tier auth files appears once. A model
// supported by BOTH "free" and "team" tiers appears as two separate rows so the
// user can authorize it under a specific tier (real isolation via Scheduler).
export function normalizeCatalog(entries: RawEntry[]): CatalogModel[] {
  const seen = new Set<string>();
  const out: CatalogModel[] = [];
  for (const e of entries) {
    const provider = (e.provider ?? "").toString().trim().toLowerCase();
    if (!provider) continue;
    const group = (e.group ?? "").toString().trim().toLowerCase();
    for (const m of toStrings(e.models)) {
      const model = m.trim();
      if (!model) continue;
      const key = provider + "" + group + "" + model.toLowerCase();
      if (seen.has(key)) continue;
      seen.add(key);
      const row: CatalogModel = { provider, model };
      if (group) row.group = group;
      out.push(row);
    }
  }
  // Stable sort: provider, then group (empty group sorts first within provider),
  // then model (case-insensitive).
  out.sort((a, b) => {
    if (a.provider !== b.provider) return a.provider.localeCompare(b.provider);
    const ga = a.group ?? "";
    const gb = b.group ?? "";
    if (ga !== gb) return ga.localeCompare(gb);
    return a.model.toLowerCase().localeCompare(b.model.toLowerCase());
  });
  return out;
}

function toStrings(v: unknown): string[] {
  if (v == null) return [];
  if (typeof v === "string") return [v];
  if (Array.isArray(v)) {
    return v
      .map((x) => {
        if (typeof x === "string") return x;
        if (x && typeof x === "object") {
          const mo = (x as Record<string, unknown>).model;
          if (typeof mo === "string") return mo;
          const id = (x as Record<string, unknown>).id;
          if (typeof id === "string") return id;
          const name = (x as Record<string, unknown>).name;
          if (typeof name === "string") return name;
        }
        return "";
      })
      .filter((s) => s !== "");
  }
  if (typeof v === "object") {
    // object map like { "model-a": {...}, "model-b": {...} }
    return Object.keys(v as Record<string, unknown>);
  }
  return [];
}

// --- CPA response adapters (best-effort, defensive) ---

// One auth-file row from /v0/management/auth-files. `id` is the runtime auth ID
// used by SchedulerAuthCandidate.ID and therefore by filename/id classify
// rules. It may differ from the display/file name. The management endpoint for
// per-auth models accepts either form, but querying by the stable ID avoids an
// ambiguous FileName when one source expands into multiple runtime auths.
interface AuthFileMeta {
  id: string;
  // provider as reported by the auth-files LIST endpoint (e.g. "codex",
  // "antigravity", "claude"). The per-file /auth-files/models endpoint does NOT
  // echo a provider/channel — its models objects carry a per-model "type"
  // ("openai" for codex-backed models) which is NOT the auth provider. We must
  // carry the list-endpoint provider here so each file's models land under the
  // right provider group; otherwise the file name leaks in as the "provider".
  provider: string;
  // Only values that correspond to Scheduler-safe Attributes are forwarded to
  // /catalog. In particular, Antigravity tier remains `tier`; it must never be
  // relabelled as Codex `plan_type`.
  attributes?: Record<string, string>;
}

function fromAuthFiles(payload: unknown): AuthFileMeta[] {
  const root = payload as Record<string, unknown> | null;
  const list = root?.["auth-files"] ?? root?.["files"];
  if (!Array.isArray(list)) return [];
  const out: AuthFileMeta[] = [];
  for (const item of list) {
    const o = (item ?? {}) as Record<string, unknown>;
    const rawID = typeof o["id"] === "string" ? o["id"].trim() : "";
    const rawName = typeof o["name"] === "string" ? o["name"].trim() : "";
    const id = rawID || rawName;
    if (!id) continue;
    const provider = ((o["provider"] as string) ?? (o["type"] as string) ?? "").trim().toLowerCase();
    const attributes = authFileCatalogAttributes(o, provider);
    out.push({
      id,
      provider,
      attributes: Object.keys(attributes).length ? attributes : undefined,
    });
  }
  return out;
}

function copyCatalogStringAttribute(
  source: Record<string, unknown>,
  target: Record<string, string>,
  key: string,
): void {
  const value = source[key];
  if (typeof value === "string" && value.trim()) {
    target[key] = value.trim();
  }
}

function copyCatalogNumericAttribute(
  source: Record<string, unknown>,
  target: Record<string, string>,
  key: string,
): void {
  const value = source[key];
  if (typeof value === "string" && value.trim()) {
    target[key] = value.trim();
  } else if (typeof value === "number" && Number.isFinite(value)) {
    target[key] = String(value);
  }
}

// Build only the Scheduler-attribute subset whose management representation has
// trustworthy provenance. `path` is emitted directly from Auth.Attributes and
// CPA normalizes file metadata `weight` into Auth.Attributes on both native and
// plugin-parsed file paths. Do not copy note/priority/websockets: ListAuthFiles
// can synthesize those fields from Metadata even when scheduler.pick receives
// no corresponding Attribute, which would create a false-positive picker
// group. Arbitrary credential metadata (email, access_token, etc.) is likewise
// deliberately excluded.
function authFileCatalogAttributes(
  entry: Record<string, unknown>,
  provider: string,
): Record<string, string> {
  const attributes: Record<string, string> = {};

  if (provider === "codex") {
    const planType = readCodexPlanType(entry);
    if (planType) attributes.plan_type = planType;
  }

  if (provider === "antigravity") {
    const tier = entry["tier"];
    if (typeof tier === "string" && tier.trim()) {
      attributes.tier = tier.trim().toLowerCase();
    }
  }

  copyCatalogStringAttribute(entry, attributes, "path");
  copyCatalogNumericAttribute(entry, attributes, "weight");
  return attributes;
}

// Extract the tier/plan identity from an auth-files list entry. codex's
// ListAuthFiles response flattens the id_token claims directly onto the
// id_token object (id_token.plan_type), NOT under a nested "claims" key —
// verified against a live CPA build. We still tolerate the nested shape
// (id_token.claims.plan_type) defensively in case a future build restructures.
// antigravity's tier identity isn't exposed on the list entry on current
// builds; its files fall through to the "supported" bucket (the Scheduler
// side reads Attributes["tier"] for antigravity, which is a separate path).
// Returns "" when no recognizable claim is present (→ "supported" bucket).
// Exported for unit testing against real ListAuthFiles payloads.
function readCodexPlanType(entry: Record<string, unknown>): string {
  const idToken = entry["id_token"];
  if (idToken && typeof idToken === "object") {
    const tok = idToken as Record<string, unknown>;
    // Primary path (verified live): plan_type flattened directly on id_token.
    const plan = tok["plan_type"];
    if (typeof plan === "string" && plan.trim() !== "") {
      return plan.trim().toLowerCase();
    }
    // Defensive fallback: nested under a "claims" sub-object.
    const claims = tok["claims"];
    if (claims && typeof claims === "object") {
      const nested = (claims as Record<string, unknown>)["plan_type"];
      if (typeof nested === "string" && nested.trim() !== "") {
        return nested.trim().toLowerCase();
      }
    }
  }
  return "";
}

export function readPlanType(entry: Record<string, unknown>): string {
  return readCodexPlanType(entry);
}

// Build a RawEntry from a per-file /auth-files/models response. The provider
// comes from the LIST endpoint (carried in `provider`), NOT the models
// payload — the models objects report a per-model "type" ("openai" for codex
// backed models) which is the upstream format, not the auth provider, and the
// response itself has no top-level channel/provider field.
function fromAuthFileModels(provider: string, payload: unknown): RawEntry[] {
  const root = payload as Record<string, unknown> | null;
  const models = root?.["models"] ?? root?.["available_models"];
  return [{ provider, models }];
}

function fromModelDefinitions(channel: string, payload: unknown): RawEntry[] {
  const root = payload as Record<string, unknown> | null;
  const models = root?.["models"] ?? root?.["definitions"];
  return [{ provider: channel, models }];
}

// Filter the collected raw entries down to those that should be visible in the
// picker. Bare (group-less) static entries — from model-definitions/<channel> —
// are dropped when EITHER:
//   1. They're a tiered provider (codex, antigravity) that the auth-files pass
//      already contributed tier subgroups for, so the bare row is a duplicate
//      with no backing auth file (the "codex · team" subgroup is the real one).
//   2. Their provider is neither configured (no credential) nor has a
//      currently-selected model — i.e. an unconfigured channel that should be
//      hidden from the picker.
// Entries carrying a `group` are auth-file sourced and already imply a
// configured credential, so they're kept unconditionally.
// Exported for unit testing.
export function filterByConfigured(
  entries: RawEntry[],
  configured: Set<string>,
  selected: Set<string>,
  tieredFromAuth: Set<string>,
): RawEntry[] {
  const out: RawEntry[] = [];
  for (const e of entries) {
    const provider = (e.provider ?? "").toLowerCase();
    if (e.group === undefined) {
      // Bare static entry (from model-definitions/<channel>).
      // Drop when it's a tiered provider already covered by auth-files (the
      // tier subgroups are the real, backed rows; this bare one is a dup with
      // no auth file behind it)...
      const dupOfTiered =
        !e.credentialBacked && TIERED_PROVIDERS.has(provider) && tieredFromAuth.has(provider);
      // ...or when the provider is neither configured nor has a selected model
      // (unconfigured channel should be hidden, but an edited key's rows stay
      // visible so the user can uncheck them).
      const unconfigured =
        !configured.has(provider) && !selected.has(provider);
      if (dupOfTiered || unconfigured) continue;
    }
    out.push(e);
  }
  return out;
}

// Fetch the composed catalog. Failures of individual sources are swallowed so
// that one unavailable endpoint doesn't blank the whole picker. A 401/403 here
// is real (bad key) and surfaces through the shared client as a forced
// re-login — that's intended; we don't mask auth failures.
//
// `selectedProviders` is the set of providers (lowercased) the caller already
// has model rules for (edit-mode prefill). Providers in this set stay visible
// even when unconfigured, so the user can see and uncheck their rows. New-key
// mode passes nothing (empty set) and only configured channels appear.
export async function fetchCatalog(
  selectedProviders?: Set<string>,
): Promise<CatalogModel[]> {
  const c = apiClient();
  const entries: RawEntry[] = [];
  // Tiered providers that the auth-files path contributed models for. Filled
  // during the auth-files pass; read in filterByConfigured to suppress bare
  // static entries for providers already covered with tier subgroups.
  const authFileTieredProviders = new Set<string>();
  // Providers the user has actually configured a credential for (an OAuth auth
  // file is present, or a non-empty *-api-key list). Bare static-definition
  // entries for providers NOT in this set are hidden unless a model under them
  // is already selected (see filterByConfigured).
  const configuredProviders = new Set<string>();
  const selected = new Set<string>();
  for (const p of selectedProviders ?? []) selected.add(p.toLowerCase());

  const safe = async <T>(
    p: Promise<{ data: T }>,
    apply: (d: T) => void | Promise<void>,
  ) => {
    try {
      const { data } = await p;
      await apply(data);
    } catch {
      /* skip unavailable source */
    }
  };

  try {
    const aiCredentials = await fetchAIProviderCredentials(c);
    for (const credential of aiCredentials) {
      const provider = credential.provider.trim().toLowerCase();
      if (!provider) continue;
      configuredProviders.add(provider);
      entries.push({
        provider,
        models: credential.models ?? [],
        credentialBacked: true,
      });
    }
  } catch {
    // Keep the remaining catalog sources available on older/incompatible CPA
    // builds. Authentication failures still clear the shared panel session via
    // the API client's response interceptor.
  }

  // auth-files: fetch file list + per-file models, then POST to the plugin
  // /catalog so classify rules + built-in tiers are applied server-side
  // (custom groups come back as classify:<name>). Compat/API-key channels
  // stay flat and are merged separately above/below.
  await safe(c.get("/v0/management/auth-files"), async (d) => {
    const metas = fromAuthFiles(d);
    for (const m of metas) {
      if (m.provider) configuredProviders.add(m.provider);
    }
    const perFile = await Promise.all(
      metas.map((m) =>
        c
          .get("/v0/management/auth-files/models", { params: { name: m.id } })
          .then((r) => ({ meta: m, data: r.data }))
          .catch(() => null),
      ),
    );

    const credentials: {
      id: string;
      provider: string;
      attributes?: Record<string, string>;
      models: string[];
    }[] = [];

    for (const res of perFile) {
      if (!res) continue;
      const fileEntries = fromAuthFileModels(res.meta.provider, res.data);
      const models = toStrings(fileEntries[0]?.models);
      if (!res.meta.provider || models.length === 0) continue;
      credentials.push({
        id: res.meta.id,
        provider: res.meta.provider,
        attributes: res.meta.attributes,
        models,
      });
    }

    if (credentials.length === 0) return;

    // Prefer plugin /catalog (classify + builtin). Fall back to the legacy
    // client-side tier split if the plugin route is unavailable.
    try {
      const { data } = await c.post<{ entries?: RawEntry[] }>(pluginPath("/catalog"), {
        credentials,
      });
      for (const e of data.entries ?? []) {
        const provider = (e.provider ?? "").toLowerCase();
        if (!provider) continue;
        entries.push(e);
        // Any grouped auth-file provider suppresses bare static definitions
        // for that provider (same as the old tiered-from-auth path).
        if (e.group) authFileTieredProviders.add(provider);
        // Tiered providers always suppress bare static even if some files
        // came back flat (empty group) — mirrors prior TIERED_PROVIDERS logic.
        if (TIERED_PROVIDERS.has(provider)) authFileTieredProviders.add(provider);
      }
    } catch {
      for (const cred of credentials) {
        const provider = cred.provider.toLowerCase();
        const e: RawEntry = { provider, models: cred.models };
        if (TIERED_PROVIDERS.has(provider)) {
          e.group = cred.attributes?.plan_type || cred.attributes?.tier || SUPPORTED_GROUP;
          authFileTieredProviders.add(provider);
        }
        entries.push(e);
      }
    }
  });

  for (const ch of STATIC_CHANNELS) {
    await safe(
      c.get("/v0/management/model-definitions/" + ch),
      (d) => {
        entries.push(...fromModelDefinitions(ch, d));
      },
    );
  }

  const filtered = filterByConfigured(
    entries,
    configuredProviders,
    selected,
    authFileTieredProviders,
  );
  return normalizeCatalog(filtered);
}

// A picker group: a provider, optionally split by tier (codex free / team /
// supported…). When group is undefined the picker renders the provider as a
// flat group (legacy behavior). When group is set, the provider's models are
// shown under a tier-labeled subgroup so the user can authorize a model under
// a specific tier — the plugin Scheduler honors the chosen tier at runtime.
export interface CatalogGroup {
  provider: string;
  group?: string;
  models: string[];
}

// Build picker groups from the normalized catalog. Adjacent (provider, group)
// buckets collapse into one group with their models merged (normalizeCatalog
// already de-duplicated within a bucket, so the merge is just concatenation).
export function groupByCatalog(catalog: CatalogModel[]): CatalogGroup[] {
  const map = new Map<string, CatalogGroup>();
  for (const c of catalog) {
    const group = c.group ?? "";
    const key = c.provider + "\0" + group;
    let bucket = map.get(key);
    if (!bucket) {
      bucket = { provider: c.provider, models: [] };
      if (group) bucket.group = group;
      map.set(key, bucket);
    }
    bucket.models.push(c.model);
  }
  return Array.from(map.values()).sort((a, b) => {
    if (a.provider !== b.provider) return a.provider.localeCompare(b.provider);
    return (a.group ?? "").localeCompare(b.group ?? "");
  });
}
