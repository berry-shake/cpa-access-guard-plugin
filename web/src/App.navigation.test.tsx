import { act } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { _resetLocale, translate } from "./i18n";
import { clearSession, setSession } from "./store/session";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock("./pages/Login", () => ({ default: () => <main>login page</main> }));
vi.mock("./pages/KeyList", () => ({ default: () => <main>downstream page</main> }));
vi.mock("./pages/KeyNew", () => ({ default: () => <main>new key page</main> }));
vi.mock("./pages/KeyEdit", () => ({ default: () => <main>edit key page</main> }));
vi.mock("./pages/KeyUsage", () => ({ default: () => <main>key usage page</main> }));
vi.mock("./pages/ModelPick", () => ({ default: () => <main>model picker page</main> }));
vi.mock("./pages/Mapping", () => ({
  default: ({ section }: { section: string }) => <main>{section} page</main>,
  AliasEditForm: () => <main>alias edit page</main>,
  RuleEditForm: () => <main>rule edit page</main>,
}));

import App, { TopNav } from "./App";

let container: HTMLDivElement;
let root: ReturnType<typeof createRoot> | null = null;

beforeEach(() => {
  _resetLocale("zh-CN");
  setSession("http://fixture.invalid", "synthetic-navigation-management-key");
  container = document.createElement("div");
  document.body.appendChild(container);
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container.remove();
  clearSession();
});

async function renderAt(path: string, navOnly = false) {
  await act(async () => {
    root = createRoot(container);
    root.render(
      <MemoryRouter initialEntries={[path]} future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        {navOnly ? <TopNav /> : <App />}
      </MemoryRouter>,
    );
  });
}

describe("shared plugin navigation", () => {
  it("exposes the five routes in one named nav and navigates each with one current-page marker", async () => {
    await renderAt("/native-keys");
    const nav = container.querySelector("nav.topnav-actions")!;
    expect(nav).toBeTruthy();
    expect(nav.getAttribute("aria-label")).toBe(translate("header.navigation"));
    expect(container.querySelectorAll("nav")).toHaveLength(1);
    const routes = [
      ["/native-keys", "native page"], ["/keys", "downstream page"],
      ["/classify", "classify page"], ["/mapping", "alias page"], ["/pricing", "pricing page"],
    ];
    expect(Array.from(nav.querySelectorAll("a")).map((link) => link.getAttribute("href"))).toEqual(routes.map(([path]) => path));
    for (const [path, page] of routes) {
      const link = nav.querySelector<HTMLAnchorElement>(`a[href="${path}"]`)!;
      await act(async () => { link.click(); });
      expect(container.querySelector("main")?.textContent).toBe(page);
      expect(nav.querySelectorAll('[aria-current="page"]')).toHaveLength(1);
      expect(nav.querySelectorAll(".active")).toHaveLength(1);
      expect(link.getAttribute("aria-current")).toBe("page");
      expect(link.classList.contains("active")).toBe(true);
    }
    expect(container.innerHTML).not.toContain("synthetic-navigation-management-key");
  });

  it.each([
    ["/keys/new", "/keys"], ["/keys/new/models", "/keys"],
    ["/keys/client-a/edit", "/keys"], ["/keys/client-a/edit/models", "/keys"],
    ["/keys/client-a/usage", "/keys"], ["/mapping/pick-target", "/mapping"],
    ["/mapping/alias/translation", "/mapping"], ["/classify/rule/team", "/classify"],
  ])("keeps the parent link current on subroute %s", async (path, parent) => {
    await renderAt(path);
    const current = container.querySelectorAll('nav a[aria-current="page"]');
    expect(current).toHaveLength(1);
    expect(current[0].getAttribute("href")).toBe(parent);
    expect(current[0].classList.contains("active")).toBe(true);
    expect(container.querySelector("main")).toBeTruthy();
  });

  it("does not mark a different path with the same leading letters active", async () => {
    await renderAt("/keys-unrelated", true);
    expect(container.querySelectorAll('nav a[aria-current="page"]')).toHaveLength(0);
    expect(container.querySelectorAll("nav .active")).toHaveLength(0);
  });
});
