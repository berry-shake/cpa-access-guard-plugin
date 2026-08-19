# Plan: 原生 Key 额度限制（RPM + 日/周美元限额 + 用量可见）

> 版本目标：`0.4.4-fork.2`。规划文档，实现前评审用。

## 0. 背景与定位

`NativeKeyBinding` 目前只做**调度隔离**（原生 key → 凭证分组），没有任何计量/限流能力。
原因是原生 key 认证在 CPA 宿主的 `config-api-key` provider 完成，插件只在调度时收到单向哈希的
`caller_scope`，无法关联到计量账户。

两个前提已核实成立（宿主源码 + 插件现有代码）：

1. **usage 记录携带原生 key 明文**：`usage_helpers.go:387 APIKeyFromContext` 从 gin context
   `userApiKey` 取值——原生 key 请求同样填充。插件 `usage.handle` 收到的 `APIKey` 字段可
   hash 成 `caller_scope`（`NativeCallerScope()` 已有），与绑定精确匹配。
2. **调度执行点已就位**：我们已是唯一调度器插件（priority 100），`pickScheduler` 已从
   `Options.Metadata` 解析 `caller_scope`（`app.go:285`），限额闸门加在同一入口。

设计原则：
- 额度字段挂在 `NativeKeyBinding` 上，**一把钥匙一份策略**，与下游 key 心智对称。
- 计价**复用全局别名表**，绑定上不单独配价。
- RPM 复用现有 `RateLimiter`（滑动窗口），账户复用现有 `usageLedger`（UTC 日窗口 + 滚动 7×24h 周窗口）。
- 明确不做：模型/别名白名单（换成下游 key）、按绑定配价、5h 窗口、per_call 计费。

## 1. 数据结构（internal/policy/native_keys.go）

```go
type NativeKeyBinding struct {
    // ...现有字段不变...
    RPM       int     `yaml:"rpm,omitempty" json:"rpm,omitempty"`           // 0 = 不限
    DailyUSD  float64 `yaml:"daily_usd,omitempty" json:"daily_usd,omitempty"` // 0 = 不限
    WeeklyUSD float64 `yaml:"weekly_usd,omitempty" json:"weekly_usd,omitempty"` // 0 = 不限
}
```

- `CreateNativeKeyBindingInput` / `UpdateNativeKeyBindingInput` 增加对应字段（Update 用
  `*int` / `*float64` 指针语义，nil = 不改，显式 0 = 清除限制）。
- `normalizeNativeKeyBindings` 校验：负值报错；`rpm` 上限沿用下游 key 相同约束（如有）。
- **向后兼容**：字段全部 omitempty，旧 state 文件加载不受影响；state schema 无需迁移。

## 2. 记账（internal/policy/store.go + usage.go）

### 2.1 usageLedger 增加 native 账户命名空间

ledger 以 `entries map[string]*UsageState` 组织，key 维度是字符串。native 账户使用保留前缀：

```go
const nativeUsageLedgerPrefix = "native-scope:"
// ledger key = nativeUsageLedgerPrefix + callerScope
```

前缀与下游 key 的 ID（uuid）无碰撞可能。日/周窗口逻辑（`ensureDailyWindowLocked` /
`ensureWeeklyWindowLocked` / `RecordCost`）零改动复用。

### 2.2 RecordUsage 增加 native fallback

`store.go:593 RecordUsage`：下游 key 匹配失败（`findByID` + `findBySecret` 均未命中）后，
不再直接丢弃，改为：

```go
// native fallback：明文 key → caller_scope → 绑定
if scope := NativeCallerScope(apiKeyOrID); scope != "" {
    if b := s.findNativeBindingByScope(scope); b != nil && b.Enabled {
        // 计价：全局别名表解析 resolved alias；无价格配置则记 0 美元（只累计 CallCount）
        // 复用 RecordCost 到 native-scope: 账户
    }
}
```

- 计价解析顺序与下游 key 相同：优先 `Alias`，fallback `Model`；从**全局别名表**取价。
- 别名表无该模型的价格 → 记账 0 USD、CallCount +1（调用次数可见，金额保守为 0）。
- Failed 请求不收费（与下游 key 一致）。
- 计价函数抽取公共 helper，避免与下游 key 路径复制粘贴。

### 2.3 状态持久化

`UsageState` 快照已由 `StartUsageFlusher` 周期落盘，native-scope 条目自动随之持久化，
无需额外改动。仅确认 flusher 的 map 序列化对新增 key 形态无假设。

## 3. 执行（internal/plugin/app.go pickScheduler）

在现有 native binding 解析点（`app.go:293` 解析出 `callerScope`）之后、分组过滤之前，
插入限额闸门：

```go
if nativeBinding 或 callerScope != "" {
    if decision, limited := a.store.CheckNativeKeyQuota(callerScope); limited {
        // 按 decision 原因返回不同错误码，便于客户端区分
        // rpm → 429 "rate_limited"（Retryable）
        // daily/weekly → 503? 见下方"错误语义"讨论，倾向 429/402 语义化处理
    }
}
```

