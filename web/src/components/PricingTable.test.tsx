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
  restoreAutomaticModelPricing: vi.fn(),
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
  apiMocks.fetchModelPricing.mockResolvedValue({
    models: [
      {
        modelId: "gpt-5.5",
        displayName: "GPT-5.5",
        inputCostPerMillion: "5",
        outputCostPerMillion: "30",
        cacheReadCostPerMillion: "0.5",
        cacheCreationCostPerMillion: "0",
        provider: "openai",
        source: "litellm",
        status: "priced",
      },
    ],
    deletedModelIds: [],
  });
  apiMocks.fetchPricingSyncStatus.mockResolvedValue({
    enabled: true,
    catalog_size: 1,
    litellm_url: "https://cdn.example/litellm.json",
    models_dev_url: "https://models.example/api.json",
    sources: {
      litellm: { accepted: 1 },
      modelsDev: { accepted: 2 },
    },
  });
  apiMocks.restoreAutomaticModelPricing.mockResolvedValue(undefined);
  apiMocks.runPricingSync.mockResolvedValue({});
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
    expect(container.textContent).toContain("LiteLLM");
    expect(container.textContent).toContain("models.dev");
    expect(container.textContent).toContain("openai");
  });

  it("renders known-but-unpriced and deleted rows safely", async () => {
    apiMocks.fetchModelPricing.mockResolvedValue({
      models: [{
        modelId: "gpt-5.3-codex-spark",
        displayName: "GPT-5.3 Codex Spark",
        inputCostPerMillion: "0",
        outputCostPerMillion: "0",
        cacheReadCostPerMillion: "0",
        cacheCreationCostPerMillion: "0",
        source: "litellm",
        status: "known_unpriced",
      }],
      deletedModelIds: ["gpt-5.4-mini"],
    });
    await act(async () => {
      root = createRoot(container);
      root.render(<PricingTable />);
      await tick();
      await tick();
    });
    expect(container.textContent).toContain("已知模型 · 无公开价格");
    expect(container.textContent).toContain("已隐藏的自动价格");
    expect(container.textContent).toContain("gpt-5.4-mini");
    expect(container.textContent).not.toContain("$0");
  });
});
