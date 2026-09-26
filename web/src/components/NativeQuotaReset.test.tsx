import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { _resetLocale } from "../i18n";
import type { NativeQuotaResetBinding } from "../api/nativeQuotaReset";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const client = vi.hoisted(() => ({ loadBindings: vi.fn(), reset: vi.fn() }));
const createClient = vi.hoisted(() => vi.fn());
vi.mock("../api/nativeQuotaReset", () => ({ createNativeQuotaResetClient: createClient }));

import NativeQuotaReset from "./NativeQuotaReset";

const secrets = [
  "sk-quota-reset-alpha-private-000001",
  "sk-quota-reset-beta-private-000002",
  "sk-quota-reset-gamma-private-000003",
];

function makeBindings(): NativeQuotaResetBinding[] {
  return [
    { id: "alpha", name: "Alpha", keyPreview: "sk-alpha...0001", enabled: true, createdAt: "2026-09-26T08:00:00Z", key: secrets[0] },
    { id: "beta", name: "Beta", keyPreview: "sk-beta...0002", enabled: false, createdAt: "2026-09-26T08:01:00Z", key: secrets[1] },
    { id: "gamma", name: "Gamma", keyPreview: "sk-gamma...0003", enabled: true, createdAt: "2026-09-26T08:02:00Z", key: secrets[2] },
  ];
}

const copy = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));
let bindings: NativeQuotaResetBinding[];
let container: HTMLDivElement;
let root: ReturnType<typeof createRoot> | null = null;
let onReset: ReturnType<typeof vi.fn>;
let onClose: ReturnType<typeof vi.fn>;

function button(text: string): HTMLButtonElement {
  const item = Array.from(container.querySelectorAll("button")).find((candidate) => candidate.textContent?.trim() === text);
  expect(item, `button ${text}`).toBeTruthy();
  return item!;
}

function submitButton(): HTMLButtonElement {
  const item = container.querySelector<HTMLButtonElement>(".native-batch-actions .danger-outline");
  expect(item, "quota reset submit button").toBeTruthy();
  return item!;
}

function checkbox(preview: string): HTMLInputElement {
  const label = Array.from(container.querySelectorAll(".native-quota-reset-key")).find((item) => item.textContent?.includes(preview));
  expect(label, `binding ${preview}`).toBeTruthy();
  const item = label!.querySelector<HTMLInputElement>('input[type="checkbox"]');
  expect(item).toBeTruthy();
  return item!;
}

async function click(element: HTMLElement) {
  await act(async () => { element.click(); await tick(); });
}

async function search(value: string) {
  const element = container.querySelector<HTMLInputElement>("#native-quota-reset-search");
  expect(element).toBeTruthy();
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(element, value);
    element!.dispatchEvent(new Event("input", { bubbles: true }));
    await tick();
  });
}

async function mount() {
  await act(async () => {
    root = createRoot(container);
    root.render(<NativeQuotaReset onClose={onClose} onReset={onReset} />);
    await tick();
  });
}

function assertNoSecrets() {
  for (const secret of secrets) {
    expect(container.innerHTML).not.toContain(secret);
    expect(JSON.stringify(Object.entries(localStorage))).not.toContain(secret);
    expect(JSON.stringify(Object.entries(sessionStorage))).not.toContain(secret);
  }
}

beforeEach(() => {
  vi.resetAllMocks();
  _resetLocale("zh-CN");
  bindings = makeBindings();
  container = document.createElement("div");
  document.body.appendChild(container);
  onClose = vi.fn();
  onReset = vi.fn().mockResolvedValue(undefined);
  createClient.mockReturnValue(client);
  client.loadBindings.mockImplementation(async () => copy(bindings));
  client.reset.mockImplementation(async (ids: string[]) => ({ reset: true, ids, count: ids.length }));
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container.remove();
  vi.restoreAllMocks();
});

