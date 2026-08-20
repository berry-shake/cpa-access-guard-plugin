# Access Guard 请求过滤原理报告

本文说明 Access Guard `v0.4.4-fork.6` 如何处理两类 API Key：

1. CPA 顶层 `api-keys` 中的原生 Key，包括已绑定和未绑定两种情况；
2. 由 Access Guard 创建并认证的下游 Key。

两类 Key 最根本的区别是：

- 原生 Key 由 CPA 创建和认证。Access Guard 不接管认证，只能在认证后对已绑定的 Key 限制凭证组和用量额度；未绑定时保持 CPA 默认调度。
- 下游 Key 由 Access Guard 创建和认证。插件可以完整控制模型或别名、目标供应商、真实模型、凭证范围、RPM、日/周额度和计费。

## 能力对照

| 能力 | CPA 原生 Key | Access Guard 下游 Key |
|---|---|---|
| 创建者 | CPA 顶层 `api-keys` | Access Guard |
| 认证者 | CPA 内置认证器 | Access Guard |
| 插件是否保存明文 | 否 | 否，只保存不可逆哈希 |
| 模型白名单 | 当前不支持 | 支持 |
| 模型别名映射 | 不参与 | 支持 |
| 凭证范围 | 已绑定时支持 | 可按模型或别名目标配置 |
| 未绑定/未配置时 | CPA 默认自由调度 | 未允许的模型直接拒绝 |
| RPM、日/周额度 | 已绑定时支持 | 支持 |
| `/v1/models` | 按 CPA 原生行为 | 可允许或拒绝，但不能过滤列表内容 |
| 组内无可用凭证 | `503 no auth available` | 配置 group 时同样返回 503 |
| 达到额度 | `429 usage_limit_reached` | 在插件认证阶段拒绝 |
| 价格来源 | 独立模型价格目录 | 别名价格、Key 覆盖价格或独立价格目录 |

---

# 第一条链路：CPA 原生 Key

## 1. 原生 Key 是什么

原生 Key 配置在 CLIProxyAPI 顶层：

```yaml
api-keys:
  - sk-...
  - sk-...
```

它不是 Access Guard 创建的。客户端发送请求时，首先由 CPA 验证 Key：

```http
POST /v1/responses
Authorization: Bearer sk-...
Content-Type: application/json

{
  "model": "grok-4.6",
  "input": "Hello"
}
```

职责分工为：

```text
CPA 顶层 Key
    ├── CPA 判断 Key 是否有效
    └── Access Guard 在认证后限制凭证组和额度
```

原生绑定不是一条新的认证记录。停用或删除绑定不会停用 CPA 顶层 Key。

## 2. `caller_scope`：原生 Key 的不可逆身份

CPA 认证原生 Key 后，会生成稳定的不可逆身份：

```text
caller_scope = SHA-256(trimmed plaintext key)
```

`caller_scope` 是 64 位十六进制字符串。CPA 在调用 Scheduler 时将它放入调度元数据。

Access Guard 创建原生绑定时，同样只计算并保存 `caller_scope`，以及：

- 绑定 ID；
- 名称；
- 启用状态；
- Key 脱敏预览；
- 凭证组；
- RPM；
- 日额度；
- 周额度。

插件不保存原生 Key 明文。

```text
请求中的 CPA 原生 Key
        │
        ▼
CPA 完成认证并生成 caller_scope
        │
        ▼
Access Guard 用 caller_scope 查找绑定
```

## 3. 未绑定原生 Key 的流程

如果 CPA 顶层 Key 没有 Access Guard 绑定：

```text
客户端请求
    │
    ▼
CPA 顶层 api-keys 认证
    │
    ├── Key 无效 → CPA 拒绝
    └── Key 有效
            │
            ▼
CPA 解析 provider/model
            │
            ▼
CPA 生成候选凭证列表
            │
            ▼
调用 Access Guard Scheduler
            │
            ▼
Access Guard 没找到 caller_scope 绑定
            │
            ▼
返回 Handled=false
            │
            ▼
CPA 按默认逻辑自由调度
```

未绑定时，Access Guard 不执行：

- 凭证组隔离；
- 原生绑定 RPM；
- 日额度或周额度；
- 原生绑定用量账本。

“未绑定”不等于“Key 无效”，只表示 Access Guard 没有附加限制。

