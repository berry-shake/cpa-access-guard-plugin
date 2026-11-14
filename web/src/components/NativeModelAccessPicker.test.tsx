import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { _resetLocale } from "../i18n";
import type { CatalogModel, NativeModelAccessPolicy } from "../types";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

import NativeModelAccessPicker, { buildNativeModelGroups } from "./NativeModelAccessPicker";

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot> | null = null;
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

const credentialModels: CatalogModel[] = [
  { provider: "codex", model: "gpt-5.6-luna" },
  { provider: "codex", model: "GPT-5.6-LUNA" },
  { provider: "codex", model: "gpt-5.5" },
  { provider: "gemini", model: "gemini-2.5-pro" },
];
const refresh = vi.fn();

function Harness({
  initial,
  catalog = credentialModels,
  loading,
}: {
  initial: NativeModelAccessPolicy;
  catalog?: CatalogModel[];
  loading?: boolean;
}) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <NativeModelAccessPicker value={value} catalog={catalog} loading={loading} onRefresh={refresh} onChange={setValue} />
      <output data-testid="model-policy">{JSON.stringify(value)}</output>
    </>
  );
}

beforeEach(() => {
  _resetLocale("zh-CN");
  container = document.createElement("div");
  document.body.appendChild(container);
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container.remove();
  vi.clearAllMocks();
});

describe("buildNativeModelGroups", () => {
  it("collapses rows reported by several credentials into exact provider/model rows", () => {
    expect(buildNativeModelGroups([
      { provider: "codex", model: "gpt-5.6-luna" },
      { provider: "CODEX", model: "GPT-5.6-LUNA" },
      { provider: "gemini", model: "gemini-2.5-pro" },
    ])).toEqual([
      { provider: "codex", models: ["gpt-5.6-luna"] },
      { provider: "gemini", models: ["gemini-2.5-pro"] },
    ]);
  });
});

describe("NativeModelAccessPicker", () => {
  it("lists only the models of the selected credentials", async () => {
    await act(async () => {
      root = createRoot(container);
      root.render(<Harness
        initial={{ mode: "allowlist", models: [{ provider: "codex", model: "gpt-5.6-luna" }] }}
        catalog={[
          { provider: "codex", model: "gpt-5.6-luna" },
          { provider: "codex", model: "gpt-5.5-unselected" },
        ]}
      />);
      await tick();
    });

    expect(container.textContent).toContain("gpt-5.6-luna");
    expect(container.textContent).toContain("gpt-5.5-unselected");
    expect(container.textContent).not.toContain("gemini-2.5-pro");
  });

  it("selects the complete visible catalog and can uncheck one model", async () => {
    await act(async () => {
      root = createRoot(container);
      root.render(<Harness initial={{ mode: "allowlist", models: [] }} />);
      await tick();
    });

    expect(container.textContent).toContain("当前未勾选任何模型");
    const selectAll = Array.from(container.querySelectorAll("button"))
      .find((button) => button.textContent === "全选当前结果");
    await act(async () => { selectAll!.click(); });

    const policy = () => JSON.parse(
      container.querySelector('[data-testid="model-policy"]')?.textContent ?? "{}",
    ) as NativeModelAccessPolicy;
    expect(policy()).toEqual({
      mode: "allowlist",
      models: [
        { provider: "codex", model: "gpt-5.5" },
        { provider: "codex", model: "gpt-5.6-luna" },
        { provider: "gemini", model: "gemini-2.5-pro" },
      ],
    });

    const luna = Array.from(container.querySelectorAll("label"))
      .find((label) => label.textContent?.trim() === "gpt-5.6-luna");
    await act(async () => {
      (luna?.querySelector("input") as HTMLInputElement).click();
    });
    expect(policy().models).not.toContainEqual({ provider: "codex", model: "gpt-5.6-luna" });
  });

  it("keeps a selected model visible when it disappears from the current catalog", async () => {
    await act(async () => {
      root = createRoot(container);
      root.render(<Harness initial={{
        mode: "allowlist",
        models: [{ provider: "codex", model: "retired-model" }],
      }} />);
      await tick();
    });

    expect(container.textContent).toContain("已保存但当前目录不存在");
    expect(container.textContent).toContain("retired-model");
    const stale = Array.from(container.querySelectorAll("label"))
      .find((label) => label.textContent?.includes("retired-model"));
    expect((stale?.querySelector("input") as HTMLInputElement).checked).toBe(true);
  });

  it("switches to unrestricted mode explicitly", async () => {
    await act(async () => {
      root = createRoot(container);
      root.render(<Harness initial={{
        mode: "allowlist",
        models: [{ provider: "codex", model: "gpt-5.6-luna" }],
      }} />);
      await tick();
    });
    const allMode = container.querySelector('input[name="native-model-mode"]') as HTMLInputElement;
    await act(async () => { allMode.click(); });
    const value = JSON.parse(container.querySelector("output")?.textContent ?? "{}") as NativeModelAccessPolicy;
    expect(value).toEqual({ mode: "all", models: [] });
    expect(container.querySelector("#native-model-search")).toBeNull();
  });

  it("asks the parent to reload credential models and waits while they load", async () => {
    await act(async () => {
      root = createRoot(container);
      root.render(<Harness initial={{ mode: "allowlist", models: [] }} catalog={[]} loading />);
      await tick();
    });
    const button = () => Array.from(container.querySelectorAll("button"))
      .find((item) => item.textContent === "刷新模型") as HTMLButtonElement;
    expect(container.textContent).toContain("正在加载模型");
    expect(button().disabled).toBe(true);

    await act(async () => {
      root?.render(<Harness initial={{ mode: "allowlist", models: [] }} />);
      await tick();
    });
    await act(async () => { button().click(); });
    expect(refresh).toHaveBeenCalledTimes(1);
  });
});