describe("NativeQuotaReset", () => {
  it("selects every current binding including disabled bindings without resetting on open", async () => {
    const writes = vi.spyOn(Storage.prototype, "setItem");
    await mount();
    expect(createClient).toHaveBeenCalledTimes(1);
    expect(client.loadBindings).toHaveBeenCalledTimes(1);
    expect(container.querySelectorAll(".native-quota-reset-key")).toHaveLength(3);
    for (const binding of bindings) {
      expect(checkbox(binding.keyPreview).checked).toBe(true);
      expect(checkbox(binding.keyPreview).disabled).toBe(false);
    }
    expect(submitButton().textContent).toBe("重置 3 个 Key");
    expect(submitButton().disabled).toBe(false);
    expect(container.textContent).toContain("不可撤销");
    expect(client.reset).not.toHaveBeenCalled();
    expect(onReset).not.toHaveBeenCalled();
    assertNoSecrets();
    for (const secret of secrets) expect(JSON.stringify(writes.mock.calls)).not.toContain(secret);
  });

  it("refreshes bindings and resets exactly the chosen subset once", async () => {
    await mount();
    await click(checkbox("sk-beta...0002"));
    expect(submitButton().textContent).toBe("重置 2 个 Key");
    await click(submitButton());
    expect(client.loadBindings).toHaveBeenCalledTimes(2);
    expect(client.reset).toHaveBeenCalledWith(["alpha", "gamma"]);
    expect(client.reset).toHaveBeenCalledTimes(1);
    expect(onReset).toHaveBeenCalledWith(2);
    expect(container.textContent).toContain("已重置 2 个 Key");
    const submit = Array.from(container.querySelectorAll("button")).find((item) => /^重置 \d+ 个 Key$/.test(item.textContent?.trim() ?? ""));
    expect(!submit || submit.disabled).toBe(true);
    assertNoSecrets();
  });

  it("keeps selected bindings while searching by name and redacted key", async () => {
    await mount();
    await click(checkbox("sk-beta...0002"));
    await search("Beta");
    expect(container.querySelectorAll(".native-quota-reset-key")).toHaveLength(1);
    expect(checkbox("sk-beta...0002").checked).toBe(false);
    await click(checkbox("sk-beta...0002"));
    await search("sk-alpha");
    expect(container.querySelectorAll(".native-quota-reset-key")).toHaveLength(1);
    expect(checkbox("sk-alpha...0001").checked).toBe(true);
    await search("");
    expect(container.querySelectorAll(".native-quota-reset-key")).toHaveLength(3);
    expect(submitButton().textContent).toBe("重置 3 个 Key");
    expect(client.reset).not.toHaveBeenCalled();
    assertNoSecrets();
  });

  it("disables reset when no bindings are selected", async () => {
    await mount();
    for (const binding of bindings) await click(checkbox(binding.keyPreview));
    expect(submitButton().disabled).toBe(true);
    await click(submitButton());
    expect(client.loadBindings).toHaveBeenCalledTimes(1);
    expect(client.reset).not.toHaveBeenCalled();
  });

  it("clears selection and selects only the current search results", async () => {
    await mount();
    const tools = container.querySelector(".native-batch-selection-tools")!;
    await click(tools.querySelectorAll<HTMLButtonElement>("button")[1]);
    expect(submitButton().disabled).toBe(true);
    await search("Beta");
    await click(tools.querySelectorAll<HTMLButtonElement>("button")[0]);
    expect(checkbox("sk-beta...0002").checked).toBe(true);
    await search("");
    expect(checkbox("sk-alpha...0001").checked).toBe(false);
    expect(checkbox("sk-gamma...0003").checked).toBe(false);
    expect(submitButton().textContent).toBe("重置 1 个 Key");
    await click(submitButton());
    expect(client.reset).toHaveBeenCalledWith(["beta"]);
  });

  it("manual refresh retains unchanged selection and deselects changed or newly added bindings", async () => {
    await mount();
    await click(checkbox("sk-beta...0002"));
    bindings[0].key = "sk-reset-replacement-private-000004";
    bindings[0].keyPreview = "sk-replaced...0004";
    bindings.push({ id: "delta", name: "Delta", keyPreview: "sk-delta...0005", enabled: true, createdAt: "2026-09-27T08:00:00Z", key: "sk-reset-delta-private-000005" });
    await click(container.querySelector(".native-batch-actions")!.querySelectorAll<HTMLButtonElement>("button")[1]);
    expect(client.loadBindings).toHaveBeenCalledTimes(2);
    expect(checkbox("sk-replaced...0004").checked).toBe(false);
    expect(checkbox("sk-beta...0002").checked).toBe(false);
    expect(checkbox("sk-gamma...0003").checked).toBe(true);
    expect(checkbox("sk-delta...0005").checked).toBe(false);
    expect(container.innerHTML).not.toContain("sk-reset-replacement-private-000004");
    expect(container.innerHTML).not.toContain("sk-reset-delta-private-000005");
    await click(submitButton());
    expect(client.reset).toHaveBeenCalledWith(["gamma"]);
  });

  it("starts with reset disabled when there are no eligible bindings", async () => {
    bindings = [];
    await mount();
    expect(container.querySelectorAll(".native-quota-reset-key")).toHaveLength(0);
    expect(submitButton().disabled).toBe(true);
    await click(submitButton());
    expect(client.reset).not.toHaveBeenCalled();
  });

  it("does not expand selection when a new binding appears during the final refresh", async () => {
    await mount();
    const addedSecret = "sk-quota-reset-added-private-000004";
    bindings.push({ id: "new", name: "New", keyPreview: "sk-new...0004", enabled: true, createdAt: "2026-09-27T09:00:00Z", key: addedSecret });
    await click(submitButton());
    expect(client.reset).toHaveBeenCalledWith(["alpha", "beta", "gamma"]);
    expect(onReset).toHaveBeenCalledWith(3);
    expect(container.innerHTML).not.toContain(addedSecret);
  });

  it("keeps selecting by binding identity if the refreshed list order changes", async () => {
    await mount();
    await click(checkbox("sk-beta...0002"));
    bindings.reverse();
    await click(submitButton());
    expect(client.reset).toHaveBeenCalledWith(expect.arrayContaining(["alpha", "gamma"]));
    expect(client.reset.mock.calls[0][0]).toHaveLength(2);
  });

  it("allows an unchanged legacy binding whose creation timestamp is absent", async () => {
    bindings[0].createdAt = "";
    await mount();
    await click(checkbox("sk-beta...0002"));
    await click(checkbox("sk-gamma...0003"));
    await click(submitButton());
    expect(client.reset).toHaveBeenCalledWith(["alpha"]);
    expect(onReset).toHaveBeenCalledWith(1);
    expect(container.textContent).toContain("已重置 1 个 Key");
  });

  it.each(["missing", "key_changed", "id_recreated"])("blocks the entire reset when a selected binding is %s", async (change) => {
    await mount();
    if (change === "missing") bindings = bindings.filter((binding) => binding.id !== "beta");
    if (change === "key_changed") bindings[1].key = "sk-replaced-private-secret";
    if (change === "id_recreated") bindings[1].createdAt = "2026-09-27T12:00:00Z";
    await click(submitButton());
    expect(client.loadBindings).toHaveBeenCalledTimes(2);
    expect(client.reset).not.toHaveBeenCalled();
    expect(onReset).not.toHaveBeenCalled();
    expect(container.querySelector('[role="alert"]')).toBeTruthy();
    assertNoSecrets();
  });

  it("does not block chosen bindings when an unselected binding disappears", async () => {
    await mount();
    await click(checkbox("sk-beta...0002"));
    bindings = bindings.filter((binding) => binding.id !== "beta");
    await click(submitButton());
    expect(client.reset).toHaveBeenCalledWith(["alpha", "gamma"]);
  });

  it("does not reset if the final binding refresh fails and never renders its exception", async () => {
    await mount();
    client.loadBindings.mockRejectedValueOnce(new Error(`refresh unavailable ${secrets[0]}`));
    await click(submitButton());
    expect(client.reset).not.toHaveBeenCalled();
    expect(onReset).not.toHaveBeenCalled();
    expect(container.querySelector('[role="alert"]')).toBeTruthy();
    expect(container.textContent).not.toContain("refresh unavailable");
    assertNoSecrets();
  });

  it("shows a safe load error when no initial binding inventory is available", async () => {
    client.loadBindings.mockRejectedValue(new Error(`cannot load ${secrets[1]}`));
    await mount();
    expect(container.querySelector('[role="alert"]')).toBeTruthy();
    expect(client.reset).not.toHaveBeenCalled();
    expect(container.textContent).not.toContain("cannot load");
    assertNoSecrets();
  });

  it("renders a controlled reset error without exposing a server exception or full key", async () => {
    client.reset.mockRejectedValue({ response: { status: 500, data: { error: `backend failed ${secrets[2]}` } } });
    await mount();
    await click(submitButton());
    expect(client.reset).toHaveBeenCalledTimes(1);
    expect(onReset).not.toHaveBeenCalled();
    expect(container.querySelector('[role="alert"]')).toBeTruthy();
    expect(container.textContent).not.toContain("backend failed");
    assertNoSecrets();
  });

  it("prevents duplicate reset submissions while the mutation is pending", async () => {
    let finish!: (value: { reset: true; ids: string[]; count: number }) => void;
    client.reset.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
    await mount();
    await click(submitButton());
    expect(submitButton().disabled).toBe(true);
    await click(submitButton());
    expect(client.reset).toHaveBeenCalledTimes(1);
    expect(onReset).not.toHaveBeenCalled();
    await act(async () => { finish({ reset: true, ids: ["alpha", "beta", "gamma"], count: 3 }); await tick(); });
    expect(onReset).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain("已重置 3 个 Key");
  });

  it("keeps a successful reset successful when the parent display refresh fails", async () => {
    onReset.mockRejectedValue(new Error(`parent refresh ${secrets[0]}`));
    await mount();
    await click(submitButton());
    expect(client.reset).toHaveBeenCalledTimes(1);
    expect(onReset).toHaveBeenCalledWith(3);
    expect(container.textContent).toContain("已重置 3 个 Key");
    expect(container.textContent).not.toContain("parent refresh");
    const submit = Array.from(container.querySelectorAll("button")).find((item) => /^重置 \d+ 个 Key$/.test(item.textContent?.trim() ?? ""));
    expect(!submit || submit.disabled).toBe(true);
    assertNoSecrets();
  });

  it("uses the server-confirmed count for success and the parent callback", async () => {
    client.reset.mockResolvedValue({ reset: true, ids: ["alpha", "gamma"], count: 2 });
    await mount();
    await click(submitButton());
    expect(container.textContent).toContain("已重置 2 个 Key");
    expect(onReset).toHaveBeenCalledWith(2);
  });

  it("can close through Escape without making a reset request", async () => {
    await mount();
    await act(async () => {
      container.querySelector('[role="dialog"]')!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      await tick();
    });
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(client.reset).not.toHaveBeenCalled();
    expect(onReset).not.toHaveBeenCalled();
  });

  it.each(["取消", "关闭"])("can %s without resetting any binding", async (label) => {
    await mount();
    await click(button(label));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(client.reset).not.toHaveBeenCalled();
    expect(onReset).not.toHaveBeenCalled();
  });

  it("prevents closing or double submission while the final inventory check is pending", async () => {
    let finish!: (value: NativeQuotaResetBinding[]) => void;
    await mount();
    client.loadBindings.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    await click(submitButton());
    expect(submitButton().disabled).toBe(true);
    expect(button("关闭").disabled).toBe(true);
    expect(button("取消").disabled).toBe(true);
    await click(submitButton());
    await act(async () => {
      container.querySelector('[role="dialog"]')!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      await tick();
    });
    expect(onClose).not.toHaveBeenCalled();
    expect(client.loadBindings).toHaveBeenCalledTimes(2);
    expect(client.reset).not.toHaveBeenCalled();
    await act(async () => { finish(copy(bindings)); await tick(); });
    expect(client.reset).toHaveBeenCalledTimes(1);
    expect(onReset).toHaveBeenCalledTimes(1);
  });

  it("traps keyboard focus in the dialog and restores focus when unmounted", async () => {
    const opener = document.createElement("button");
    opener.textContent = "Open quota reset";
    document.body.appendChild(opener);
    opener.focus();
    try {
      await mount();
      const dialog = container.querySelector<HTMLElement>('[role="dialog"]')!;
      const targets = Array.from(dialog.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), [tabindex="0"]'));
      const first = targets[0];
      const last = targets[targets.length - 1];
      expect(document.activeElement).toBe(dialog);
      await act(async () => { dialog.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true })); });
      expect(document.activeElement).toBe(first);
      await act(async () => { first.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", shiftKey: true, bubbles: true, cancelable: true })); });
      expect(document.activeElement).toBe(last);
      await act(async () => { last.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true, cancelable: true })); });
      expect(document.activeElement).toBe(first);
      await act(async () => { root!.unmount(); root = null; });
      expect(document.activeElement).toBe(opener);
      expect(client.reset).not.toHaveBeenCalled();
    } finally {
      opener.remove();
    }
  });
});
