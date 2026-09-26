import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { _resetLocale } from "../i18n";
import type {
  NativeBindingBatchInventory,
  NativeBindingBatchRequest,
  NativeBindingOperation,
  NativeBindingPreview,
} from "../api/nativeBindingBatch";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const client = vi.hoisted(() => ({
  loadInventory: vi.fn(),
  history: vi.fn(),
  preview: vi.fn(),
  apply: vi.fn(),
  previewRollback: vi.fn(),
  rollback: vi.fn(),
}));
const createClient = vi.hoisted(() => vi.fn());
vi.mock("../api/nativeBindingBatch", () => ({ createNativeBindingBatchClient: createClient }));

import NativeBindingBatch from "./NativeBindingBatch";

const secrets = [
  "sk-batch-alpha-private-secret-000001",
  "sk-batch-beta-private-secret-000002",
  "sk-batch-gamma-private-secret-000003",
];

function makeInventory(): NativeBindingBatchInventory {
  return {
    apiKeys: [...secrets],
    catalog: {
      entries: [
        {
          key_index: 0,
          key_preview: "sk-alpha...0001",
          binding: {
            id: "alpha", name: "Alpha", key_preview: "sk-alpha...0001", enabled: false,
            group: "team", round_robin: true, rpm: 37, daily_usd: 4, weekly_usd: 19,
            model_access: { mode: "allowlist", models: [{ provider: "codex", model: "spark-test" }] },
          },
        },
        {
          key_index: 1,
          key_preview: "sk-beta...0002",
          binding: {
            id: "beta", name: "Beta", key_preview: "sk-beta...0002", enabled: true,
            auth_ids: ["auth-old"], round_robin: false,
            model_access: { mode: "all", models: [] },
          },
        },
        { key_index: 2, key_preview: "sk-gamma...0003" },
      ],
      orphan_bindings: [],
    },
    credentials: {
      credentials: [
        { id: "auth-a", provider: "codex", email: "alice@example.test", label: "Translation A", plan: "team", source: "auth_file" },
        { id: "auth-b", provider: "codex", email: "bob@example.test", label: "Translation B", plan: "plus", source: "auth_file" },
        { id: "auth-c", provider: "gemini", email: "carol@example.test", label: "Other provider", source: "auth_file" },
        { id: "auth-old", provider: "codex", email: "old@example.test", source: "auth_file" },
      ],
      identitiesComplete: true,
      groups: { team: ["auth-a"] },
      unavailableGroups: [],
      groupsAvailable: true,
    },
  };
}

function makePreview(): NativeBindingPreview {
  return {
    revision: "server-revision-1",
    changes: [
      { binding_id: "alpha", name: "Alpha", key_preview: "sk-alpha...0001", before: { group: "team" }, after: { auth_ids: ["auth-a", "auth-b"] } },
      { binding_id: "new-gamma", name: "", key_preview: "sk-gamma...0003", before: null, after: { auth_ids: ["auth-a", "auth-b"] } },
    ],
    conflicts: [], can_apply: true, noop: false, warnings: [],
  };
}

function makeOperation(): NativeBindingOperation {
  return {
    id: "operation-1", kind: "batch", created_at: "2026-09-26T08:00:00Z",
    changes: makePreview().changes,
  };
}

const copy = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T;
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));
let inventory: NativeBindingBatchInventory;
let container: HTMLDivElement;
let root: ReturnType<typeof createRoot> | null = null;
let onChanged: ReturnType<typeof vi.fn>;
let onClose: ReturnType<typeof vi.fn>;

function button(text: string): HTMLButtonElement {
  const found = Array.from(container.querySelectorAll("button")).find((item) => item.textContent?.trim() === text);
  expect(found, `button ${text}`).toBeTruthy();
  return found!;
}

async function click(element: HTMLElement) {
  await act(async () => { element.click(); await tick(); });
}

