import axios from "axios";
import type { AxiosAdapter } from "axios";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearSession, getSession, setSession } from "../store/session";
import { createNativeKeyNotesClient, currentNativeKeyNotes, nativeKeyNote } from "./nativeKeyNotes";

const key = "test";
const digest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08";

function serve(status: number, data: unknown) {
  const adapter = vi.fn<AxiosAdapter>(async (config) => {
    const response = { status, data, config, statusText: "", headers: {} };
    if (!config.validateStatus?.(status)) throw { response };
    return response;
  });
  const create = axios.create.bind(axios);
  vi.spyOn(axios, "create").mockImplementation((config) => create({ ...config, adapter }));
  return adapter;
}

beforeEach(() => setSession("http://fixture.invalid:8317", "test-manager-key"));
afterEach(() => {
  clearSession();
  vi.restoreAllMocks();
});

describe("CPA client Key notes", () => {
  it("validates the optional map and joins current notes by the full CPA SHA-256 digest", async () => {
    const request = serve(200, {
      [digest]: "  手机  ",
      ["a".repeat(64)]: true,
      ["b".repeat(64)]: "　",
      ["c".repeat(64)]: "🙂".repeat(121),
      ["D".repeat(64)]: "Uppercase hash",
      garbage: "Malformed fingerprint",
    });
    const notes = await createNativeKeyNotesClient().read();
    expect(notes).toEqual(new Map([[digest, "手机"]]));
    expect(nativeKeyNote(currentNativeKeyNotes(notes, [key]), " test ")).toBe("手机");
    expect(currentNativeKeyNotes(notes, ["rotated"])).toHaveProperty("size", 0);
    expect(nativeKeyNote(currentNativeKeyNotes(notes, ["手", key]), key)).toBe("手机");
    const config = request.mock.calls[0][0];
    expect(config.baseURL).toBe("http://fixture.invalid:8317");
    expect(config.url).toBe("/v8/management/config/access/api-key-notes");
    expect(config.timeout).toBeGreaterThan(0);
  });

  it.each([401, 403, 404, 500])("keeps the authenticated session for optional response %s", async (status) => {
    serve(status, { [digest]: "Ignored" });
    const session = getSession();
    expect(await createNativeKeyNotesClient().read()).toHaveProperty("size", 0);
    expect(getSession()).toBe(session);
  });

  it("does not send a write from an obsolete CPA session", async () => {
    const request = serve(200, { updated_ids: ["client-a"], skipped_ids: [] });
    const client = createNativeKeyNotesClient();
    setSession("http://different.fixture.invalid", "another-test-manager");
    await expect(client.sync([{ id: "client-a", key, expected_name: "", name: "Desk" }])).rejects.toThrow(
      "native_key_notes_session_changed",
    );
    expect(request).not.toHaveBeenCalled();
  });

  it("uses the same authenticated host for a bounded name-only batch", async () => {
    const request = serve(200, { updated_ids: ["client-a"], skipped_ids: [] });
    const updates = [{ id: "client-a", key, expected_name: "", name: "Desk" }];
    await expect(createNativeKeyNotesClient().sync(updates)).resolves.toEqual({ skipped_ids: [] });
    const config = request.mock.calls[0][0];
    expect(config.baseURL).toBe("http://fixture.invalid:8317");
    expect(config.url).toBe("/v0/management/plugins/access-guard/native-key-bindings/sync-names");
    expect(JSON.parse(config.data)).toEqual({ updates });
  });
});
