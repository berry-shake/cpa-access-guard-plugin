import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ClassifyRule, NativeBindingUsageSummary, NativeKeyBinding } from "../types";
import { _resetLocale } from "../i18n";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const apiMocks = vi.hoisted(() => ({
  fetchTopLevelAPIKeys: vi.fn(),
  fetchNativeKeyBindingCatalog: vi.fn(),
  fetchNativeCredentialOptions: vi.fn(),
  fetchClassifyRules: vi.fn(),
  createNativeKeyBinding: vi.fn(),
  updateNativeKeyBinding: vi.fn(),
  deleteNativeKeyBinding: vi.fn(),
  resetNativeKeyBindingQuota: vi.fn(),
}));
const modelMocks = vi.hoisted(() => ({ fetchCatalog: vi.fn() }));

vi.mock("../api/mappings", () => apiMocks);
vi.mock("../api/models", () => modelMocks);

import NativeKeyBindingsTab, { buildNativeBindingGroupOptions } from "./NativeKeyBindings";

const existing: NativeKeyBinding = {
  id: "client-a",
  name: "Client A",
  enabled: true,
  key_preview: "sk-ab...wxyz",
  group: "team",
  model_access: { mode: "all", models: [] },
};

const existingSecret = "sk-existing-native-secret-0123456789";

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot> | null = null;
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

const weeklyUsage: NativeBindingUsageSummary = {
  rpm_limit: 0,
  daily_usd_limit: 0,
  weekly_usd_limit: 100,
  rpm_used: 0,
  daily_usd_used: 0,
  weekly_usd_used: 17,
  daily_calls: 1840,
  weekly_calls: 3932,
};

function quotaCatalog(binding: NativeKeyBinding) {
  return {
    entries: [{ key_index: 0, key_preview: binding.key_preview, binding }],
    orphan_bindings: [],
  };
}

async function renderQuotaBinding(binding: NativeKeyBinding) {
  apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue(quotaCatalog(binding));
  await act(async () => {
    root = createRoot(container);
    root.render(<NativeKeyBindingsTab />);
    await tick();
  });
}

function weeklyMeter() {
  return container.querySelector('[role="meter"][aria-label="周额度剩余"]');
}

