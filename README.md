# Access Guard

Downstream **API key policy** plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).

In plain words: you issue your own `cpa_…` keys to clients. Each key only sees the models you allow, can be rate-limited and budget-limited, and is routed to real CPA upstream providers (Codex, Claude, OpenAI-compat channels, etc.). CPA’s top-level native `api-keys` can also be bound to specific auth-file groups — **do not put plugin-issued keys into `api-keys`**, or you bypass the plugin-key model, RPM, and budget policies.

| | |
|---|---|
| **Repo** | [berry-shake/cpa-access-guard-plugin](https://github.com/berry-shake/cpa-access-guard-plugin) |
| **License** | MIT |
| **Install** | Build from source until the first Access Guard GitHub release is published |
| **Lineage** | Derived from [origin652/cpa-plugin-key-policy](https://github.com/origin652/cpa-plugin-key-policy) under the MIT license |
| **中文说明** | [README.zh-CN.md](./README.zh-CN.md) |

---

## What it does (human version)

1. **Issue keys** — create many downstream keys; each has an allow-list of models (or shared aliases).
2. **Route** — client calls with alias name `fast`; plugin rewrites to e.g. `codex` + `gpt-5.4-mini`.
3. **Limit** — per-key RPM, optional daily/weekly USD caps, token or per-call billing.
4. **Isolate credentials (tiers / groups)** — pin a request to Codex free/team/… or to a **custom classify group** so it never lands on the wrong auth file.
5. **Multi-target aliases** — one alias can point at several backends (priority or round-robin).
6. **Bind native CPA keys** — retain top-level `api-keys` authentication while limiting each key to an auth-file group.
7. **Web UI** — manage keys, native-key bindings, global aliases, and credential classification inside CPA.

---

## Concepts

### Downstream key

A plugin-owned secret (`cpa_…`). Authenticated only by this plugin. Holds:

- allowed **models** and/or **aliases**
- RPM
- optional daily / weekly dollar limits
- optional `allow_models_endpoint` (see below)

### Alias (global mapping table)

A reusable name like `fast` that expands to one or more **targets**:

| Field | Meaning |
|--------|---------|
| `provider` | CPA provider id (`codex`, `claude`, or an openai-compatibility **name** such as `cerebras`) |
| `target_model` | Real upstream model id |
| `group` | Optional credential filter (see [Credential groups](#credential-groups-tiers--classify)) |
| `dispatch` | `priority` (always first usable target) or `round-robin` |
| billing | `tokens` (per-million prices) or `per_call` (fixed USD) |

Keys can **reference** aliases instead of duplicating targets. Multi-target aliases expand to several rules with the same alias name; auth and routing share one pick per request so the `group` filter matches the chosen target.

### Credential groups (tiers + classify)

Two sources of “which auth file may serve this request”:

| Kind | How it appears in the picker | Stored in mapping as |
|------|------------------------------|----------------------|
| **Built-in tier** (Codex `plan_type`, Antigravity `tier`) | e.g. Free tier / Team | bare name: `free`, `team`, `supported` |
| **Custom classify rule** | e.g. **Custom · vip** | prefixed: `classify:vip` |

**Runtime rule on the normal Scheduler path:** if a mapping sets a group, the plugin scheduler **only** picks auth files in that group. No match → hard failure (`auth_not_found`); this plugin does not fall back to another tier. CPA's special Antigravity credits fallback bypasses the Scheduler, so strict isolation also requires `quota-exceeded.antigravity-credits: false`.

**Custom classification** (Web UI → Mapping → Credential Classification):

- Match auth-file fields (`filename`, `provider`, `plan_type`, `tier`, …) with a regex.
- Assign a **group name** you choose (stored bare on the rule).
- Catalog and mappings use `classify:<name>` so it never collides with built-in `free` / `team`.
- One file can match **multiple** custom groups (shown under each).
- Rules are combined as a union. Multiple rules assigning the same group widen that group's allow-list; use one anchored auth-ID regex and avoid overlapping rules for strict isolation.
- If no custom rule matches → built-in tier (for Codex/Antigravity) or flat (no group) for other auth-file providers.
- OpenAI-compat / API-key channels stay **flat** (no groups).

Upgrade note: from v0.5.0, `supported` is only the built-in unknown tier for Codex/Antigravity. If an older hand-written mapping assigned `group: supported` to Claude, Gemini, OpenAI-compatible, or another non-tier provider, it now fails with `auth_not_found`; remove that group to restore flat scheduling, or replace it with an explicit `classify:<name>` group.

#### What “custom field name” actually reads

It is not an arbitrary auth-JSON field selector. The exact runtime mapping is:

| UI field | Runtime value |
|----------|---------------|
| `filename` | `SchedulerAuthCandidate.ID` — CPA's **Auth ID**, not the display FileName |
| `id` | Exactly the same as `filename` |
| `provider` | The candidate's runtime provider; openai-compat may use the internal `openai-compatible-<name>` key |
| Any other lowercase field | `SchedulerAuthCandidate.Attributes[field]` |

Custom names are trimmed and lowercased, then looked up as an exact Attribute key. They do **not** read `Auth.Metadata` / `Candidate.Metadata`, and adding an arbitrary top-level field to an auth JSON file does not automatically make it classifiable. Common—but source/version-dependent—safe Attributes include:

```text
plan_type  tier  auth_kind  note  priority  weight  websockets
path  source  source_backend  config_index  runtime_only
codex_alpha_search  plugin_virtual  virtual_source  auth_index_seed
```

Actual keys depend on the credential source, provider plugin, and CPA version. Fields such as `email` or `project_id` may exist only in Metadata/Label and must not be assumed to reach the Scheduler. CPA also removes Attributes whose names contain any of these sensitive fragments, so they cannot drive runtime classification:

```text
api_key  apikey  token  secret  cookie  credential  password
storage  authorization  auth_header  proxy_url
```

Example by credential kind (only when the candidate really exposes `auth_kind`):

```json
{
  "name": "oauth-only",
  "field": "auth_kind",
  "pattern": "^oauth$",
  "group": "oauth-only",
  "enabled": true
}
```

Example using `note` (only when the real Scheduler candidate exposes that Attribute; the management page may show a Metadata fallback instead):

```json
{
  "name": "tenant-a-note",
  "field": "note",
  "pattern": "^tenant-a$",
  "group": "tenant-a",
  "enabled": true
}
```

Patterns use Go RE2: matching is case-sensitive and substring-based by default; use `^...$` for a full value and `(?i)^...$` for case-insensitive matching. RE2 does not support lookahead, lookbehind, or backreferences. The Web UI now validates with the server's RE2 engine on save instead of substituting JavaScript RegExp semantics.

The Web UI shows match counts only for `filename` / `id`, `provider`, `plan_type`, `tier`, `path`, and `weight`. CPA's `/auth-files` response may synthesize management fields such as `note`, `priority`, and `websockets` from Metadata even when the Scheduler candidate has no same-named Attribute. Those and other custom fields therefore show “Preview unavailable”; a value visible on the management page is not runtime-classification proof. Preview also ignores credential health, model capability, priority pre-filtering, and which Scheduler plugin wins. Verify the final isolation boundary with the `auth_id` from a real request.

For a strict file allow-list, prefer `filename` and anchor the exact `id` values returned by `/v0/management/auth-files`:

```json
{
  "name": "tenant-a-files",
  "field": "filename",
  "pattern": "^(?:tenant-a/codex-01\\.json|tenant-a/codex-02\\.json)$",
  "group": "tenant-a",
  "enabled": true
}
```

Configure classify rules in the UI, or via management API (`/classify-rules`, `/classify-preview`, `/catalog`). You do not need to hand-edit state JSON for normal use.

### CPA-native top-level key bindings

Native-key bindings are deliberately separate from plugin-owned downstream keys:

| Key type | Authentication owner | What this plugin enforces |
|----------|----------------------|---------------------------|
| Plugin key (`cpa_…` or a plugin-owned custom `sk-…`) | `cpa-access-guard` | model/alias policy, RPM, budgets, billing, and optional credential group |
| CPA top-level `api-keys` entry | CPA's built-in config API-key provider | **auth-file group only**; no automatic model/RPM/budget policy |

After CPA authenticates a native key, it derives a stable one-way `caller_scope`. The plugin persists only that scope, a redacted preview, and the target group — never the plaintext top-level key:

```text
top-level api-key → CPA native auth → caller_scope
                  → native binding → classify group
                  → pick only an auth ID in that group
```

Requirements and behavior:

- Requires CLIProxyAPI **v7.2.101 or newer**, where Scheduler metadata includes `caller_scope`.
- The Web UI loads the current top-level list from CPA's Management-key-protected `GET /v0/management/api-keys` endpoint and shows every key as a redacted row, including keys with no binding. A binding whose key was removed from the host remains visible as an orphan record.
- Selecting an unbound row avoids manual copy/paste. The plaintext stays in page memory, is sent only in Management-authenticated JSON request bodies for exact scope matching and binding creation, and is never rendered, placed in a URL, stored in browser storage, persisted, or returned by plugin APIs.
- The key must remain in CPA's top-level `api-keys`; a binding is authorization metadata, not authentication.
- A bound, enabled key with no usable candidate in its group fails closed with `auth_not_found` (503). It never falls back outside the group.
- When a caller scope matches an enabled native binding, that binding takes precedence over generic Scheduler `group` metadata.
- Unbound native keys and disabled bindings keep CPA's existing unrestricted scheduling behavior.
- Top-level config and plugin state are not updated atomically. For a strict-isolation rotation, create a second temporary binding for the new key, add and test that key in CPA, remove the old top-level key, and only then delete the old binding.
- Use a high-entropy native key and do not reuse the same plaintext as a plugin-owned key or another authentication principal. CPA's `caller_scope` identifies the principal text, but does not include the authentication-provider name.
- CPA activates only the highest-priority Scheduler plugin. `cpa-access-guard` must be the winning Scheduler for isolation to run.
- CPA may narrow candidates to the highest auth-priority tier before invoking the plugin. Keep isolated pools at compatible priorities so an out-of-group high-priority auth does not hide the intended pool and cause `auth_not_found`.
- Native-key isolation is enforced only on CPA's normal AuthManager / `scheduler.pick` path when the request forwards `caller_scope` to the Scheduler. **CPA Home scheduling currently bypasses plugin Scheduler selection**; do not start CPA with `-home-jwt` when relying on these bindings. Direct plugin-executor, Alpha Search (`/v1/alpha/search` and `/backend-api/codex/alpha/search`), all Codex Live/Realtime routes (`/v1/live*` and `/v1/realtime*`), and any other route that bypasses `scheduler.pick` or omits `caller_scope` are unsupported.
- Explicitly set CPA's top-level `quota-exceeded.antigravity-credits: false`. When enabled, that last-resort Antigravity credits path bypasses plugin Scheduler selection and enumerates Antigravity credentials directly, so a request can land outside the bound group. The current plugin ABI cannot enforce a binding inside that fallback.
- If this plugin is disabled, unloaded, or fails to load, CPA may still accept the top-level key and use default scheduling. A host-enforced required-policy/fail-closed mechanism is needed if plugin failure itself must never bypass the boundary.
- Back up `state_file` before downgrading below v0.5.0. Older releases do not understand `native_key_bindings` and can discard them when rewriting state.

For normal operation, create bindings through this plugin's Management API or Web UI. A first-boot YAML seed necessarily contains `caller_scope`; CPA's generic, Management-key-protected plugin-config endpoint can return that YAML field. The dedicated native-binding list API and UI never return it.

Bindings use the same group syntax as alias targets: built-ins such as `free`, `team`, `plus`, or `supported`, and `classify:<name>` for custom groups. For an exact file allow-list, create an anchored `filename` classify rule first. Despite that historical field name, it matches the Scheduler **auth ID**, which may include a relative directory and may differ from the UI display name.

### OpenAI-compatibility providers

Channels under CPA `openai-compatibility` (e.g. a named proxy) use the **channel name** as `provider`. The plugin maps it to CPA’s internal key `openai-compatible-<name>` when routing. Models must be listed on that channel in CPA config, or the host reports no auth for that model.

---

## Capabilities (plugin hooks)

| Hook | Role |
|------|------|
| Frontend auth | Know plugin keys; enforce alias allow-list, RPM, budget; stamp route + group metadata |
| Model router | Alias → provider + target model |
| Scheduler | When `group` is set, filter auth candidates by tier / `classify:` group |
| Response interceptor | Non-stream JSON: rewrite top-level `model` back to the alias |
| Usage | Token / per-call billing into the state file |
| Management API + embedded Web UI | Keys, native-key bindings, aliases, classify rules, status |

---

## Build

Linux `.so` needs cgo and a matching toolchain:

```bash
make test
make build-linux          # builds web UI, then linux amd64/arm64 .so
# or
make web-build
mkdir -p dist/linux/amd64
GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -buildvcs=false -tags cshared \
  -buildmode=c-shared -o dist/linux/amd64/cpa-access-guard.so ./cmd/cpa-access-guard
```

On Windows, build the `.so` via WSL/Linux. `go test ./...` uses a non-cgo stub so unit tests run without a shared-library toolchain.

Copy the library into CPA's matching platform directory, for example
`plugins/linux/amd64/cpa-access-guard.so`, and enable the plugin in config.
The library basename must be `cpa-access-guard` (optionally
`cpa-access-guard-v<version>`); putting `_linux_amd64` in the basename changes
the plugin ID and prevents `plugins.configs.cpa-access-guard` from matching it.

---

## Config

Minimal shape (see also [`config.example.yaml`](./config.example.yaml)):

```yaml
quota-exceeded:
  # Required for native-key auth-file isolation. The Antigravity credits
  # fallback bypasses plugin Scheduler selection when enabled.
  antigravity-credits: false

plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-access-guard:
      enabled: true
      priority: 100 # must beat other Scheduler plugins for credential isolation
      state_file: "cpa-access-guard-state.json"
      pricing_file: "cpa-access-guard-model-pricing.json"
```

Notes:

- If `state_file` exists, it is the source of truth for keys / native-key bindings / aliases / classify rules / usage.
- `pricing_file` is the standalone model price catalog (USD per 1M tokens). Billing uses alias prices first and falls back to this catalog. Omit it to place `cpa-access-guard-model-pricing.json` next to `state_file`.
- The plugin refreshes the catalog (and matching alias prices) from [models.dev](https://models.dev) at startup and on a timer. Optional `pricing_sync.interval_hours` / `pricing_sync.url` override the default 24h public catalog.
- A relative `state_file` / `pricing_file` is resolved against the **CPA process working directory**, not the plugin `.so` directory. Use an absolute path in production and ensure the CPA user can write it. `GET /v0/management/plugins/cpa-access-guard/status` reports the resolved `state_file` and `pricing_file`.
- When upgrading from `cpa-key-policy` while relying on its default relative state filename, Access Guard copies a valid sibling `cpa-key-policy-state.json` to `cpa-access-guard-state.json` once. The legacy file is retained as a recovery backup. Explicit `state_file` paths are never auto-migrated; copy those files deliberately while CPA is stopped.
- The plugin creates the state directory with mode `0700` and atomically writes the file with mode `0600`. Back it up, and never place a plaintext top-level API key in it manually.
- Prefer creating keys, native bindings, and aliases in the **Web UI** or Management API; seed YAML is mainly for first boot.
- Never commit real key hashes, management secrets, or live host URLs into public docs.

---

## Web Management UI

Embedded in the plugin. After load, open:

```text
http://<your-cpa-host>:<api-port>/v0/resource/plugins/cpa-access-guard/index.html
```

Login with CPA **management** secret (`remote-management.secret-key` / management password). The secret stays in memory only (not `localStorage`); refresh → re-login.

UI areas:

| Tab / page | Use for |
|------------|---------|
| Keys | Create / edit / rotate / delete keys; bind models or aliases; RPM & budgets |
| Mapping → Aliases | Global multi-target aliases, dispatch, pricing |
| Mapping → Classification | Custom credential groups + match preview |
| Mapping → Native key bindings | List every CPA top-level key; configure built-in or `classify:` auth-file groups; retain orphan bindings for review |
| Model picker | Catalog of providers; tier / **Custom · …** subgroups |

Dev UI without rebuilding the `.so`:

```bash
cd web
npm install
VITE_CPA_BASE=http://127.0.0.1:8317 npm run dev
```

---

## Management API (summary)

Exact paths (no path templates). Auth: CPA management bearer token.

**Keys**

- `GET/POST/PATCH/DELETE …/keys` (`id` in query or body for mutate)
- `POST …/keys/rotate?id=…`
- `POST …/keys/reset-rpm?id=…`
- `GET …/keys/usage?id=…`
- `GET …/status`

**Aliases**

- `GET/POST/DELETE …/aliases`

**Classify**

- `GET/POST/DELETE …/classify-rules`
- `POST …/classify-rules/reorder`
- `POST …/classify-preview` — de-duplicated group unions plus per-rule `rule_matches`; explicit draft rules are validated strictly by server-side Go/RE2
- `POST …/catalog` — body: auth-file credentials + models; response: picker `entries` with `classify:` groups

**CPA-native key bindings**

- `GET/POST/PATCH/DELETE …/native-key-bindings`
- `POST …/native-key-bindings/catalog` — Management-UI helper: accepts the current `api_keys` array in a JSON body, matches bindings by exact `caller_scope`, and returns only redacted entries plus orphan bindings. It never returns the supplied keys or caller scopes.

Create a binding (the plaintext key appears only in this request; neither it nor the full caller scope is returned):

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-access-guard/native-key-bindings" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "client-a-native",
    "name": "Client A",
    "key": "sk-replace-with-an-existing-top-level-api-key",
    "enabled": true,
    "group": "classify:client-a"
  }'
```

Rotate the key or change the binding:

```bash
curl -X PATCH "$CPA/v0/management/plugins/cpa-access-guard/native-key-bindings" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "client-a-native",
    "key": "sk-the-new-top-level-key",
    "group": "classify:client-a",
    "enabled": true
  }'
```

Omit `key` (or send an empty string) in PATCH to keep the existing scope. Directly replacing `key` changes the bound scope immediately and is not atomic with CPA's top-level configuration. For strict isolation, use this safe sequence instead:

1. Create a second temporary binding for the new key and the same group.
2. Add the new key to CPA's top-level `api-keys`.
3. Send a real request with the new key and verify the selected `auth_id` belongs to the group.
4. Remove the old key from CPA's top-level `api-keys`.
5. Delete the old binding.

Create key (plain key returned **once**):

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-access-guard/keys" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "team-a",
    "name": "Team A",
    "rpm": 60,
    "models": [
      {"alias":"fast","provider":"codex","target_model":"gpt-5.4-mini","group":"free"}
    ]
  }'
```

Create a multi-target alias:

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-access-guard/aliases" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "alias": "cheap-chat",
    "dispatch": "priority",
    "billing_mode": "tokens",
    "targets": [
      {"provider":"cerebras","target_model":"gpt-oss-120b"},
      {"provider":"codex","target_model":"gpt-5.4-mini","group":"free"}
    ]
  }'
```

---

## Client request behavior

| Case | Result |
|------|--------|
| Known key + allowed alias | Auth OK → route → optional group filter → upstream |
| Known key + unknown model | Auth rejected |
| RPM / budget exceeded | Rejected |
| Group set, no matching auth file | `auth_not_found` / unavailable (no cross-tier leak) |
| Bound native key + usable in-group auth | Only an auth ID in that group is selected |
| Bound native key + no in-group auth | `auth_not_found` / 503; no out-of-group fallback |
| Unbound native key or disabled binding | Existing CPA unrestricted scheduling behavior |
| Unknown key | Plugin declines; CPA may try native `api-keys` |
| Non-stream chat response | Top-level `model` rewritten to alias |
| Stream | Body not rewritten (v1) |

### `/v1/models` on CPA main port

Per-key `allow_models_endpoint`: **binary** — deny (401) or full global list. CPA cannot filter that list per plugin key on the main port.


---

## Setup checklist

1. Build / install the `.so` into CPA `plugins.dir`.
2. Enable `plugins` + `cpa-access-guard` in CPA config; set `state_file`.
3. Open the Web UI with the management secret.
4. (Optional) Define **classify rules** if you need custom credential buckets.
5. (Optional) Bind existing CPA top-level native keys to those groups.
6. Create **aliases** (multi-target / pricing) and/or pick models per plugin key (with tier or Custom group).
7. Create plugin keys, save the one-time `plain_key`, and hand them out to clients.
8. Client: OpenAI-compatible base URL = CPA; `Authorization: Bearer cpa_…`; `model` = alias name.
9. Ensure openai-compat channels list the models you map; empty model lists → host “no auth” errors.

---

## Tests

```bash
go test ./...
cd web && npm test && npm run build
```
