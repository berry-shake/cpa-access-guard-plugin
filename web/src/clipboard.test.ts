import { afterEach, describe, expect, it, vi } from "vitest";
import { copyTextToClipboard } from "./clipboard";

const secret = "sk-synthetic-clipboard-full-value";
const originalClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");
const originalExecCommand = Object.getOwnPropertyDescriptor(document, "execCommand");

function clipboard(value: unknown) {
  Object.defineProperty(navigator, "clipboard", { configurable: true, value });
}

function command(value: unknown) {
  Object.defineProperty(document, "execCommand", { configurable: true, value });
}

afterEach(() => {
  if (originalClipboard) Object.defineProperty(navigator, "clipboard", originalClipboard);
  else Reflect.deleteProperty(navigator, "clipboard");
  if (originalExecCommand) Object.defineProperty(document, "execCommand", originalExecCommand);
  else Reflect.deleteProperty(document, "execCommand");
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

describe("copyTextToClipboard", () => {
  it("copies the complete value through the Clipboard API without adding it to the page", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    clipboard({ writeText });
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    await copyTextToClipboard(secret);
    expect(writeText).toHaveBeenCalledWith(secret);
    expect(document.body.innerHTML).not.toContain(secret);
    expect(setItem).not.toHaveBeenCalled();
  });

  it("supports an HTTP-origin copy command and removes its transient input", async () => {
    clipboard(undefined);
    const button = document.createElement("button");
    document.body.appendChild(button);
    button.focus();
    let transient: HTMLTextAreaElement | undefined;
    const exec = vi.fn(() => {
      transient = document.activeElement as HTMLTextAreaElement;
      expect(transient.value).toBe(secret);
      expect(transient.selectionStart).toBe(0);
      expect(transient.selectionEnd).toBe(secret.length);
      return true;
    });
    command(exec);
    await copyTextToClipboard(secret);
    expect(exec).toHaveBeenCalledWith("copy");
    expect(transient?.value).toBe("");
    expect(transient?.isConnected).toBe(false);
    expect(document.activeElement).toBe(button);
    expect(document.body.innerHTML).not.toContain(secret);
  });

  it("falls back when the Clipboard API rejects permission", async () => {
    clipboard({ writeText: vi.fn().mockRejectedValue(new Error("permission denied")) });
    const exec = vi.fn(() => true);
    command(exec);
    await expect(copyTextToClipboard(secret)).resolves.toBeUndefined();
    expect(exec).toHaveBeenCalledOnce();
    expect(document.querySelector("textarea")).toBeNull();
  });

  it.each([false, "throw"])("cleans up and rejects when fallback fails: %s", async (outcome) => {
    clipboard(undefined);
    let transient: HTMLTextAreaElement | undefined;
    command(() => {
      transient = document.activeElement as HTMLTextAreaElement;
      if (outcome === "throw") throw new Error("copy denied");
      return false;
    });
    await expect(copyTextToClipboard(secret)).rejects.toThrow();
    expect(transient?.value).toBe("");
    expect(transient?.isConnected).toBe(false);
    expect(document.body.innerHTML).not.toContain(secret);
  });
});
