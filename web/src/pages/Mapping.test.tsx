import { act } from "react";
import type { ReactElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock("../components/NativeKeyBindings", () => ({ default: () => <section>native bindings</section> }));
vi.mock("../components/PricingTable", () => ({ default: () => <section>model pricing</section> }));

import Mapping from "./Mapping";

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot> | null = null;

beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container.remove();
});

async function render(node: ReactElement) {
  await act(async () => {
    root = createRoot(container);
    root.render(node);
  });
}

describe("mapping sections", () => {
  it("renders native bindings by default without a tab bar", async () => {
    await render(<Mapping />);
    expect(container.textContent).toBe("native bindings");
    expect(container.querySelector(".map-tabs")).toBeNull();
  });

  it("renders model pricing as its own top-level section", async () => {
    await render(<Mapping section="pricing" />);
    expect(container.textContent).toBe("model pricing");
  });
});
