# CPA Access Guard（中文说明）

面向 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的**下游 API Key 策略插件**。

用人话说：你可以给客户发自己的 `cpa_…` 钥匙。每把钥匙只能用你允许的模型，还能限速、限额，并转到 CPA 真实上游（Codex、Claude、openai-compatibility 通道等）。CPA 自带的顶层 `api-keys` 也可以单独绑定到指定认证文件组；**不要把插件下发的 key 再写进 `api-keys`**，否则会绕过插件 Key 的模型、RPM 和额度策略。

| | |
|---|---|
| **仓库** | [berry-shake/cpa-access-guard-plugin](https://github.com/berry-shake/cpa-access-guard-plugin) |
| **协议** | MIT |
| **安装** | 首个 CPA Access Guard GitHub Release 发布前请从源码编译 |
| **沿袭** | 基于 MIT 协议的 [origin652/cpa-plugin-key-policy](https://github.com/origin652/cpa-plugin-key-policy) 演进而来 |
| **English** | [README.md](./README.md) |

---

## 它能干什么

1. **发钥匙** — 批量创建下游 key，每把绑定可用模型 / 别名。
2. **做映射** — 客户端写 `model: fast`，插件转到例如 `codex` + `gpt-5.4-mini`。
3. **做限制** — 单 key 的 RPM、可选每日/每周美元额度，按 token 或按次计费。
4. **凭证分档 / 归类** — 请求可以钉死在 Codex free/team 等内置档，或你自定义的归类组，**不会串到别的凭证文件**。
5. **多目标别名** — 一个别名挂多个后端（优先 或 轮询）。
6. **原生 Key 绑定** — 保留 CPA 顶层 `api-keys` 鉴权，同时限制每把 Key 能使用的认证文件。
7. **网页管理** — 在 CPA 里管 key、原生 Key 绑定、全局别名、凭证归类。

---

## 核心概念

### 下游 Key

插件自己发的密钥（`cpa_…`），只由本插件鉴权。上面可以配置：

- 允许的 **模型** 和/或 **别名**
- RPM
- 每日 / 每周美元上限（可选）
- 是否允许主端口访问 `/v1/models`（见下文）

### 别名（全局映射表）

可复用的名字，例如 `fast`，展开成一条或多条 **目标**：

| 字段 | 含义 |
|------|------|
| `provider` | CPA 提供商标识（`codex`、`claude`，或 openai-compatibility 的 **name**，如 `cerebras`） |
| `target_model` | 上游真实模型 id |
| `group` | 可选，限制用哪一类凭证（见下节） |
| `dispatch` | `priority`（始终尝试第一个）或 `round-robin`（轮询） |
| 计费 | `tokens`（百万 token 单价）或 `per_call`（每次固定金额） |

Key 可以**引用**别名，不必重复填目标。多目标别名会展开成多条同名规则；**同一次请求**里鉴权与路由共用同一次选择，保证 `group` 与真实目标一致。

### 凭证组：内置档位 + 自定义归类

| 类型 | 选择器里长什么样 | 写进映射的 group |
|------|------------------|------------------|
| **内置档**（Codex `plan_type`、Antigravity `tier`） | 如「免费档 / Team」 | 裸名：`free`、`team`、`supported` |
| **自定义归类** | 如 **「自定义 · vip」** | 带前缀：`classify:vip` |

**正常 Scheduler 路径的运行时规则：** 映射里写了 group，调度就**只**在该组凭证里选文件。没有可用文件 → 直接失败（`auth_not_found`），不会由本插件落到其他档。CPA 的 Antigravity credits 特殊回退会绕过 Scheduler，因此严格隔离必须同时设置 `quota-exceeded.antigravity-credits: false`。

**自定义归类**（网页 → 映射 → 凭证归类）：

- 用正则匹配凭证字段（`filename`、`provider`、`plan_type`、`tier` 等）。
- 规则上保存你起的**组名**（裸名）。
- 目录与映射使用 `classify:组名`，避免和内置的 `free`/`team` 撞名。
- 一个文件可命中**多个**自定义组（选择器里每组都会出现）。
- 多条规则按并集合并；同一个组写多条规则会扩大白名单。严格隔离时应使用一条锚定 Auth ID 的正则，并避免重叠规则误收文件。
- 没命中自定义规则 → Codex/Antigravity 走内置档；其它 auth-file 渠道默认扁平（无组）。
- openai-compat / API Key 类通道保持**扁平**，不拆组。

升级兼容提醒：v0.5.0 起，`supported` 只属于 Codex/Antigravity 的内置未识别档。若旧配置曾给 Claude、Gemini、OpenAI-compatible 等非内置分档 provider 手工写 `group: supported`，升级后会返回 `auth_not_found`；删除该 group 可恢复扁平调度，或改为明确的 `classify:<组名>`。

#### “自定义字段名”到底读取什么

它不是任意认证 JSON 字段选择器。运行时的准确映射是：

| 网页字段名 | 实际读取值 |
|------------|------------|
| `filename` | `SchedulerAuthCandidate.ID`，也就是 CPA 的 **Auth ID**；不是展示用 FileName |
| `id` | 与 `filename` 完全相同 |
| `provider` | Scheduler 候选的运行时 provider；openai-compat 可能是内部名 `openai-compatible-<name>` |
| 其它小写字段名 | `SchedulerAuthCandidate.Attributes[field]` |

自定义字段名会先去首尾空白并转为小写，再精确查找同名 Attribute。它**不会**读取 `Auth.Metadata` / `Candidate.Metadata`，认证文件里随手增加一个 JSON 字段也不会自动变成可分类字段。常见但不保证存在的安全 Attributes 包括：

```text
plan_type  tier  auth_kind  note  priority  weight  websockets
path  source  source_backend  config_index  runtime_only
codex_alpha_search  plugin_virtual  virtual_source  auth_index_seed
```

实际键由凭证来源、provider 插件和 CPA 版本决定。`email`、`project_id` 等字段有时只存在于 Metadata/Label，不能假定 Scheduler 一定会传。CPA 还会过滤键名包含以下片段的敏感 Attributes，因此它们不能用于运行时归类：

```text
api_key  apikey  token  secret  cookie  credential  password
storage  authorization  auth_header  proxy_url
```

例如按 OAuth/API-key 类型归类（仅当当前凭证候选确实带 `auth_kind`）：

```json
{
  "name": "oauth-only",
  "field": "auth_kind",
  "pattern": "^oauth$",
  "group": "oauth-only",
  "enabled": true
}
```

按 `note` 归类（仅当真实 Scheduler 候选确实带同名 Attribute；管理页显示的 note 可能只是 Metadata 回退）：

```json
{
  "name": "tenant-a-note",
  "field": "note",
  "pattern": "^tenant-a$",
  "group": "tenant-a",
  "enabled": true
}
```

正则由 Go RE2 执行：默认区分大小写，`MatchString` 默认是子串匹配；完整值必须写 `^...$`，忽略大小写可写 `(?i)^...$`。RE2 不支持 lookahead、lookbehind 或反向引用。网页保存时也由服务端 RE2 校验，不再用 JavaScript RegExp 代替。

网页只对 `filename` / `id`、`provider`、`plan_type`、`tier`、`path`、`weight` 显示命中计数。CPA 的 `/auth-files` 管理响应可能从 Metadata 回退生成 `note`、`priority`、`websockets` 等字段，即使 Scheduler 候选没有同名 Attribute；这些字段及其它自定义字段因此显示“无法预览”，不能把管理页看到的值当作运行时分类证据。预览也不检查凭证状态、模型能力、priority 预筛选或实际获胜的 Scheduler。最终隔离结果必须用真实请求日志中的 `auth_id` 验证。

严格认证文件白名单仍推荐 `filename`，按 `/v0/management/auth-files` 返回的 `id` 写完整锚定正则，例如：

```json
{
  "name": "tenant-a-files",
  "field": "filename",
  "pattern": "^(?:tenant-a/codex-01\\.json|tenant-a/codex-02\\.json)$",
  "group": "tenant-a",
  "enabled": true
}
```

一般在网页或管理 API 配置即可，不必手改 state JSON。

### CPA 顶层原生 Key 绑定

原生 Key 绑定与插件自己发行的下游 Key 是两套独立语义：

| 类型 | 谁负责鉴权 | 本插件负责什么 |
|------|------------|----------------|
| 插件 Key（`cpa_…` 或插件托管的自定义 `sk-…`） | `cpa-access-guard` | 模型 / Alias、RPM、额度、计费和可选凭证组 |
| CPA 顶层 `api-keys` | CPA 原生 config API-key provider | **只限制认证文件组**；不自动增加模型、RPM 或额度策略 |

CPA 原生鉴权成功后会产生稳定、不可逆的 `caller_scope`。插件只保存这个 scope、脱敏预览与目标组，不保存顶层 Key 明文：

```text
顶层 api-key → CPA 原生鉴权 → caller_scope
             → native binding → classify group
             → 只从组内 Auth ID 选择认证文件
```

要求与行为：

- 需要 CLIProxyAPI **v7.2.101 或更高版本**，因为 Scheduler 必须收到 `Options.Metadata.caller_scope`。
- 网页通过受 Management Key 保护的 `GET /v0/management/api-keys` 读取 CPA 当前顶层列表，默认以脱敏形式列出每一把 Key，包括尚未绑定的 Key；已经从宿主删除、但 state 中仍有绑定的 Key 会保留为“孤立绑定”供检查。
- 从未绑定行直接创建时无需手动复制粘贴。明文只停留在页面内存，并且只通过受 Management 鉴权的 JSON 请求体用于精确 scope 匹配和创建绑定；不会渲染到页面、放进 URL、写入浏览器存储或插件 state，也不会由插件 API 返回。
- 仍可手动新建绑定：粘贴原生 Key 一次；明文只用于计算 scope 和预览，不会写入 state，也不会在 API 响应中回显。
- Key 必须仍然存在于 CPA 顶层 `api-keys`；绑定本身不负责认证。
- 已绑定且启用的 Key 如果找不到组内可用凭证，会返回 `auth_not_found`（503），不会退回组外文件。
- `caller_scope` 命中启用的原生绑定时，绑定组优先于通用 Scheduler `group` 元数据。
- 未绑定或禁用绑定的原生 Key 保持 CPA 原来的自由调度行为。
- CPA 顶层配置与插件 state 不能原子更新。严格隔离的安全轮换方式是：先为新 Key 新建第二条临时绑定，再把新 Key 加入 CPA 并验证，随后删除旧顶层 Key，最后删除旧绑定。
- 顶层 Key 应使用高熵随机值，并且不要与插件托管 Key 或其它认证来源的 principal 重用同一明文。CPA 的 `caller_scope` 标识的是 principal 文本，不包含认证 provider 名称。
- CPA 只启用优先级最高的一个 Scheduler。必须确保 `cpa-access-guard` 是实际获胜的 Scheduler，否则文件隔离不会执行。
- CPA 可能在调用插件前先把候选缩到最高 auth priority 档。各隔离池应使用兼容的优先级，避免组外高优先级认证遮住目标池并触发 `auth_not_found`。
- 原生 Key 隔离只覆盖 CPA 正常的 AuthManager / `scheduler.pick` 且向 Scheduler 转发 `caller_scope` 的路径。**CPA Home 调度目前会绕过插件 Scheduler**；依赖这些绑定时不要用 `-home-jwt` 启动 CPA。直接 plugin-executor、Alpha Search（`/v1/alpha/search` 与 `/backend-api/codex/alpha/search`）、全部 Codex Live/Realtime 路由（`/v1/live*` 与 `/v1/realtime*`），以及其它绕过 `scheduler.pick` 或未转发 `caller_scope` 的路径均不受支持。
- 必须在 CPA 顶层配置中显式设置 `quota-exceeded.antigravity-credits: false`。启用后，该 Antigravity 最终额度回退会绕过插件 Scheduler，直接枚举 Antigravity 凭证，因此请求可能落到绑定组之外；当前插件 ABI 无法在这个回退阶段强制绑定。
- 插件被禁用、卸载或加载失败时，顶层 Key 仍可能由 CPA 原生鉴权并走默认调度；若需要“插件故障也绝不放行”的强安全边界，应让宿主增加必需策略/失败关闭机制。
- 降级到 v0.5.0 之前的版本前必须备份 `state_file`；旧版不认识 `native_key_bindings`，重新写 state 时可能丢弃这些绑定。

日常请通过本插件管理 API 或网页创建绑定。首次启动 YAML 种子必须包含 `caller_scope`，而 CPA 通用的插件配置接口会向持有 Management Key 的调用方返回该 YAML 字段；专用绑定列表 API 和网页不会返回它。

绑定目标使用与 Alias 相同的 group 格式：内置档写 `free` / `team` / `plus` / `supported`，自定义组写 `classify:组名`。严格文件白名单推荐先建一条锚定的 `filename` 归类规则；这里的 `filename` 实际匹配 Scheduler 的 **Auth ID**，可能包含相对目录，不一定等于页面显示的文件名。

### openai-compatibility 通道

CPA 里配置的兼容通道，映射时 `provider` 填通道 **name**。插件路由时会对应到主机内部的 `openai-compatible-<name>`。通道配置里的 **models 列表要写全**，否则主机会报「该模型无可用 auth」。

---

## 插件能力一览

| 钩子 | 作用 |
|------|------|
| 前端鉴权 | 识别插件 key；校验别名、RPM、额度；写入路由与 group 元数据 |
| 模型路由 | 别名 → provider + 目标模型 |
| 调度 | 有 group 时按档位 / `classify:` 过滤凭证 |
| 响应拦截 | 非流式 JSON：把顶层 `model` 改回别名 |
| 用量 | token / 按次计费写入 state |
| 管理 API + 内嵌网页 | Key、原生 Key 绑定、别名、归类、状态 |

---

## 编译

Linux `.so` 需要 cgo：

```bash
make test
make build-linux          # 先编前端，再编 linux amd64/arm64 .so
# 或
make web-build
mkdir -p dist/linux/amd64
GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -buildvcs=false -tags cshared \
  -buildmode=c-shared -o dist/linux/amd64/cpa-access-guard.so ./cmd/cpa-access-guard
```

Windows 上请用 WSL/Linux 编 `.so`。`go test ./...` 可用非 cgo stub，不依赖动态库工具链。

把动态库放进 CPA 对应平台目录，例如
`plugins/linux/amd64/cpa-access-guard.so`，并在配置里启用插件。动态库 basename
必须是 `cpa-access-guard`（也可用 `cpa-access-guard-v<版本>`）；不要把
`_linux_amd64` 写进 basename，否则 CPA 会把它识别成另一个插件 ID，无法命中
`plugins.configs.cpa-access-guard`。

---

## 配置

最小形态（完整示例见 [`config.example.yaml`](./config.example.yaml)）：

```yaml
quota-exceeded:
  # 原生 Key 的认证文件隔离必须关闭此项；Antigravity credits 回退会绕过插件 Scheduler。
  antigravity-credits: false

plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-access-guard:
      enabled: true
      priority: 100 # 必须高于其它 Scheduler，才能执行凭证隔离
      state_file: "cpa-access-guard-state.json"
      pricing_file: "cpa-access-guard-model-pricing.json"
```

说明：

- 若已有 `state_file`，则以其中的 keys / 原生 Key 绑定 / 别名 / 归类 / 用量为准。
- `pricing_file` 是独立的模型价格表（美元 / 百万 token）。计费优先用别名价格，未配价时回退到此表。省略则把 `cpa-access-guard-model-pricing.json` 放在 `state_file` 同目录。
- 插件会在启动时以及之后按间隔从 [models.dev](https://models.dev) 刷新价格表，并回写匹配的别名价格。可用 `pricing_sync.interval_hours` / `pricing_sync.url` 覆盖默认 24 小时和公共目录地址。
- 相对 `state_file` / `pricing_file` 会按 **CPA 进程当前工作目录**解析为绝对路径，不是相对插件 `.so`。生产环境建议写绝对路径，并确保 CPA 运行用户可写；可通过 `GET /v0/management/plugins/cpa-access-guard/status` 的 `state_file`、`pricing_file` 查看最终解析位置。
- 从 `cpa-key-policy` 升级且一直使用旧版默认相对文件名时，CPA Access Guard 会一次性把同目录中校验有效的 `cpa-key-policy-state.json` 复制为 `cpa-access-guard-state.json`；旧文件会保留作恢复备份。显式配置的 `state_file` 不会自动迁移，请先停掉 CPA，再人工、精确地复制。
- state 目录由插件按 `0700` 创建，文件按 `0600` 原子写入。请纳入备份，不要手工写入顶层 API Key 明文。
- 日常请用**网页**或管理 API 建 key、原生绑定和别名；YAML 种子数据主要用于首次启动。
- 公开文档里不要写真实管理密钥、主机名或凭证内容。

---

## 网页管理界面

插件内嵌。加载后访问：

```text
http://<你的-cpa-主机>:<api端口>/v0/resource/plugins/cpa-access-guard/index.html
```

用 CPA **管理密钥**登录（`remote-management.secret-key` 或管理密码）。密钥只放在内存，不写 `localStorage`；刷新页面需重新登录。

| 区域 | 用途 |
|------|------|
| Keys | 创建/编辑/轮换/删除 key；绑模型或别名；RPM 与额度 |
| 映射 → 别名 | 全局多目标别名、调度方式、定价 |
| 映射 → 凭证归类 | 自定义分组规则与命中预览 |
| 映射 → 原生 Key 绑定 | 列出全部 CPA 顶层 Key；配置内置档或 `classify:` 文件组；保留孤立绑定供检查 |
| 选模型 | 提供商目录；内置档 / **自定义 · …** 子组 |

不重编 `.so` 时开发前端：

```bash
cd web
npm install
VITE_CPA_BASE=http://127.0.0.1:8317 npm run dev
```

---

## 管理 API 摘要

路径为精确匹配。鉴权：CPA 管理 Bearer。

**Key：** `GET/POST/PATCH/DELETE …/keys`，以及 `rotate` / `reset-rpm` / `usage` / `status`  

**别名：** `GET/POST/DELETE …/aliases`  

**归类：**  

- `…/classify-rules`（含 reorder）  
- `POST …/classify-preview` — 返回去重后的组并集 `groups`，以及每条规则自己的 `rule_matches`；显式 draft 规则由服务端 Go/RE2 严格校验
- `POST …/catalog` — 前端提交 auth-file + 模型列表，返回带 `classify:` 的选择器条目  

**CPA 顶层原生 Key 绑定：**

- `GET/POST/PATCH/DELETE …/native-key-bindings`
- `POST …/native-key-bindings/catalog` — 管理网页辅助接口：通过 JSON 请求体接收当前 `api_keys` 数组，按精确 `caller_scope` 关联绑定，只返回脱敏条目与孤立绑定；绝不返回传入的 Key 或 caller scope。

创建绑定（Key 明文只在请求中出现一次；响应不返回明文或完整 caller scope）：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-access-guard/native-key-bindings" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "client-a-native",
    "name": "Client A",
    "key": "sk-替换为已经存在于顶层-api-keys-的真实-Key",
    "enabled": true,
    "group": "classify:client-a"
  }'
```

轮换 Key 或修改绑定：

```bash
curl -X PATCH "$CPA/v0/management/plugins/cpa-access-guard/native-key-bindings" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "client-a-native",
    "key": "sk-新的顶层-Key",
    "group": "classify:client-a",
    "enabled": true
  }'
```

PATCH 省略 `key` 或传空字符串时保留原 scope。直接替换 `key` 会立即改变绑定 scope，无法与 CPA 顶层配置原子更新；严格隔离时请改用以下顺序：

1. 为新 Key 和同一 group 创建第二条临时绑定。
2. 将新 Key 加入 CPA 顶层 `api-keys`。
3. 用新 Key 发真实请求，确认最终选择的 `auth_id` 属于目标组。
4. 从 CPA 顶层 `api-keys` 删除旧 Key。
5. 删除旧绑定。

创建 key（`plain_key` **只返回一次**）：

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

多目标别名示例：

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

## 客户端请求行为

| 情况 | 结果 |
|------|------|
| 认识的 key + 允许的别名 | 鉴权通过 → 路由 → 可选 group 过滤 → 上游 |
| 不允许的模型名 | 鉴权失败 |
| 超 RPM / 额度 | 拒绝 |
| 写了 group 但组内无可用凭证 | `auth_not_found` / 不可用（不串档） |
| 已绑定原生 Key + 组内有可用凭证 | 只选择该组 Auth ID |
| 已绑定原生 Key + 组内无可用凭证 | `auth_not_found` / 503（不回退组外） |
| 未绑定或禁用绑定的原生 Key | 保持 CPA 原生自由调度 |
| 不认识的 key | 插件放弃，CPA 可尝试原生 `api-keys` |
| 非流式对话响应 | 顶层 `model` 改回别名 |
| 流式 | v1 不改写 body |

### 主端口的 `/v1/models`

每 key 的 `allow_models_endpoint` 是**开关**：拒绝（401）或看**全局完整列表**。主端口无法按插件 key 过滤列表。


---

## 上手清单

1. 编译/安装 `.so` 到 CPA `plugins.dir`。
2. 启用 `plugins` 与 `cpa-access-guard`，配置 `state_file`。
3. 用管理密钥打开网页 UI。
4. （可选）配置**凭证归类**规则。
5. （可选）把已有 CPA 顶层原生 Key 绑定到这些组。
6. 建**别名**（多目标/定价）和/或给 key 勾选模型（含档位或「自定义 · …」）。
7. 创建插件 key，保存一次性 `plain_key`，发给客户。
8. 客户：OpenAI 兼容 base URL = CPA；`Bearer cpa_…`；`model` = 别名。
9. openai-compat 通道务必声明 models，否则会「无 auth」。

---

## 测试

```bash
go test ./...
cd web && npm test && npm run build
```