## 4. 已绑定原生 Key 的流程

假设绑定如下：

```text
绑定 ID：native-key-3
名称：XDL
凭证范围：classify:share
周额度：$1000
状态：启用
```

完整流程：

```text
客户端请求
    │
    ▼
CPA 认证原生 Key
    │
    ▼
CPA 生成 caller_scope
    │
    ▼
CPA 根据模型确定 provider 并生成候选凭证
    │
    ▼
Access Guard 找到已启用绑定
    │
    ▼
检查 RPM、日额度、周额度
    │
    ▼
按绑定 group 过滤候选凭证
    │
    ▼
从组内可用候选中选择最高优先级凭证
```

### 4.1 额度门禁

检查顺序为：

1. RPM；
2. 日美元额度；
3. 周美元额度。

未设置或为 0 表示不限制。如果三个限制都未设置，额度门禁直接通过，不产生 RPM 副作用。

达到额度后，原生 Key 在 Scheduler 阶段收到：

```http
HTTP 429 Too Many Requests
```

```json
{
  "error": {
    "message": "The usage limit has been reached",
    "type": "usage_limit_reached"
  }
}
```

插件内部会区分：

```text
rpm_exceeded
daily_exceeded
weekly_exceeded
```

但客户端统一看到 `usage_limit_reached`。

### 4.2 为什么超额的第一笔请求通常仍能通过

额度检查在请求前进行，用量在请求完成后写入：

```text
请求前：当前用量 < 限额 → 允许
请求后：写入费用，可能超过限额
下一笔：当前用量 >= 限额 → 拒绝
```

因此额度语义是：

> 已记录用量达到或超过限额后，阻止后续请求。

它不是请求前预估费用并预扣的硬余额系统。并发或紧邻请求还可能在前一笔记账完成前同时通过。

## 5. 凭证分类

Access Guard 对 CPA 提供的每个 Scheduler 候选凭证进行分类。

### 5.1 自定义分类优先

自定义规则可以匹配：

- `filename` 或 `id`；
- `provider`；
- `plan_type`；
- `tier`；
- Scheduler 候选中携带的其他 Attribute。

规则中的裸组名，例如：

```text
share
```

运行时会格式化为：

```text
classify:share
```

这样不会与内置 `free`、`team`、`plus`、`supported` 等组冲突。

一个凭证可以同时命中多个自定义规则。

### 5.2 内置分类回退

只有没有命中任何自定义规则时，才读取：

```text
plan_type
tier
```

例如：

```text
plan_type=free → free
plan_type=team → team
plan_type=plus → plus
tier=paid → paid
```

参与内置分层但缺少明确套餐信息的凭证可进入 `supported`。

## 6. 候选状态过滤

下列状态不会参与调度：

```text
disabled
error
expired
revoked
invalid
unavailable
cooldown
cooling_down
quota_exhausted
exhausted
blocked
```

过滤顺序：

```text
CPA 候选凭证
    ├── 状态不可用 → 排除
    ├── 不属于绑定组 → 排除
    └── 状态可用且属于绑定组 → 保留
```

## 7. 组内选择与重试

组内选择规则：

1. 选择 Priority 最高的凭证；
2. Priority 相同时，选择 ID 字典序较小的凭证，保证结果稳定。

上游失败后，CPA 会移除已尝试凭证并重新调用 Scheduler。Access Guard 会再次执行相同组过滤，因此重试不会越过绑定边界：

```text
第一次：share/A
    │
    ├── 上游失败
    ▼
第二次：仍然只在 share 中选择 share/B
```

## 8. 组内没有可用凭证

Access Guard 不会返回 `Handled=false` 让 CPA 退回组外，因为这会突破隔离边界。它会失败关闭：

```http
HTTP 503 Service Unavailable
```

```json
{
  "error": {
    "message": "no auth available (providers=xai, model=grok-4.6)",
    "type": "server_error",
    "code": "internal_server_error"
  }
}
```

错误不会暴露内部组名，客户端无法据此推断凭证分类结构。

## 9. 停用或删除绑定

停用或删除绑定后：

- CPA 顶层 Key 仍然有效；
- Access Guard 不再限制凭证组；
- 原生绑定额度门禁停止生效；
- CPA 恢复默认自由调度。

