import { act } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { _resetLocale } from "../i18n";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const apiMocks = vi.hoisted(() => ({
  fetchAliases: vi.fn(),
  upsertAlias: vi.fn(),
  deleteAlias: vi.fn(),
  fetchClassifyRules: vi.fn(),
  upsertClassifyRule: vi.fn(),
  deleteClassifyRule: vi.fn(),
  reorderClassifyRules: vi.fn(),
  classifyPreview: vi.fn(),
  fetchCredentialDescriptors: vi.fn(),
  canPreviewClassifyField: vi.fn((field: string) =>
    ["filename", "id", "provider", "plan_type", "tier", "path", "weight"]
      .includes(field.trim().toLowerCase()),
  ),
}));

vi.mock("../api/mappings", () => apiMocks);
vi.mock("../components/NativeKeyBindings", () => ({ default: () => null }));
vi.mock("../components/PricingTable", () => ({ default: () => null }));

import Mapping from "./Mapping";

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot> | null = null;
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

async function openClassifyTab() {
  await act(async () => {
    root = createRoot(container);
    root.render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <Mapping />
      </MemoryRouter>,
    );
    await tick();
  });

  const classifyButton = Array.from(container.querySelectorAll("button"))
    .find((button) => button.textContent?.includes("凭证归类"));
  expect(classifyButton).toBeTruthy();
  await act(async () => {
    classifyButton!.click();
    await tick();
    await tick();
  });
}

beforeEach(() => {
  _resetLocale("zh-CN");
  container = document.createElement("div");
  document.body.appendChild(container);
  apiMocks.fetchAliases.mockResolvedValue([]);
  apiMocks.fetchClassifyRules.mockResolvedValue([
    { name: "tenant-a", field: "filename", pattern: "^a\\.json$", group: "tenant-a", enabled: true },
  ]);
  apiMocks.fetchCredentialDescriptors.mockResolvedValue([]);
  apiMocks.classifyPreview.mockResolvedValue({ groups: {}, group_counts: {}, rule_matches: {} });
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container.remove();
  vi.clearAllMocks();
});

describe("classification preview failures", () => {
  it("does not turn an auth-files request failure into a zero-match claim", async () => {
    apiMocks.fetchCredentialDescriptors.mockRejectedValueOnce(new Error("auth-files unavailable"));

    await openClassifyTab();

    expect(container.textContent).toContain("凭证命中预览加载失败");
    expect(container.textContent).toContain("无法预览");
    expect(container.textContent).not.toContain("匹配 0 个凭证");
    expect(apiMocks.classifyPreview).not.toHaveBeenCalled();
  });

  it("does not turn a classify-preview failure into a zero-match claim", async () => {
    apiMocks.classifyPreview.mockRejectedValueOnce(new Error("preview unavailable"));

    await openClassifyTab();

    expect(container.textContent).toContain("凭证命中预览加载失败");
    expect(container.textContent).toContain("无法预览");
    expect(container.textContent).not.toContain("匹配 0 个凭证");
    expect(apiMocks.classifyPreview).toHaveBeenCalledWith([]);
  });

  it("treats a missing per-rule result as unavailable instead of zero", async () => {
    apiMocks.classifyPreview.mockResolvedValueOnce({
      groups: {},
      group_counts: {},
      rule_matches: { "some-other-rule": [] },
    });

    await openClassifyTab();

    expect(container.textContent).toContain("无法预览");
    expect(container.textContent).not.toContain("匹配 0 个凭证");
  });
});
