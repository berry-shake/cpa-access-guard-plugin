import { sha256Hex } from "../sha256";
import { getSession } from "../store/session";
import { apiClient, pluginPath } from "./client";

interface NativeKeyNameUpdate {
  id: string;
  key: string;
  expected_name: string;
  name: string;
}

export function createNativeKeyNotesClient() {
  const session = getSession();
  const client = apiClient();

  return {
    async read(): Promise<ReadonlyMap<string, string>> {
      try {
        const { status, data } = await client.get<unknown>("/v8/management/config/access/api-key-notes", {
          timeout: 2500,
          // This host feature is optional. A proxy can deny only this route.
          validateStatus: () => true,
        });
        if (getSession() === session && status === 200) return parseNativeKeyNotes(data);
      } catch {
        // The normal native-key inventory remains usable if notes are unavailable.
      }
      return new Map();
    },

    async sync(updates: NativeKeyNameUpdate[]): Promise<{ skipped_ids: string[] }> {
      if (getSession() !== session) throw new Error("native_key_notes_session_changed");
      const { status, data } = await client.post(
        pluginPath("/native-key-bindings/sync-names"),
        { updates },
        { timeout: 10000, validateStatus: () => true },
      );
      if (getSession() !== session || status !== 200 ||
          !Array.isArray(data?.updated_ids) || !Array.isArray(data?.skipped_ids)) {
        throw new Error("native_key_notes_sync_failed");
      }
      return { skipped_ids: data.skipped_ids };
    },
  };
}

function parseNativeKeyNotes(value: unknown): ReadonlyMap<string, string> {
  const notes = new Map<string, string>();
  if (!value || typeof value !== "object" || Array.isArray(value)) return notes;
  for (const [digest, valueEntry] of Object.entries(value)) {
    if (!/^[0-9a-f]{64}$/.test(digest) || typeof valueEntry !== "string") continue;
    const note = valueEntry.trim();
    if (note && Array.from(note).length <= 120) notes.set(digest, note);
  }
  return notes;
}

// CPA hashes the complete key without a caller-scope domain or a traffic salt.
// Keep only notes for current keys so stale entries cannot label a rotation.
export function currentNativeKeyNotes(notes: ReadonlyMap<string, string>, keys: string[]): ReadonlyMap<string, string> {
  const current = new Map<string, string>();
  if (!notes.size) return current;
  for (const key of keys) {
    const digest = sha256Hex(key.trim());
    const note = notes.get(digest);
    if (note) current.set(digest, note);
  }
  return current;
}

export function nativeKeyNote(notes: ReadonlyMap<string, string>, apiKey?: string): string | undefined {
  if (apiKey && notes.size) return notes.get(sha256Hex(apiKey.trim()));
  return undefined;
}