新增 `Store.CheckNativeKeyQuota(scope) (QuotaDecision, bool)`：

- **RPM**：查绑定 `RPM>0` 时 `RateLimiter.Allow("native-scope:"+scope, rpm)`。注意：RPM 计数
  应在闸门处（调度时）扣减，不能在 usage.handle 事后扣——否则突发并发会漏计。现有下游 key
  的 RPM 扣减点在认证处，语义对齐。
- **美元**：读 ledger 当前窗口累计，对比绑定 `DailyUSD` / `WeeklyUSD`。只读判断，不扣减
  （扣减由 usage.handle 记账完成）。
- 三项均 0 或未绑定 → 放行（保持现状，零回归风险）。

### 错误语义（决策）

- RPM 超限：`ErrorEnvelope("rate_limited", ..., 429)`，`Retryable=true`。
- 美元超限：`ErrorEnvelope("quota_exceeded", "...daily|weekly budget exhausted", 429)`，
  Retryable=true（窗口滚动后自然恢复）。不用 503——503 已被隔离语义占用
  （`auth_not_found`），区分度优先。
- 现有隔离 503 路径保持原样，不受影响。

### 边界与已知缺口

- **先扣 RPM 后被上游拒绝**：调度失败/上游 429 的请求已消耗一次 RPM 计数。与下游 key
  行为一致（认证即扣），接受。
- **不过 scheduler 的旁路端点**（如 count-tokens 预检）：RPM 闸门可能漏计、记账仍全量
  （usage.handle 广播保证）。主流量（chat/completions/messages）全覆盖。
- **usage.handle 与调度并发**：记账是异步 fire-and-forget，美元限额判定存在窗口期竞态
  （超限后极短时间内已发起的请求仍可能通过）。与下游 key 限享有相同的最终一致性，接受。

## 4. 管理 API 与 UI

### 4.1 API（internal/plugin/native_binding_handlers.go）

- `POST/PATCH /native-key-bindings`：接受 `rpm` / `daily_usd` / `weekly_usd` 字段。
- `GET /native-key-bindings`：响应体每条绑定附带实时用量：

```json
{
  "rpm_limit": 30,
  "daily_usd_limit": 5.0,
  "weekly_usd_limit": 50.0,
  "usage": {
    "rpm_used": 12,
    "daily_usd_used": 1.23,
    "weekly_usd_used": 8.9,
    "daily_calls": 45,
    "weekly_calls": 320
  }
}
```

- 新增 `GET /native-key-bindings/usage?id=...`：per-alias 明细（复用下游 key usage 端点
  的响应形态），供 UI 展开视图。

### 4.2 Web UI（web/src）

- 绑定编辑表单增加三个输入（RPM / 日 USD / 周 USD），留空 = 不限。
- 绑定卡片显示用量条：日/周美元进度条 + RPM 当前值；接近限额（≥80%）变色提示。
- 超限错误在 key 侧无感知（原生 key 是配置在 CPA 的），仅在 UI 用量面板体现。

## 5. 测试计划

| 层 | 用例 |
|---|---|
| policy 单测 | binding 字段校验（负值/零/省略）；RecordUsage native fallback 命中与未命中；无价格模型记 0 USD 但计 CallCount；日/周窗口滚动对 native 账户生效；CheckNativeKeyQuota 三档判定（RPM 拒、日拒、周拒、全过）；RPM 扣减点在调度处 |
| plugin 单测 | pickScheduler：限额超限时返回对应错误码；未配置限额的绑定行为与现状 bit-for-bit 一致（回归重点）；隔离 503 与限额 429 错误码互不串扰 |
| 持久化 | state 含 native-scope 条目的加载/落盘 round-trip；旧 state（无新字段）加载兼容 |
| 集成（本地） | 三 key 场景：无绑定原生 key（行为不变）、绑定无限额（仅隔离）、绑定限额（超限 429、恢复后放行）；streaming 请求记账正确 |

## 6. 实施顺序（每步可独立验证）

1. `native_keys.go`：字段 + 校验 + input/update 类型（纯数据层，单测先行）
2. `usage.go` / `store.go`：ledger native 命名空间 + RecordUsage fallback + 计价 helper 抽取
3. `store.go`：`CheckNativeKeyQuota` + RPM 扣减
4. `app.go`：pickScheduler 闸门 + 错误语义
5. handlers：API 字段透传 + usage 端点
6. web UI：表单 + 用量展示
7. 集成测试 + 三端部署（本地 → pro → open）

## 7. 风险与回滚

- 最大风险点：pickScheduler 闸门是所有请求的必经路径，第 4 步必须保证"未配置限额 = 零行为
  差异"，用回归单测锁死。
- 回滚：新字段全部 omitempty、闸门默认短路，回滚二进制即回滚行为；state 文件含
  native-scope 条目在旧版本下会被当作未知 ledger 条目加载（无害，仅多余数据）。
- 发布：`v0.4.4-fork.2` tag，走现有 CI（版本注入已就绪），registry 同步 bump。