真正撤销访问必须同时从 CPA 顶层 `api-keys` 删除 Key。

## 10. 原生 Key 的模型能力

当前原生绑定只配置：

```text
凭证组、RPM、日额度、周额度
```

它没有显式模型白名单。

因此原生绑定控制的是：

> 这个 Key 的请求只能使用某个凭证组中的候选。

模型是否能调用，由以下条件共同决定：

```text
CPA 是否认识该模型
+
绑定组内是否有对应 provider/model 的可用凭证
```

## 11. 原生 Key 用量记账

请求完成后，CPA 调用 `usage.handle`。Access Guard 根据原生 Key 重新计算 `caller_scope`，把用量记入独立账户：

```text
native-scope:<caller_scope>
```

原生 Key 价格只来自独立模型价格目录，不使用下游别名价格。

查价会尝试：

1. 上游实际模型 ID；
2. 客户端请求模型名。

模型没有价格时：

- 调用次数仍可记录；
- Token 数仍可记录；
- USD 为 0；
- 美元额度不会因该请求增长。

## 12. 原生 Key 总流程

### 未绑定

```text
客户端 → CPA 认证 → CPA 生成候选
                     │
                     ▼
              Access Guard 无绑定
                     │
                     ▼
                Handled=false
                     │
                     ▼
                CPA 自由调度
```

### 已绑定

```text
客户端 → CPA 认证 → caller_scope → Access Guard 找到绑定
                                      │
                                      ├── 检查 RPM
                                      ├── 检查日额度
                                      └── 检查周额度
                                      │
                                      ▼
                                过滤组内可用凭证
                                      │
                         ┌────────────┴────────────┐
                         │                         │
                      无候选                    有候选
                         │                         │
                   503 no auth            选择最高优先级凭证
                                                   │
                                                   ▼
                                             请求完成并记账
```

---

# 第二条链路：Access Guard 下游 Key

## 1. 下游 Key 是什么

下游 Key 由 Access Guard 管理面板创建。插件保存：

- Key ID；
- 名称；
- 启用状态；
- Key 的不可逆哈希；
- 脱敏预览；
- RPM；
- 允许的模型或别名；
- 日额度；
- 周额度；
- 是否允许访问 `/v1/models`。

明文只在新建或轮换成功后显示一次，插件不会保存可恢复的明文。

## 2. 下游 Key 认证

```text
提取 Authorization 或查询参数中的 Key
    │
    ▼
计算不可逆哈希
    │
    ▼
查询下游 Key 哈希索引
    │
    ├── 找不到 → 插件表示“不认识”，CPA 可继续尝试其他认证器
    └── 找到
            │
            ├── 插件是否启用
            ├── Key 是否启用
            ├── 端点是否允许
            └── 模型或别名是否允许
```

Access Guard 的认证提供者不是全局独占的。因此不应将同一明文同时用于：

- CPA 顶层原生 Key；
- Access Guard 下游 Key；
- 其他认证提供者。

否则身份归属可能产生歧义。

## 3. 模型提取与白名单

插件从路径、查询参数或请求体提取模型，例如：

```json
{
  "model": "fast"
}
```

下游 Key 使用严格模型/别名白名单：

```text
请求模型
    │
    ▼
是否存在于该 Key 的配置
    ├── 否 → model_not_allowed，拒绝
    └── 是 → 解析 provider/model/group
```

与未绑定原生 Key 不同，未允许的下游模型不会交给 CPA 自由路由。

## 4. 模型别名映射

客户端模型名可以映射到真实上游目标：

```text
alias: fast
provider: xai
target_model: grok-4.6
group: classify:share
```

链路：

```text
客户端模型名
    │
    ▼
Access Guard 别名
    │
    ▼
目标 provider + 真实模型 + 可选凭证组
```

## 5. 多目标别名

一个别名可以有多个目标：

```text
alias = smart
targets:
  1. codex / gpt-5.3-codex / classify:premium
  2. xai / grok-4.6 / classify:share
```

支持两种分发：

### Round-robin

默认轮询：

```text
第 1 次 → target 1
第 2 次 → target 2
第 3 次 → target 1
```

轮询计数属于全局别名，由引用该别名的 Key 共享。

### Priority

静态选择列表中的第一个目标。当前不是完整的上游失败自动切换机制。

## 6. 认证和路由保持同一次目标选择

