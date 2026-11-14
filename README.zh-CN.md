# Access Guard（中文说明）

把 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的客户端 API 密钥（`access.api-keys`，旧版配置是顶层 `api-keys`）绑定到指定凭证，并按 Key 限制可用模型、RPM / 美元额度，统计用量。

| | |
|---|---|
| **仓库** | [berry-shake/cpa-access-guard-plugin](https://github.com/berry-shake/cpa-access-guard-plugin) |
| **协议** | MIT |
| **安装** | 从 [GitHub Releases](https://github.com/berry-shake/cpa-access-guard-plugin/releases) 下载对应平台的插件包 |
| **沿袭** | 基于 MIT 协议的 [origin652/cpa-plugin-key-policy](https://github.com/origin652/cpa-plugin-key-policy) 演进而来 |
| **English** | [README.md](./README.md) |

## v0.4.4-fork.26：只保留 API 密钥绑定

fork.26 删除了「下游 Key」「别名映射」「凭证归类」，API 密钥只能绑定**指定凭证**：

- 插件不再注册前端认证、模型路由和响应拦截。CPA 不再把完整请求体（以及非流式响应体）发给插件，每个请求只剩一次很小的 `scheduler.pick` 和一次 `usage.handle`。
- `scheduler.pick` 只解码候选凭证的 ID、供应商、优先级和状态；用量有变化才写入 `state_file`，不再每 15 秒写一次；价格目录按流解析，同步结束立即归还内存；改用 Go 1.26 编译。
- 首次启动时状态文件会去掉 `keys`、`aliases`、`classify_rules`，已删除的下游 Key 的用量一并清掉。来自别名的价格行保留为受保护的手工行。
- 旧版按凭证组（内置等级或 `classify:`）保存的绑定仍能加载，但在重新选择凭证前**拒绝全部请求（503）**，页面会在提示旁显示原来的组名。批量历史里要恢复成凭证组的操作会被拒绝（`group_unsupported`）。
- 回退到 fork.25 时，指定凭证的绑定照常生效：编码后的 fail-closed 组仍会写入。

## 工作方式

```text
API 密钥 → CPA 自身认证 → caller_scope（SHA-256，不可逆）
            → 绑定 → model_access 且 auth_ids → RPM / 美元额度
            → 只在绑定的凭证里选
```

插件只保存 caller scope、打码的 Key 预览和策略，从不保存明文 Key。Key 仍写在 CPA 的 `api-keys` 里；绑定只做授权，不做认证。

| 请求 | 结果 |
|---|---|
| 已绑定，模型允许，有可用的绑定凭证 | 选优先级最高的绑定凭证（同优先级取 ID 最小），开启轮询时轮流选 |
| 已绑定，模型不在 `model_access` 内 | `model_not_allowed` / 403，不消耗 RPM |
| 已绑定，超出 RPM / 日 / 周美元额度 | `quota_exceeded` / 429 |
| 已绑定，没有可用的绑定凭证 | `auth_not_found` / 503，绝不落到绑定以外 |
| 已绑定，旧版凭证组绑定 | `auth_not_found` / 503，重新选择凭证后恢复 |
| 未绑定或绑定已停用 | 走 CPA 自己的调度 |

- `model_access.mode: all` 允许现有和以后新增的模型；`allowlist` 只允许列出的供应商 + 模型（`(high)` 这类思考后缀沿用基础模型的权限）。空的允许列表拒绝一切。页面上新建的绑定默认是空允许列表。
- **并发轮询**（`round_robin`，默认关）在优先级最高的可用绑定凭证之间轮流，每个 Key、供应商池、模型各有一个游标。想让所有凭证都参与，就把它们设成同一优先级。
- 在支持 `scheduler_across_priorities` 的 CPA 上，插件能拿到所有优先级的候选，先按绑定过滤再选优先级。
- `auth_ids` 是区分大小写的 Scheduler 凭证 ID（认证文件，以及 Codex API Key 等 AI 提供商供应）。找不到的 ID 会继续显示；全部失效时请求一律拒绝。
- 用量按 CPA `usage.handle` 给的 token 数和模型价格表计费（先查上游模型，再查请求的模型）。没有价格的模型照样计调用次数和 token，金额记 0。
- 周额度从最近一次手动重置起按七天周期滚动；日用量在 UTC 零点清零。

**要求与限制**

- CLIProxyAPI **v7.2.101 或更新**（Scheduler 元数据带 `caller_scope`）。
- Access Guard 必须是优先级最高的 Scheduler 插件。
- 设置 `quota-exceeded.antigravity-credits: false`，否则这条兜底路径会绕过插件调度。
- 不经过 `scheduler.pick` 或不带 `caller_scope` 的路径不受约束：Home 调度（`-home-jwt`）、直接插件执行器、Alpha Search、Codex Live/Realtime。
- 插件被停用、卸载或加载失败时，CPA 仍可能用默认调度接受这把 Key。
- `/v1/models` 无法按 Key 过滤，实际执行在 `scheduler.pick` 拦截。
- CPA 配置和插件状态是两套系统。严格轮换时：先为新 Key 建绑定，再加进 CPA、测试，然后删掉旧 Key，最后删旧绑定。

## 管理页

```text
http://<cpa-host>:<port>/v0/resource/plugins/access-guard/index.html
```

用 CPA 管理密钥登录。嵌在同源的 CPA 或 CPAMP 面板里且勾选了「记住密码」时，会直接沿用面板保存的登录（支持 `enc::v1::` 和两种 `enc::v2::`）。

- **API 密钥**：CPA 当前所有客户端 API 密钥以打码卡片显示（需要时可复制完整 Key）；绑定编辑器带凭证和模型选择；显示账号邮箱和 Codex 套餐、7 天额度条；可从 CPA Key 备注同步名称；批量绑定支持预览、撤销和历史（保留 50 条）；批量重置额度。找不到 Key 的孤立绑定也会显示。
- **模型价格**：LiteLLM（主）和 models.dev（补充）同步状态、搜索、手工覆盖、删除墓碑、恢复自动。

## 配置

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
      # pricing_file: 默认放在 state_file 旁边
      # pricing_sync: {interval_hours: 24, litellm_url: ..., models_dev_url: ...}
```

- `state_file` 一旦存在，就以它为准（绑定、历史、用量）；YAML 里的 `native_key_bindings` 只在首次启动时导入（见 [`config.example.yaml`](./config.example.yaml)）。建议在页面或接口里建绑定。
- 相对路径按 CPA 的工作目录解析。文件以 `0600` 原子写入。`GET …/status` 显示实际路径。
- 价格在启动时和之后每 24 小时同步一次。某个来源失败时保留上次的值并标为过期。手工修改优先于两个来源。

## 管理接口

前缀 `/v0/management/plugins/access-guard`，使用 CPA 管理密钥（Bearer）。

| 路由 | 用途 |
|---|---|
| `GET /status` | 运行状态和实际文件路径 |
| `GET/POST/PATCH/DELETE /native-key-bindings` | 列出 / 新建 / 修改 / 删除（`id` 放在 body 或 query） |
| `POST /native-key-bindings/catalog` | 用当前 `api_keys` 匹配绑定，只返回打码条目和孤立绑定 |
| `POST /native-key-bindings/sync-names` | 把 CPA Key 备注同步为绑定名称 |
| `POST /native-key-bindings/reset-quota`、`/reset-quota-batch` | 重置单个 / 选中绑定的 RPM 和用量 |
| `GET /native-key-bindings/history` | 批量与恢复历史 |
| `POST /native-key-bindings/batch-preview`、`/batch` | 预览 / 提交批量替换凭证 |
| `POST /native-key-bindings/rollback-preview`、`/rollback` | 预览 / 提交历史恢复 |
| `GET/POST/DELETE /pricing`、`POST /pricing/restore-auto` | 模型价格表 |
| `GET /pricing-sync`、`POST /pricing-sync/run` | 同步状态 / 立即同步 |

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

`key` 只用来算 caller scope，不会返回。PATCH 时不带 `key` 就保留原来的 scope；非空的 `auth_ids` 会替换凭证。传 `group` 返回 `400 group_unsupported`。

## 构建

```bash
make test
make build-linux   # 先构建管理页，再编 linux amd64/arm64 .so
```

把库文件放到 `plugins/<os>/<arch>/access-guard.so`，文件名必须保持 `access-guard`，`plugins.configs.access-guard` 才能对上。`go test ./...` 使用非 cgo 的桩。前端开发：`cd web && npm install && VITE_CPA_BASE=http://127.0.0.1:8317 npm run dev`。