async function input(selector: string, value: string) {
  const element = container.querySelector<HTMLInputElement>(selector)!;
  expect(element, selector).toBeTruthy();
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(element, value);
    element.dispatchEvent(new Event("input", { bubbles: true }));
    await tick();
  });
}

function checkbox(kind: "key" | "credential", text: string): HTMLInputElement {
  const label = Array.from(container.querySelectorAll(`.native-batch-${kind}`))
    .find((item) => item.textContent?.includes(text));
  expect(label, `${kind} ${text}`).toBeTruthy();
  const element = label!.querySelector<HTMLInputElement>('input[type="checkbox"]');
  expect(element).toBeTruthy();
  return element!;
}

async function mount(initialView: "select" | "history" | "undo" = "select") {
  await act(async () => {
    root = createRoot(container);
    root.render(<NativeBindingBatch initialView={initialView} onClose={onClose} onChanged={onChanged} />);
    await tick();
  });
}

async function choosePair() {
  expect(checkbox("key", "sk-alpha...0001").checked).toBe(true);
  expect(checkbox("key", "sk-beta...0002").checked).toBe(true);
  expect(checkbox("key", "sk-gamma...0003").checked).toBe(true);
  await click(checkbox("key", "sk-beta...0002"));
  await click(checkbox("credential", "alice@example.test"));
  await click(checkbox("credential", "bob@example.test"));
}

async function previewPair() {
  await mount();
  await choosePair();
  await click(button("预览变更"));
}

function assertNoSecret() {
  for (const secret of secrets) {
    expect(container.innerHTML).not.toContain(secret);
    expect(JSON.stringify(Object.entries(localStorage))).not.toContain(secret);
    expect(JSON.stringify(Object.entries(sessionStorage))).not.toContain(secret);
  }
}

beforeEach(() => {
  vi.resetAllMocks();
  _resetLocale("zh-CN");
  inventory = makeInventory();
  container = document.createElement("div");
  document.body.appendChild(container);
  onChanged = vi.fn().mockResolvedValue(undefined);
  onClose = vi.fn();
  createClient.mockReturnValue(client);
  client.loadInventory.mockImplementation(async () => copy(inventory));
  client.history.mockResolvedValue([makeOperation()]);
  client.preview.mockResolvedValue(makePreview());
  client.apply.mockResolvedValue({ operation: makeOperation(), changed: 2, noop: false });
  client.previewRollback.mockResolvedValue({
    ...makePreview(), revision: "rollback-revision-1",
    changes: makePreview().changes.map((change) => ({ ...change, before: change.after, after: change.before })),
  });
  client.rollback.mockResolvedValue({ operation: { ...makeOperation(), id: "rollback-1", kind: "rollback", source_operation_id: "operation-1" }, changed: 2, noop: false });
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container.remove();
  vi.restoreAllMocks();
});

