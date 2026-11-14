import { act } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { _resetLocale } from "../i18n";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const sessionMocks = vi.hoisted(() => ({
  setSession: vi.fn(),
  verifySession: vi.fn(),
}));
vi.mock("../store/session", () => sessionMocks);

import Login from "./Login";

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot> | null = null;

beforeEach(() => {
  _resetLocale("zh-CN");
  container = document.createElement("div");
  document.body.appendChild(container);
  sessionMocks.verifySession.mockResolvedValue(undefined);
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container.remove();
  vi.clearAllMocks();
});

describe("Login", () => {
  it("opens the native key page after a successful login", async () => {
    await act(async () => {
      root = createRoot(container);
      root.render(
        <MemoryRouter initialEntries={["/login"]} future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
          <Routes>
            <Route path="/login" element={<Login />} />
            <Route path="/native-keys" element={<main>native keys page</main>} />
          </Routes>
        </MemoryRouter>,
      );
    });

    const secret = container.querySelector<HTMLInputElement>('input[type="password"]')!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(secret, "synthetic-management-key");
      secret.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => {
      container.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });

    expect(sessionMocks.setSession).toHaveBeenCalledWith(expect.any(String), "synthetic-management-key");
    expect(container.querySelector("main")?.textContent).toBe("native keys page");
  });
});
