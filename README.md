# Access Guard

Binds [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) client API keys (`access.api-keys`; top-level `api-keys` in older configs) to exact credentials, with per-key model access, RPM / USD limits and usage billing.

| | |
|---|---|
| **Repo** | [berry-shake/cpa-access-guard-plugin](https://github.com/berry-shake/cpa-access-guard-plugin) |
| **License** | MIT |
| **Install** | Download the archive for your platform from [GitHub Releases](https://github.com/berry-shake/cpa-access-guard-plugin/releases) |
| **Lineage** | Derived from [origin652/cpa-plugin-key-policy](https://github.com/origin652/cpa-plugin-key-policy) under the MIT license |
| **中文说明** | [README.zh-CN.md](./README.zh-CN.md) |

## v0.4.4-fork.26: API key bindings only

fork.26 removes **downstream keys**, **alias mapping** and **credential classification**, and binds API keys to **exact credentials** only:

- The plugin no longer registers frontend auth, the model router or the response interceptor. CPA stops sending full request bodies (and non-stream response bodies) to the plugin; each request costs one small `scheduler.pick` and one `usage.handle` call.
- `scheduler.pick` decodes only candidate ID / provider / priority / status. Usage is written to `state_file` only after it changed, instead of every 15 s. Pricing catalogs are decoded as a stream, and the memory used by a sync is returned right away. Built with Go 1.26.
- On first start the state file is rewritten without `keys`, `aliases` and `classify_rules`, and usage of removed downstream keys is dropped. Price rows that came from aliases stay as protected manual rows.
- A binding saved with a credential group (built-in tier or `classify:`) still loads but **rejects every request (503)** until you select credentials again; the page shows the old group next to the prompt. Batch history that would restore a group is refused (`group_unsupported`).
- Downgrading to fork.25 keeps exact-credential bindings working: their encoded fail-closed group is still written.

## How it works

```text
API key → CPA's own auth → caller_scope (SHA-256, one-way)
                  → binding → model_access AND auth_ids → RPM / USD quota
                  → pick only among the bound credentials
```

The plugin stores only the caller scope, a redacted key preview and the policy — never the plaintext key. Keys stay in CPA's `api-keys`; a binding is authorization, not authentication.

| Request | Result |
|---|---|
| Bound key, allowed model, usable bound credential | Highest priority bound credential (lowest ID on ties), or round robin when enabled |
| Bound key, model outside `model_access` | `model_not_allowed` / 403, no RPM spent |
| Bound key over RPM / daily / weekly USD | `quota_exceeded` / 429 |
| Bound key, no usable bound credential | `auth_not_found` / 503; never falls back outside the binding |
| Bound key, legacy group binding | `auth_not_found` / 503 until credentials are reselected |
| Unbound key or disabled binding | CPA's own scheduling |

- `model_access.mode: all` allows current and future models; `allowlist` allows only exact provider + model pairs (a thinking suffix like `(high)` inherits the base model). An empty allow-list denies everything. New bindings in the UI start with an empty allow-list.
- **Round robin** (`round_robin`, default off) rotates among the highest-priority usable bound credentials, with a cursor per key, provider pool and model. Give credentials equal priority to rotate across all of them.
- On CPA versions supporting `scheduler_across_priorities`, the plugin sees candidates from every priority tier and filters by the binding before choosing a priority.
- `auth_ids` are exact, case-sensitive Scheduler auth IDs (auth files and configured AI-provider supplies such as Codex API keys). Missing IDs stay visible; if all are gone, requests fail closed.
- Usage is billed from CPA's `usage.handle` token counts with the model price catalog (upstream model first, then the requested model). Unpriced models count calls and tokens at $0.
- Weekly quotas run in seven-day periods starting from the last manual reset; daily usage resets at UTC midnight.

**Requirements and limits**

- CLIProxyAPI **v7.2.101 or newer** (Scheduler metadata carries `caller_scope`).
- Access Guard must be the highest-priority Scheduler plugin.
- Set `quota-exceeded.antigravity-credits: false`; that fallback bypasses plugin scheduling.
- Not enforced on paths that skip `scheduler.pick` or omit `caller_scope`: Home scheduling (`-home-jwt`), direct plugin executors, Alpha Search, Codex Live/Realtime.
- If the plugin is disabled, unloaded or fails to load, CPA may still accept the key with default scheduling.
- `/v1/models` cannot be filtered per key; execution is enforced at `scheduler.pick`.
- CPA config and plugin state are separate. For strict rotation: bind the new key, add it to CPA, test it, remove the old key, then delete the old binding.

## Web UI

```text
http://<cpa-host>:<port>/v0/resource/plugins/access-guard/index.html
```

Log in with the CPA management key. Inside a same-origin CPA or CPAMP panel with **Remember password**, the saved login is reused (`enc::v1::` and both `enc::v2::` formats).

- **API keys** — every current CPA client API key as a redacted card (copy the full key on demand), binding editor with credential and model pickers, account emails and Codex plans, 7-day quota bar, name sync from CPA key notes, batch binding with preview / undo / history (50 records), batch quota reset. Orphan bindings stay visible.
- **Model pricing** — LiteLLM (primary) and models.dev (fallback) sync status, search, manual overrides, deletion tombstones and restore-automatic.

## Config

```yaml
quota-exceeded:
  antigravity-credits: false
plugins:
  enabled: true
  dir: "plugins"
  configs:
    access-guard:
      enabled: true
      priority: 100
      state_file: "/data/access-guard-state.json"
      # pricing_file: next to state_file by default
      # pricing_sync: {interval_hours: 24, litellm_url: ..., models_dev_url: ...}
```

- Once `state_file` exists it is the source of truth for bindings, history and usage; `native_key_bindings` in YAML only seeds the first boot (see [`config.example.yaml`](./config.example.yaml)). Prefer creating bindings in the UI or API.
- Relative paths resolve against CPA's working directory. Files are written atomically with mode `0600`. `GET …/status` shows the resolved paths.
- Pricing syncs at startup and every 24 h. A failed source keeps its last values marked stale. Manual edits win over both sources.

## Management API

Base `/v0/management/plugins/access-guard`, CPA management bearer token.

| Route | Purpose |
|---|---|
| `GET /status` | Runtime status and resolved file paths |
| `GET/POST/PATCH/DELETE /native-key-bindings` | List / create / update / delete (`id` in body or query) |
| `POST /native-key-bindings/catalog` | Match current `api_keys` to bindings; returns redacted entries and orphans |
| `POST /native-key-bindings/sync-names` | Copy CPA key notes into binding names |
| `POST /native-key-bindings/reset-quota`, `/reset-quota-batch` | Reset RPM and usage for one / selected bindings |
| `GET /native-key-bindings/history` | Batch and restore history |
| `POST /native-key-bindings/batch-preview`, `/batch` | Preview / commit a credential replacement |
| `POST /native-key-bindings/rollback-preview`, `/rollback` | Preview / commit a restore |
| `GET/POST/DELETE /pricing`, `POST /pricing/restore-auto` | Model price catalog |
| `GET /pricing-sync`, `POST /pricing-sync/run` | Sync status / run now |

```bash
curl -X POST "$CPA/v0/management/plugins/access-guard/native-key-bindings" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" -H "Content-Type: application/json" \
  -d '{
    "id": "client-a", "name": "Client A", "enabled": true,
    "key": "sk-an-existing-client-api-key",
    "auth_ids": ["tenant-a/codex-01.json", "tenant-a/codex-02.json"],
    "model_access": {"mode": "allowlist", "models": [{"provider": "codex", "model": "gpt-5.6-luna"}]},
    "weekly_usd": 100
  }'
```

The key is used once to derive the caller scope and is never returned. In PATCH, omit `key` to keep the scope; a non-empty `auth_ids` replaces the credentials. Sending `group` returns `400 group_unsupported`.

## Build

```bash
make test
make build-linux   # web UI, then linux amd64/arm64 .so
```

Copy the library to `plugins/<os>/<arch>/access-guard.so`; the basename must stay `access-guard` so `plugins.configs.access-guard` matches. `go test ./...` uses a non-cgo stub. Web dev server: `cd web && npm install && VITE_CPA_BASE=http://127.0.0.1:8317 npm run dev`.
