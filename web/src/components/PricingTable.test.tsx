import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { _resetLocale } from "../i18n";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const apiMocks = vi.hoisted(() => ({
  fetchModelPricing: vi.fn(),
  fetchPricingSyncStatus: vi.fn(),
  runPricingSync: vi.fn(),
  upsertModelPricing: vi.fn(),
  deleteModelPricing: vi.fn(),
}));

vi.mock("../api/mappings", () => apiMocks);

import PricingTable from "./PricingTable";

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot> | null = null;
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

beforeEach(() => {
  _resetLocale("zh-CN");
  container = document.createElement("div");
  document.body.appendChild(container);
  apiMocks.fetchModelPricing.mockResolvedValue([
    {
      modelId: "gpt-5.5",
      displayName: "GPT-5.5",
      inputCostPerMillion: "5",
      outputCostPerMillion: "30",
      cacheReadCostPerMillion: "0.5",
      cacheCreationCostPerMillion: "0",
    },
  ]);
  apiMocks.fetchPricingSyncStatus.mockResolvedValue({ enabled: false, catalog_size: 1 });
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container.remove();
  vi.clearAllMocks();
});

describe("PricingTable", () => {
  it("renders catalog rows like cc-switch", async () => {
    await act(async () => {
      root = createRoot(container);
      root.render(<PricingTable />);
      await tick();
      await tick();
    });
    expect(container.textContent).toContain("gpt-5.5");
    expect(container.textContent).toContain("GPT-5.5");
    expect(container.textContent).toContain("$5");
    expect(container.textContent).toContain("$30");
    expect(container.textContent).toContain("缓存读取");
    expect(container.textContent).toContain("缓存写入");
  });
});