function change(input: HTMLInputElement, value: string) {
  // Bypass React's per-element value tracker so the synthetic onChange sees
  // this as a real browser edit under jsdom.
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

function changeSelect(select: HTMLSelectElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")?.set;
  setter?.call(select, value);
  select.dispatchEvent(new Event("change", { bubbles: true }));
}

beforeEach(() => {
  _resetLocale("zh-CN");
  vi.spyOn(window, "confirm").mockReturnValue(true);
  container = document.createElement("div");
  document.body.appendChild(container);
  apiMocks.fetchTopLevelAPIKeys.mockResolvedValue([existingSecret]);
  apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
    entries: [{ key_index: 0, key_preview: existing.key_preview, binding: existing }],
    orphan_bindings: [],
  });
  apiMocks.fetchClassifyRules.mockResolvedValue([
    { name: "sample-tenant-a-filename", field: "filename", pattern: "tenant-a", group: "tenant-a", enabled: true },
    { name: "sample-claude-provider", field: "provider", pattern: "claude", group: "claude-auth", enabled: true },
    { name: "sample-codex-team-plan", field: "plan_type", pattern: "team", group: "codex-premium", enabled: true },
    { name: "sample-antigravity-paid-tier", field: "tier", pattern: "paid", group: "antigravity-paid", enabled: true },
  ] satisfies ClassifyRule[]);
  apiMocks.fetchNativeCredentialOptions.mockResolvedValue([
    { id: "tenant/codex-a.json", provider: "codex", label: "Account A", status: "active", plan: "team", source: "auth_file" },
    {
      id: "tenant/codex-b.json",
      provider: "codex",
      name: "Codex #1",
      label: "Codex",
      status: "configured",
      source: "ai_provider",
      authIndex: "index-12345678",
      models: ["gpt-5.6-luna", "gpt-5.5"],
    },
  ]);
  modelMocks.fetchCatalog.mockResolvedValue([
    { provider: "codex", group: "team", model: "gpt-5.6-luna" },
    { provider: "codex", group: "plus", model: "gpt-5.6-luna" },
    { provider: "codex", group: "team", model: "gpt-5.5" },
    { provider: "gemini", model: "gemini-2.5-pro" },
  ]);
  apiMocks.createNativeKeyBinding.mockResolvedValue(existing);
  apiMocks.updateNativeKeyBinding.mockResolvedValue(existing);
  apiMocks.deleteNativeKeyBinding.mockResolvedValue(undefined);
  apiMocks.resetNativeKeyBindingQuota.mockResolvedValue(undefined);
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container.remove();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

describe("buildNativeBindingGroupOptions", () => {
  it("combines built-in groups with enabled classify rules and de-duplicates them", () => {
    const options = buildNativeBindingGroupOptions([
      { name: "a", field: "filename", pattern: "a", group: "VIP", enabled: true },
      { name: "b", field: "filename", pattern: "b", group: "classify:vip", enabled: true },
      { name: "c", field: "filename", pattern: "c", group: "off", enabled: false },
    ]);
    expect(options).toEqual(["free", "team", "plus", "supported", "classify:vip"]);
  });
});

describe("NativeKeyBindingsTab weekly quota", () => {
  it("shows remaining dollars and percentage between calls and card actions", async () => {
    await renderQuotaBinding({ ...existing, usage: weeklyUsage });

    const meter = weeklyMeter();
    expect(meter).toBeTruthy();
    expect(meter!.getAttribute("aria-valuemin")).toBe("0");
    expect(meter!.getAttribute("aria-valuemax")).toBe("100");
    expect(meter!.getAttribute("aria-valuenow")).toBe("83");
    const quota = meter!.closest(".native-weekly-quota")!;
    expect(quota.textContent).toContain("7D");
    expect(quota.textContent).toContain("83%");
    expect(quota.textContent).toContain("剩余 $83.00 / $100.00");
    expect(quota.classList.contains("ok")).toBe(true);

    const card = meter!.closest(".native-binding-card")!;
    const calls = Array.from(card.querySelectorAll("dd"))
      .find((node) => node.textContent?.trim() === "1840/3932");
    const actions = card.querySelector(".native-binding-actions")!;
    expect(calls).toBeTruthy();
    expect(calls!.compareDocumentPosition(meter!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(meter!.compareDocumentPosition(actions) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(card.textContent).not.toContain("$17.00/$100.00");
  });

  it.each([
    { name: "unlimited weekly quota with daily and RPM limits", usage: { ...weeklyUsage, weekly_usd_limit: 0, daily_usd_limit: 10, rpm_limit: 60 } },
    { name: "negative weekly limit", usage: { ...weeklyUsage, weekly_usd_limit: -1 } },
    { name: "non-finite weekly limit", usage: { ...weeklyUsage, weekly_usd_limit: Infinity } },
    { name: "NaN weekly limit", usage: { ...weeklyUsage, weekly_usd_limit: NaN } },
    { name: "negative used dollars", usage: { ...weeklyUsage, weekly_usd_used: -1 } },
    { name: "non-finite used dollars", usage: { ...weeklyUsage, weekly_usd_used: Infinity } },
    { name: "NaN used dollars", usage: { ...weeklyUsage, weekly_usd_used: NaN } },
    { name: "missing usage", usage: undefined },
  ])("hides the entire remaining quota region for $name", async ({ usage }) => {
    await renderQuotaBinding({ ...existing, usage });

    expect(weeklyMeter()).toBeNull();
    expect(container.querySelector(".native-weekly-quota")).toBeNull();
    expect(container.textContent).not.toContain("周额度剩余");
    expect(container.textContent).not.toContain("NaN");
    expect(container.textContent).not.toContain("Infinity");
  });

  it("hides the quota region for a disabled binding with a configured limit", async () => {
    await renderQuotaBinding({ ...existing, enabled: false, usage: weeklyUsage });

    expect(weeklyMeter()).toBeNull();
    expect(container.querySelector(".native-weekly-quota")).toBeNull();
  });

  it.each([
    { used: 0, percent: "100%", value: 100, color: "ok", remaining: "$100.00", exhausted: false },
    { used: 42, percent: "58%", value: 58, color: "ok", remaining: "$58.00", exhausted: false },
    { used: 71, percent: "29%", value: 29, color: "ok", remaining: "$29.00", exhausted: false },
    { used: 79, percent: "21%", value: 21, color: "ok", remaining: "$21.00", exhausted: false },
    { used: 80, percent: "20%", value: 20, color: "warn", remaining: "$20.00", exhausted: false },
    { used: 95, percent: "5%", value: 5, color: "warn", remaining: "$5.00", exhausted: false },
    { used: 96, percent: "4%", value: 4, color: "danger", remaining: "$4.00", exhausted: false },
    { used: 99.5, percent: "<1%", value: 0.5, color: "danger", remaining: "$0.50", exhausted: false },
    { used: 99.999, percent: "<1%", value: 0.001, color: "danger", remaining: "<$0.01", exhausted: false },
    { used: 100, percent: "0%", value: 0, color: "danger", remaining: "$0.00", exhausted: true },
    { used: 117, percent: "0%", value: 0, color: "danger", remaining: "$0.00", exhausted: true },
  ])("represents a $used dollar usage balance without losing threshold or exhaustion state", async ({ used, percent, value, color, remaining, exhausted }) => {
    await renderQuotaBinding({ ...existing, usage: { ...weeklyUsage, weekly_usd_used: used } });

    const meter = weeklyMeter()!;
    expect(meter).toBeTruthy();
    expect(Number(meter.getAttribute("aria-valuenow"))).toBeCloseTo(value, 6);
    const quota = meter.closest(".native-weekly-quota")!;
    expect(quota.classList.contains(color)).toBe(true);
    expect(quota.textContent).toContain(percent);
    expect(quota.textContent).toContain(`剩余 ${remaining} / $100.00`);
    expect(quota.textContent?.includes("已用尽")).toBe(exhausted);
    expect(quota.textContent).not.toContain("NaN");
  });

  it.each([
    { used: 2.4, percent: "20%", value: 20, remaining: "$0.60" },
    { used: 2.85, percent: "5%", value: 5, remaining: "$0.15" },
  ])("keeps the warning threshold exact for $used dollars used from a 3 dollar limit", async ({ used, percent, value, remaining }) => {
    await renderQuotaBinding({
      ...existing,
      usage: { ...weeklyUsage, weekly_usd_limit: 3, weekly_usd_used: used },
    });

    const meter = weeklyMeter()!;
    const quota = meter.closest(".native-weekly-quota")!;
    expect(Number(meter.getAttribute("aria-valuenow"))).toBeCloseTo(value, 6);
    expect(quota.querySelector(".native-weekly-quota-percent")?.textContent).toBe(percent);
    expect(quota.classList.contains("warn")).toBe(true);
    expect(quota.textContent).toContain(`剩余 ${remaining} / $3.00`);
  });

  it("does not label a nearly full balance as completely unused after precision normalization", async () => {
    await renderQuotaBinding({ ...existing, usage: { ...weeklyUsage, weekly_usd_used: 1e-12 } });

    const quota = weeklyMeter()!.closest(".native-weekly-quota")!;
    expect(quota.querySelector(".native-weekly-quota-percent")?.textContent).toBe("99%");
    expect(quota.classList.contains("ok")).toBe(true);
    expect(quota.textContent).not.toContain("已用尽");
  });

  it("shows the backend reset time in local time for an active weekly window", async () => {
    const resetAt = "2099-09-14T10:29:00";
    await renderQuotaBinding({
      ...existing,
      usage: { ...weeklyUsage, weekly_reset_at: resetAt },
    });

    const quota = weeklyMeter()!.closest(".native-weekly-quota")!;
    const resetTime = quota.querySelector("time");
    expect(resetTime?.getAttribute("datetime")).toBe(resetAt);
    expect(resetTime?.textContent).toContain("09/14");
    expect(resetTime?.textContent).toContain("10:29");
    expect(quota.textContent).toContain("重置");
    expect(quota.textContent).not.toContain("使用后开始计时");
  });

  it.each([undefined, "2099-09-14T10:29:00", "invalid-date"])(
    "shows the start-on-use state without a moving date when no weekly calls exist (%s)",
    async (resetAt) => {
      await renderQuotaBinding({
        ...existing,
        usage: { ...weeklyUsage, weekly_usd_used: 0, daily_calls: 0, weekly_calls: 0, weekly_reset_at: resetAt },
      });

      const quota = weeklyMeter()!.closest(".native-weekly-quota")!;
      expect(quota.textContent).toContain("使用后开始计时");
      expect(quota.querySelector("time")).toBeNull();
      expect(quota.textContent).not.toContain("Invalid Date");
    },
  );

  it.each([undefined, "", "invalid-date"])(
    "omits unavailable or invalid reset dates for an active weekly window (%s)",
    async (resetAt) => {
      await renderQuotaBinding({ ...existing, usage: { ...weeklyUsage, weekly_reset_at: resetAt } });

      const quota = weeklyMeter()!.closest(".native-weekly-quota")!;
      expect(quota.querySelector("time")).toBeNull();
      expect(quota.textContent).not.toContain("使用后开始计时");
      expect(quota.textContent).not.toContain("Invalid Date");
      expect(quota.textContent).toContain("83%");
    },
  );

  it("refreshes the meter and clears the old reset date after resetting quota", async () => {
    await renderQuotaBinding({
      ...existing,
      usage: { ...weeklyUsage, weekly_reset_at: "2099-09-14T10:29:00" },
    });
    expect(weeklyMeter()!.getAttribute("aria-valuenow")).toBe("83");
    expect(weeklyMeter()!.closest(".native-weekly-quota")!.querySelector("time")).toBeTruthy();

    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue(quotaCatalog({
      ...existing,
      usage: { ...weeklyUsage, weekly_usd_used: 0, daily_calls: 0, weekly_calls: 0, weekly_reset_at: "2099-09-21T10:29:00" },
    }));
    const resetButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("重置额度"));
    await act(async () => {
      resetButton!.click();
      await tick();
    });

    expect(apiMocks.resetNativeKeyBindingQuota).toHaveBeenCalledWith("client-a");
    expect(apiMocks.fetchNativeKeyBindingCatalog).toHaveBeenCalledTimes(2);
    const meter = weeklyMeter()!;
    const quota = meter.closest(".native-weekly-quota")!;
    expect(meter.getAttribute("aria-valuenow")).toBe("100");
    expect(quota.textContent).toContain("剩余 $100.00 / $100.00");
    expect(quota.textContent).toContain("使用后开始计时");
    expect(quota.querySelector("time")).toBeNull();
  });
});

describe("NativeKeyBindingsTab", () => {
  it("confirms and resets all quota counters for a native binding", async () => {
    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });

    const resetButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("重置额度"));
    expect(resetButton).toBeTruthy();
    await act(async () => {
      resetButton!.click();
      await tick();
    });

    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("全部用量记录"));
    expect(apiMocks.resetNativeKeyBindingQuota).toHaveBeenCalledWith("client-a");
    expect(apiMocks.fetchNativeKeyBindingCatalog).toHaveBeenCalledTimes(2);
  });

  it("warns before disabling because the top-level key remains valid", async () => {
    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });

    const toggle = container.querySelector(
      'input[aria-label="切换绑定 client-a 的状态"]',
    ) as HTMLInputElement;
    expect(toggle).toBeTruthy();

    vi.mocked(window.confirm).mockReturnValueOnce(false);
    await act(async () => { toggle.click(); });
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("仍然有效"));
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("默认/自由调度"));
    expect(apiMocks.updateNativeKeyBinding).not.toHaveBeenCalled();

    vi.mocked(window.confirm).mockReturnValueOnce(true);
    await act(async () => {
      toggle.click();
      await tick();
    });
    expect(apiMocks.updateNativeKeyBinding).toHaveBeenCalledWith({ id: "client-a", enabled: false });
  });

  it("re-enables a disabled binding without a confirmation prompt", async () => {
    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
      entries: [{ key_index: 0, key_preview: existing.key_preview, binding: { ...existing, enabled: false } }],
      orphan_bindings: [],
    });
    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });

    const toggle = container.querySelector(
      'input[aria-label="切换绑定 client-a 的状态"]',
    ) as HTMLInputElement;
    await act(async () => {
      toggle.click();
      await tick();
    });

    expect(window.confirm).not.toHaveBeenCalled();
    expect(apiMocks.updateNativeKeyBinding).toHaveBeenCalledWith({ id: "client-a", enabled: true });
  });

  it("keeps the current top-level key when the edit key field is blank", async () => {
    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });

    const editButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("编辑 / 轮换"));
    expect(editButton).toBeTruthy();
    await act(async () => { editButton!.click(); });

    const keyInput = container.querySelector("#native-binding-key") as HTMLInputElement;
    expect(keyInput.value).toBe("");
    expect(keyInput.required).toBe(false);

    const form = container.querySelector(".native-binding-editor form") as HTMLFormElement;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await tick();
    });

    expect(apiMocks.updateNativeKeyBinding).toHaveBeenCalledWith({
      id: "client-a",
      name: "Client A",
      enabled: true,
      group: "team",
      model_access: { mode: "all", models: [] },
    });
    expect(apiMocks.updateNativeKeyBinding.mock.calls[0][0]).not.toHaveProperty("key");
  });

  it("also warns before disabling through the edit dialog", async () => {
    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });

    const editButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("编辑 / 轮换"));
    await act(async () => { editButton!.click(); });

    const enabledInput = container.querySelector(
      '.native-binding-editor input[type="checkbox"]',
    ) as HTMLInputElement;
    const switchLabel = enabledInput.closest("label");
    expect(switchLabel?.classList.contains("native-binding-enable-switch")).toBe(true);
    expect(switchLabel?.textContent).toContain("启用绑定");
    expect(switchLabel?.querySelector(":scope > .track > .thumb")).toBeTruthy();
    expect(enabledInput.labels?.[0]).toBe(switchLabel);
    await act(async () => { enabledInput.click(); });
    expect(enabledInput.checked).toBe(false);

    const form = container.querySelector(".native-binding-editor form") as HTMLFormElement;
    vi.mocked(window.confirm).mockReturnValueOnce(false);
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("仍然有效"));
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("默认/自由调度"));
    expect(apiMocks.updateNativeKeyBinding).not.toHaveBeenCalled();

    vi.mocked(window.confirm).mockReturnValueOnce(true);
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await tick();
    });
    expect(apiMocks.updateNativeKeyBinding).toHaveBeenCalledWith({
      id: "client-a",
      name: "Client A",
      enabled: false,
      group: "team",
      model_access: { mode: "all", models: [] },
    });
  });

  it("renders an explicit group selector and creates a binding from a suggested group", async () => {
    apiMocks.fetchTopLevelAPIKeys.mockResolvedValue([existingSecret, "sk-client-b-secret"]);
    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
      entries: [
        { key_index: 0, key_preview: existing.key_preview, binding: existing },
        { key_index: 1, key_preview: "sk-clie...ecret" },
      ],
      orphan_bindings: [],
    });
    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });

    expect(container.textContent).toContain("Client A");
    expect(container.textContent).toContain("sk-ab...wxyz");
    expect(container.textContent).toContain("仅用于 CPA 顶层 api-keys");
    expect(container.textContent).toContain("停用或删除绑定不会停用或删除 CPA 顶层 API Key");
    expect(container.textContent).toContain("该 Key 仍然有效");
    expect(container.textContent).toContain("恢复默认/自由调度");
    const bindButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("配置绑定"));
    expect(bindButton).toBeTruthy();
    await act(async () => { bindButton!.click(); });

    const groupSelect = container.querySelector("#native-binding-group") as HTMLSelectElement;
    expect(groupSelect.tagName).toBe("SELECT");
    expect(groupSelect.classList.contains("native-binding-group-select")).toBe(true);
    expect(container.querySelector("datalist")).toBeNull();
    const optionValues = Array.from(groupSelect.options)
      .map((option) => (option as HTMLOptionElement).value);
    expect(optionValues).toEqual(expect.arrayContaining([
      "free",
      "team",
      "plus",
      "supported",
      "classify:tenant-a",
      "classify:claude-auth",
      "classify:codex-premium",
      "classify:antigravity-paid",
    ]));
    const optionLabels = Array.from(groupSelect.options).map((option) => option.textContent);
    expect(optionLabels).toEqual(expect.arrayContaining([
      "free",
      "team",
      "plus",
      "supported",
      "classify:tenant-a",
      "classify:claude-auth",
      "classify:codex-premium",
      "classify:antigravity-paid",
      "手动输入其他组…",
    ]));

    await act(async () => {
      change(container.querySelector("#native-binding-id") as HTMLInputElement, "client-b");
      change(container.querySelector("#native-binding-name") as HTMLInputElement, "Client B");
      changeSelect(groupSelect, "classify:tenant-a");
    });

    const form = container.querySelector(".native-binding-editor form") as HTMLFormElement;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await tick();
    });

    expect(apiMocks.createNativeKeyBinding).toHaveBeenCalledWith({
      id: "client-b",
      name: "Client B",
      enabled: true,
      key: "sk-client-b-secret",
      group: "classify:tenant-a",
      model_access: { mode: "allowlist", models: [] },
    });
  });

  it("shows a separate input for a manually entered group", async () => {
    apiMocks.fetchTopLevelAPIKeys.mockResolvedValue([existingSecret, "sk-client-manual-secret"]);
    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
      entries: [
        { key_index: 0, key_preview: existing.key_preview, binding: existing },
        { key_index: 1, key_preview: "sk-clie...ecret" },
      ],
      orphan_bindings: [],
    });
    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });

    const bindButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("配置绑定"));
    await act(async () => { bindButton!.click(); });

    const groupSelect = container.querySelector("#native-binding-group") as HTMLSelectElement;
    const manualOption = Array.from(groupSelect.options)
      .find((option) => option.textContent === "手动输入其他组…");
    expect(manualOption).toBeTruthy();

    await act(async () => {
      change(container.querySelector("#native-binding-id") as HTMLInputElement, "client-manual");
      changeSelect(groupSelect, manualOption!.value);
    });

    const manualInput = container.querySelector("#native-binding-manual-group") as HTMLInputElement;
    expect(manualInput).toBeTruthy();
    expect(manualInput.labels?.[0]?.textContent).toBe("其他凭证组");
    expect(container.querySelector('.map-form-foot button[type="submit"]')?.hasAttribute("disabled")).toBe(true);
    await act(async () => { change(manualInput, "classify:manual"); });
    expect(container.querySelector('.map-form-foot button[type="submit"]')?.hasAttribute("disabled")).toBe(false);

    const form = container.querySelector(".native-binding-editor form") as HTMLFormElement;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await tick();
    });

    expect(apiMocks.createNativeKeyBinding).toHaveBeenCalledWith({
      id: "client-manual",
      name: "顶层 API Key 2",
      enabled: true,
      key: "sk-client-manual-secret",
      group: "classify:manual",
      model_access: { mode: "allowlist", models: [] },
      rpm: undefined,
      daily_usd: undefined,
      weekly_usd: undefined,
    });
  });

  it("lists every host key as a redacted row even when none has a binding", async () => {
    const secrets = [
      "sk-first-top-level-secret-0123456789",
      "sk-second-top-level-secret-9876543210",
      "sk-third-top-level-secret-abcdefghij",
    ];
    const previews = ["sk-firs...56789", "sk-seco...43210", "sk-thir...fghij"];
    apiMocks.fetchTopLevelAPIKeys.mockResolvedValue(secrets);
    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
      entries: previews.map((key_preview, key_index) => ({ key_index, key_preview })),
      orphan_bindings: [],
    });

    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });

    expect(container.querySelectorAll('[data-testid="native-key-row-unbound"]')).toHaveLength(3);
    expect(container.textContent).toContain("共 3 个顶层 Key · 0 个已配置绑定");
    for (const preview of previews) expect(container.textContent).toContain(preview);
    for (const secret of secrets) {
      expect(container.textContent).not.toContain(secret);
      expect(container.innerHTML).not.toContain(secret);
    }
    expect(apiMocks.fetchNativeKeyBindingCatalog).toHaveBeenCalledWith(secrets);
  });

  it("creates a binding directly from a selected host row without rendering its plaintext", async () => {
    const secret = "sk-selected-top-level-secret-0123456789";
    const preview = "sk-sele...56789";
    apiMocks.fetchTopLevelAPIKeys.mockResolvedValue([secret]);
    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
      entries: [{ key_index: 0, key_preview: preview }],
      orphan_bindings: [],
    });

    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });

    const bindButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("配置绑定"));
    expect(bindButton).toBeTruthy();
    await act(async () => { bindButton!.click(); });

    expect((container.querySelector("#native-binding-id") as HTMLInputElement).value).toBe("native-key-1");
    expect((container.querySelector("#native-binding-name") as HTMLInputElement).value).toBe("顶层 API Key 1");
    expect(container.textContent).toContain(preview);
    expect(container.querySelector("#native-binding-key")).toBeNull();
    expect(container.innerHTML).not.toContain(secret);

    await act(async () => {
      changeSelect(
        container.querySelector("#native-binding-group") as HTMLSelectElement,
        "classify:codex-premium",
      );
    });
    const form = container.querySelector(".native-binding-editor form") as HTMLFormElement;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await tick();
    });

    expect(apiMocks.createNativeKeyBinding).toHaveBeenCalledWith({
      id: "native-key-1",
      name: "顶层 API Key 1",
      enabled: true,
      key: secret,
      group: "classify:codex-premium",
      model_access: { mode: "allowlist", models: [] },
    });
  });

  it("submits only the checked provider/model pairs for a new binding", async () => {
    const secret = "sk-model-restricted-top-level-secret-0123456789";
    apiMocks.fetchTopLevelAPIKeys.mockResolvedValue([secret]);
    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
      entries: [{ key_index: 0, key_preview: "sk-mode...56789" }],
      orphan_bindings: [],
    });
    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });
    const bindButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("配置绑定"));
    await act(async () => {
      bindButton!.click();
      await tick();
    });
    await act(async () => {
      changeSelect(container.querySelector("#native-binding-group") as HTMLSelectElement, "team");
    });
    const luna = Array.from(container.querySelectorAll(".native-model-options label"))
      .find((label) => label.textContent?.trim() === "gpt-5.6-luna");
    expect(luna).toBeTruthy();
    await act(async () => {
      (luna!.querySelector("input") as HTMLInputElement).click();
    });

    const form = container.querySelector(".native-binding-editor form") as HTMLFormElement;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await tick();
    });
    expect(apiMocks.createNativeKeyBinding).toHaveBeenCalledWith(expect.objectContaining({
      id: "native-key-1",
      key: secret,
      group: "team",
      model_access: {
        mode: "allowlist",
        models: [{ provider: "codex", model: "gpt-5.6-luna" }],
      },
    }));
  });

  it("creates a binding from an exact multi-select credential allow-list", async () => {
    const secret = "sk-direct-top-level-secret-0123456789";
    apiMocks.fetchTopLevelAPIKeys.mockResolvedValue([secret]);
    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
      entries: [{ key_index: 0, key_preview: "sk-dire...56789" }],
      orphan_bindings: [],
    });

    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });
    const bindButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("配置绑定"));
    await act(async () => {
      bindButton!.click();
      await tick();
    });

    const directMode = container.querySelector('input[name="native-restriction-mode"][value="auth_ids"]') as HTMLInputElement;
    await act(async () => { directMode.click(); });
    expect(container.querySelector("#native-binding-group")).toBeNull();
    const credentialCheckboxes = Array.from(
      container.querySelectorAll<HTMLInputElement>(".native-credential-option input[type=checkbox]"),
    );
    expect(credentialCheckboxes).toHaveLength(2);
    expect(container.textContent).toContain("Auth 目录凭证");
    expect(container.textContent).toContain("AI 提供商凭证");
    expect(container.textContent).toContain("2 个模型");
    const selectAll = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("全选当前结果"));
    await act(async () => {
      selectAll!.click();
    });
    expect(container.textContent).toContain("已选择 2 个凭证");

    const form = container.querySelector(".native-binding-editor form") as HTMLFormElement;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await tick();
    });

    expect(apiMocks.createNativeKeyBinding).toHaveBeenCalledWith(expect.objectContaining({
      id: "native-key-1",
      enabled: true,
      key: secret,
      auth_ids: ["tenant/codex-a.json", "tenant/codex-b.json"],
    }));
    const payload = apiMocks.createNativeKeyBinding.mock.calls[0][0];
    expect(payload).not.toHaveProperty("group");
  });

  it("preserves selected Auth IDs that are no longer returned by the host", async () => {
    const directBinding: NativeKeyBinding = {
      ...existing,
      group: undefined,
      auth_ids: ["tenant/missing.json", "tenant/codex-a.json"],
    };
    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
      entries: [{ key_index: 0, key_preview: directBinding.key_preview, binding: directBinding }],
      orphan_bindings: [],
    });

    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });
    expect(container.textContent).toContain("指定 2 个凭证");
    const editButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("编辑 / 轮换"));
    await act(async () => {
      editButton!.click();
      await tick();
    });
    expect(container.textContent).toContain("已保存但当前不存在");
    expect(container.textContent).toContain("tenant/missing.json");

    const form = container.querySelector(".native-binding-editor form") as HTMLFormElement;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await tick();
    });
    expect(apiMocks.updateNativeKeyBinding).toHaveBeenCalledWith(expect.objectContaining({
      id: "client-a",
      auth_ids: ["tenant/codex-a.json", "tenant/missing.json"],
    }));
    const payload = apiMocks.updateNativeKeyBinding.mock.calls[0][0];
    expect(payload).not.toHaveProperty("group");
  });

  it("round-trips a fork.13 direct binding unchanged when AI provider credentials are available", async () => {
    const legacyDirectBinding: NativeKeyBinding = {
      ...existing,
      group: undefined,
      auth_ids: ["tenant/missing.json", "tenant/codex-a.json"],
      model_access: {
        mode: "allowlist",
        models: [
          { provider: "codex", model: "retired-model" },
          { provider: "gemini", model: "gemini-2.5-pro" },
        ],
      },
    };
    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
      entries: [{
        key_index: 0,
        key_preview: legacyDirectBinding.key_preview,
        binding: legacyDirectBinding,
      }],
      orphan_bindings: [],
    });

    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });
    const editButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("编辑 / 轮换"));
    await act(async () => {
      editButton!.click();
      await tick();
    });

    expect(container.textContent).toContain("AI 提供商凭证");
    expect(container.textContent).toContain("tenant/missing.json");
    expect(container.textContent).toContain("retired-model");

    const form = container.querySelector(".native-binding-editor form") as HTMLFormElement;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await tick();
    });

    expect(apiMocks.updateNativeKeyBinding).toHaveBeenCalledWith({
      id: "client-a",
      name: "Client A",
      enabled: true,
      auth_ids: ["tenant/codex-a.json", "tenant/missing.json"],
      model_access: {
        mode: "allowlist",
        models: [
          { provider: "codex", model: "retired-model" },
          { provider: "gemini", model: "gemini-2.5-pro" },
        ],
      },
    });
  });

  it("shows a degraded direct binding as requiring credential reselection", async () => {
    const degraded: NativeKeyBinding = {
      ...existing,
      group: undefined,
      auth_ids: undefined,
      needs_reselection: true,
    };
    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
      entries: [{ key_index: 0, key_preview: degraded.key_preview, binding: degraded }],
      orphan_bindings: [],
    });

    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });
    expect(container.textContent).toContain("直接凭证记录已丢失，请重新选择");
    expect(container.textContent).not.toContain("@access-guard/direct-auth-ids");

    const editButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("编辑 / 轮换"));
    await act(async () => {
      editButton!.click();
      await tick();
    });
    const directMode = container.querySelector('input[name="native-restriction-mode"][value="auth_ids"]') as HTMLInputElement;
    expect(directMode.checked).toBe(true);
    expect(container.textContent).toContain("已选择 0 个凭证");
    const submit = container.querySelector('.map-form-foot button[type="submit"]') as HTMLButtonElement;
    expect(submit.disabled).toBe(true);

    const credential = container.querySelector(".native-credential-option input[type=checkbox]") as HTMLInputElement;
    await act(async () => { credential.click(); });
    expect(submit.disabled).toBe(false);
    const form = container.querySelector(".native-binding-editor form") as HTMLFormElement;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await tick();
    });
    expect(apiMocks.updateNativeKeyBinding).toHaveBeenCalledWith(expect.objectContaining({
      id: "client-a",
      auth_ids: ["tenant/codex-a.json"],
    }));
    expect(apiMocks.updateNativeKeyBinding.mock.calls[0][0]).not.toHaveProperty("group");
  });

  it("preserves an existing group that is not in the suggestion list", async () => {
    const legacy = { ...existing, group: "classify:legacy-customer" };
    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
      entries: [{ key_index: 0, key_preview: legacy.key_preview, binding: legacy }],
      orphan_bindings: [],
    });

    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });

    const editButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("编辑 / 轮换"));
    await act(async () => { editButton!.click(); });

    const groupSelect = container.querySelector("#native-binding-group") as HTMLSelectElement;
    expect(groupSelect.selectedOptions[0]?.textContent).toBe("手动输入其他组…");
    expect((container.querySelector("#native-binding-manual-group") as HTMLInputElement).value)
      .toBe("classify:legacy-customer");

    const form = container.querySelector(".native-binding-editor form") as HTMLFormElement;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await tick();
    });

    expect(apiMocks.updateNativeKeyBinding).toHaveBeenCalledWith({
      id: "client-a",
      name: "Client A",
      enabled: true,
      group: "classify:legacy-customer",
      model_access: { mode: "all", models: [] },
    });
  });

  it("keeps bindings whose top-level key was removed visible as orphan records", async () => {
    apiMocks.fetchTopLevelAPIKeys.mockResolvedValue([]);
    apiMocks.fetchNativeKeyBindingCatalog.mockResolvedValue({
      entries: [],
      orphan_bindings: [existing],
    });

    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });

    expect(container.querySelectorAll('[data-testid="native-key-row-orphan"]')).toHaveLength(1);
    expect(container.textContent).toContain("顶层已删除");
    expect(container.textContent).toContain("已不在 CPA 顶层 api-keys 中");
    expect(container.textContent).toContain("Client A");
  });

  it("reports a host key load failure instead of claiming the host has no keys", async () => {
    apiMocks.fetchTopLevelAPIKeys.mockRejectedValue(new Error("management unavailable"));

    await act(async () => {
      root = createRoot(container);
      root.render(<NativeKeyBindingsTab />);
      await tick();
    });

    expect(container.querySelector('[role="alert"]')?.textContent).toContain("management unavailable");
    expect(container.textContent).toContain("顶层 Key 列表加载失败");
    expect(container.textContent).not.toContain("CPA 尚未配置顶层 API Key");
    expect(apiMocks.fetchNativeKeyBindingCatalog).not.toHaveBeenCalled();
  });
});
