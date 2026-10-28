import { afterEach, describe, expect, test } from "bun:test";

// The backend refuses a reboot request without an explicit action (a bare POST
// must never reboot the stick). rebootNow is used by the top-bar Restart and the
// band-unlock card, so it has to send one.
describe("rebootNow", () => {
  const g = globalThis as Record<string, unknown>;
  const saved = { window: g.window, fetch: g.fetch };
  afterEach(() => {
    g.window = saved.window;
    g.fetch = saved.fetch;
  });

  test("posts an explicit reboot action", async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    g.window = { setTimeout: () => 0, sessionStorage: { setItem() {}, getItem: () => null } };
    g.fetch = (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      return Promise.resolve(new Response("{}"));
    };
    const { rebootNow } = await import("@/lib/reboot");
    rebootNow("manual");
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("/cgi-bin/quecmanager/system/reboot.sh");
    expect(calls[0].init?.method).toBe("POST");
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({ action: "reboot" });
  });
});