多目标别名在认证阶段选中目标后，会临时保存：

```text
key_id + alias → selected ModelRule
```

模型路由阶段消费同一个选择，保证：

```text
provider、target_model、group
```

始终一致，避免认证阶段与路由阶段分别轮询到不同目标。

暂存选择：

- TTL 为 30 秒；
- 每个 Key/别名最多 32 条；
- 只保存已经通过认证的请求。

## 7. RPM 和额度门禁

模型白名单通过后，依次检查：

1. RPM；
2. 日美元额度；
3. 周美元额度。

RPM 和用量账本以 Key ID 隔离。0 表示不限制。

下游 Key 也采用请求前检查、请求后记账，所以使额度第一次超限的请求会通过，后续请求才会被拒绝。

与原生 Key 的区别是：

- 原生 Key 在 Scheduler 阶段检查额度；
- 下游 Key 在前端认证阶段检查额度。

## 8. `/v1/models`

CPA 的 `/v1/models` 返回全局模型注册表，插件无法按 Key 改写列表内容。因此下游 Key 只有二选一：

```text
allow_models_endpoint=false → 拒绝访问
allow_models_endpoint=true  → 允许访问完整全局列表
```

不能只显示当前 Key 允许的模型。因此列表中出现某模型，不代表该 Key 实际可调用。

## 9. 认证成功后的元数据

Access Guard 向 CPA 返回：

```text
provider = access-guard
key_id
requested_model
alias
target_provider
target_model
group
```

认证 Principal 是 Key ID，不是明文 Key。CPA 后续用量上报也使用 Key ID。

## 10. 模型路由

路由结果为：

```text
TargetKind = provider
Target = 目标供应商
TargetModel = 真实模型
```

例如：

```text
客户端请求 fast
    │
    ▼
provider=xai
target_model=grok-4.6
```

CPA 接下来只生成符合目标 provider/model 的凭证候选。

## 11. 下游 Key 的凭证组过滤

如果模型规则包含 group，Scheduler 使用与原生绑定相同的分类和状态过滤：

```text
CPA provider/model 候选
    │
    ▼
排除不可用状态
    │
    ▼
自定义分类优先，内置 plan_type/tier 回退
    │
    ▼
只保留目标 group
    │
    ▼
选择最高优先级凭证
```

如果没有 group，Access Guard 返回 `Handled=false`，由 CPA 在目标 provider/model 的全部凭证中自由选择。

因此下游 Key 可以：

- 只限制模型，不限制凭证；
- 同时限制模型和凭证；
- 为不同模型配置不同凭证组；
- 通过别名跨 provider 路由。

## 12. 组内无凭证

配置了 group 但组内没有可用候选时，插件失败关闭并返回：

```http
HTTP 503
```

```json
{
  "error": {
    "message": "no auth available (providers=xai, model=grok-4.6)",
    "type": "server_error",
    "code": "internal_server_error"
  }
}
```

不会退回组外凭证。

## 13. 响应模型名回写

非流式响应可以把真实模型名改回客户端别名：

```text
客户端请求：fast
真实上游：grok-4.6
响应 model：fast
```

流式 SSE 响应不会改写，以免破坏分帧。

## 14. 下游 Key 用量记账

请求完成后，CPA 调用 `usage.handle`，提供：

- Key Principal；
- 客户端请求别名；
- 上游真实模型；
- 是否失败；
- 输入、输出、推理、缓存读取、缓存写入和总 Token。

计费价格来源优先级为：

1. Key 对别名价格的覆盖；
2. 全局别名价格；
3. 独立模型价格目录。

支持：

- 输入 Token 价格；
- 输出 Token 价格；
- 缓存读取价格；
- 缓存写入价格；
- 固定每次调用价格。

### Token 计费

根据实际 Token 和 provider 的缓存语义计算。部分 provider 的缓存 Token 已包含在 input 中，部分则单独上报；插件按 provider 拆分，避免重复计费。

### 每次调用计费

```text
billing_mode = per_call
per_call_usd = 0.01
```

成功请求收取固定费用；失败请求通常不收费、不增加成功调用次数。

图像和视频端点是例外：CPA 的 xAI 图像/视频路径可能不发送标准 UsageReporter，插件会在认证通过时预扣 per-call 费用，因此上游失败也可能已经计费。

