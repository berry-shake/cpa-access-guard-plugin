# Access Guard 请求过滤原理

本文说明 Access Guard `v0.4.4-fork.26` 如何处理 CPA 的客户端 API 密钥（`access.api-keys`，旧版配置是顶层 `api-keys`）。fork.26 起插件只做这一件事：下游 Key、别名映射和凭证归类都已删除，绑定只能指定具体凭证。

## 1. 插件注册了哪些钩子

| 钩子 | 用途 | 收到的数据 |
|---|---|---|
| `scheduler.pick`（含 `scheduler_across_priorities`） | 按绑定过滤候选凭证并选出一个 | 供应商、模型、`caller_scope`、候选凭证列表 |
| `usage.handle` | 请求完成后记账 | 模型名、明文 Key、token 数 |
| 管理接口 + 管理页 | 绑定、额度、价格 | 管理请求 |

插件不再注册前端认证、模型路由和响应拦截，所以 CPA 不会把请求体或响应体发给插件。每个请求只有上面两次调用。

## 2. `caller_scope`：API 密钥的不可逆身份

CPA 认证 API 密钥后算出：

```text
caller_scope = hex(SHA-256("cli-proxy-api:caller-scope:v1\x00" + trim(api-key)))
```

插件只保存 `caller_scope`、打码预览和策略。新建绑定时明文 Key 只用来算一次 scope，不落盘，也不在任何接口里返回。记账时 `usage.handle` 带来的明文 Key 同样只用来重新算 scope。

## 3. 未绑定或已停用

`scheduler.pick` 没有 `caller_scope`、找不到绑定、绑定已停用，或插件 `enabled: false` 时，插件返回 `Handled=false`，由 CPA 自己调度，会话粘滞等行为不变。用量也不记账。

`caller_scope` 存在但不是 64 位小写十六进制时，返回 `503 invalid_scheduler_metadata`，不当作未绑定处理。

## 4. 已绑定的处理顺序

```text
scheduler.pick
  ├─ 模型不在 model_access 内      → 403 model_not_allowed（不消耗 RPM）
  ├─ RPM / 日额度 / 周额度已用完    → 429 quota_exceeded
  ├─ 绑定没有 auth_ids（旧版凭证组） → 503 auth_not_found
  ├─ 过滤候选：
  │    状态不可用           → 排除
  │    ID 不在 auth_ids 内   → 排除
  │    该供应商不允许此模型  → 排除
  ├─ 没有剩余候选            → 503 auth_not_found（不退回 CPA）
  └─ 选出凭证：
       轮询关闭：优先级最高，同优先级取 ID 最小
       轮询开启：在最高优先级里轮流
```

### 4.1 模型权限

- `all`：现在和以后新增的模型都允许。
- `allowlist`：只允许列出的供应商 + 模型，大小写不敏感；`(high)` 这类思考后缀沿用基础模型的权限。空列表拒绝一切。
- 请求级先检查一次（任一请求供应商允许即可），每个候选再按自己的供应商检查一次，防止多供应商路由绕过白名单。

### 4.2 额度

顺序是 RPM、日美元额度、周美元额度，0 表示不限。超额统一返回 429 `quota_exceeded`，响应体是：

```json
{"error": {"type": "usage_limit_reached", "message": "The usage limit has been reached"}}
```

额度在请求前检查、请求后记账，所以语义是「已记录的用量达到限额后，阻止后续请求」：触线的那一笔通常仍会通过，并发请求也可能在记账前一起通过。它不是预扣余额。

日用量在 UTC 零点清零。周额度从最近一次手动重置开始按七天周期滚动；从未手动重置的旧账户沿用首次使用起算的七天窗口。

### 4.3 候选状态

下列状态不参与调度：`disabled`、`expired`、`revoked`、`invalid`、`unavailable`、`cooldown`、`cooling_down`、`quota_exhausted`、`exhausted`、`blocked`。

`error` 仍参与：CPA 在冷却到期后会重新提供这类凭证，状态要等成功一次才恢复，排除它会让冷却变成永久 503。

### 4.4 重试和跨优先级

上游失败后，CPA 去掉已试过的凭证再调一次 `scheduler.pick`，插件按同样规则过滤，所以重试不会越过绑定。

在支持 `scheduler_across_priorities` 的 CPA 上，候选列表包含所有优先级；插件先按绑定过滤再挑优先级，绑定以外的高优先级凭证不会把绑定内的低优先级凭证挡掉。

### 4.5 503 的内容

```text
no auth available (providers=codex, model=gpt-5.6-luna)
```

和 CPA 自己找不到凭证时的报错一样，不暴露绑定了哪些凭证。

## 5. 旧版凭证组绑定

fork.25 及以前可以把绑定指向凭证组（内置等级或 `classify:` 自定义组）。fork.26 加载这类绑定时：

- 不报错，管理页把它标成「需要重新选择凭证」，并显示原来的组名；
- 在重新选择凭证之前，`scheduler.pick` 一律返回 503；
- 批量历史里要恢复成凭证组的操作会被拒绝（`group_unsupported`）。

指定凭证的绑定会额外写一个编码了凭证 ID 的保留组名。回退到旧版本时，旧版只认组名，这个保留组名匹配不到任何凭证，因此仍然失败关闭；升级回来时还能从中恢复凭证 ID。

## 6. 记账

请求完成后 CPA 调用 `usage.handle`（流式和非流式都会调）。插件用明文 Key 算出 `caller_scope`，找到已启用的绑定后记到：

```text
native-scope:<caller_scope>
```

- 价格来自独立的模型价格表：先查上游实际模型，再查客户端请求的模型名。
- 没有价格的模型仍记调用次数和 token，金额为 0。
- 失败的请求不记账。
- 用量先记在内存里，后台每 15 秒检查一次，有变化才写回 `state_file`；修改绑定时会把当前用量一起写入；卸载插件时强制写一次。

## 7. 停用或删除绑定

停用或删除后，这把 API 密钥在 CPA 里仍然有效，只是不再受凭证和额度限制，CPA 恢复默认调度。要真正撤销访问，必须同时从 CPA 的客户端 API 密钥里删除它。

## 8. 插件以外的边界

- 绕过 `scheduler.pick` 或不带 `caller_scope` 的路径不受约束：Home 调度（`-home-jwt`）、直接插件执行器、Alpha Search、Codex Live/Realtime。
- `quota-exceeded.antigravity-credits: true` 时的 Antigravity credits 兜底会直接枚举凭证，必须关掉。
- CPA 只启用优先级最高的 Scheduler 插件，Access Guard 必须排第一。
- `/v1/models` 无法按 Key 过滤，可能仍显示全局模型列表。
- 插件停用、卸载或加载失败时，CPA 可能直接按默认调度接受这把 Key。

## 相关实现

- `internal/plugin/app.go`：`pickScheduler`、`handleUsage`、管理路由
- `internal/plugin/native_round_robin.go`：轮询游标
- `internal/policy/native_keys.go`：绑定校验、`ResolveNativeKeyConstraint`
- `internal/policy/native_quota.go`：额度检查、记账、重置
- `internal/policy/store.go`：状态加载、按需落盘
