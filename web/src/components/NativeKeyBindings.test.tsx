import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ClassifyRule, NativeKeyBinding } from "../types";
import { _resetLocale } from "../i18n";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const apiMocks = vi.hoisted(() => ({
  fetchNativeKeyBindings: vi.fn(),
  fetchClassifyRules: vi.fn(),
  createNativeKeyBinding: vi.fn(),
  updateNativeKeyBinding: vi.fn(),
  deleteNativeKeyBinding: vi.fn(),
}));

vi.mock("../api/mappings", () => apiMocks);

import NativeKeyBindingsTab, { buildNativeBindingGroupOptions } from "./NativeKeyBindings";

const existing: NativeKeyBinding = {
  id: "client-a",
  name: "Client A",
  enabled: true,
  key_preview: "sk-ab...wxyz",
  group: "team",
};

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot> | null = null;
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

function change(input: HTMLInputElement, value: string) {
  // Bypass React's per-element value tracker so the synthetic onChange sees
  // this as a real browser edit under jsdom.
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  setter?.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

beforeEach(() => {
  _resetLocale("zh-CN");
  vi.spyOn(window, "confirm").mockReturnValue(true);
  container = document.createElement("div");
  document.body.appendChild(container);
  apiMocks.fetchNativeKeyBindings.mockResolvedValue([existing]);
  apiMocks.fetchClassifyRules.mockResolvedValue([
    { name: "vip", field: "filename", pattern: "vip", group: "VIP", enabled: true },
  ] satisfies ClassifyRule[]);
  apiMocks.createNativeKeyBinding.mockResolvedValue(existing);
  apiMocks.updateNativeKeyBinding.mockResolvedValue(existing);
  apiMocks.deleteNativeKeyBinding.mockResolvedValue(undefined);
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

describe("NativeKeyBindingsTab", () => {
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
    apiMocks.fetchNativeKeyBindings.mockResolvedValue([{ ...existing, enabled: false }]);
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
    });
  });

  it("renders existing bindings and creates one with free-form group support", async () => {
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
    expect(container.textContent).toContain("仅支持经过正常 AuthManager/Scheduler");
    expect(container.textContent).toContain("CPA Home");
    expect(container.textContent).toContain("Alpha Search");
    expect(container.textContent).toContain("Codex Live/Realtime");
    expect(container.textContent).toContain("plugin-executor");
    expect(container.textContent).toContain("caller_scope");
    expect(container.textContent).toContain("quota-exceeded.antigravity-credits: false");
    expect(container.textContent).toContain("可能选择组外凭证");

    const newButton = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent?.includes("新建绑定"));
    expect(newButton).toBeTruthy();
    await act(async () => { newButton!.click(); });

    const optionValues = Array.from(container.querySelectorAll("datalist option"))
      .map((option) => (option as HTMLOptionElement).value);
    expect(optionValues).toContain("classify:vip");

    await act(async () => {
      change(container.querySelector("#native-binding-id") as HTMLInputElement, "client-b");
      change(container.querySelector("#native-binding-name") as HTMLInputElement, "Client B");
      change(container.querySelector("#native-binding-key") as HTMLInputElement, "sk-client-b-secret");
      // This value is intentionally not in the datalist: manual groups remain supported.
      change(container.querySelector("#native-binding-group") as HTMLInputElement, "classify:manual");
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
      group: "classify:manual",
    });
  });
});