## 15. 禁用、删除与轮换

### 禁用

插件仍能识别哈希，但不会允许认证。只要明文没有同时存在于其他认证提供者中，请求最终失败。

### 删除

Key 哈希被移除，插件不再认识该 Key，无法恢复。

### 轮换

轮换会生成新明文和新哈希，旧 Key 立即失效；新明文只显示一次。

## 16. 下游 Key 总流程

```text
客户端请求
  │
  ▼
Access Guard 提取并哈希 Key
  │
  ├── 未知 → 交给其他认证器尝试
  └── 已知且启用
        │
        ▼
解析请求模型/别名
        │
        ├── 不在白名单 → 拒绝
        └── 在白名单
              │
              ▼
选择别名目标（priority / round-robin）
              │
              ▼
检查 RPM、日额度、周额度
              │
              ▼
认证成功，Principal=Key ID
              │
              ▼
模型路由：alias → provider + target_model
              │
              ▼
CPA 生成 provider/model 候选
              │
              ▼
是否配置 group
  ┌───────────┴───────────┐
  │                       │
没有 group              有 group
  │                       │
CPA 默认选择            Access Guard 分类过滤
                          │
                          ├── 无候选 → 503
                          └── 有候选 → 选择最高优先级
              │
              ▼
执行上游请求
              │
              ▼
非流式响应可将 model 改回 alias
              │
              ▼
CPA 上报最终 Token 用量
              │
              ▼
Access Guard 记账，供下一次额度检查使用
```

---

# 两条链路的核心差异

## 1. 认证位置

```text
原生 Key：CPA 认证，Access Guard 不参与认证
下游 Key：Access Guard 认证，CPA 接受插件身份
```

## 2. 模型控制

```text
原生 Key：没有显式模型白名单
下游 Key：严格模型/别名白名单
```

## 3. 凭证限制

```text
原生 Key：一个绑定对应一个固定 group，所有模型请求共用
下游 Key：每个模型或别名目标可以配置不同 group
```

## 4. 未配置时

```text
未绑定原生 Key：恢复 CPA 自由调度
下游 Key 未允许模型：直接拒绝
```

## 5. 额度门禁位置

```text
原生 Key：Scheduler 阶段检查，超限返回 429 usage_limit_reached
下游 Key：前端认证阶段检查
```

## 6. 计价来源

```text
原生 Key：独立模型价格目录
下游 Key：别名价格、Key 覆盖价格或独立模型价格目录
```

---

# 常见误解

## 绑定原生 Key 等于创建原生 Key吗？

不是。Access Guard 只保存 `caller_scope`、脱敏预览、group 和 limits。Key 仍由 CPA 顶层 `api-keys` 管理。

## 删除原生绑定会让 Key 失效吗？

不会。删除后 Key 恢复 CPA 默认自由调度。真正撤销必须从 CPA 顶层 `api-keys` 删除。

## 原生绑定能直接限制模型吗？

当前不能。它限制的是凭证组。模型能否调用取决于组内是否有对应 provider/model 的可用凭证。

## `/v1/models` 能准确反映权限吗？

不能。它是全局目录。真正调用仍要经过模型白名单、模型路由和凭证组过滤。

## 配置额度后是否绝对不会超过？

不是预扣模型。使额度第一次超限的请求会通过，记账完成后的后续请求才会被阻止；并发请求还可能产生短暂超额。

---

# 一句话总结

原生 Key 的链路是：

> CPA 负责确认“你是谁”；Access Guard 在已绑定时负责限制“你只能使用哪组凭证，以及是否已超过额度”；未绑定时恢复 CPA 默认调度。

下游 Key 的链路是：

> Access Guard 同时负责确认“你是谁”、判断“你能请求哪个模型或别名”、把请求路由到哪个真实模型、限制可用凭证组，并执行 RPM、额度和用量计费。

## 相关实现

- `internal/plugin/app.go`：插件认证、模型路由、Scheduler、响应处理和用量入口；
- `internal/policy/store.go`：下游 Key 认证、模型白名单、别名选择、RPM、额度和用量记账；
- `internal/policy/native_quota.go`：原生 Key 绑定的 RPM、日/周额度和独立账本；
- `internal/policy/config.go`：Key、模型规则、别名、分类规则和价格配置结构。