describe("NativeBindingBatch", () => {
  it("previews and applies only the selected keys and credentials after refreshing inventories", async () => {
    const writes = vi.spyOn(Storage.prototype, "setItem");
    await previewPair();
    expect(client.loadInventory).toHaveBeenCalledTimes(2);
    expect(client.preview).toHaveBeenCalledWith({
      api_keys: secrets, selected_indices: [0, 2], auth_ids: ["auth-a", "auth-b"],
      available_auth_ids: ["auth-a", "auth-b", "auth-c", "auth-old"], catalog_complete: true,
    });
    expect(client.apply).not.toHaveBeenCalled();
    assertNoSecret();

    await click(button("确认批量绑定"));
    expect(client.loadInventory).toHaveBeenCalledTimes(3);
    expect(client.apply).toHaveBeenCalledWith({
      api_keys: secrets, selected_indices: [0, 2], auth_ids: ["auth-a", "auth-b"],
      available_auth_ids: ["auth-a", "auth-b", "auth-c", "auth-old"], catalog_complete: true,
      expected_revision: "server-revision-1",
    });
    const submitted = client.apply.mock.calls[0][0] as NativeBindingBatchRequest;
    for (const field of ["enabled", "round_robin", "rpm", "daily_usd", "weekly_usd", "model_access", "force"]) {
      expect(submitted).not.toHaveProperty(field);
    }
    expect(onChanged).toHaveBeenCalledTimes(1);
    assertNoSecret();
    for (const secret of secrets) expect(JSON.stringify(writes.mock.calls)).not.toContain(secret);
  });

  it("keeps selected credentials while searching by their identities", async () => {
    await mount();
    await click(checkbox("key", "sk-alpha...0001"));
    await input("#native-batch-credential-search", "alice@example.test");
    expect(container.querySelectorAll(".native-batch-credential")).toHaveLength(1);
    await click(checkbox("credential", "alice@example.test"));
    await input("#native-batch-credential-search", "bob@example.test");
    expect(container.querySelectorAll(".native-batch-credential")).toHaveLength(1);
    await click(checkbox("credential", "bob@example.test"));
    await input("#native-batch-credential-search", "");
    expect(checkbox("credential", "alice@example.test").checked).toBe(true);
    expect(checkbox("credential", "bob@example.test").checked).toBe(true);
    await click(button("预览变更"));
    expect(client.preview).toHaveBeenCalledWith(expect.objectContaining({ auth_ids: ["auth-a", "auth-b"] }));
  });

  it("keeps key selections while searching without exposing raw key values", async () => {
    await mount();
    await click(checkbox("key", "sk-beta...0002"));
    await input("#native-batch-key-search", "Beta");
    expect(container.querySelectorAll(".native-batch-key")).toHaveLength(1);
    await click(checkbox("key", "sk-beta...0002"));
    await input("#native-batch-key-search", "");
    expect(checkbox("key", "sk-alpha...0001").checked).toBe(true);
    expect(checkbox("key", "sk-beta...0002").checked).toBe(true);
    assertNoSecret();
  });

  it("does not silently select new host keys or credentials discovered after the preview", async () => {
    await previewPair();
    const addedSecret = "sk-batch-added-private-secret-000004";
    inventory.apiKeys.push(addedSecret);
    inventory.catalog.entries.push({ key_index: 3, key_preview: "sk-added...0004" });
    inventory.credentials.credentials.push({ id: "auth-new", provider: "codex", email: "new@example.test" });
    await click(button("确认批量绑定"));
    expect(client.apply).toHaveBeenCalledWith({
      api_keys: [...secrets, addedSecret], selected_indices: [0, 2], auth_ids: ["auth-a", "auth-b"],
      available_auth_ids: ["auth-a", "auth-b", "auth-c", "auth-new", "auth-old"], catalog_complete: true,
      expected_revision: "server-revision-1",
    });
    expect(container.innerHTML).not.toContain(addedSecret);
    await click(button("返回选择"));
    expect(checkbox("key", "sk-added...0004").checked).toBe(false);
    expect(checkbox("credential", "new@example.test").checked).toBe(false);
  });

  it("selects only verified filtered credentials while retaining the complete inventory in requests", async () => {
    inventory.credentials.credentials.find((item) => item.id === "auth-b")!.identityVerified = false;
    inventory.credentials.credentials.find((item) => item.id === "auth-c")!.disabled = true;
    inventory.credentials.credentials.find((item) => item.id === "auth-c")!.unavailable = true;
    await mount();
    await input("#native-batch-credential-search", "Translation");
    expect(checkbox("credential", "bob@example.test").disabled).toBe(true);
    const section = container.querySelector<HTMLInputElement>("#native-batch-credential-search")!.closest("section")!;
    const select = Array.from(section.querySelectorAll("button")).find((item) => item.textContent?.trim() === "全选当前结果")!;
    await click(select);
    expect(checkbox("credential", "alice@example.test").checked).toBe(true);
    expect(checkbox("credential", "bob@example.test").checked).toBe(false);
    await click(button("预览变更"));
    expect(client.preview).toHaveBeenCalledWith(expect.objectContaining({
      auth_ids: ["auth-a"], available_auth_ids: ["auth-a", "auth-b", "auth-c", "auth-old"],
    }));
  });

  it("allows complete identities even when classification group preview is unavailable", async () => {
    inventory.credentials.groupsAvailable = false;
    inventory.credentials.groups = {};
    await previewPair();
    expect(client.preview).toHaveBeenCalledOnce();
  });

  it("blocks preview when the initial identity inventory is incomplete", async () => {
    inventory.credentials.identitiesComplete = false;
    await mount();
    const preview = Array.from(container.querySelectorAll("button")).find((item) => item.textContent?.trim() === "预览变更");
    if (preview) {
      expect(preview.disabled).toBe(true);
      await click(preview);
    }
    expect(client.preview).not.toHaveBeenCalled();
    expect(client.apply).not.toHaveBeenCalled();
    assertNoSecret();
  });

  it("blocks initial inventory failures and never renders backend exception secrets", async () => {
    client.loadInventory.mockRejectedValue(new Error(`cannot load inventory ${secrets[0]}`));
    await mount();
    expect(container.querySelector('[role="alert"]')).toBeTruthy();
    expect(client.preview).not.toHaveBeenCalled();
    expect(client.apply).not.toHaveBeenCalled();
    expect(container.textContent).not.toContain("cannot load inventory");
    assertNoSecret();
  });

  it.each(["incomplete", "failed", "credential_removed", "identity_unverified", "key_removed"])(
    "blocks preview when the refreshed inventory is %s",
    async (failure) => {
      await mount();
      await choosePair();
      if (failure === "incomplete") inventory.credentials.identitiesComplete = false;
      if (failure === "failed") client.loadInventory.mockRejectedValueOnce(new Error(`refresh failed ${secrets[0]}`));
      if (failure === "credential_removed") inventory.credentials.credentials = inventory.credentials.credentials.filter((item) => item.id !== "auth-b");
      if (failure === "identity_unverified") inventory.credentials.credentials.find((item) => item.id === "auth-b")!.identityVerified = false;
      if (failure === "key_removed") {
        inventory.apiKeys = [secrets[0], secrets[1]];
        inventory.catalog.entries = inventory.catalog.entries.filter((item) => item.key_index !== 2);
      }
      await click(button("预览变更"));
      expect(client.preview).not.toHaveBeenCalled();
      expect(client.apply).not.toHaveBeenCalled();
      expect(container.querySelector('[role="alert"]')).toBeTruthy();
      assertNoSecret();
    },
  );

  it.each(["incomplete", "failed", "credential_removed", "identity_unverified", "key_removed"])(
    "blocks apply when the refreshed inventory is %s",
    async (failure) => {
      await previewPair();
      if (failure === "incomplete") inventory.credentials.identitiesComplete = false;
      if (failure === "failed") client.loadInventory.mockRejectedValueOnce(new Error(`refresh failed ${secrets[0]}`));
      if (failure === "credential_removed") inventory.credentials.credentials = inventory.credentials.credentials.filter((item) => item.id !== "auth-b");
      if (failure === "identity_unverified") inventory.credentials.credentials.find((item) => item.id === "auth-b")!.identityVerified = false;
      if (failure === "key_removed") {
        inventory.apiKeys = [secrets[0], secrets[1]];
        inventory.catalog.entries = inventory.catalog.entries.filter((item) => item.key_index !== 2);
      }
      await click(button("确认批量绑定"));
      expect(client.apply).not.toHaveBeenCalled();
      expect(onChanged).not.toHaveBeenCalled();
      expect(container.querySelector('[role="alert"]')).toBeTruthy();
      assertNoSecret();
    },
  );

  it("maps selected keys to their current indices after host key reordering", async () => {
    await mount();
    await click(checkbox("key", "sk-beta...0002"));
    await click(checkbox("key", "sk-gamma...0003"));
    await click(checkbox("credential", "alice@example.test"));
    inventory.apiKeys = [secrets[2], secrets[1], secrets[0]];
    inventory.catalog.entries = inventory.catalog.entries.map((entry) => ({ ...entry, key_index: 2 - entry.key_index }));
    await click(button("预览变更"));
    expect(client.preview).toHaveBeenCalledWith(expect.objectContaining({
      api_keys: [secrets[2], secrets[1], secrets[0]], selected_indices: [2], auth_ids: ["auth-a"],
    }));
  });

  it("does not apply a server preview containing conflicts", async () => {
    client.preview.mockResolvedValue({
      ...makePreview(), can_apply: false,
      conflicts: [{ binding_id: "alpha", code: "binding_changed" }],
    });
    await previewPair();
    const apply = Array.from(container.querySelectorAll("button")).find((item) => item.textContent?.trim() === "确认批量绑定");
    if (apply) {
      expect(apply.disabled).toBe(true);
      await click(apply);
    }
    expect(client.apply).not.toHaveBeenCalled();
    expect(container.textContent).not.toMatch(/强制(?:应用|覆盖|恢复)/);
  });

  it("invalidates an apply revision conflict instead of offering a forced retry", async () => {
    client.apply.mockRejectedValue({ response: { status: 409, data: { error: `revision_conflict ${secrets[0]}` } } });
    await previewPair();
    await click(button("确认批量绑定"));
    expect(client.apply).toHaveBeenCalledTimes(1);
    expect(onChanged).not.toHaveBeenCalled();
    expect(container.querySelector('[role="alert"]')).toBeTruthy();
    const retry = Array.from(container.querySelectorAll("button")).find((item) => item.textContent?.trim() === "确认批量绑定");
    expect(retry === undefined || retry.disabled).toBe(true);
    expect(container.textContent).not.toMatch(/强制(?:应用|覆盖|恢复)/);
    assertNoSecret();
  });

  it("reviews existing group replacement separately from creating an unbound key restriction", async () => {
    await previewPair();
    const changes = container.querySelectorAll(".native-batch-change");
    expect(changes).toHaveLength(2);
    expect(changes[0].textContent).toContain("Alpha");
    expect(changes[0].textContent).toContain("team");
    expect(changes[0].textContent).toContain("alice@example.test");
    expect(changes[0].textContent).toContain("bob@example.test");
    expect(changes[0].textContent).toContain("保持停用");
    expect(changes[0].textContent).not.toContain("新建：");
    const defaults = changes[1].querySelector(".native-batch-new");
    expect(defaults?.textContent).toContain("启用");
    expect(defaults?.textContent).toContain("所有模型");
    expect(defaults?.textContent).toContain("不限额度");
    expect(defaults?.textContent).toContain("轮询关闭");
    expect(client.apply).not.toHaveBeenCalled();
  });

  it("does not submit a no-change preview", async () => {
    client.preview.mockResolvedValue({ ...makePreview(), changes: [], noop: true });
    await previewPair();
    expect(button("确认批量绑定").disabled).toBe(true);
    await click(button("确认批量绑定"));
    expect(client.apply).not.toHaveBeenCalled();
  });

  it("prevents duplicate requests while the server preview is pending", async () => {
    let finishPreview!: (preview: NativeBindingPreview) => void;
    client.preview.mockReturnValue(new Promise((resolve) => { finishPreview = resolve; }));
    await mount();
    await choosePair();
    await click(button("预览变更"));
    expect(button("预览变更").disabled).toBe(true);
    await click(button("预览变更"));
    expect(client.preview).toHaveBeenCalledTimes(1);
    expect(client.apply).not.toHaveBeenCalled();
    await act(async () => { finishPreview(makePreview()); await tick(); });
    expect(button("确认批量绑定").disabled).toBe(false);
  });

  it("loads operation history and separately previews and confirms restoration", async () => {
    await mount("history");
    expect(client.history).toHaveBeenCalledTimes(1);
    expect(client.previewRollback).not.toHaveBeenCalled();
    await click(container.querySelector<HTMLButtonElement>(".native-batch-operation")!);
    expect(container.querySelector(".native-batch-operation-detail")?.textContent).toContain("operation-1");
    await click(button("预览恢复"));
    expect(client.loadInventory).toHaveBeenCalledTimes(2);
    expect(client.previewRollback).toHaveBeenCalledWith({
      operation_id: "operation-1", api_keys: secrets,
      available_auth_ids: ["auth-a", "auth-b", "auth-c", "auth-old"], catalog_complete: true,
    });
    expect(client.rollback).not.toHaveBeenCalled();
    expect(container.querySelectorAll(".native-batch-change")[1].querySelector(".native-batch-new")).toBeTruthy();
    await click(button("确认恢复"));
    expect(client.loadInventory).toHaveBeenCalledTimes(3);
    expect(client.rollback).toHaveBeenCalledWith({
      operation_id: "operation-1", api_keys: secrets,
      available_auth_ids: ["auth-a", "auth-b", "auth-c", "auth-old"], catalog_complete: true,
      expected_revision: "rollback-revision-1",
    });
    expect(client.apply).not.toHaveBeenCalled();
    expect(onChanged).toHaveBeenCalledTimes(1);
    expect(client.history).toHaveBeenCalledTimes(2);
    assertNoSecret();
  });

  it("opens undo at the most recent batch not already restored without immediately mutating", async () => {
    client.history.mockResolvedValue([
      { ...makeOperation(), id: "already-restored", reverted_by: "restore-id" },
      { ...makeOperation(), id: "new-rollback", kind: "rollback", source_operation_id: "already-restored" },
      makeOperation(),
    ]);
    await mount("undo");
    const detail = container.querySelector(".native-batch-operation-detail");
    expect(detail?.textContent).toContain("operation-1");
    expect(detail?.textContent).not.toContain("already-restored");
    expect(client.previewRollback).not.toHaveBeenCalled();
    expect(client.rollback).not.toHaveBeenCalled();
    await click(button("预览恢复"));
    expect(client.previewRollback).toHaveBeenCalledWith(expect.objectContaining({ operation_id: "operation-1" }));
  });

  it.each(["preview", "confirm"])("blocks restoration at %s when identity refresh is incomplete", async (phase) => {
    await mount("undo");
    if (phase === "confirm") await click(button("预览恢复"));
    inventory.credentials.identitiesComplete = false;
    await click(button(phase === "preview" ? "预览恢复" : "确认恢复"));
    if (phase === "preview") expect(client.previewRollback).not.toHaveBeenCalled();
    expect(client.rollback).not.toHaveBeenCalled();
    expect(container.querySelector('[role="alert"]')).toBeTruthy();
  });

  it("cannot force restoration over a server-reported conflict", async () => {
    client.previewRollback.mockResolvedValue({
      ...makePreview(), can_apply: false,
      conflicts: [{ binding_id: "alpha", code: "current_state_changed" }],
    });
    await mount("undo");
    await click(button("预览恢复"));
    expect(button("确认恢复").disabled).toBe(true);
    await click(button("确认恢复"));
    expect(client.rollback).not.toHaveBeenCalled();
    expect(container.textContent).not.toMatch(/强制(?:应用|覆盖|恢复)/);
  });

  it("does not reapply a successful mutation if the later history refresh fails", async () => {
    await previewPair();
    client.history.mockRejectedValue(new Error(`history unavailable ${secrets[0]}`));
    await click(button("确认批量绑定"));
    expect(client.apply).toHaveBeenCalledTimes(1);
    expect(onChanged).toHaveBeenCalledTimes(1);
    expect(container.querySelector('[role="status"]')).toBeTruthy();
    expect(container.querySelector('[role="alert"]')).toBeTruthy();
    expect(container.textContent).not.toContain("history unavailable");
    expect(Array.from(container.querySelectorAll("button")).some((item) => item.textContent?.trim() === "确认批量绑定")).toBe(false);
    assertNoSecret();
  });
});
